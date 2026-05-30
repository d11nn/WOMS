package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/d11nn/woms/internal/domain"
	womslock "github.com/d11nn/woms/internal/lock"
	"github.com/segmentio/kafka-go"
)

func TestLoadWorkerConfigParsesDefaultsAndTrimmedValues(t *testing.T) {
	config, err := loadWorkerConfig(mapLookup(map[string]string{
		"KAFKA_BROKERS":                          " kafka-a:9092,kafka-b:9092 ",
		"KAFKA_SCHEDULE_TOPIC":                   " custom.topic ",
		"KAFKA_CONSUMER_GROUP":                   " custom-group ",
		"DATABASE_URL":                           " postgres://example ",
		"REDIS_ADDR":                             " redis:6379 ",
		"WORKER_MIN_JOB_DURATION_MS":             "125",
		"WORKER_MAX_RETRIES":                     "7",
		"WORKER_LOCK_TTL_MS":                     "20000",
		"WORKER_LOCK_RENEW_INTERVAL_MS":          "4000",
		"WORKER_LOCK_TIMEOUT_MS":                 "3000",
		"WORKER_BACKFILL_INTERVAL_MS":            "6000",
		"WORKER_DEPENDENCY_RETRY_TIMEOUT_MS":     "9000",
		"WORKER_DEPENDENCY_RETRY_INTERVAL_MS":    "250",
		"WORKER_START_OFFSET":                    " earliest ",
		"UNRELATED_EMPTY_VALUES_SHOULD_NOT_HURT": "",
	}))
	if err != nil {
		t.Fatalf("load worker config: %v", err)
	}
	if config.brokers != "kafka-a:9092,kafka-b:9092" {
		t.Fatalf("unexpected brokers %q", config.brokers)
	}
	if config.topic != "custom.topic" || config.group != "custom-group" {
		t.Fatalf("unexpected topic/group %q/%q", config.topic, config.group)
	}
	if config.databaseURL != "postgres://example" || config.redisAddr != "redis:6379" {
		t.Fatalf("unexpected database/redis config %q/%q", config.databaseURL, config.redisAddr)
	}
	if config.minJobDuration != 125*time.Millisecond || config.maxRetries != 7 {
		t.Fatalf("unexpected duration/retries %s/%d", config.minJobDuration, config.maxRetries)
	}
	if config.lockTTL != 20*time.Second || config.lockRenewInterval != 4*time.Second || config.lockTimeout != 3*time.Second {
		t.Fatalf("unexpected lock config %s/%s/%s", config.lockTTL, config.lockRenewInterval, config.lockTimeout)
	}
	if config.backfillInterval != 6*time.Second || config.dependencyTimeout != 9*time.Second || config.dependencyInterval != 250*time.Millisecond {
		t.Fatalf("unexpected worker intervals %s/%s/%s", config.backfillInterval, config.dependencyTimeout, config.dependencyInterval)
	}
	if config.startOffset != kafka.FirstOffset {
		t.Fatalf("expected first offset, got %d", config.startOffset)
	}
}

func TestLoadWorkerConfigUsesDefaultsForMissingOrEmptyValues(t *testing.T) {
	config, err := loadWorkerConfig(mapLookup(map[string]string{
		"KAFKA_BROKERS":              " ",
		"KAFKA_SCHEDULE_TOPIC":       "",
		"KAFKA_CONSUMER_GROUP":       " ",
		"WORKER_START_OFFSET":        "",
		"WORKER_MIN_JOB_DURATION_MS": " ",
		"WORKER_MAX_RETRIES":         "",
	}))
	if err != nil {
		t.Fatalf("load worker config: %v", err)
	}
	if config.brokers != "kafka:9092" {
		t.Fatalf("unexpected default brokers %q", config.brokers)
	}
	if config.topic != "woms.schedule.jobs" || config.group != "woms-scheduler-workers" {
		t.Fatalf("unexpected default topic/group %q/%q", config.topic, config.group)
	}
	if config.minJobDuration != 0 || config.maxRetries != 3 {
		t.Fatalf("unexpected default duration/retries %s/%d", config.minJobDuration, config.maxRetries)
	}
	if config.startOffset != kafka.LastOffset {
		t.Fatalf("expected last offset, got %d", config.startOffset)
	}
}

func TestConfigStartOffsetLabels(t *testing.T) {
	cases := []struct {
		label string
		want  int64
	}{
		{"earliest", kafka.FirstOffset},
		{"first", kafka.FirstOffset},
		{"oldest", kafka.FirstOffset},
		{" EARLIEST ", kafka.FirstOffset},
		{"latest", kafka.LastOffset},
		{"last", kafka.LastOffset},
		{"newest", kafka.LastOffset},
		{"", kafka.LastOffset},
	}
	for _, tc := range cases {
		got, err := configStartOffset(tc.label)
		if err != nil {
			t.Fatalf("%q: unexpected error %v", tc.label, err)
		}
		if got != tc.want {
			t.Fatalf("%q: got %d want %d", tc.label, got, tc.want)
		}
	}
	if _, err := configStartOffset("middle"); err == nil || !strings.Contains(err.Error(), "WORKER_START_OFFSET") {
		t.Fatalf("expected clear invalid offset error, got %v", err)
	}
}

