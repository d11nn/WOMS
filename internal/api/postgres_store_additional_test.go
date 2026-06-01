package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/d11nn/woms/internal/auth"
	"github.com/d11nn/woms/internal/domain"
	"github.com/d11nn/woms/internal/scheduler"
)

func mockHPAPeakSummaryDB(mock sqlmock.Sqlmock) {
	mock.ExpectQuery("SELECT COUNT\\(DISTINCT line_id\\)").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(4))
	mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM orders").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(20))
	mock.ExpectQuery("SELECT status, COUNT\\(\\*\\) FROM schedule_jobs").
		WillReturnRows(sqlmock.NewRows([]string{"status", "count"}).AddRow("queued", 8))
	mock.ExpectQuery("SELECT id, line_id, status").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "line_id", "status", "message", "source", "preview_id", "request_hash", "line_revision", "attempt_count", "order_ids", "created_at", "updated_at",
		}).AddRow("JOB-1", "L001", "queued", "", "hpa-peak-demo", "", "", int64(0), 0, []byte(`[]`), time.Now(), time.Now()))
}

func TestPostgresStore_ResubmitOrder(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()
	mock.MatchExpectationsInOrder(false)

	store := &PostgresStore{
		MemoryStore: NewMemoryStore(),
		db:          db,
	}

	claims := auth.Claims{
		Subject: "sales-1",
		Role:    domain.RoleSales,
	}

	tNow := time.Now().UTC()

	// 1. Success case: note empty, quantity modified, due date modified
	rows := sqlmock.NewRows([]string{
		"id", "customer", "line_id", "quantity", "priority", "status", "due_date", "note", "created_by",
		"source_order", "rejection_reason", "rejected_by", "rejected_at", "created_at", "updated_at",
	}).AddRow("ORD-1", "ACME", "line-a", 100, string(domain.PriorityLow), string(domain.StatusRejected), tNow, "", "sales-1", "", "too slow", "sched", &tNow, tNow, tNow)

	mock.ExpectQuery("SELECT id, customer, line_id.* FROM orders WHERE id = \\$1").
		WithArgs("ORD-1").
		WillReturnRows(rows)

	// productionLine query
	mock.ExpectQuery("SELECT id, name, capacity_per_day").
		WithArgs("line-a", "Asia/Taipei").
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "capacity_per_day", "timezone", "schedule_revision"}).
			AddRow("line-a", "Line A", 1000, "Asia/Taipei", 1))

	// Begin, Exec, Commit
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE orders SET").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("UPDATE production_lines").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("INSERT INTO audit_logs").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	req := resubmitOrderRequest{
		OrderID:  "ORD-1",
		Quantity: 50,
		DueDate:  time.Now().AddDate(0, 0, 5).Format("2006-01-02"),
	}

	order, err := store.ResubmitOrder(req, claims)
	if err != nil {
		t.Fatalf("ResubmitOrder failed: %v", err)
	}
	if order.Status != domain.StatusPending || order.Quantity != 50 {
		t.Errorf("unexpected order: %+v", order)
	}

	// 2. Reject note modification
	reqWithNote := resubmitOrderRequest{
		OrderID: "ORD-1",
		Note:    "new note",
	}
	// Mock select order again
	rows2 := sqlmock.NewRows([]string{
		"id", "customer", "line_id", "quantity", "priority", "status", "due_date", "note", "created_by",
		"source_order", "rejection_reason", "rejected_by", "rejected_at", "created_at", "updated_at",
	}).AddRow("ORD-1", "ACME", "line-a", 100, string(domain.PriorityLow), string(domain.StatusRejected), tNow, "", "sales-1", "", "too slow", "sched", &tNow, tNow, tNow)

	mock.ExpectQuery("SELECT id, customer, line_id.* FROM orders WHERE id = \\$1").
		WithArgs("ORD-1").
		WillReturnRows(rows2)

	_, err = store.ResubmitOrder(reqWithNote, claims)
	if err == nil || err.Error() != "note cannot be updated after order creation" {
		t.Errorf("expected note error, got %v", err)
	}
}

func TestPostgresStore_CreateDemoConflictOrders(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()
	mock.MatchExpectationsInOrder(false)

	store := &PostgresStore{
		MemoryStore: NewMemoryStore(),
		db:          db,
	}

	claims := auth.Claims{
		Subject: "admin-1",
		Role:    domain.RoleAdmin,
	}

	mock.ExpectQuery("SELECT id, name, capacity_per_day").
		WithArgs("line-a", "Asia/Taipei").
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "capacity_per_day", "timezone", "schedule_revision"}).
			AddRow("line-a", "Line A", 1000, "Asia/Taipei", 1))

	mock.ExpectBegin()
	for i := 1; i <= 5; i++ {
		mock.ExpectExec("INSERT INTO orders").WillReturnResult(sqlmock.NewResult(1, 1))
		mock.ExpectExec("INSERT INTO audit_logs").WillReturnResult(sqlmock.NewResult(1, 1))
	}
	mock.ExpectExec("UPDATE production_lines").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	req := demoConflictRequest{
		LineID:  "line-a",
		Count:   5,
		DueDate: time.Now().AddDate(0, 0, 2).Format("2006-01-02"),
	}

	orders, err := store.CreateDemoConflictOrders(req, claims)
	if err != nil {
		t.Fatalf("CreateDemoConflictOrders failed: %v", err)
	}
	if len(orders) != 5 {
		t.Errorf("expected 5 orders, got %d", len(orders))
	}
}

func TestPostgresStore_HPADemoAdditional(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()
	mock.MatchExpectationsInOrder(false)

	store := &PostgresStore{
		MemoryStore: NewMemoryStore(),
		db:          db,
	}

	claims := auth.Claims{
		Subject: "admin-1",
		Role:    domain.RoleAdmin,
	}

	// 1. CreateHPAPeakDemo
	mock.ExpectBegin()
	// resetHPAPeakDemoDB executes
	mock.ExpectExec("UPDATE schedule_jobs SET status = 'cancelled'").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("DELETE FROM schedule_jobs").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("DELETE FROM schedule_allocations").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("DELETE FROM orders").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("DELETE FROM audit_logs").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("DELETE FROM production_lines WHERE id").WillReturnResult(sqlmock.NewResult(1, 1))

	// Loop inserts production lines, orders, schedule jobs
	for lineIndex := 1; lineIndex <= 200; lineIndex++ {
		mock.ExpectExec("INSERT INTO production_lines").WillReturnResult(sqlmock.NewResult(1, 1))
		for orderIndex := 1; orderIndex <= 5; orderIndex++ {
			mock.ExpectExec("INSERT INTO orders").WillReturnResult(sqlmock.NewResult(1, 1))
		}
		for jobIndex := 1; jobIndex <= 2; jobIndex++ {
			mock.ExpectExec("INSERT INTO schedule_jobs").WillReturnResult(sqlmock.NewResult(1, 1))
			mock.ExpectExec("INSERT INTO audit_logs").WillReturnResult(sqlmock.NewResult(1, 1))
		}
	}
	mock.ExpectCommit()

	mockHPAPeakSummaryDB(mock)

	_, err = store.CreateHPAPeakDemo(claims)
	if err != nil {
		t.Fatalf("CreateHPAPeakDemo failed: %v", err)
	}

	// 2. ClearHPAPeakDemo
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE schedule_jobs").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("DELETE FROM schedule_jobs").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("DELETE FROM schedule_allocations").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("DELETE FROM orders").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("DELETE FROM audit_logs").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	mockHPAPeakSummaryDB(mock)

	_, err = store.ClearHPAPeakDemo(claims)
	if err != nil {
		t.Fatalf("ClearHPAPeakDemo failed: %v", err)
	}

	// 3. HPAPeakJobs
	mock.ExpectQuery("SELECT id, line_id, status").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "line_id", "status", "message", "source", "preview_id", "request_hash", "line_revision", "attempt_count", "order_ids", "created_at", "updated_at",
		}).AddRow("JOB-1", "L001", "queued", "", "hpa-peak-demo", "", "", int64(0), 0, []byte(`[]`), time.Now(), time.Now()))

	jobs := store.HPAPeakJobs()
	if len(jobs) != 1 || jobs[0].ID != "JOB-1" {
		t.Errorf("unexpected jobs: %+v", jobs)
	}
}

func TestPostgresStore_JobsAndPreviews(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()
	mock.MatchExpectationsInOrder(false)

	store := &PostgresStore{
		MemoryStore: NewMemoryStore(),
		db:          db,
	}

	claims := auth.Claims{
		Subject: "admin-1",
		Role:    domain.RoleAdmin,
		LineID:  "A",
	}

	// 1. CreateScheduleJob with preview using line A (exists in memory fallback lines)
	nowStr := time.Now().Format("2006-01-02")
	req := scheduleRequest{
		LineID:      "A",
		OrderIDs:    []string{"ORD-1"},
		Reason:      "test",
		StartDate:   nowStr,
		CurrentDate: nowStr,
		PreviewID:   "preview-1",
	}

	previewReq := req
	previewReq.PreviewID = ""

	// Pre-populate memory store preview to pass validation
	store.MemoryStore.mu.Lock()
	store.MemoryStore.previews["preview-1"] = previewRecord{
		ActorID:   "admin-1",
		ActorRole: domain.RoleAdmin,
		LineID:    "A",
		Request:   previewReq,
	}
	store.MemoryStore.mu.Unlock()

	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO schedule_jobs").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("INSERT INTO audit_logs").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	job, err := store.CreateScheduleJob(req, claims)
	if err != nil {
		t.Fatalf("CreateScheduleJob failed: %v", err)
	}
	if job.LineID != "A" {
		t.Errorf("unexpected job: %+v", job)
	}

	// 2. GetScheduleJob
	mock.ExpectQuery("SELECT id, line_id, status").
		WithArgs(job.ID).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "line_id", "status", "message", "source", "preview_id", "request_hash", "line_revision", "attempt_count", "order_ids", "created_at", "updated_at",
		}).AddRow(job.ID, "A", "queued", "", "", "", "", int64(0), 0, []byte(`[]`), time.Now(), time.Now()))

	retrieved, found := store.GetScheduleJob(job.ID)
	if !found || retrieved.ID != job.ID {
		t.Errorf("GetScheduleJob failed")
	}

	// 3. DeleteQueuedScheduleJob
	mock.ExpectExec("DELETE FROM schedule_jobs").
		WithArgs("JOB-1").
		WillReturnResult(sqlmock.NewResult(1, 1))
	store.DeleteQueuedScheduleJob("JOB-1")

	// 4. ExecuteScheduleJob
	mock.ExpectExec("INSERT INTO schedule_jobs").WillReturnResult(sqlmock.NewResult(1, 1))
	store.ExecuteScheduleJob(job.ID)
}

