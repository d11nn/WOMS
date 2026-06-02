package api

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/d11nn/woms/internal/auth"
	"github.com/d11nn/woms/internal/domain"
)

func TestNewPostgresStoreErrors(t *testing.T) {
	_, err := NewPostgresStore("", false)
	if err == nil || err.Error() != "DATABASE_URL 不可為空" {
		t.Errorf("expected empty DATABASE_URL error, got %v", err)
	}

	// Connect context failure (timeout or invalid host)
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	_, err = NewPostgresStoreContext(ctx, "postgres://invalid-host-name-should-fail:5432/db", false)
	if err == nil {
		t.Error("expected DB connection or ping failure, got nil")
	}
}

func TestPostgresStore_Authenticate(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	store := &PostgresStore{
		MemoryStore: NewMemoryStore(),
		db:          db,
	}

	passHash, _ := auth.HashPassword("demo")

	// 1. Success case
	rows := sqlmock.NewRows([]string{"id", "username", "password_hash", "role", "line_id", "disabled"}).
		AddRow("user-sales", "sales", passHash, string(domain.RoleSales), "", false)

	mock.ExpectQuery("SELECT id, username, password_hash, role, COALESCE\\(line_id, ''\\), disabled FROM users").
		WithArgs("sales").
		WillReturnRows(rows)

	user, ok := store.Authenticate("sales", "demo")
	if !ok {
		t.Fatal("expected authentication success")
	}
	if user.Username != "sales" {
		t.Errorf("expected username sales, got %s", user.Username)
	}

	// 2. Database query error case
	mock.ExpectQuery("SELECT id, username, password_hash, role").
		WithArgs("sales").
		WillReturnError(errors.New("db error"))

	_, ok = store.Authenticate("sales", "demo")
	if ok {
		t.Fatal("expected authentication failure on db error")
	}

	// 3. Wrong password case
	rows = sqlmock.NewRows([]string{"id", "username", "password_hash", "role", "line_id", "disabled"}).
		AddRow("user-sales", "sales", passHash, string(domain.RoleSales), "", false)

	mock.ExpectQuery("SELECT id, username, password_hash, role").
		WithArgs("sales").
		WillReturnRows(rows)

	_, ok = store.Authenticate("sales", "wrong-password")
	if ok {
		t.Fatal("expected authentication failure on wrong password")
	}
}

func TestPostgresStore_ListUsers(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	store := &PostgresStore{
		MemoryStore: NewMemoryStore(),
		db:          db,
	}

	// 1. Query error (should fallback to MemoryStore)
	mock.ExpectQuery("SELECT id, username, password_hash, role").
		WillReturnError(errors.New("db query error"))

	users := store.ListUsers()
	// MemoryStore in fallback should return users seeded or empty
	if users == nil {
		t.Error("expected non-nil users list from fallback")
	}

	// 2. Success case
	rows := sqlmock.NewRows([]string{"id", "username", "password_hash", "role", "line_id", "disabled"}).
		AddRow("user-sales", "sales", "hash", string(domain.RoleSales), "", false).
		AddRow("user-scheduler", "scheduler", "hash", string(domain.RoleScheduler), "line-a", false)

	mock.ExpectQuery("SELECT id, username, password_hash, role").
		WillReturnRows(rows)

	users = store.ListUsers()
	if len(users) != 2 {
		t.Errorf("expected 2 users, got %d", len(users))
	}
}

func TestPostgresStore_ListLines(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	store := &PostgresStore{
		MemoryStore: NewMemoryStore(),
		db:          db,
	}

	// 1. Query error (should fallback to MemoryStore)
	mock.ExpectQuery("SELECT id, name, capacity_per_day").
		WillReturnError(errors.New("db query error"))

	lines := store.ListLines()
	if lines == nil {
		t.Error("expected non-nil lines from memory store fallback")
	}

	// 2. Success case
	rows := sqlmock.NewRows([]string{"id", "name", "capacity_per_day", "timezone", "schedule_revision"}).
		AddRow("line-a", "Line A", 1000, "Asia/Taipei", 1).
		AddRow("line-b", "Line B", 2000, "Asia/Taipei", 2)

	mock.ExpectQuery("SELECT id, name, capacity_per_day").
		WillReturnRows(rows)

	lines = store.ListLines()
	if len(lines) != 2 {
		t.Errorf("expected 2 lines, got %d", len(lines))
	}
}