func TestLoadWorkerConfigRejectsInvalidDurationsAndIntegers(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want string
	}{
		{
			name: "malformed duration",
			env:  map[string]string{"WORKER_LOCK_TTL_MS": "abc"},
			want: "WORKER_LOCK_TTL_MS must be an integer number of milliseconds",
		},
		{
			name: "negative duration",
			env:  map[string]string{"WORKER_LOCK_TIMEOUT_MS": "-1"},
			want: "WORKER_LOCK_TIMEOUT_MS must be greater than or equal to zero",
		},
		{
			name: "malformed int",
			env:  map[string]string{"WORKER_MAX_RETRIES": "many"},
			want: "WORKER_MAX_RETRIES must be an integer",
		},
		{
			name: "negative int",
			env:  map[string]string{"WORKER_MAX_RETRIES": "-1"},
			want: "WORKER_MAX_RETRIES must be greater than or equal to zero",
		},
		{
			name: "invalid offset",
			env:  map[string]string{"WORKER_START_OFFSET": "middle"},
			want: "WORKER_START_OFFSET must be one of",
		},
		{
			name: "malformed min job duration",
			env:  map[string]string{"WORKER_MIN_JOB_DURATION_MS": "abc"},
			want: "WORKER_MIN_JOB_DURATION_MS must be an integer number of milliseconds",
		},
		{
			name: "malformed lock renew interval",
			env:  map[string]string{"WORKER_LOCK_RENEW_INTERVAL_MS": "abc"},
			want: "WORKER_LOCK_RENEW_INTERVAL_MS must be an integer number of milliseconds",
		},
		{
			name: "malformed backfill interval",
			env:  map[string]string{"WORKER_BACKFILL_INTERVAL_MS": "abc"},
			want: "WORKER_BACKFILL_INTERVAL_MS must be an integer number of milliseconds",
		},
		{
			name: "malformed dependency timeout",
			env:  map[string]string{"WORKER_DEPENDENCY_RETRY_TIMEOUT_MS": "abc"},
			want: "WORKER_DEPENDENCY_RETRY_TIMEOUT_MS must be an integer number of milliseconds",
		},
		{
			name: "malformed dependency interval",
			env:  map[string]string{"WORKER_DEPENDENCY_RETRY_INTERVAL_MS": "abc"},
			want: "WORKER_DEPENDENCY_RETRY_INTERVAL_MS must be an integer number of milliseconds",
		},
	}
	for _, tc := range cases {
		_, err := loadWorkerConfig(mapLookup(tc.env))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: expected error containing %q, got %v", tc.name, tc.want, err)
		}
	}
}

func TestScheduleLineLockKeyScopesByProductionLine(t *testing.T) {
	if got := scheduleLineLockKey("A"); got != "woms:locks:schedule-line:A" {
		t.Fatalf("unexpected line A key %q", got)
	}
	if scheduleLineLockKey("A") == scheduleLineLockKey("B") {
		t.Fatal("different production lines must use different Redis lock keys")
	}
}

func TestAcquireLineLockRetriesContentionUntilAvailable(t *testing.T) {
	provider := &retryLockProvider{failures: 2}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	lineLock, err := acquireLineLock(ctx, provider, "woms:locks:schedule-line:A", time.Second)
	if err != nil {
		t.Fatalf("acquire lock: %v", err)
	}
	if lineLock == nil {
		t.Fatal("expected lock")
	}
	if provider.attempts != 3 {
		t.Fatalf("expected 3 attempts, got %d", provider.attempts)
	}
}

func TestAcquireLineLockStopsOnNonContentionError(t *testing.T) {
	expected := errors.New("redis unavailable")
	provider := &retryLockProvider{err: expected}
	_, err := acquireLineLock(context.Background(), provider, "woms:locks:schedule-line:A", time.Second)
	if !errors.Is(err, expected) {
		t.Fatalf("expected %v, got %v", expected, err)
	}
	if provider.attempts != 1 {
		t.Fatalf("expected 1 attempt, got %d", provider.attempts)
	}
}

func TestProcessJobPayloadRejectsInvalidJSON(t *testing.T) {
	executor := &fakeJobExecutor{}
	err := processJobPayload(context.Background(), executor, nil, []byte("{"), workerJobConfig{maxRetries: 3, lockTTL: time.Second, lockTimeout: time.Second})
	if err == nil {
		t.Fatal("expected invalid JSON error")
	}
	if executor.failedJobs != 0 || executor.retryJobs != 0 || executor.lockedJobs != 0 {
		t.Fatalf("invalid JSON should not touch job state: %+v", executor)
	}
}

func TestProcessJobPayloadIgnoresMissingJobOrLineID(t *testing.T) {
	cases := []domain.ScheduleJob{
		{LineID: "A"},
		{ID: "JOB-1"},
	}
	for _, job := range cases {
		payload := mustMarshalJob(t, job)
		executor := &fakeJobExecutor{}
		if err := processJobPayload(context.Background(), executor, nil, payload, workerJobConfig{maxRetries: 3, lockTTL: time.Second, lockTimeout: time.Second}); err != nil {
			t.Fatalf("missing ID/line should be ignored: %v", err)
		}
		if executor.failedJobs != 0 || executor.retryJobs != 0 || executor.lockedJobs != 0 {
			t.Fatalf("missing ID/line should not touch job state: %+v", executor)
		}
	}
}

func TestProcessJobPayloadMarksFailedWhenLockProviderMissing(t *testing.T) {
	executor := &fakeJobExecutor{}
	payload := mustMarshalJob(t, domain.ScheduleJob{ID: "JOB-1", LineID: "A"})
	if err := processJobPayload(context.Background(), executor, nil, payload, workerJobConfig{maxRetries: 3, lockTTL: time.Second, lockTimeout: time.Second}); err != nil {
		t.Fatalf("process payload: %v", err)
	}
	if executor.failedJobs != 1 || executor.failedJobID != "JOB-1" {
		t.Fatalf("expected failed JOB-1, got %+v", executor)
	}
	if executor.failedMessage != "Redis 排程鎖未設定。" {
		t.Fatalf("unexpected failure message %q", executor.failedMessage)
	}
}

func TestProcessJobPayloadMarksRetryWhenLockAcquisitionTimesOut(t *testing.T) {
	executor := &fakeJobExecutor{}
	provider := &retryLockProvider{failures: 100}
	payload := mustMarshalJob(t, domain.ScheduleJob{ID: "JOB-2", LineID: "A"})
	if err := processJobPayload(context.Background(), executor, provider, payload, workerJobConfig{maxRetries: 3, lockTTL: time.Second, lockTimeout: time.Nanosecond}); err != nil {
		t.Fatalf("process payload: %v", err)
	}
	if executor.retryJobs != 1 || executor.retryJobID != "JOB-2" {
		t.Fatalf("expected retry JOB-2, got %+v", executor)
	}
	if executor.retryMessage != "同產線排程鎖取得逾時，等待重試。" {
		t.Fatalf("unexpected retry message %q", executor.retryMessage)
	}
	if executor.lockedJobs != 0 {
		t.Fatalf("timed out job should not execute, got %d executions", executor.lockedJobs)
	}
}

func TestStartLockRenewalCancelsWorkWhenRefreshFails(t *testing.T) {
	refreshErr := errors.New("refresh failed")
	lineLock := &recordingLock{refreshErr: refreshErr}
	runCtx, stop := startLockRenewal(context.Background(), lineLock, time.Second, time.Millisecond)
	defer stop()

	select {
	case <-runCtx.Done():
	case <-time.After(200 * time.Millisecond):
		t.Fatal("expected renewal failure to cancel run context")
	}
	if lineLock.refreshes == 0 {
		t.Fatal("expected at least one refresh attempt")
	}
}