func TestPostgresStore_ConfirmPreviewOrder(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()
	mock.MatchExpectationsInOrder(false)

	store := &PostgresStore{
		MemoryStore: NewMemoryStore(),
		db:          db,
	}

	claims := auth.Claims{
		Subject: "sales-1",
		Role:    domain.RoleSales,
	}

	draft := createOrderRequest{
		Customer: "ACME",
		LineID:   "A",
		Quantity: 500,
		Priority: domain.PriorityLow,
		DueDate:  "2026-06-01",
	}
	draftJSON, _ := json.Marshal(draft)

	mock.ExpectQuery("SELECT actor_id, actor_role, line_id, allocations, conflicts, draft_order FROM schedule_previews").
		WithArgs("preview-1").
		WillReturnRows(sqlmock.NewRows([]string{"actor_id", "actor_role", "line_id", "allocations", "conflicts", "draft_order"}).
			AddRow("sales-1", string(domain.RoleSales), "A", []byte("[]"), []byte("[]"), sql.NullString{String: string(draftJSON), Valid: true}))

	// CreateOrder calls
	mock.ExpectQuery("SELECT id, name, capacity_per_day").
		WithArgs("A", "Asia/Taipei").
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "capacity_per_day", "timezone", "schedule_revision"}).
			AddRow("A", "Line A", 1000, "Asia/Taipei", 1))

	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO orders").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("INSERT INTO audit_logs").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("UPDATE production_lines").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("DELETE FROM schedule_previews").
		WithArgs("preview-1", "sales-1", domain.RoleSales).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	order, err := store.ConfirmPreviewOrder(confirmPreviewRequest{PreviewID: "preview-1"}, claims)
	if err != nil {
		t.Fatalf("ConfirmPreviewOrder failed: %v", err)
	}
	if order.Customer != "ACME" {
		t.Errorf("unexpected order: %+v", order)
	}
}

func TestPostgresStore_ConfirmPreviewOrderValidationBranches(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()
	mock.MatchExpectationsInOrder(false)
	store := &PostgresStore{MemoryStore: NewMemoryStore(), db: db}
	claims := auth.Claims{Subject: "sales-1", Role: domain.RoleSales}
	validDraft := `{"customer":"ACME","lineId":"A","quantity":100,"priority":"low","dueDate":"2026-06-03"}`

	for _, tt := range []struct {
		name             string
		row              *sqlmock.Rows
		err              error
		wantError        string
		deferDraft       bool
		deferredOrderIDs []string
	}{
		{name: "preview expired", err: sql.ErrNoRows, wantError: "preview result expired or not found"},
		{name: "database error", err: errors.New("preview db error"), wantError: "preview db error"},
		{name: "other actor", row: sqlmock.NewRows([]string{"actor_id", "actor_role", "line_id", "allocations", "conflicts", "draft_order"}).
			AddRow("other-sales", string(domain.RoleSales), "A", []byte("[]"), []byte("[]"), sql.NullString{String: validDraft, Valid: true}), wantError: "preview result belongs to another user"},
		{name: "missing draft", row: sqlmock.NewRows([]string{"actor_id", "actor_role", "line_id", "allocations", "conflicts", "draft_order"}).
			AddRow("sales-1", string(domain.RoleSales), "A", []byte("[]"), []byte("[]"), sql.NullString{}), wantError: "preview does not contain a draft order"},
		{name: "invalid draft json", row: sqlmock.NewRows([]string{"actor_id", "actor_role", "line_id", "allocations", "conflicts", "draft_order"}).
			AddRow("sales-1", string(domain.RoleSales), "A", []byte("[]"), []byte("[]"), sql.NullString{String: "{", Valid: true}), wantError: "unexpected end of JSON input"},
		{name: "invalid allocations json", row: sqlmock.NewRows([]string{"actor_id", "actor_role", "line_id", "allocations", "conflicts", "draft_order"}).
			AddRow("sales-1", string(domain.RoleSales), "A", []byte("{"), []byte("[]"), sql.NullString{String: validDraft, Valid: true}), wantError: "unexpected end of JSON input"},
		{name: "invalid conflicts json", row: sqlmock.NewRows([]string{"actor_id", "actor_role", "line_id", "allocations", "conflicts", "draft_order"}).
			AddRow("sales-1", string(domain.RoleSales), "A", []byte("[]"), []byte("{"), sql.NullString{String: validDraft, Valid: true}), wantError: "unexpected end of JSON input"},
		{name: "defer draft with pending ids", row: sqlmock.NewRows([]string{"actor_id", "actor_role", "line_id", "allocations", "conflicts", "draft_order"}).
			AddRow("sales-1", string(domain.RoleSales), "A", []byte("[]"), []byte(`[{"orderId":"PREVIEW-DRAFT"}]`), sql.NullString{String: validDraft, Valid: true}), wantError: "draft defer cannot include deferred pending orders", deferDraft: true, deferredOrderIDs: []string{"ORD-1"}},
		{name: "defer draft without conflicts", row: sqlmock.NewRows([]string{"actor_id", "actor_role", "line_id", "allocations", "conflicts", "draft_order"}).
			AddRow("sales-1", string(domain.RoleSales), "A", []byte("[]"), []byte("[]"), sql.NullString{String: validDraft, Valid: true}), wantError: "draft can be deferred only when preview has conflicts", deferDraft: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			expect := mock.ExpectQuery("SELECT actor_id, actor_role, line_id, allocations, conflicts, draft_order").
				WithArgs("preview-" + strings.ReplaceAll(tt.name, " ", "-"))
			if tt.err != nil {
				expect.WillReturnError(tt.err)
			} else {
				expect.WillReturnRows(tt.row)
			}
			req := confirmPreviewRequest{
				PreviewID:        "preview-" + strings.ReplaceAll(tt.name, " ", "-"),
				DeferDraft:       tt.deferDraft,
				DeferredOrderIDs: tt.deferredOrderIDs,
			}
			_, err := store.ConfirmPreviewOrder(req, claims)
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("expected %q error, got %v", tt.wantError, err)
			}
		})
	}
}

func TestPostgresStoreValidateSalesDeferredOrdersBranches(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()
	mock.MatchExpectationsInOrder(false)
	store := &PostgresStore{MemoryStore: NewMemoryStore(), db: db}
	claims := auth.Claims{Subject: "sales-1", Role: domain.RoleSales}
	preview := previewRecord{LineID: "A", Conflicts: []scheduler.Conflict{{OrderID: "ORD-1", AffectedOrderIDs: []string{"ORD-2"}}}}

	if orders, err := store.validateSalesDeferredOrders(nil, preview, claims); err != nil || orders != nil {
		t.Fatalf("empty deferred orders should be nil without error, got %+v %v", orders, err)
	}
	if _, err := store.validateSalesDeferredOrders([]string{"ORD-X"}, preview, claims); err == nil || !strings.Contains(err.Error(), "deferred order must belong") {
		t.Fatalf("expected not allowed error, got %v", err)
	}
	mockPostgresOrder(t, mock, "ORD-1", "A", "sales-2", domain.StatusPending)
	if _, err := store.validateSalesDeferredOrders([]string{"ORD-1"}, preview, claims); err == nil || !strings.Contains(err.Error(), "sales can defer only their own orders") {
		t.Fatalf("expected owner error, got %v", err)
	}
	mockPostgresOrder(t, mock, "ORD-1", "B", "sales-1", domain.StatusPending)
	if _, err := store.validateSalesDeferredOrders([]string{"ORD-1"}, preview, claims); err == nil || !strings.Contains(err.Error(), "line must match") {
		t.Fatalf("expected line error, got %v", err)
	}
	mockPostgresOrder(t, mock, "ORD-1", "A", "sales-1", domain.StatusScheduled)
	if _, err := store.validateSalesDeferredOrders([]string{"ORD-1"}, preview, claims); err == nil || !strings.Contains(err.Error(), "only pending") {
		t.Fatalf("expected status error, got %v", err)
	}
	mockPostgresOrder(t, mock, "ORD-1", "A", "sales-1", domain.StatusPending)
	orders, err := store.validateSalesDeferredOrders([]string{"ORD-1", "ORD-1"}, preview, claims)
	if err != nil {
		t.Fatalf("expected deferred order success, got %v", err)
	}
	if len(orders) != 1 || orders[0].ID != "ORD-1" {
		t.Fatalf("unexpected deferred orders: %+v", orders)
	}
}

func mockPostgresOrder(t *testing.T, mock sqlmock.Sqlmock, id, lineID, createdBy string, status domain.OrderStatus) {
	t.Helper()
	now := time.Now().UTC()
	mock.ExpectQuery("SELECT id, customer, line_id.* FROM orders WHERE id = \\$1").
		WithArgs(id).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "customer", "line_id", "quantity", "priority", "status", "due_date", "note", "created_by",
			"source_order", "rejection_reason", "rejected_by", "rejected_at", "created_at", "updated_at",
		}).AddRow(id, "ACME", lineID, 100, string(domain.PriorityLow), string(status), now.AddDate(0, 0, 1), "", createdBy, "", "", "", nil, now, now))
}

func TestPostgresStore_ConfirmPreviewOrderTxRejectsStalePreviewDelete(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()
	mock.MatchExpectationsInOrder(false)

	store := &PostgresStore{
		MemoryStore: NewMemoryStore(),
		db:          db,
	}
	claims := auth.Claims{Subject: "sales-1", Role: domain.RoleSales}
	draft := createOrderRequest{
		Customer: "ACME",
		LineID:   "A",
		Quantity: 500,
		Priority: domain.PriorityLow,
		DueDate:  "2026-06-03",
	}

	mock.ExpectQuery("SELECT id, name, capacity_per_day").
		WithArgs("A", "Asia/Taipei").
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "capacity_per_day", "timezone", "schedule_revision"}).
			AddRow("A", "Line A", 1000, "Asia/Taipei", 1))
	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO orders").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("INSERT INTO audit_logs").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("UPDATE production_lines").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("DELETE FROM schedule_previews").
		WithArgs("preview-1", "sales-1", domain.RoleSales).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectRollback()

	_, err = store.confirmPreviewOrderTx("preview-1", draft, nil, false, claims)
	if err == nil || !strings.Contains(err.Error(), "preview result expired or not found") {
		t.Fatalf("expected stale preview error, got %v", err)
	}
}

