package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/d11nn/woms/internal/domain"
	womslock "github.com/d11nn/woms/internal/lock"
	"github.com/d11nn/woms/internal/scheduler"
	"github.com/d11nn/woms/internal/startup"
	_ "github.com/lib/pq"
	"github.com/segmentio/kafka-go"
)

func main() {
	config, err := loadWorkerConfig(os.LookupEnv)
	if err != nil {
		log.Fatalf("invalid scheduler worker configuration: %v", err)
	}
	brokerList := startup.SplitCSV(config.brokers)
	var db *sql.DB
	var lockProvider womslock.Provider
	if config.databaseURL != "" {
		if err := validateLockConfig(config.lockTTL, config.lockRenewInterval, config.lockTimeout); err != nil {
			log.Fatalf("invalid Redis lock configuration: %v", err)
		}
		if config.backfillInterval <= 0 {
			log.Fatal("WORKER_BACKFILL_INTERVAL_MS must be greater than zero when DATABASE_URL is set")
		}
		if config.redisAddr == "" {
			log.Fatal("REDIS_ADDR is required when DATABASE_URL is set; scheduler-worker refuses to run without Redis line locks")
		}
		var err error
		redisLocks := womslock.NewRedisProvider(config.redisAddr)
		ctx, cancel := context.WithTimeout(context.Background(), config.dependencyTimeout)
		err = startup.RetryDependency(ctx, "redis line lock", config.dependencyInterval, log.Printf, func(ctx context.Context) error {
			return redisLocks.Ping(ctx)
		})
		cancel()
		if err != nil {
			log.Fatalf("redis line lock failed: %v", err)
		}
		lockProvider = redisLocks
		db, err = sql.Open("postgres", config.databaseURL)
		if err != nil {
			log.Fatalf("postgres open failed: %v", err)
		}
		ctx, cancel = context.WithTimeout(context.Background(), config.dependencyTimeout)
		err = startup.RetryDependency(ctx, "postgres", config.dependencyInterval, log.Printf, func(ctx context.Context) error {
			return db.PingContext(ctx)
		})
		cancel()
		if err != nil {
			log.Fatalf("postgres ping failed: %v", err)
		}
		defer db.Close()
		if err := backfillQueuedJobs(context.Background(), db, lockProvider, config.maxRetries, config.lockTTL, config.lockRenewInterval, config.lockTimeout); err != nil {
			log.Printf("scheduler backfill failed: %v", err)
		}
	}

	log.Printf("scheduler worker starting brokers=%s topic=%s group=%s minJobDuration=%s", config.brokers, config.topic, config.group, config.minJobDuration)
	ctx, cancel := context.WithTimeout(context.Background(), config.dependencyTimeout)
	if err := startup.RetryDependency(ctx, "kafka broker", config.dependencyInterval, log.Printf, func(ctx context.Context) error {
		return startup.PingAnyTCP(ctx, brokerList)
	}); err != nil {
		cancel()
		log.Fatalf("kafka broker failed: %v", err)
	}
	cancel()
	reader := kafka.NewReader(kafka.ReaderConfig{
		Brokers: brokerList,
		Topic:   config.topic,
		GroupID: config.group,
		// Ensure the consumer picks up topics/partitions created after startup.
		WatchPartitionChanges:  true,
		PartitionWatchInterval: 5 * time.Second,
		StartOffset:            config.startOffset,
	})
	defer reader.Close()
	if db != nil && config.backfillInterval > 0 {
		go func() {
			ticker := time.NewTicker(config.backfillInterval)
			defer ticker.Stop()
			for range ticker.C {
				if err := backfillQueuedJobs(context.Background(), db, lockProvider, config.maxRetries, config.lockTTL, config.lockRenewInterval, config.lockTimeout); err != nil {
					log.Printf("scheduler backfill failed: %v", err)
				}
			}
		}()
	}

	for {
		message, err := reader.FetchMessage(context.Background())
		if err != nil {
			log.Printf("scheduler worker read failed: %v", err)
			time.Sleep(5 * time.Second)
			continue
		}
		started := time.Now()
		log.Printf("scheduler job received topic=%s partition=%d offset=%d key=%s bytes=%d", message.Topic, message.Partition, message.Offset, string(message.Key), len(message.Value))
		if config.minJobDuration > 0 {
			time.Sleep(config.minJobDuration)
		}
		if db != nil {
			if err := processDBJob(context.Background(), db, lockProvider, message.Value, workerJobConfigFromConfig(config)); err != nil {
				log.Printf("scheduler job db execution failed key=%s error=%v", string(message.Key), err)
				time.Sleep(2 * time.Second)
				continue
			}
		}
		if err := reader.CommitMessages(context.Background(), message); err != nil {
			log.Printf("scheduler job commit failed key=%s error=%v", string(message.Key), err)
			continue
		}
		log.Printf("scheduler job acknowledged key=%s elapsed=%s", string(message.Key), time.Since(started).Round(time.Millisecond))
	}
}

