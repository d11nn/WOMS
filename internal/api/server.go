package api

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
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
const hpaDemoJobsPerLine = 2
const unacceptableDueDateMessage = "無法被接受的交期"
const defaultLineTimezone = "Asia/Taipei"
const orderIDDigits = 7
const orderIDModulo int64 = 10000000
const notFoundMsg = "user not found"
const removeUser = "user.disable"
const createJob = "schedule.job.create"
const errRouteNotFound = "route not found"
const errMethodNotAllowed = "method not allowed"
const errUnauthorized = "unauthorized"
const errAuthSessionUnavailable = "auth session store unavailable"
const errAdminManageAccounts = "only admin can manage accounts"
const errOrderNotFound = "order not found"
const errOrderNotFoundPrefix = "order not found: "
const errProductionLineNotFound = "production line does not exist"
const errCannotAccessAnotherLine = "cannot access another production line"
const errLineIDRequired = "lineId is required"
const errOrderIDsRequired = "orderIds is required"
const errQuantityRange = "quantity must be between 25 and 2500"
const errNoteImmutable = "note cannot be updated after order creation"
const errPreviewExpired = "preview result expired or not found"
const errPreviewOtherUser = "preview result belongs to another user"
const errRoleInvalid = "role must be admin, sales, or scheduler"
const errSchedulerLineInvalid = "scheduler lineId must be A, B, C, or D"

var nowUTC = func() time.Time {
	return time.Now().UTC()
}

var hpaAutoscalingCache = struct {
	sync.Mutex
	key     string
	expires time.Time
	state   *hpaAutoscalingState
}{}

type Server struct {
	jwtSecret         string
	store             Store
	publisher         ScheduleJobPublisher
	tokenSessions     TokenSessionStore
	corsAllowedOrigin string
	authMode          string
}

type ServerConfig struct {
	TokenSessions     TokenSessionStore
	CORSAllowedOrigin string
	AuthMode          string
}

type claimsContextKey struct{}

type serverRoute struct {
	method  string
	path    string
	prefix  string
	handler http.HandlerFunc
}

func NewServer(jwtSecret string, store *MemoryStore) *Server {
	return NewServerWithPublisher(jwtSecret, store, NoopScheduleJobPublisher{})
}

func NewServerWithPublisher(jwtSecret string, store Store, publisher ScheduleJobPublisher) *Server {
	return NewServerWithPublisherAndConfig(jwtSecret, store, publisher, ServerConfig{})
}

func NewServerWithPublisherAndConfig(jwtSecret string, store Store, publisher ScheduleJobPublisher, config ServerConfig) *Server {
	if store == nil {
		store = NewMemoryStore()
	}
	if publisher == nil {
		publisher = NoopScheduleJobPublisher{}
	}
	if config.TokenSessions == nil {
		config.TokenSessions = NoopTokenSessionStore{}
	}
	corsAllowedOrigin := strings.TrimSpace(config.CORSAllowedOrigin)
	if corsAllowedOrigin == "" {
		corsAllowedOrigin = "*"
	}
	authMode := strings.ToLower(strings.TrimSpace(config.AuthMode))
	if authMode == "" {
		authMode = "local"
	}
	metrics.Register()
	return &Server{
		jwtSecret:         jwtSecret,
		store:             store,
		publisher:         publisher,
		tokenSessions:     config.TokenSessions,
		corsAllowedOrigin: corsAllowedOrigin,
		authMode:          authMode,
	}
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
	CancelOrders(req cancelOrdersRequest, claims auth.Claims) (cancelOrdersResponse, error)
	UpdateOrderDueDate(id string, req updateOrderRequest, claims auth.Claims) (domain.Order, error)
	ConfirmPreviewOrder(req confirmPreviewRequest, claims auth.Claims) (domain.Order, error)
	RejectOrders(req rejectOrdersRequest, claims auth.Claims) (rejectOrdersResponse, error)
	ResubmitOrder(req resubmitOrderRequest, claims auth.Claims) (domain.Order, error)
	ListUsers() []domain.User
	CreateUser(req createUserRequest, actorID string) (domain.User, error)
	AssignUser(req assignUserRequest, actorID string) (domain.User, error)
	ResetUserPassword(req resetUserPasswordRequest, actorID string) (domain.User, error)
	DeleteUser(username, actorID string) (domain.User, error)
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
	setSecurityHeaders(w, s.corsAllowedOrigin)
	start := time.Now()
	rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
	defer func() {
		statusStr := strconv.Itoa(rec.status)
		metrics.HTTPRequestsTotal.WithLabelValues(
			r.Method,
			r.URL.Path,
			statusStr,
		).Inc()
		metrics.HTTPRequestDuration.WithLabelValues(
			r.Method,
			r.URL.Path,
			statusStr,
		).Observe(time.Since(start).Seconds())
	}()

	if r.Method == http.MethodOptions {
		rec.WriteHeader(http.StatusNoContent)
		return
	}

	if r.Method == http.MethodGet && r.URL.Path == "/metrics" {
		metrics.Handler().ServeHTTP(rec, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/") && !isPublicAPIPath(r) {
		claims, err := s.claimsFromRequest(r)
		if err != nil {
			writeClaimsError(rec, err)
			return
		}
		r = r.WithContext(context.WithValue(r.Context(), claimsContextKey{}, claims))
	}

	if handler := s.routeHandler(r); handler != nil {
		handler(rec, r)
		return
	}
	writeError(rec, http.StatusNotFound, errRouteNotFound)
}

func (s *Server) routeHandler(r *http.Request) http.HandlerFunc {
	for _, route := range s.routes() {
		if route.matches(r) {
			return route.handler
		}
	}
	return nil
}

func (s *Server) routes() []serverRoute {
	return []serverRoute{
		{method: http.MethodGet, path: "/healthz", handler: healthzHandler},
		{method: http.MethodGet, path: "/readyz", handler: readyzHandler},
		{method: http.MethodPost, path: "/api/auth/login", handler: s.handleLogin},
		{method: http.MethodPost, path: "/api/auth/logout", handler: s.handleLogout},
		{method: http.MethodGet, path: "/internal/auth/verify", handler: s.handleIngressAuth},
		{path: "/api/orders", handler: s.handleOrders},
		{method: http.MethodGet, path: "/api/lines", handler: s.handleLines},
		{method: http.MethodPost, path: "/api/orders/preview-confirm", handler: s.handleConfirmPreviewOrder},
		{method: http.MethodPost, path: "/api/orders/reject", handler: s.handleRejectOrders},
		{method: http.MethodPost, path: "/api/orders/resubmit", handler: s.handleResubmitOrder},
		{method: http.MethodPatch, prefix: "/api/orders/", handler: s.handleUpdateOrder},
		{method: http.MethodPatch, path: "/api/users/password", handler: s.handleResetUserPassword},
		{prefix: "/api/users/", handler: s.handleUserByUsername},
		{path: "/api/users", handler: s.handleUsers},
		{path: "/api/demo/conflict-orders", handler: s.handleDemoConflictOrders},
		{path: "/api/demo/hpa-peak", handler: s.handleHPAPeakDemo},
		{method: http.MethodPost, path: "/api/schedules/preview", handler: s.handleSchedulePreview},
		{method: http.MethodGet, path: "/api/schedules/calendar", handler: s.handleScheduleCalendar},
		{method: http.MethodGet, path: "/api/schedules/history", handler: s.handleScheduleHistory},
		{path: "/api/schedules/jobs", handler: s.handleScheduleJobs},
		{method: http.MethodGet, prefix: "/api/schedules/jobs/", handler: s.handleGetScheduleJob},
		{method: http.MethodPost, path: "/api/production/confirm", handler: s.handleProductionConfirm},
		{method: http.MethodPost, path: "/api/production/start", handler: s.handleProductionStart},
	}
}

func (r serverRoute) matches(req *http.Request) bool {
	if r.method != "" && r.method != req.Method {
		return false
	}
	if r.path != "" {
		return r.path == req.URL.Path
	}
	return strings.HasPrefix(req.URL.Path, r.prefix)
}

func healthzHandler(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func readyzHandler(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
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
	claims := auth.Claims{
		Subject: user.ID,
		Role:    user.Role,
		LineID:  user.LineID,
	}
	token, err := auth.CreateToken(s.jwtSecret, claims, 8*time.Hour)
	if err != nil {
		log.Printf("create auth token failed user=%s error=%v", user.Username, err)
		writeError(w, http.StatusInternalServerError, "登入服務暫時不可用。")
		return
	}
	claims, err = auth.VerifyToken(s.jwtSecret, token)
	if err != nil {
		log.Printf("verify generated auth token failed user=%s error=%v", user.Username, err)
		writeError(w, http.StatusInternalServerError, "登入服務暫時不可用。")
		return
	}
	if err := s.tokenSessions.Save(r.Context(), token, claims); err != nil {
		writeError(w, http.StatusServiceUnavailable, errAuthSessionUnavailable)
		return
	}
	metrics.CurrentOnlineUserCount.Inc()
	writeJSON(w, http.StatusOK, map[string]any{
		"token": token,
		"user":  user,
	})
}

func (s *Server) handleIngressAuth(w http.ResponseWriter, r *http.Request) {
	claims, err := s.claimsFromRequest(r)
	if err != nil {
		writeClaimsError(w, err)
		return
	}
	w.Header().Set("X-User-ID", claims.Subject)
	w.Header().Set("X-User-Role", string(claims.Role))
	if claims.LineID != "" {
		w.Header().Set("X-User-Line", claims.LineID)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	token, _ := auth.BearerToken(r.Header.Get("Authorization"))
	if token != "" {
		s.tokenSessions.Revoke(r.Context(), token)
	}
	metrics.CurrentOnlineUserCount.Dec()
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleOrders(w http.ResponseWriter, r *http.Request) {
	claims, err := s.claimsFromRequest(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, errUnauthorized)
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
		var req cancelOrdersRequest
		if err := readJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		result, err := s.store.CancelOrders(req, claims)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, result)
	default:
		writeError(w, http.StatusMethodNotAllowed, errMethodNotAllowed)
	}
}

func (s *Server) handleLines(w http.ResponseWriter, r *http.Request) {
	if _, err := s.claimsFromRequest(r); err != nil {
		writeError(w, http.StatusUnauthorized, errUnauthorized)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"lines": s.store.ListLines()})
}

func (s *Server) handleConfirmPreviewOrder(w http.ResponseWriter, r *http.Request) {
	claims, err := s.claimsFromRequest(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, errUnauthorized)
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
	order, err := s.store.ConfirmPreviewOrder(req, claims)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, order)
}

