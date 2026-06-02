package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/d11nn/woms/internal/auth"
	"github.com/d11nn/woms/internal/domain"
	"github.com/d11nn/woms/internal/scheduler"
	_ "github.com/lib/pq"
)

const userIdPrefix = "AUD-USER-"
const bumpProductionLineRevisionSQL = "UPDATE production_lines SET schedule_revision = schedule_revision + 1 WHERE id = $1"
const orderNotFoundMsg = "order not found"

type PostgresStore struct {
	*MemoryStore
	db *sql.DB
}

func NewPostgresStore(databaseURL string, seedDemo bool) (*PostgresStore, error) {
	return NewPostgresStoreContext(context.Background(), databaseURL, seedDemo)
}

func NewPostgresStoreContext(ctx context.Context, databaseURL string, seedDemo bool) (*PostgresStore, error) {
	if databaseURL == "" {
		return nil, errors.New("DATABASE_URL 不可為空")
	}
	db, err := sql.Open("postgres", databaseURL)
	if err != nil {
		return nil, err
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	store := &PostgresStore{MemoryStore: NewMemoryStore(), db: db}
	if err := store.applyMigrations(seedDemo); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *PostgresStore) Close() error {
	return s.db.Close()
}

func (s *PostgresStore) Authenticate(username, password string) (domain.User, bool) {
	var user domain.User
	err := s.db.QueryRow(`
		SELECT id, username, password_hash, role, COALESCE(line_id, ''), disabled
		FROM users
		WHERE username = $1 AND disabled = FALSE
	`, username).Scan(&user.ID, &user.Username, &user.PasswordHash, &user.Role, &user.LineID, &user.Disabled)
	if err != nil || !auth.VerifyPassword(user.PasswordHash, password) {
		return domain.User{}, false
	}
	return user, true
}

func (s *PostgresStore) ListUsers() []domain.User {
	rows, err := s.db.Query("SELECT id, username, password_hash, role, COALESCE(line_id, ''), disabled FROM users ORDER BY username")
	if err != nil {
		return s.MemoryStore.ListUsers()
	}
	defer rows.Close()
	users := []domain.User{}
	for rows.Next() {
		var user domain.User
		if err := rows.Scan(&user.ID, &user.Username, &user.PasswordHash, &user.Role, &user.LineID, &user.Disabled); err == nil {
			users = append(users, user)
		}
	}
	return users
}

func (s *PostgresStore) CreateUser(req createUserRequest, actorID string) (domain.User, error) {
	username := strings.TrimSpace(req.Username)
	if err := validateUsername(username); err != nil {
		return domain.User{}, err
	}
	lines := map[string]domain.ProductionLine{}
	for _, line := range s.ListLines() {
		lines[line.ID] = line
	}
	if err := validateUserRole(req.Role, req.LineID, lines); err != nil {
		return domain.User{}, err
	}
	if req.Role != domain.RoleScheduler {
		req.LineID = ""
	}
	passwordHash, err := auth.HashPassword(req.Password)
	if err != nil {
		return domain.User{}, err
	}
	user := domain.User{
		ID:           "user-" + username,
		Username:     username,
		PasswordHash: passwordHash,
		Role:         req.Role,
		LineID:       req.LineID,
	}
	err = s.db.QueryRow(`
		INSERT INTO users (id, username, password_hash, role, line_id, disabled)
		VALUES ($1, $2, $3, $4, NULLIF($5, ''), FALSE)
		RETURNING id, username, password_hash, role, COALESCE(line_id, ''), disabled
	`, user.ID, user.Username, user.PasswordHash, user.Role, user.LineID).Scan(&user.ID, &user.Username, &user.PasswordHash, &user.Role, &user.LineID, &user.Disabled)
	if err != nil {
		if strings.Contains(err.Error(), "duplicate") || strings.Contains(err.Error(), "unique") {
			return domain.User{}, errors.New("username already exists")
		}
		return domain.User{}, err
	}
	_, _ = s.db.Exec(`
		INSERT INTO audit_logs (id, actor_id, action, resource, reason, created_at)
		VALUES ($1, $2, 'user.create', $3, $4, NOW())
	`, auditID(userIdPrefix+user.ID), actorID, user.ID, string(req.Role)+" "+req.LineID)
	return user, nil
}

func (s *PostgresStore) ListLines() []domain.ProductionLine {
	rows, err := s.db.Query(`
		SELECT id, name, capacity_per_day, COALESCE(timezone, $1), schedule_revision
		FROM production_lines
		ORDER BY id
	`, defaultLineTimezone)
	if err != nil {
		return s.MemoryStore.ListLines()
	}
	defer rows.Close()
	lines := []domain.ProductionLine{}
	for rows.Next() {
		var line domain.ProductionLine
		if err := rows.Scan(&line.ID, &line.Name, &line.CapacityPerDay, &line.Timezone, &line.ScheduleRevision); err == nil {
			lines = append(lines, line)
		}
	}
	return lines
}

func (s *PostgresStore) ListOrders(claims auth.Claims) []domain.Order {
	query := `
		SELECT id, customer, line_id, quantity, priority, status, due_date, COALESCE(note, ''), created_by,
		       COALESCE(source_order, ''), COALESCE(rejection_reason, ''), COALESCE(rejected_by, ''), rejected_at, created_at, updated_at
	FROM orders`
	args := []any{}
	if claims.Role == domain.RoleScheduler {
		query += " WHERE line_id = $1"
		args = append(args, claims.LineID)
	} else if claims.Role == domain.RoleSales {
		query += " WHERE created_by = $1"
		args = append(args, claims.Subject)
	}
	query += " ORDER BY id"
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return s.MemoryStore.ListOrders(claims)
	}
	defer rows.Close()
	orders := []domain.Order{}
	for rows.Next() {
		order, err := scanOrder(rows)
		if err == nil {
			orders = append(orders, order)
		}
	}
	return orders
}

func (s *PostgresStore) CreateOrder(req createOrderRequest, actorID string) (domain.Order, error) {
	if err := validateOrderFields(req.Customer, req.Quantity, req.Note); err != nil {
		return domain.Order{}, err
	}
	if req.Priority == "" {
		req.Priority = domain.PriorityLow
	}
	if req.Priority != domain.PriorityLow && req.Priority != domain.PriorityHigh {
		return domain.Order{}, errors.New("priority must be low or high")
	}
	line, err := s.productionLine(req.LineID)
	if err != nil {
		return domain.Order{}, err
	}
	currentDate, err := currentDateInLineTimezone(line, nowUTC())
	if err != nil {
		return domain.Order{}, err
	}
	dueDate, err := validateOrderRequest(req, map[string]domain.ProductionLine{line.ID: line}, currentDate)
	if err != nil {
		return domain.Order{}, err
	}
	now := time.Now().UTC()
	id := orderIDFromTime(now)
	order := domain.Order{
		ID:        id,
		Customer:  req.Customer,
		LineID:    req.LineID,
		Quantity:  req.Quantity,
		Priority:  req.Priority,
		Status:    domain.StatusPending,
		DueDate:   dueDate,
		Note:      strings.TrimSpace(req.Note),
		CreatedBy: actorID,
		CreatedAt: now,
		UpdatedAt: now,
	}
	tx, err := s.db.Begin()
	if err != nil {
		return domain.Order{}, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`
		INSERT INTO orders (id, customer, line_id, quantity, priority, status, due_date, note, created_by, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $10)
	`, order.ID, order.Customer, order.LineID, order.Quantity, order.Priority, order.Status, order.DueDate, order.Note, order.CreatedBy, now); err != nil {
		return domain.Order{}, err
	}
	if _, err := tx.Exec(bumpProductionLineRevisionSQL, order.LineID); err != nil {
		return domain.Order{}, err
	}
	if _, err := tx.Exec(`
		INSERT INTO audit_logs (id, actor_id, action, resource, reason, created_at)
		VALUES ($1, $2, 'order.create', $3, '', $4)
	`, "AUD-"+order.ID, actorID, order.ID, now); err != nil {
		return domain.Order{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Order{}, err
	}
	return order, nil
}

func (s *PostgresStore) UpdateOrderDueDate(id string, req updateOrderRequest, claims auth.Claims) (domain.Order, error) {
	order, err := s.order(id)
	if err != nil {
		return domain.Order{}, err
	}
	if strings.TrimSpace(req.Note) != "" {
		return domain.Order{}, errors.New("note cannot be updated after order creation")
	}
	if err := canUpdateOrderDetails(order, claims); err != nil {
		return domain.Order{}, err
	}
	if err := applyOptionalQuantity(&order, req.Quantity); err != nil {
		return domain.Order{}, err
	}
	if req.DueDate != "" {
		line, err := s.productionLine(order.LineID)
		if err != nil {
			return domain.Order{}, err
		}
		currentDate, err := currentDateInLineTimezone(line, nowUTC())
		if err != nil {
			return domain.Order{}, err
		}
		if err := applyOptionalDueDate(&order, req.DueDate, currentDate); err != nil {
			return domain.Order{}, err
		}
	}
	order.UpdatedAt = time.Now().UTC()
	if err := s.updateOrderAndRevision(order, claims.Subject, "order.update_due_date", req.DueDate); err != nil {
		return domain.Order{}, err
	}
	return order, nil
}

func (s *PostgresStore) RejectOrders(req rejectOrdersRequest, claims auth.Claims) (rejectOrdersResponse, error) {
	if len(req.OrderIDs) == 0 {
		return rejectOrdersResponse{}, errors.New("orderIds is required")
	}
	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		return rejectOrdersResponse{}, errors.New("rejection reason is required")
	}
	if len([]rune(reason)) > 240 {
		return rejectOrdersResponse{}, errors.New("rejection reason must be 240 characters or fewer")
	}
	result := rejectOrdersResponse{Orders: []domain.Order{}}
	for _, id := range req.OrderIDs {
		order, err := s.order(id)
		if err != nil {
			return rejectOrdersResponse{}, err
		}
		if err := canRejectOrder(order, claims); err != nil {
			return rejectOrdersResponse{}, err
		}
		now := time.Now().UTC()
		order.Status = domain.StatusRejected
		order.RejectionReason = reason
		order.RejectedBy = claims.Subject
		order.RejectedAt = now
		order.UpdatedAt = now
		if err := s.updateOrderAndRevision(order, claims.Subject, "order.reject", reason); err != nil {
			return rejectOrdersResponse{}, err
		}
		result.Orders = append(result.Orders, order)
	}
	return result, nil
}