type workerConfig struct {
	brokers            string
	topic              string
	group              string
	databaseURL        string
	redisAddr          string
	minJobDuration     time.Duration
	maxRetries         int
	lockTTL            time.Duration
	lockRenewInterval  time.Duration
	lockTimeout        time.Duration
	backfillInterval   time.Duration
	dependencyTimeout  time.Duration
	dependencyInterval time.Duration
	startOffset        int64
}

func loadWorkerConfig(lookup func(string) (string, bool)) (workerConfig, error) {
	var config workerConfig
	var err error

	config.brokers = configString(lookup, "KAFKA_BROKERS", "kafka:9092")
	config.topic = configString(lookup, "KAFKA_SCHEDULE_TOPIC", "woms.schedule.jobs")
	config.group = configString(lookup, "KAFKA_CONSUMER_GROUP", "woms-scheduler-workers")
	config.databaseURL = configString(lookup, "DATABASE_URL", "")
	config.redisAddr = configString(lookup, "REDIS_ADDR", "")
	if config.minJobDuration, err = configDuration(lookup, "WORKER_MIN_JOB_DURATION_MS", 0); err != nil {
		return workerConfig{}, err
	}
	if config.maxRetries, err = configInt(lookup, "WORKER_MAX_RETRIES", 3); err != nil {
		return workerConfig{}, err
	}
	if config.lockTTL, err = configDuration(lookup, "WORKER_LOCK_TTL_MS", 15*time.Second); err != nil {
		return workerConfig{}, err
	}
	if config.lockRenewInterval, err = configDuration(lookup, "WORKER_LOCK_RENEW_INTERVAL_MS", 5*time.Second); err != nil {
		return workerConfig{}, err
	}
	if config.lockTimeout, err = configDuration(lookup, "WORKER_LOCK_TIMEOUT_MS", 10*time.Second); err != nil {
		return workerConfig{}, err
	}
	if config.backfillInterval, err = configDuration(lookup, "WORKER_BACKFILL_INTERVAL_MS", 5*time.Second); err != nil {
		return workerConfig{}, err
	}
	if config.dependencyTimeout, err = configDuration(lookup, "WORKER_DEPENDENCY_RETRY_TIMEOUT_MS", 2*time.Minute); err != nil {
		return workerConfig{}, err
	}
	if config.dependencyInterval, err = configDuration(lookup, "WORKER_DEPENDENCY_RETRY_INTERVAL_MS", 2*time.Second); err != nil {
		return workerConfig{}, err
	}
	config.startOffset, err = configStartOffset(configString(lookup, "WORKER_START_OFFSET", "latest"))
	if err != nil {
		return workerConfig{}, err
	}
	return config, nil
}

type workerJobConfig struct {
	maxRetries        int
	lockTTL           time.Duration
	lockRenewInterval time.Duration
	lockTimeout       time.Duration
}

func workerJobConfigFromConfig(config workerConfig) workerJobConfig {
	return workerJobConfig{
		maxRetries:        config.maxRetries,
		lockTTL:           config.lockTTL,
		lockRenewInterval: config.lockRenewInterval,
		lockTimeout:       config.lockTimeout,
	}
}