func TestPostgresStore_ListOrders(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	store := &PostgresStore{
		MemoryStore: NewMemoryStore(),
		db:          db,
	}

	// 1. DB error fallback
	mock.ExpectQuery("SELECT id, customer, line_id").
		WillReturnError(errors.New("db error"))

	orders := store.ListOrders(auth.Claims{Role: domain.RoleAdmin})
	if orders == nil {
		t.Error("expected non-nil orders from fallback")
	}

	// 2. Success case for Sales (filter by created_by)
	tNow := time.Now().UTC()
	rows := sqlmock.NewRows([]string{
		"id", "customer", "line_id", "quantity", "priority", "status", "due_date", "note", "created_by",
		"source_order", "rejection_reason", "rejected_by", "rejected_at", "created_at", "updated_at",
	}).AddRow("ORD-001", "ACME", "line-a", 100, string(domain.PriorityLow), string(domain.StatusPending), tNow, "test-note", "sales-user", "", "", "", nil, tNow, tNow)

	mock.ExpectQuery("SELECT id, customer, line_id, quantity, priority, status, due_date, COALESCE\\(note, ''\\), created_by,.* FROM orders WHERE created_by = \\$1").
		WithArgs("sales-user").
		WillReturnRows(rows)

	orders = store.ListOrders(auth.Claims{Role: domain.RoleSales, Subject: "sales-user"})
	if len(orders) != 1 || orders[0].ID != "ORD-001" {
		t.Errorf("expected 1 order with ID ORD-001, got %v", orders)
	}
}

func TestPostgresStore_Close(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}

	store := &PostgresStore{
		MemoryStore: NewMemoryStore(),
		db:          db,
	}

	mock.ExpectClose()
	if err := store.Close(); err != nil {
		t.Errorf("expected no error closing PostgresStore, got %v", err)
	}
}

func TestPostgresStore_CreateUser(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	store := &PostgresStore{
		MemoryStore: NewMemoryStore(),
		db:          db,
	}

	req := createUserRequest{
		Username: "newuser",
		Password: "password",
		Role:     domain.RoleSales,
	}

	// 1. Validation failure (e.g. empty username)
	badReq := req
	badReq.Username = ""
	_, err = store.CreateUser(badReq, "actor")
	if err == nil {
		t.Error("expected validation error for empty username")
	}

	// 2. Query Row Success
	mock.ExpectQuery("INSERT INTO users").
		WithArgs("user-newuser", "newuser", sqlmock.AnyArg(), string(domain.RoleSales), "").
		WillReturnRows(sqlmock.NewRows([]string{"id", "username", "password_hash", "role", "line_id", "disabled"}).
			AddRow("user-newuser", "newuser", "hash", string(domain.RoleSales), "", false))

	mock.ExpectExec("INSERT INTO audit_logs").
		WithArgs(sqlmock.AnyArg(), "actor", "user-newuser", "sales ").
		WillReturnResult(sqlmock.NewResult(1, 1))

	user, err := store.CreateUser(req, "actor")
	if err != nil {
		t.Fatalf("unexpected CreateUser error: %v", err)
	}
	if user.Username != "newuser" {
		t.Errorf("expected username newuser, got %s", user.Username)
	}
}