func TestProcessJobPayloadStopsLockedWorkWhenRenewalFails(t *testing.T) {
	lineLock := &recordingLock{refreshErr: errors.New("refresh failed")}
	provider := &singleLockProvider{lock: lineLock}
	executor := &fakeJobExecutor{
		processFn: func(ctx context.Context, _ domain.ScheduleJob, _ int) error {
			<-ctx.Done()
			return ctx.Err()
		},
	}
	payload := mustMarshalJob(t, domain.ScheduleJob{ID: "JOB-3", LineID: "A"})
	err := processJobPayload(context.Background(), executor, provider, payload, workerJobConfig{maxRetries: 3, lockTTL: time.Second, lockRenewInterval: time.Millisecond, lockTimeout: time.Second})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled work after renewal failure, got %v", err)
	}
	if executor.lockedJobs != 1 {
		t.Fatalf("expected one locked execution, got %d", executor.lockedJobs)
	}
	if lineLock.releases != 1 {
		t.Fatalf("expected lock release, got %d", lineLock.releases)
	}
}

func TestRunLockedJobStateSkipsNonExecutableJobs(t *testing.T) {
	cases := []struct {
		name   string
		found  bool
		status domain.ScheduleJobStatus
	}{
		{name: "missing", found: false},
		{name: "cancelled", found: true, status: domain.JobCancelled},
		{name: "running", found: true, status: domain.JobRunning},
		{name: "completed", found: true, status: domain.JobCompleted},
	}
	for _, tc := range cases {
		store := &fakeLockedJobStore{found: tc.found, status: tc.status}
		err, commit := runLockedJobState(context.Background(), store, domain.ScheduleJob{ID: "JOB-STATE", LineID: "A"}, 3)
		if err != nil || !commit {
			t.Fatalf("%s: expected clean commit, got err=%v commit=%v", tc.name, err, commit)
		}
		if store.runningCalls != 0 || store.persistCalls != 0 || store.completedCalls != 0 {
			t.Fatalf("%s: should not execute job, got store %+v", tc.name, store)
		}
	}
}

func TestRunLockedJobStateCompletesQueuedJob(t *testing.T) {
	store := &fakeLockedJobStore{found: true, status: domain.JobQueued}
	err, commit := runLockedJobState(context.Background(), store, domain.ScheduleJob{ID: "JOB-QUEUED", LineID: "A"}, 3)
	if err != nil || !commit {
		t.Fatalf("expected success commit, got err=%v commit=%v", err, commit)
	}
	if store.status != domain.JobCompleted {
		t.Fatalf("expected completed status, got %q", store.status)
	}
	if store.attempt != 1 {
		t.Fatalf("expected attempt count 1, got %d", store.attempt)
	}
	if store.persistCalls != 1 || store.completedCalls != 1 {
		t.Fatalf("expected persist and complete calls, got %+v", store)
	}
}

func TestRunLockedJobStateRetriesTransientPersistFailureBelowMaxRetries(t *testing.T) {
	persistErr := errors.New("temporary database problem")
	store := &fakeLockedJobStore{found: true, status: domain.JobQueued, persistErr: persistErr}
	err, commit := runLockedJobState(context.Background(), store, domain.ScheduleJob{ID: "JOB-RETRY", LineID: "A"}, 3)
	if !errors.Is(err, persistErr) || !commit {
		t.Fatalf("expected retryable persist error with commit, got err=%v commit=%v", err, commit)
	}
	if store.status != domain.JobQueued || store.retryCalls != 1 {
		t.Fatalf("expected queued retry state, got %+v", store)
	}
	if store.retryMessage != "排程任務暫時失敗，等待重試。" {
		t.Fatalf("unexpected retry message %q", store.retryMessage)
	}
}

func TestRunLockedJobStateFailsPersistFailureAtMaxRetries(t *testing.T) {
	persistErr := errors.New("permanent database problem")
	store := &fakeLockedJobStore{found: true, status: domain.JobQueued, attempt: 2, persistErr: persistErr}
	err, commit := runLockedJobState(context.Background(), store, domain.ScheduleJob{ID: "JOB-FAIL", LineID: "A"}, 3)
	if err != nil || !commit {
		t.Fatalf("expected failed job commit without returned error, got err=%v commit=%v", err, commit)
	}
	if store.status != domain.JobFailed || store.failedCalls != 1 {
		t.Fatalf("expected failed state, got %+v", store)
	}
	if store.failedMessage != "排程任務失敗："+persistErr.Error() || store.failedReason != persistErr.Error() {
		t.Fatalf("unexpected failed message/reason %q/%q", store.failedMessage, store.failedReason)
	}
}

func TestRunLockedJobStateFailsStaleScheduleDataWithoutRetry(t *testing.T) {
	store := &fakeLockedJobStore{found: true, status: domain.JobQueued, persistErr: errStaleScheduleData{}}
	err, commit := runLockedJobState(context.Background(), store, domain.ScheduleJob{ID: "JOB-STALE", LineID: "A"}, 3)
	if err != nil || !commit {
		t.Fatalf("expected stale data to fail cleanly, got err=%v commit=%v", err, commit)
	}
	if store.status != domain.JobFailed || store.retryCalls != 0 {
		t.Fatalf("expected failed stale-data state without retry, got %+v", store)
	}
}

func TestValidateLockConfigRejectsInvalidDurations(t *testing.T) {
	validTTL := 15 * time.Second
	validRenew := 5 * time.Second
	validTimeout := 10 * time.Second
	if err := validateLockConfig(validTTL, validRenew, validTimeout); err != nil {
		t.Fatalf("expected valid lock config, got %v", err)
	}
	cases := []struct {
		name    string
		ttl     time.Duration
		renew   time.Duration
		timeout time.Duration
	}{
		{"zero ttl", 0, validRenew, validTimeout},
		{"zero timeout", validTTL, validRenew, 0},
		{"zero renew", validTTL, 0, validTimeout},
		{"renew equals ttl", validTTL, validTTL, validTimeout},
	}
	for _, tc := range cases {
		if err := validateLockConfig(tc.ttl, tc.renew, tc.timeout); err == nil {
			t.Fatalf("%s: expected invalid lock config", tc.name)
		}
	}
}

type retryLockProvider struct {
	failures int
	attempts int
	err      error
}