func (s *Server) handleRejectOrders(w http.ResponseWriter, r *http.Request) {
	claims, err := s.claimsFromRequest(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, errUnauthorized)
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
		writeError(w, http.StatusUnauthorized, errUnauthorized)
		return
	}
	if claims.Role != domain.RoleSales {
		writeError(w, http.StatusForbidden, "only sales can resubmit pending or rejected orders")
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
		writeError(w, http.StatusUnauthorized, errUnauthorized)
		return
	}
	if claims.Role != domain.RoleAdmin {
		writeError(w, http.StatusForbidden, errAdminManageAccounts)
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"users": s.store.ListUsers()})
	case http.MethodPost:
		var req createUserRequest
		if err := readJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		user, err := s.store.CreateUser(req, claims.Subject)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, user)
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
		writeError(w, http.StatusMethodNotAllowed, errMethodNotAllowed)
	}
}

func (s *Server) handleResetUserPassword(w http.ResponseWriter, r *http.Request) {
	claims, err := s.claimsFromRequest(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, errUnauthorized)
		return
	}
	if claims.Role != domain.RoleAdmin {
		writeError(w, http.StatusForbidden, errAdminManageAccounts)
		return
	}
	var req resetUserPasswordRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	user, err := s.store.ResetUserPassword(req, claims.Subject)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, user)
}

func (s *Server) handleUserByUsername(w http.ResponseWriter, r *http.Request) {
	claims, err := s.claimsFromRequest(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, errUnauthorized)
		return
	}
	if claims.Role != domain.RoleAdmin {
		writeError(w, http.StatusForbidden, errAdminManageAccounts)
		return
	}
	username := strings.TrimSpace(strings.TrimPrefix(r.URL.Path, "/api/users/"))
	if username == "" || strings.Contains(username, "/") {
		writeError(w, http.StatusNotFound, errRouteNotFound)
		return
	}
	if r.Method != http.MethodDelete {
		writeError(w, http.StatusMethodNotAllowed, errMethodNotAllowed)
		return
	}
	user, err := s.store.DeleteUser(username, claims.Subject)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, user)
}

func (s *Server) handleDemoConflictOrders(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, errMethodNotAllowed)
		return
	}
	claims, err := s.claimsFromRequest(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, errUnauthorized)
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
		writeError(w, http.StatusUnauthorized, errUnauthorized)
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
		writeError(w, http.StatusUnauthorized, errUnauthorized)
		return
	}
	if claims.Role != domain.RoleAdmin {
		writeError(w, http.StatusForbidden, "只有管理員可以查看 web autoscaling demo。")
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, hpaPeakResponse{Summary: s.store.HPAPeakSummary()})
	case http.MethodPost:
		writeJSON(w, http.StatusAccepted, hpaPeakResponse{Summary: s.store.HPAPeakSummary()})
	case http.MethodDelete:
		summary, err := s.store.ClearHPAPeakDemo(claims)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, hpaPeakResponse{Summary: summary})
	default:
		writeError(w, http.StatusMethodNotAllowed, errMethodNotAllowed)
	}
}

func (s *Server) publishHPAPeakJobs(ctx context.Context, jobs []domain.ScheduleJob) error {
	if len(jobs) == 0 {
		return nil
	}
	for _, job := range jobs {
		if err := s.publisher.PublishScheduleJob(ctx, job); err != nil {
			return fmt.Errorf("job %s: %w", job.ID, err)
		}
	}
	return nil
}