func TestPostgresStore_ConfirmPreviewOrderTxRejectsChangedDeferredOrder(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()
	mock.MatchExpectationsInOrder(false)

	store := &PostgresStore{
		MemoryStore: NewMemoryStore(),
		db:          db,
	}
	claims := auth.Claims{Subject: "sales-1", Role: domain.RoleSales}
	draft := createOrderRequest{
		Customer: "ACME",
		LineID:   "A",
		Quantity: 500,
		Priority: domain.PriorityLow,
		DueDate:  "2026-06-03",
	}
	deferredOrders := []domain.Order{{
		ID:        "ORD-DEFER",
		LineID:    "A",
		Status:    domain.StatusPending,
		CreatedBy: "sales-1",
	}}

	mock.ExpectQuery("SELECT id, name, capacity_per_day").
		WithArgs("A", "Asia/Taipei").
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "capacity_per_day", "timezone", "schedule_revision"}).
			AddRow("A", "Line A", 1000, "Asia/Taipei", 1))
	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO orders").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("INSERT INTO audit_logs").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("UPDATE orders").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectRollback()

	_, err = store.confirmPreviewOrderTx("preview-1", draft, deferredOrders, false, claims)
	if err == nil || !strings.Contains(err.Error(), "deferred order changed before confirmation") {
		t.Fatalf("expected changed deferred order error, got %v", err)
	}
}

func TestPostgresStore_ConfirmPreviewOrderTxCanDeferDraft(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()
	mock.MatchExpectationsInOrder(false)

	store := &PostgresStore{
		MemoryStore: NewMemoryStore(),
		db:          db,
	}
	claims := auth.Claims{Subject: "sales-1", Role: domain.RoleSales}
	draft := createOrderRequest{
		Customer: "ACME",
		LineID:   "A",
		Quantity: 500,
		Priority: domain.PriorityLow,
		DueDate:  "2026-06-03",
	}

	mock.ExpectQuery("SELECT id, name, capacity_per_day").
		WithArgs("A", "Asia/Taipei").
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "capacity_per_day", "timezone", "schedule_revision"}).
			AddRow("A", "Line A", 1000, "Asia/Taipei", 1))
	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO orders").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("INSERT INTO audit_logs").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("INSERT INTO audit_logs").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("UPDATE production_lines").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("DELETE FROM schedule_previews").
		WithArgs("preview-1", "sales-1", domain.RoleSales).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	order, err := store.confirmPreviewOrderTx("preview-1", draft, nil, true, claims)
	if err != nil {
		t.Fatalf("confirmPreviewOrderTx failed: %v", err)
	}
	if order.Status != domain.StatusRejected || order.RejectedBy != "sales-1" || order.RejectionReason != "" {
		t.Fatalf("expected rejected draft without reason, got %+v", order)
	}
}

func TestPostgresStore_ScheduleHistoryAndStartProduction(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()
	mock.MatchExpectationsInOrder(false)

	store := &PostgresStore{
		MemoryStore: NewMemoryStore(),
		db:          db,
	}

	claims := auth.Claims{
		Subject: "sched-1",
		Role:    domain.RoleScheduler,
		LineID:  "A",
	}

	// 1. ScheduleHistory
	mock.ExpectQuery("SELECT a.id, a.actor_id, a.action").
		WillReturnRows(sqlmock.NewRows([]string{"id", "actor_id", "action", "resource", "reason", "created_at"}).
			AddRow("AUD-1", "sched-1", "production.start", "ORD-1", "", time.Now()))

	history, err := store.ScheduleHistory("A", claims)
	if err != nil {
		t.Fatalf("ScheduleHistory failed: %v", err)
	}
	if len(history) != 1 || history[0].ID != "AUD-1" {
		t.Errorf("unexpected history: %+v", history)
	}

	// 2. StartProduction
	tNow := time.Now().UTC()
	rows := sqlmock.NewRows([]string{
		"id", "customer", "line_id", "quantity", "priority", "status", "due_date", "note", "created_by",
		"source_order", "rejection_reason", "rejected_by", "rejected_at", "created_at", "updated_at",
	}).AddRow("ORD-1", "ACME", "A", 100, string(domain.PriorityLow), string(domain.StatusScheduled), tNow, "", "sales-1", "", "", "", nil, tNow, tNow)

	mock.ExpectQuery("SELECT id, customer, line_id.* FROM orders WHERE id = \\$1").
		WithArgs("ORD-1").
		WillReturnRows(rows)

	// allocations count query
	mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM schedule_allocations").
		WithArgs("ORD-1").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))

	// Begin Tx
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE orders SET status = '生產中'").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("UPDATE schedule_allocations SET locked = TRUE").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("UPDATE production_lines SET schedule_revision").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("INSERT INTO audit_logs").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	order, err := store.StartProduction(productionStartRequest{OrderID: "ORD-1"}, claims)
	if err != nil {
		t.Fatalf("StartProduction failed: %v", err)
	}
	if order.Status != domain.StatusInProgress {
		t.Errorf("unexpected order status: %v", order.Status)
	}
}

func TestPostgresStore_PreviewSchedule(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()
	mock.MatchExpectationsInOrder(false)

	store := &PostgresStore{
		MemoryStore: NewMemoryStore(),
		db:          db,
	}

	claims := auth.Claims{
		Subject: "sched-1",
		Role:    domain.RoleScheduler,
		LineID:  "A",
	}

	// 1. PreviewSchedule
	mock.ExpectQuery("SELECT id, name, capacity_per_day").
		WithArgs("A", "Asia/Taipei").
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "capacity_per_day", "timezone", "schedule_revision"}).
			AddRow("A", "Line A", 1000, "Asia/Taipei", 1))

	// pendingOrderInputs query
	mock.ExpectQuery("SELECT id, customer, line_id, quantity, priority, status, due_date, created_at[\\s\\S]*FROM orders[\\s\\S]*WHERE line_id = \\$1 AND status = '待排程'").
		WithArgs("A").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "customer", "line_id", "quantity", "priority", "status", "due_date", "created_at",
		}))

	// existingAllocations query - expects 6 columns
	mock.ExpectQuery("SELECT order_id, line_id, allocation_date, quantity, priority, locked FROM schedule_allocations").
		WithArgs("A").
		WillReturnRows(sqlmock.NewRows([]string{"order_id", "line_id", "allocation_date", "quantity", "priority", "locked"}))

	// insert schedule preview
	mock.ExpectExec("INSERT INTO schedule_previews").WillReturnResult(sqlmock.NewResult(1, 1))

	req := scheduleRequest{
		LineID:    "A",
		StartDate: time.Now().Format("2006-01-02"),
	}

	resp, err := store.PreviewSchedule(req, claims)
	if err != nil {
		t.Fatalf("PreviewSchedule failed: %v", err)
	}
	if resp.PreviewID == "" {
		t.Errorf("expected non-empty preview ID")
	}
}

func TestPostgresStore_ScheduleCalendar(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()
	mock.MatchExpectationsInOrder(false)

	store := &PostgresStore{
		MemoryStore: NewMemoryStore(),
		db:          db,
	}

	claims := auth.Claims{
		Subject: "sales-1",
		Role:    domain.RoleSales,
	}

	mock.ExpectQuery("SELECT id, name, capacity_per_day").
		WithArgs("A", "Asia/Taipei").
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "capacity_per_day", "timezone", "schedule_revision"}).
			AddRow("A", "Line A", 1000, "Asia/Taipei", 1))

	// Query allocations
	mock.ExpectQuery("SELECT a.order_id, o.customer, a.line_id").
		WithArgs("A", sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{
			"order_id", "customer", "line_id", "allocation_date", "quantity", "completed_quantity", "priority", "status", "locked", "due_date", "created_at",
		}).AddRow("ORD-1", "ACME", "A", time.Now(), 500, 0, "high", "已排程", false, time.Now(), time.Now()))

	// pendingOrderInputs query for Sales
	mock.ExpectQuery("SELECT id, customer, line_id, quantity, priority, status, due_date, created_at[\\s\\S]*FROM orders[\\s\\S]*WHERE line_id = \\$1 AND status = '待排程'").
		WithArgs("A").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "customer", "line_id", "quantity", "priority", "status", "due_date", "created_at",
		}).AddRow("ORD-2", "ACME", "A", 300, string(domain.PriorityLow), string(domain.StatusPending), time.Now(), time.Now()))

	// existingAllocations query - expects 6 columns
	mock.ExpectQuery("SELECT order_id, line_id, allocation_date, quantity, priority, locked FROM schedule_allocations").
		WithArgs("A").
		WillReturnRows(sqlmock.NewRows([]string{"order_id", "line_id", "allocation_date", "quantity", "priority", "locked"}))

	resp, err := store.ScheduleCalendar("A", "2026-05", claims)
	if err != nil {
		t.Fatalf("ScheduleCalendar failed: %v", err)
	}
	if len(resp.Allocations) != 1 || resp.Allocations[0].OrderID != "ORD-1" {
		t.Errorf("unexpected allocations: %+v", resp.Allocations)
	}
}