func (p *retryLockProvider) Acquire(context.Context, string, time.Duration) (womslock.Lock, error) {
	p.attempts++
	if p.err != nil {
		return nil, p.err
	}
	if p.attempts <= p.failures {
		return nil, womslock.ErrNotAcquired
	}
	return noopLock{}, nil
}

type noopLock struct{}

func (noopLock) Refresh(context.Context, time.Duration) error { return nil }
func (noopLock) Release(context.Context) error                { return nil }

func mapLookup(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}

func mustMarshalJob(t *testing.T, job domain.ScheduleJob) []byte {
	t.Helper()
	payload, err := json.Marshal(job)
	if err != nil {
		t.Fatalf("marshal job: %v", err)
	}
	return payload
}

type fakeJobExecutor struct {
	failedJobs    int
	failedJobID   string
	failedMessage string
	retryJobs     int
	retryJobID    string
	retryMessage  string
	lockedJobs    int
	processFn     func(context.Context, domain.ScheduleJob, int) error
}

func (e *fakeJobExecutor) markJobFailed(_ context.Context, jobID, message string) error {
	e.failedJobs++
	e.failedJobID = jobID
	e.failedMessage = message
	return nil
}

func (e *fakeJobExecutor) markJobRetry(_ context.Context, jobID, message string) error {
	e.retryJobs++
	e.retryJobID = jobID
	e.retryMessage = message
	return nil
}

func (e *fakeJobExecutor) processJobLocked(ctx context.Context, job domain.ScheduleJob, maxRetries int) error {
	e.lockedJobs++
	if e.processFn != nil {
		return e.processFn(ctx, job, maxRetries)
	}
	return nil
}

type singleLockProvider struct {
	lock     womslock.Lock
	attempts int
}

func (p *singleLockProvider) Acquire(context.Context, string, time.Duration) (womslock.Lock, error) {
	p.attempts++
	return p.lock, nil
}

type recordingLock struct {
	mu         sync.Mutex
	refreshes  int
	releases   int
	refreshErr error
}

func (l *recordingLock) Refresh(context.Context, time.Duration) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.refreshes++
	return l.refreshErr
}

func (l *recordingLock) Release(context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.releases++
	return nil
}

type fakeLockedJobStore struct {
	found          bool
	status         domain.ScheduleJobStatus
	attempt        int
	persistErr     error
	runningCalls   int
	persistCalls   int
	retryCalls     int
	retryMessage   string
	failedCalls    int
	failedMessage  string
	failedReason   string
	completedCalls int
}

func (s *fakeLockedJobStore) jobStatus(context.Context, string) (domain.ScheduleJobStatus, bool, error) {
	return s.status, s.found, nil
}

func (s *fakeLockedJobStore) markRunning(context.Context, string) (int, error) {
	s.runningCalls++
	s.status = domain.JobRunning
	s.attempt++
	return s.attempt, nil
}

func (s *fakeLockedJobStore) persist(context.Context, domain.ScheduleJob) error {
	s.persistCalls++
	return s.persistErr
}

func (s *fakeLockedJobStore) markRetryAfterRun(_ context.Context, _ string, message string) error {
	s.retryCalls++
	s.status = domain.JobQueued
	s.retryMessage = message
	return nil
}

func (s *fakeLockedJobStore) markFailedAfterRun(_ context.Context, _ string, message, reason string) error {
	s.failedCalls++
	s.status = domain.JobFailed
	s.failedMessage = message
	s.failedReason = reason
	return nil
}

func (s *fakeLockedJobStore) markCompleted(context.Context, domain.ScheduleJob) error {
	s.completedCalls++
	s.status = domain.JobCompleted
	return nil
}

func TestContainsAndTruncateDate(t *testing.T) {
	if !contains([]string{"a", "b", "c"}, "b") {
		t.Error("contains failed")
	}
	if contains([]string{"a", "b", "c"}, "d") {
		t.Error("contains should be false")
	}
	tm := time.Date(2026, 5, 30, 15, 30, 0, 0, time.UTC)
	truncated := truncateDate(tm)
	if truncated.Hour() != 0 || truncated.Minute() != 0 || truncated.Second() != 0 {
		t.Errorf("truncateDate failed: %v", truncated)
	}
}

func TestSqlLockedJobStore_JobStatus(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer tx.Rollback()

	store := sqlLockedJobStore{tx: tx}

	// Case 1: Job found
	mock.ExpectQuery("SELECT status FROM schedule_jobs WHERE id = \\$1 FOR UPDATE").
		WithArgs("job-1").
		WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow("queued"))

	status, found, err := store.jobStatus(context.Background(), "job-1")
	if err != nil || !found || status != domain.JobQueued {
		t.Errorf("jobStatus failed: status=%v found=%v err=%v", status, found, err)
	}

	// Case 2: Job not found
	mock.ExpectQuery("SELECT status FROM schedule_jobs WHERE id = \\$1 FOR UPDATE").
		WithArgs("job-missing").
		WillReturnError(sql.ErrNoRows)

	status, found, err = store.jobStatus(context.Background(), "job-missing")
	if err != nil || found {
		t.Errorf("jobStatus expected not found: found=%v err=%v", found, err)
	}

	// Case 3: Error
	mock.ExpectQuery("SELECT status FROM schedule_jobs").
		WillReturnError(errors.New("db error"))
	_, _, err = store.jobStatus(context.Background(), "job-err")
	if err == nil {
		t.Error("expected error")
	}
}

func TestSqlLockedJobStore_MarkRunning(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer tx.Rollback()

	store := sqlLockedJobStore{tx: tx}

	mock.ExpectQuery("UPDATE schedule_jobs SET status = 'running'").
		WithArgs("job-1").
		WillReturnRows(sqlmock.NewRows([]string{"attempt_count"}).AddRow(2))

	attempt, err := store.markRunning(context.Background(), "job-1")
	if err != nil || attempt != 2 {
		t.Errorf("markRunning failed: attempt=%d err=%v", attempt, err)
	}
}

func TestSqlLockedJobStore_MarkRetryAfterRun(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer tx.Rollback()

	store := sqlLockedJobStore{tx: tx}

	mock.ExpectExec("UPDATE schedule_jobs SET status = 'queued'").
		WithArgs("job-1", "some message").
		WillReturnResult(sqlmock.NewResult(1, 1))

	err = store.markRetryAfterRun(context.Background(), "job-1", "some message")
	if err != nil {
		t.Errorf("markRetryAfterRun failed: %v", err)
	}
}