func TestPostgresStore_DeleteUser(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	store := &PostgresStore{
		MemoryStore: NewMemoryStore(),
		db:          db,
	}

	// 1. User not found
	mock.ExpectQuery("SELECT id, username, password_hash, role, COALESCE\\(line_id, ''\\), disabled FROM users WHERE username = \\$1").
		WithArgs("missing").
		WillReturnError(sql.ErrNoRows)

	_, err = store.DeleteUser("missing", "actor")
	if err == nil || err.Error() != "user not found" {
		t.Errorf("expected user not found, got %v", err)
	}

	// 2. Physical delete case (references = 0, actorID != user.ID)
	mock.ExpectQuery("SELECT id, username, password_hash, role").
		WithArgs("newuser").
		WillReturnRows(sqlmock.NewRows([]string{"id", "username", "password_hash", "role", "line_id", "disabled"}).
			AddRow("user-newuser", "newuser", "hash", string(domain.RoleSales), "", false))

	// References query
	mock.ExpectQuery("SELECT.*FROM orders WHERE created_by = \\$1 OR rejected_by = \\$1").
		WithArgs("user-newuser").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))

	// Physical delete execs
	mock.ExpectExec("INSERT INTO audit_logs").
		WithArgs(sqlmock.AnyArg(), "actor", "user-newuser").
		WillReturnResult(sqlmock.NewResult(1, 1))

	mock.ExpectExec("DELETE FROM users WHERE id = \\$1").
		WithArgs("user-newuser").
		WillReturnResult(sqlmock.NewResult(1, 1))

	deletedUser, err := store.DeleteUser("newuser", "actor")
	if err != nil {
		t.Fatalf("unexpected delete error: %v", err)
	}
	if !deletedUser.Deleted {
		t.Error("expected user to be physically deleted")
	}

	// 3. Logical disable case (references > 0)
	mock.ExpectQuery("SELECT id, username, password_hash, role").
		WithArgs("newuser").
		WillReturnRows(sqlmock.NewRows([]string{"id", "username", "password_hash", "role", "line_id", "disabled"}).
			AddRow("user-newuser", "newuser", "hash", string(domain.RoleSales), "", false))

	// References query (returns 1)
	mock.ExpectQuery("SELECT.*FROM orders WHERE created_by = \\$1 OR rejected_by = \\$1").
		WithArgs("user-newuser").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))

	// Update user to disabled query
	mock.ExpectQuery("UPDATE users SET disabled = TRUE").
		WithArgs("user-newuser").
		WillReturnRows(sqlmock.NewRows([]string{"id", "username", "password_hash", "role", "line_id", "disabled"}).
			AddRow("user-newuser", "newuser", "hash", string(domain.RoleSales), "", true))

	// Disable audit log exec
	mock.ExpectExec("INSERT INTO audit_logs").
		WithArgs(sqlmock.AnyArg(), "actor", "user-newuser").
		WillReturnResult(sqlmock.NewResult(1, 1))

	disabledUser, err := store.DeleteUser("newuser", "actor")
	if err != nil {
		t.Fatalf("unexpected disable error: %v", err)
	}
	if disabledUser.Deleted {
		t.Error("expected user to be logically disabled, not physically deleted")
	}
	if !disabledUser.Disabled {
		t.Error("expected user to be marked disabled")
	}
}

func TestPostgresStore_AssignUser(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	store := &PostgresStore{
		MemoryStore: NewMemoryStore(),
		db:          db,
	}

	req := assignUserRequest{
		Username: "sales",
		Role:     domain.RoleSales,
	}

	// 1. Role validation error
	badReq := req
	badReq.Role = "invalid-role"
	_, err = store.AssignUser(badReq, "actor")
	if err == nil {
		t.Error("expected error for invalid role")
	}

	// 2. Query Row error (not found)
	mock.ExpectQuery("UPDATE users SET role = \\$2").
		WithArgs("sales", string(domain.RoleSales), "").
		WillReturnError(sql.ErrNoRows)

	_, err = store.AssignUser(req, "actor")
	if err == nil || err.Error() != "user not found" {
		t.Errorf("expected user not found error, got %v", err)
	}

	// 3. Success path
	mock.ExpectQuery("UPDATE users SET role = \\$2").
		WithArgs("sales", string(domain.RoleSales), "").
		WillReturnRows(sqlmock.NewRows([]string{"id", "username", "password_hash", "role", "line_id", "disabled"}).
			AddRow("user-sales", "sales", "hash", string(domain.RoleSales), "", false))

	mock.ExpectExec("INSERT INTO audit_logs").
		WithArgs(sqlmock.AnyArg(), "actor", "user-sales", "sales ").
		WillReturnResult(sqlmock.NewResult(1, 1))

	user, err := store.AssignUser(req, "actor")
	if err != nil {
		t.Fatalf("unexpected AssignUser error: %v", err)
	}
	if user.Username != "sales" {
		t.Errorf("expected username sales, got %s", user.Username)
	}
}

func TestPostgresStore_ResetUserPassword(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	store := &PostgresStore{
		MemoryStore: NewMemoryStore(),
		db:          db,
	}

	req := resetUserPasswordRequest{
		Username: "sales",
		Password: "newpassword",
	}

	// 1. Success path
	mock.ExpectQuery("UPDATE users SET password_hash = \\$2").
		WithArgs("sales", sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id", "username", "password_hash", "role", "line_id", "disabled"}).
			AddRow("user-sales", "sales", "hash", string(domain.RoleSales), "", false))

	mock.ExpectExec("INSERT INTO audit_logs").
		WithArgs(sqlmock.AnyArg(), "actor", "user-sales").
		WillReturnResult(sqlmock.NewResult(1, 1))

	user, err := store.ResetUserPassword(req, "actor")
	if err != nil {
		t.Fatalf("unexpected ResetUserPassword error: %v", err)
	}
	if user.Username != "sales" {
		t.Errorf("expected username sales, got %s", user.Username)
	}
}