func TestPostgresStore_EnsurePreviewLoaded(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()
	mock.MatchExpectationsInOrder(false)

	store := &PostgresStore{
		MemoryStore: NewMemoryStore(),
		db:          db,
	}

	// 1. empty preview ID
	err = store.ensurePreviewLoaded("")
	if err != nil {
		t.Errorf("expected no error for empty preview ID, got %v", err)
	}

	// 2. already loaded in MemoryStore
	store.MemoryStore.mu.Lock()
	store.MemoryStore.previews["p-loaded"] = previewRecord{LineID: "A"}
	store.MemoryStore.mu.Unlock()
	err = store.ensurePreviewLoaded("p-loaded")
	if err != nil {
		t.Errorf("expected no error for already loaded preview ID, got %v", err)
	}

	// 3. not loaded, query returns sql.ErrNoRows
	mock.ExpectQuery("SELECT actor_id, actor_role, line_id, line_revision, request_hash, request, draft_order, created_at").
		WithArgs("p-norows").
		WillReturnError(sql.ErrNoRows)
	err = store.ensurePreviewLoaded("p-norows")
	if err != nil {
		t.Errorf("expected no error when preview not found in DB, got %v", err)
	}

	// 4. not loaded, query returns some other DB error
	mock.ExpectQuery("SELECT actor_id, actor_role, line_id, line_revision, request_hash, request, draft_order, created_at").
		WithArgs("p-error").
		WillReturnError(errors.New("db error"))
	err = store.ensurePreviewLoaded("p-error")
	if err == nil || err.Error() != "db error" {
		t.Errorf("expected db error, got %v", err)
	}

	// 5. invalid json in request
	mock.ExpectQuery("SELECT actor_id, actor_role, line_id, line_revision, request_hash, request, draft_order, created_at").
		WithArgs("p-invalid-json").
		WillReturnRows(sqlmock.NewRows([]string{"actor_id", "actor_role", "line_id", "line_revision", "request_hash", "request", "draft_order", "created_at"}).
			AddRow("admin-1", string(domain.RoleAdmin), "A", int64(1), "hash", []byte("invalid json"), sql.NullString{}, time.Now()))
	err = store.ensurePreviewLoaded("p-invalid-json")
	if err == nil || !strings.Contains(err.Error(), "invalid character") {
		t.Errorf("expected json unmarshal error, got %v", err)
	}

	// 6. invalid json in draft_order
	mock.ExpectQuery("SELECT actor_id, actor_role, line_id, line_revision, request_hash, request, draft_order, created_at").
		WithArgs("p-invalid-draft").
		WillReturnRows(sqlmock.NewRows([]string{"actor_id", "actor_role", "line_id", "line_revision", "request_hash", "request", "draft_order", "created_at"}).
			AddRow("admin-1", string(domain.RoleAdmin), "A", int64(1), "hash", []byte(`{}`), sql.NullString{String: "invalid json", Valid: true}, time.Now()))
	err = store.ensurePreviewLoaded("p-invalid-draft")
	if err == nil || !strings.Contains(err.Error(), "invalid character") {
		t.Errorf("expected draft json unmarshal error, got %v", err)
	}

	// 7. productionLine fails
	mock.ExpectQuery("SELECT actor_id, actor_role, line_id, line_revision, request_hash, request, draft_order, created_at").
		WithArgs("p-line-fail").
		WillReturnRows(sqlmock.NewRows([]string{"actor_id", "actor_role", "line_id", "line_revision", "request_hash", "request", "draft_order", "created_at"}).
			AddRow("admin-1", string(domain.RoleAdmin), "A", int64(1), "hash", []byte(`{}`), sql.NullString{}, time.Now()))
	mock.ExpectQuery("SELECT id, name, capacity_per_day, COALESCE\\(timezone").
		WithArgs("A", "Asia/Taipei").
		WillReturnError(errors.New("line fetch error"))
	err = store.ensurePreviewLoaded("p-line-fail")
	if err == nil || err.Error() != "line fetch error" {
		t.Errorf("expected line fetch error, got %v", err)
	}

	// 8. fully successful load
	mock.ExpectQuery("SELECT actor_id, actor_role, line_id, line_revision, request_hash, request, draft_order, created_at").
		WithArgs("p-success").
		WillReturnRows(sqlmock.NewRows([]string{"actor_id", "actor_role", "line_id", "line_revision", "request_hash", "request", "draft_order", "created_at"}).
			AddRow("admin-1", string(domain.RoleAdmin), "A", int64(5), "hash-123", []byte(`{"lineId": "A"}`), sql.NullString{String: `{"customer": "ACME", "quantity": 100, "dueDate": "2026-06-01"}`, Valid: true}, time.Now()))
	mock.ExpectQuery("SELECT id, name, capacity_per_day, COALESCE\\(timezone").
		WithArgs("A", "Asia/Taipei").
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "capacity_per_day", "timezone", "schedule_revision"}).
			AddRow("A", "Line A", 1000, "Asia/Taipei", 1))
	err = store.ensurePreviewLoaded("p-success")
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	// Verify memory store states
	store.MemoryStore.mu.Lock()
	p, ok := store.MemoryStore.previews["p-success"]
	store.MemoryStore.mu.Unlock()
	if !ok || p.LineRevision != 5 || p.DraftOrder.Customer != "ACME" {
		t.Errorf("failed to populate memory store correctly: %+v", p)
	}
}

func TestPostgresStore_HPAPeakSummary_Error(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()
	mock.MatchExpectationsInOrder(false)

	store := &PostgresStore{
		MemoryStore: NewMemoryStore(),
		db:          db,
	}

	// Mock failure in the first query (line count)
	mock.ExpectQuery("SELECT COUNT\\(DISTINCT line_id\\)").
		WillReturnError(errors.New("db count error"))

	summary := store.HPAPeakSummary()
	if summary.LineCount != 0 {
		t.Errorf("expected active lines 0, got %d", summary.LineCount)
	}
}

func TestPostgresStore_SplitAllocationOrderIDsDB(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()
	mock.MatchExpectationsInOrder(false)

	store := &PostgresStore{
		MemoryStore: NewMemoryStore(),
		db:          db,
	}

	// We pass allocations with one duplicate OrderID (so "ORD-1" twice).
	allocations := []scheduler.Allocation{
		{OrderID: "ORD-1", LineID: "A", Quantity: 50},
		{OrderID: "ORD-1", LineID: "A", Quantity: 50},
	}

	// 1. Mock order query returns error
	mock.ExpectQuery("SELECT id, customer, line_id, quantity, priority, status, due_date, COALESCE\\(note").
		WithArgs("ORD-1").
		WillReturnError(errors.New("order query error"))

	_, err = store.splitAllocationOrderIDsDB(allocations)
	if err == nil || err.Error() != "order query error" {
		t.Errorf("expected order query error, got %v", err)
	}

	// 2. Mock order query succeeds, but EXISTS query returns error
	mock.ExpectQuery("SELECT id, customer, line_id, quantity, priority, status, due_date, COALESCE\\(note").
		WithArgs("ORD-1").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "customer", "line_id", "quantity", "priority", "status", "due_date", "note", "created_by", "source_order", "rejection_reason", "rejected_by", "rejected_at", "created_at", "updated_at",
		}).AddRow("ORD-1", "ACME", "A", 100, "low", "待排程", time.Now(), "", "", "", "", "", nil, time.Now(), time.Now()))

	mock.ExpectQuery("SELECT EXISTS \\(SELECT 1 FROM orders WHERE id = \\$1\\)").
		WillReturnError(errors.New("exists query error"))

	_, err = store.splitAllocationOrderIDsDB(allocations)
	if err == nil || err.Error() != "exists query error" {
		t.Errorf("expected exists query error, got %v", err)
	}

	// 3. Mock success
	mock.ExpectQuery("SELECT id, customer, line_id, quantity, priority, status, due_date, COALESCE\\(note").
		WithArgs("ORD-1").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "customer", "line_id", "quantity", "priority", "status", "due_date", "note", "created_by", "source_order", "rejection_reason", "rejected_by", "rejected_at", "created_at", "updated_at",
		}).AddRow("ORD-1", "ACME", "A", 100, "low", "待排程", time.Now(), "", "", "", "", "", nil, time.Now(), time.Now()))

	mock.ExpectQuery("SELECT EXISTS \\(SELECT 1 FROM orders WHERE id = \\$1\\)").
		WithArgs("ORD-1-1").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))

	res, err := store.splitAllocationOrderIDsDB(allocations)
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if len(res) != 2 || res[0].OrderID != "ORD-1" || res[1].OrderID != "ORD-1-1" {
		t.Errorf("unexpected split results: %+v", res)
	}
}

func TestPostgresStore_PreviewSchedule_DraftAndResolution(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()
	mock.MatchExpectationsInOrder(false)

	store := &PostgresStore{
		MemoryStore: NewMemoryStore(),
		db:          db,
	}

	salesClaims := auth.Claims{
		Subject: "sales-1",
		Role:    domain.RoleSales,
	}

	// 1. Role validation for draft: Scheduler cannot preview draft orders
	schedulerClaims := auth.Claims{
		Subject: "scheduler-1",
		Role:    domain.RoleScheduler,
		LineID:  "A",
	}
	draftReq := scheduleRequest{
		LineID: "A",
		DraftOrder: &createOrderRequest{
			Customer: "ACME",
			Quantity: 100,
			DueDate:  "2026-06-01",
		},
	}
	// Mock productionLine call
	mock.ExpectQuery("SELECT id, name, capacity_per_day, COALESCE\\(timezone").
		WithArgs("A", "Asia/Taipei").
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "capacity_per_day", "timezone", "schedule_revision"}).
			AddRow("A", "Line A", 1000, "Asia/Taipei", 1))

	_, err = store.PreviewSchedule(draftReq, schedulerClaims)
	if err == nil || !strings.Contains(err.Error(), "only sales can preview draft orders") {
		t.Errorf("expected RoleSales error, got %v", err)
	}

	// 2. Draft order line ID mismatch
	mismatchReq := scheduleRequest{
		LineID: "A",
		DraftOrder: &createOrderRequest{
			Customer: "ACME",
			Quantity: 100,
			DueDate:  "2026-06-01",
			LineID:   "B",
		},
	}
	// Mock productionLine call
	mock.ExpectQuery("SELECT id, name, capacity_per_day, COALESCE\\(timezone").
		WithArgs("A", "Asia/Taipei").
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "capacity_per_day", "timezone", "schedule_revision"}).
			AddRow("A", "Line A", 1000, "Asia/Taipei", 1))

	_, err = store.PreviewSchedule(mismatchReq, salesClaims)
	if err == nil || !strings.Contains(err.Error(), "draft order line must match preview line") {
		t.Errorf("expected draft line mismatch error, got %v", err)
	}

	// 3. Draft preview cannot include resolution orders
	resolutionDraftReq := scheduleRequest{
		LineID: "A",
		DraftOrder: &createOrderRequest{
			Customer: "ACME",
			Quantity: 100,
			DueDate:  "2026-06-01",
		},
		ResolutionOrderIDs: []string{"RES-1"},
	}
	// Mock productionLine call
	mock.ExpectQuery("SELECT id, name, capacity_per_day, COALESCE\\(timezone").
		WithArgs("A", "Asia/Taipei").
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "capacity_per_day", "timezone", "schedule_revision"}).
			AddRow("A", "Line A", 1000, "Asia/Taipei", 1))

	_, err = store.PreviewSchedule(resolutionDraftReq, salesClaims)
	if err == nil || !strings.Contains(err.Error(), "draft previews cannot include resolution orders") {
		t.Errorf("expected draft resolution error, got %v", err)
	}

	// 4. Successful draft preview with baseline plan
	// It calls productionLine twice: once for previewFromDB and once for schedulerInputs validation.
	mock.ExpectQuery("SELECT id, name, capacity_per_day, COALESCE\\(timezone").
		WithArgs("A", "Asia/Taipei").
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "capacity_per_day", "timezone", "schedule_revision"}).
			AddRow("A", "Line A", 1000, "Asia/Taipei", 1))
	mock.ExpectQuery("SELECT id, name, capacity_per_day, COALESCE\\(timezone").
		WithArgs("A", "Asia/Taipei").
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "capacity_per_day", "timezone", "schedule_revision"}).
			AddRow("A", "Line A", 1000, "Asia/Taipei", 1))

	// pendingOrderInputs query for A
	mock.ExpectQuery("SELECT id, customer, line_id, quantity, priority, status, due_date, created_at[\\s\\S]*FROM orders[\\s\\S]*WHERE line_id = \\$1 AND status = '待排程'").
		WithArgs("A").
		WillReturnRows(sqlmock.NewRows([]string{"id", "customer", "line_id", "quantity", "priority", "status", "due_date", "created_at"}).
			AddRow("ORD-PENDING", "PENDING_C", "A", 200, "low", "待排程", time.Now().AddDate(0, 0, 1), time.Now()))

	// existingAllocations query (empty)
	mock.ExpectQuery("SELECT order_id, line_id, allocation_date, quantity, priority, locked FROM schedule_allocations").
		WithArgs("A").
		WillReturnRows(sqlmock.NewRows([]string{"order_id", "line_id", "allocation_date", "quantity", "priority", "locked"}))

	// Mock the insert into schedule_previews table
	mock.ExpectExec("INSERT INTO schedule_previews").
		WillReturnResult(sqlmock.NewResult(1, 1))

	resp, err := store.PreviewSchedule(draftReq, salesClaims)
	if err != nil {
		t.Fatalf("PreviewSchedule for draft failed: %v", err)
	}
	if resp.DraftOrder == nil || resp.DraftOrder.Customer != "ACME" {
		t.Errorf("unexpected preview response draft: %+v", resp.DraftOrder)
	}

	// 5. Preview schedule with resolution orders
	schedulerClaimsA := auth.Claims{
		Subject: "scheduler-1",
		Role:    domain.RoleScheduler,
		LineID:  "A",
	}

	resReq := scheduleRequest{
		LineID:             "A",
		OrderIDs:           []string{"ORD-2"},
		ResolutionOrderIDs: []string{"RES-1"},
	}

	mock.ExpectQuery("SELECT id, name, capacity_per_day, COALESCE\\(timezone").
		WithArgs("A", "Asia/Taipei").
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "capacity_per_day", "timezone", "schedule_revision"}).
			AddRow("A", "Line A", 1000, "Asia/Taipei", 1))

	// pendingOrderInputs query (returns ORD-2)
	mock.ExpectQuery("SELECT id, customer, line_id, quantity, priority, status, due_date, created_at[\\s\\S]*FROM orders[\\s\\S]*WHERE line_id = \\$1 AND status = '待排程'").
		WithArgs("A").
		WillReturnRows(sqlmock.NewRows([]string{"id", "customer", "line_id", "quantity", "priority", "status", "due_date", "created_at"}).
			AddRow("ORD-2", "C1", "A", 100, "high", "待排程", time.Now().AddDate(0, 0, 2), time.Now()))

	// resolutionOrderInputs query (returns RES-1)
	mock.ExpectQuery("SELECT id, customer, line_id, quantity, priority, status, due_date, created_at[\\s\\S]*FROM orders").
		WillReturnRows(sqlmock.NewRows([]string{"id", "customer", "line_id", "quantity", "priority", "status", "due_date", "created_at"}).
			AddRow("RES-1", "C2", "A", 50, "low", "已排程", time.Now().AddDate(0, 0, 2), time.Now()))

	// ensureResolutionOrderMovable query (returns one unlocked, scheduled allocation to pass)
	mock.ExpectQuery("SELECT locked, COALESCE\\(status, \\$1\\) FROM schedule_allocations WHERE order_id = \\$2").
		WithArgs(string(domain.StatusScheduled), "RES-1").
		WillReturnRows(sqlmock.NewRows([]string{"locked", "status"}).AddRow(false, "已排程"))

	// existingAllocations query (returns one matching RES-1 to test continue path, one non-matching)
	mock.ExpectQuery("SELECT order_id, line_id, allocation_date, quantity, priority, locked FROM schedule_allocations").
		WithArgs("A").
		WillReturnRows(sqlmock.NewRows([]string{"order_id", "line_id", "allocation_date", "quantity", "priority", "locked"}).
			AddRow("RES-1", "A", time.Now().AddDate(0, 0, 2), 50, "low", false).
			AddRow("ORD-3", "A", time.Now().AddDate(0, 0, 1), 200, "high", true))

	mock.ExpectExec("INSERT INTO schedule_previews").
		WillReturnResult(sqlmock.NewResult(1, 1))

	resResp, err := store.PreviewSchedule(resReq, schedulerClaimsA)
	if err != nil {
		t.Fatalf("PreviewSchedule for resolution failed: %v", err)
	}
	if len(resResp.Allocations) == 0 {
		t.Errorf("expected allocations, got empty")
	}
}