func TestSqlLockedJobStore_MarkFailedAfterRun(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback()

	store := sqlLockedJobStore{tx: tx}

	mock.ExpectExec("UPDATE schedule_jobs SET status = 'failed'").
		WithArgs("job-1", "message").
		WillReturnResult(sqlmock.NewResult(1, 1))

	// Mock insertWorkerAuditTx (note: using pattern matched string for query)
	mock.ExpectExec("INSERT INTO audit_logs").
		WithArgs("job-1", "schedule.job.fail", "reason").
		WillReturnResult(sqlmock.NewResult(1, 1))

	err = store.markFailedAfterRun(context.Background(), "job-1", "message", "reason")
	if err != nil {
		t.Errorf("markFailedAfterRun failed: %v", err)
	}
}

func TestSqlLockedJobStore_MarkCompleted(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback()

	store := sqlLockedJobStore{tx: tx}

	job := domain.ScheduleJob{
		ID:        "job-1",
		PreviewID: "preview-1",
	}

	mock.ExpectExec("UPDATE schedule_jobs SET status = 'completed'").
		WithArgs("job-1").
		WillReturnResult(sqlmock.NewResult(1, 1))

	// Mock insertWorkerAuditTx
	mock.ExpectExec("INSERT INTO audit_logs").
		WithArgs("job-1", "schedule.job.complete", "排程任務已完成。").
		WillReturnResult(sqlmock.NewResult(1, 1))

	mock.ExpectExec("DELETE FROM schedule_previews WHERE id = \\$1").
		WithArgs("preview-1").
		WillReturnResult(sqlmock.NewResult(1, 1))

	err = store.markCompleted(context.Background(), job)
	if err != nil {
		t.Errorf("markCompleted failed: %v", err)
	}
}

func TestPersistLineSchedule(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback()

	// Query orders
	mock.ExpectQuery("SELECT id, quantity, priority FROM orders").
		WithArgs("A").
		WillReturnRows(sqlmock.NewRows([]string{"id", "quantity", "priority"}).
			AddRow("ORD-1", 500, "high").
			AddRow("ORD-2", 600, "low"))

	// Query line capacity & revision
	mock.ExpectQuery("SELECT capacity_per_day, schedule_revision FROM production_lines").
		WithArgs("A").
		WillReturnRows(sqlmock.NewRows([]string{"capacity_per_day", "schedule_revision"}).
			AddRow(1000, int64(10)))

	// Expect allocations inserts
	// For ORD-1: quantity 500 < capacity 1000, so scheduled on today
	mock.ExpectExec("INSERT INTO schedule_allocations").
		WithArgs("ORD-1", "A", sqlmock.AnyArg(), 500, "high").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("UPDATE orders SET status = '已排程'").
		WithArgs("ORD-1").
		WillReturnResult(sqlmock.NewResult(1, 1))

	// For ORD-2: quantity 500+600 = 1100 > capacity 1000, so scheduled on next day
	mock.ExpectExec("INSERT INTO schedule_allocations").
		WithArgs("ORD-2", "A", sqlmock.AnyArg(), 600, "low").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("UPDATE orders SET status = '已排程'").
		WithArgs("ORD-2").
		WillReturnResult(sqlmock.NewResult(1, 1))

	// Update production line revision
	mock.ExpectExec("UPDATE production_lines SET schedule_revision = schedule_revision \\+ 1").
		WithArgs("A").
		WillReturnResult(sqlmock.NewResult(1, 1))

	job := domain.ScheduleJob{
		ID:           "job-1",
		LineID:       "A",
		LineRevision: 10,
	}

	err = persistLineSchedule(context.Background(), tx, job)
	if err != nil {
		t.Fatalf("persistLineSchedule failed: %v", err)
	}

	// Test stale line revision error
	mock.ExpectQuery("SELECT id, quantity, priority FROM orders").
		WithArgs("A").
		WillReturnRows(sqlmock.NewRows([]string{"id", "quantity", "priority"}).AddRow("ORD-1", 500, "high"))

	mock.ExpectQuery("SELECT capacity_per_day, schedule_revision FROM production_lines").
		WithArgs("A").
		WillReturnRows(sqlmock.NewRows([]string{"capacity_per_day", "schedule_revision"}).
			AddRow(1000, int64(11))) // diff from job.LineRevision (10)

	job.LineRevision = 10
	err = persistLineSchedule(context.Background(), tx, job)
	if _, ok := err.(errStaleScheduleData); !ok {
		t.Errorf("expected errStaleScheduleData, got %v", err)
	}
}

func TestPersistPreviewAllocations(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()
	mock.MatchExpectationsInOrder(false)

	mock.ExpectBegin()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback()

	allocs := []map[string]any{
		{
			"orderId":       "ORD-1-1",
			"lineId":        "A",
			"date":          "2026-05-30T00:00:00Z",
			"quantity":      300,
			"priority":      "high",
			"locked":        false,
			"sourceOrderId": "ORD-1",
		},
		{
			"orderId":       "ORD-1",
			"lineId":        "A",
			"date":          "2026-05-30T00:00:00Z",
			"quantity":      200,
			"priority":      "high",
			"locked":        false,
			"sourceOrderId": "",
		},
	}
	allocsJSON, _ := json.Marshal(allocs)

	mock.ExpectQuery("SELECT line_revision, allocations FROM schedule_previews").
		WithArgs("preview-1", "A").
		WillReturnRows(sqlmock.NewRows([]string{"line_revision", "allocations"}).
			AddRow(int64(5), allocsJSON))

	mock.ExpectQuery("SELECT schedule_revision FROM production_lines").
		WithArgs("A").
		WillReturnRows(sqlmock.NewRows([]string{"schedule_revision"}).AddRow(int64(5)))

	// splitAllocations handling
	mock.ExpectExec("UPDATE orders SET quantity = \\$2").
		WithArgs("ORD-1", 200).
		WillReturnResult(sqlmock.NewResult(1, 1))

	mock.ExpectExec("INSERT INTO orders").
		WithArgs("ORD-1-1", 300, "ORD-1").
		WillReturnResult(sqlmock.NewResult(1, 1))

	// delete old allocations
	mock.ExpectExec("DELETE FROM schedule_allocations").
		WithArgs("ORD-1-1").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("DELETE FROM schedule_allocations").
		WithArgs("ORD-1").
		WillReturnResult(sqlmock.NewResult(1, 1))

	// insert new allocations
	mock.ExpectExec("INSERT INTO schedule_allocations").
		WithArgs("ORD-1-1", "A", sqlmock.AnyArg(), 300, "high", false).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("INSERT INTO schedule_allocations").
		WithArgs("ORD-1", "A", sqlmock.AnyArg(), 200, "high", false).
		WillReturnResult(sqlmock.NewResult(1, 1))

	// update order status
	mock.ExpectExec("UPDATE orders SET status = '已排程'").
		WithArgs("ORD-1-1").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("UPDATE orders SET status = '已排程'").
		WithArgs("ORD-1").
		WillReturnResult(sqlmock.NewResult(1, 1))

	// update production line revision
	mock.ExpectExec("UPDATE production_lines SET schedule_revision = schedule_revision \\+ 1").
		WithArgs("A").
		WillReturnResult(sqlmock.NewResult(1, 1))

	job := domain.ScheduleJob{
		ID:           "job-1",
		LineID:       "A",
		PreviewID:    "preview-1",
		LineRevision: 5,
	}

	err = persistPreviewAllocations(context.Background(), tx, job)
	if err != nil {
		t.Fatalf("persistPreviewAllocations failed: %v", err)
	}
}