func processDBJob(ctx context.Context, db *sql.DB, lockProvider womslock.Provider, payload []byte, config workerJobConfig) error {
	return processJobPayload(ctx, sqlScheduleJobExecutor{db: db}, lockProvider, payload, config)
}

type scheduleJobExecutor interface {
	markJobFailed(ctx context.Context, jobID, message string) error
	markJobRetry(ctx context.Context, jobID, message string) error
	processJobLocked(ctx context.Context, job domain.ScheduleJob, maxRetries int) error
}

type sqlScheduleJobExecutor struct {
	db *sql.DB
}

func (e sqlScheduleJobExecutor) markJobFailed(ctx context.Context, jobID, message string) error {
	return markJobFailed(ctx, e.db, jobID, message)
}

func (e sqlScheduleJobExecutor) markJobRetry(ctx context.Context, jobID, message string) error {
	return markJobRetry(ctx, e.db, jobID, message)
}

func (e sqlScheduleJobExecutor) processJobLocked(ctx context.Context, job domain.ScheduleJob, maxRetries int) error {
	return processDBJobLocked(ctx, e.db, job, maxRetries)
}

func processJobPayload(ctx context.Context, executor scheduleJobExecutor, lockProvider womslock.Provider, payload []byte, config workerJobConfig) error {
	var job domain.ScheduleJob
	if err := json.Unmarshal(payload, &job); err != nil {
		return err
	}
	if job.ID == "" || job.LineID == "" {
		return nil
	}
	if lockProvider == nil {
		if err := executor.markJobFailed(ctx, job.ID, "Redis 排程鎖未設定。"); err != nil {
			return err
		}
		return nil
	}
	lockCtx, cancel := context.WithTimeout(ctx, config.lockTimeout)
	defer cancel()
	lineLock, err := acquireLineLock(lockCtx, lockProvider, scheduleLineLockKey(job.LineID), config.lockTTL)
	if err != nil {
		message := "Redis 排程鎖取得失敗，等待重試：" + err.Error()
		if lockCtx.Err() != nil || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			message = "同產線排程鎖取得逾時，等待重試。"
		}
		if retryErr := executor.markJobRetry(ctx, job.ID, message); retryErr != nil {
			return retryErr
		}
		return nil
	}
	defer func() {
		if err := lineLock.Release(context.Background()); err != nil {
			log.Printf("failed to release line lock for job %s on line %s: %v", job.ID, job.LineID, err)
		}
	}()
	runCtx, stopRenewal := startLockRenewal(ctx, lineLock, config.lockTTL, config.lockRenewInterval)
	defer stopRenewal()
	return executor.processJobLocked(runCtx, job, config.maxRetries)
}

func validateLockConfig(lockTTL, lockRenewInterval, lockTimeout time.Duration) error {
	if lockTTL <= 0 {
		return errors.New("WORKER_LOCK_TTL_MS must be greater than zero")
	}
	if lockTimeout <= 0 {
		return errors.New("WORKER_LOCK_TIMEOUT_MS must be greater than zero")
	}
	if lockRenewInterval <= 0 || lockRenewInterval >= lockTTL {
		return errors.New("WORKER_LOCK_RENEW_INTERVAL_MS must be greater than zero and less than WORKER_LOCK_TTL_MS")
	}
	return nil
}