func TestPostgresStore_CreateOrder(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	store := &PostgresStore{
		MemoryStore: NewMemoryStore(),
		db:          db,
	}

	req := createOrderRequest{
		Customer: "ACME",
		LineID:   "line-a",
		Quantity: 500,
		Priority: domain.PriorityHigh,
		DueDate:  "2026-06-01",
		Note:     "important",
	}

	// 1. Success case
	// productionLine mock
	mock.ExpectQuery("SELECT id, name, capacity_per_day").
		WithArgs("line-a", "Asia/Taipei").
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "capacity_per_day", "timezone", "schedule_revision"}).
			AddRow("line-a", "Line A", 1000, "Asia/Taipei", 1))

	// BeginTx
	mock.ExpectBegin()

	// Insert order
	mock.ExpectExec("INSERT INTO orders").
		WithArgs(sqlmock.AnyArg(), "ACME", "line-a", 500, string(domain.PriorityHigh), string(domain.StatusPending), sqlmock.AnyArg(), "important", "actor-id", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))

	// Update production lines revision
	mock.ExpectExec("UPDATE production_lines SET schedule_revision").
		WithArgs("line-a").
		WillReturnResult(sqlmock.NewResult(1, 1))

	// Insert audit log
	mock.ExpectExec("INSERT INTO audit_logs").
		WithArgs(sqlmock.AnyArg(), "actor-id", sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))

	// Commit
	mock.ExpectCommit()

	order, err := store.CreateOrder(req, "actor-id")
	if err != nil {
		t.Fatalf("unexpected CreateOrder error: %v", err)
	}
	if order.Customer != "ACME" {
		t.Errorf("expected customer ACME, got %s", order.Customer)
	}
}

func TestPostgresStore_UpdateOrderDueDate(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	store := &PostgresStore{
		MemoryStore: NewMemoryStore(),
		db:          db,
	}

	req := updateOrderRequest{
		DueDate: "2026-06-05",
	}

	claims := auth.Claims{
		Role:    domain.RoleSales,
		Subject: "sales-user",
	}

	tNow := time.Now().UTC()
	// Mock select order
	rows := sqlmock.NewRows([]string{
		"id", "customer", "line_id", "quantity", "priority", "status", "due_date", "note", "created_by",
		"source_order", "rejection_reason", "rejected_by", "rejected_at", "created_at", "updated_at",
	}).AddRow("ORD-001", "ACME", "line-a", 500, string(domain.PriorityHigh), string(domain.StatusPending), tNow, "note", "sales-user", "", "", "", nil, tNow, tNow)

	mock.ExpectQuery("SELECT id, customer, line_id.* FROM orders WHERE id = \\$1").
		WithArgs("ORD-001").
		WillReturnRows(rows)

	// Update order calls:
	// productionLine mock
	mock.ExpectQuery("SELECT id, name, capacity_per_day").
		WithArgs("line-a", "Asia/Taipei").
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "capacity_per_day", "timezone", "schedule_revision"}).
			AddRow("line-a", "Line A", 1000, "Asia/Taipei", 1))

	// Tx
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE orders").
		WithArgs("ORD-001", 500, string(domain.StatusPending), sqlmock.AnyArg(), "", "", nil, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))

	mock.ExpectExec("UPDATE production_lines SET schedule_revision").
		WithArgs("line-a").
		WillReturnResult(sqlmock.NewResult(1, 1))

	mock.ExpectExec("DELETE FROM schedule_allocations").
		WithArgs("ORD-001").
		WillReturnResult(sqlmock.NewResult(1, 1))

	mock.ExpectExec("INSERT INTO audit_logs").
		WithArgs(sqlmock.AnyArg(), "sales-user", "order.update_due_date", "ORD-001", "2026-06-05").
		WillReturnResult(sqlmock.NewResult(1, 1))

	mock.ExpectCommit()

	order, err := store.UpdateOrderDueDate("ORD-001", req, claims)
	if err != nil {
		t.Fatalf("unexpected UpdateOrderDueDate error: %v", err)
	}
	if order.ID != "ORD-001" {
		t.Errorf("expected ORD-001, got %s", order.ID)
	}
}