func TestBackfillQueuedJobs(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	// Mock backfillQueuedJobs queries
	job := domain.ScheduleJob{
		ID:     "job-1",
		LineID: "A",
	}
	jobJSON, _ := json.Marshal(job)

	// Since we run batch queries, first query returns rows
	mock.ExpectQuery("SELECT id, line_id, COALESCE").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "line_id", "source", "preview_id", "request_hash", "line_revision", "order_ids", "created_at", "updated_at",
		}).AddRow("job-1", "A", "", "", "", int64(0), jobJSON, time.Now(), time.Now()))

	// Executed job needs to update status of the job in DB
	// Inside processDBJob -> processJobLocked -> sqlLockedJobStore methods.
	// But wait, processDBJob calls processJobPayload with sqlScheduleJobExecutor.
	// That will do acquireLineLock. Since lockProvider is nil, it will call markJobFailed.
	// Let's mock markJobFailed: UPDATE schedule_jobs SET status = 'failed'
	mock.ExpectExec("UPDATE schedule_jobs SET status = 'failed'").
		WithArgs("job-1", "Redis 排程鎖未設定。").
		WillReturnResult(sqlmock.NewResult(1, 1))

	// The query will loop until batchCount < batchSize (100).
	// So second query should return 0 rows to terminate the loop.
	mock.ExpectQuery("SELECT id, line_id, COALESCE").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "line_id", "source", "preview_id", "request_hash", "line_revision", "order_ids", "created_at", "updated_at",
		}))

	err = backfillQueuedJobs(context.Background(), db, nil, 3, time.Second, time.Second, time.Second)
	if err != nil {
		t.Fatalf("backfillQueuedJobs failed: %v", err)
	}
}

func TestProcessDBJob(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	payload, _ := json.Marshal(domain.ScheduleJob{ID: "job-1", LineID: "A"})

	mock.ExpectExec("UPDATE schedule_jobs SET status = 'failed'").
		WithArgs("job-1", "Redis 排程鎖未設定。").
		WillReturnResult(sqlmock.NewResult(1, 1))

	err = processDBJob(context.Background(), db, nil, payload, workerJobConfig{maxRetries: 3, lockTTL: time.Second, lockRenewInterval: time.Second, lockTimeout: time.Second})
	if err != nil {
		t.Fatalf("processDBJob failed: %v", err)
	}
}

func TestSqlScheduleJobExecutor_DelegatesAndDBLocked(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	executor := sqlScheduleJobExecutor{db: db}

	// 1. Test markJobRetry
	mock.ExpectExec("UPDATE schedule_jobs SET status = 'queued'").
		WithArgs("job-1", "retry msg").
		WillReturnResult(sqlmock.NewResult(1, 1))

	err = executor.markJobRetry(context.Background(), "job-1", "retry msg")
	if err != nil {
		t.Errorf("markJobRetry delegate failed: %v", err)
	}

	// 2. Test processJobLocked / processDBJobLocked
	mock.ExpectBegin()
	// Inside runLockedJobState -> store.jobStatus: mock it
	mock.ExpectQuery("SELECT status FROM schedule_jobs").
		WithArgs("job-1").
		WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow("queued"))
	// markRunning
	mock.ExpectQuery("UPDATE schedule_jobs SET status = 'running'").
		WithArgs("job-1").
		WillReturnRows(sqlmock.NewRows([]string{"attempt_count"}).AddRow(1))
	// persist -> persistLineSchedule (since PreviewID is empty) -> QueryContext orders
	mock.ExpectQuery("SELECT id, quantity, priority FROM orders").
		WithArgs("A").
		WillReturnRows(sqlmock.NewRows([]string{"id", "quantity", "priority"})) // empty orders to return nil fast
	// markCompleted
	mock.ExpectExec("UPDATE schedule_jobs SET status = 'completed'").
		WithArgs("job-1").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("INSERT INTO audit_logs").
		WithArgs("job-1", "schedule.job.complete", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))

	mock.ExpectCommit()

	job := domain.ScheduleJob{
		ID:     "job-1",
		LineID: "A",
	}
	err = executor.processJobLocked(context.Background(), job, 3)
	if err != nil {
		t.Errorf("processJobLocked delegate failed: %v", err)
	}
}

func TestSqlLockedJobStore_PersistBranch(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback()

	store := sqlLockedJobStore{tx: tx}

	// 1. job.Source == "hpa-peak-demo"
	mock.ExpectQuery("SELECT id, quantity, priority FROM orders").
		WithArgs("A").
		WillReturnRows(sqlmock.NewRows([]string{"id", "quantity", "priority"}))

	job1 := domain.ScheduleJob{
		LineID: "A",
		Source: "hpa-peak-demo",
	}
	err = store.persist(context.Background(), job1)
	if err != nil {
		t.Errorf("persist hpa-peak-demo failed: %v", err)
	}

	// 2. job.PreviewID != ""
	// Query schedule_previews: return stale schedule data error to keep it simple and terminate fast
	mock.ExpectQuery("SELECT line_revision, allocations FROM schedule_previews").
		WithArgs("preview-1", "A").
		WillReturnError(sql.ErrNoRows)

	job2 := domain.ScheduleJob{
		LineID:    "A",
		PreviewID: "preview-1",
	}
	err = store.persist(context.Background(), job2)
	if _, ok := err.(errStaleScheduleData); !ok {
		t.Errorf("expected errStaleScheduleData, got %v", err)
	}
}