func acquireLineLock(ctx context.Context, provider womslock.Provider, key string, ttl time.Duration) (womslock.Lock, error) {
	retry := 200 * time.Millisecond
	for {
		lineLock, err := provider.Acquire(ctx, key, ttl)
		if err == nil {
			return lineLock, nil
		}
		if !errors.Is(err, womslock.ErrNotAcquired) {
			return nil, err
		}
		timer := time.NewTimer(retry)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func processDBJobLocked(ctx context.Context, db *sql.DB, job domain.ScheduleJob, maxRetries int) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	runErr, shouldCommit := runLockedJobState(ctx, sqlLockedJobStore{tx: tx}, job, maxRetries)
	if shouldCommit {
		if commitErr := tx.Commit(); commitErr != nil {
			return commitErr
		}
	}
	return runErr
}

type lockedJobStore interface {
	jobStatus(ctx context.Context, id string) (domain.ScheduleJobStatus, bool, error)
	markRunning(ctx context.Context, id string) (int, error)
	persist(ctx context.Context, job domain.ScheduleJob) error
	markRetryAfterRun(ctx context.Context, id, message string) error
	markFailedAfterRun(ctx context.Context, id, message, reason string) error
	markCompleted(ctx context.Context, job domain.ScheduleJob) error
}

type sqlLockedJobStore struct {
	tx *sql.Tx
}

func (s sqlLockedJobStore) jobStatus(ctx context.Context, id string) (domain.ScheduleJobStatus, bool, error) {
	var status domain.ScheduleJobStatus
	if err := s.tx.QueryRowContext(ctx, "SELECT status FROM schedule_jobs WHERE id = $1 FOR UPDATE", id).Scan(&status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", false, nil
		}
		return "", false, err
	}
	return status, true, nil
}

func (s sqlLockedJobStore) markRunning(ctx context.Context, id string) (int, error) {
	var attempt int
	err := s.tx.QueryRowContext(ctx, `
		UPDATE schedule_jobs
		SET status = 'running',
		    message = '排程任務執行中。',
		    started_at = COALESCE(started_at, NOW()),
		    attempt_count = attempt_count + 1,
		    updated_at = NOW()
		WHERE id = $1
		RETURNING attempt_count
	`, id).Scan(&attempt)
	return attempt, err
}

func (s sqlLockedJobStore) persist(ctx context.Context, job domain.ScheduleJob) error {
	if job.Source == "hpa-peak-demo" || job.PreviewID == "" {
		return persistLineSchedule(ctx, s.tx, job)
	}
	return persistPreviewAllocations(ctx, s.tx, job)
}

func (s sqlLockedJobStore) markRetryAfterRun(ctx context.Context, id, message string) error {
	_, err := s.tx.ExecContext(ctx, `
		UPDATE schedule_jobs
		SET status = 'queued', message = $2, updated_at = NOW()
		WHERE id = $1
	`, id, message)
	return err
}

func (s sqlLockedJobStore) markFailedAfterRun(ctx context.Context, id, message, reason string) error {
	_, err := s.tx.ExecContext(ctx, `
		UPDATE schedule_jobs
		SET status = 'failed', message = $2, completed_at = NOW(), updated_at = NOW()
		WHERE id = $1
	`, id, message)
	if err != nil {
		return err
	}
	return insertWorkerAuditTx(ctx, s.tx, id, "schedule.job.fail", reason)
}

func (s sqlLockedJobStore) markCompleted(ctx context.Context, job domain.ScheduleJob) error {
	if _, err := s.tx.ExecContext(ctx, `
		UPDATE schedule_jobs
		SET status = 'completed', message = '排程任務已完成。', completed_at = NOW(), updated_at = NOW()
		WHERE id = $1
	`, job.ID); err != nil {
		return err
	}
	if err := insertWorkerAuditTx(ctx, s.tx, job.ID, "schedule.job.complete", "排程任務已完成。"); err != nil {
		return err
	}
	if job.PreviewID != "" {
		if _, err := s.tx.ExecContext(ctx, "DELETE FROM schedule_previews WHERE id = $1", job.PreviewID); err != nil {
			return err
		}
	}
	return nil
}

func runLockedJobState(ctx context.Context, store lockedJobStore, job domain.ScheduleJob, maxRetries int) (error, bool) {
	status, found, err := store.jobStatus(ctx, job.ID)
	if err != nil {
		return err, false
	}
	if !found {
		return nil, true
	}
	if status == domain.JobCancelled {
		return nil, true
	}
	if status != domain.JobQueued {
		return nil, true
	}

	attempt, err := store.markRunning(ctx, job.ID)
	if err != nil {
		return err, false
	}
	if err := store.persist(ctx, job); err != nil {
		if _, ok := err.(errStaleScheduleData); !ok && attempt < maxRetries {
			_ = store.markRetryAfterRun(ctx, job.ID, "排程任務暫時失敗，等待重試。")
			return err, true
		}
		_ = store.markFailedAfterRun(ctx, job.ID, "排程任務失敗："+err.Error(), err.Error())
		return nil, true
	}
	if err := store.markCompleted(ctx, job); err != nil {
		return err, false
	}
	return nil, true
}

