package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/d11nn/woms/internal/auth"
	"github.com/d11nn/woms/internal/domain"
	"github.com/d11nn/woms/internal/metrics"
	"github.com/d11nn/woms/internal/scheduler"
)

const dateLayout = "2006-01-02"
const hpaDemoSource = "hpa-peak-demo"
const hpaDemoFirstLine = 1
const hpaDemoLastLine = 200
const hpaDemoOrdersPerLine = 5
const unacceptableDueDateMessage = "無法被接受的交期"
const defaultLineTimezone = "Asia/Taipei"
const orderIDDigits = 7
const orderIDModulo int64 = 10000000

var nowUTC = func() time.Time {
	return time.Now().UTC()
}

type Server struct {
	jwtSecret string
	store     Store
	publisher ScheduleJobPublisher
}

func NewServer(jwtSecret string, store *MemoryStore) *Server {
	return NewServerWithPublisher(jwtSecret, store, NoopScheduleJobPublisher{})
}

func NewServerWithPublisher(jwtSecret string, store Store, publisher ScheduleJobPublisher) *Server {
	if store == nil {
		store = NewMemoryStore()
	}
	if publisher == nil {
		publisher = NoopScheduleJobPublisher{}
	}
	metrics.Register()
	return &Server{jwtSecret: jwtSecret, store: store, publisher: publisher}
}

// statusRecorder wraps http.ResponseWriter to capture the HTTP status code
// written by a handler so ServeHTTP can record it in HTTPRequestsTotal.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Write captures an implicit 200 if WriteHeader was never called.
func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.ResponseWriter.Write(b)
}

type Store interface {
	Authenticate(username, password string) (domain.User, bool)
	ListOrders(claims auth.Claims) []domain.Order
	ListLines() []domain.ProductionLine
	CreateOrder(req createOrderRequest, actorID string) (domain.Order, error)
	DeleteOrders(req deleteOrdersRequest, claims auth.Claims) (deleteOrdersResponse, error)
	UpdateOrderDueDate(id string, req updateOrderRequest, claims auth.Claims) (domain.Order, error)
	ConfirmPreviewOrder(previewID string, claims auth.Claims) (domain.Order, error)
	RejectOrders(req rejectOrdersRequest, claims auth.Claims) (rejectOrdersResponse, error)
	ResubmitOrder(req resubmitOrderRequest, claims auth.Claims) (domain.Order, error)
	ListUsers() []domain.User
	AssignUser(req assignUserRequest, actorID string) (domain.User, error)
	CreateDemoConflictOrders(req demoConflictRequest, claims auth.Claims) ([]domain.Order, error)
	PreviewSchedule(req scheduleRequest, claims auth.Claims) (schedulePreviewResponse, error)
	CreateScheduleJob(req scheduleRequest, claims auth.Claims) (domain.ScheduleJob, error)
	DeleteQueuedScheduleJob(id string)
	ExecuteScheduleJob(id string) domain.ScheduleJob
	GetScheduleJob(id string) (domain.ScheduleJob, bool)
	ScheduleCalendar(lineID, month string, claims auth.Claims) (calendarResponse, error)
	ScheduleHistory(lineID string, claims auth.Claims) ([]domain.AuditEntry, error)
	StartProduction(req productionStartRequest, claims auth.Claims) (domain.Order, error)
	ConfirmProduction(req productionConfirmRequest, claims auth.Claims) (productionConfirmResponse, error)
	CreateHPAPeakDemo(claims auth.Claims) (hpaPeakSummary, error)
	ClearHPAPeakDemo(claims auth.Claims) (hpaPeakSummary, error)
	HPAPeakSummary() hpaPeakSummary
	HPAPeakJobs() []domain.ScheduleJob
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	setSecurityHeaders(w)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// Expose Prometheus metrics endpoint (unauthenticated, for scraping).
	if r.Method == http.MethodGet && r.URL.Path == "/metrics" {
		metrics.Handler().ServeHTTP(w, r)
		return
	}

	// Wrap the writer to capture the response status for HTTPRequestsTotal.
	rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
	defer func() {
		metrics.HTTPRequestsTotal.WithLabelValues(
			r.Method,
			r.URL.Path,
			strconv.Itoa(rec.status),
		).Inc()
	}()

	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/healthz":
		writeJSON(rec, http.StatusOK, map[string]string{"status": "ok"})
	case r.Method == http.MethodGet && r.URL.Path == "/readyz":
		writeJSON(rec, http.StatusOK, map[string]string{"status": "ready"})
	case r.Method == http.MethodPost && r.URL.Path == "/api/auth/login":
		s.handleLogin(rec, r)
	case r.Method == http.MethodGet && r.URL.Path == "/internal/auth/verify":
		s.handleIngressAuth(rec, r)
	case r.URL.Path == "/api/orders":
		s.handleOrders(rec, r)
	case r.Method == http.MethodGet && r.URL.Path == "/api/lines":
		s.handleLines(rec, r)
	case r.Method == http.MethodPost && r.URL.Path == "/api/orders/preview-confirm":
		s.handleConfirmPreviewOrder(rec, r)
	case r.Method == http.MethodPost && r.URL.Path == "/api/orders/reject":
		s.handleRejectOrders(rec, r)
	case r.Method == http.MethodPost && r.URL.Path == "/api/orders/resubmit":
		s.handleResubmitOrder(rec, r)
	case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/api/orders/"):
		s.handleUpdateOrder(rec, r)
	case r.URL.Path == "/api/users":
		s.handleUsers(rec, r)
	case r.Method == http.MethodPost && r.URL.Path == "/api/demo/conflict-orders":
		s.handleDemoConflictOrders(rec, r)
	case r.URL.Path == "/api/demo/hpa-peak":
		s.handleHPAPeakDemo(rec, r)
	case r.Method == http.MethodPost && r.URL.Path == "/api/schedules/preview":
		s.handleSchedulePreview(rec, r)
	case r.Method == http.MethodGet && r.URL.Path == "/api/schedules/calendar":
		s.handleScheduleCalendar(rec, r)
	case r.Method == http.MethodGet && r.URL.Path == "/api/schedules/history":
		s.handleScheduleHistory(rec, r)
	case r.URL.Path == "/api/schedules/jobs":
		s.handleScheduleJobs(rec, r)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/schedules/jobs/"):
		s.handleGetScheduleJob(rec, r)
	case r.Method == http.MethodPost && r.URL.Path == "/api/production/confirm":
		s.handleProductionConfirm(rec, r)
	case r.Method == http.MethodPost && r.URL.Path == "/api/production/start":
		s.handleProductionStart(rec, r)
	default:
		writeError(rec, http.StatusNotFound, "route not found")
	}
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	user, ok := s.store.Authenticate(req.Username, req.Password)
	if !ok {
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	token, err := auth.CreateToken(s.jwtSecret, auth.Claims{
		Subject: user.ID,
		Role:    user.Role,
		LineID:  user.LineID,
	}, 8*time.Hour)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	metrics.CurrentOnlineUserCount.Inc()
	writeJSON(w, http.StatusOK, map[string]any{
		"token": token,
		"user":  user,
	})
}

func (s *Server) handleIngressAuth(w http.ResponseWriter, r *http.Request) {
	if _, err := s.claimsFromRequest(r); err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleOrders(w http.ResponseWriter, r *http.Request) {
	claims, err := s.claimsFromRequest(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	switch r.Method {
	case http.MethodGet:
		orders := s.store.ListOrders(claims)
		writeJSON(w, http.StatusOK, map[string]any{"orders": orders})
	case http.MethodPost:
		if claims.Role != domain.RoleSales {
			writeError(w, http.StatusForbidden, "only sales can create orders")
			return
		}
		var req createOrderRequest
		if err := readJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		order, err := s.store.CreateOrder(req, claims.Subject)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, order)
	case http.MethodDelete:
		var req deleteOrdersRequest
		if err := readJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		result, err := s.store.DeleteOrders(req, claims)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, result)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) handleLines(w http.ResponseWriter, r *http.Request) {
	if _, err := s.claimsFromRequest(r); err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"lines": s.store.ListLines()})
}

func (s *Server) handleConfirmPreviewOrder(w http.ResponseWriter, r *http.Request) {
	claims, err := s.claimsFromRequest(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if claims.Role != domain.RoleSales {
		writeError(w, http.StatusForbidden, "only sales can confirm preview orders")
		return
	}
	var req confirmPreviewRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	order, err := s.store.ConfirmPreviewOrder(req.PreviewID, claims)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, order)
}

func (s *Server) handleRejectOrders(w http.ResponseWriter, r *http.Request) {
	claims, err := s.claimsFromRequest(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if claims.Role != domain.RoleScheduler {
		writeError(w, http.StatusForbidden, "only schedulers can reject orders")
		return
	}
	var req rejectOrdersRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	result, err := s.store.RejectOrders(req, claims)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleResubmitOrder(w http.ResponseWriter, r *http.Request) {
	claims, err := s.claimsFromRequest(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if claims.Role != domain.RoleSales {
		writeError(w, http.StatusForbidden, "only sales can resubmit rejected orders")
		return
	}
	var req resubmitOrderRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	order, err := s.store.ResubmitOrder(req, claims)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, order)
}

func (s *Server) handleUsers(w http.ResponseWriter, r *http.Request) {
	claims, err := s.claimsFromRequest(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if claims.Role != domain.RoleAdmin {
		writeError(w, http.StatusForbidden, "only admin can manage accounts")
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"users": s.store.ListUsers()})
	case http.MethodPatch:
		var req assignUserRequest
		if err := readJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		user, err := s.store.AssignUser(req, claims.Subject)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, user)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) handleDemoConflictOrders(w http.ResponseWriter, r *http.Request) {
	claims, err := s.claimsFromRequest(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if claims.Role != domain.RoleAdmin && claims.Role != domain.RoleScheduler {
		writeError(w, http.StatusForbidden, "only admin or schedulers can create demo conflict orders")
		return
	}
	var req demoConflictRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	orders, err := s.store.CreateDemoConflictOrders(req, claims)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"orders": orders})
}