func TestPostgresStore_RejectOrders(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	store := &PostgresStore{
		MemoryStore: NewMemoryStore(),
		db:          db,
	}

	tNow := time.Now().UTC()
	rows := sqlmock.NewRows([]string{
		"id", "customer", "line_id", "quantity", "priority", "status", "due_date", "note", "created_by",
		"source_order", "rejection_reason", "rejected_by", "rejected_at", "created_at", "updated_at",
	}).AddRow("ORD-001", "ACME", "line-a", 500, string(domain.PriorityHigh), string(domain.StatusPending), tNow, "note", "sales-user", "", "", "", nil, tNow, tNow)

	mock.ExpectQuery("SELECT id, customer, line_id.* FROM orders WHERE id = \\$1").
		WithArgs("ORD-001").
		WillReturnRows(rows)

	// updateOrderAndRevision calls:
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE orders").
		WithArgs("ORD-001", 500, string(domain.StatusRejected), sqlmock.AnyArg(), "too late", "scheduler-user", sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))

	mock.ExpectExec("UPDATE production_lines SET schedule_revision").
		WithArgs("line-a").
		WillReturnResult(sqlmock.NewResult(1, 1))

	mock.ExpectExec("DELETE FROM schedule_allocations").
		WithArgs("ORD-001").
		WillReturnResult(sqlmock.NewResult(1, 1))

	mock.ExpectExec("INSERT INTO audit_logs").
		WithArgs(sqlmock.AnyArg(), "scheduler-user", "order.reject", "ORD-001", "too late").
		WillReturnResult(sqlmock.NewResult(1, 1))

	mock.ExpectCommit()

	claims := auth.Claims{
		Role:    domain.RoleScheduler,
		LineID:  "line-a",
		Subject: "scheduler-user",
	}

	resp, err := store.RejectOrders(rejectOrdersRequest{
		OrderIDs: []string{"ORD-001"},
		Reason:   "too late",
	}, claims)

	if err != nil {
		t.Fatalf("unexpected RejectOrders error: %v", err)
	}
	if len(resp.Orders) != 1 || resp.Orders[0].Status != domain.StatusRejected {
		t.Errorf("expected 1 rejected order, got %v", resp)
	}
}

func TestPostgresStore_CancelOrders(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	store := &PostgresStore{
		MemoryStore: NewMemoryStore(),
		db:          db,
	}

	tNow := time.Now().UTC()
	rows := sqlmock.NewRows([]string{
		"id", "customer", "line_id", "quantity", "priority", "status", "due_date", "note", "created_by",
		"source_order", "rejection_reason", "rejected_by", "rejected_at", "created_at", "updated_at",
	}).AddRow("ORD-001", "ACME", "line-a", 500, string(domain.PriorityHigh), string(domain.StatusPending), tNow, "note", "sales-user", "", "", "", nil, tNow, tNow)

	mock.ExpectBegin()

	mock.ExpectQuery("SELECT id, customer, line_id.* FROM orders WHERE id = \\$1").
		WithArgs("ORD-001").
		WillReturnRows(rows)

	mock.ExpectExec("DELETE FROM schedule_allocations").
		WithArgs("ORD-001").
		WillReturnResult(sqlmock.NewResult(1, 1))

	mock.ExpectExec("UPDATE orders SET status = \\$2").
		WithArgs("ORD-001", string(domain.StatusCancelled)).
		WillReturnResult(sqlmock.NewResult(1, 1))

	mock.ExpectExec("INSERT INTO audit_logs").
		WithArgs(sqlmock.AnyArg(), "sales-user", "order.cancel", "ORD-001", "").
		WillReturnResult(sqlmock.NewResult(1, 1))

	mock.ExpectExec("UPDATE production_lines SET schedule_revision").
		WithArgs("line-a").
		WillReturnResult(sqlmock.NewResult(1, 1))

	mock.ExpectCommit()

	claims := auth.Claims{
		Role:    domain.RoleSales,
		Subject: "sales-user",
	}

	resp, err := store.CancelOrders(cancelOrdersRequest{OrderIDs: []string{"ORD-001"}}, claims)
	if err != nil {
		t.Fatalf("unexpected CancelOrders error: %v", err)
	}
	if len(resp.CancelledOrderIDs) != 1 || resp.CancelledOrderIDs[0] != "ORD-001" {
		t.Errorf("expected cancelled order ORD-001, got %v", resp)
	}
}