func (s *Server) handleScheduleCalendar(w http.ResponseWriter, r *http.Request) {
	claims, err := s.claimsFromRequest(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, errUnauthorized)
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
		writeError(w, http.StatusUnauthorized, errUnauthorized)
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
		writeError(w, http.StatusUnauthorized, errUnauthorized)
		return
	}
	if claims.Role != domain.RoleScheduler {
		writeError(w, http.StatusForbidden, "only schedulers can create schedule jobs")
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, errMethodNotAllowed)
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
		writeError(w, http.StatusUnauthorized, errUnauthorized)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/schedules/jobs/")
	job, ok := s.store.GetScheduleJob(id)
	if !ok {
		writeError(w, http.StatusNotFound, "schedule job not found")
		return
	}
	if claims.Role == domain.RoleScheduler && claims.LineID != job.LineID {
		writeError(w, http.StatusForbidden, errCannotAccessAnotherLine)
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (s *Server) handleProductionConfirm(w http.ResponseWriter, r *http.Request) {
	claims, err := s.claimsFromRequest(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, errUnauthorized)
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
	if claims, ok := r.Context().Value(claimsContextKey{}).(auth.Claims); ok {
		return claims, nil
	}
	if s.authMode != "local" && s.authMode != "edge" {
		return auth.Claims{}, auth.ErrInvalidToken
	}
	token, err := auth.BearerToken(r.Header.Get("Authorization"))
	if err != nil {
		return auth.Claims{}, err
	}
	claims, err := auth.VerifyToken(s.jwtSecret, token)
	if err != nil {
		return auth.Claims{}, err
	}
	if err := s.tokenSessions.Verify(r.Context(), token, claims); err != nil {
		return auth.Claims{}, err
	}
	return claims, nil
}

func writeClaimsError(w http.ResponseWriter, err error) {
	if errors.Is(err, auth.ErrInvalidToken) || errors.Is(err, auth.ErrExpiredToken) || errors.Is(err, ErrTokenSessionNotFound) {
		writeError(w, http.StatusUnauthorized, errUnauthorized)
		return
	}
	writeError(w, http.StatusServiceUnavailable, errAuthSessionUnavailable)
}

func isPublicAPIPath(r *http.Request) bool {
	return r.Method == http.MethodPost && r.URL.Path == "/api/auth/login"
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
	if !ok || user.Disabled || !auth.VerifyPassword(user.PasswordHash, password) {
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

type cancelOrdersRequest struct {
	OrderIDs []string `json:"orderIds"`
}

type cancelOrdersResponse struct {
	CancelledOrderIDs []string `json:"cancelledOrderIds"`
	SkippedOrderIDs   []string `json:"skippedOrderIds,omitempty"`
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

func canSalesResubmitStatus(status domain.OrderStatus) bool {
	return status == domain.StatusPending || status == domain.StatusRejected
}

func canSalesCancelStatus(status domain.OrderStatus) bool {
	return status == domain.StatusPending || status == domain.StatusRejected
}

type assignUserRequest struct {
	Username string      `json:"username"`
	Role     domain.Role `json:"role"`
	LineID   string      `json:"lineId"`
}

type createUserRequest struct {
	Username string      `json:"username"`
	Password string      `json:"password"`
	Role     domain.Role `json:"role"`
	LineID   string      `json:"lineId"`
}

type resetUserPasswordRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type confirmPreviewRequest struct {
	PreviewID        string   `json:"previewId"`
	DeferredOrderIDs []string `json:"deferredOrderIds,omitempty"`
	DeferDraft       bool     `json:"deferDraft,omitempty"`
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
	Autoscaling    *hpaAutoscalingState `json:"autoscaling,omitempty"`
	Topic          string               `json:"topic"`
	ConsumerGroup  string               `json:"consumerGroup"`
	HPAName        string               `json:"hpaName"`
	DeploymentName string               `json:"deploymentName"`
	MetricName     string               `json:"metricName"`
	GrafanaPath    string               `json:"grafanaPath"`
	LoadCommand    string               `json:"loadCommand"`
	Reason         string               `json:"reason"`
	WatchCommand   string               `json:"watchCommand"`
	FailedMessages []string             `json:"failedMessages,omitempty"`
	RecentJobs     []domain.ScheduleJob `json:"recentJobs,omitempty"`
}

type hpaPeakResponse struct {
	Summary hpaPeakSummary `json:"summary"`
}

type hpaAutoscalingState struct {
	CurrentReplicas    int    `json:"currentReplicas"`
	DesiredReplicas    int    `json:"desiredReplicas"`
	MinReplicas        int    `json:"minReplicas"`
	MaxReplicas        int    `json:"maxReplicas"`
	DeploymentReplicas int    `json:"deploymentReplicas"`
	ReadyReplicas      int    `json:"readyReplicas"`
	AvailableReplicas  int    `json:"availableReplicas"`
	PodCount           int    `json:"podCount"`
	ReadyPods          int    `json:"readyPods"`
	Error              string `json:"error,omitempty"`
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

const salesConflictDeferredReason = "Sales 接單衝突處理：改由業務重新確認"

func (s *MemoryStore) CreateOrder(req createOrderRequest, actorID string) (domain.Order, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.createOrderLocked(req, actorID)
}

func (s *Server) handleUpdateOrder(w http.ResponseWriter, r *http.Request) {
	claims, err := s.claimsFromRequest(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, errUnauthorized)
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
		return domain.Order{}, errors.New(errOrderNotFound)
	}
	if strings.TrimSpace(req.Note) != "" {
		return domain.Order{}, errors.New(errNoteImmutable)
	}
	if err := canUpdateOrderDetails(order, claims); err != nil {
		return domain.Order{}, err
	}
	if req.DueDate != "" {
		currentDate, err := s.currentDateForLineLocked(order.LineID, now)
		if err != nil {
			return domain.Order{}, err
		}
		if err := applyOptionalDueDate(&order, req.DueDate, currentDate); err != nil {
			return domain.Order{}, err
		}
	}
	if err := applyOptionalQuantity(&order, req.Quantity); err != nil {
		return domain.Order{}, err
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

func canUpdateOrderDetails(order domain.Order, claims auth.Claims) error {
	if claims.Role == domain.RoleScheduler && order.LineID != claims.LineID {
		return errors.New("cannot update another production line")
	}
	if claims.Role == domain.RoleSales && order.CreatedBy != claims.Subject {
		return errors.New("sales can update only their own orders")
	}
	if claims.Role != domain.RoleAdmin && claims.Role != domain.RoleSales && claims.Role != domain.RoleScheduler {
		return errors.New("role cannot update orders")
	}
	if order.Status != domain.StatusPending && order.Status != domain.StatusRejected {
		return errors.New("only pending or rejected orders can change order details")
	}
	return nil
}

func canRejectOrder(order domain.Order, claims auth.Claims) error {
	if order.LineID != claims.LineID {
		return errors.New("cannot reject another production line")
	}
	if order.Status != domain.StatusPending {
		return errors.New("only pending orders can be rejected")
	}
	return nil
}

func canCancelOrder(order domain.Order, claims auth.Claims) error {
	if claims.Role == domain.RoleSales {
		if order.CreatedBy != claims.Subject {
			return errors.New("sales can cancel only their own orders")
		}
		if !canSalesCancelStatus(order.Status) {
			return errors.New("sales can cancel only pending or rejected orders")
		}
	}
	if claims.Role == domain.RoleScheduler && order.LineID != claims.LineID {
		return errors.New("cannot cancel another production line")
	}
	if claims.Role != domain.RoleAdmin && claims.Role != domain.RoleSales && claims.Role != domain.RoleScheduler {
		return errors.New("role cannot cancel orders")
	}
	if order.Status == domain.StatusInProgress || order.Status == domain.StatusCompleted {
		return errors.New("cannot cancel in-progress or completed orders")
	}
	return nil
}

func applyOptionalQuantity(order *domain.Order, quantity int) error {
	if quantity == 0 {
		return nil
	}
	if quantity < 25 || quantity > 2500 {
		return errors.New(errQuantityRange)
	}
	order.Quantity = quantity
	return nil
}

func applyOptionalDueDate(order *domain.Order, dueDate string, currentDate time.Time) error {
	if dueDate == "" {
		return nil
	}
	parsed, err := validateFutureDueDate(dueDate, currentDate)
	if err != nil {
		return err
	}
	order.DueDate = parsed
	return nil
}

func resetRejectedState(order *domain.Order) {
	order.RejectionReason = ""
	order.RejectedBy = ""
	order.RejectedAt = time.Time{}
}

func (s *MemoryStore) RejectOrders(req rejectOrdersRequest, claims auth.Claims) (rejectOrdersResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(req.OrderIDs) == 0 {
		return rejectOrdersResponse{}, errors.New(errOrderIDsRequired)
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
			return rejectOrdersResponse{}, errors.New(errOrderNotFoundPrefix + id)
		}
		if err := canRejectOrder(order, claims); err != nil {
			return rejectOrdersResponse{}, err
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
		return domain.Order{}, errors.New(errOrderNotFound)
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
		currentDate, err := s.currentDateForLineLocked(order.LineID, now)
		if err != nil {
			return domain.Order{}, err
		}
		if err := applyOptionalDueDate(&order, req.DueDate, currentDate); err != nil {
			return domain.Order{}, err
		}
	}
	order.Status = domain.StatusPending
	resetRejectedState(&order)
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
		orders = append(orders, order)
	}
	sort.Slice(orders, func(i, j int) bool {
		return orders[i].ID < orders[j].ID
	})
	return orders
}

func (s *MemoryStore) CancelOrders(req cancelOrdersRequest, claims auth.Claims) (cancelOrdersResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(req.OrderIDs) == 0 {
		return cancelOrdersResponse{}, errors.New(errOrderIDsRequired)
	}
	result := cancelOrdersResponse{}
	now := nowUTC()
	for _, id := range req.OrderIDs {
		order, ok := s.orders[id]
		if !ok {
			result.SkippedOrderIDs = append(result.SkippedOrderIDs, id)
			continue
		}
		if order.Status == domain.StatusCancelled {
			result.SkippedOrderIDs = append(result.SkippedOrderIDs, id)
			continue
		}
		if err := canCancelOrder(order, claims); err != nil {
			return cancelOrdersResponse{}, err
		}
		order.Status = domain.StatusCancelled
		order.UpdatedAt = now
		s.orders[order.ID] = order
		s.removeAllocationsLocked(id)
		s.bumpLineRevisionLocked(order.LineID)
		s.auditLocked(claims.Subject, "order.cancel", id, "")
		result.CancelledOrderIDs = append(result.CancelledOrderIDs, id)
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
		return domain.User{}, errors.New(notFoundMsg)
	}
	if req.Role != domain.RoleAdmin && req.Role != domain.RoleSales && req.Role != domain.RoleScheduler {
		return domain.User{}, errors.New(errRoleInvalid)
	}
	if req.Role == domain.RoleScheduler {
		if _, ok := s.lines[req.LineID]; !ok {
			return domain.User{}, errors.New(errSchedulerLineInvalid)
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

func (s *MemoryStore) CreateUser(req createUserRequest, actorID string) (domain.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	username := strings.TrimSpace(req.Username)
	if err := validateUsername(username); err != nil {
		return domain.User{}, err
	}
	if _, exists := s.users[username]; exists {
		return domain.User{}, errors.New("username already exists")
	}
	if err := validateUserRole(req.Role, req.LineID, s.lines); err != nil {
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
	s.users[user.Username] = user
	s.auditLocked(actorID, "user.create", user.ID, string(req.Role)+" "+req.LineID)
	return user, nil
}

func (s *MemoryStore) ResetUserPassword(req resetUserPasswordRequest, actorID string) (domain.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	user, ok := s.users[strings.TrimSpace(req.Username)]
	if !ok {
		return domain.User{}, errors.New(notFoundMsg)
	}
	passwordHash, err := auth.HashPassword(req.Password)
	if err != nil {
		return domain.User{}, err
	}
	user.PasswordHash = passwordHash
	s.users[user.Username] = user
	s.auditLocked(actorID, "user.reset_password", user.ID, "")
	return user, nil
}

func (s *MemoryStore) DeleteUser(username, actorID string) (domain.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	username = strings.TrimSpace(username)
	user, ok := s.users[username]
	if !ok {
		return domain.User{}, errors.New(notFoundMsg)
	}
	if actorID == user.ID || s.userHasOrderReferencesLocked(user.ID) || s.userHasAuditReferencesLocked(user.ID) || s.userHasPreviewReferencesLocked(user.ID) {
		return s.disableUserLocked(username, user, actorID), nil
	}
	delete(s.users, username)
	s.auditLocked(actorID, "user.delete", user.ID, "")
	user.Disabled = false
	user.Deleted = true
	return user, nil
}

func (s *MemoryStore) userHasOrderReferencesLocked(userID string) bool {
	for _, order := range s.orders {
		if order.CreatedBy == userID || order.RejectedBy == userID {
			return true
		}
	}
	return false
}

func (s *MemoryStore) userHasAuditReferencesLocked(userID string) bool {
	for _, audit := range s.audits {
		if audit.ActorID == userID {
			return true
		}
	}
	return false
}

func (s *MemoryStore) userHasPreviewReferencesLocked(userID string) bool {
	for _, preview := range s.previews {
		if preview.ActorID == userID {
			return true
		}
	}
	return false
}

func (s *MemoryStore) disableUserLocked(username string, user domain.User, actorID string) domain.User {
	user.Disabled = true
	user.Deleted = false
	s.users[username] = user
	s.auditLocked(actorID, removeUser, user.ID, "")
	return user
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

const previewDraftOrderID = "PREVIEW-DRAFT"

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
	if req.DraftOrder == nil {
		result.Allocations, _ = s.splitAllocationOrderIDsLocked(result.Allocations)
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

	preview, err := s.validatedScheduleJobPreviewLocked(req, claims)
	if err != nil {
		return domain.ScheduleJob{}, err
	}
	id := "JOB-" + strconv.Itoa(s.nextJobID)
	s.nextJobID++
	job := domain.ScheduleJob{
		ID:           id,
		LineID:       scheduleLineID(req, claims),
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
	s.auditLocked(claims.Subject, createJob, id, req.Reason)
	if req.ManualForce {
		s.auditLocked(claims.Subject, "schedule.job.manual_force", id, req.Reason)
	}
	return job, nil
}

func (s *MemoryStore) validatedScheduleJobPreviewLocked(req scheduleRequest, claims auth.Claims) (previewRecord, error) {
	if req.PreviewID == "" {
		return previewRecord{}, errors.New("previewId is required before creating a schedule job")
	}
	preview, ok := s.previews[req.PreviewID]
	if !ok {
		return previewRecord{}, errors.New(errPreviewExpired)
	}
	if preview.ActorID != claims.Subject || preview.ActorRole != claims.Role {
		return previewRecord{}, errors.New(errPreviewOtherUser)
	}
	if !sameScheduleRequest(preview.Request, normalizedPreviewRequest(req)) {
		return previewRecord{}, errors.New("schedule request changed after preview")
	}
	line, ok := s.lines[preview.LineID]
	if !ok {
		return previewRecord{}, errors.New(errProductionLineNotFound)
	}
	if line.ScheduleRevision != preview.LineRevision {
		return previewRecord{}, errors.New("排程資料已變更，請重新試排。")
	}
	if claims.LineID != scheduleLineID(req, claims) {
		return previewRecord{}, errors.New("cannot schedule another production line")
	}
	return preview, nil
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
		return s.failScheduleJobLocked(job, "找不到排程任務內容。")
	}

	if s.lineLocks[job.LineID] {
		return s.failScheduleJobLocked(job, "產線正在排程中，請稍後再試。")
	}
	s.lineLocks[job.LineID] = true
	defer delete(s.lineLocks, job.LineID)

	job = s.runScheduleJobLocked(job)
	if job.Status == domain.JobCancelled {
		return job
	}
	if current := s.lines[job.LineID].ScheduleRevision; current != job.LineRevision {
		return s.failScheduleJobLocked(job, "排程資料已變更，請重新試排。")
	}
	claims := s.previewClaimsLocked(job.PreviewID, job.LineID)
	result, err := s.planLocked(req, claims)
	if err != nil {
		return s.failScheduleJobLocked(job, err.Error())
	}
	if len(result.Conflicts) > 0 && !canPersistConflicts(req, result.Conflicts) {
		return s.failScheduleJobLocked(job, "排程結果仍有衝突，請重新檢查後再送出。")
	}

	return s.completeScheduleJobLocked(job, req, result)
}

func (s *MemoryStore) failScheduleJobLocked(job domain.ScheduleJob, message string) domain.ScheduleJob {
	job.Status = domain.JobFailed
	job.Message = message
	job.UpdatedAt = time.Now().UTC()
	s.jobs[job.ID] = job
	return job
}

func (s *MemoryStore) runScheduleJobLocked(job domain.ScheduleJob) domain.ScheduleJob {
	job.Status = domain.JobRunning
	job.Message = "排程任務執行中。"
	job.StartedAt = time.Now().UTC()
	job.UpdatedAt = job.StartedAt
	s.jobs[job.ID] = job
	return job
}

func (s *MemoryStore) completeScheduleJobLocked(job domain.ScheduleJob, req scheduleRequest, result scheduler.Result) domain.ScheduleJob {
	job.Status = domain.JobCompleted
	job.Message = "排程任務已完成。"
	job.CompletedAt = time.Now().UTC()
	job.UpdatedAt = job.CompletedAt
	s.jobs[job.ID] = job
	s.persistAllocationsLocked(result.Allocations)
	s.bumpLineRevisionLocked(job.LineID)
	delete(s.previews, req.PreviewID)
	delete(s.jobRequests, job.ID)
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
	OrderID            string             `json:"orderId"`
	Customer           string             `json:"customer"`
	LineID             string             `json:"lineId"`
	Date               time.Time          `json:"date"`
	Quantity           int                `json:"quantity"`
	CompletedQuantity  int                `json:"completedQuantity,omitempty"`
	Priority           domain.Priority    `json:"priority"`
	Status             domain.OrderStatus `json:"status"`
	Locked             bool               `json:"locked"`
	DueDate            time.Time          `json:"dueDate"`
	CreatedAtTimestamp int64              `json:"createdAtTimestamp"`
}

type calendarResponse struct {
	LineID             string               `json:"lineId"`
	Timezone           string               `json:"timezone"`
	Month              string               `json:"month"`
	Allocations        []calendarAllocation `json:"allocations"`
	PendingAllocations []calendarAllocation `json:"pendingAllocations,omitempty"`
}

type calendarWindow struct {
	Month string
	Start time.Time
	End   time.Time
}

func (s *MemoryStore) ScheduleCalendar(lineID, month string, claims auth.Claims) (calendarResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	lineID, line, err := s.resolveCalendarLineLocked(lineID, claims)
	if err != nil {
		return calendarResponse{}, err
	}
	window, err := parseCalendarWindow(line, month)
	if err != nil {
		return calendarResponse{}, err
	}
	pendingAllocations, err := s.salesPendingBacklogCalendarAllocationsLocked(line, window, claims)
	if err != nil {
		return calendarResponse{}, err
	}
	return calendarResponse{
		LineID:             lineID,
		Timezone:           line.Timezone,
		Month:              window.Month,
		Allocations:        s.persistedCalendarAllocationsLocked(lineID, window),
		PendingAllocations: pendingAllocations,
	}, nil
}

func (s *MemoryStore) resolveCalendarLineLocked(lineID string, claims auth.Claims) (string, domain.ProductionLine, error) {
	if lineID == "" && claims.Role == domain.RoleScheduler {
		lineID = claims.LineID
	}
	if lineID == "" {
		return "", domain.ProductionLine{}, errors.New(errLineIDRequired)
	}
	if claims.Role == domain.RoleScheduler && claims.LineID != lineID {
		return "", domain.ProductionLine{}, errors.New(errCannotAccessAnotherLine)
	}
	line, ok := s.lines[lineID]
	if !ok {
		return "", domain.ProductionLine{}, errors.New(errProductionLineNotFound)
	}
	return lineID, line, nil
}

func parseCalendarWindow(line domain.ProductionLine, month string) (calendarWindow, error) {
	if month == "" {
		currentDate, err := currentDateInLineTimezone(line, nowUTC())
		if err != nil {
			return calendarWindow{}, err
		}
		month = currentDate.Format("2006-01")
	}
	monthStart, err := time.Parse("2006-01", month)
	if err != nil {
		return calendarWindow{}, errors.New("month must use YYYY-MM")
	}
	start := monthStart.AddDate(0, 0, -int(monthStart.Weekday()))
	return calendarWindow{Month: month, Start: start, End: start.AddDate(0, 0, 42)}, nil
}

func (s *MemoryStore) persistedCalendarAllocationsLocked(lineID string, window calendarWindow) []calendarAllocation {
	allocations := []calendarAllocation{}
	for _, allocation := range s.allocations {
		if allocation.LineID != lineID {
			continue
		}
		allocationDate := truncateDate(allocation.Date)
		if allocationDate.Before(window.Start) || !allocationDate.Before(window.End) {
			continue
		}
		allocations = append(allocations, s.calendarAllocationFromSchedule(allocation, allocationDate))
	}
	sortCalendarAllocations(allocations)
	return allocations
}

func (s *MemoryStore) calendarAllocationFromSchedule(allocation domain.ScheduleAllocation, allocationDate time.Time) calendarAllocation {
	order := s.orders[allocation.OrderID]
	status := allocation.Status
	if status == "" {
		status = order.Status
	}
	completedQuantity := 0
	if status == domain.StatusCompleted {
		completedQuantity = order.Quantity
	}
	return calendarAllocation{
		OrderID:            allocation.OrderID,
		Customer:           order.Customer,
		LineID:             allocation.LineID,
		Date:               allocationDate,
		Quantity:           allocation.Quantity,
		CompletedQuantity:  completedQuantity,
		Priority:           allocation.Priority,
		Status:             status,
		Locked:             allocation.Locked,
		DueDate:            order.DueDate,
		CreatedAtTimestamp: unixMilliseconds(order.CreatedAt),
	}
}

func (s *MemoryStore) salesPendingBacklogCalendarAllocationsLocked(line domain.ProductionLine, window calendarWindow, claims auth.Claims) ([]calendarAllocation, error) {
	if claims.Role != domain.RoleSales {
		return []calendarAllocation{}, nil
	}
	currentDate, err := currentDateInLineTimezone(line, nowUTC())
	if err != nil {
		return nil, err
	}
	return pendingBacklogCalendarAllocations(
		line,
		s.pendingOrderInputsForLineLocked(line.ID),
		s.existingAllocationsForLineLocked(line.ID),
		currentDate,
		window.Start,
		window.End,
	)
}

func (s *MemoryStore) pendingOrderInputsForLineLocked(lineID string) []scheduler.OrderInput {
	inputs := []scheduler.OrderInput{}
	for _, order := range s.orders {
		if order.LineID == lineID && order.Status == domain.StatusPending {
			inputs = append(inputs, orderInputFromOrder(order))
		}
	}
	return inputs
}

func (s *MemoryStore) existingAllocationsForLineLocked(lineID string) []scheduler.ExistingAllocation {
	existing := []scheduler.ExistingAllocation{}
	for _, allocation := range s.allocations {
		if allocation.LineID == lineID {
			existing = append(existing, scheduler.ExistingAllocation{
				OrderID:  allocation.OrderID,
				LineID:   allocation.LineID,
				Date:     allocation.Date,
				Quantity: allocation.Quantity,
				Priority: allocation.Priority,
				Locked:   allocation.Locked,
			})
		}
	}
	return existing
}

func pendingBacklogCalendarAllocations(line domain.ProductionLine, pendingInputs []scheduler.OrderInput, existing []scheduler.ExistingAllocation, currentDate, calendarStart, calendarEnd time.Time) ([]calendarAllocation, error) {
	if len(pendingInputs) == 0 {
		return []calendarAllocation{}, nil
	}
	orderDueDates := map[string]time.Time{}
	orderCreatedAtTimestamps := map[string]int64{}
	for _, input := range pendingInputs {
		orderDueDates[input.ID] = input.DueDate
		orderCreatedAtTimestamps[input.ID] = input.CreatedAtTimestamp
	}
	result, err := scheduler.Plan(scheduler.Request{
		LineID:              line.ID,
		CapacityPerDay:      line.CapacityPerDay,
		StartDate:           truncateDate(currentDate).AddDate(0, 0, 1),
		CurrentDate:         currentDate,
		Orders:              pendingInputs,
		ExistingAllocations: existing,
	})
	if err != nil {
		return nil, err
	}
	allocations := []calendarAllocation{}
	for _, allocation := range result.Allocations {
		allocationDate := truncateDate(allocation.Date)
		if allocationDate.Before(calendarStart) || !allocationDate.Before(calendarEnd) {
			continue
		}
		allocations = append(allocations, calendarAllocation{
			OrderID:            allocation.OrderID,
			Customer:           allocation.Customer,
			LineID:             allocation.LineID,
			Date:               allocationDate,
			Quantity:           allocation.Quantity,
			Priority:           allocation.Priority,
			Status:             domain.StatusPending,
			Locked:             allocation.Locked,
			DueDate:            orderDueDates[allocation.OrderID],
			CreatedAtTimestamp: orderCreatedAtTimestamps[allocation.OrderID],
		})
	}
	sortCalendarAllocations(allocations)
	return allocations, nil
}

func sortCalendarAllocations(allocations []calendarAllocation) {
	sort.Slice(allocations, func(i, j int) bool {
		if !allocations[i].Date.Equal(allocations[j].Date) {
			return allocations[i].Date.Before(allocations[j].Date)
		}
		if allocations[i].Priority != allocations[j].Priority {
			return allocations[i].Priority == domain.PriorityHigh
		}
		if !allocations[i].DueDate.Equal(allocations[j].DueDate) {
			return allocations[i].DueDate.Before(allocations[j].DueDate)
		}
		if allocations[i].CreatedAtTimestamp != allocations[j].CreatedAtTimestamp {
			return allocations[i].CreatedAtTimestamp < allocations[j].CreatedAtTimestamp
		}
		return allocations[i].OrderID < allocations[j].OrderID
	})
}

func unixMilliseconds(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}
	return value.UTC().UnixNano() / int64(time.Millisecond)
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
			return nil, errors.New(errProductionLineNotFound)
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
	case createJob,
		"schedule.job.manual_force",
		"order.reject",
		"order.cancel",
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
		writeError(w, http.StatusUnauthorized, errUnauthorized)
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
	order, err := s.validateProductionStartLocked(req, claims)
	if err != nil {
		return domain.Order{}, err
	}
	order.Status = domain.StatusInProgress
	order.UpdatedAt = time.Now().UTC()
	s.orders[order.ID] = order
	s.lockAllocationsLocked(order.ID)
	s.bumpLineRevisionLocked(order.LineID)
	s.auditLocked(claims.Subject, "production.start", order.ID, "")
	return order, nil
}

func (s *MemoryStore) validateProductionStartLocked(req productionStartRequest, claims auth.Claims) (domain.Order, error) {
	order, ok := s.orders[req.OrderID]
	if !ok {
		return domain.Order{}, errors.New(errOrderNotFound)
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
	return order, nil
}

func (s *MemoryStore) ConfirmProduction(req productionConfirmRequest, claims auth.Claims) (productionConfirmResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	order, productionDate, err := s.validateProductionConfirmLocked(req, claims)
	if err != nil {
		return productionConfirmResponse{}, err
	}
	result, err := scheduler.ConfirmProduction(order, req.ProducedQuantity, time.Now().UTC())
	if err != nil {
		return productionConfirmResponse{}, err
	}
	if result.Completed {
		return s.completeProductionOrderLocked(order, productionDate, claims.Subject), nil
	}
	return s.partialProductionOrderLocked(order, *result.Remainder, req, productionDate, claims.Subject), nil
}

func (s *MemoryStore) validateProductionConfirmLocked(req productionConfirmRequest, claims auth.Claims) (domain.Order, time.Time, error) {
	order, ok := s.orders[req.OrderID]
	if !ok {
		return domain.Order{}, time.Time{}, errors.New(errOrderNotFound)
	}
	if order.LineID != claims.LineID {
		return domain.Order{}, time.Time{}, errors.New("cannot confirm another production line")
	}
	if order.Status != domain.StatusInProgress {
		return domain.Order{}, time.Time{}, errors.New("only in-progress orders can be confirmed")
	}
	if req.ProducedQuantity <= 0 {
		return domain.Order{}, time.Time{}, errors.New("producedQuantity must be greater than zero")
	}
	productionDate, err := time.Parse(dateLayout, req.ProductionDate)
	if err != nil {
		return domain.Order{}, time.Time{}, errors.New("productionDate must use YYYY-MM-DD")
	}
	allocation, ok := s.productionAllocationLocked(order.ID, productionDate)
	if !ok {
		return domain.Order{}, time.Time{}, errors.New("scheduled allocation not found for productionDate")
	}
	if allocation.Status == domain.StatusCompleted {
		return domain.Order{}, time.Time{}, errors.New("productionDate has already been confirmed")
	}
	if req.ProducedQuantity > allocation.Quantity {
		return domain.Order{}, time.Time{}, errors.New("producedQuantity cannot exceed scheduled allocation quantity")
	}
	return order, productionDate, nil
}

func (s *MemoryStore) completeProductionOrderLocked(order domain.Order, productionDate time.Time, actorID string) productionConfirmResponse {
	order.Status = domain.StatusCompleted
	order.UpdatedAt = time.Now().UTC()
	s.orders[order.ID] = order
	s.completeProductionAllocationLocked(order.ID, productionDate)
	s.bumpLineRevisionLocked(order.LineID)
	s.auditLocked(actorID, "production.confirm.complete", order.ID, "")
	return productionConfirmResponse{Order: order}
}

func (s *MemoryStore) partialProductionOrderLocked(order domain.Order, remainder domain.Order, req productionConfirmRequest, productionDate time.Time, actorID string) productionConfirmResponse {
	originalQuantity := order.Quantity
	now := time.Now().UTC()
	remainder.ID = nextRemainderOrderID(order.ID, order.SourceOrder != "", func(id string) bool {
		_, ok := s.orders[id]
		return ok
	})
	remainder.CreatedAt = now
	remainder.UpdatedAt = now
	order.Quantity = req.ProducedQuantity
	order.Status = domain.StatusCompleted
	order.UpdatedAt = now
	s.orders[order.ID] = order
	s.orders[remainder.ID] = remainder

	s.replaceOrderAllocationsWithCompletedLocked(order.ID, productionDate)
	s.bumpLineRevisionLocked(order.LineID)
	s.auditLocked(actorID, "production.confirm.partial", order.ID, productionAuditReason(req.ProducedQuantity, originalQuantity, remainder))
	return productionConfirmResponse{Order: order, Remainder: &remainder}
}

func productionAuditReason(producedQuantity, originalQuantity int, remainder domain.Order) string {
	return "produced " + strconv.Itoa(producedQuantity) +
		" of " + strconv.Itoa(originalQuantity) +
		", remainder " + remainder.ID +
		" quantity " + strconv.Itoa(remainder.Quantity) +
		" returned to pending"
}

func (s *MemoryStore) planLocked(req scheduleRequest, claims auth.Claims) (scheduler.Result, error) {
	lineID, line, err := s.resolveScheduleLineLocked(req, claims)
	if err != nil {
		return scheduler.Result{}, err
	}
	if req.ManualForce && strings.TrimSpace(req.Reason) == "" {
		return scheduler.Result{}, errors.New("manual force requires a reason")
	}
	currentDate, startDate, err := parseScheduleDates(line, req)
	if err != nil {
		return scheduler.Result{}, err
	}
	inputs, err := s.scheduleOrderInputsLocked(lineID, currentDate, req, claims)
	if err != nil {
		return scheduler.Result{}, err
	}
	resolutionOrderIDs, err := s.resolutionOrderIDSetLocked(lineID, req)
	if err != nil {
		return scheduler.Result{}, err
	}
	return runSchedulePlan(scheduler.Request{
		LineID:              lineID,
		CapacityPerDay:      line.CapacityPerDay,
		StartDate:           startDate,
		CurrentDate:         currentDate,
		Orders:              inputs,
		ExistingAllocations: s.existingAllocationInputsLocked(resolutionOrderIDs),
		ManualForce:         req.ManualForce,
		ForceReason:         req.Reason,
		AllowLateCompletion: req.AllowLateCompletion,
	}, req.DraftOrder != nil)
}

func (s *MemoryStore) resolveScheduleLineLocked(req scheduleRequest, claims auth.Claims) (string, domain.ProductionLine, error) {
	lineID := scheduleLineID(req, claims)
	if lineID == "" {
		return "", domain.ProductionLine{}, errors.New(errLineIDRequired)
	}
	if claims.Role == domain.RoleScheduler && claims.LineID != lineID {
		return "", domain.ProductionLine{}, errors.New(errCannotAccessAnotherLine)
	}
	line, ok := s.lines[lineID]
	if !ok {
		return "", domain.ProductionLine{}, errors.New(errProductionLineNotFound)
	}
	return lineID, line, nil
}

func parseScheduleDates(line domain.ProductionLine, req scheduleRequest) (time.Time, time.Time, error) {
	currentDate := time.Time{}
	if req.CurrentDate != "" {
		parsed, err := time.Parse(dateLayout, req.CurrentDate)
		if err != nil {
			return time.Time{}, time.Time{}, errors.New("currentDate must use YYYY-MM-DD")
		}
		currentDate = parsed
	}
	startDate := currentDate
	if startDate.IsZero() {
		lineCurrentDate, err := currentDateInLineTimezone(line, nowUTC())
		if err != nil {
			return time.Time{}, time.Time{}, err
		}
		startDate = lineCurrentDate
	}
	if req.StartDate != "" {
		parsed, err := time.Parse(dateLayout, req.StartDate)
		if err != nil {
			return time.Time{}, time.Time{}, errors.New("startDate must use YYYY-MM-DD")
		}
		startDate = parsed
	}
	return currentDate, startDate, nil
}

func (s *MemoryStore) scheduleOrderInputsLocked(lineID string, currentDate time.Time, req scheduleRequest, claims auth.Claims) ([]scheduler.OrderInput, error) {
	if req.DraftOrder != nil {
		return s.draftOrderInputsLocked(lineID, currentDate, req, claims)
	}
	return s.selectedOrderInputsLocked(lineID, req), nil
}

func (s *MemoryStore) draftOrderInputsLocked(lineID string, currentDate time.Time, req scheduleRequest, claims auth.Claims) ([]scheduler.OrderInput, error) {
	draft, dueDate, err := validateDraftPreviewRequest(lineID, currentDate, req, claims, s.lines)
	if err != nil {
		return nil, err
	}
	inputs := []scheduler.OrderInput{draftOrderInput(draft, dueDate)}
	inputs = append(inputs, s.pendingOrderInputsForLineLocked(lineID)...)
	return inputs, nil
}

func validateDraftPreviewRequest(lineID string, currentDate time.Time, req scheduleRequest, claims auth.Claims, lines map[string]domain.ProductionLine) (createOrderRequest, time.Time, error) {
	if claims.Role != domain.RoleSales {
		return createOrderRequest{}, time.Time{}, errors.New("only sales can preview draft orders")
	}
	draft := *req.DraftOrder
	if draft.LineID == "" {
		draft.LineID = lineID
	}
	if draft.LineID != lineID {
		return createOrderRequest{}, time.Time{}, errors.New("draft order line must match preview line")
	}
	if draft.Priority == "" {
		draft.Priority = domain.PriorityLow
	}
	dueDate, err := validateOrderRequest(draft, lines, effectiveCurrentDate(currentDate))
	if err != nil {
		return createOrderRequest{}, time.Time{}, err
	}
	return draft, dueDate, nil
}

func draftOrderInput(draft createOrderRequest, dueDate time.Time) scheduler.OrderInput {
	return scheduler.OrderInput{
		ID:                 previewDraftOrderID,
		Customer:           strings.TrimSpace(draft.Customer),
		LineID:             draft.LineID,
		Quantity:           draft.Quantity,
		Priority:           draft.Priority,
		Status:             domain.StatusPending,
		DueDate:            dueDate,
		CreatedAtTimestamp: unixMilliseconds(nowUTC()),
	}
}

func (s *MemoryStore) selectedOrderInputsLocked(lineID string, req scheduleRequest) []scheduler.OrderInput {
	selected := map[string]bool{}
	for _, id := range req.OrderIDs {
		selected[id] = true
	}
	inputs := []scheduler.OrderInput{}
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
		inputs = append(inputs, orderInputFromOrder(order))
	}
	return inputs
}

func orderInputFromOrder(order domain.Order) scheduler.OrderInput {
	return scheduler.OrderInput{
		ID:                 order.ID,
		Customer:           order.Customer,
		LineID:             order.LineID,
		Quantity:           order.Quantity,
		Priority:           order.Priority,
		Status:             order.Status,
		DueDate:            order.DueDate,
		CreatedAtTimestamp: unixMilliseconds(order.CreatedAt),
	}
}

func (s *MemoryStore) resolutionOrderIDSetLocked(lineID string, req scheduleRequest) (map[string]bool, error) {
	resolutionOrderIDs := map[string]bool{}
	for _, orderID := range req.ResolutionOrderIDs {
		if orderID == "" {
			continue
		}
		if req.DraftOrder != nil {
			return nil, errors.New("draft previews cannot include resolution orders")
		}
		order, ok := s.orders[orderID]
		if !ok {
			return nil, errors.New("resolution order not found")
		}
		if order.LineID != lineID {
			return nil, errors.New("resolution order line must match preview line")
		}
		if !s.canMoveScheduledOrderLocked(orderID) {
			return nil, errors.New("resolution orders must be low-priority scheduled orders without locked or completed allocations")
		}
		resolutionOrderIDs[orderID] = true
	}
	return resolutionOrderIDs, nil
}

func (s *MemoryStore) existingAllocationInputsLocked(resolutionOrderIDs map[string]bool) []scheduler.ExistingAllocation {
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
	return existingAllocations
}

func runSchedulePlan(planRequest scheduler.Request, hasDraftOrder bool) (scheduler.Result, error) {
	var baseline scheduler.Result
	var err error
	if hasDraftOrder {
		baselineRequest := planRequest
		baselineRequest.Orders = withoutDraftOrderInputs(planRequest.Orders)
		baseline, err = scheduler.Plan(baselineRequest)
		if err != nil {
			return scheduler.Result{}, err
		}
	}
	result, err := scheduler.Plan(planRequest)
	if err != nil {
		return scheduler.Result{}, err
	}
	if hasDraftOrder {
		result = salesDraftPreviewResult(result, baseline)
	}
	return result, nil
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

func withoutDraftOrderInputs(inputs []scheduler.OrderInput) []scheduler.OrderInput {
	orders := make([]scheduler.OrderInput, 0, len(inputs))
	for _, input := range inputs {
		if input.ID == previewDraftOrderID {
			continue
		}
		orders = append(orders, input)
	}
	return orders
}

func salesDraftPreviewResult(result, baseline scheduler.Result) scheduler.Result {
	filtered := scheduler.Result{
		Allocations: result.Allocations,
	}
	baselineConflicts := map[string]bool{}
	for _, conflict := range baseline.Conflicts {
		baselineConflicts[conflict.OrderID+"|"+conflict.Reason] = true
	}
	for _, conflict := range result.Conflicts {
		if conflict.OrderID == previewDraftOrderID || !baselineConflicts[conflict.OrderID+"|"+conflict.Reason] {
			filtered.Conflicts = append(filtered.Conflicts, conflict)
		}
	}
	for _, allocation := range result.Allocations {
		if allocation.OrderID == previewDraftOrderID {
			filtered.FinishDate = allocation.Date
		}
	}
	return filtered
}

func (s *MemoryStore) splitAllocationOrderIDsLocked(allocations []scheduler.Allocation) ([]scheduler.Allocation, map[string]int) {
	seen := map[string]int{}
	reserved := map[string]bool{}
	firstQuantities := map[string]int{}
	normalized := make([]scheduler.Allocation, 0, len(allocations))
	for _, allocation := range allocations {
		seen[allocation.OrderID]++
		if seen[allocation.OrderID] == 1 {
			firstQuantities[allocation.OrderID] = allocation.Quantity
			normalized = append(normalized, allocation)
			continue
		}
		source, ok := s.orders[allocation.OrderID]
		if !ok {
			normalized = append(normalized, allocation)
			continue
		}
		sourceID := allocation.OrderID
		allocation.SourceOrderID = sourceID
		allocation.OrderID = nextRemainderOrderID(sourceID, source.SourceOrder != "", func(id string) bool {
			_, exists := s.orders[id]
			return exists || reserved[id]
		})
		reserved[allocation.OrderID] = true
		normalized = append(normalized, allocation)
	}
	return normalized, firstQuantities
}

func (s *MemoryStore) persistAllocationsLocked(allocations []scheduler.Allocation) {
	allocations, firstQuantities := s.splitAllocationOrderIDsLocked(allocations)
	s.updateSourceOrderFirstQuantitiesLocked(firstQuantities)
	s.createSplitOrdersLocked(allocations)
	s.removeOpenAllocationsForOrdersLocked(replacedAllocationOrderIDs(allocations))
	for _, allocation := range allocations {
		s.appendAllocationAndMarkOrderScheduledLocked(allocation)
	}
}

func (s *MemoryStore) updateSourceOrderFirstQuantitiesLocked(firstQuantities map[string]int) {
	for sourceID, firstQuantity := range firstQuantities {
		order, ok := s.orders[sourceID]
		if ok && order.Quantity != firstQuantity {
			order.Quantity = firstQuantity
			order.UpdatedAt = time.Now().UTC()
			s.orders[sourceID] = order
		}
	}
}

func (s *MemoryStore) createSplitOrdersLocked(allocations []scheduler.Allocation) {
	for _, allocation := range allocations {
		if allocation.SourceOrderID == "" {
			continue
		}
		if _, exists := s.orders[allocation.OrderID]; exists {
			continue
		}
		source := s.orders[allocation.SourceOrderID]
		source.ID = allocation.OrderID
		source.Quantity = allocation.Quantity
		source.Status = domain.StatusPending
		source.SourceOrder = allocation.SourceOrderID
		source.CreatedAt = time.Now().UTC()
		source.UpdatedAt = source.CreatedAt
		s.orders[source.ID] = source
	}
}

func replacedAllocationOrderIDs(allocations []scheduler.Allocation) map[string]bool {
	replacedOrderIDs := map[string]bool{}
	for _, allocation := range allocations {
		replacedOrderIDs[allocation.OrderID] = true
		if allocation.SourceOrderID != "" {
			replacedOrderIDs[allocation.SourceOrderID] = true
		}
	}
	return replacedOrderIDs
}

func (s *MemoryStore) appendAllocationAndMarkOrderScheduledLocked(allocation scheduler.Allocation) {
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

func (s *MemoryStore) ConfirmPreviewOrder(req confirmPreviewRequest, claims auth.Claims) (domain.Order, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	preview, err := s.loadPreviewForConfirmationLocked(req, claims)
	if err != nil {
		return domain.Order{}, err
	}
	if err := validateDraftDeferRequest(req, preview); err != nil {
		return domain.Order{}, err
	}
	deferredOrderIDs, err := s.validateSalesDeferredOrdersLocked(req.DeferredOrderIDs, preview, claims)
	if err != nil {
		return domain.Order{}, err
	}
	draft := *preview.DraftOrder
	order, err := s.createOrderLocked(draft, claims.Subject)
	if err != nil {
		return domain.Order{}, err
	}
	now := nowUTC()
	if req.DeferDraft {
		order = s.applyDeferredDraftLocked(order, claims, now)
	}
	s.deferPreviewConflictOrdersLocked(deferredOrderIDs, claims, now)
	delete(s.previews, req.PreviewID)
	return order, nil
}

func (s *MemoryStore) loadPreviewForConfirmationLocked(req confirmPreviewRequest, claims auth.Claims) (previewRecord, error) {
	preview, ok := s.previews[req.PreviewID]
	if !ok {
		return previewRecord{}, errors.New(errPreviewExpired)
	}
	if preview.ActorID != claims.Subject || preview.ActorRole != claims.Role {
		return previewRecord{}, errors.New(errPreviewOtherUser)
	}
	if preview.DraftOrder == nil {
		return previewRecord{}, errors.New("preview does not contain a draft order")
	}
	return preview, nil
}

func (s *MemoryStore) applyDeferredDraftLocked(order domain.Order, claims auth.Claims, now time.Time) domain.Order {
	order.Status = domain.StatusRejected
	order.RejectedBy = claims.Subject
	order.RejectedAt = now
	order.UpdatedAt = now
	s.orders[order.ID] = order
	s.auditLocked(claims.Subject, "order.sales_conflict_defer_draft", order.ID, "")
	return order
}

func (s *MemoryStore) deferPreviewConflictOrdersLocked(orderIDs []string, claims auth.Claims, now time.Time) {
	for _, orderID := range orderIDs {
		deferred := s.orders[orderID]
		deferred.Status = domain.StatusRejected
		deferred.RejectionReason = salesConflictDeferredReason
		deferred.RejectedBy = claims.Subject
		deferred.RejectedAt = now
		deferred.UpdatedAt = now
		s.orders[orderID] = deferred
		s.bumpLineRevisionLocked(deferred.LineID)
		s.auditLocked(claims.Subject, "order.sales_conflict_defer", orderID, salesConflictDeferredReason)
	}
}

func (s *MemoryStore) validateSalesDeferredOrdersLocked(orderIDs []string, preview previewRecord, claims auth.Claims) ([]string, error) {
	ids := uniqueOrderIDs(orderIDs)
	if len(ids) == 0 {
		return nil, nil
	}
	allowed := previewDeferredOrderIDs(preview)
	for _, orderID := range ids {
		if !allowed[orderID] {
			return nil, errors.New("deferred order must belong to preview conflicts")
		}
		order, ok := s.orders[orderID]
		if !ok {
			return nil, errors.New(errOrderNotFoundPrefix + orderID)
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
	}
	return ids, nil
}

func previewDeferredOrderIDs(preview previewRecord) map[string]bool {
	allowed := map[string]bool{}
	for _, conflict := range preview.Conflicts {
		if conflict.OrderID != "" && conflict.OrderID != previewDraftOrderID {
			allowed[conflict.OrderID] = true
		}
		for _, orderID := range conflict.AffectedOrderIDs {
			if orderID != "" && orderID != previewDraftOrderID {
				allowed[orderID] = true
			}
		}
	}
	return allowed
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
		return nil, errors.New(errProductionLineNotFound)
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
		s.audits[len(s.audits)-1].Action = "order.create_demo_conflict"
		s.audits[len(s.audits)-1].Reason = req.DueDate
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
		s.createHPADemoLineLocked(lineID)
		orderIDs := s.createHPADemoOrdersLocked(lineID, claims.Subject, now)
		s.createHPADemoJobsLocked(lineID, orderIDs, claims.Subject, now)
	}
	return s.hpaPeakSummaryLocked(), nil
}

func (s *MemoryStore) createHPADemoLineLocked(lineID string) {
	s.lines[lineID] = domain.ProductionLine{
		ID:             lineID,
		Name:           "HPA Demo Line " + lineID,
		CapacityPerDay: 10000,
		Timezone:       defaultLineTimezone,
	}
}

func (s *MemoryStore) createHPADemoOrdersLocked(lineID, actorID string, now time.Time) []string {
	orderIDs := make([]string, 0, hpaDemoOrdersPerLine)
	for orderIndex := 1; orderIndex <= hpaDemoOrdersPerLine; orderIndex++ {
		id := fmt.Sprintf("HPA-%s-%03d", lineID, orderIndex)
		s.orders[id] = domain.Order{
			ID:        id,
			Customer:  "HPA Demo",
			LineID:    lineID,
			Quantity:  2500,
			Priority:  domain.PriorityLow,
			Status:    domain.StatusPending,
			DueDate:   now.AddDate(0, 0, 7),
			Note:      hpaDemoSource,
			CreatedBy: actorID,
			CreatedAt: now,
			UpdatedAt: now,
		}
		orderIDs = append(orderIDs, id)
	}
	return orderIDs
}

func (s *MemoryStore) createHPADemoJobsLocked(lineID string, orderIDs []string, actorID string, now time.Time) {
	for jobIndex := 1; jobIndex <= hpaDemoJobsPerLine; jobIndex++ {
		jobID := fmt.Sprintf("HPA-JOB-%s-%03d", lineID, jobIndex)
		s.jobs[jobID] = domain.ScheduleJob{
			ID:        jobID,
			LineID:    lineID,
			Status:    domain.JobQueued,
			Message:   "多產線排程尖峰任務已送入背景佇列。",
			Source:    hpaDemoSource,
			OrderIDs:  []string{orderIDs[jobIndex-1]},
			CreatedAt: now,
			UpdatedAt: now,
		}
		s.auditLocked(actorID, createJob, jobID, hpaDemoSource)
	}
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
	summary := hpaPeakSummaryDefaults()
	statuses := hpaJobStatusDefaults()
	lineIDs := map[string]bool{}
	orderCount := s.collectHPADemoOrdersLocked(lineIDs)
	s.collectHPADemoLinesLocked(lineIDs)
	recentJobs, failedMessages := s.collectHPADemoJobsLocked(statuses)
	summary.LineCount = len(lineIDs)
	summary.OrderCount = orderCount
	summary.JobCount = hpaJobCount(statuses)
	summary.Statuses = statuses
	summary.FailedMessages = failedMessages
	summary.RecentJobs = recentJobs
	return summary
}

func hpaJobStatusDefaults() map[string]int {
	return map[string]int{
		string(domain.JobQueued):    0,
		string(domain.JobRunning):   0,
		string(domain.JobCompleted): 0,
		string(domain.JobFailed):    0,
		string(domain.JobCancelled): 0,
	}
}

func (s *MemoryStore) collectHPADemoOrdersLocked(lineIDs map[string]bool) int {
	orderCount := 0
	for _, order := range s.orders {
		if isHPADemoLine(order.LineID) {
			orderCount++
			lineIDs[order.LineID] = true
		}
	}
	return orderCount
}

func (s *MemoryStore) collectHPADemoLinesLocked(lineIDs map[string]bool) {
	for _, line := range s.lines {
		if isHPADemoLine(line.ID) {
			lineIDs[line.ID] = true
		}
	}
}

func (s *MemoryStore) collectHPADemoJobsLocked(statuses map[string]int) ([]domain.ScheduleJob, []string) {
	failedMessages := []string{}
	recentJobs := []domain.ScheduleJob{}
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
	return limitRecentHPAJobs(recentJobs), failedMessages
}

func limitRecentHPAJobs(jobs []domain.ScheduleJob) []domain.ScheduleJob {
	sort.Slice(jobs, func(i, j int) bool {
		return jobs[i].ID < jobs[j].ID
	})
	if len(jobs) > 10 {
		return jobs[:10]
	}
	return jobs
}

func hpaJobCount(statuses map[string]int) int {
	return statuses[string(domain.JobQueued)] +
		statuses[string(domain.JobRunning)] +
		statuses[string(domain.JobCompleted)] +
		statuses[string(domain.JobFailed)] +
		statuses[string(domain.JobCancelled)]
}

func hpaPeakSummaryDefaults() hpaPeakSummary {
	namespace := envDefault("POD_NAMESPACE", "woms")
	summary := hpaPeakSummary{
		Statuses:       map[string]int{},
		Topic:          envDefault("KAFKA_SCHEDULE_TOPIC", "woms.schedule.jobs"),
		ConsumerGroup:  envDefault("KAFKA_CONSUMER_GROUP", "woms-scheduler-workers"),
		HPAName:        envDefault("HPA_DEMO_HPA_NAME", "woms-woms-web-hpa"),
		DeploymentName: envDefault("HPA_DEMO_DEPLOYMENT_NAME", "woms-woms-web"),
		MetricName:     envDefault("HPA_DEMO_METRIC_NAME", "woms_web_nginx_requests_per_second_per_pod"),
		GrafanaPath:    envDefault("HPA_DEMO_GRAFANA_PATH", "/grafana/d/woms-monitoring/woms-monitoring"),
		LoadCommand:    envDefault("HPA_DEMO_LOAD_COMMAND", `hey -z 5m -c 80 "https://<INGRESS_HOST>/"`),
		Reason:         "NGINX Ingress 或 LoadBalancer 導入多使用者 web 流量時，web pod 的 NGINX exporter 暴露 per-pod req/s；Prometheus、Grafana 與 KEDA 使用同一個指標擴充 web pods。",
		WatchCommand:   fmt.Sprintf("kubectl get hpa,deploy,pod -n %s -l app.kubernetes.io/component=web -w", namespace),
	}
	summary.Autoscaling = loadHPAAutoscalingState(namespace, summary.HPAName, summary.DeploymentName)
	return summary
}

func loadHPAAutoscalingState(namespace, hpaName, deploymentName string) *hpaAutoscalingState {
	host := strings.TrimSpace(os.Getenv("KUBERNETES_SERVICE_HOST"))
	if host == "" {
		return nil
	}
	labelSelector := envDefault("HPA_DEMO_POD_LABEL_SELECTOR", "app.kubernetes.io/component=web")
	cacheKey := hpaAutoscalingCacheKey(host, namespace, hpaName, deploymentName, labelSelector)
	if state := cachedHPAAutoscalingState(cacheKey, time.Now()); state != nil {
		return state
	}
	client, baseURL, token, errState := kubernetesAutoscalingClient(host)
	if errState != nil {
		return errState
	}
	ctx, cancel := context.WithTimeout(context.Background(), 900*time.Millisecond)
	defer cancel()

	state := &hpaAutoscalingState{}
	messages := []string{}
	messages = append(messages, loadKubernetesHPAState(ctx, client, baseURL, token, namespace, hpaName, state)...)
	messages = append(messages, loadKubernetesDeploymentState(ctx, client, baseURL, token, namespace, deploymentName, state)...)
	messages = append(messages, loadKubernetesPodState(ctx, client, baseURL, token, namespace, labelSelector, state)...)
	if len(messages) > 0 {
		state.Error = strings.Join(messages, "；")
	}
	storeCachedHPAAutoscalingState(cacheKey, state)
	return state
}

func hpaAutoscalingCacheKey(host, namespace, hpaName, deploymentName, labelSelector string) string {
	return strings.Join([]string{host, namespace, hpaName, deploymentName, labelSelector}, "\x00")
}

func cachedHPAAutoscalingState(cacheKey string, now time.Time) *hpaAutoscalingState {
	hpaAutoscalingCache.Lock()
	defer hpaAutoscalingCache.Unlock()
	if hpaAutoscalingCache.key == cacheKey && now.Before(hpaAutoscalingCache.expires) {
		return hpaAutoscalingCache.state
	}
	return nil
}

func storeCachedHPAAutoscalingState(cacheKey string, state *hpaAutoscalingState) {
	hpaAutoscalingCache.Lock()
	hpaAutoscalingCache.key = cacheKey
	hpaAutoscalingCache.expires = time.Now().Add(2 * time.Second)
	hpaAutoscalingCache.state = state
	hpaAutoscalingCache.Unlock()
}

func kubernetesAutoscalingClient(host string) (*http.Client, string, string, *hpaAutoscalingState) {
	token, err := os.ReadFile(kubernetesServiceAccountTokenPath)
	if err != nil {
		return nil, "", "", &hpaAutoscalingState{Error: "無法讀取 Kubernetes service account token：" + err.Error()}
	}
	ca, err := os.ReadFile(kubernetesServiceAccountCAPath)
	if err != nil {
		return nil, "", "", &hpaAutoscalingState{Error: "無法讀取 Kubernetes CA：" + err.Error()}
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		return nil, "", "", &hpaAutoscalingState{Error: "無法載入 Kubernetes CA。"}
	}
	client := newKubernetesHTTPClient(host, roots)
	baseURL := "https://" + host + ":" + envDefault("KUBERNETES_SERVICE_PORT", "443")
	return client, baseURL, string(token), nil
}

func loadKubernetesHPAState(ctx context.Context, client *http.Client, baseURL, token, namespace, hpaName string, state *hpaAutoscalingState) []string {
	var hpa struct {
		Spec struct {
			MinReplicas *int `json:"minReplicas"`
			MaxReplicas int  `json:"maxReplicas"`
		} `json:"spec"`
		Status struct {
			CurrentReplicas int `json:"currentReplicas"`
			DesiredReplicas int `json:"desiredReplicas"`
		} `json:"status"`
	}
	if err := kubernetesGetJSON(ctx, client, baseURL, string(token), path.Join("/apis/autoscaling/v2/namespaces", namespace, "horizontalpodautoscalers", hpaName), &hpa); err != nil {
		return []string{"HPA 狀態讀取失敗：" + err.Error()}
	}
	if hpa.Spec.MinReplicas != nil {
		state.MinReplicas = *hpa.Spec.MinReplicas
	}
	state.MaxReplicas = hpa.Spec.MaxReplicas
	state.CurrentReplicas = hpa.Status.CurrentReplicas
	state.DesiredReplicas = hpa.Status.DesiredReplicas
	return nil
}

func loadKubernetesDeploymentState(ctx context.Context, client *http.Client, baseURL, token, namespace, deploymentName string, state *hpaAutoscalingState) []string {
	var deployment struct {
		Status struct {
			Replicas          int `json:"replicas"`
			ReadyReplicas     int `json:"readyReplicas"`
			AvailableReplicas int `json:"availableReplicas"`
		} `json:"status"`
	}
	if err := kubernetesGetJSON(ctx, client, baseURL, string(token), path.Join("/apis/apps/v1/namespaces", namespace, "deployments", deploymentName), &deployment); err != nil {
		return []string{"Deployment 狀態讀取失敗：" + err.Error()}
	}
	state.DeploymentReplicas = deployment.Status.Replicas
	state.ReadyReplicas = deployment.Status.ReadyReplicas
	state.AvailableReplicas = deployment.Status.AvailableReplicas
	return nil
}

func loadKubernetesPodState(ctx context.Context, client *http.Client, baseURL, token, namespace, labelSelector string, state *hpaAutoscalingState) []string {
	var pods struct {
		Items []struct {
			Status struct {
				Phase      string `json:"phase"`
				Conditions []struct {
					Type   string `json:"type"`
					Status string `json:"status"`
				} `json:"conditions"`
			} `json:"status"`
		} `json:"items"`
	}
	query := url.Values{}
	query.Set("labelSelector", labelSelector)
	podsPath := path.Join("/api/v1/namespaces", namespace, "pods") + "?" + query.Encode()
	if err := kubernetesGetJSON(ctx, client, baseURL, string(token), podsPath, &pods); err != nil {
		return []string{"Pod 狀態讀取失敗：" + err.Error()}
	}
	state.PodCount = len(pods.Items)
	for _, pod := range pods.Items {
		if pod.Status.Phase == "Running" && podHasReadyCondition(pod.Status.Conditions) {
			state.ReadyPods++
		}
	}
	return nil
}

func podHasReadyCondition(conditions []struct {
	Type   string `json:"type"`
	Status string `json:"status"`
}) bool {
	for _, condition := range conditions {
		if condition.Type == "Ready" && condition.Status == "True" {
			return true
		}
	}
	return false
}

var (
	kubernetesServiceAccountTokenPath = "/var/run/secrets/kubernetes.io/serviceaccount/token"
	kubernetesServiceAccountCAPath    = "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"
	newKubernetesHTTPClient           = func(host string, roots *x509.CertPool) *http.Client {
		return &http.Client{
			Timeout: 900 * time.Millisecond,
			Transport: &http.Transport{TLSClientConfig: &tls.Config{
				RootCAs:    roots,
				ServerName: host,
				MinVersion: tls.VersionTLS12,
			}},
		}
	}
)

func kubernetesGetJSON(ctx context.Context, client *http.Client, baseURL, token, apiPath string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+apiPath, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(token))
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return fmt.Errorf("%s %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return json.NewDecoder(resp.Body).Decode(target)
}

func envDefault(key, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
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

func (s *MemoryStore) completeProductionAllocationLocked(orderID string, productionDate time.Time) {
	date := truncateDate(productionDate)
	for index, allocation := range s.allocations {
		if allocation.OrderID == orderID && truncateDate(allocation.Date).Equal(date) && allocation.Status != domain.StatusCompleted {
			s.allocations[index].Locked = true
			s.allocations[index].Status = domain.StatusCompleted
			return
		}
	}
}

func (s *MemoryStore) replaceOrderAllocationsWithCompletedLocked(orderID string, productionDate time.Time) {
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
		return time.Time{}, errors.New(errProductionLineNotFound)
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

func validateUsername(username string) error {
	if username == "" {
		return errors.New("username is required")
	}
	if len(username) > 40 {
		return errors.New("username must be 40 characters or fewer")
	}
	for _, r := range username {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			continue
		}
		return errors.New("username can contain only letters, numbers, dash, underscore, or dot")
	}
	return nil
}

func validateUserRole(role domain.Role, lineID string, lines map[string]domain.ProductionLine) error {
	if role != domain.RoleAdmin && role != domain.RoleSales && role != domain.RoleScheduler {
		return errors.New(errRoleInvalid)
	}
	if role == domain.RoleScheduler {
		if _, ok := lines[lineID]; !ok {
			return errors.New(errSchedulerLineInvalid)
		}
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
		return time.Time{}, errors.New(errProductionLineNotFound)
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

func nextRemainderOrderID(originalID string, incrementExistingSuffix bool, exists func(string) bool) string {
	base := originalID
	next := 1
	if incrementExistingSuffix {
		if split := strings.LastIndex(originalID, "-"); split > len("ORD-") {
			if suffix, err := strconv.Atoi(originalID[split+1:]); err == nil {
				base = originalID[:split]
				next = suffix + 1
			}
		}
	}
	for {
		candidate := fmt.Sprintf("%s-%d", base, next)
		if !exists(candidate) {
			return candidate
		}
		next++
	}
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
	if strings.HasPrefix(message, errOrderNotFoundPrefix) {
		return "找不到訂單：" + strings.TrimPrefix(message, errOrderNotFoundPrefix)
	}
	translations := map[string]string{
		errRouteNotFound:                                           "找不到 API 路由。",
		errMethodNotAllowed:                                        "不支援此 HTTP 方法。",
		"invalid credentials":                                      "帳號或密碼錯誤。",
		errAuthSessionUnavailable:                                  "登入狀態服務暫時無法使用，請稍後再試。",
		errUnauthorized:                                            "請先登入後再操作。",
		"only sales can create orders":                             "只有業務可以建立訂單。",
		"only sales can confirm preview orders":                    "只有業務可以確認訂單預覽。",
		"only schedulers can reject orders":                        "只有排程工程師可以駁回訂單。",
		"only sales can resubmit pending or rejected orders":       "只有業務可以重新送出待排程或需業務處理的訂單。",
		errAdminManageAccounts:                                     "只有管理員可以管理帳號。",
		"only admin or schedulers can create demo conflict orders": "只有管理員或排程工程師可以建立衝突展示訂單。",
		"only schedulers can create schedule jobs":                 "只有排程工程師可以建立排程任務。",
		"schedule job not found":                                   "找不到排程任務。",
		"only schedulers can confirm production":                   "只有排程工程師可以回報生產。",
		"only schedulers can start production":                     "只有排程工程師可以開始生產。",
		errOrderNotFound:                                           "找不到訂單。",
		"cannot update another production line":                    "不能更新其他產線的訂單。",
		"sales can update only their own orders":                   "業務只能更新自己的訂單。",
		"role cannot update orders":                                "此角色不能更新訂單。",
		"only pending or rejected orders can change order details": "只有待排程或需業務處理的訂單可以變更內容。",
		errNoteImmutable:                                           "備註建立後不能修改。",
		"dueDate must use YYYY-MM-DD":                              "交期格式必須是 YYYY-MM-DD。",
		errQuantityRange:                                           "數量必須介於 25 到 2500。",
		errProductionLineNotFound:                                  "產線不存在。",
		"priority must be low or high":                             "優先級必須是 low 或 high。",
		errOrderIDsRequired:                                        "請至少選取一張訂單。",
		"rejection reason is required":                             "請填寫駁回理由。",
		"rejection reason must be 240 characters or fewer":         "駁回理由最多 240 個字。",
		"cannot reject another production line":                    "不能駁回其他產線的訂單。",
		"only pending orders can be rejected":                      "只有待排程訂單可以被駁回。",
		"sales can resubmit only their own orders":                 "只能重新送出自己的訂單。",
		"only pending or rejected orders can be resubmitted":       "只有待排程或需業務處理的訂單可以重新送出。",
		"sales can cancel only their own orders":                   "業務只能取消自己的訂單。",
		"sales can cancel only pending or rejected orders":         "業務只能取消待排程或需業務處理的訂單。",
		"cannot cancel another production line":                    "不能取消其他產線的訂單。",
		"role cannot cancel orders":                                "此角色不能取消訂單。",
		"cannot cancel in-progress or completed orders":            "不能取消生產中或已完成的訂單。",
		notFoundMsg:                               "找不到使用者。",
		"username is required":                    "請填寫帳號。",
		"username already exists":                 "帳號已存在。",
		"username must be 40 characters or fewer": "帳號最多 40 個字。",
		"username can contain only letters, numbers, dash, underscore, or dot": "帳號只能包含英文字母、數字、連字號、底線或句點。",
		"password is required":  "請填寫密碼。",
		errRoleInvalid:          "角色必須是 admin、sales 或 scheduler。",
		errSchedulerLineInvalid: "排程工程師的產線必須存在。",
		"previewId is required before creating a schedule job": "建立排程任務前必須先完成試排。",
		errPreviewExpired:                                              "試排結果已過期或不存在。",
		errPreviewOtherUser:                                            "試排結果屬於其他使用者。",
		"schedule request changed after preview":                       "排程請求與試排內容不同，請重新試排。",
		"cannot schedule another production line":                      "不能排程其他產線。",
		errLineIDRequired:                                              "請選擇產線。",
		errCannotAccessAnotherLine:                                     "不能存取其他產線。",
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
		"draft defer cannot include deferred pending orders":                                              "取消目前草稿訂單時不能同時改送其他待排程訂單。",
		"draft can be deferred only when preview has conflicts":                                           "只有發生衝突的草稿訂單可以改送需業務處理。",
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

func setSecurityHeaders(w http.ResponseWriter, corsAllowedOrigin string) {
	if corsAllowedOrigin == "" {
		corsAllowedOrigin = "*"
	}
	w.Header().Set("Access-Control-Allow-Origin", corsAllowedOrigin)
	w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
	w.Header().Set("Content-Security-Policy", "default-src 'self'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
}