func TestPostgresStore_ApplyMigrations(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()
	mock.MatchExpectationsInOrder(false)

	store := &PostgresStore{
		MemoryStore: NewMemoryStore(),
		db:          db,
	}

	// Create temporary migrations directory inside the package test dir
	err = os.MkdirAll("db/migrations", 0755)
	if err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}
	defer os.RemoveAll("db")

	err = os.WriteFile("db/migrations/001_init.sql", []byte("CREATE TABLE test (id int);"), 0644)
	if err != nil {
		t.Fatalf("write file 1 failed: %v", err)
	}
	err = os.WriteFile("db/migrations/002_seed_demo.sql", []byte("INSERT INTO test VALUES (1);"), 0644)
	if err != nil {
		t.Fatalf("write file 2 failed: %v", err)
	}

	// Mock DB Exec
	mock.ExpectExec("CREATE TABLE test").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("INSERT INTO test").WillReturnResult(sqlmock.NewResult(1, 1))

	err = store.applyMigrations(true)
	if err != nil {
		t.Fatalf("applyMigrations failed: %v", err)
	}
}

func TestPostgresStore_NewPostgresStoreContext_PingFailure(t *testing.T) {
	// Empty DB URL validation
	_, err := NewPostgresStoreContext(context.Background(), "", false)
	if err == nil || err.Error() != "DATABASE_URL 不可為空" {
		t.Errorf("expected DATABASE_URL error, got %v", err)
	}

	// Invalid driver URL or nonexistent connection failure
	_, err = NewPostgresStoreContext(context.Background(), "postgres://localhost:5432/nonexistent?sslmode=disable", false)
	if err == nil {
		t.Error("expected ping failure error")
	}
}

func TestPostgresStore_CancelOrders_Initial(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()
	mock.MatchExpectationsInOrder(false)

	store := &PostgresStore{
		MemoryStore: NewMemoryStore(),
		db:          db,
	}

	salesClaims := auth.Claims{
		Subject: "sales-1",
		Role:    domain.RoleSales,
	}

	// 1. empty order IDs
	_, err = store.CancelOrders(cancelOrdersRequest{}, salesClaims)
	if err == nil || err.Error() != "orderIds is required" {
		t.Errorf("expected error, got %v", err)
	}

	// 2. Begin tx fails
	mock.ExpectBegin().WillReturnError(errors.New("begin error"))
	_, err = store.CancelOrders(cancelOrdersRequest{OrderIDs: []string{"ORD-1"}}, salesClaims)
	if err == nil || err.Error() != "begin error" {
		t.Errorf("expected begin error, got %v", err)
	}

	// 3. Order not found and already cancelled status (skipped list)
	mock.ExpectBegin()
	// ORD-1: not found
	mock.ExpectQuery("SELECT id, customer, line_id, quantity, priority, status, due_date, COALESCE\\(note").
		WithArgs("ORD-1").
		WillReturnError(sql.ErrNoRows)
	// ORD-2: already cancelled
	mock.ExpectQuery("SELECT id, customer, line_id, quantity, priority, status, due_date, COALESCE\\(note").
		WithArgs("ORD-2").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "customer", "line_id", "quantity", "priority", "status", "due_date", "note", "created_by", "source_order", "rejection_reason", "rejected_by", "rejected_at", "created_at", "updated_at",
		}).AddRow("ORD-2", "C", "A", 100, "low", string(domain.StatusCancelled), time.Now(), "", "sales-1", "", "", "", nil, time.Now(), time.Now()))
	mock.ExpectCommit()

	resp, err := store.CancelOrders(cancelOrdersRequest{OrderIDs: []string{"ORD-1", "ORD-2"}}, salesClaims)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.SkippedOrderIDs) != 2 {
		t.Errorf("expected 2 skipped orders, got %+v", resp.SkippedOrderIDs)
	}
}

func TestPostgresStore_CancelOrders_Permissions(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()
	mock.MatchExpectationsInOrder(false)

	store := &PostgresStore{
		MemoryStore: NewMemoryStore(),
		db:          db,
	}

	salesClaims := auth.Claims{
		Subject: "sales-1",
		Role:    domain.RoleSales,
	}

	schedulerClaimsA := auth.Claims{
		Subject: "scheduler-1",
		Role:    domain.RoleScheduler,
		LineID:  "A",
	}

	// 4. Sales cannot cancel other sales' order
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT id, customer, line_id, quantity, priority, status, due_date, COALESCE\\(note").
		WithArgs("ORD-3").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "customer", "line_id", "quantity", "priority", "status", "due_date", "note", "created_by", "source_order", "rejection_reason", "rejected_by", "rejected_at", "created_at", "updated_at",
		}).AddRow("ORD-3", "C", "A", 100, "low", string(domain.StatusPending), time.Now(), "", "sales-other", "", "", "", nil, time.Now(), time.Now()))
	_, err = store.CancelOrders(cancelOrdersRequest{OrderIDs: []string{"ORD-3"}}, salesClaims)
	if err == nil || !strings.Contains(err.Error(), "sales can cancel only their own orders") {
		t.Errorf("expected sales own cancel error, got %v", err)
	}

	// 5. Sales cannot cancel scheduled orders
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT id, customer, line_id, quantity, priority, status, due_date, COALESCE\\(note").
		WithArgs("ORD-4").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "customer", "line_id", "quantity", "priority", "status", "due_date", "note", "created_by", "source_order", "rejection_reason", "rejected_by", "rejected_at", "created_at", "updated_at",
		}).AddRow("ORD-4", "C", "A", 100, "low", string(domain.StatusScheduled), time.Now(), "", "sales-1", "", "", "", nil, time.Now(), time.Now()))
	_, err = store.CancelOrders(cancelOrdersRequest{OrderIDs: []string{"ORD-4"}}, salesClaims)
	if err == nil || !strings.Contains(err.Error(), "sales can cancel only pending or rejected orders") {
		t.Errorf("expected status restriction error, got %v", err)
	}

	// 6. Scheduler cannot cancel another line
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT id, customer, line_id, quantity, priority, status, due_date, COALESCE\\(note").
		WithArgs("ORD-5").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "customer", "line_id", "quantity", "priority", "status", "due_date", "note", "created_by", "source_order", "rejection_reason", "rejected_by", "rejected_at", "created_at", "updated_at",
		}).AddRow("ORD-5", "C", "B", 100, "low", string(domain.StatusScheduled), time.Now(), "", "sales-1", "", "", "", nil, time.Now(), time.Now()))
	_, err = store.CancelOrders(cancelOrdersRequest{OrderIDs: []string{"ORD-5"}}, schedulerClaimsA)
	if err == nil || !strings.Contains(err.Error(), "cannot cancel another production line") {
		t.Errorf("expected line mismatch error, got %v", err)
	}

	// 7. Cannot cancel in progress or completed orders
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT id, customer, line_id, quantity, priority, status, due_date, COALESCE\\(note").
		WithArgs("ORD-6").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "customer", "line_id", "quantity", "priority", "status", "due_date", "note", "created_by", "source_order", "rejection_reason", "rejected_by", "rejected_at", "created_at", "updated_at",
		}).AddRow("ORD-6", "C", "A", 100, "low", string(domain.StatusInProgress), time.Now(), "", "sales-1", "", "", "", nil, time.Now(), time.Now()))
	_, err = store.CancelOrders(cancelOrdersRequest{OrderIDs: []string{"ORD-6"}}, schedulerClaimsA)
	if err == nil || !strings.Contains(err.Error(), "cannot cancel in-progress or completed orders") {
		t.Errorf("expected progress restriction error, got %v", err)
	}
}

