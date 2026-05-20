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
	"github.com/lib/pq"
)

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
		SELECT id, username, password_hash, role, COALESCE(line_id, '')
		FROM users
		WHERE username = $1
	`, username).Scan(&user.ID, &user.Username, &user.PasswordHash, &user.Role, &user.LineID)
	if err != nil || password == "" || password != user.PasswordHash {
		return domain.User{}, false
	}
	return user, true
}

func (s *PostgresStore) ListUsers() []domain.User {
	rows, err := s.db.Query("SELECT id, username, password_hash, role, COALESCE(line_id, '') FROM users ORDER BY username")
	if err != nil {
		return s.MemoryStore.ListUsers()
	}
	defer rows.Close()
	users := []domain.User{}
	for rows.Next() {
		var user domain.User
		if err := rows.Scan(&user.ID, &user.Username, &user.PasswordHash, &user.Role, &user.LineID); err == nil {
			users = append(users, user)
		}
	}
	return users
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
		query += " WHERE line_id = $1 AND status <> '需業務處理'"
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
	if _, err := tx.Exec("UPDATE production_lines SET schedule_revision = schedule_revision + 1 WHERE id = $1", order.LineID); err != nil {
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
	if claims.Role == domain.RoleScheduler && order.LineID != claims.LineID {
		return domain.Order{}, errors.New("cannot update another production line")
	}
	if claims.Role == domain.RoleSales && order.CreatedBy != claims.Subject {
		return domain.Order{}, errors.New("sales can update only their own orders")
	}
	if order.Status != domain.StatusPending && order.Status != domain.StatusRejected {
		return domain.Order{}, errors.New("only pending or rejected orders can change order details")
	}
	if strings.TrimSpace(req.Note) != "" {
		return domain.Order{}, errors.New("note cannot be updated after order creation")
	}
	if req.Quantity != 0 {
		if req.Quantity < 25 || req.Quantity > 2500 {
			return domain.Order{}, errors.New("quantity must be between 25 and 2500")
		}
		order.Quantity = req.Quantity
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
		dueDate, err := validateFutureDueDate(req.DueDate, currentDate)
		if err != nil {
			return domain.Order{}, err
		}
		order.DueDate = dueDate
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
		if claims.Role == domain.RoleScheduler && order.LineID != claims.LineID {
			return rejectOrdersResponse{}, errors.New("cannot reject another production line")
		}
		if order.Status != domain.StatusPending {
			return rejectOrdersResponse{}, errors.New("only pending orders can be rejected")
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
	if order.Status != domain.StatusRejected {
		return domain.Order{}, errors.New("only rejected orders can be resubmitted")
	}
	if strings.TrimSpace(req.Note) != "" {
		return domain.Order{}, errors.New("note cannot be updated after order creation")
	}
	if req.Quantity != 0 {
		if req.Quantity < 25 || req.Quantity > 2500 {
			return domain.Order{}, errors.New("quantity must be between 25 and 2500")
		}
		order.Quantity = req.Quantity
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
		dueDate, err := validateFutureDueDate(req.DueDate, currentDate)
		if err != nil {
			return domain.Order{}, err
		}
		order.DueDate = dueDate
	}
	order.Status = domain.StatusPending
	order.RejectionReason = ""
	order.RejectedBy = ""
	order.RejectedAt = time.Time{}
	order.UpdatedAt = time.Now().UTC()
	if err := s.updateOrderAndRevision(order, claims.Subject, "order.resubmit", ""); err != nil {
		return domain.Order{}, err
	}
	return order, nil
}

func (s *PostgresStore) CreateDemoConflictOrders(req demoConflictRequest, claims auth.Claims) ([]domain.Order, error) {
	lineID := req.LineID
	if lineID == "" && claims.Role == domain.RoleScheduler {
		lineID = claims.LineID
	}
	if claims.Role == domain.RoleScheduler && lineID != claims.LineID {
		return nil, errors.New("cannot create demo orders for another production line")
	}
	line, err := s.productionLine(lineID)
	if err != nil {
		return nil, err
	}
	if req.Count == 0 {
		req.Count = 6
	}
	if req.Count < 5 || req.Count > 20 {
		return nil, errors.New("count must be between 5 and 20")
	}
	currentDate, err := currentDateInLineTimezone(line, nowUTC())
	if err != nil {
		return nil, err
	}
	if req.DueDate == "" {
		req.DueDate = currentDate.AddDate(0, 0, 1).Format(dateLayout)
	}
	dueDate, err := validateFutureDueDate(req.DueDate, currentDate)
	if err != nil {
		return nil, err
	}

	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	now := time.Now().UTC()
	orders := make([]domain.Order, 0, req.Count)
	for index := 1; index <= req.Count; index++ {
		createdAt := now.Add(time.Duration(index) * time.Nanosecond)
		order := domain.Order{
			ID:        "ORD-" + strconv.FormatInt(createdAt.UnixNano(), 10),
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
		if _, err := tx.Exec(`
			INSERT INTO orders (id, customer, line_id, quantity, priority, status, due_date, note, created_by, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, '', $8, $9, $9)
		`, order.ID, order.Customer, order.LineID, order.Quantity, order.Priority, order.Status, order.DueDate, order.CreatedBy, order.CreatedAt); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(`
			INSERT INTO audit_logs (id, actor_id, action, resource, reason, created_at)
			VALUES ($1, $2, 'order.create_demo_conflict', $3, $4, $5)
		`, "AUD-"+order.ID, claims.Subject, order.ID, req.DueDate, order.CreatedAt); err != nil {
			return nil, err
		}
		orders = append(orders, order)
	}
	if _, err := tx.Exec("UPDATE production_lines SET schedule_revision = schedule_revision + 1 WHERE id = $1", lineID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return orders, nil
}

func (s *PostgresStore) DeleteOrders(req deleteOrdersRequest, claims auth.Claims) (deleteOrdersResponse, error) {
	if len(req.OrderIDs) == 0 {
		return deleteOrdersResponse{}, errors.New("orderIds is required")
	}
	result := deleteOrdersResponse{}
	tx, err := s.db.Begin()
	if err != nil {
		return deleteOrdersResponse{}, err
	}
	defer tx.Rollback()
	revisions := map[string]bool{}
	for _, id := range req.OrderIDs {
		order, err := s.order(id)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) || strings.Contains(err.Error(), "找不到") || strings.Contains(err.Error(), "order not found") {
				result.SkippedOrderIDs = append(result.SkippedOrderIDs, id)
				continue
			}
			return deleteOrdersResponse{}, err
		}
		if claims.Role == domain.RoleSales && order.CreatedBy != claims.Subject {
			return deleteOrdersResponse{}, errors.New("sales can delete only their own orders")
		}
		if claims.Role == domain.RoleScheduler && order.LineID != claims.LineID {
			return deleteOrdersResponse{}, errors.New("cannot delete another production line")
		}
		if order.Status == domain.StatusInProgress || order.Status == domain.StatusCompleted {
			return deleteOrdersResponse{}, errors.New("cannot delete in-progress or completed orders")
		}
		if _, err := tx.Exec("DELETE FROM schedule_allocations WHERE order_id = $1", id); err != nil {
			return deleteOrdersResponse{}, err
		}
		if _, err := tx.Exec("DELETE FROM orders WHERE id = $1", id); err != nil {
			return deleteOrdersResponse{}, err
		}
		if _, err := insertAuditTx(tx, claims.Subject, "order.delete", id, ""); err != nil {
			return deleteOrdersResponse{}, err
		}
		revisions[order.LineID] = true
		result.DeletedOrderIDs = append(result.DeletedOrderIDs, id)
	}
	for lineID := range revisions {
		if _, err := tx.Exec("UPDATE production_lines SET schedule_revision = schedule_revision + 1 WHERE id = $1", lineID); err != nil {
			return deleteOrdersResponse{}, err
		}
	}
	return result, tx.Commit()
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
		RETURNING id, username, password_hash, role, COALESCE(line_id, '')
	`, req.Username, req.Role, req.LineID).Scan(&user.ID, &user.Username, &user.PasswordHash, &user.Role, &user.LineID)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.User{}, errors.New("user not found")
	}
	if err != nil {
		return domain.User{}, err
	}
	_, _ = s.db.Exec(`
		INSERT INTO audit_logs (id, actor_id, action, resource, reason, created_at)
		VALUES ($1, $2, 'user.assign', $3, $4, NOW())
	`, auditID("AUD-USER-"+user.ID), actorID, user.ID, string(req.Role)+" "+req.LineID)
	return user, nil
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
		PreviewID:   preview.ID,
		CurrentDate: req.CurrentDate,
		Allocations: result.Allocations,
		Conflicts:   result.Conflicts,
		FinishDate:  result.FinishDate,
		DraftOrder:  req.DraftOrder,
	}, nil
}