func (s *Server) handleSchedulePreview(w http.ResponseWriter, r *http.Request) {
	claims, err := s.claimsFromRequest(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var req scheduleRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	result, err := s.store.PreviewSchedule(req, claims)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleHPAPeakDemo(w http.ResponseWriter, r *http.Request) {
	claims, err := s.claimsFromRequest(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if claims.Role != domain.RoleAdmin {
		writeError(w, http.StatusForbidden, "只有管理員可以觸發多產線排程尖峰。")
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, hpaPeakResponse{Summary: s.store.HPAPeakSummary()})
	case http.MethodPost:
		summary, err := s.store.CreateHPAPeakDemo(claims)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		for _, job := range s.store.HPAPeakJobs() {
			if err := s.publisher.PublishScheduleJob(r.Context(), job); err != nil {
				_, _ = s.store.ClearHPAPeakDemo(claims)
				writeError(w, http.StatusBadGateway, "排程任務送出失敗，請稍後再試。")
				return
			}
		}
		writeJSON(w, http.StatusAccepted, hpaPeakResponse{Summary: summary})
	case http.MethodDelete:
		summary, err := s.store.ClearHPAPeakDemo(claims)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, hpaPeakResponse{Summary: summary})
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) handleScheduleCalendar(w http.ResponseWriter, r *http.Request) {
	claims, err := s.claimsFromRequest(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	lineID := r.URL.Query().Get("lineId")
	month := r.URL.Query().Get("month")
	result, err := s.store.ScheduleCalendar(lineID, month, claims)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleScheduleHistory(w http.ResponseWriter, r *http.Request) {
	claims, err := s.claimsFromRequest(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	lineID := r.URL.Query().Get("lineId")
	history, err := s.store.ScheduleHistory(lineID, claims)
	if err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"history": history})
}

func (s *Server) handleScheduleJobs(w http.ResponseWriter, r *http.Request) {
	claims, err := s.claimsFromRequest(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if claims.Role != domain.RoleScheduler {
		writeError(w, http.StatusForbidden, "only schedulers can create schedule jobs")
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req scheduleRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	job, err := s.store.CreateScheduleJob(req, claims)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.publisher.PublishScheduleJob(r.Context(), job); err != nil {
		s.store.DeleteQueuedScheduleJob(job.ID)
		writeError(w, http.StatusBadGateway, "排程任務送出失敗，請稍後再試。")
		return
	}
	if _, ok := s.publisher.(NoopScheduleJobPublisher); ok {
		job = s.store.ExecuteScheduleJob(job.ID)
	}
	writeJSON(w, http.StatusAccepted, job)
}

func (s *Server) handleGetScheduleJob(w http.ResponseWriter, r *http.Request) {
	claims, err := s.claimsFromRequest(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/schedules/jobs/")
	job, ok := s.store.GetScheduleJob(id)
	if !ok {
		writeError(w, http.StatusNotFound, "schedule job not found")
		return
	}
	if claims.Role == domain.RoleScheduler && claims.LineID != job.LineID {
		writeError(w, http.StatusForbidden, "cannot access another production line")
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (s *Server) handleProductionConfirm(w http.ResponseWriter, r *http.Request) {
	claims, err := s.claimsFromRequest(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if claims.Role != domain.RoleScheduler {
		writeError(w, http.StatusForbidden, "only schedulers can confirm production")
		return
	}
	var req productionConfirmRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	result, err := s.store.ConfirmProduction(req, claims)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) claimsFromRequest(r *http.Request) (auth.Claims, error) {
	token, err := auth.BearerToken(r.Header.Get("Authorization"))
	if err != nil {
		return auth.Claims{}, err
	}
	return auth.VerifyToken(s.jwtSecret, token)
}

type MemoryStore struct {
	mu            sync.Mutex
	nextOrderID   int
	nextJobID     int
	nextAuditID   int
	nextPreviewID int
	lines         map[string]domain.ProductionLine
	users         map[string]domain.User
	orders        map[string]domain.Order
	jobs          map[string]domain.ScheduleJob
	jobRequests   map[string]scheduleRequest
	allocations   []domain.ScheduleAllocation
	previews      map[string]previewRecord
	audits        []domain.AuditEntry
	lineLocks     map[string]bool
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		nextOrderID:   1,
		nextJobID:     1,
		nextAuditID:   1,
		nextPreviewID: 1,
		lines: map[string]domain.ProductionLine{
			"A": {ID: "A", Name: "Line A", CapacityPerDay: 10000, Timezone: defaultLineTimezone},
			"B": {ID: "B", Name: "Line B", CapacityPerDay: 10000, Timezone: defaultLineTimezone},
			"C": {ID: "C", Name: "Line C", CapacityPerDay: 10000, Timezone: defaultLineTimezone},
			"D": {ID: "D", Name: "Line D", CapacityPerDay: 10000, Timezone: "Europe/London"},
		},
		users: map[string]domain.User{
			"admin":       {ID: "user-admin", Username: "admin", PasswordHash: "demo", Role: domain.RoleAdmin},
			"sales":       {ID: "user-sales", Username: "sales", PasswordHash: "demo", Role: domain.RoleSales},
			"scheduler-a": {ID: "user-scheduler-a", Username: "scheduler-a", PasswordHash: "demo", Role: domain.RoleScheduler, LineID: "A"},
			"scheduler-b": {ID: "user-scheduler-b", Username: "scheduler-b", PasswordHash: "demo", Role: domain.RoleScheduler, LineID: "B"},
			"scheduler-c": {ID: "user-scheduler-c", Username: "scheduler-c", PasswordHash: "demo", Role: domain.RoleScheduler, LineID: "C"},
			"scheduler-d": {ID: "user-scheduler-d", Username: "scheduler-d", PasswordHash: "demo", Role: domain.RoleScheduler, LineID: "D"},
		},
		orders:      map[string]domain.Order{},
		jobs:        map[string]domain.ScheduleJob{},
		jobRequests: map[string]scheduleRequest{},
		allocations: []domain.ScheduleAllocation{},
		previews:    map[string]previewRecord{},
		lineLocks:   map[string]bool{},
	}
}

func (s *MemoryStore) Authenticate(username, password string) (domain.User, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	user, ok := s.users[username]
	if !ok || password == "" || password != user.PasswordHash {
		return domain.User{}, false
	}
	return user, true
}

type createOrderRequest struct {
	Customer string          `json:"customer"`
	LineID   string          `json:"lineId"`
	Quantity int             `json:"quantity"`
	Priority domain.Priority `json:"priority"`
	DueDate  string          `json:"dueDate"`
	Note     string          `json:"note"`
}

type deleteOrdersRequest struct {
	OrderIDs []string `json:"orderIds"`
}

type deleteOrdersResponse struct {
	DeletedOrderIDs []string `json:"deletedOrderIds"`
	SkippedOrderIDs []string `json:"skippedOrderIds,omitempty"`
}

type updateOrderRequest struct {
	DueDate  string `json:"dueDate"`
	Quantity int    `json:"quantity"`
	Note     string `json:"note"`
}

type rejectOrdersRequest struct {
	OrderIDs []string `json:"orderIds"`
	Reason   string   `json:"reason"`
}

type rejectOrdersResponse struct {
	Orders []domain.Order `json:"orders"`
}

type resubmitOrderRequest struct {
	OrderID  string `json:"orderId"`
	DueDate  string `json:"dueDate"`
	Quantity int    `json:"quantity"`
	Note     string `json:"note"`
}

type assignUserRequest struct {
	Username string      `json:"username"`
	Role     domain.Role `json:"role"`
	LineID   string      `json:"lineId"`
}

type confirmPreviewRequest struct {
	PreviewID string `json:"previewId"`
}

type demoConflictRequest struct {
	LineID  string `json:"lineId"`
	DueDate string `json:"dueDate"`
	Count   int    `json:"count"`
}

type hpaPeakSummary struct {
	LineCount      int                  `json:"lineCount"`
	OrderCount     int                  `json:"orderCount"`
	JobCount       int                  `json:"jobCount"`
	Statuses       map[string]int       `json:"statuses"`
	Topic          string               `json:"topic"`
	ConsumerGroup  string               `json:"consumerGroup"`
	HPAName        string               `json:"hpaName"`
	DeploymentName string               `json:"deploymentName"`
	Reason         string               `json:"reason"`
	WatchCommand   string               `json:"watchCommand"`
	FailedMessages []string             `json:"failedMessages,omitempty"`
	RecentJobs     []domain.ScheduleJob `json:"recentJobs,omitempty"`
}

type hpaPeakResponse struct {
	Summary hpaPeakSummary `json:"summary"`
}

type previewRecord struct {
	ActorID      string
	ActorRole    domain.Role
	LineID       string
	LineRevision int64
	Request      scheduleRequest
	RequestHash  string
	DraftOrder   *createOrderRequest
	Allocations  []scheduler.Allocation
	Conflicts    []scheduler.Conflict
	CreatedAt    time.Time
}

type schedulePreviewResponse struct {
	PreviewID   string                 `json:"previewId"`
	CurrentDate string                 `json:"currentDate"`
	Allocations []scheduler.Allocation `json:"allocations"`
	Conflicts   []scheduler.Conflict   `json:"conflicts"`
	FinishDate  time.Time              `json:"finishDate"`
	DraftOrder  *createOrderRequest    `json:"draftOrder,omitempty"`
}

func (s *MemoryStore) CreateOrder(req createOrderRequest, actorID string) (domain.Order, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.createOrderLocked(req, actorID)
}

func (s *Server) handleUpdateOrder(w http.ResponseWriter, r *http.Request) {
	claims, err := s.claimsFromRequest(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/orders/")
	var req updateOrderRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	order, err := s.store.UpdateOrderDueDate(id, req, claims)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, order)
}

func (s *MemoryStore) UpdateOrderDueDate(id string, req updateOrderRequest, claims auth.Claims) (domain.Order, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := nowUTC()
	order, ok := s.orders[id]
	if !ok {
		return domain.Order{}, errors.New("order not found")
	}
	if claims.Role == domain.RoleScheduler && order.LineID != claims.LineID {
		return domain.Order{}, errors.New("cannot update another production line")
	}
	if claims.Role == domain.RoleSales && order.CreatedBy != claims.Subject {
		return domain.Order{}, errors.New("sales can update only their own orders")
	}
	if claims.Role != domain.RoleAdmin && claims.Role != domain.RoleSales && claims.Role != domain.RoleScheduler {
		return domain.Order{}, errors.New("role cannot update orders")
	}
	if order.Status != domain.StatusPending && order.Status != domain.StatusRejected {
		return domain.Order{}, errors.New("only pending or rejected orders can change order details")
	}
	if strings.TrimSpace(req.Note) != "" {
		return domain.Order{}, errors.New("note cannot be updated after order creation")
	}
	if req.DueDate != "" {
		currentDate, err := s.currentDateForLineLocked(order.LineID, now)
		if err != nil {
			return domain.Order{}, err
		}
		dueDate, err := validateFutureDueDate(req.DueDate, currentDate)
		if err != nil {
			return domain.Order{}, err
		}
		order.DueDate = dueDate
	}
	if req.Quantity != 0 {
		if req.Quantity < 25 || req.Quantity > 2500 {
			return domain.Order{}, errors.New("quantity must be between 25 and 2500")
		}
		order.Quantity = req.Quantity
	}
	order.UpdatedAt = now
	s.orders[order.ID] = order
	s.bumpLineRevisionLocked(order.LineID)
	s.auditLocked(claims.Subject, "order.update_due_date", order.ID, req.DueDate)
	return order, nil
}

func (s *MemoryStore) createOrderLocked(req createOrderRequest, actorID string) (domain.Order, error) {
	now := nowUTC()
	currentDate, err := s.currentDateForLineLocked(req.LineID, now)
	if err != nil {
		return domain.Order{}, err
	}
	dueDate, err := validateOrderRequest(req, s.lines, currentDate)
	if err != nil {
		return domain.Order{}, err
	}

	id := orderIDFromSequence(s.nextOrderID)
	s.nextOrderID++
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
	s.orders[id] = order
	s.bumpLineRevisionLocked(order.LineID)
	s.auditLocked(actorID, "order.create", id, "")
	return order, nil
}

func (s *MemoryStore) RejectOrders(req rejectOrdersRequest, claims auth.Claims) (rejectOrdersResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

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
	now := time.Now().UTC()
	result := rejectOrdersResponse{Orders: []domain.Order{}}
	for _, id := range req.OrderIDs {
		order, ok := s.orders[id]
		if !ok {
			return rejectOrdersResponse{}, errors.New("order not found: " + id)
		}
		if order.LineID != claims.LineID {
			return rejectOrdersResponse{}, errors.New("cannot reject another production line")
		}
		if order.Status != domain.StatusPending {
			return rejectOrdersResponse{}, errors.New("only pending orders can be rejected")
		}
		order.Status = domain.StatusRejected
		order.RejectionReason = reason
		order.RejectedBy = claims.Subject
		order.RejectedAt = now
		order.UpdatedAt = now
		s.orders[order.ID] = order
		s.bumpLineRevisionLocked(order.LineID)
		s.auditLocked(claims.Subject, "order.reject", order.ID, reason)
		result.Orders = append(result.Orders, order)
	}
	return result, nil
}

func (s *MemoryStore) ResubmitOrder(req resubmitOrderRequest, claims auth.Claims) (domain.Order, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := nowUTC()

	order, ok := s.orders[req.OrderID]
	if !ok {
		return domain.Order{}, errors.New("order not found")
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
		currentDate, err := s.currentDateForLineLocked(order.LineID, now)
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
	order.UpdatedAt = now
	s.orders[order.ID] = order
	s.bumpLineRevisionLocked(order.LineID)
	s.auditLocked(claims.Subject, "order.resubmit", order.ID, "")
	return order, nil
}

func (s *MemoryStore) ListOrders(claims auth.Claims) []domain.Order {
	s.mu.Lock()
	defer s.mu.Unlock()

	orders := make([]domain.Order, 0, len(s.orders))
	for _, order := range s.orders {
		if claims.Role == domain.RoleSales && order.CreatedBy != claims.Subject {
			continue
		}
		if claims.Role == domain.RoleScheduler && order.LineID != claims.LineID {
			continue
		}
		if claims.Role == domain.RoleScheduler && order.Status == domain.StatusRejected {
			continue
		}
		orders = append(orders, order)
	}
	sort.Slice(orders, func(i, j int) bool {
		return orders[i].ID < orders[j].ID
	})
	return orders
}

func (s *MemoryStore) DeleteOrders(req deleteOrdersRequest, claims auth.Claims) (deleteOrdersResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(req.OrderIDs) == 0 {
		return deleteOrdersResponse{}, errors.New("orderIds is required")
	}
	result := deleteOrdersResponse{}
	for _, id := range req.OrderIDs {
		order, ok := s.orders[id]
		if !ok {
			result.SkippedOrderIDs = append(result.SkippedOrderIDs, id)
			continue
		}
		if claims.Role == domain.RoleSales && order.CreatedBy != claims.Subject {
			return deleteOrdersResponse{}, errors.New("sales can delete only their own orders")
		}
		if claims.Role == domain.RoleScheduler && order.LineID != claims.LineID {
			return deleteOrdersResponse{}, errors.New("cannot delete another production line")
		}
		if claims.Role != domain.RoleAdmin && claims.Role != domain.RoleSales && claims.Role != domain.RoleScheduler {
			return deleteOrdersResponse{}, errors.New("role cannot delete orders")
		}
		if order.Status == domain.StatusInProgress || order.Status == domain.StatusCompleted {
			return deleteOrdersResponse{}, errors.New("cannot delete in-progress or completed orders")
		}
		delete(s.orders, id)
		s.removeAllocationsLocked(id)
		s.bumpLineRevisionLocked(order.LineID)
		s.auditLocked(claims.Subject, "order.delete", id, "")
		result.DeletedOrderIDs = append(result.DeletedOrderIDs, id)
	}
	return result, nil
}

func (s *MemoryStore) ListUsers() []domain.User {
	s.mu.Lock()
	defer s.mu.Unlock()

	users := make([]domain.User, 0, len(s.users))
	for _, user := range s.users {
		users = append(users, user)
	}
	sort.Slice(users, func(i, j int) bool {
		return users[i].Username < users[j].Username
	})
	return users
}

func (s *MemoryStore) ListLines() []domain.ProductionLine {
	s.mu.Lock()
	defer s.mu.Unlock()

	lines := make([]domain.ProductionLine, 0, len(s.lines))
	for _, line := range s.lines {
		lines = append(lines, line)
	}
	sort.Slice(lines, func(i, j int) bool {
		return lines[i].ID < lines[j].ID
	})
	return lines
}

func (s *MemoryStore) AssignUser(req assignUserRequest, actorID string) (domain.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	user, ok := s.users[req.Username]
	if !ok {
		return domain.User{}, errors.New("user not found")
	}
	if req.Role != domain.RoleAdmin && req.Role != domain.RoleSales && req.Role != domain.RoleScheduler {
		return domain.User{}, errors.New("role must be admin, sales, or scheduler")
	}
	if req.Role == domain.RoleScheduler {
		if _, ok := s.lines[req.LineID]; !ok {
			return domain.User{}, errors.New("scheduler lineId must be A, B, C, or D")
		}
	} else {
		req.LineID = ""
	}
	user.Role = req.Role
	user.LineID = req.LineID
	s.users[user.Username] = user
	s.auditLocked(actorID, "user.assign", user.ID, string(req.Role)+" "+req.LineID)
	return user, nil
}

type scheduleRequest struct {
	LineID              string              `json:"lineId"`
	StartDate           string              `json:"startDate"`
	CurrentDate         string              `json:"currentDate"`
	OrderIDs            []string            `json:"orderIds"`
	ResolutionOrderIDs  []string            `json:"resolutionOrderIds,omitempty"`
	ManualForce         bool                `json:"manualForce"`
	AllowLateCompletion bool                `json:"allowLateCompletion"`
	Reason              string              `json:"reason"`
	PreviewID           string              `json:"previewId"`
	DraftOrder          *createOrderRequest `json:"draftOrder,omitempty"`
}

func (s *MemoryStore) PreviewSchedule(req scheduleRequest, claims auth.Claims) (schedulePreviewResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := nowUTC()
	var err error
	req, err = s.defaultScheduleCurrentDateLocked(req, claims, now)
	if err != nil {
		return schedulePreviewResponse{}, err
	}
	result, err := s.planLocked(req, claims)
	if err != nil {
		return schedulePreviewResponse{}, err
	}
	lineID := scheduleLineID(req, claims)
	id := "PREVIEW-" + strconv.Itoa(s.nextPreviewID)
	s.nextPreviewID++
	normalized := normalizedPreviewRequest(req)
	s.previews[id] = previewRecord{
		ActorID:      claims.Subject,
		ActorRole:    claims.Role,
		LineID:       lineID,
		LineRevision: s.lines[lineID].ScheduleRevision,
		Request:      normalized,
		RequestHash:  requestHash(normalized),
		DraftOrder:   req.DraftOrder,
		Allocations:  append([]scheduler.Allocation(nil), result.Allocations...),
		Conflicts:    append([]scheduler.Conflict(nil), result.Conflicts...),
		CreatedAt:    now,
	}
	return schedulePreviewResponse{
		PreviewID:   id,
		CurrentDate: req.CurrentDate,
		Allocations: result.Allocations,
		Conflicts:   result.Conflicts,
		FinishDate:  result.FinishDate,
		DraftOrder:  req.DraftOrder,
	}, nil
}

func (s *MemoryStore) CreateScheduleJob(req scheduleRequest, claims auth.Claims) (domain.ScheduleJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := nowUTC()
	var err error
	req, err = s.defaultScheduleCurrentDateLocked(req, claims, now)
	if err != nil {
		return domain.ScheduleJob{}, err
	}

	if req.PreviewID == "" {
		return domain.ScheduleJob{}, errors.New("previewId is required before creating a schedule job")
	}
	preview, ok := s.previews[req.PreviewID]
	if !ok {
		return domain.ScheduleJob{}, errors.New("preview result expired or not found")
	}
	if preview.ActorID != claims.Subject || preview.ActorRole != claims.Role {
		return domain.ScheduleJob{}, errors.New("preview result belongs to another user")
	}
	if !sameScheduleRequest(preview.Request, normalizedPreviewRequest(req)) {
		return domain.ScheduleJob{}, errors.New("schedule request changed after preview")
	}
	line, ok := s.lines[preview.LineID]
	if !ok {
		return domain.ScheduleJob{}, errors.New("production line does not exist")
	}
	if line.ScheduleRevision != preview.LineRevision {
		return domain.ScheduleJob{}, errors.New("排程資料已變更，請重新試排。")
	}

	jobLine := req.LineID
	if jobLine == "" {
		jobLine = claims.LineID
	}
	if claims.LineID != jobLine {
		return domain.ScheduleJob{}, errors.New("cannot schedule another production line")
	}

	id := "JOB-" + strconv.Itoa(s.nextJobID)
	s.nextJobID++
	job := domain.ScheduleJob{
		ID:           id,
		LineID:       jobLine,
		Status:       domain.JobQueued,
		PreviewID:    req.PreviewID,
		RequestHash:  preview.RequestHash,
		LineRevision: preview.LineRevision,
		OrderIDs:     append([]string(nil), req.OrderIDs...),
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	s.jobs[id] = job
	s.jobRequests[id] = req
	s.auditLocked(claims.Subject, "schedule.job.create", id, req.Reason)
	if req.ManualForce {
		s.auditLocked(claims.Subject, "schedule.job.manual_force", id, req.Reason)
	}
	return job, nil
}

func (s *MemoryStore) DeleteQueuedScheduleJob(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	job, ok := s.jobs[id]
	if !ok || job.Status != domain.JobQueued {
		return
	}
	delete(s.jobs, id)
	delete(s.jobRequests, id)
}

func (s *MemoryStore) ExecuteScheduleJob(id string) domain.ScheduleJob {
	s.mu.Lock()
	defer s.mu.Unlock()

	job, ok := s.jobs[id]
	if !ok {
		return domain.ScheduleJob{}
	}
	if job.Status == domain.JobCancelled || job.Status == domain.JobCompleted || job.Status == domain.JobFailed {
		return job
	}
	req, ok := s.jobRequests[id]
	if !ok {
		job.Status = domain.JobFailed
		job.Message = "找不到排程任務內容。"
		job.UpdatedAt = time.Now().UTC()
		s.jobs[id] = job
		return job
	}

	if s.lineLocks[job.LineID] {
		job.Status = domain.JobFailed
		job.Message = "產線正在排程中，請稍後再試。"
		job.UpdatedAt = time.Now().UTC()
		s.jobs[id] = job
		return job
	}
	s.lineLocks[job.LineID] = true
	defer delete(s.lineLocks, job.LineID)

	job.Status = domain.JobRunning
	job.Message = "排程任務執行中。"
	job.StartedAt = time.Now().UTC()
	job.UpdatedAt = job.StartedAt
	s.jobs[id] = job
	if job.Status == domain.JobCancelled {
		return job
	}
	if current := s.lines[job.LineID].ScheduleRevision; current != job.LineRevision {
		job.Status = domain.JobFailed
		job.Message = "排程資料已變更，請重新試排。"
		job.UpdatedAt = time.Now().UTC()
		s.jobs[id] = job
		return job
	}
	claims := s.previewClaimsLocked(job.PreviewID, job.LineID)
	result, err := s.planLocked(req, claims)
	if err != nil {
		job.Status = domain.JobFailed
		job.Message = err.Error()
		job.UpdatedAt = time.Now().UTC()
		s.jobs[id] = job
		return job
	}
	if len(result.Conflicts) > 0 && !canPersistConflicts(req, result.Conflicts) {
		job.Status = domain.JobFailed
		job.Message = "排程結果仍有衝突，請重新檢查後再送出。"
		job.UpdatedAt = time.Now().UTC()
		s.jobs[id] = job
		return job
	}

	job.Status = domain.JobCompleted
	job.Message = "排程任務已完成。"
	job.CompletedAt = time.Now().UTC()
	job.UpdatedAt = job.CompletedAt
	s.jobs[id] = job
	s.persistAllocationsLocked(result.Allocations)
	s.bumpLineRevisionLocked(job.LineID)
	delete(s.previews, req.PreviewID)
	delete(s.jobRequests, id)
	return job
}

func (s *MemoryStore) GetScheduleJob(id string) (domain.ScheduleJob, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[id]
	return job, ok
}

func (s *MemoryStore) previewClaimsLocked(previewID, lineID string) auth.Claims {
	preview, ok := s.previews[previewID]
	if !ok {
		return auth.Claims{Role: domain.RoleScheduler, LineID: lineID}
	}
	return auth.Claims{Subject: preview.ActorID, Role: preview.ActorRole, LineID: lineID}
}

type calendarAllocation struct {
	OrderID  string             `json:"orderId"`
	Customer string             `json:"customer"`
	LineID   string             `json:"lineId"`
	Date     time.Time          `json:"date"`
	Quantity int                `json:"quantity"`
	Priority domain.Priority    `json:"priority"`
	Status   domain.OrderStatus `json:"status"`
	Locked   bool               `json:"locked"`
	DueDate  time.Time          `json:"dueDate"`
}

type calendarResponse struct {
	LineID      string               `json:"lineId"`
	Timezone    string               `json:"timezone"`
	Month       string               `json:"month"`
	Allocations []calendarAllocation `json:"allocations"`
}

func (s *MemoryStore) ScheduleCalendar(lineID, month string, claims auth.Claims) (calendarResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if lineID == "" && claims.Role == domain.RoleScheduler {
		lineID = claims.LineID
	}
	if lineID == "" {
		return calendarResponse{}, errors.New("lineId is required")
	}
	if claims.Role == domain.RoleScheduler && claims.LineID != lineID {
		return calendarResponse{}, errors.New("cannot access another production line")
	}
	line, ok := s.lines[lineID]
	if !ok {
		return calendarResponse{}, errors.New("production line does not exist")
	}
	if month == "" {
		currentDate, err := currentDateInLineTimezone(line, nowUTC())
		if err != nil {
			return calendarResponse{}, err
		}
		month = currentDate.Format("2006-01")
	}
	monthStart, err := time.Parse("2006-01", month)
	if err != nil {
		return calendarResponse{}, errors.New("month must use YYYY-MM")
	}
	calendarStart := monthStart.AddDate(0, 0, -int(monthStart.Weekday()))
	calendarEnd := calendarStart.AddDate(0, 0, 42)

	allocations := []calendarAllocation{}
	for _, allocation := range s.allocations {
		if allocation.LineID != lineID {
			continue
		}
		allocationDate := truncateDate(allocation.Date)
		if allocationDate.Before(calendarStart) || !allocationDate.Before(calendarEnd) {
			continue
		}
		order := s.orders[allocation.OrderID]
		status := allocation.Status
		if status == "" {
			status = order.Status
		}
		allocations = append(allocations, calendarAllocation{
			OrderID:  allocation.OrderID,
			Customer: order.Customer,
			LineID:   allocation.LineID,
			Date:     allocationDate,
			Quantity: allocation.Quantity,
			Priority: allocation.Priority,
			Status:   status,
			Locked:   allocation.Locked,
			DueDate:  order.DueDate,
		})
	}
	sort.Slice(allocations, func(i, j int) bool {
		if !allocations[i].Date.Equal(allocations[j].Date) {
			return allocations[i].Date.Before(allocations[j].Date)
		}
		return allocations[i].OrderID < allocations[j].OrderID
	})

	return calendarResponse{LineID: lineID, Timezone: line.Timezone, Month: month, Allocations: allocations}, nil
}

func (s *MemoryStore) ScheduleHistory(lineID string, claims auth.Claims) ([]domain.AuditEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if claims.Role != domain.RoleAdmin && claims.Role != domain.RoleScheduler {
		return nil, errors.New("only admin or schedulers can read schedule history")
	}
	if claims.Role == domain.RoleScheduler {
		lineID = claims.LineID
	} else if lineID != "" {
		if _, ok := s.lines[lineID]; !ok {
			return nil, errors.New("production line does not exist")
		}
	}

	history := []domain.AuditEntry{}
	for index := len(s.audits) - 1; index >= 0 && len(history) < 12; index-- {
		entry := s.audits[index]
		if !isSchedulerWorkflowAudit(entry.Action) {
			continue
		}
		if lineID != "" && s.auditResourceLineLocked(entry) != lineID {
			continue
		}
		history = append(history, entry)
	}
	return history, nil
}

func isSchedulerWorkflowAudit(action string) bool {
	switch action {
	case "schedule.job.create",
		"schedule.job.manual_force",
		"order.reject",
		"production.start",
		"production.confirm.complete",
		"production.confirm.partial":
		return true
	default:
		return false
	}
}

func (s *MemoryStore) auditResourceLineLocked(entry domain.AuditEntry) string {
	if job, ok := s.jobs[entry.Resource]; ok {
		return job.LineID
	}
	if order, ok := s.orders[entry.Resource]; ok {
		return order.LineID
	}
	return ""
}

type productionConfirmRequest struct {
	OrderID          string `json:"orderId"`
	ProductionDate   string `json:"productionDate"`
	ProducedQuantity int    `json:"producedQuantity"`
}

type productionStartRequest struct {
	OrderID string `json:"orderId"`
}

type productionConfirmResponse struct {
	Order     domain.Order  `json:"order"`
	Remainder *domain.Order `json:"remainder,omitempty"`
}

func (s *Server) handleProductionStart(w http.ResponseWriter, r *http.Request) {
	claims, err := s.claimsFromRequest(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if claims.Role != domain.RoleScheduler {
		writeError(w, http.StatusForbidden, "only schedulers can start production")
		return
	}
	var req productionStartRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	order, err := s.store.StartProduction(req, claims)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, order)
}

func (s *MemoryStore) StartProduction(req productionStartRequest, claims auth.Claims) (domain.Order, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	order, ok := s.orders[req.OrderID]
	if !ok {
		return domain.Order{}, errors.New("order not found")
	}
	if order.LineID != claims.LineID {
		return domain.Order{}, errors.New("cannot start another production line")
	}
	if order.Status != domain.StatusScheduled {
		return domain.Order{}, errors.New("only scheduled orders can start production")
	}
	if !s.hasAllocationLocked(order.ID) {
		return domain.Order{}, errors.New("scheduled order has no allocation")
	}
	order.Status = domain.StatusInProgress
	order.UpdatedAt = time.Now().UTC()
	s.orders[order.ID] = order
	s.lockAllocationsLocked(order.ID)
	s.bumpLineRevisionLocked(order.LineID)
	s.auditLocked(claims.Subject, "production.start", order.ID, "")
	return order, nil
}

func (s *MemoryStore) ConfirmProduction(req productionConfirmRequest, claims auth.Claims) (productionConfirmResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	order, ok := s.orders[req.OrderID]
	if !ok {
		return productionConfirmResponse{}, errors.New("order not found")
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
	allocation, ok := s.productionAllocationLocked(order.ID, productionDate)
	if !ok {
		return productionConfirmResponse{}, errors.New("scheduled allocation not found for productionDate")
	}
	if allocation.Status == domain.StatusCompleted {
		return productionConfirmResponse{}, errors.New("productionDate has already been confirmed")
	}
	if req.ProducedQuantity > allocation.Quantity {
		return productionConfirmResponse{}, errors.New("producedQuantity cannot exceed scheduled allocation quantity")
	}
	result, err := scheduler.ConfirmProduction(order, req.ProducedQuantity, time.Now().UTC())
	if err != nil {
		return productionConfirmResponse{}, err
	}
	if result.Completed {
		order.Status = domain.StatusCompleted
		order.UpdatedAt = time.Now().UTC()
		s.orders[order.ID] = order
		s.completeProductionAllocationLocked(order.ID, productionDate, req.ProducedQuantity)
		s.bumpLineRevisionLocked(order.LineID)
		s.auditLocked(claims.Subject, "production.confirm.complete", order.ID, "")
		return productionConfirmResponse{Order: order}, nil
	}

	originalQuantity := order.Quantity
	order.Quantity = originalQuantity - req.ProducedQuantity
	order.Status = domain.StatusPending
	order.UpdatedAt = time.Now().UTC()
	s.orders[order.ID] = order

	s.replaceOrderAllocationsWithCompletedLocked(order.ID, productionDate, req.ProducedQuantity)
	s.bumpLineRevisionLocked(order.LineID)
	s.auditLocked(claims.Subject, "production.confirm.partial", order.ID, "produced "+strconv.Itoa(req.ProducedQuantity)+" of "+strconv.Itoa(originalQuantity)+", remaining "+strconv.Itoa(order.Quantity)+" returned to pending")
	return productionConfirmResponse{Order: order, Remainder: &order}, nil
}

func (s *MemoryStore) planLocked(req scheduleRequest, claims auth.Claims) (scheduler.Result, error) {
	lineID := req.LineID
	if lineID == "" && claims.Role == domain.RoleScheduler {
		lineID = claims.LineID
	}
	if lineID == "" {
		return scheduler.Result{}, errors.New("lineId is required")
	}
	if req.ManualForce && strings.TrimSpace(req.Reason) == "" {
		return scheduler.Result{}, errors.New("manual force requires a reason")
	}
	if claims.Role == domain.RoleScheduler && claims.LineID != lineID {
		return scheduler.Result{}, errors.New("cannot access another production line")
	}
	line, ok := s.lines[lineID]
	if !ok {
		return scheduler.Result{}, errors.New("production line does not exist")
	}
	currentDate := time.Time{}
	if req.CurrentDate != "" {
		parsed, err := time.Parse(dateLayout, req.CurrentDate)
		if err != nil {
			return scheduler.Result{}, errors.New("currentDate must use YYYY-MM-DD")
		}
		currentDate = parsed
	}
	startDate := currentDate
	if startDate.IsZero() {
		lineCurrentDate, err := currentDateInLineTimezone(line, nowUTC())
		if err != nil {
			return scheduler.Result{}, err
		}
		startDate = lineCurrentDate
	}
	if req.StartDate != "" {
		parsed, err := time.Parse(dateLayout, req.StartDate)
		if err != nil {
			return scheduler.Result{}, errors.New("startDate must use YYYY-MM-DD")
		}
		startDate = parsed
	}

	selected := map[string]bool{}
	for _, id := range req.OrderIDs {
		selected[id] = true
	}
	inputs := []scheduler.OrderInput{}
	if req.DraftOrder != nil {
		if claims.Role != domain.RoleSales {
			return scheduler.Result{}, errors.New("only sales can preview draft orders")
		}
		draft := *req.DraftOrder
		if draft.LineID == "" {
			draft.LineID = lineID
		}
		if draft.LineID != lineID {
			return scheduler.Result{}, errors.New("draft order line must match preview line")
		}
		if draft.Priority == "" {
			draft.Priority = domain.PriorityLow
		}
		dueDate, err := validateOrderRequest(draft, s.lines, effectiveCurrentDate(currentDate))
		if err != nil {
			return scheduler.Result{}, err
		}
		inputs = append(inputs, scheduler.OrderInput{
			ID:       "PREVIEW-DRAFT",
			LineID:   draft.LineID,
			Quantity: draft.Quantity,
			Priority: draft.Priority,
			DueDate:  dueDate,
		})
	}
	if req.DraftOrder == nil {
		for _, order := range s.orders {
			if order.LineID != lineID {
				continue
			}
			if order.Status == domain.StatusPending {
				if len(selected) > 0 && !selected[order.ID] {
					continue
				}
			} else if !slicesContains(req.ResolutionOrderIDs, order.ID) {
				continue
			}
			inputs = append(inputs, scheduler.OrderInput{
				ID:       order.ID,
				LineID:   order.LineID,
				Quantity: order.Quantity,
				Priority: order.Priority,
				DueDate:  order.DueDate,
			})
		}
	}
	resolutionOrderIDs := map[string]bool{}
	for _, orderID := range req.ResolutionOrderIDs {
		if orderID == "" {
			continue
		}
		if req.DraftOrder != nil {
			return scheduler.Result{}, errors.New("draft previews cannot include resolution orders")
		}
		order, ok := s.orders[orderID]
		if !ok {
			return scheduler.Result{}, errors.New("resolution order not found")
		}
		if order.LineID != lineID {
			return scheduler.Result{}, errors.New("resolution order line must match preview line")
		}
		if !s.canMoveScheduledOrderLocked(orderID) {
			return scheduler.Result{}, errors.New("resolution orders must be low-priority scheduled orders without locked or completed allocations")
		}
		resolutionOrderIDs[orderID] = true
	}
	existingAllocations := []scheduler.ExistingAllocation{}
	for _, allocation := range s.allocations {
		if resolutionOrderIDs[allocation.OrderID] {
			continue
		}
		existingAllocations = append(existingAllocations, scheduler.ExistingAllocation{
			OrderID:  allocation.OrderID,
			LineID:   allocation.LineID,
			Date:     allocation.Date,
			Quantity: allocation.Quantity,
			Priority: allocation.Priority,
			Locked:   allocation.Locked,
		})
	}

	return scheduler.Plan(scheduler.Request{
		LineID:              lineID,
		CapacityPerDay:      line.CapacityPerDay,
		StartDate:           startDate,
		CurrentDate:         currentDate,
		Orders:              inputs,
		ExistingAllocations: existingAllocations,
		ManualForce:         req.ManualForce,
		ForceReason:         req.Reason,
		AllowLateCompletion: req.AllowLateCompletion,
	})
}

func canPersistConflicts(req scheduleRequest, conflicts []scheduler.Conflict) bool {
	if !req.ManualForce || strings.TrimSpace(req.Reason) == "" {
		return false
	}
	for _, conflict := range conflicts {
		if conflict.Reason != "existing allocations require manual review or reschedule" {
			return false
		}
	}
	return true
}

func (s *MemoryStore) persistAllocationsLocked(allocations []scheduler.Allocation) {
	replacedOrderIDs := map[string]bool{}
	for _, allocation := range allocations {
		replacedOrderIDs[allocation.OrderID] = true
	}
	s.removeOpenAllocationsForOrdersLocked(replacedOrderIDs)
	for _, allocation := range allocations {
		s.allocations = append(s.allocations, domain.ScheduleAllocation{
			OrderID:  allocation.OrderID,
			LineID:   allocation.LineID,
			Date:     truncateDate(allocation.Date),
			Quantity: allocation.Quantity,
			Priority: allocation.Priority,
			Locked:   allocation.Locked,
			Status:   domain.StatusScheduled,
		})
		order, ok := s.orders[allocation.OrderID]
		if ok && order.Status == domain.StatusPending {
			order.Status = domain.StatusScheduled
			order.UpdatedAt = time.Now().UTC()
			s.orders[order.ID] = order
		}
	}
}

func (s *MemoryStore) ConfirmPreviewOrder(previewID string, claims auth.Claims) (domain.Order, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	preview, ok := s.previews[previewID]
	if !ok {
		return domain.Order{}, errors.New("preview result expired or not found")
	}
	if preview.ActorID != claims.Subject || preview.ActorRole != claims.Role {
		return domain.Order{}, errors.New("preview result belongs to another user")
	}
	if preview.DraftOrder == nil {
		return domain.Order{}, errors.New("preview does not contain a draft order")
	}
	draft := *preview.DraftOrder
	order, err := s.createOrderLocked(draft, claims.Subject)
	if err != nil {
		return domain.Order{}, err
	}
	delete(s.previews, previewID)
	return order, nil
}

func (s *MemoryStore) CreateDemoConflictOrders(req demoConflictRequest, claims auth.Claims) ([]domain.Order, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	lineID := req.LineID
	if lineID == "" && claims.Role == domain.RoleScheduler {
		lineID = claims.LineID
	}
	if claims.Role == domain.RoleScheduler && lineID != claims.LineID {
		return nil, errors.New("cannot create demo orders for another production line")
	}
	if _, ok := s.lines[lineID]; !ok {
		return nil, errors.New("production line does not exist")
	}
	if req.Count == 0 {
		req.Count = 6
	}
	if req.Count < 5 || req.Count > 20 {
		return nil, errors.New("count must be between 5 and 20")
	}
	if req.DueDate == "" {
		currentDate, err := s.currentDateForLineLocked(lineID, nowUTC())
		if err != nil {
			return nil, err
		}
		req.DueDate = currentDate.AddDate(0, 0, 1).Format(dateLayout)
	}

	orders := make([]domain.Order, 0, req.Count)
	for index := 1; index <= req.Count; index++ {
		order, err := s.createOrderLocked(createOrderRequest{
			Customer: "Conflict Demo " + strconv.Itoa(index),
			LineID:   lineID,
			Quantity: 2500,
			Priority: domain.PriorityLow,
			DueDate:  req.DueDate,
		}, claims.Subject)
		if err != nil {
			return nil, err
		}
		orders = append(orders, order)
	}
	return orders, nil
}

func (s *MemoryStore) CreateHPAPeakDemo(claims auth.Claims) (hpaPeakSummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.resetHPAPeakDemoLocked(claims.Subject)
	now := time.Now().UTC()
	for lineIndex := hpaDemoFirstLine; lineIndex <= hpaDemoLastLine; lineIndex++ {
		lineID := hpaDemoLineID(lineIndex)
		s.lines[lineID] = domain.ProductionLine{
			ID:             lineID,
			Name:           "HPA Demo Line " + lineID,
			CapacityPerDay: 10000,
			Timezone:       defaultLineTimezone,
		}
		orderIDs := make([]string, 0, hpaDemoOrdersPerLine)
		for orderIndex := 1; orderIndex <= hpaDemoOrdersPerLine; orderIndex++ {
			id := fmt.Sprintf("HPA-%s-%03d", lineID, orderIndex)
			order := domain.Order{
				ID:        id,
				Customer:  "HPA Demo",
				LineID:    lineID,
				Quantity:  2500,
				Priority:  domain.PriorityLow,
				Status:    domain.StatusPending,
				DueDate:   now.AddDate(0, 0, 7),
				Note:      hpaDemoSource,
				CreatedBy: claims.Subject,
				CreatedAt: now,
				UpdatedAt: now,
			}
			s.orders[id] = order
			orderIDs = append(orderIDs, id)
		}

		jobID := "HPA-JOB-" + lineID
		s.jobs[jobID] = domain.ScheduleJob{
			ID:        jobID,
			LineID:    lineID,
			Status:    domain.JobQueued,
			Message:   "多產線排程尖峰任務已送入背景佇列。",
			Source:    hpaDemoSource,
			OrderIDs:  orderIDs,
			CreatedAt: now,
			UpdatedAt: now,
		}
		s.auditLocked(claims.Subject, "schedule.job.create", jobID, hpaDemoSource)
	}
	return s.hpaPeakSummaryLocked(), nil
}

func (s *MemoryStore) ClearHPAPeakDemo(claims auth.Claims) (hpaPeakSummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.clearHPAPeakDemoLocked(claims.Subject)
	return s.hpaPeakSummaryLocked(), nil
}

func (s *MemoryStore) HPAPeakSummary() hpaPeakSummary {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hpaPeakSummaryLocked()
}

func (s *MemoryStore) HPAPeakJobs() []domain.ScheduleJob {
	s.mu.Lock()
	defer s.mu.Unlock()

	jobs := []domain.ScheduleJob{}
	for _, job := range s.jobs {
		if job.Source == hpaDemoSource || isHPADemoLine(job.LineID) {
			jobs = append(jobs, job)
		}
	}
	sort.Slice(jobs, func(i, j int) bool {
		return jobs[i].ID < jobs[j].ID
	})
	return jobs
}

func (s *MemoryStore) clearHPAPeakDemoLocked(actorID string) {
	for id, job := range s.jobs {
		if job.Source == hpaDemoSource || isHPADemoLine(job.LineID) {
			if job.Status == domain.JobQueued || job.Status == domain.JobRunning {
				job.Status = domain.JobCancelled
				job.Message = "排程尖峰展示已取消。"
				job.UpdatedAt = time.Now().UTC()
				s.jobs[id] = job
				continue
			}
			delete(s.jobs, id)
		}
	}
	for id, order := range s.orders {
		if isHPADemoLine(order.LineID) {
			delete(s.orders, id)
		}
	}
	keptAllocations := s.allocations[:0]
	for _, allocation := range s.allocations {
		if !isHPADemoLine(allocation.LineID) {
			keptAllocations = append(keptAllocations, allocation)
		}
	}
	s.allocations = keptAllocations
	for lineID := range s.lines {
		if isHPADemoLine(lineID) {
			delete(s.lines, lineID)
		}
	}
	keptAudits := s.audits[:0]
	for _, audit := range s.audits {
		if audit.Reason == hpaDemoSource {
			continue
		}
		if job, ok := s.jobs[audit.Resource]; ok && isHPADemoLine(job.LineID) {
			continue
		}
		keptAudits = append(keptAudits, audit)
	}
	s.audits = keptAudits
	if actorID != "" {
		s.auditLocked(actorID, "demo.hpa_peak.clear", hpaDemoSource, hpaDemoSource)
	}
}

func (s *MemoryStore) resetHPAPeakDemoLocked(actorID string) {
	s.clearHPAPeakDemoLocked(actorID)
	for id, job := range s.jobs {
		if job.Source == hpaDemoSource || isHPADemoLine(job.LineID) {
			delete(s.jobs, id)
		}
	}
}

func (s *MemoryStore) hpaPeakSummaryLocked() hpaPeakSummary {
	statuses := map[string]int{
		string(domain.JobQueued):    0,
		string(domain.JobRunning):   0,
		string(domain.JobCompleted): 0,
		string(domain.JobFailed):    0,
		string(domain.JobCancelled): 0,
	}
	lineIDs := map[string]bool{}
	orderCount := 0
	failedMessages := []string{}
	recentJobs := []domain.ScheduleJob{}
	for _, order := range s.orders {
		if isHPADemoLine(order.LineID) {
			orderCount++
			lineIDs[order.LineID] = true
		}
	}
	for _, line := range s.lines {
		if isHPADemoLine(line.ID) {
			lineIDs[line.ID] = true
		}
	}
	for _, job := range s.jobs {
		if job.Source != hpaDemoSource && !isHPADemoLine(job.LineID) {
			continue
		}
		statuses[string(job.Status)]++
		recentJobs = append(recentJobs, job)
		if job.Status == domain.JobFailed && job.Message != "" && len(failedMessages) < 5 {
			failedMessages = append(failedMessages, job.ID+"："+job.Message)
		}
	}
	sort.Slice(recentJobs, func(i, j int) bool {
		return recentJobs[i].ID < recentJobs[j].ID
	})
	if len(recentJobs) > 10 {
		recentJobs = recentJobs[:10]
	}
	return hpaPeakSummary{
		LineCount:      len(lineIDs),
		OrderCount:     orderCount,
		JobCount:       statuses[string(domain.JobQueued)] + statuses[string(domain.JobRunning)] + statuses[string(domain.JobCompleted)] + statuses[string(domain.JobFailed)] + statuses[string(domain.JobCancelled)],
		Statuses:       statuses,
		Topic:          "woms.schedule.jobs",
		ConsumerGroup:  "woms-scheduler-workers",
		HPAName:        "woms-woms-worker-hpa",
		DeploymentName: "woms-woms-worker",
		Reason:         "幾百條產線同時進行月底排程，Kafka lag 上升時 KEDA 會擴充 scheduler-worker pods。",
		WatchCommand:   "kubectl get hpa,deploy,pod -n woms -w",
		FailedMessages: failedMessages,
		RecentJobs:     recentJobs,
	}
}

func (s *MemoryStore) removeAllocationsLocked(orderID string) {
	kept := s.allocations[:0]
	for _, allocation := range s.allocations {
		if allocation.OrderID != orderID {
			kept = append(kept, allocation)
		}
	}
	s.allocations = kept
}

func (s *MemoryStore) removeOpenAllocationsForOrdersLocked(orderIDs map[string]bool) {
	if len(orderIDs) == 0 {
		return
	}
	kept := s.allocations[:0]
	for _, allocation := range s.allocations {
		if orderIDs[allocation.OrderID] && allocation.Status != domain.StatusCompleted {
			continue
		}
		kept = append(kept, allocation)
	}
	s.allocations = kept
}

func (s *MemoryStore) canMoveScheduledOrderLocked(orderID string) bool {
	order, ok := s.orders[orderID]
	if !ok || order.Status != domain.StatusScheduled || order.Priority != domain.PriorityLow {
		return false
	}
	hasOpenAllocation := false
	for _, allocation := range s.allocations {
		if allocation.OrderID != orderID {
			continue
		}
		if allocation.Locked || allocation.Status == domain.StatusInProgress || allocation.Status == domain.StatusCompleted {
			return false
		}
		hasOpenAllocation = true
	}
	return hasOpenAllocation
}

func slicesContains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func (s *MemoryStore) productionAllocationLocked(orderID string, productionDate time.Time) (domain.ScheduleAllocation, bool) {
	date := truncateDate(productionDate)
	var completed domain.ScheduleAllocation
	for _, allocation := range s.allocations {
		if allocation.OrderID == orderID && truncateDate(allocation.Date).Equal(date) {
			if allocation.Status == domain.StatusCompleted {
				completed = allocation
				continue
			}
			return allocation, true
		}
	}
	if completed.OrderID != "" {
		return completed, true
	}
	return domain.ScheduleAllocation{}, false
}

func (s *MemoryStore) completeProductionAllocationLocked(orderID string, productionDate time.Time, producedQuantity int) {
	date := truncateDate(productionDate)
	for index, allocation := range s.allocations {
		if allocation.OrderID == orderID && truncateDate(allocation.Date).Equal(date) && allocation.Status != domain.StatusCompleted {
			s.allocations[index].Quantity = producedQuantity
			s.allocations[index].Locked = true
			s.allocations[index].Status = domain.StatusCompleted
			return
		}
	}
}

func (s *MemoryStore) replaceOrderAllocationsWithCompletedLocked(orderID string, productionDate time.Time, producedQuantity int) {
	date := truncateDate(productionDate)
	completed := domain.ScheduleAllocation{}
	kept := s.allocations[:0]
	for _, allocation := range s.allocations {
		if allocation.OrderID != orderID {
			kept = append(kept, allocation)
			continue
		}
		if allocation.Status == domain.StatusCompleted {
			kept = append(kept, allocation)
			continue
		}
		if truncateDate(allocation.Date).Equal(date) && completed.OrderID == "" {
			completed = allocation
			completed.Quantity = producedQuantity
			completed.Locked = true
			completed.Status = domain.StatusCompleted
		}
	}
	if completed.OrderID != "" {
		kept = append(kept, completed)
	}
	s.allocations = kept
}

func (s *MemoryStore) hasAllocationLocked(orderID string) bool {
	for _, allocation := range s.allocations {
		if allocation.OrderID == orderID && allocation.Status != domain.StatusCompleted {
			return true
		}
	}
	return false
}

func (s *MemoryStore) lockAllocationsLocked(orderID string) {
	for index, allocation := range s.allocations {
		if allocation.OrderID == orderID && allocation.Status != domain.StatusCompleted {
			s.allocations[index].Locked = true
			s.allocations[index].Status = domain.StatusInProgress
		}
	}
}

func validateOrderRequest(req createOrderRequest, lines map[string]domain.ProductionLine, currentDate time.Time) (time.Time, error) {
	if err := validateOrderFields(req.Customer, req.Quantity, req.Note); err != nil {
		return time.Time{}, err
	}
	if _, ok := lines[req.LineID]; !ok {
		return time.Time{}, errors.New("production line does not exist")
	}
	if req.Priority == "" {
		req.Priority = domain.PriorityLow
	}
	if req.Priority != domain.PriorityLow && req.Priority != domain.PriorityHigh {
		return time.Time{}, errors.New("priority must be low or high")
	}
	return validateFutureDueDate(req.DueDate, currentDate)
}

func validateOrderFields(customer string, quantity int, note string) error {
	if strings.TrimSpace(customer) == "" || quantity < 25 || quantity > 2500 {
		return errors.New("customer is required and quantity must be between 25 and 2500")
	}
	if len([]rune(note)) > 120 {
		return errors.New("note must be 120 characters or fewer")
	}
	return nil
}

func validateFutureDueDate(value string, currentDate time.Time) (time.Time, error) {
	dueDate, err := time.Parse(dateLayout, value)
	if err != nil {
		return time.Time{}, errors.New("dueDate must use YYYY-MM-DD")
	}
	if !dueDate.After(effectiveCurrentDate(currentDate)) {
		return time.Time{}, errors.New(unacceptableDueDateMessage)
	}
	return dueDate, nil
}

func effectiveCurrentDate(currentDate time.Time) time.Time {
	if currentDate.IsZero() {
		return truncateDate(nowUTC())
	}
	return truncateDate(currentDate)
}

func normalizedPreviewRequest(req scheduleRequest) scheduleRequest {
	normalized := req
	normalized.PreviewID = ""
	normalized.DraftOrder = nil
	normalized.OrderIDs = append([]string(nil), req.OrderIDs...)
	normalized.ResolutionOrderIDs = append([]string(nil), req.ResolutionOrderIDs...)
	sort.Strings(normalized.OrderIDs)
	sort.Strings(normalized.ResolutionOrderIDs)
	return normalized
}

func requestHash(req scheduleRequest) string {
	payload, _ := json.Marshal(req)
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func scheduleLineID(req scheduleRequest, claims auth.Claims) string {
	if req.LineID != "" {
		return req.LineID
	}
	if claims.Role == domain.RoleScheduler {
		return claims.LineID
	}
	return ""
}

func hpaDemoLineID(index int) string {
	return fmt.Sprintf("L%03d", index)
}

func isHPADemoLine(lineID string) bool {
	if len(lineID) != 4 || lineID[0] != 'L' {
		return false
	}
	index, err := strconv.Atoi(lineID[1:])
	return err == nil && index >= hpaDemoFirstLine && index <= hpaDemoLastLine
}

func (s *MemoryStore) bumpLineRevisionLocked(lineID string) {
	line, ok := s.lines[lineID]
	if !ok {
		return
	}
	line.ScheduleRevision++
	s.lines[lineID] = line
}

func defaultScheduleCurrentDate(req scheduleRequest, now time.Time) scheduleRequest {
	if req.CurrentDate == "" {
		req.CurrentDate = truncateDate(now).Format(dateLayout)
	}
	return req
}

func (s *MemoryStore) defaultScheduleCurrentDateLocked(req scheduleRequest, claims auth.Claims, now time.Time) (scheduleRequest, error) {
	if req.CurrentDate != "" {
		return req, nil
	}
	lineID := scheduleRequestLineID(req, claims)
	if lineID == "" {
		return req, nil
	}
	currentDate, err := s.currentDateForLineLocked(lineID, now)
	if err != nil {
		return scheduleRequest{}, err
	}
	req.CurrentDate = currentDate.Format(dateLayout)
	return req, nil
}

func scheduleRequestLineID(req scheduleRequest, claims auth.Claims) string {
	if req.LineID != "" {
		return req.LineID
	}
	if claims.Role == domain.RoleScheduler && claims.LineID != "" {
		return claims.LineID
	}
	if req.DraftOrder != nil {
		return req.DraftOrder.LineID
	}
	return ""
}

func (s *MemoryStore) currentDateForLineLocked(lineID string, now time.Time) (time.Time, error) {
	line, ok := s.lines[lineID]
	if !ok {
		return time.Time{}, errors.New("production line does not exist")
	}
	return currentDateInLineTimezone(line, now)
}

func currentDateInLineTimezone(line domain.ProductionLine, now time.Time) (time.Time, error) {
	timezone := strings.TrimSpace(line.Timezone)
	if timezone == "" {
		timezone = defaultLineTimezone
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return time.Time{}, errors.New("production line timezone is invalid")
	}
	year, month, day := now.In(location).Date()
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC), nil
}

func sameScheduleRequest(a, b scheduleRequest) bool {
	if a.LineID != b.LineID || a.StartDate != b.StartDate || a.CurrentDate != b.CurrentDate || a.ManualForce != b.ManualForce || a.AllowLateCompletion != b.AllowLateCompletion || a.Reason != b.Reason {
		return false
	}
	if len(a.OrderIDs) != len(b.OrderIDs) {
		return false
	}
	for index := range a.OrderIDs {
		if a.OrderIDs[index] != b.OrderIDs[index] {
			return false
		}
	}
	if len(a.ResolutionOrderIDs) != len(b.ResolutionOrderIDs) {
		return false
	}
	for index := range a.ResolutionOrderIDs {
		if a.ResolutionOrderIDs[index] != b.ResolutionOrderIDs[index] {
			return false
		}
	}
	return true
}

func truncateDate(value time.Time) time.Time {
	year, month, day := value.Date()
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}

func (s *MemoryStore) auditLocked(actorID, action, resource, reason string) {
	id := "AUD-" + strconv.Itoa(s.nextAuditID)
	s.nextAuditID++
	s.audits = append(s.audits, domain.AuditEntry{
		ID:        id,
		ActorID:   actorID,
		Action:    action,
		Resource:  resource,
		Reason:    reason,
		CreatedAt: time.Now().UTC(),
	})
}

func readJSON(r *http.Request, target any) error {
	defer r.Body.Close()
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

func orderIDFromSequence(seq int) string {
	return fmt.Sprintf("ORD-%0*d", orderIDDigits, seq)
}

func orderIDFromTime(now time.Time) string {
	return fmt.Sprintf("ORD-%0*d", orderIDDigits, now.UnixNano()%orderIDModulo)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": zhUserMessage(message)})
}

func zhUserMessage(message string) string {
	message = strings.TrimSpace(message)
	if message == "" {
		return "操作失敗，請稍後再試。"
	}
	if containsCJK(message) {
		return message
	}
	if strings.HasPrefix(message, "json: unknown field ") {
		return "請求包含不支援的欄位。"
	}
	if strings.HasPrefix(message, "order not found: ") {
		return "找不到訂單：" + strings.TrimPrefix(message, "order not found: ")
	}
	translations := map[string]string{
		"route not found":                                              "找不到 API 路由。",
		"method not allowed":                                           "不支援此 HTTP 方法。",
		"invalid credentials":                                          "帳號或密碼錯誤。",
		"unauthorized":                                                 "請先登入後再操作。",
		"only sales can create orders":                                 "只有業務可以建立訂單。",
		"only sales can confirm preview orders":                        "只有業務可以確認訂單預覽。",
		"only schedulers can reject orders":                            "只有排程工程師可以駁回訂單。",
		"only sales can resubmit rejected orders":                      "只有業務可以重新送出被駁回的訂單。",
		"only admin can manage accounts":                               "只有管理員可以管理帳號。",
		"only admin or schedulers can create demo conflict orders":     "只有管理員或排程工程師可以建立衝突展示訂單。",
		"only schedulers can create schedule jobs":                     "只有排程工程師可以建立排程任務。",
		"schedule job not found":                                       "找不到排程任務。",
		"only schedulers can confirm production":                       "只有排程工程師可以回報生產。",
		"only schedulers can start production":                         "只有排程工程師可以開始生產。",
		"order not found":                                              "找不到訂單。",
		"cannot update another production line":                        "不能更新其他產線的訂單。",
		"sales can update only their own orders":                       "業務只能更新自己的訂單。",
		"role cannot update orders":                                    "此角色不能更新訂單。",
		"only pending or rejected orders can change order details":     "只有待排程或需業務處理的訂單可以變更內容。",
		"note cannot be updated after order creation":                  "備註建立後不能修改。",
		"dueDate must use YYYY-MM-DD":                                  "交期格式必須是 YYYY-MM-DD。",
		"quantity must be between 25 and 2500":                         "數量必須介於 25 到 2500。",
		"production line does not exist":                               "產線不存在。",
		"priority must be low or high":                                 "優先級必須是 low 或 high。",
		"orderIds is required":                                         "請至少選取一張訂單。",
		"rejection reason is required":                                 "請填寫駁回理由。",
		"rejection reason must be 240 characters or fewer":             "駁回理由最多 240 個字。",
		"cannot reject another production line":                        "不能駁回其他產線的訂單。",
		"only pending orders can be rejected":                          "只有待排程訂單可以被駁回。",
		"sales can resubmit only their own orders":                     "只能重新送出自己的訂單。",
		"only rejected orders can be resubmitted":                      "只有需業務處理的訂單可以重新送出。",
		"sales can delete only their own orders":                       "業務只能刪除自己的訂單。",
		"cannot delete another production line":                        "不能刪除其他產線的訂單。",
		"role cannot delete orders":                                    "此角色不能刪除訂單。",
		"cannot delete in-progress or completed orders":                "不能刪除生產中或已完成的訂單。",
		"user not found":                                               "找不到使用者。",
		"role must be admin, sales, or scheduler":                      "角色必須是 admin、sales 或 scheduler。",
		"scheduler lineId must be A, B, C, or D":                       "排程工程師的產線必須存在。",
		"previewId is required before creating a schedule job":         "建立排程任務前必須先完成試排。",
		"preview result expired or not found":                          "試排結果已過期或不存在。",
		"preview result belongs to another user":                       "試排結果屬於其他使用者。",
		"schedule request changed after preview":                       "排程請求與試排內容不同，請重新試排。",
		"cannot schedule another production line":                      "不能排程其他產線。",
		"lineId is required":                                           "請選擇產線。",
		"cannot access another production line":                        "不能存取其他產線。",
		"month must use YYYY-MM":                                       "月份格式必須是 YYYY-MM。",
		"only admin or schedulers can read schedule history":           "只有管理員或排程工程師可以讀取排程紀錄。",
		"only scheduled orders can start production":                   "只有已排程訂單可以開始生產。",
		"scheduled order has no allocation":                            "已排程訂單沒有分配紀錄。",
		"cannot start another production line":                         "不能開始其他產線的生產。",
		"cannot confirm another production line":                       "不能回報其他產線的生產。",
		"only in-progress orders can be confirmed":                     "只有生產中訂單可以回報生產。",
		"producedQuantity must be greater than zero":                   "完成片數必須大於 0。",
		"productionDate must use YYYY-MM-DD":                           "生產日期格式必須是 YYYY-MM-DD。",
		"scheduled allocation not found for productionDate":            "找不到該生產日期的排程。",
		"productionDate has already been confirmed":                    "該生產日期已經回報過。",
		"producedQuantity cannot exceed scheduled allocation quantity": "完成片數不能超過本日排程量。",
		"manual force requires a reason":                               "人工介入必須填寫原因。",
		"startDate must use YYYY-MM-DD":                                "開始日期格式必須是 YYYY-MM-DD。",
		"currentDate must use YYYY-MM-DD":                              "目前日期格式必須是 YYYY-MM-DD。",
		"only sales can preview draft orders":                          "只有業務可以試排草稿訂單。",
		"draft order line must match preview line":                     "草稿訂單產線必須符合試排產線。",
		"draft previews cannot include resolution orders":              "草稿試排不能包含解法訂單。",
		"resolution order not found":                                   "找不到解法訂單。",
		"resolution order line must match preview line":                "解法訂單產線必須符合試排產線。",
		"resolution orders must be low-priority scheduled orders without locked or completed allocations": "解法訂單必須是低優先級、已排程、且沒有鎖定或已完成分配的訂單。",
		"preview does not contain a draft order":                                                          "試排結果不包含草稿訂單。",
		"cannot create demo orders for another production line":                                           "不能為其他產線建立展示訂單。",
		"count must be between 5 and 20":                                                                  "數量必須介於 5 到 20。",
		"customer is required and quantity must be between 25 and 2500":                                   "請填寫客戶，且數量必須介於 25 到 2500。",
		"note must be 120 characters or fewer":                                                            "備註最多 120 個字。",
		"schedule conflicts require review":                                                               "排程結果仍有衝突，請重新檢查後再送出。",
		"invalid schedule request":                                                                        "排程請求無效。",
	}
	if translated, ok := translations[message]; ok {
		return translated
	}
	return "操作失敗，請稍後再試。"
}

func containsCJK(value string) bool {
	for _, r := range value {
		if (r >= 0x4E00 && r <= 0x9FFF) || (r >= 0x3400 && r <= 0x4DBF) {
			return true
		}
	}
	return false
}

func setSecurityHeaders(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
	w.Header().Set("Content-Security-Policy", "default-src 'self'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
}