func TestPostgresStore_CancelOrders_Success(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()
	mock.MatchExpectationsInOrder(false)

	store := &PostgresStore{
		MemoryStore: NewMemoryStore(),
		db:          db,
	}

	schedulerClaimsA := auth.Claims{
		Subject: "scheduler-1",
		Role:    domain.RoleScheduler,
		LineID:  "A",
	}

	// 8. Successful cancellation
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT id, customer, line_id, quantity, priority, status, due_date, COALESCE\\(note").
		WithArgs("ORD-7").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "customer", "line_id", "quantity", "priority", "status", "due_date", "note", "created_by", "source_order", "rejection_reason", "rejected_by", "rejected_at", "created_at", "updated_at",
		}).AddRow("ORD-7", "C", "A", 100, "low", string(domain.StatusScheduled), time.Now(), "", "sales-1", "", "", "", nil, time.Now(), time.Now()))
	mock.ExpectExec("DELETE FROM schedule_allocations WHERE order_id = \\$1").
		WithArgs("ORD-7").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("UPDATE orders SET status = \\$2, updated_at = NOW\\(\\) WHERE id = \\$1").
		WithArgs("ORD-7", domain.StatusCancelled).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("INSERT INTO audit_logs").
		WithArgs(sqlmock.AnyArg(), "scheduler-1", "order.cancel", "ORD-7", "").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("UPDATE production_lines SET schedule_revision = schedule_revision \\+ 1 WHERE id = \\$1").
		WithArgs("A").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	succResp, err := store.CancelOrders(cancelOrdersRequest{OrderIDs: []string{"ORD-7"}}, schedulerClaimsA)
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if len(succResp.CancelledOrderIDs) != 1 || succResp.CancelledOrderIDs[0] != "ORD-7" {
		t.Errorf("unexpected success response: %+v", succResp)
	}
}

func TestPostgresStore_AssignUser_Various(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()
	mock.MatchExpectationsInOrder(false)

	store := &PostgresStore{
		MemoryStore: NewMemoryStore(),
		db:          db,
	}

	// 1. Invalid role
	_, err = store.AssignUser(assignUserRequest{Username: "u1", Role: "invalid"}, "actor-1")
	if err == nil || err.Error() != "role must be admin, sales, or scheduler" {
		t.Errorf("expected role error, got %v", err)
	}

	// 2. Scheduler role, productionLine fails
	mock.ExpectQuery("SELECT id, name, capacity_per_day, COALESCE\\(timezone").
		WithArgs("A", "Asia/Taipei").
		WillReturnError(errors.New("line error"))
	_, err = store.AssignUser(assignUserRequest{Username: "u1", Role: domain.RoleScheduler, LineID: "A"}, "actor-1")
	if err == nil || err.Error() != "line error" {
		t.Errorf("expected line error, got %v", err)
	}

	// 3. User not found (sql.ErrNoRows)
	mock.ExpectQuery("UPDATE users SET role = \\$2, line_id = NULLIF\\(\\$3, ''\\)").
		WithArgs("u1", domain.RoleSales, "").
		WillReturnError(sql.ErrNoRows)
	_, err = store.AssignUser(assignUserRequest{Username: "u1", Role: domain.RoleSales}, "actor-1")
	if err == nil || err.Error() != "user not found" {
		t.Errorf("expected user not found, got %v", err)
	}

	// 4. Successful Assign
	mock.ExpectQuery("UPDATE users SET role = \\$2, line_id = NULLIF\\(\\$3, ''\\)").
		WithArgs("u1", domain.RoleSales, "").
		WillReturnRows(sqlmock.NewRows([]string{"id", "username", "password_hash", "role", "line_id", "disabled"}).
			AddRow("user-1", "u1", "hash", string(domain.RoleSales), "", false))
	mock.ExpectExec("INSERT INTO audit_logs").
		WithArgs(sqlmock.AnyArg(), "actor-1", "user-1", "sales ").
		WillReturnResult(sqlmock.NewResult(1, 1))

	user, err := store.AssignUser(assignUserRequest{Username: "u1", Role: domain.RoleSales}, "actor-1")
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if user.Username != "u1" || user.Role != domain.RoleSales {
		t.Errorf("unexpected user: %+v", user)
	}
}

func TestPostgresStore_DeleteUser_Various(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()
	mock.MatchExpectationsInOrder(false)

	store := &PostgresStore{
		MemoryStore: NewMemoryStore(),
		db:          db,
	}

	// 1. User not found
	mock.ExpectQuery("SELECT id, username, password_hash, role, COALESCE\\(line_id, ''\\), disabled").
		WithArgs("u1").
		WillReturnError(sql.ErrNoRows)
	_, err = store.DeleteUser("u1", "actor-1")
	if err == nil || err.Error() != "user not found" {
		t.Errorf("expected user not found, got %v", err)
	}

	// 2. Case: References > 0 (Disables user instead of delete)
	mock.ExpectQuery("SELECT id, username, password_hash, role, COALESCE\\(line_id, ''\\), disabled").
		WithArgs("u2").
		WillReturnRows(sqlmock.NewRows([]string{"id", "username", "password_hash", "role", "line_id", "disabled"}).
			AddRow("user-2", "u2", "hash", string(domain.RoleSales), "", false))
	// references select count
	mock.ExpectQuery("SELECT").
		WithArgs("user-2").
		WillReturnRows(sqlmock.NewRows([]string{"references"}).AddRow(5))
	// disabled UPDATE
	mock.ExpectQuery("UPDATE users SET disabled = TRUE").
		WithArgs("user-2").
		WillReturnRows(sqlmock.NewRows([]string{"id", "username", "password_hash", "role", "line_id", "disabled"}).
			AddRow("user-2", "u2", "hash", string(domain.RoleSales), "", true))
	mock.ExpectExec("INSERT INTO audit_logs").
		WithArgs(sqlmock.AnyArg(), "actor-1", "user-2").
		WillReturnResult(sqlmock.NewResult(1, 1))

	user, err := store.DeleteUser("u2", "actor-1")
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if !user.Disabled || user.Deleted {
		t.Errorf("expected user disabled, not deleted: %+v", user)
	}

	// 3. Case: References == 0 (Deletes user completely)
	mock.ExpectQuery("SELECT id, username, password_hash, role, COALESCE\\(line_id, ''\\), disabled").
		WithArgs("u3").
		WillReturnRows(sqlmock.NewRows([]string{"id", "username", "password_hash", "role", "line_id", "disabled"}).
			AddRow("user-3", "u3", "hash", string(domain.RoleSales), "", false))
	// references select count = 0
	mock.ExpectQuery("SELECT").
		WithArgs("user-3").
		WillReturnRows(sqlmock.NewRows([]string{"references"}).AddRow(0))
	// delete EXEC and audit log INSERT
	mock.ExpectExec("INSERT INTO audit_logs").
		WithArgs(sqlmock.AnyArg(), "actor-1", "user-3").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("DELETE FROM users WHERE id = \\$1").
		WithArgs("user-3").
		WillReturnResult(sqlmock.NewResult(1, 1))

	user, err = store.DeleteUser("u3", "actor-1")
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if user.Disabled || !user.Deleted {
		t.Errorf("expected user deleted: %+v", user)
	}
}

func TestPostgresStore_UpdateOrderDueDate_Various(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()
	mock.MatchExpectationsInOrder(false)

	store := &PostgresStore{
		MemoryStore: NewMemoryStore(),
		db:          db,
	}

	tNow := time.Now().UTC()

	// Helper to mock fetching order
	mockOrder := func(id, lineID, createdBy, status string) {
		rows := sqlmock.NewRows([]string{
			"id", "customer", "line_id", "quantity", "priority", "status", "due_date", "note", "created_by",
			"source_order", "rejection_reason", "rejected_by", "rejected_at", "created_at", "updated_at",
		}).AddRow(id, "ACME", lineID, 100, string(domain.PriorityLow), status, tNow, "", createdBy, "", "", "", nil, tNow, tNow)
		mock.ExpectQuery("SELECT id, customer, line_id.* FROM orders WHERE id = \\$1").
			WithArgs(id).
			WillReturnRows(rows)
	}

	// 1. scheduler cannot update another production line
	mockOrder("ORD-1", "line-a", "sales-1", string(domain.StatusPending))
	claims := auth.Claims{Role: domain.RoleScheduler, LineID: "line-b"}
	_, err = store.UpdateOrderDueDate("ORD-1", updateOrderRequest{}, claims)
	if err == nil || err.Error() != "cannot update another production line" {
		t.Errorf("expected scheduler line check error, got %v", err)
	}

	// 2. sales can update only their own orders
	mockOrder("ORD-1", "line-a", "sales-1", string(domain.StatusPending))
	claims = auth.Claims{Role: domain.RoleSales, Subject: "sales-2"}
	_, err = store.UpdateOrderDueDate("ORD-1", updateOrderRequest{}, claims)
	if err == nil || err.Error() != "sales can update only their own orders" {
		t.Errorf("expected sales ownership check error, got %v", err)
	}

	// 3. only pending or rejected orders can change details
	mockOrder("ORD-1", "line-a", "sales-1", string(domain.StatusScheduled))
	claims = auth.Claims{Role: domain.RoleSales, Subject: "sales-1"}
	_, err = store.UpdateOrderDueDate("ORD-1", updateOrderRequest{}, claims)
	if err == nil || err.Error() != "only pending or rejected orders can change order details" {
		t.Errorf("expected status check error, got %v", err)
	}

	// 4. note cannot be updated after order creation
	mockOrder("ORD-1", "line-a", "sales-1", string(domain.StatusPending))
	claims = auth.Claims{Role: domain.RoleSales, Subject: "sales-1"}
	_, err = store.UpdateOrderDueDate("ORD-1", updateOrderRequest{Note: "new note"}, claims)
	if err == nil || err.Error() != "note cannot be updated after order creation" {
		t.Errorf("expected note check error, got %v", err)
	}

	// 5. quantity check: < 25
	mockOrder("ORD-1", "line-a", "sales-1", string(domain.StatusPending))
	_, err = store.UpdateOrderDueDate("ORD-1", updateOrderRequest{Quantity: 10}, claims)
	if err == nil || err.Error() != "quantity must be between 25 and 2500" {
		t.Errorf("expected quantity < 25 error, got %v", err)
	}

	// 6. quantity check: > 2500
	mockOrder("ORD-1", "line-a", "sales-1", string(domain.StatusPending))
	_, err = store.UpdateOrderDueDate("ORD-1", updateOrderRequest{Quantity: 3000}, claims)
	if err == nil || err.Error() != "quantity must be between 25 and 2500" {
		t.Errorf("expected quantity > 2500 error, got %v", err)
	}

	// 7. s.productionLine returns error
	mockOrder("ORD-1", "line-a", "sales-1", string(domain.StatusPending))
	mock.ExpectQuery("SELECT id, name, capacity_per_day").
		WithArgs("line-a", "Asia/Taipei").
		WillReturnError(errors.New("db error production line"))
	_, err = store.UpdateOrderDueDate("ORD-1", updateOrderRequest{DueDate: "2026-06-05"}, claims)
	if err == nil || err.Error() != "db error production line" {
		t.Errorf("expected production line query error, got %v", err)
	}
}