func (s *PostgresStore) ScheduleCalendar(lineID, month string, claims auth.Claims) (calendarResponse, error) {
	if lineID == "" && claims.Role == domain.RoleScheduler {
		lineID = claims.LineID
	}
	if lineID == "" {
		return calendarResponse{}, errors.New("lineId is required")
	}
	if claims.Role == domain.RoleScheduler && claims.LineID != lineID {
		return calendarResponse{}, errors.New("cannot access another production line")
	}
	if _, err := s.productionLine(lineID); err != nil {
		return calendarResponse{}, err
	}
	if month == "" {
		month = time.Now().UTC().Format("2006-01")
	}
	monthStart, err := time.Parse("2006-01", month)
	if err != nil {
		return calendarResponse{}, errors.New("month must use YYYY-MM")
	}
	calendarStart := monthStart.AddDate(0, 0, -int(monthStart.Weekday()))
	calendarEnd := calendarStart.AddDate(0, 0, 42)
	rows, err := s.db.Query(`
		SELECT a.order_id, o.customer, a.line_id, a.allocation_date, a.quantity, a.priority, COALESCE(a.status, o.status), a.locked, o.due_date
		FROM schedule_allocations a
		JOIN orders o ON o.id = a.order_id
		WHERE a.line_id = $1 AND a.allocation_date >= $2 AND a.allocation_date < $3
		ORDER BY a.allocation_date, a.order_id
	`, lineID, calendarStart, calendarEnd)
	if err != nil {
		return calendarResponse{}, err
	}
	defer rows.Close()
	allocations := []calendarAllocation{}
	for rows.Next() {
		var allocation calendarAllocation
		if err := rows.Scan(&allocation.OrderID, &allocation.Customer, &allocation.LineID, &allocation.Date, &allocation.Quantity, &allocation.Priority, &allocation.Status, &allocation.Locked, &allocation.DueDate); err != nil {
			return calendarResponse{}, err
		}
		allocations = append(allocations, allocation)
	}
	return calendarResponse{LineID: lineID, Month: month, Allocations: allocations}, nil
}