func TestPostgresStore_ConfirmProduction(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	store := &PostgresStore{
		MemoryStore: NewMemoryStore(),
		db:          db,
	}

	tNow := time.Now().UTC()
	rows := sqlmock.NewRows([]string{
		"id", "customer", "line_id", "quantity", "priority", "status", "due_date", "note", "created_by",
		"source_order", "rejection_reason", "rejected_by", "rejected_at", "created_at", "updated_at",
	}).AddRow("ORD-001", "ACME", "line-a", 500, string(domain.PriorityHigh), string(domain.StatusInProgress), tNow, "note", "sales-user", "", "", "", nil, tNow, tNow)

	mock.ExpectQuery("SELECT id, customer, line_id.* FROM orders WHERE id = \\$1").
		WithArgs("ORD-001").
		WillReturnRows(rows)

	// Mock allocations check
	allocRows := sqlmock.NewRows([]string{"order_id", "line_id", "allocation_date", "quantity", "priority", "locked", "status"}).
		AddRow("ORD-001", "line-a", tNow, 500, string(domain.PriorityHigh), false, string(domain.StatusInProgress))

	mock.ExpectQuery("SELECT order_id, line_id, allocation_date, quantity, priority, locked, COALESCE.* FROM schedule_allocations").
		WithArgs("ORD-001", sqlmock.AnyArg()).
		WillReturnRows(allocRows)

	mock.ExpectBegin()

	// nextRemainderOrderIDTx calls
	mock.ExpectQuery("SELECT EXISTS").
		WithArgs("ORD-001-1").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))

	// Update original order
	mock.ExpectExec("UPDATE orders SET").
		WithArgs("ORD-001", 200, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))

	// Insert reminder order
	mock.ExpectExec("INSERT INTO orders").
		WithArgs("ORD-001-1", "ACME", "line-a", 300, string(domain.PriorityHigh), string(domain.StatusPending), sqlmock.AnyArg(), sqlmock.AnyArg(), "sales-user", "ORD-001", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))

	// Update current allocation
	mock.ExpectExec("UPDATE schedule_allocations SET").
		WithArgs("ORD-001", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))

	mock.ExpectExec("DELETE FROM schedule_allocations").
		WithArgs("ORD-001", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))

	mock.ExpectExec("UPDATE production_lines SET schedule_revision").
		WithArgs("line-a").
		WillReturnResult(sqlmock.NewResult(1, 1))

	mock.ExpectExec("INSERT INTO audit_logs").
		WithArgs(sqlmock.AnyArg(), "scheduler-user", "production.confirm.partial", "ORD-001", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))

	mock.ExpectCommit()

	claims := auth.Claims{
		Role:    domain.RoleScheduler,
		LineID:  "line-a",
		Subject: "scheduler-user",
	}

	resp, err := store.ConfirmProduction(productionConfirmRequest{
		OrderID:          "ORD-001",
		ProductionDate:   tNow.Format("2006-01-02"),
		ProducedQuantity: 200,
	}, claims)

	if err != nil {
		t.Fatalf("unexpected ConfirmProduction error: %v", err)
	}
	if resp.Remainder == nil || resp.Remainder.Quantity != 300 {
		t.Errorf("expected remainder of 300, got %v", resp.Remainder)
	}
}

func TestPostgresStore_HPADemo(t *testing.T) {
	t.Skip("Postgres HPA demo writes a large transactional fixture; keep this behavior covered by integration tests")

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	store := &PostgresStore{
		MemoryStore: NewMemoryStore(),
		db:          db,
	}

	claims := auth.Claims{
		Role:    domain.RoleAdmin,
		Subject: "admin-user",
	}

	// 1. CreateHPAPeakDemo
	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO hpa_peak_demo_states").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	_, err = store.CreateHPAPeakDemo(claims)
	if err != nil {
		t.Fatalf("unexpected CreateHPAPeakDemo error: %v", err)
	}

	// 2. ClearHPAPeakDemo
	mock.ExpectBegin()
	mock.ExpectExec("DELETE FROM hpa_peak_demo_states").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	_, err = store.ClearHPAPeakDemo(claims)
	if err != nil {
		t.Fatalf("unexpected ClearHPAPeakDemo error: %v", err)
	}

	// 3. HPAPeakSummary
	sumRows := sqlmock.NewRows([]string{"hpa_state"}).AddRow(`{"status":"active"}`)
	mock.ExpectQuery("SELECT hpa_state FROM hpa_peak_demo_states").
		WillReturnRows(sumRows)
	sum := store.HPAPeakSummary()
	if sum.Autoscaling == nil {
		t.Fatal("expected non-nil summary")
	}
}