func TestPostgresStore_StartProduction_Various(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()
	mock.MatchExpectationsInOrder(false)

	store := &PostgresStore{
		MemoryStore: NewMemoryStore(),
		db:          db,
	}

	tNow := time.Now().UTC()

	// Helper to mock fetching order
	mockOrder := func(id, lineID, status string) {
		rows := sqlmock.NewRows([]string{
			"id", "customer", "line_id", "quantity", "priority", "status", "due_date", "note", "created_by",
			"source_order", "rejection_reason", "rejected_by", "rejected_at", "created_at", "updated_at",
		}).AddRow(id, "ACME", lineID, 100, string(domain.PriorityLow), status, tNow, "", "sales-1", "", "", "", nil, tNow, tNow)
		mock.ExpectQuery("SELECT id, customer, line_id.* FROM orders WHERE id = \\$1").
			WithArgs(id).
			WillReturnRows(rows)
	}

	claims := auth.Claims{
		Role:    domain.RoleScheduler,
		LineID:  "line-a",
		Subject: "sched-1",
	}

	// 1. order not found
	mock.ExpectQuery("SELECT id, customer, line_id.* FROM orders WHERE id = \\$1").
		WithArgs("ORD-1").
		WillReturnError(sql.ErrNoRows)
	_, err = store.StartProduction(productionStartRequest{OrderID: "ORD-1"}, claims)
	if err == nil || err.Error() != "order not found" {
		t.Errorf("expected 'order not found' error, got %v", err)
	}

	// 2. cannot start another production line
	mockOrder("ORD-1", "line-b", string(domain.StatusScheduled))
	_, err = store.StartProduction(productionStartRequest{OrderID: "ORD-1"}, claims)
	if err == nil || err.Error() != "cannot start another production line" {
		t.Errorf("expected line mismatch error, got %v", err)
	}

	// 3. only scheduled orders can start production
	mockOrder("ORD-1", "line-a", string(domain.StatusPending))
	_, err = store.StartProduction(productionStartRequest{OrderID: "ORD-1"}, claims)
	if err == nil || err.Error() != "only scheduled orders can start production" {
		t.Errorf("expected status mismatch error, got %v", err)
	}

	// 4. database query error on allocations count
	mockOrder("ORD-1", "line-a", string(domain.StatusScheduled))
	mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM schedule_allocations").
		WithArgs("ORD-1").
		WillReturnError(errors.New("db count error"))
	_, err = store.StartProduction(productionStartRequest{OrderID: "ORD-1"}, claims)
	if err == nil || err.Error() != "db count error" {
		t.Errorf("expected db count error, got %v", err)
	}

	// 5. scheduled order has no allocation (count == 0)
	mockOrder("ORD-1", "line-a", string(domain.StatusScheduled))
	mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM schedule_allocations").
		WithArgs("ORD-1").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	_, err = store.StartProduction(productionStartRequest{OrderID: "ORD-1"}, claims)
	if err == nil || err.Error() != "scheduled order has no allocation" {
		t.Errorf("expected no allocation error, got %v", err)
	}

	// 6. Begin transaction failure
	mockOrder("ORD-1", "line-a", string(domain.StatusScheduled))
	mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM schedule_allocations").
		WithArgs("ORD-1").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectBegin().WillReturnError(errors.New("begin error"))
	_, err = store.StartProduction(productionStartRequest{OrderID: "ORD-1"}, claims)
	if err == nil || err.Error() != "begin error" {
		t.Errorf("expected begin error, got %v", err)
	}

	// 7. Successful start production
	mockOrder("ORD-1", "line-a", string(domain.StatusScheduled))
	mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM schedule_allocations").
		WithArgs("ORD-1").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE orders SET status = '生產中'").
		WithArgs("ORD-1", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("UPDATE schedule_allocations SET locked = TRUE").
		WithArgs("ORD-1").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("UPDATE production_lines SET schedule_revision").
		WithArgs("line-a").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("INSERT INTO audit_logs").
		WithArgs(sqlmock.AnyArg(), "sched-1", "production.start", "ORD-1", "").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	ord, err := store.StartProduction(productionStartRequest{OrderID: "ORD-1"}, claims)
	if err != nil {
		t.Fatalf("unexpected StartProduction error: %v", err)
	}
	if ord.Status != domain.StatusInProgress {
		t.Errorf("expected status '生產中', got %v", ord.Status)
	}
}

func TestPostgresStore_ConfirmProduction_Initial(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()
	mock.MatchExpectationsInOrder(false)

	store := &PostgresStore{
		MemoryStore: NewMemoryStore(),
		db:          db,
	}

	tNow := time.Now().UTC()

	mockOrder := func(id, lineID, status string) {
		rows := sqlmock.NewRows([]string{
			"id", "customer", "line_id", "quantity", "priority", "status", "due_date", "note", "created_by",
			"source_order", "rejection_reason", "rejected_by", "rejected_at", "created_at", "updated_at",
		}).AddRow(id, "ACME", lineID, 100, string(domain.PriorityLow), status, tNow, "", "sales-1", "", "", "", nil, tNow, tNow)
		mock.ExpectQuery("SELECT id, customer, line_id.* FROM orders WHERE id = \\$1").
			WithArgs(id).
			WillReturnRows(rows)
	}

	claims := auth.Claims{
		Role:    domain.RoleScheduler,
		LineID:  "line-a",
		Subject: "sched-1",
	}

	// 0. order not found
	mock.ExpectQuery("SELECT id, customer, line_id.* FROM orders WHERE id = \\$1").
		WithArgs("ORD-999").
		WillReturnError(sql.ErrNoRows)
	_, err = store.ConfirmProduction(productionConfirmRequest{OrderID: "ORD-999"}, claims)
	if err == nil || err.Error() != "order not found" {
		t.Errorf("expected 'order not found' error, got %v", err)
	}

	// 1. cannot confirm another production line
	mockOrder("ORD-1", "line-b", string(domain.StatusInProgress))
	_, err = store.ConfirmProduction(productionConfirmRequest{OrderID: "ORD-1"}, claims)
	if err == nil || err.Error() != "cannot confirm another production line" {
		t.Errorf("expected line mismatch error, got %v", err)
	}
}

func TestPostgresStore_ConfirmProduction_Validation(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()
	mock.MatchExpectationsInOrder(false)

	store := &PostgresStore{
		MemoryStore: NewMemoryStore(),
		db:          db,
	}

	tNow := time.Now().UTC()

	mockOrder := func(id, lineID, status string) {
		rows := sqlmock.NewRows([]string{
			"id", "customer", "line_id", "quantity", "priority", "status", "due_date", "note", "created_by",
			"source_order", "rejection_reason", "rejected_by", "rejected_at", "created_at", "updated_at",
		}).AddRow(id, "ACME", lineID, 100, string(domain.PriorityLow), status, tNow, "", "sales-1", "", "", "", nil, tNow, tNow)
		mock.ExpectQuery("SELECT id, customer, line_id.* FROM orders WHERE id = \\$1").
			WithArgs(id).
			WillReturnRows(rows)
	}

	claims := auth.Claims{
		Role:    domain.RoleScheduler,
		LineID:  "line-a",
		Subject: "sched-1",
	}

	// 2. only in-progress orders can be confirmed
	mockOrder("ORD-1", "line-a", string(domain.StatusPending))
	_, err = store.ConfirmProduction(productionConfirmRequest{OrderID: "ORD-1"}, claims)
	if err == nil || err.Error() != "only in-progress orders can be confirmed" {
		t.Errorf("expected status mismatch error, got %v", err)
	}

	// 3. producedQuantity must be greater than zero
	mockOrder("ORD-1", "line-a", string(domain.StatusInProgress))
	_, err = store.ConfirmProduction(productionConfirmRequest{OrderID: "ORD-1", ProducedQuantity: 0}, claims)
	if err == nil || err.Error() != "producedQuantity must be greater than zero" {
		t.Errorf("expected quantity error, got %v", err)
	}

	// 4. productionDate must use YYYY-MM-DD
	mockOrder("ORD-1", "line-a", string(domain.StatusInProgress))
	_, err = store.ConfirmProduction(productionConfirmRequest{OrderID: "ORD-1", ProducedQuantity: 50, ProductionDate: "invalid-date"}, claims)
	if err == nil || err.Error() != "productionDate must use YYYY-MM-DD" {
		t.Errorf("expected invalid date error, got %v", err)
	}
}

func TestPostgresStore_ConfirmProduction_Allocation(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()
	mock.MatchExpectationsInOrder(false)

	store := &PostgresStore{
		MemoryStore: NewMemoryStore(),
		db:          db,
	}

	tNow := time.Now().UTC()

	mockOrder := func(id, lineID, status string) {
		rows := sqlmock.NewRows([]string{
			"id", "customer", "line_id", "quantity", "priority", "status", "due_date", "note", "created_by",
			"source_order", "rejection_reason", "rejected_by", "rejected_at", "created_at", "updated_at",
		}).AddRow(id, "ACME", lineID, 100, string(domain.PriorityLow), status, tNow, "", "sales-1", "", "", "", nil, tNow, tNow)
		mock.ExpectQuery("SELECT id, customer, line_id.* FROM orders WHERE id = \\$1").
			WithArgs(id).
			WillReturnRows(rows)
	}

	claims := auth.Claims{
		Role:    domain.RoleScheduler,
		LineID:  "line-a",
		Subject: "sched-1",
	}

	// 5. scheduled allocation not found for productionDate (sql.ErrNoRows)
	mockOrder("ORD-1", "line-a", string(domain.StatusInProgress))
	mock.ExpectQuery("SELECT order_id, line_id, allocation_date, quantity, priority, locked, COALESCE").
		WillReturnError(sql.ErrNoRows)
	_, err = store.ConfirmProduction(productionConfirmRequest{OrderID: "ORD-1", ProducedQuantity: 50, ProductionDate: "2026-06-01"}, claims)
	if err == nil || err.Error() != "scheduled allocation not found for productionDate" {
		t.Errorf("expected allocation not found error, got %v", err)
	}

	// 6. general DB error on allocation select
	mockOrder("ORD-1", "line-a", string(domain.StatusInProgress))
	mock.ExpectQuery("SELECT order_id, line_id, allocation_date, quantity, priority, locked, COALESCE").
		WillReturnError(errors.New("db error allocation"))
	_, err = store.ConfirmProduction(productionConfirmRequest{OrderID: "ORD-1", ProducedQuantity: 50, ProductionDate: "2026-06-01"}, claims)
	if err == nil || err.Error() != "db error allocation" {
		t.Errorf("expected db error, got %v", err)
	}

	// 7. productionDate has already been confirmed
	mockOrder("ORD-1", "line-a", string(domain.StatusInProgress))
	allocRows := sqlmock.NewRows([]string{"order_id", "line_id", "allocation_date", "quantity", "priority", "locked", "status"}).
		AddRow("ORD-1", "line-a", tNow, 100, string(domain.PriorityLow), true, string(domain.StatusCompleted))
	mock.ExpectQuery("SELECT order_id, line_id, allocation_date, quantity, priority, locked, COALESCE").
		WillReturnRows(allocRows)
	_, err = store.ConfirmProduction(productionConfirmRequest{OrderID: "ORD-1", ProducedQuantity: 50, ProductionDate: "2026-06-01"}, claims)
	if err == nil || err.Error() != "productionDate has already been confirmed" {
		t.Errorf("expected already confirmed error, got %v", err)
	}

	// 8. producedQuantity cannot exceed scheduled allocation quantity
	mockOrder("ORD-1", "line-a", string(domain.StatusInProgress))
	allocRows2 := sqlmock.NewRows([]string{"order_id", "line_id", "allocation_date", "quantity", "priority", "locked", "status"}).
		AddRow("ORD-1", "line-a", tNow, 40, string(domain.PriorityLow), false, string(domain.StatusInProgress))
	mock.ExpectQuery("SELECT order_id, line_id, allocation_date, quantity, priority, locked, COALESCE").
		WillReturnRows(allocRows2)
	_, err = store.ConfirmProduction(productionConfirmRequest{OrderID: "ORD-1", ProducedQuantity: 50, ProductionDate: "2026-06-01"}, claims)
	if err == nil || err.Error() != "producedQuantity cannot exceed scheduled allocation quantity" {
		t.Errorf("expected quantity limit exceeded error, got %v", err)
	}
}

func TestPostgresStore_ConfirmProduction_CompleteSuccess(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()
	mock.MatchExpectationsInOrder(false)

	store := &PostgresStore{
		MemoryStore: NewMemoryStore(),
		db:          db,
	}

	productionDate := mustAPIDate(t, "2026-06-01")
	orderUpdatedAt := productionDate.Add(-time.Hour)
	mock.ExpectQuery("SELECT id, customer, line_id.* FROM orders WHERE id = \\$1").
		WithArgs("ORD-100").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "customer", "line_id", "quantity", "priority", "status", "due_date", "note", "created_by",
			"source_order", "rejection_reason", "rejected_by", "rejected_at", "created_at", "updated_at",
		}).AddRow("ORD-100", "ACME", "A", 100, string(domain.PriorityHigh), string(domain.StatusInProgress), productionDate, "", "sales-1", "", "", "", nil, orderUpdatedAt, orderUpdatedAt))
	mock.ExpectQuery("SELECT order_id, line_id, allocation_date, quantity, priority, locked, COALESCE").
		WithArgs("ORD-100", productionDate).
		WillReturnRows(sqlmock.NewRows([]string{"order_id", "line_id", "allocation_date", "quantity", "priority", "locked", "status"}).
			AddRow("ORD-100", "A", productionDate, 100, string(domain.PriorityHigh), true, string(domain.StatusInProgress)))
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE orders SET status = '已完成'").
		WithArgs("ORD-100", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("UPDATE schedule_allocations SET locked = TRUE, status = '已完成'").
		WithArgs("ORD-100", productionDate).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("UPDATE production_lines SET schedule_revision").
		WithArgs("A").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("INSERT INTO audit_logs").
		WithArgs(sqlmock.AnyArg(), "sched-1", "production.confirm.complete", "ORD-100", "").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	resp, err := store.ConfirmProduction(productionConfirmRequest{
		OrderID:          "ORD-100",
		ProducedQuantity: 100,
		ProductionDate:   "2026-06-01",
	}, auth.Claims{Subject: "sched-1", Role: domain.RoleScheduler, LineID: "A"})
	if err != nil {
		t.Fatalf("ConfirmProduction complete failed: %v", err)
	}
	if resp.Order.Status != domain.StatusCompleted || resp.Remainder != nil {
		t.Fatalf("expected completed order without remainder, got %+v", resp)
	}
}