func TestMainExitsOnInvalidConfig(t *testing.T) {
	if os.Getenv("BE_CRASHY_WORKER") == "1" {
		t.Setenv("WORKER_START_OFFSET", "invalid_offset")
		main()
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=TestMainExitsOnInvalidConfig")
	cmd.Env = append(os.Environ(), "BE_CRASHY_WORKER=1")
	err := cmd.Run()
	if err == nil {
		t.Fatalf("expected command to exit with error")
	}
}

func TestMainExitsOnInvalidRedisLockConfig(t *testing.T) {
	if os.Getenv("BE_CRASHY_WORKER") == "2" {
		t.Setenv("DATABASE_URL", "postgres://example")
		t.Setenv("WORKER_LOCK_TTL_MS", "0") // invalid lock config (<=0)
		main()
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=TestMainExitsOnInvalidRedisLockConfig")
	cmd.Env = append(os.Environ(), "BE_CRASHY_WORKER=2")
	err := cmd.Run()
	if err == nil {
		t.Fatalf("expected command to exit with error")
	}
}

func TestMainExitsOnInvalidBackfillInterval(t *testing.T) {
	if os.Getenv("BE_CRASHY_WORKER") == "3" {
		t.Setenv("DATABASE_URL", "postgres://example")
		t.Setenv("WORKER_BACKFILL_INTERVAL_MS", "0")
		main()
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=TestMainExitsOnInvalidBackfillInterval")
	cmd.Env = append(os.Environ(), "BE_CRASHY_WORKER=3")
	err := cmd.Run()
	if err == nil {
		t.Fatalf("expected command to exit with error")
	}
}

func TestMainExitsOnMissingRedisAddr(t *testing.T) {
	if os.Getenv("BE_CRASHY_WORKER") == "4" {
		t.Setenv("DATABASE_URL", "postgres://example")
		t.Setenv("WORKER_BACKFILL_INTERVAL_MS", "5000")
		t.Setenv("REDIS_ADDR", "")
		main()
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=TestMainExitsOnMissingRedisAddr")
	cmd.Env = append(os.Environ(), "BE_CRASHY_WORKER=4")
	err := cmd.Run()
	if err == nil {
		t.Fatalf("expected command to exit with error")
	}
}

func TestMainExitsOnRedisLockPingFailure(t *testing.T) {
	if os.Getenv("BE_CRASHY_WORKER") == "5" {
		t.Setenv("DATABASE_URL", "postgres://example")
		t.Setenv("WORKER_BACKFILL_INTERVAL_MS", "5000")
		t.Setenv("REDIS_ADDR", "127.0.0.1:9999") // invalid redis addr to cause timeout
		t.Setenv("WORKER_DEPENDENCY_RETRY_TIMEOUT_MS", "1")
		t.Setenv("WORKER_DEPENDENCY_RETRY_INTERVAL_MS", "1")
		main()
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=TestMainExitsOnRedisLockPingFailure")
	cmd.Env = append(os.Environ(), "BE_CRASHY_WORKER=5")
	err := cmd.Run()
	if err == nil {
		t.Fatalf("expected command to exit with error")
	}
}

func TestAllErrorPaths_ProcessDBJobLocked(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()
	mock.MatchExpectationsInOrder(false)

	// 1. processDBJobLocked BeginTx failure
	mock.ExpectBegin().WillReturnError(errors.New("begin error"))
	err = processDBJobLocked(context.Background(), db, domain.ScheduleJob{ID: "job-1"}, 3)
	if err == nil || err.Error() != "begin error" {
		t.Errorf("expected begin error, got %v", err)
	}

	// 2. processDBJobLocked Commit failure
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT status FROM schedule_jobs").
		WithArgs("job-1").
		WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow("cancelled"))
	mock.ExpectCommit().WillReturnError(errors.New("commit error"))
	err = processDBJobLocked(context.Background(), db, domain.ScheduleJob{ID: "job-1"}, 3)
	if err == nil || err.Error() != "commit error" {
		t.Errorf("expected commit error, got %v", err)
	}
}

func TestAllErrorPaths_SqlLockedJobStore(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()
	mock.MatchExpectationsInOrder(false)

	mock.ExpectBegin()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback()

	store := sqlLockedJobStore{tx: tx}

	// 3. markFailedAfterRun ExecContext failure
	mock.ExpectExec("UPDATE schedule_jobs SET status = 'failed'").
		WillReturnError(errors.New("exec error"))
	err = store.markFailedAfterRun(context.Background(), "job-1", "message", "reason")
	if err == nil || err.Error() != "exec error" {
		t.Errorf("expected exec error, got %v", err)
	}

	// 4. markCompleted UPDATE failure
	mock.ExpectExec("UPDATE schedule_jobs SET status = 'completed'").
		WillReturnError(errors.New("update error"))
	err = store.markCompleted(context.Background(), domain.ScheduleJob{ID: "job-1"})
	if err == nil || err.Error() != "update error" {
		t.Errorf("expected update error, got %v", err)
	}

	// 5. markCompleted audit log failure
	mock.ExpectExec("UPDATE schedule_jobs SET status = 'completed'").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("INSERT INTO audit_logs").
		WillReturnError(errors.New("audit error"))
	err = store.markCompleted(context.Background(), domain.ScheduleJob{ID: "job-1"})
	if err == nil || err.Error() != "audit error" {
		t.Errorf("expected audit error, got %v", err)
	}

	// 6. markCompleted DELETE schedule_previews failure
	mock.ExpectExec("UPDATE schedule_jobs SET status = 'completed'").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("INSERT INTO audit_logs").
		WithArgs("job-1", "schedule.job.complete", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("DELETE FROM schedule_previews WHERE id = \\$1").
		WillReturnError(errors.New("delete error"))
	err = store.markCompleted(context.Background(), domain.ScheduleJob{ID: "job-1", PreviewID: "preview-1"})
	if err == nil || err.Error() != "delete error" {
		t.Errorf("expected delete error, got %v", err)
	}
}

func TestAllErrorPaths_PersistPreviewAllocations(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()
	mock.MatchExpectationsInOrder(false)

	mock.ExpectBegin()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback()

	// 7. persistPreviewAllocations other query error
	mock.ExpectQuery("SELECT line_revision, allocations FROM schedule_previews").
		WillReturnError(errors.New("previews error"))
	err = persistPreviewAllocations(context.Background(), tx, domain.ScheduleJob{ID: "job-1", PreviewID: "preview-1", LineID: "A"})
	if err == nil || err.Error() != "previews error" {
		t.Errorf("expected previews error, got %v", err)
	}

	// 8. persistPreviewAllocations production line query error
	mock.ExpectQuery("SELECT line_revision, allocations FROM schedule_previews").
		WillReturnRows(sqlmock.NewRows([]string{"line_revision", "allocations"}).AddRow(int64(5), []byte(`[]`)))
	mock.ExpectQuery("SELECT schedule_revision FROM production_lines").
		WillReturnError(errors.New("revision error"))
	err = persistPreviewAllocations(context.Background(), tx, domain.ScheduleJob{ID: "job-1", PreviewID: "preview-1", LineID: "A"})
	if err == nil || err.Error() != "revision error" {
		t.Errorf("expected revision error, got %v", err)
	}

	// 9. persistPreviewAllocations json.Unmarshal error
	mock.ExpectQuery("SELECT line_revision, allocations FROM schedule_previews").
		WillReturnRows(sqlmock.NewRows([]string{"line_revision", "allocations"}).AddRow(int64(5), []byte(`invalid json`)))
	mock.ExpectQuery("SELECT schedule_revision FROM production_lines").
		WillReturnRows(sqlmock.NewRows([]string{"schedule_revision"}).AddRow(int64(5)))
	err = persistPreviewAllocations(context.Background(), tx, domain.ScheduleJob{ID: "job-1", PreviewID: "preview-1", LineID: "A", LineRevision: 5})
	if err == nil || !strings.Contains(err.Error(), "invalid character") {
		t.Errorf("expected json unmarshal error, got %v", err)
	}

	// 10. persistPreviewAllocations line ID mismatch
	mismatchAllocs := []map[string]any{{"orderId": "ORD-1", "lineId": "B"}}
	mismatchJSON, _ := json.Marshal(mismatchAllocs)
	mock.ExpectQuery("SELECT line_revision, allocations FROM schedule_previews").
		WillReturnRows(sqlmock.NewRows([]string{"line_revision", "allocations"}).AddRow(int64(5), mismatchJSON))
	mock.ExpectQuery("SELECT schedule_revision FROM production_lines").
		WillReturnRows(sqlmock.NewRows([]string{"schedule_revision"}).AddRow(int64(5)))
	err = persistPreviewAllocations(context.Background(), tx, domain.ScheduleJob{ID: "job-1", PreviewID: "preview-1", LineID: "A", LineRevision: 5})
	if _, ok := err.(errStaleScheduleData); !ok {
		t.Errorf("expected errStaleScheduleData on line mismatch, got %v", err)
	}
}

func TestAllErrorPaths_PersistLineSchedule(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()
	mock.MatchExpectationsInOrder(false)

	mock.ExpectBegin()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback()

	// 11. persistLineSchedule query orders error
	mock.ExpectQuery("SELECT id, quantity, priority FROM orders").
		WillReturnError(errors.New("orders error"))
	err = persistLineSchedule(context.Background(), tx, domain.ScheduleJob{LineID: "A"})
	if err == nil || err.Error() != "orders error" {
		t.Errorf("expected orders error, got %v", err)
	}

	// 12. persistLineSchedule query production lines error
	mock.ExpectQuery("SELECT id, quantity, priority FROM orders").
		WithArgs("A").
		WillReturnRows(sqlmock.NewRows([]string{"id", "quantity", "priority"}).AddRow("ORD-1", 500, "high"))
	mock.ExpectQuery("SELECT capacity_per_day, schedule_revision FROM production_lines").
		WithArgs("A").
		WillReturnError(errors.New("lines error"))
	err = persistLineSchedule(context.Background(), tx, domain.ScheduleJob{LineID: "A"})
	if err == nil || err.Error() != "lines error" {
		t.Errorf("expected lines error, got %v", err)
	}
}

func TestAllErrorPaths_BackfillQueuedJobs(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()
	mock.MatchExpectationsInOrder(false)

	// 13. backfillQueuedJobs db query error
	mock.ExpectQuery("SELECT id, line_id, COALESCE").
		WillReturnError(errors.New("query error"))
	err = backfillQueuedJobs(context.Background(), db, nil, 3, time.Second, time.Second, time.Second)
	if err == nil || err.Error() != "query error" {
		t.Errorf("expected backfill query error, got %v", err)
	}

	// 14. backfillQueuedJobs rows Scan error
	mock.ExpectQuery("SELECT id, line_id, COALESCE").
		WillReturnRows(sqlmock.NewRows([]string{"id", "line_id"}).AddRow("job-1", nil))
	err = backfillQueuedJobs(context.Background(), db, nil, 3, time.Second, time.Second, time.Second)
	if err == nil {
		t.Error("expected scan error")
	}
}

type errorJobExecutor struct {
	fakeJobExecutor
	err error
}

func (e *errorJobExecutor) markJobFailed(ctx context.Context, jobID, message string) error {
	return e.err
}

func (e *errorJobExecutor) markJobRetry(ctx context.Context, jobID, message string) error {
	return e.err
}

func TestProcessJobPayloadErrorPropagation(t *testing.T) {
	// 1. markJobFailed error propagation when lockProvider is nil
	executor := errorJobExecutor{err: errors.New("failed to mark failed")}
	payload := mustMarshalJob(t, domain.ScheduleJob{ID: "JOB-1", LineID: "A"})
	err := processJobPayload(context.Background(), &executor, nil, payload, workerJobConfig{maxRetries: 3, lockTTL: time.Second, lockTimeout: time.Second})
	if err == nil || err.Error() != "failed to mark failed" {
		t.Errorf("expected error, got %v", err)
	}

	// 2. markJobRetry error propagation when lock acquisition times out
	provider := &retryLockProvider{failures: 100}
	err = processJobPayload(context.Background(), &executor, provider, payload, workerJobConfig{maxRetries: 3, lockTTL: time.Second, lockTimeout: time.Nanosecond})
	if err == nil || err.Error() != "failed to mark failed" {
		t.Errorf("expected error, got %v", err)
	}
}