func startLockRenewal(ctx context.Context, lineLock womslock.Lock, ttl, interval time.Duration) (context.Context, context.CancelFunc) {
	runCtx, cancel := context.WithCancel(ctx)
	if interval <= 0 {
		return runCtx, cancel
	}
	var stopped atomic.Bool
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				if stopped.Load() {
					return
				}
				if err := lineLock.Refresh(runCtx, ttl); err != nil {
					log.Printf("redis line lock renewal failed: %v", err)
					cancel()
					return
				}
			}
		}
	}()
	return runCtx, func() {
		stopped.Store(true)
		cancel()
	}
}

func markJobRetry(ctx context.Context, db *sql.DB, jobID, message string) error {
	_, err := db.ExecContext(ctx, `
		UPDATE schedule_jobs
		SET status = 'queued', message = $2, updated_at = NOW()
		WHERE id = $1 AND status = 'queued'
	`, jobID, message)
	return err
}

func markJobFailed(ctx context.Context, db *sql.DB, jobID, message string) error {
	_, err := db.ExecContext(ctx, `
		UPDATE schedule_jobs
		SET status = 'failed', message = $2, completed_at = NOW(), updated_at = NOW()
		WHERE id = $1
	`, jobID, message)
	return err
}

func insertWorkerAuditTx(ctx context.Context, tx *sql.Tx, jobID, action, reason string) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO audit_logs (id, actor_id, action, resource, reason, created_at)
		SELECT 'AUD-WORKER-' || $2 || '-' || $1, actor_id, $2, $1, $3, NOW()
		FROM audit_logs
		WHERE resource = $1 AND action = 'schedule.job.create'
		ORDER BY created_at
		LIMIT 1
		ON CONFLICT (id) DO NOTHING
	`, jobID, action, reason)
	return err
}

func persistLineSchedule(ctx context.Context, tx *sql.Tx, job domain.ScheduleJob) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT id, quantity, priority
		FROM orders
		WHERE line_id = $1 AND status = '待排程'
		ORDER BY CASE WHEN priority = 'high' THEN 0 ELSE 1 END, due_date, created_at, id
	`, job.LineID)
	if err != nil {
		return err
	}
	defer rows.Close()

	type orderRow struct {
		id       string
		quantity int
		priority string
	}
	orders := []orderRow{}
	for rows.Next() {
		var order orderRow
		if err := rows.Scan(&order.id, &order.quantity, &order.priority); err != nil {
			return err
		}
		if len(job.OrderIDs) > 0 && !contains(job.OrderIDs, order.id) {
			continue
		}
		orders = append(orders, order)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(orders) == 0 {
		return nil
	}

	var capacity int
	var revision int64
	if err := tx.QueryRowContext(ctx, "SELECT capacity_per_day, schedule_revision FROM production_lines WHERE id = $1 FOR UPDATE", job.LineID).Scan(&capacity, &revision); err != nil {
		return err
	}
	if job.Source != "hpa-peak-demo" && job.LineRevision != 0 && revision != job.LineRevision {
		return errStaleScheduleData{}
	}
	scheduleDate := truncateDate(time.Now().UTC())
	used := 0
	for _, order := range orders {
		if used+order.quantity > capacity {
			scheduleDate = scheduleDate.AddDate(0, 0, 1)
			used = 0
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO schedule_allocations (order_id, line_id, allocation_date, quantity, priority, locked, status)
			VALUES ($1, $2, $3, $4, $5, FALSE, '已排程')
		`, order.id, job.LineID, scheduleDate, order.quantity, order.priority); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE orders SET status = '已排程', updated_at = NOW() WHERE id = $1", order.id); err != nil {
			return err
		}
		used += order.quantity
	}
	_, err = tx.ExecContext(ctx, "UPDATE production_lines SET schedule_revision = schedule_revision + 1 WHERE id = $1", job.LineID)
	return err
}

func persistPreviewAllocations(ctx context.Context, tx *sql.Tx, job domain.ScheduleJob) error {
	var revision int64
	var allocationsJSON []byte
	if err := tx.QueryRowContext(ctx, `
		SELECT line_revision, allocations
		FROM schedule_previews
		WHERE id = $1 AND line_id = $2 AND expires_at > NOW()
	`, job.PreviewID, job.LineID).Scan(&revision, &allocationsJSON); err != nil {
		if err == sql.ErrNoRows {
			return errStaleScheduleData{}
		}
		return err
	}
	var currentRevision int64
	if err := tx.QueryRowContext(ctx, "SELECT schedule_revision FROM production_lines WHERE id = $1 FOR UPDATE", job.LineID).Scan(&currentRevision); err != nil {
		return err
	}
	if currentRevision != revision || (job.LineRevision != 0 && job.LineRevision != revision) {
		return errStaleScheduleData{}
	}
	var allocations []scheduler.Allocation
	if err := json.Unmarshal(allocationsJSON, &allocations); err != nil {
		return err
	}
	if len(allocations) == 0 {
		return nil
	}
	orderIDs := map[string]bool{}
	for _, allocation := range allocations {
		if allocation.LineID != job.LineID {
			return errStaleScheduleData{}
		}
		orderIDs[allocation.OrderID] = true
	}
	sourceFirstQuantities := map[string]int{}
	splitAllocations := []scheduler.Allocation{}
	for _, allocation := range allocations {
		if allocation.SourceOrderID == "" {
			if _, ok := sourceFirstQuantities[allocation.OrderID]; !ok {
				sourceFirstQuantities[allocation.OrderID] = allocation.Quantity
			}
			continue
		}
		splitAllocations = append(splitAllocations, allocation)
	}
	for _, allocation := range splitAllocations {
		if firstQuantity, ok := sourceFirstQuantities[allocation.SourceOrderID]; ok {
			if _, err := tx.ExecContext(ctx, "UPDATE orders SET quantity = $2, updated_at = NOW() WHERE id = $1", allocation.SourceOrderID, firstQuantity); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO orders (id, customer, line_id, quantity, priority, status, due_date, note, created_by, source_order, created_at, updated_at)
			SELECT $1, customer, line_id, $2, priority, '待排程', due_date, note, created_by, $3, NOW(), NOW()
			FROM orders
			WHERE id = $3
			ON CONFLICT (id) DO NOTHING
		`, allocation.OrderID, allocation.Quantity, allocation.SourceOrderID); err != nil {
			return err
		}
	}
	for orderID := range orderIDs {
		if _, err := tx.ExecContext(ctx, "DELETE FROM schedule_allocations WHERE order_id = $1 AND COALESCE(status, '已排程') <> '已完成'", orderID); err != nil {
			return err
		}
	}
	for _, allocation := range allocations {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO schedule_allocations (order_id, line_id, allocation_date, quantity, priority, locked, status)
			VALUES ($1, $2, $3, $4, $5, $6, '已排程')
		`, allocation.OrderID, allocation.LineID, truncateDate(allocation.Date), allocation.Quantity, allocation.Priority, allocation.Locked); err != nil {
			return err
		}
	}
	for orderID := range orderIDs {
		if _, err := tx.ExecContext(ctx, "UPDATE orders SET status = '已排程', updated_at = NOW() WHERE id = $1 AND status = '待排程'", orderID); err != nil {
			return err
		}
	}
	_, err := tx.ExecContext(ctx, "UPDATE production_lines SET schedule_revision = schedule_revision + 1 WHERE id = $1", job.LineID)
	return err
}

func backfillQueuedJobs(ctx context.Context, db *sql.DB, lockProvider womslock.Provider, maxRetries int, lockTTL, lockRenewInterval, lockTimeout time.Duration) error {
	const backfillBatchSize = 100

	var (
		lastCreatedAt time.Time
		lastID        string
		hasCursor     bool
	)

	for {
		var (
			rows *sql.Rows
			err  error
		)

		if hasCursor {
			rows, err = db.QueryContext(ctx, `
				SELECT id, line_id, COALESCE(source, ''), COALESCE(preview_id, ''),
				       COALESCE(request_hash, ''), line_revision, order_ids, created_at, updated_at
				FROM schedule_jobs
				WHERE status = 'queued'
				  AND (created_at > $1 OR (created_at = $1 AND id > $2))
				ORDER BY created_at, id
				LIMIT $3
			`, lastCreatedAt, lastID, backfillBatchSize)
		} else {
			rows, err = db.QueryContext(ctx, `
				SELECT id, line_id, COALESCE(source, ''), COALESCE(preview_id, ''),
				       COALESCE(request_hash, ''), line_revision, order_ids, created_at, updated_at
				FROM schedule_jobs
				WHERE status = 'queued'
				ORDER BY created_at, id
				LIMIT $1
			`, backfillBatchSize)
		}
		if err != nil {
			return err
		}

		batchCount := 0
		for rows.Next() {
			var job domain.ScheduleJob
			var orderIDsJSON []byte
			if err := rows.Scan(
				&job.ID,
				&job.LineID,
				&job.Source,
				&job.PreviewID,
				&job.RequestHash,
				&job.LineRevision,
				&orderIDsJSON,
				&job.CreatedAt,
				&job.UpdatedAt,
			); err != nil {
				rows.Close()
				return err
			}
			_ = json.Unmarshal(orderIDsJSON, &job.OrderIDs)
			job.Status = domain.JobQueued
			payload, err := json.Marshal(job)
			if err != nil {
				rows.Close()
				return err
			}
			if err := processDBJob(ctx, db, lockProvider, payload, workerJobConfig{
				maxRetries:        maxRetries,
				lockTTL:           lockTTL,
				lockRenewInterval: lockRenewInterval,
				lockTimeout:       lockTimeout,
			}); err != nil {
				log.Printf("scheduler backfill job failed id=%s error=%v", job.ID, err)
			}

			lastCreatedAt = job.CreatedAt
			lastID = job.ID
			hasCursor = true
			batchCount++
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if batchCount < backfillBatchSize {
			return nil
		}
	}
}

type errStaleScheduleData struct{}

func (errStaleScheduleData) Error() string {
	return "排程資料已變更，請重新試排。"
}

func configString(lookup func(string) (string, bool), key, fallback string) string {
	value, ok := lookup(key)
	if !ok {
		return fallback
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

func configDuration(lookup func(string) (string, bool), key string, fallback time.Duration) (time.Duration, error) {
	raw, ok := lookup(key)
	value := strings.TrimSpace(raw)
	if !ok || value == "" {
		return fallback, nil
	}
	millis, err := strconv.Atoi(value)
	if err != nil {
		return 0, errors.New(key + " must be an integer number of milliseconds")
	}
	if millis < 0 {
		return 0, errors.New(key + " must be greater than or equal to zero")
	}
	return time.Duration(millis) * time.Millisecond, nil
}

func configInt(lookup func(string) (string, bool), key string, fallback int) (int, error) {
	raw, ok := lookup(key)
	value := strings.TrimSpace(raw)
	if !ok || value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, errors.New(key + " must be an integer")
	}
	if parsed < 0 {
		return 0, errors.New(key + " must be greater than or equal to zero")
	}
	return parsed, nil
}

func configStartOffset(value string) (int64, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "earliest", "first", "oldest":
		return kafka.FirstOffset, nil
	case "", "latest", "last", "newest":
		return kafka.LastOffset, nil
	default:
		return 0, errors.New("WORKER_START_OFFSET must be one of earliest, first, oldest, latest, last, newest, or empty")
	}
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func scheduleLineLockKey(lineID string) string {
	return "woms:locks:schedule-line:" + lineID
}

func truncateDate(value time.Time) time.Time {
	year, month, day := value.Date()
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}