func TestPostgresStore_ConfirmProduction_PartialSuccessCreatesRemainder(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()
	mock.MatchExpectationsInOrder(false)

	store := &PostgresStore{
		MemoryStore: NewMemoryStore(),
		db:          db,
	}

	productionDate := mustAPIDate(t, "2026-06-01")
	orderUpdatedAt := productionDate.Add(-time.Hour)
	mock.ExpectQuery("SELECT id, customer, line_id.* FROM orders WHERE id = \\$1").
		WithArgs("ORD-200").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "customer", "line_id", "quantity", "priority", "status", "due_date", "note", "created_by",
			"source_order", "rejection_reason", "rejected_by", "rejected_at", "created_at", "updated_at",
		}).AddRow("ORD-200", "ACME", "A", 100, string(domain.PriorityLow), string(domain.StatusInProgress), productionDate, "note", "sales-1", "", "", "", nil, orderUpdatedAt, orderUpdatedAt))
	mock.ExpectQuery("SELECT order_id, line_id, allocation_date, quantity, priority, locked, COALESCE").
		WithArgs("ORD-200", productionDate).
		WillReturnRows(sqlmock.NewRows([]string{"order_id", "line_id", "allocation_date", "quantity", "priority", "locked", "status"}).
			AddRow("ORD-200", "A", productionDate, 100, string(domain.PriorityLow), true, string(domain.StatusInProgress)))
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT EXISTS \\(SELECT 1 FROM orders WHERE id = \\$1\\)").
		WithArgs("ORD-200-1").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectExec("UPDATE orders SET status = '已完成', quantity = \\$2").
		WithArgs("ORD-200", 40, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("INSERT INTO orders").
		WithArgs("ORD-200-1", "ACME", "A", 60, domain.PriorityLow, domain.StatusPending, productionDate, "note", "sales-1", "ORD-200", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("UPDATE schedule_allocations SET locked = TRUE, status = '已完成'").
		WithArgs("ORD-200", productionDate).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("DELETE FROM schedule_allocations WHERE order_id = \\$1 AND allocation_date <> \\$2").
		WithArgs("ORD-200", productionDate).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("UPDATE production_lines SET schedule_revision").
		WithArgs("A").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("INSERT INTO audit_logs").
		WithArgs(sqlmock.AnyArg(), "sched-1", "production.confirm.partial", "ORD-200", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	resp, err := store.ConfirmProduction(productionConfirmRequest{
		OrderID:          "ORD-200",
		ProducedQuantity: 40,
		ProductionDate:   "2026-06-01",
	}, auth.Claims{Subject: "sched-1", Role: domain.RoleScheduler, LineID: "A"})
	if err != nil {
		t.Fatalf("ConfirmProduction partial failed: %v", err)
	}
	if resp.Order.Quantity != 40 || resp.Order.Status != domain.StatusCompleted {
		t.Fatalf("expected original order completed with produced quantity, got %+v", resp.Order)
	}
	if resp.Remainder == nil || resp.Remainder.ID != "ORD-200-1" || resp.Remainder.Quantity != 60 || resp.Remainder.Status != domain.StatusPending {
		t.Fatalf("expected pending remainder, got %+v", resp.Remainder)
	}
}

func TestPostgresStore_ConfirmPreviewOrderTxUpdatesExistingOrder(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()
	mock.MatchExpectationsInOrder(false)

	store := &PostgresStore{
		MemoryStore: NewMemoryStore(),
		db:          db,
	}
	claims := auth.Claims{Subject: "sales-1", Role: domain.RoleSales}
	draft := createOrderRequest{
		ID:       "ORD-EXIST",
		Customer: "ACME",
		LineID:   "A",
		Quantity: 500,
		Priority: domain.PriorityLow,
		DueDate:  "2026-06-03",
	}

	createdAt := time.Now().Add(-10 * time.Minute)

	// Mock SELECT created_by, status, created_at FROM orders WHERE id = $1
	mock.ExpectQuery("SELECT created_by, status, created_at FROM orders WHERE id =").
		WithArgs("ORD-EXIST").
		WillReturnRows(sqlmock.NewRows([]string{"created_by", "status", "created_at"}).
			AddRow("sales-1", "待排程", createdAt))

	mock.ExpectQuery("SELECT id, name, capacity_per_day").
		WithArgs("A", "Asia/Taipei").
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "capacity_per_day", "timezone", "schedule_revision"}).
			AddRow("A", "Line A", 1000, "Asia/Taipei", 1))

	mock.ExpectBegin()
	// Mock UPDATE orders SET quantity = $2... WHERE id = $1
	mock.ExpectExec("UPDATE orders SET quantity =").WillReturnResult(sqlmock.NewResult(1, 1))
	// Mock INSERT INTO audit_logs for order.resubmit
	mock.ExpectExec("INSERT INTO audit_logs").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("UPDATE production_lines").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("DELETE FROM schedule_previews").
		WithArgs("preview-1", "sales-1", domain.RoleSales).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	order, err := store.confirmPreviewOrderTx("preview-1", draft, nil, false, claims)
	if err != nil {
		t.Fatalf("confirmPreviewOrderTx failed: %v", err)
	}
	if order.ID != "ORD-EXIST" || order.Status != domain.StatusPending {
		t.Fatalf("expected updated pending order, got %+v", order)
	}
}