func (s *PostgresStore) ResubmitOrder(req resubmitOrderRequest, claims auth.Claims) (domain.Order, error) {
	order, err := s.order(req.OrderID)
	if err != nil {
		return domain.Order{}, err
	}
	if order.CreatedBy != claims.Subject {
		return domain.Order{}, errors.New("sales can resubmit only their own orders")
	}
	if !canSalesResubmitStatus(order.Status) {
		return domain.Order{}, errors.New("only pending or rejected orders can be resubmitted")
	}
	if strings.TrimSpace(req.Note) != "" {
		return domain.Order{}, errors.New("note cannot be updated after order creation")
	}
	if err := applyOptionalQuantity(&order, req.Quantity); err != nil {
		return domain.Order{}, err
	}
	if req.DueDate != "" {
		line, err := s.productionLine(order.LineID)
		if err != nil {
			return domain.Order{}, err
		}
		currentDate, err := currentDateInLineTimezone(line, nowUTC())
		if err != nil {
			return domain.Order{}, err
		}
		if err := applyOptionalDueDate(&order, req.DueDate, currentDate); err != nil {
			return domain.Order{}, err
		}
	}
	order.Status = domain.StatusPending
	resetRejectedState(&order)
	order.UpdatedAt = time.Now().UTC()
	if err := s.updateOrderAndRevision(order, claims.Subject, "order.resubmit", ""); err != nil {
		return domain.Order{}, err
	}
	return order, nil
}

func (s *PostgresStore) CreateDemoConflictOrders(req demoConflictRequest, claims auth.Claims) ([]domain.Order, error) {
	lineID, err := resolveDemoConflictLine(req, claims)
	if err != nil {
		return nil, err
	}
	line, err := s.productionLine(lineID)
	if err != nil {
		return nil, err
	}
	dueDate, req, err := validateDemoConflictRequest(req, line, nowUTC())
	if err != nil {
		return nil, err
	}
	return s.createDemoConflictOrdersTx(req, claims, lineID, dueDate)
}

func resolveDemoConflictLine(req demoConflictRequest, claims auth.Claims) (string, error) {
	lineID := req.LineID
	if lineID == "" && claims.Role == domain.RoleScheduler {
		lineID = claims.LineID
	}
	if claims.Role == domain.RoleScheduler && lineID != claims.LineID {
		return "", errors.New("cannot create demo orders for another production line")
	}
	return lineID, nil
}

func validateDemoConflictRequest(req demoConflictRequest, line domain.ProductionLine, now time.Time) (time.Time, demoConflictRequest, error) {
	if req.Count == 0 {
		req.Count = 6
	}
	if req.Count < 5 || req.Count > 20 {
		return time.Time{}, req, errors.New("count must be between 5 and 20")
	}
	currentDate, err := currentDateInLineTimezone(line, now)
	if err != nil {
		return time.Time{}, req, err
	}
	if req.DueDate == "" {
		req.DueDate = currentDate.AddDate(0, 0, 1).Format(dateLayout)
	}
	dueDate, err := validateFutureDueDate(req.DueDate, currentDate)
	if err != nil {
		return time.Time{}, req, err
	}
	return dueDate, req, nil
}

func (s *PostgresStore) createDemoConflictOrdersTx(req demoConflictRequest, claims auth.Claims, lineID string, dueDate time.Time) ([]domain.Order, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	now := time.Now().UTC()
	orders := make([]domain.Order, 0, req.Count)
	for index := 1; index <= req.Count; index++ {
		createdAt := now.Add(time.Duration(index) * time.Microsecond)
		order := domain.Order{
			ID:        orderIDFromTime(createdAt),
			Customer:  "Conflict Demo " + strconv.Itoa(index),
			LineID:    lineID,
			Quantity:  2500,
			Priority:  domain.PriorityLow,
			Status:    domain.StatusPending,
			DueDate:   dueDate,
			CreatedBy: claims.Subject,
			CreatedAt: createdAt,
			UpdatedAt: createdAt,
		}
		if err := insertDemoConflictOrderTx(tx, order, claims, req.DueDate); err != nil {
			return nil, err
		}
		orders = append(orders, order)
	}
	if _, err := tx.Exec(bumpProductionLineRevisionSQL, lineID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return orders, nil
}

func insertDemoConflictOrderTx(tx *sql.Tx, order domain.Order, claims auth.Claims, reason string) error {
	if _, err := tx.Exec(`
		INSERT INTO orders (id, customer, line_id, quantity, priority, status, due_date, note, created_by, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, '', $8, $9, $9)
	`, order.ID, order.Customer, order.LineID, order.Quantity, order.Priority, order.Status, order.DueDate, order.CreatedBy, order.CreatedAt); err != nil {
		return err
	}
	_, err := tx.Exec(`
		INSERT INTO audit_logs (id, actor_id, action, resource, reason, created_at)
		VALUES ($1, $2, 'order.create_demo_conflict', $3, $4, $5)
	`, "AUD-"+order.ID, claims.Subject, order.ID, reason, order.CreatedAt)
	return err
}

func (s *PostgresStore) CancelOrders(req cancelOrdersRequest, claims auth.Claims) (cancelOrdersResponse, error) {
	if len(req.OrderIDs) == 0 {
		return cancelOrdersResponse{}, errors.New("orderIds is required")
	}
	result := cancelOrdersResponse{}
	tx, err := s.db.Begin()
	if err != nil {
		return cancelOrdersResponse{}, err
	}
	defer tx.Rollback()
	revisions := map[string]bool{}
	for _, id := range req.OrderIDs {
		lineID, skipped, err := s.cancelOrderTx(tx, id, claims)
		if err != nil {
			return cancelOrdersResponse{}, err
		}
		if skipped {
			result.SkippedOrderIDs = append(result.SkippedOrderIDs, id)
			continue
		}
		revisions[lineID] = true
		result.CancelledOrderIDs = append(result.CancelledOrderIDs, id)
	}
	if err := bumpCancelledLineRevisionsTx(tx, revisions); err != nil {
		return cancelOrdersResponse{}, err
	}
	return result, tx.Commit()
}

func (s *PostgresStore) cancelOrderTx(tx *sql.Tx, id string, claims auth.Claims) (string, bool, error) {
	order, err := s.order(id)
	if err != nil {
		if isOrderLookupSkip(err) {
			return "", true, nil
		}
		return "", false, err
	}
	if order.Status == domain.StatusCancelled {
		return "", true, nil
	}
	if err := applyCancelOrderTx(tx, order, claims); err != nil {
		return "", false, err
	}
	return order.LineID, false, nil
}

func isOrderLookupSkip(err error) bool {
	return errors.Is(err, sql.ErrNoRows) || strings.Contains(err.Error(), "找不到") || strings.Contains(err.Error(), orderNotFoundMsg)
}

func applyCancelOrderTx(tx *sql.Tx, order domain.Order, claims auth.Claims) error {
	if err := canCancelOrder(order, claims); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM schedule_allocations WHERE order_id = $1", order.ID); err != nil {
		return err
	}
	if _, err := tx.Exec("UPDATE orders SET status = $2, updated_at = NOW() WHERE id = $1", order.ID, domain.StatusCancelled); err != nil {
		return err
	}
	_, err := insertAuditTx(tx, claims.Subject, "order.cancel", order.ID, "")
	return err
}

func bumpCancelledLineRevisionsTx(tx *sql.Tx, revisions map[string]bool) error {
	for lineID := range revisions {
		if _, err := tx.Exec(bumpProductionLineRevisionSQL, lineID); err != nil {
			return err
		}
	}
	return nil
}

func (s *PostgresStore) AssignUser(req assignUserRequest, actorID string) (domain.User, error) {
	if req.Role != domain.RoleAdmin && req.Role != domain.RoleSales && req.Role != domain.RoleScheduler {
		return domain.User{}, errors.New("role must be admin, sales, or scheduler")
	}
	if req.Role == domain.RoleScheduler {
		if _, err := s.productionLine(req.LineID); err != nil {
			return domain.User{}, err
		}
	} else {
		req.LineID = ""
	}
	var user domain.User
	err := s.db.QueryRow(`
		UPDATE users SET role = $2, line_id = NULLIF($3, '')
		WHERE username = $1
		RETURNING id, username, password_hash, role, COALESCE(line_id, ''), disabled
	`, req.Username, req.Role, req.LineID).Scan(&user.ID, &user.Username, &user.PasswordHash, &user.Role, &user.LineID, &user.Disabled)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.User{}, errors.New(notFoundMsg)
	}
	if err != nil {
		return domain.User{}, err
	}
	_, _ = s.db.Exec(`
		INSERT INTO audit_logs (id, actor_id, action, resource, reason, created_at)
		VALUES ($1, $2, 'user.assign', $3, $4, NOW())
	`, auditID(userIdPrefix+user.ID), actorID, user.ID, string(req.Role)+" "+req.LineID)
	return user, nil
}

func (s *PostgresStore) ResetUserPassword(req resetUserPasswordRequest, actorID string) (domain.User, error) {
	passwordHash, err := auth.HashPassword(req.Password)
	if err != nil {
		return domain.User{}, err
	}
	var user domain.User
	err = s.db.QueryRow(`
		UPDATE users SET password_hash = $2
		WHERE username = $1
		RETURNING id, username, password_hash, role, COALESCE(line_id, ''), disabled
	`, strings.TrimSpace(req.Username), passwordHash).Scan(&user.ID, &user.Username, &user.PasswordHash, &user.Role, &user.LineID, &user.Disabled)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.User{}, errors.New(notFoundMsg)
	}
	if err != nil {
		return domain.User{}, err
	}
	_, _ = s.db.Exec(`
		INSERT INTO audit_logs (id, actor_id, action, resource, reason, created_at)
		VALUES ($1, $2, 'user.reset_password', $3, '', NOW())
	`, auditID(userIdPrefix+user.ID), actorID, user.ID)
	return user, nil
}

