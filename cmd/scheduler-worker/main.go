package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/d11nn/woms/internal/domain"
	"github.com/d11nn/woms/internal/scheduler"
	"github.com/d11nn/woms/internal/startup"
	_ "github.com/lib/pq"
	"github.com/segmentio/kafka-go"
)

func main() {
	brokers := env("KAFKA_BROKERS", "kafka:9092")
	brokerList := startup.SplitCSV(brokers)
	topic := env("KAFKA_SCHEDULE_TOPIC", "woms.schedule.jobs")
	group := env("KAFKA_CONSUMER_GROUP", "woms-scheduler-workers")
	databaseURL := env("DATABASE_URL", "")
	minJobDuration := envDuration("WORKER_MIN_JOB_DURATION_MS", 0)
	maxRetries := envInt("WORKER_MAX_RETRIES", 3)
	backfillInterval := envDuration("WORKER_BACKFILL_INTERVAL_MS", 5*time.Second)
	dependencyTimeout := envDuration("WORKER_DEPENDENCY_RETRY_TIMEOUT_MS", 2*time.Minute)
	dependencyInterval := envDuration("WORKER_DEPENDENCY_RETRY_INTERVAL_MS", 2*time.Second)
	startOffsetLabel := strings.ToLower(strings.TrimSpace(env("WORKER_START_OFFSET", "latest")))
	startOffset := kafka.LastOffset
	switch startOffsetLabel {
	case "earliest", "first", "oldest":
		startOffset = kafka.FirstOffset
	case "latest", "last", "newest", "":
		startOffset = kafka.LastOffset
	default:
		log.Printf("invalid WORKER_START_OFFSET=%q, defaulting to latest", startOffsetLabel)
		startOffset = kafka.LastOffset
	}
	var db *sql.DB
	if databaseURL != "" {
		var err error
		db, err = sql.Open("postgres", databaseURL)
		if err != nil {
			log.Fatalf("postgres open failed: %v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), dependencyTimeout)
		err = startup.RetryDependency(ctx, "postgres", dependencyInterval, log.Printf, func(ctx context.Context) error {
			return db.PingContext(ctx)
		})
		cancel()
		if err != nil {
			log.Fatalf("postgres ping failed: %v", err)
		}
		defer db.Close()
		if err := backfillQueuedJobs(context.Background(), db, maxRetries); err != nil {
			log.Printf("scheduler backfill failed: %v", err)
		}
	}

	log.Printf("scheduler worker starting brokers=%s topic=%s group=%s minJobDuration=%s", brokers, topic, group, minJobDuration)
	ctx, cancel := context.WithTimeout(context.Background(), dependencyTimeout)
	if err := startup.RetryDependency(ctx, "kafka broker", dependencyInterval, log.Printf, func(ctx context.Context) error {
		return startup.PingAnyTCP(ctx, brokerList)
	}); err != nil {
		cancel()
		log.Fatalf("kafka broker failed: %v", err)
	}
	cancel()
	reader := kafka.NewReader(kafka.ReaderConfig{
		Brokers: brokerList,
		Topic:   topic,
		GroupID: group,
		// Ensure the consumer picks up topics/partitions created after startup.
		WatchPartitionChanges:  true,
		PartitionWatchInterval: 5 * time.Second,
		StartOffset:            startOffset,
	})
	defer reader.Close()
	if db != nil && backfillInterval > 0 {
		go func() {
			ticker := time.NewTicker(backfillInterval)
			defer ticker.Stop()
			for range ticker.C {
				if err := backfillQueuedJobs(context.Background(), db, maxRetries); err != nil {
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
		if minJobDuration > 0 {
			time.Sleep(minJobDuration)
		}
		if db != nil {
			if err := processDBJob(context.Background(), db, message.Value, maxRetries); err != nil {
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

func processDBJob(ctx context.Context, db *sql.DB, payload []byte, maxRetries int) error {
	var job domain.ScheduleJob
	if err := json.Unmarshal(payload, &job); err != nil {
		return err
	}
	if job.ID == "" || job.LineID == "" {
		return nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(hashtext($1))", job.LineID); err != nil {
		return err
	}
	var status domain.ScheduleJobStatus
	if err := tx.QueryRowContext(ctx, "SELECT status FROM schedule_jobs WHERE id = $1 FOR UPDATE", job.ID).Scan(&status); err != nil {
		return err
	}
	if status == domain.JobCancelled {
		return tx.Commit()
	}
	if status != domain.JobQueued {
		return tx.Commit()
	}
	var attempt int
	if err := tx.QueryRowContext(ctx, `
		UPDATE schedule_jobs
		SET status = 'running',
		    message = '排程任務執行中。',
		    started_at = COALESCE(started_at, NOW()),
		    attempt_count = attempt_count + 1,
		    updated_at = NOW()
		WHERE id = $1
		RETURNING attempt_count
	`, job.ID).Scan(&attempt); err != nil {
		return err
	}

	var persistErr error
	if job.Source == "hpa-peak-demo" || job.PreviewID == "" {
		persistErr = persistLineSchedule(ctx, tx, job)
	} else {
		persistErr = persistPreviewAllocations(ctx, tx, job)
	}
	if err := persistErr; err != nil {
		if _, ok := err.(errStaleScheduleData); !ok && attempt < maxRetries {
			_, _ = tx.ExecContext(ctx, `
				UPDATE schedule_jobs
				SET status = 'queued', message = $2, updated_at = NOW()
				WHERE id = $1
			`, job.ID, "排程任務暫時失敗，等待重試。")
			if commitErr := tx.Commit(); commitErr != nil {
				return commitErr
			}
			return err
		}
		_, _ = tx.ExecContext(ctx, `
			UPDATE schedule_jobs
			SET status = 'failed', message = $2, completed_at = NOW(), updated_at = NOW()
			WHERE id = $1
		`, job.ID, "排程任務失敗："+err.Error())
		_ = insertWorkerAuditTx(ctx, tx, job.ID, "schedule.job.fail", err.Error())
		return tx.Commit()
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE schedule_jobs
		SET status = 'completed', message = '排程任務已完成。', completed_at = NOW(), updated_at = NOW()
		WHERE id = $1
	`, job.ID); err != nil {
		return err
	}
	if err := insertWorkerAuditTx(ctx, tx, job.ID, "schedule.job.complete", "排程任務已完成。"); err != nil {
		return err
	}
	if job.PreviewID != "" {
		if _, err := tx.ExecContext(ctx, "DELETE FROM schedule_previews WHERE id = $1", job.PreviewID); err != nil {
			return err
		}
	}
	return tx.Commit()
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
		ORDER BY due_date, id
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

func backfillQueuedJobs(ctx context.Context, db *sql.DB, maxRetries int) error {
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
			if err := processDBJob(ctx, db, payload, maxRetries); err != nil {
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

func env(key, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}

func envDuration(key string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	millis, err := strconv.Atoi(value)
	if err != nil || millis < 0 {
		return fallback
	}
	return time.Duration(millis) * time.Millisecond
}

func envInt(key string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 0 {
		return fallback
	}
	return parsed
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func truncateDate(value time.Time) time.Time {
	year, month, day := value.Date()
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}