func (s *PostgresStore) ConfirmPreviewOrder(previewID string, claims auth.Claims) (domain.Order, error) {
	var draftRaw sql.NullString
	var actorID string
	var actorRole domain.Role
	err := s.db.QueryRow(`
		SELECT actor_id, actor_role, draft_order
		FROM schedule_previews
		WHERE id = $1 AND expires_at > NOW()
	`, previewID).Scan(&actorID, &actorRole, &draftRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Order{}, errors.New("preview result expired or not found")
	}
	if err != nil {
		return domain.Order{}, err
	}
	if actorID != claims.Subject || actorRole != claims.Role {
		return domain.Order{}, errors.New("preview result belongs to another user")
	}
	if !draftRaw.Valid || draftRaw.String == "" {
		return domain.Order{}, errors.New("preview does not contain a draft order")
	}
	var draft createOrderRequest
	if err := json.Unmarshal([]byte(draftRaw.String), &draft); err != nil {
		return domain.Order{}, err
	}
	order, err := s.CreateOrder(draft, claims.Subject)
	if err != nil {
		return domain.Order{}, err
	}
	_, _ = s.db.Exec("DELETE FROM schedule_previews WHERE id = $1", previewID)
	return order, nil
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
		WHERE a.action IN ('schedule.job.create','schedule.job.manual_force','schedule.job.complete','schedule.job.fail','order.reject','production.start','production.confirm.complete','production.confirm.partial')`
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
	if _, err := tx.Exec("UPDATE production_lines SET schedule_revision = schedule_revision + 1 WHERE id = $1", order.LineID); err != nil {
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
	order, err := s.order(req.OrderID)
	if err != nil {
		return productionConfirmResponse{}, err
	}
	if order.LineID != claims.LineID {
		return productionConfirmResponse{}, errors.New("cannot confirm another production line")
	}
	if order.Status != domain.StatusInProgress {
		return productionConfirmResponse{}, errors.New("only in-progress orders can be confirmed")
	}
	if req.ProducedQuantity <= 0 {
		return productionConfirmResponse{}, errors.New("producedQuantity must be greater than zero")
	}
	productionDate, err := time.Parse(dateLayout, req.ProductionDate)
	if err != nil {
		return productionConfirmResponse{}, errors.New("productionDate must use YYYY-MM-DD")
	}
	var allocation domain.ScheduleAllocation
	err = s.db.QueryRow(`
		SELECT order_id, line_id, allocation_date, quantity, priority, locked, COALESCE(status, '已排程')
		FROM schedule_allocations
		WHERE order_id = $1 AND allocation_date = $2
		LIMIT 1
	`, order.ID, productionDate).Scan(&allocation.OrderID, &allocation.LineID, &allocation.Date, &allocation.Quantity, &allocation.Priority, &allocation.Locked, &allocation.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return productionConfirmResponse{}, errors.New("scheduled allocation not found for productionDate")
	}
	if err != nil {
		return productionConfirmResponse{}, err
	}
	if allocation.Status == domain.StatusCompleted {
		return productionConfirmResponse{}, errors.New("productionDate has already been confirmed")
	}
	if req.ProducedQuantity > allocation.Quantity {
		return productionConfirmResponse{}, errors.New("producedQuantity cannot exceed scheduled allocation quantity")
	}
	completed := req.ProducedQuantity >= order.Quantity
	tx, err := s.db.Begin()
	if err != nil {
		return productionConfirmResponse{}, err
	}
	defer tx.Rollback()
	action := "production.confirm.partial"
	if completed {
		action = "production.confirm.complete"
		order.Status = domain.StatusCompleted
		if _, err := tx.Exec("UPDATE orders SET status = '已完成', updated_at = NOW() WHERE id = $1", order.ID); err != nil {
			return productionConfirmResponse{}, err
		}
	} else {
		order.Quantity -= req.ProducedQuantity
		order.Status = domain.StatusPending
		if _, err := tx.Exec("UPDATE orders SET status = '待排程', quantity = $2, updated_at = NOW() WHERE id = $1", order.ID, order.Quantity); err != nil {
			return productionConfirmResponse{}, err
		}
	}
	if _, err := tx.Exec("UPDATE schedule_allocations SET quantity = $3, locked = TRUE, status = '已完成' WHERE order_id = $1 AND allocation_date = $2", order.ID, productionDate, req.ProducedQuantity); err != nil {
		return productionConfirmResponse{}, err
	}
	if _, err := tx.Exec("UPDATE production_lines SET schedule_revision = schedule_revision + 1 WHERE id = $1", order.LineID); err != nil {
		return productionConfirmResponse{}, err
	}
	if _, err := insertAuditTx(tx, claims.Subject, action, order.ID, ""); err != nil {
		return productionConfirmResponse{}, err
	}
	if err := tx.Commit(); err != nil {
		return productionConfirmResponse{}, err
	}
	if completed {
		return productionConfirmResponse{Order: order}, nil
	}
	return productionConfirmResponse{Order: order, Remainder: &order}, nil
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
		if _, err := tx.Exec(`
			INSERT INTO production_lines (id, name, capacity_per_day, timezone, schedule_revision)
			VALUES ($1, $2, 10000, $3, 0)
			ON CONFLICT (id) DO UPDATE SET name = EXCLUDED.name, capacity_per_day = EXCLUDED.capacity_per_day, timezone = EXCLUDED.timezone, schedule_revision = 0
		`, lineID, "HPA Demo Line "+lineID, defaultLineTimezone); err != nil {
			return hpaPeakSummary{}, err
		}

		orderIDs := make([]string, 0, hpaDemoOrdersPerLine)
		for orderIndex := 1; orderIndex <= hpaDemoOrdersPerLine; orderIndex++ {
			orderID := fmt.Sprintf("HPA-%s-%03d", lineID, orderIndex)
			orderIDs = append(orderIDs, orderID)
			if _, err := tx.Exec(`
				INSERT INTO orders (id, customer, line_id, quantity, priority, status, due_date, note, created_by, created_at, updated_at)
				VALUES ($1, 'HPA Demo', $2, 2500, 'low', '待排程', $3, $4, $5, $6, $6)
			`, orderID, lineID, now.AddDate(0, 0, 7), hpaDemoSource, claims.Subject, now); err != nil {
				return hpaPeakSummary{}, err
			}
		}

		jobID := "HPA-JOB-" + lineID
		orderJSON, _ := json.Marshal(orderIDs)
		if _, err := tx.Exec(`
			INSERT INTO schedule_jobs (id, line_id, status, message, source, order_ids, created_at, updated_at)
			VALUES ($1, $2, 'queued', '多產線排程尖峰任務已送入背景佇列。', $3, $4::jsonb, $5, $5)
		`, jobID, lineID, hpaDemoSource, string(orderJSON), now); err != nil {
			return hpaPeakSummary{}, err
		}
		if _, err := tx.Exec(`
			INSERT INTO audit_logs (id, actor_id, action, resource, reason, created_at)
			VALUES ($1, $2, 'schedule.job.create', $3, $4, $5)
		`, "AUD-HPA-"+lineID, claims.Subject, jobID, hpaDemoSource, now); err != nil {
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
	summary := hpaPeakSummary{
		Statuses: map[string]int{
			string(domain.JobQueued):    0,
			string(domain.JobRunning):   0,
			string(domain.JobCompleted): 0,
			string(domain.JobFailed):    0,
			string(domain.JobCancelled): 0,
		},
		Topic:          "woms.schedule.jobs",
		ConsumerGroup:  "woms-scheduler-workers",
		HPAName:        "woms-woms-worker-hpa",
		DeploymentName: "woms-woms-worker",
		Reason:         "幾百條產線同時進行月底排程，Kafka lag 上升時 KEDA 會擴充 scheduler-worker pods。",
		WatchCommand:   "kubectl get hpa,deploy,pod -n woms -w",
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
	lineID := scheduleLineID(req, claims)
	if lineID == "" {
		return scheduler.Result{}, previewFromDBResult{}, errors.New("lineId is required")
	}
	if claims.Role == domain.RoleScheduler && claims.LineID != lineID {
		return scheduler.Result{}, previewFromDBResult{}, errors.New("cannot access another production line")
	}
	line, err := s.productionLine(lineID)
	if err != nil {
		return scheduler.Result{}, previewFromDBResult{}, err
	}
	startDate := time.Now().UTC()
	if req.StartDate != "" {
		parsed, err := time.Parse(dateLayout, req.StartDate)
		if err != nil {
			return scheduler.Result{}, previewFromDBResult{}, errors.New("startDate must use YYYY-MM-DD")
		}
		startDate = parsed
	}
	currentDate := time.Time{}
	if req.CurrentDate != "" {
		parsed, err := time.Parse(dateLayout, req.CurrentDate)
		if err != nil {
			return scheduler.Result{}, previewFromDBResult{}, errors.New("currentDate must use YYYY-MM-DD")
		}
		currentDate = parsed
	}
	inputs, err := s.schedulerInputs(req, claims, lineID)
	if err != nil {
		return scheduler.Result{}, previewFromDBResult{}, err
	}
	existing, err := s.existingAllocations(lineID, req.ResolutionOrderIDs)
	if err != nil {
		return scheduler.Result{}, previewFromDBResult{}, err
	}
	result, err := scheduler.Plan(scheduler.Request{
		LineID:              lineID,
		CapacityPerDay:      line.CapacityPerDay,
		StartDate:           startDate,
		CurrentDate:         currentDate,
		Orders:              inputs,
		ExistingAllocations: existing,
		ManualForce:         req.ManualForce,
		ForceReason:         req.Reason,
		AllowLateCompletion: req.AllowLateCompletion,
	})
	if err != nil {
		return scheduler.Result{}, previewFromDBResult{}, err
	}
	normalized := normalizedPreviewRequest(req)
	now := time.Now().UTC()
	id := "PREVIEW-" + strconv.FormatInt(now.UnixNano(), 10)
	return result, previewFromDBResult{
		ID: id,
		record: previewRecord{
			ActorID:      claims.Subject,
			ActorRole:    claims.Role,
			LineID:       lineID,
			LineRevision: line.ScheduleRevision,
			Request:      normalized,
			RequestHash:  requestHash(normalized),
			DraftOrder:   req.DraftOrder,
			Allocations:  append([]scheduler.Allocation(nil), result.Allocations...),
			Conflicts:    append([]scheduler.Conflict(nil), result.Conflicts...),
			CreatedAt:    now,
		},
	}, nil
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
		if claims.Role != domain.RoleSales {
			return nil, errors.New("only sales can preview draft orders")
		}
		if len(req.ResolutionOrderIDs) > 0 {
			return nil, errors.New("draft previews cannot include resolution orders")
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
		return []scheduler.OrderInput{{ID: "PREVIEW-DRAFT", LineID: draft.LineID, Quantity: draft.Quantity, Priority: draft.Priority, DueDate: dueDate}}, nil
	}
	selected := map[string]bool{}
	for _, id := range req.OrderIDs {
		selected[id] = true
	}
	rows, err := s.db.Query(`
		SELECT id, line_id, quantity, priority, due_date
		FROM orders
		WHERE line_id = $1 AND status = '待排程'
		ORDER BY due_date, id
	`, lineID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	inputs := []scheduler.OrderInput{}
	for rows.Next() {
		var input scheduler.OrderInput
		if err := rows.Scan(&input.ID, &input.LineID, &input.Quantity, &input.Priority, &input.DueDate); err != nil {
			return nil, err
		}
		if len(selected) > 0 && !selected[input.ID] {
			continue
		}
		inputs = append(inputs, input)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	resolutionInputs, err := s.resolutionOrderInputs(req.ResolutionOrderIDs, lineID)
	if err != nil {
		return nil, err
	}
	inputs = append(inputs, resolutionInputs...)
	return inputs, nil
}

func (s *PostgresStore) resolutionOrderInputs(resolutionOrderIDs []string, lineID string) ([]scheduler.OrderInput, error) {
	ids := uniqueOrderIDs(resolutionOrderIDs)
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := s.db.Query(`
		SELECT id, line_id, quantity, priority, status, due_date
		FROM orders
		WHERE id = ANY($1)
	`, pq.Array(ids))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	inputs := []scheduler.OrderInput{}
	found := map[string]bool{}
	for rows.Next() {
		var id string
		var orderLineID string
		var quantity int
		var priority domain.Priority
		var status string
		var dueDate time.Time
		if err := rows.Scan(&id, &orderLineID, &quantity, &priority, &status, &dueDate); err != nil {
			return nil, err
		}
		if orderLineID != lineID {
			return nil, errors.New("resolution order line must match preview line")
		}
		if status != string(domain.StatusScheduled) {
			return nil, errors.New("resolution orders must be low-priority scheduled orders without locked or completed allocations")
		}
		if priority != domain.PriorityLow {
			return nil, errors.New("resolution orders must be low-priority scheduled orders without locked or completed allocations")
		}
		if err := s.ensureResolutionOrderMovable(id); err != nil {
			return nil, err
		}
		inputs = append(inputs, scheduler.OrderInput{
			ID:       id,
			LineID:   orderLineID,
			Quantity: quantity,
			Priority: priority,
			DueDate:  dueDate,
		})
		found[id] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, id := range ids {
		if !found[id] {
			return nil, errors.New("resolution order not found")
		}
	}
	return inputs, nil
}

func (s *PostgresStore) ensureResolutionOrderMovable(orderID string) error {
	rows, err := s.db.Query(`
		SELECT locked, COALESCE(status, $1)
		FROM schedule_allocations
		WHERE order_id = $2
	`, string(domain.StatusScheduled), orderID)
	if err != nil {
		return err
	}
	defer rows.Close()

	hasAllocation := false
	for rows.Next() {
		var locked bool
		var status string
		if err := rows.Scan(&locked, &status); err != nil {
			return err
		}
		hasAllocation = true
		if locked || status == string(domain.StatusInProgress) || status == string(domain.StatusCompleted) {
			return errors.New("resolution orders must be low-priority scheduled orders without locked or completed allocations")
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if !hasAllocation {
		return errors.New("resolution orders must be low-priority scheduled orders without locked or completed allocations")
	}
	return nil
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

func (s *PostgresStore) existingAllocations(lineID string, resolutionOrderIDs []string) ([]scheduler.ExistingAllocation, error) {
	resolution := map[string]bool{}
	for _, id := range resolutionOrderIDs {
		resolution[id] = true
	}
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
		if resolution[allocation.OrderID] {
			continue
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
		return domain.Order{}, errors.New("order not found")
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
	if _, err := tx.Exec("UPDATE production_lines SET schedule_revision = schedule_revision + 1 WHERE id = $1", order.LineID); err != nil {
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