func (s *PostgresStore) DeleteUser(username, actorID string) (domain.User, error) {
	username = strings.TrimSpace(username)
	var user domain.User
	err := s.db.QueryRow(`
		SELECT id, username, password_hash, role, COALESCE(line_id, ''), disabled
		FROM users WHERE username = $1
	`, username).Scan(&user.ID, &user.Username, &user.PasswordHash, &user.Role, &user.LineID, &user.Disabled)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.User{}, errors.New(notFoundMsg)
	}
	if err != nil {
		return domain.User{}, err
	}
	referenced, err := s.postgresUserHasReferences(user.ID)
	if err != nil {
		return domain.User{}, err
	}
	if !referenced && actorID != user.ID {
		if _, err := s.db.Exec(`
			INSERT INTO audit_logs (id, actor_id, action, resource, reason, created_at)
			VALUES ($1, $2, 'user.delete', $3, '', NOW())
		`, auditID(userIdPrefix+user.ID), actorID, user.ID); err != nil {
			return domain.User{}, err
		}
		if _, err := s.db.Exec("DELETE FROM users WHERE id = $1", user.ID); err != nil {
			return domain.User{}, err
		}
		user.Disabled = false
		user.Deleted = true
		return user, nil
	}
	err = s.db.QueryRow(`
		UPDATE users SET disabled = TRUE
		WHERE id = $1
		RETURNING id, username, password_hash, role, COALESCE(line_id, ''), disabled
	`, user.ID).Scan(&user.ID, &user.Username, &user.PasswordHash, &user.Role, &user.LineID, &user.Disabled)
	if err != nil {
		return domain.User{}, err
	}
	user.Deleted = false
	_, _ = s.db.Exec(`
		INSERT INTO audit_logs (id, actor_id, action, resource, reason, created_at)
		VALUES ($1, $2, 'user.disable', $3, '', NOW())
	`, auditID(userIdPrefix+user.ID), actorID, user.ID)
	return user, nil
}

func (s *PostgresStore) postgresUserHasReferences(userID string) (bool, error) {
	var references int
	if err := s.db.QueryRow(`
		SELECT
			(SELECT COUNT(*) FROM orders WHERE created_by = $1 OR rejected_by = $1) +
			(SELECT COUNT(*) FROM audit_logs WHERE actor_id = $1) +
			(SELECT COUNT(*) FROM schedule_previews WHERE actor_id = $1)
	`, userID).Scan(&references); err != nil {
		return false, err
	}
	return references > 0, nil
}

func (s *PostgresStore) postgresUserHasOrderReferences(userID string) (bool, error) {
	return postgresHasReference(s.db, "SELECT COUNT(*) FROM orders WHERE created_by = $1 OR rejected_by = $1", userID)
}

func (s *PostgresStore) postgresUserHasAuditReferences(userID string) (bool, error) {
	return postgresHasReference(s.db, "SELECT COUNT(*) FROM audit_logs WHERE actor_id = $1", userID)
}

func (s *PostgresStore) postgresUserHasPreviewReferences(userID string) (bool, error) {
	return postgresHasReference(s.db, "SELECT COUNT(*) FROM schedule_previews WHERE actor_id = $1", userID)
}

func postgresHasReference(db *sql.DB, query, userID string) (bool, error) {
	var references int
	if err := db.QueryRow(query, userID).Scan(&references); err != nil {
		return false, err
	}
	return references > 0, nil
}

func (s *PostgresStore) applyMigrations(seedDemo bool) error {
	schema, err := os.ReadFile("db/migrations/001_init.sql")
	if err != nil {
		return err
	}
	if _, err := s.db.Exec(string(schema)); err != nil {
		return err
	}
	if seedDemo {
		seed, err := os.ReadFile("db/migrations/002_seed_demo.sql")
		if err != nil {
			return err
		}
		if _, err := s.db.Exec(string(seed)); err != nil {
			return err
		}
	}
	return nil
}

func (s *PostgresStore) CreateScheduleJob(req scheduleRequest, claims auth.Claims) (domain.ScheduleJob, error) {
	if err := s.ensurePreviewLoaded(req.PreviewID); err != nil {
		return domain.ScheduleJob{}, err
	}
	job, err := s.MemoryStore.CreateScheduleJob(req, claims)
	if err != nil {
		return domain.ScheduleJob{}, err
	}
	oldID := job.ID
	job.ID = "JOB-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	s.MemoryStore.mu.Lock()
	if old, ok := s.MemoryStore.jobs[oldID]; ok {
		delete(s.MemoryStore.jobs, oldID)
		old.ID = job.ID
		s.MemoryStore.jobs[job.ID] = old
	}
	if jobReq, ok := s.MemoryStore.jobRequests[oldID]; ok {
		delete(s.MemoryStore.jobRequests, oldID)
		s.MemoryStore.jobRequests[job.ID] = jobReq
	}
	for index := range s.MemoryStore.audits {
		if s.MemoryStore.audits[index].Resource == oldID {
			s.MemoryStore.audits[index].Resource = job.ID
		}
	}
	s.MemoryStore.mu.Unlock()
	if err := s.insertScheduleJob(job, claims.Subject, req.Reason); err != nil {
		s.MemoryStore.DeleteQueuedScheduleJob(job.ID)
		return domain.ScheduleJob{}, err
	}
	return job, nil
}

func (s *PostgresStore) ensurePreviewLoaded(previewID string) error {
	if previewID == "" {
		return nil
	}
	s.MemoryStore.mu.Lock()
	_, ok := s.MemoryStore.previews[previewID]
	s.MemoryStore.mu.Unlock()
	if ok {
		return nil
	}
	var record previewRecord
	var requestJSON []byte
	var draftJSON sql.NullString
	err := s.db.QueryRow(`
		SELECT actor_id, actor_role, line_id, line_revision, request_hash, request, draft_order, created_at
		FROM schedule_previews
		WHERE id = $1 AND expires_at > NOW()
	`, previewID).Scan(&record.ActorID, &record.ActorRole, &record.LineID, &record.LineRevision, &record.RequestHash, &requestJSON, &draftJSON, &record.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(requestJSON, &record.Request); err != nil {
		return err
	}
	if draftJSON.Valid && draftJSON.String != "" {
		var draft createOrderRequest
		if err := json.Unmarshal([]byte(draftJSON.String), &draft); err != nil {
			return err
		}
		record.DraftOrder = &draft
	}
	if record.Request.ResubmitOrder != nil {
		record.ResubmitOrder = record.Request.ResubmitOrder
	}
	line, err := s.productionLine(record.LineID)
	if err != nil {
		return err
	}
	line.ScheduleRevision = record.LineRevision
	s.MemoryStore.mu.Lock()
	s.MemoryStore.previews[previewID] = record
	s.MemoryStore.lines[record.LineID] = line
	s.MemoryStore.mu.Unlock()
	return nil
}

func (s *PostgresStore) PreviewSchedule(req scheduleRequest, claims auth.Claims) (schedulePreviewResponse, error) {
	var err error
	req, err = s.defaultScheduleCurrentDateLocked(req, claims, nowUTC())
	if err != nil {
		return schedulePreviewResponse{}, err
	}
	result, preview, err := s.previewFromDB(req, claims)
	if err != nil {
		return schedulePreviewResponse{}, err
	}
	s.MemoryStore.mu.Lock()
	s.MemoryStore.previews[preview.ID] = preview.record
	s.MemoryStore.mu.Unlock()

	requestJSON, _ := json.Marshal(preview.record.Request)
	allocationsJSON, _ := json.Marshal(result.Allocations)
	conflictsJSON, _ := json.Marshal(result.Conflicts)
	var draftJSON any
	if preview.record.DraftOrder != nil {
		payload, _ := json.Marshal(preview.record.DraftOrder)
		draftJSON = string(payload)
	}
	_, err = s.db.Exec(`
		INSERT INTO schedule_previews (id, actor_id, actor_role, line_id, line_revision, request_hash, request, allocations, conflicts, draft_order, created_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb, $8::jsonb, $9::jsonb, $10::jsonb, $11, $12)
	`, preview.ID, preview.record.ActorID, preview.record.ActorRole, preview.record.LineID, preview.record.LineRevision, preview.record.RequestHash, string(requestJSON), string(allocationsJSON), string(conflictsJSON), draftJSON, preview.record.CreatedAt, preview.record.CreatedAt.Add(10*time.Minute))
	if err != nil {
		return schedulePreviewResponse{}, err
	}
	return schedulePreviewResponse{
		PreviewID:     preview.ID,
		CurrentDate:   req.CurrentDate,
		Allocations:   result.Allocations,
		Conflicts:     result.Conflicts,
		FinishDate:    result.FinishDate,
		DraftOrder:    req.DraftOrder,
		ResubmitOrder: req.ResubmitOrder,
	}, nil
}

func (s *PostgresStore) ScheduleCalendar(lineID, month string, claims auth.Claims) (calendarResponse, error) {
	lineID, line, err := s.resolvePostgresCalendarLine(lineID, claims)
	if err != nil {
		return calendarResponse{}, err
	}
	window, err := parsePostgresCalendarWindow(month)
	if err != nil {
		return calendarResponse{}, err
	}
	allocations, err := s.calendarAllocationsFromRows(lineID, window)
	if err != nil {
		return calendarResponse{}, err
	}
	pendingAllocations, err := s.postgresPendingBacklogCalendarAllocations(line, window, claims)
	if err != nil {
		return calendarResponse{}, err
	}
	return calendarResponse{LineID: lineID, Timezone: line.Timezone, Month: window.Month, Allocations: allocations, PendingAllocations: pendingAllocations}, nil
}

func (s *PostgresStore) resolvePostgresCalendarLine(lineID string, claims auth.Claims) (string, domain.ProductionLine, error) {
	if lineID == "" && claims.Role == domain.RoleScheduler {
		lineID = claims.LineID
	}
	if lineID == "" {
		return "", domain.ProductionLine{}, errors.New("lineId is required")
	}
	if claims.Role == domain.RoleScheduler && claims.LineID != lineID {
		return "", domain.ProductionLine{}, errors.New("cannot access another production line")
	}
	line, err := s.productionLine(lineID)
	return lineID, line, err
}

func parsePostgresCalendarWindow(month string) (calendarWindow, error) {
	if month == "" {
		month = time.Now().UTC().Format("2006-01")
	}
	monthStart, err := time.Parse("2006-01", month)
	if err != nil {
		return calendarWindow{}, errors.New("month must use YYYY-MM")
	}
	start := monthStart.AddDate(0, 0, -int(monthStart.Weekday()))
	return calendarWindow{Month: month, Start: start, End: start.AddDate(0, 0, 42)}, nil
}

func (s *PostgresStore) calendarAllocationsFromRows(lineID string, window calendarWindow) ([]calendarAllocation, error) {
	rows, err := s.db.Query(`
		SELECT a.order_id, o.customer, a.line_id, a.allocation_date, a.quantity, CASE WHEN COALESCE(a.status, o.status) = '已完成' THEN o.quantity ELSE 0 END, a.priority, COALESCE(a.status, o.status), a.locked, o.due_date, o.created_at
		FROM schedule_allocations a
		JOIN orders o ON o.id = a.order_id
		WHERE a.line_id = $1 AND a.allocation_date >= $2 AND a.allocation_date < $3
		ORDER BY a.allocation_date, CASE WHEN a.priority = 'high' THEN 0 ELSE 1 END, o.due_date, o.created_at, a.order_id
	`, lineID, window.Start, window.End)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	allocations := []calendarAllocation{}
	for rows.Next() {
		var allocation calendarAllocation
		var createdAt time.Time
		if err := rows.Scan(&allocation.OrderID, &allocation.Customer, &allocation.LineID, &allocation.Date, &allocation.Quantity, &allocation.CompletedQuantity, &allocation.Priority, &allocation.Status, &allocation.Locked, &allocation.DueDate, &createdAt); err != nil {
			return nil, err
		}
		allocation.CreatedAtTimestamp = unixMilliseconds(createdAt)
		allocations = append(allocations, allocation)
	}
	return allocations, rows.Err()
}

func (s *PostgresStore) postgresPendingBacklogCalendarAllocations(line domain.ProductionLine, window calendarWindow, claims auth.Claims) ([]calendarAllocation, error) {
	if claims.Role != domain.RoleSales {
		return []calendarAllocation{}, nil
	}
	pendingInputs, err := s.pendingOrderInputs(line.ID, nil)
	if err != nil {
		return nil, err
	}
	existing, err := s.existingAllocations(line.ID)
	if err != nil {
		return nil, err
	}
	currentDate, err := currentDateInLineTimezone(line, nowUTC())
	if err != nil {
		return nil, err
	}
	return pendingBacklogCalendarAllocations(line, pendingInputs, existing, currentDate, window.Start, window.End)
}

func (s *PostgresStore) ConfirmPreviewOrder(req confirmPreviewRequest, claims auth.Claims) (domain.Order, error) {
	preview, err := s.loadPreviewRecordForConfirmation(req.PreviewID)
	if err != nil {
		return domain.Order{}, err
	}
	if err := validatePreviewOwnership(preview, claims); err != nil {
		return domain.Order{}, err
	}
	if err := validateDraftDeferRequest(req, preview); err != nil {
		return domain.Order{}, err
	}
	deferredOrders, err := s.validateSalesDeferredOrders(req.DeferredOrderIDs, preview, claims)
	if err != nil {
		return domain.Order{}, err
	}
	if preview.ResubmitOrder != nil {
		order, err := s.confirmPreviewResubmitTx(req.PreviewID, *preview.ResubmitOrder, deferredOrders, claims)
		if err != nil {
			return domain.Order{}, err
		}
		return order, nil
	}
	order, err := s.confirmPreviewOrderTx(req.PreviewID, *preview.DraftOrder, deferredOrders, req.DeferDraft, strings.TrimSpace(req.DeferReason), claims)
	if err != nil {
		return domain.Order{}, err
	}
	return order, nil
}

func (s *PostgresStore) loadPreviewRecordForConfirmation(previewID string) (previewRecord, error) {
	var draftRaw sql.NullString
	var actorID string
	var actorRole domain.Role
	var lineID string
	var conflictsRaw []byte
	var allocationsRaw []byte
	var requestRaw []byte
	err := s.db.QueryRow(`
		SELECT actor_id, actor_role, line_id, allocations, conflicts, draft_order, request
		FROM schedule_previews
		WHERE id = $1 AND expires_at > NOW()
	`, previewID).Scan(&actorID, &actorRole, &lineID, &allocationsRaw, &conflictsRaw, &draftRaw, &requestRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return previewRecord{}, errors.New("preview result expired or not found")
	}
	if err != nil {
		return previewRecord{}, err
	}
	var previewRequest scheduleRequest
	if err := json.Unmarshal(requestRaw, &previewRequest); err != nil {
		return previewRecord{}, err
	}
	var draft *createOrderRequest
	if draftRaw.Valid && draftRaw.String != "" {
		var parsed createOrderRequest
		if err := json.Unmarshal([]byte(draftRaw.String), &parsed); err != nil {
			return previewRecord{}, err
		}
		draft = &parsed
	}
	var allocations []scheduler.Allocation
	if err := json.Unmarshal(allocationsRaw, &allocations); err != nil {
		return previewRecord{}, err
	}
	var conflicts []scheduler.Conflict
	if err := json.Unmarshal(conflictsRaw, &conflicts); err != nil {
		return previewRecord{}, err
	}
	preview := previewRecord{
		ActorID:       actorID,
		ActorRole:     actorRole,
		LineID:        lineID,
		Request:       previewRequest,
		DraftOrder:    draft,
		ResubmitOrder: previewRequest.ResubmitOrder,
		Allocations:   allocations,
		Conflicts:     conflicts,
	}
	if preview.DraftOrder == nil && preview.ResubmitOrder == nil {
		return previewRecord{}, errors.New("preview does not contain a sales order")
	}
	return preview, nil
}

func validatePreviewOwnership(preview previewRecord, claims auth.Claims) error {
	if preview.ActorID != claims.Subject || preview.ActorRole != claims.Role {
		return errors.New("preview result belongs to another user")
	}
	return nil
}

func validateDraftDeferRequest(req confirmPreviewRequest, preview previewRecord) error {
	if req.DeferDraft && len(req.DeferredOrderIDs) > 0 {
		return errors.New("draft defer cannot include deferred pending orders")
	}
	if req.DeferDraft && len(preview.Conflicts) == 0 {
		return errors.New("draft can be deferred only when preview has conflicts")
	}
	return nil
}

func (s *PostgresStore) validateSalesDeferredOrders(orderIDs []string, preview previewRecord, claims auth.Claims) ([]domain.Order, error) {
	ids := uniqueOrderIDs(orderIDs)
	if len(ids) == 0 {
		return nil, nil
	}
	allowed := previewDeferredOrderIDs(preview)
	orders := make([]domain.Order, 0, len(ids))
	for _, orderID := range ids {
		if !allowed[orderID] {
			return nil, errors.New("deferred order must belong to preview conflicts")
		}
		order, err := s.order(orderID)
		if err != nil {
			return nil, err
		}
		if order.CreatedBy != claims.Subject {
			return nil, errors.New("sales can defer only their own orders")
		}
		if order.LineID != preview.LineID {
			return nil, errors.New("deferred order line must match preview line")
		}
		if order.Status != domain.StatusPending {
			return nil, errors.New("only pending preview conflicts can be deferred")
		}
		orders = append(orders, order)
	}
	return orders, nil
}

func (s *PostgresStore) prepareConfirmedOrder(draft createOrderRequest, claims auth.Claims) (domain.Order, error) {
	if err := validateOrderFields(draft.Customer, draft.Quantity, draft.Note); err != nil {
		return domain.Order{}, err
	}
	if draft.Priority == "" {
		draft.Priority = domain.PriorityLow
	}
	if draft.Priority != domain.PriorityLow && draft.Priority != domain.PriorityHigh {
		return domain.Order{}, errors.New("priority must be low or high")
	}
	line, err := s.productionLine(draft.LineID)
	if err != nil {
		return domain.Order{}, err
	}
	currentDate, err := currentDateInLineTimezone(line, nowUTC())
	if err != nil {
		return domain.Order{}, err
	}
	dueDate, err := validateOrderRequest(draft, map[string]domain.ProductionLine{line.ID: line}, currentDate)
	if err != nil {
		return domain.Order{}, err
	}
	now := time.Now().UTC()
	order := domain.Order{
		ID:        orderIDFromTime(now),
		Customer:  draft.Customer,
		LineID:    draft.LineID,
		Quantity:  draft.Quantity,
		Priority:  draft.Priority,
		Status:    domain.StatusPending,
		DueDate:   dueDate,
		Note:      strings.TrimSpace(draft.Note),
		CreatedBy: claims.Subject,
		CreatedAt: now,
		UpdatedAt: now,
	}
	return order, nil
}

func deferConflictOrdersTx(tx *sql.Tx, deferredOrders []domain.Order, claims auth.Claims, now time.Time) error {
	for _, deferred := range deferredOrders {
		result, err := tx.Exec(`
			UPDATE orders
			SET status = $2, rejection_reason = $3, rejected_by = $4, rejected_at = $5, updated_at = $5
			WHERE id = $1 AND status = $6 AND created_by = $7 AND line_id = $8
		`, deferred.ID, domain.StatusRejected, salesConflictDeferredReason, claims.Subject, now, domain.StatusPending, claims.Subject, deferred.LineID)
		if err != nil {
			return err
		}
		if err := requireOneRowAffected(result, "deferred order changed before confirmation"); err != nil {
			return err
		}
		if _, err := insertAuditTx(tx, claims.Subject, "order.sales_conflict_defer", deferred.ID, salesConflictDeferredReason); err != nil {
			return err
		}
	}
	return nil
}

func (s *PostgresStore) confirmPreviewOrderTx(previewID string, draft createOrderRequest, deferredOrders []domain.Order, deferDraft bool, deferReason string, claims auth.Claims) (domain.Order, error) {
	order, err := s.prepareConfirmedOrder(draft, claims)
	if err != nil {
		return domain.Order{}, err
	}
	order = applyDraftDeferState(order, claims, deferDraft, deferReason)
	tx, err := s.db.Begin()
	if err != nil {
		return domain.Order{}, err
	}
	defer tx.Rollback()
	if err := insertConfirmedOrderTx(tx, order); err != nil {
		return domain.Order{}, err
	}
	if err := auditConfirmedOrderTx(tx, order, claims, deferDraft, deferReason); err != nil {
		return domain.Order{}, err
	}
	if err := deferConflictOrdersTx(tx, deferredOrders, claims, order.CreatedAt); err != nil {
		return domain.Order{}, err
	}
	if _, err := tx.Exec(bumpProductionLineRevisionSQL, order.LineID); err != nil {
		return domain.Order{}, err
	}
	if err := deleteConfirmedPreviewTx(tx, previewID, claims); err != nil {
		return domain.Order{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Order{}, err
	}
	return order, nil
}

func applyDraftDeferState(order domain.Order, claims auth.Claims, deferDraft bool, deferReason string) domain.Order {
	if deferDraft {
		order.Status = domain.StatusRejected
		order.RejectionReason = deferReason
		order.RejectedBy = claims.Subject
		order.RejectedAt = order.CreatedAt
	}
	return order
}

func insertConfirmedOrderTx(tx *sql.Tx, order domain.Order) error {
	_, err := tx.Exec(`
		INSERT INTO orders (id, customer, line_id, quantity, priority, status, due_date, note, created_by, rejection_reason, rejected_by, rejected_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, NULLIF($10, ''), NULLIF($11, ''), $12, $13, $13)
	`, order.ID, order.Customer, order.LineID, order.Quantity, order.Priority, order.Status, order.DueDate, order.Note, order.CreatedBy, order.RejectionReason, order.RejectedBy, nullableTime(order.RejectedAt), order.CreatedAt)
	return err
}

func auditConfirmedOrderTx(tx *sql.Tx, order domain.Order, claims auth.Claims, deferDraft bool, deferReason string) error {
	if _, err := insertAuditTx(tx, claims.Subject, "order.create", order.ID, ""); err != nil {
		return err
	}
	if deferDraft {
		_, err := insertAuditTx(tx, claims.Subject, "order.sales_conflict_defer_draft", order.ID, deferReason)
		return err
	}
	return nil
}

func deleteConfirmedPreviewTx(tx *sql.Tx, previewID string, claims auth.Claims) error {
	result, err := tx.Exec("DELETE FROM schedule_previews WHERE id = $1 AND actor_id = $2 AND actor_role = $3 AND expires_at > NOW()", previewID, claims.Subject, claims.Role)
	if err != nil {
		return err
	}
	return requireOneRowAffected(result, "preview result expired or not found")
}

func (s *PostgresStore) confirmPreviewResubmitTx(previewID string, req resubmitOrderRequest, deferredOrders []domain.Order, claims auth.Claims) (domain.Order, error) {
	order, err := s.order(req.OrderID)
	if err != nil {
		return domain.Order{}, err
	}
	if order.CreatedBy != claims.Subject {
		return domain.Order{}, errors.New("sales can resubmit only their own orders")
	}
	if !canSalesResubmitStatus(order.Status) {
		return domain.Order{}, errors.New("only pending or rejected orders can be resubmitted")
	}
	if strings.TrimSpace(req.Note) != "" {
		return domain.Order{}, errors.New(errNoteImmutable)
	}
	if err := applyOptionalQuantity(&order, req.Quantity); err != nil {
		return domain.Order{}, err
	}
	if req.DueDate != "" {
		line, err := s.productionLine(order.LineID)
		if err != nil {
			return domain.Order{}, err
		}
		currentDate, err := currentDateInLineTimezone(line, nowUTC())
		if err != nil {
			return domain.Order{}, err
		}
		if err := applyOptionalDueDate(&order, req.DueDate, currentDate); err != nil {
			return domain.Order{}, err
		}
	}
	order.Status = domain.StatusPending
	resetRejectedState(&order)
	order.UpdatedAt = time.Now().UTC()

	tx, err := s.db.Begin()
	if err != nil {
		return domain.Order{}, err
	}
	defer tx.Rollback()
	result, err := tx.Exec(`
		UPDATE orders
		SET quantity = $2, status = $3, due_date = $4, rejection_reason = NULL,
		    rejected_by = NULL, rejected_at = NULL, updated_at = $5
		WHERE id = $1 AND created_by = $6
	`, order.ID, order.Quantity, order.Status, order.DueDate, order.UpdatedAt, claims.Subject)
	if err != nil {
		return domain.Order{}, err
	}
	if err := requireOneRowAffected(result, "resubmitted order changed before confirmation"); err != nil {
		return domain.Order{}, err
	}
	if _, err := insertAuditTx(tx, claims.Subject, "order.resubmit", order.ID, ""); err != nil {
		return domain.Order{}, err
	}
	if err := deferConflictOrdersTx(tx, deferredOrders, claims, order.UpdatedAt); err != nil {
		return domain.Order{}, err
	}
	if _, err := tx.Exec(bumpProductionLineRevisionSQL, order.LineID); err != nil {
		return domain.Order{}, err
	}
	if err := deleteConfirmedPreviewTx(tx, previewID, claims); err != nil {
		return domain.Order{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Order{}, err
	}
	return order, nil
}

func requireOneRowAffected(result sql.Result, message string) error {
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return errors.New(message)
	}
	return nil
}

func (s *PostgresStore) GetScheduleJob(id string) (domain.ScheduleJob, bool) {
	row := s.db.QueryRow(`
		SELECT id, line_id, status, COALESCE(message, ''), COALESCE(source, ''), COALESCE(preview_id, ''),
		       COALESCE(request_hash, ''), line_revision, attempt_count, order_ids, created_at, updated_at
		FROM schedule_jobs
		WHERE id = $1
	`, id)
	job, err := scanScheduleJob(row)
	if err != nil {
		return s.MemoryStore.GetScheduleJob(id)
	}
	return job, true
}

func (s *PostgresStore) ScheduleHistory(lineID string, claims auth.Claims) ([]domain.AuditEntry, error) {
	if claims.Role != domain.RoleAdmin && claims.Role != domain.RoleScheduler {
		return nil, errors.New("only admin or schedulers can read schedule history")
	}
	if claims.Role == domain.RoleScheduler {
		lineID = claims.LineID
	}
	query := `
		SELECT a.id, a.actor_id, a.action, a.resource, COALESCE(a.reason, ''), a.created_at
		FROM audit_logs a
		LEFT JOIN orders o ON o.id = a.resource
		LEFT JOIN schedule_jobs j ON j.id = a.resource
		WHERE a.action IN ('schedule.job.create','schedule.job.manual_force','schedule.job.complete','schedule.job.fail','order.reject','order.cancel','production.start','production.confirm.complete','production.confirm.partial')`
	args := []any{}
	if lineID != "" {
		query += " AND (o.line_id = $1 OR j.line_id = $1)"
		args = append(args, lineID)
	}
	query += " ORDER BY a.created_at DESC LIMIT 12"
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	history := []domain.AuditEntry{}
	for rows.Next() {
		var entry domain.AuditEntry
		if err := rows.Scan(&entry.ID, &entry.ActorID, &entry.Action, &entry.Resource, &entry.Reason, &entry.CreatedAt); err != nil {
			return nil, err
		}
		history = append(history, entry)
	}
	return history, rows.Err()
}

func (s *PostgresStore) StartProduction(req productionStartRequest, claims auth.Claims) (domain.Order, error) {
	order, err := s.order(req.OrderID)
	if err != nil {
		return domain.Order{}, err
	}
	if order.LineID != claims.LineID {
		return domain.Order{}, errors.New("cannot start another production line")
	}
	if order.Status != domain.StatusScheduled {
		return domain.Order{}, errors.New("only scheduled orders can start production")
	}
	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM schedule_allocations WHERE order_id = $1 AND COALESCE(status, '已排程') <> '已完成'", order.ID).Scan(&count); err != nil {
		return domain.Order{}, err
	}
	if count == 0 {
		return domain.Order{}, errors.New("scheduled order has no allocation")
	}
	now := time.Now().UTC()
	tx, err := s.db.Begin()
	if err != nil {
		return domain.Order{}, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec("UPDATE orders SET status = '生產中', updated_at = $2 WHERE id = $1", order.ID, now); err != nil {
		return domain.Order{}, err
	}
	if _, err := tx.Exec("UPDATE schedule_allocations SET locked = TRUE, status = '生產中' WHERE order_id = $1 AND COALESCE(status, '已排程') <> '已完成'", order.ID); err != nil {
		return domain.Order{}, err
	}
	if _, err := tx.Exec(bumpProductionLineRevisionSQL, order.LineID); err != nil {
		return domain.Order{}, err
	}
	if _, err := insertAuditTx(tx, claims.Subject, "production.start", order.ID, ""); err != nil {
		return domain.Order{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Order{}, err
	}
	order.Status = domain.StatusInProgress
	order.UpdatedAt = now
	return order, nil
}

func (s *PostgresStore) ConfirmProduction(req productionConfirmRequest, claims auth.Claims) (productionConfirmResponse, error) {
	order, err := s.loadConfirmableProductionOrder(req, claims)
	if err != nil {
		return productionConfirmResponse{}, err
	}
	productionDate, err := validateProductionConfirmRequest(req)
	if err != nil {
		return productionConfirmResponse{}, err
	}
	allocation, err := s.loadProductionAllocation(order.ID, productionDate)
	if err != nil {
		return productionConfirmResponse{}, err
	}
	if err := validateProductionAllocation(allocation, req); err != nil {
		return productionConfirmResponse{}, err
	}
	now := time.Now().UTC()
	result, err := scheduler.ConfirmProduction(order, req.ProducedQuantity, now)
	if err != nil {
		return productionConfirmResponse{}, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return productionConfirmResponse{}, err
	}
	defer tx.Rollback()
	action := "production.confirm.partial"
	reason := ""
	var remainder *domain.Order
	if result.Completed {
		if err := completeProductionTx(tx, &order, now); err != nil {
			return productionConfirmResponse{}, err
		}
		action = "production.confirm.complete"
	} else {
		remainderValue, partialReason, err := s.partialProductionTx(tx, &order, req, result, now)
		if err != nil {
			return productionConfirmResponse{}, err
		}
		reason = partialReason
		remainder = remainderValue
	}
	if err := finalizeProductionConfirmationTx(tx, order, productionDate, action, reason, claims, !result.Completed); err != nil {
		return productionConfirmResponse{}, err
	}
	if err := tx.Commit(); err != nil {
		return productionConfirmResponse{}, err
	}
	if result.Completed {
		return productionConfirmResponse{Order: order}, nil
	}
	return productionConfirmResponse{Order: order, Remainder: remainder}, nil
}

func (s *PostgresStore) loadConfirmableProductionOrder(req productionConfirmRequest, claims auth.Claims) (domain.Order, error) {
	order, err := s.order(req.OrderID)
	if err != nil {
		return domain.Order{}, err
	}
	if order.LineID != claims.LineID {
		return domain.Order{}, errors.New("cannot confirm another production line")
	}
	if order.Status != domain.StatusInProgress {
		return domain.Order{}, errors.New("only in-progress orders can be confirmed")
	}
	return order, nil
}

func validateProductionConfirmRequest(req productionConfirmRequest) (time.Time, error) {
	if req.ProducedQuantity <= 0 {
		return time.Time{}, errors.New("producedQuantity must be greater than zero")
	}
	productionDate, err := time.Parse(dateLayout, req.ProductionDate)
	if err != nil {
		return time.Time{}, errors.New("productionDate must use YYYY-MM-DD")
	}
	return productionDate, nil
}

func (s *PostgresStore) loadProductionAllocation(orderID string, productionDate time.Time) (domain.ScheduleAllocation, error) {
	var allocation domain.ScheduleAllocation
	err := s.db.QueryRow(`
		SELECT order_id, line_id, allocation_date, quantity, priority, locked, COALESCE(status, '已排程')
		FROM schedule_allocations
		WHERE order_id = $1 AND allocation_date = $2
		LIMIT 1
	`, orderID, productionDate).Scan(&allocation.OrderID, &allocation.LineID, &allocation.Date, &allocation.Quantity, &allocation.Priority, &allocation.Locked, &allocation.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ScheduleAllocation{}, errors.New("scheduled allocation not found for productionDate")
	}
	return allocation, err
}

func validateProductionAllocation(allocation domain.ScheduleAllocation, req productionConfirmRequest) error {
	if allocation.Status == domain.StatusCompleted {
		return errors.New("productionDate has already been confirmed")
	}
	if req.ProducedQuantity > allocation.Quantity {
		return errors.New("producedQuantity cannot exceed scheduled allocation quantity")
	}
	return nil
}

func completeProductionTx(tx *sql.Tx, order *domain.Order, now time.Time) error {
	order.Status = domain.StatusCompleted
	order.UpdatedAt = now
	_, err := tx.Exec("UPDATE orders SET status = '已完成', updated_at = $2 WHERE id = $1", order.ID, now)
	return err
}

func (s *PostgresStore) partialProductionTx(tx *sql.Tx, order *domain.Order, req productionConfirmRequest, result scheduler.ConfirmationResult, now time.Time) (*domain.Order, string, error) {
	originalQuantity := order.Quantity
	remainderValue := *result.Remainder
	remainderID, err := s.nextRemainderOrderIDTx(tx, order.ID, order.SourceOrder != "")
	if err != nil {
		return nil, "", err
	}
	remainderValue.ID = remainderID
	remainderValue.CreatedAt = now
	remainderValue.UpdatedAt = now
	order.Quantity = req.ProducedQuantity
	order.Status = domain.StatusCompleted
	order.UpdatedAt = now
	if _, err := tx.Exec("UPDATE orders SET status = '已完成', quantity = $2, updated_at = $3 WHERE id = $1", order.ID, order.Quantity, now); err != nil {
		return nil, "", err
	}
	if _, err := tx.Exec(`
		INSERT INTO orders (id, customer, line_id, quantity, priority, status, due_date, note, created_by, source_order, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $11)
	`, remainderValue.ID, remainderValue.Customer, remainderValue.LineID, remainderValue.Quantity, remainderValue.Priority, remainderValue.Status, remainderValue.DueDate, remainderValue.Note, remainderValue.CreatedBy, order.ID, now); err != nil {
		return nil, "", err
	}
	reason := "produced " + strconv.Itoa(req.ProducedQuantity) + " of " + strconv.Itoa(originalQuantity) + ", remainder " + remainderValue.ID + " quantity " + strconv.Itoa(remainderValue.Quantity) + " returned to pending"
	return &remainderValue, reason, nil
}

func finalizeProductionConfirmationTx(tx *sql.Tx, order domain.Order, productionDate time.Time, action, reason string, claims auth.Claims, partial bool) error {
	if _, err := tx.Exec("UPDATE schedule_allocations SET locked = TRUE, status = '已完成' WHERE order_id = $1 AND allocation_date = $2", order.ID, productionDate); err != nil {
		return err
	}
	if partial {
		if _, err := tx.Exec("DELETE FROM schedule_allocations WHERE order_id = $1 AND allocation_date <> $2", order.ID, productionDate); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(bumpProductionLineRevisionSQL, order.LineID); err != nil {
		return err
	}
	_, err := insertAuditTx(tx, claims.Subject, action, order.ID, reason)
	return err
}

func (s *PostgresStore) splitAllocationOrderIDsDB(allocations []scheduler.Allocation) ([]scheduler.Allocation, error) {
	seen := map[string]int{}
	reserved := map[string]bool{}
	normalized := make([]scheduler.Allocation, 0, len(allocations))
	for _, allocation := range allocations {
		seen[allocation.OrderID]++
		if seen[allocation.OrderID] == 1 {
			normalized = append(normalized, allocation)
			continue
		}
		source, err := s.order(allocation.OrderID)
		if err != nil {
			return nil, err
		}
		sourceID := allocation.OrderID
		allocation.SourceOrderID = sourceID
		var existsErr error
		allocation.OrderID = nextRemainderOrderID(sourceID, source.SourceOrder != "", func(id string) bool {
			if existsErr != nil {
				return false
			}
			var exists bool
			existsErr = s.db.QueryRow("SELECT EXISTS (SELECT 1 FROM orders WHERE id = $1)", id).Scan(&exists)
			return existsErr != nil || exists || reserved[id]
		})
		if existsErr != nil {
			return nil, existsErr
		}
		reserved[allocation.OrderID] = true
		normalized = append(normalized, allocation)
	}
	return normalized, nil
}

func (s *PostgresStore) nextRemainderOrderIDTx(tx *sql.Tx, originalID string, incrementExistingSuffix bool) (string, error) {
	candidate := ""
	var err error
	candidate = nextRemainderOrderID(originalID, incrementExistingSuffix, func(id string) bool {
		if err != nil {
			return false
		}
		var exists bool
		err = tx.QueryRow("SELECT EXISTS (SELECT 1 FROM orders WHERE id = $1)", id).Scan(&exists)
		if err != nil {
			return false
		}
		return exists
	})
	if err != nil {
		return "", err
	}
	return candidate, nil
}

func (s *PostgresStore) DeleteQueuedScheduleJob(id string) {
	s.MemoryStore.DeleteQueuedScheduleJob(id)
	_, _ = s.db.Exec("DELETE FROM audit_logs WHERE resource = $1 AND action IN ('schedule.job.create', 'schedule.job.manual_force')", id)
	_, _ = s.db.Exec("DELETE FROM schedule_jobs WHERE id = $1 AND status = 'queued'", id)
}

func (s *PostgresStore) ExecuteScheduleJob(id string) domain.ScheduleJob {
	job := s.MemoryStore.ExecuteScheduleJob(id)
	if job.ID != "" {
		_ = s.upsertScheduleJob(job)
	}
	return job
}

func (s *PostgresStore) CreateHPAPeakDemo(claims auth.Claims) (hpaPeakSummary, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return hpaPeakSummary{}, err
	}
	defer tx.Rollback()

	if err := s.resetHPAPeakDemoDB(tx); err != nil {
		return hpaPeakSummary{}, err
	}
	now := time.Now().UTC()
	for lineIndex := hpaDemoFirstLine; lineIndex <= hpaDemoLastLine; lineIndex++ {
		lineID := hpaDemoLineID(lineIndex)
		if err := upsertHPADemoLineTx(tx, lineID); err != nil {
			return hpaPeakSummary{}, err
		}
		orderIDs, err := insertHPADemoOrdersTx(tx, lineID, claims, now)
		if err != nil {
			return hpaPeakSummary{}, err
		}
		if err := insertHPADemoJobsTx(tx, lineID, orderIDs, claims, now); err != nil {
			return hpaPeakSummary{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return hpaPeakSummary{}, err
	}
	summary, err := s.hpaPeakSummaryDB()
	if err != nil {
		return hpaPeakSummary{}, err
	}
	return summary, nil
}

func upsertHPADemoLineTx(tx *sql.Tx, lineID string) error {
	_, err := tx.Exec(`
		INSERT INTO production_lines (id, name, capacity_per_day, timezone, schedule_revision)
		VALUES ($1, $2, 10000, $3, 0)
		ON CONFLICT (id) DO UPDATE SET name = EXCLUDED.name, capacity_per_day = EXCLUDED.capacity_per_day, timezone = EXCLUDED.timezone, schedule_revision = 0
	`, lineID, "HPA Demo Line "+lineID, defaultLineTimezone)
	return err
}

func insertHPADemoOrdersTx(tx *sql.Tx, lineID string, claims auth.Claims, now time.Time) ([]string, error) {
	orderIDs := make([]string, 0, hpaDemoOrdersPerLine)
	for orderIndex := 1; orderIndex <= hpaDemoOrdersPerLine; orderIndex++ {
		orderID := fmt.Sprintf("HPA-%s-%03d", lineID, orderIndex)
		orderIDs = append(orderIDs, orderID)
		if _, err := tx.Exec(`
			INSERT INTO orders (id, customer, line_id, quantity, priority, status, due_date, note, created_by, created_at, updated_at)
			VALUES ($1, 'HPA Demo', $2, 2500, 'low', '待排程', $3, $4, $5, $6, $6)
		`, orderID, lineID, now.AddDate(0, 0, 7), hpaDemoSource, claims.Subject, now); err != nil {
			return nil, err
		}
	}
	return orderIDs, nil
}

func insertHPADemoJobsTx(tx *sql.Tx, lineID string, orderIDs []string, claims auth.Claims, now time.Time) error {
	for jobIndex := 1; jobIndex <= hpaDemoJobsPerLine; jobIndex++ {
		jobID := fmt.Sprintf("HPA-JOB-%s-%03d", lineID, jobIndex)
		orderJSON, _ := json.Marshal([]string{orderIDs[jobIndex-1]})
		if _, err := tx.Exec(`
			INSERT INTO schedule_jobs (id, line_id, status, message, source, order_ids, created_at, updated_at)
			VALUES ($1, $2, 'queued', '多產線排程尖峰任務已送入背景佇列。', $3, $4::jsonb, $5, $5)
		`, jobID, lineID, hpaDemoSource, string(orderJSON), now); err != nil {
			return err
		}
		if _, err := tx.Exec(`
			INSERT INTO audit_logs (id, actor_id, action, resource, reason, created_at)
			VALUES ($1, $2, 'schedule.job.create', $3, $4, $5)
		`, fmt.Sprintf("AUD-HPA-%s-%03d", lineID, jobIndex), claims.Subject, jobID, hpaDemoSource, now); err != nil {
			return err
		}
	}
	return nil
}

func (s *PostgresStore) ClearHPAPeakDemo(claims auth.Claims) (hpaPeakSummary, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return hpaPeakSummary{}, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`
		UPDATE schedule_jobs
		SET status = 'cancelled', message = '排程尖峰展示已取消。', updated_at = NOW()
		WHERE (source = $1 OR line_id BETWEEN 'L001' AND 'L200') AND status IN ('queued', 'running')
	`, hpaDemoSource); err != nil {
		return hpaPeakSummary{}, err
	}
	if _, err := tx.Exec(`
		DELETE FROM schedule_jobs
		WHERE (source = $1 OR line_id BETWEEN 'L001' AND 'L200')
		  AND status NOT IN ('queued', 'running', 'cancelled')
	`, hpaDemoSource); err != nil {
		return hpaPeakSummary{}, err
	}
	if _, err := tx.Exec("DELETE FROM schedule_allocations WHERE line_id BETWEEN 'L001' AND 'L200'"); err != nil {
		return hpaPeakSummary{}, err
	}
	if _, err := tx.Exec("DELETE FROM orders WHERE line_id BETWEEN 'L001' AND 'L200'"); err != nil {
		return hpaPeakSummary{}, err
	}
	if _, err := tx.Exec("DELETE FROM audit_logs WHERE reason = $1", hpaDemoSource); err != nil {
		return hpaPeakSummary{}, err
	}
	if err := tx.Commit(); err != nil {
		return hpaPeakSummary{}, err
	}
	return s.hpaPeakSummaryDB()
}

func (s *PostgresStore) HPAPeakSummary() hpaPeakSummary {
	summary, err := s.hpaPeakSummaryDB()
	if err != nil {
		return s.MemoryStore.HPAPeakSummary()
	}
	return summary
}

func (s *PostgresStore) HPAPeakJobs() []domain.ScheduleJob {
	rows, err := s.db.Query(`
		SELECT id, line_id, status, COALESCE(message, ''), COALESCE(source, ''), COALESCE(preview_id, ''),
		       COALESCE(request_hash, ''), line_revision, attempt_count, order_ids, created_at, updated_at
		FROM schedule_jobs
		WHERE source = $1 OR line_id BETWEEN 'L001' AND 'L200'
		ORDER BY id
	`, hpaDemoSource)
	if err != nil {
		return s.MemoryStore.HPAPeakJobs()
	}
	defer rows.Close()

	jobs := []domain.ScheduleJob{}
	for rows.Next() {
		job, err := scanScheduleJob(rows)
		if err == nil {
			jobs = append(jobs, job)
		}
	}
	return jobs
}

func (s *PostgresStore) insertScheduleJob(job domain.ScheduleJob, actorID, reason string) error {
	orderJSON, _ := json.Marshal(job.OrderIDs)
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`
		INSERT INTO schedule_jobs (id, line_id, status, message, source, preview_id, request_hash, line_revision, order_ids, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::jsonb, $10, $11)
	`, job.ID, job.LineID, job.Status, job.Message, job.Source, job.PreviewID, job.RequestHash, job.LineRevision, string(orderJSON), job.CreatedAt, job.UpdatedAt); err != nil {
		return err
	}
	if _, err := insertAuditTx(tx, actorID, "schedule.job.create", job.ID, reason); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *PostgresStore) upsertScheduleJob(job domain.ScheduleJob) error {
	orderJSON, _ := json.Marshal(job.OrderIDs)
	_, err := s.db.Exec(`
		INSERT INTO schedule_jobs (id, line_id, status, message, source, preview_id, request_hash, line_revision, order_ids, started_at, completed_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::jsonb, $10, $11, $12, $13)
		ON CONFLICT (id) DO UPDATE SET
			status = EXCLUDED.status,
			message = EXCLUDED.message,
			started_at = EXCLUDED.started_at,
			completed_at = EXCLUDED.completed_at,
			updated_at = EXCLUDED.updated_at
	`, job.ID, job.LineID, job.Status, job.Message, job.Source, job.PreviewID, job.RequestHash, job.LineRevision, string(orderJSON), nullableTime(job.StartedAt), nullableTime(job.CompletedAt), job.CreatedAt, job.UpdatedAt)
	return err
}

func (s *PostgresStore) resetHPAPeakDemoDB(tx *sql.Tx) error {
	statements := []string{
		"DELETE FROM schedule_allocations WHERE line_id BETWEEN 'L001' AND 'L200'",
		"DELETE FROM schedule_jobs WHERE source = 'hpa-peak-demo' OR line_id BETWEEN 'L001' AND 'L200'",
		"DELETE FROM orders WHERE line_id BETWEEN 'L001' AND 'L200'",
		"DELETE FROM audit_logs WHERE reason = 'hpa-peak-demo'",
		"DELETE FROM production_lines WHERE id BETWEEN 'L001' AND 'L200'",
	}
	for _, statement := range statements {
		if _, err := tx.Exec(statement); err != nil {
			return err
		}
	}
	return nil
}

func (s *PostgresStore) hpaPeakSummaryDB() (hpaPeakSummary, error) {
	summary := hpaPeakSummaryDefaults()
	summary.Statuses = map[string]int{
		string(domain.JobQueued):    0,
		string(domain.JobRunning):   0,
		string(domain.JobCompleted): 0,
		string(domain.JobFailed):    0,
		string(domain.JobCancelled): 0,
	}
	if err := s.db.QueryRow(`
		SELECT COUNT(DISTINCT line_id)
		FROM (
			SELECT line_id FROM orders WHERE line_id BETWEEN 'L001' AND 'L200'
			UNION
			SELECT line_id FROM schedule_jobs WHERE (source = $1 OR line_id BETWEEN 'L001' AND 'L200') AND status <> 'cancelled'
		) active_lines
	`, hpaDemoSource).Scan(&summary.LineCount); err != nil {
		return hpaPeakSummary{}, err
	}
	if err := s.db.QueryRow("SELECT COUNT(*) FROM orders WHERE line_id BETWEEN 'L001' AND 'L200'").Scan(&summary.OrderCount); err != nil {
		return hpaPeakSummary{}, err
	}
	rows, err := s.db.Query(`
		SELECT status, COUNT(*)
		FROM schedule_jobs
		WHERE source = $1 OR line_id BETWEEN 'L001' AND 'L200'
		GROUP BY status
	`, hpaDemoSource)
	if err != nil {
		return hpaPeakSummary{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return hpaPeakSummary{}, err
		}
		summary.Statuses[status] = count
		summary.JobCount += count
	}
	summary.RecentJobs = s.HPAPeakJobs()
	if len(summary.RecentJobs) > 10 {
		summary.RecentJobs = summary.RecentJobs[:10]
	}
	for _, job := range summary.RecentJobs {
		if job.Status == domain.JobFailed && job.Message != "" && len(summary.FailedMessages) < 5 {
			summary.FailedMessages = append(summary.FailedMessages, job.ID+"："+job.Message)
		}
	}
	return summary, nil
}

type scheduleJobScanner interface {
	Scan(dest ...any) error
}

func scanScheduleJob(scanner scheduleJobScanner) (domain.ScheduleJob, error) {
	var job domain.ScheduleJob
	var orderJSON []byte
	err := scanner.Scan(&job.ID, &job.LineID, &job.Status, &job.Message, &job.Source, &job.PreviewID, &job.RequestHash, &job.LineRevision, &job.AttemptCount, &orderJSON, &job.CreatedAt, &job.UpdatedAt)
	if err != nil {
		return domain.ScheduleJob{}, err
	}
	_ = json.Unmarshal(orderJSON, &job.OrderIDs)
	sort.Strings(job.OrderIDs)
	return job, nil
}

func nullableTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value
}

type previewFromDBResult struct {
	ID     string
	record previewRecord
}

func (s *PostgresStore) previewFromDB(req scheduleRequest, claims auth.Claims) (scheduler.Result, previewFromDBResult, error) {
	lineID, line, err := s.resolvePreviewLineFromDB(req, claims)
	if err != nil {
		return scheduler.Result{}, previewFromDBResult{}, err
	}
	currentDate, startDate, err := parsePreviewDatesFromDB(req)
	if err != nil {
		return scheduler.Result{}, previewFromDBResult{}, err
	}
	if err := validateNoResolutionOrderIDs(req); err != nil {
		return scheduler.Result{}, previewFromDBResult{}, err
	}
	inputs, err := s.schedulerInputs(req, claims, lineID)
	if err != nil {
		return scheduler.Result{}, previewFromDBResult{}, err
	}
	existing, err := s.existingAllocations(lineID)
	if err != nil {
		return scheduler.Result{}, previewFromDBResult{}, err
	}
	result, err := runSchedulePlan(scheduler.Request{
		LineID:              lineID,
		CapacityPerDay:      line.CapacityPerDay,
		StartDate:           startDate,
		CurrentDate:         currentDate,
		Orders:              inputs,
		ExistingAllocations: existing,
		ManualForce:         req.ManualForce,
		ForceReason:         req.Reason,
		AllowLateCompletion: req.AllowLateCompletion,
	}, req.DraftOrder != nil)
	if err != nil {
		return scheduler.Result{}, previewFromDBResult{}, err
	}
	if req.DraftOrder == nil {
		result.Allocations, err = s.splitAllocationOrderIDsDB(result.Allocations)
		if err != nil {
			return scheduler.Result{}, previewFromDBResult{}, err
		}
	}
	return result, previewRecordFromDBResult(result, req, claims, lineID, line.ScheduleRevision), nil
}

func (s *PostgresStore) resolvePreviewLineFromDB(req scheduleRequest, claims auth.Claims) (string, domain.ProductionLine, error) {
	lineID := scheduleLineID(req, claims)
	if lineID == "" {
		return "", domain.ProductionLine{}, errors.New("lineId is required")
	}
	if claims.Role == domain.RoleScheduler && claims.LineID != lineID {
		return "", domain.ProductionLine{}, errors.New("cannot access another production line")
	}
	line, err := s.productionLine(lineID)
	return lineID, line, err
}

func parsePreviewDatesFromDB(req scheduleRequest) (time.Time, time.Time, error) {
	startDate := time.Now().UTC()
	if req.StartDate != "" {
		parsed, err := time.Parse(dateLayout, req.StartDate)
		if err != nil {
			return time.Time{}, time.Time{}, errors.New("startDate must use YYYY-MM-DD")
		}
		startDate = parsed
	}
	currentDate := time.Time{}
	if req.CurrentDate != "" {
		parsed, err := time.Parse(dateLayout, req.CurrentDate)
		if err != nil {
			return time.Time{}, time.Time{}, errors.New("currentDate must use YYYY-MM-DD")
		}
		currentDate = parsed
	}
	return currentDate, startDate, nil
}

func previewRecordFromDBResult(result scheduler.Result, req scheduleRequest, claims auth.Claims, lineID string, lineRevision int64) previewFromDBResult {
	now := time.Now().UTC()
	id := "PREVIEW-" + strconv.FormatInt(now.UnixNano(), 10)
	normalized := normalizedPreviewRequest(req)
	return previewFromDBResult{
		ID: id,
		record: previewRecord{
			ActorID:       claims.Subject,
			ActorRole:     claims.Role,
			LineID:        lineID,
			LineRevision:  lineRevision,
			Request:       normalized,
			RequestHash:   requestHash(normalized),
			DraftOrder:    req.DraftOrder,
			ResubmitOrder: req.ResubmitOrder,
			Allocations:   append([]scheduler.Allocation(nil), result.Allocations...),
			Conflicts:     append([]scheduler.Conflict(nil), result.Conflicts...),
			CreatedAt:     now,
		},
	}
}

func (s *PostgresStore) productionLine(lineID string) (domain.ProductionLine, error) {
	var line domain.ProductionLine
	err := s.db.QueryRow("SELECT id, name, capacity_per_day, COALESCE(timezone, $2), schedule_revision FROM production_lines WHERE id = $1", lineID, defaultLineTimezone).Scan(&line.ID, &line.Name, &line.CapacityPerDay, &line.Timezone, &line.ScheduleRevision)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ProductionLine{}, errors.New("production line does not exist")
	}
	if err != nil {
		return domain.ProductionLine{}, err
	}
	s.MemoryStore.mu.Lock()
	s.MemoryStore.lines[line.ID] = line
	s.MemoryStore.mu.Unlock()
	return line, nil
}

func (s *PostgresStore) schedulerInputs(req scheduleRequest, claims auth.Claims, lineID string) ([]scheduler.OrderInput, error) {
	if req.DraftOrder != nil {
		return s.schedulerDraftInputs(req, claims, lineID)
	}
	if req.ResubmitOrder != nil {
		return s.schedulerResubmitInputs(req, claims, lineID)
	}
	return s.schedulerSelectedInputs(req, lineID)
}

func (s *PostgresStore) schedulerDraftInputs(req scheduleRequest, claims auth.Claims, lineID string) ([]scheduler.OrderInput, error) {
	if claims.Role != domain.RoleSales {
		return nil, errors.New("only sales can preview draft orders")
	}
	draft := *req.DraftOrder
	if draft.LineID == "" {
		draft.LineID = lineID
	}
	if draft.LineID != lineID {
		return nil, errors.New("draft order line must match preview line")
	}
	if draft.Priority == "" {
		draft.Priority = domain.PriorityLow
	}
	if _, err := s.productionLine(draft.LineID); err != nil {
		return nil, err
	}
	if err := validateOrderFields(draft.Customer, draft.Quantity, draft.Note); err != nil {
		return nil, err
	}
	dueDate, err := time.Parse(dateLayout, draft.DueDate)
	if err != nil {
		return nil, errors.New("dueDate must use YYYY-MM-DD")
	}
	inputs, err := s.pendingOrderInputs(lineID, nil)
	if err != nil {
		return nil, err
	}
	// Sales draft previews account for the pending backlog as capacity usage
	// and return those pending preview allocations for the preview dialog only.
	// Scheduler previews/jobs do not use this draft branch, so formal scheduling
	// keeps excluding unrelated pending orders from daily capacity.
	inputs = append(inputs, scheduler.OrderInput{
		ID:                 previewDraftOrderID,
		Customer:           strings.TrimSpace(draft.Customer),
		LineID:             draft.LineID,
		Quantity:           draft.Quantity,
		Priority:           draft.Priority,
		Status:             domain.StatusPending,
		DueDate:            dueDate,
		CreatedAtTimestamp: unixMilliseconds(time.Now().UTC()),
	})
	return inputs, nil
}

func (s *PostgresStore) schedulerResubmitInputs(req scheduleRequest, claims auth.Claims, lineID string) ([]scheduler.OrderInput, error) {
	if claims.Role != domain.RoleSales {
		return nil, errors.New("only sales can preview resubmitted orders")
	}
	order, err := s.preparePreviewResubmitOrder(*req.ResubmitOrder, claims, lineID)
	if err != nil {
		return nil, err
	}
	inputs, err := s.pendingOrderInputs(lineID, nil)
	if err != nil {
		return nil, err
	}
	replaced := false
	for index := range inputs {
		if inputs[index].ID == order.ID {
			inputs[index] = orderInputFromOrder(order)
			replaced = true
			break
		}
	}
	if !replaced {
		inputs = append(inputs, orderInputFromOrder(order))
	}
	return inputs, nil
}

func (s *PostgresStore) preparePreviewResubmitOrder(req resubmitOrderRequest, claims auth.Claims, lineID string) (domain.Order, error) {
	order, err := s.order(req.OrderID)
	if err != nil {
		return domain.Order{}, err
	}
	if order.CreatedBy != claims.Subject {
		return domain.Order{}, errors.New("sales can resubmit only their own orders")
	}
	if order.LineID != lineID {
		return domain.Order{}, errors.New("resubmit order line must match preview line")
	}
	if !canSalesResubmitStatus(order.Status) {
		return domain.Order{}, errors.New("only pending or rejected orders can be resubmitted")
	}
	if strings.TrimSpace(req.Note) != "" {
		return domain.Order{}, errors.New(errNoteImmutable)
	}
	if err := applyOptionalQuantity(&order, req.Quantity); err != nil {
		return domain.Order{}, err
	}
	if req.DueDate != "" {
		line, err := s.productionLine(order.LineID)
		if err != nil {
			return domain.Order{}, err
		}
		currentDate, err := currentDateInLineTimezone(line, nowUTC())
		if err != nil {
			return domain.Order{}, err
		}
		if err := applyOptionalDueDate(&order, req.DueDate, currentDate); err != nil {
			return domain.Order{}, err
		}
	}
	order.Status = domain.StatusPending
	resetRejectedState(&order)
	return order, nil
}

func (s *PostgresStore) schedulerSelectedInputs(req scheduleRequest, lineID string) ([]scheduler.OrderInput, error) {
	inputs, err := s.pendingOrderInputs(lineID, selectedOrderIDMap(req.OrderIDs))
	if err != nil {
		return nil, err
	}
	return inputs, nil
}

func selectedOrderIDMap(orderIDs []string) map[string]bool {
	selected := map[string]bool{}
	for _, id := range orderIDs {
		selected[id] = true
	}
	return selected
}

func (s *PostgresStore) pendingOrderInputs(lineID string, selected map[string]bool) ([]scheduler.OrderInput, error) {
	rows, err := s.db.Query(`
		SELECT id, customer, line_id, quantity, priority, status, due_date, created_at
		FROM orders
		WHERE line_id = $1 AND status = '待排程'
		ORDER BY CASE WHEN priority = 'high' THEN 0 ELSE 1 END, due_date, created_at, id
	`, lineID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	inputs := []scheduler.OrderInput{}
	for rows.Next() {
		var input scheduler.OrderInput
		var createdAt time.Time
		if err := rows.Scan(&input.ID, &input.Customer, &input.LineID, &input.Quantity, &input.Priority, &input.Status, &input.DueDate, &createdAt); err != nil {
			return nil, err
		}
		input.CreatedAtTimestamp = unixMilliseconds(createdAt)
		if len(selected) > 0 && !selected[input.ID] {
			continue
		}
		inputs = append(inputs, input)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return inputs, nil
}

func uniqueOrderIDs(values []string) []string {
	seen := map[string]bool{}
	result := []string{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	return result
}

func (s *PostgresStore) existingAllocations(lineID string) ([]scheduler.ExistingAllocation, error) {
	rows, err := s.db.Query(`
		SELECT order_id, line_id, allocation_date, quantity, priority, locked
		FROM schedule_allocations
		WHERE line_id = $1
	`, lineID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	allocations := []scheduler.ExistingAllocation{}
	for rows.Next() {
		var allocation scheduler.ExistingAllocation
		if err := rows.Scan(&allocation.OrderID, &allocation.LineID, &allocation.Date, &allocation.Quantity, &allocation.Priority, &allocation.Locked); err != nil {
			return nil, err
		}
		allocations = append(allocations, allocation)
	}
	return allocations, rows.Err()
}

func scanOrder(scanner scheduleJobScanner) (domain.Order, error) {
	var order domain.Order
	var rejectedAt sql.NullTime
	err := scanner.Scan(&order.ID, &order.Customer, &order.LineID, &order.Quantity, &order.Priority, &order.Status, &order.DueDate, &order.Note, &order.CreatedBy, &order.SourceOrder, &order.RejectionReason, &order.RejectedBy, &rejectedAt, &order.CreatedAt, &order.UpdatedAt)
	if err != nil {
		return domain.Order{}, err
	}
	if rejectedAt.Valid {
		order.RejectedAt = rejectedAt.Time
	}
	return order, nil
}

func (s *PostgresStore) order(id string) (domain.Order, error) {
	row := s.db.QueryRow(`
		SELECT id, customer, line_id, quantity, priority, status, due_date, COALESCE(note, ''), created_by,
		       COALESCE(source_order, ''), COALESCE(rejection_reason, ''), COALESCE(rejected_by, ''), rejected_at, created_at, updated_at
		FROM orders
		WHERE id = $1
	`, id)
	order, err := scanOrder(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Order{}, errors.New(orderNotFoundMsg)
	}
	return order, err
}

func (s *PostgresStore) updateOrderAndRevision(order domain.Order, actorID, action, reason string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`
		UPDATE orders
		SET quantity = $2, status = $3, due_date = $4, rejection_reason = NULLIF($5, ''),
		    rejected_by = NULLIF($6, ''), rejected_at = $7, updated_at = $8
		WHERE id = $1
	`, order.ID, order.Quantity, order.Status, order.DueDate, order.RejectionReason, order.RejectedBy, nullableTime(order.RejectedAt), order.UpdatedAt); err != nil {
		return err
	}
	if _, err := tx.Exec(bumpProductionLineRevisionSQL, order.LineID); err != nil {
		return err
	}
	if _, err := insertAuditTx(tx, actorID, action, order.ID, reason); err != nil {
		return err
	}
	return tx.Commit()
}

func insertAuditTx(tx *sql.Tx, actorID, action, resource, reason string) (sql.Result, error) {
	return tx.Exec(`
		INSERT INTO audit_logs (id, actor_id, action, resource, reason, created_at)
		VALUES ($1, $2, $3, $4, $5, NOW())
	`, auditID("AUD-"+action+"-"+resource), actorID, action, resource, reason)
}

func auditID(prefix string) string {
	clean := strings.NewReplacer(".", "-", ":", "-", "/", "-").Replace(prefix)
	return clean + "-" + strconv.FormatInt(time.Now().UnixNano(), 10)
}
