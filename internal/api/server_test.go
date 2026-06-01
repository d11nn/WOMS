package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/d11nn/woms/internal/auth"
	"github.com/d11nn/woms/internal/domain"
	"github.com/d11nn/woms/internal/metrics"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func init() {
	nowUTC = func() time.Time {
		return time.Date(2026, 4, 30, 0, 0, 0, 0, time.UTC)
	}
}

func TestIngressAuthRejectsMissingToken(t *testing.T) {
	server := NewServer("secret", NewMemoryStore())
	req := httptest.NewRequest(http.MethodGet, "/internal/auth/verify", nil)
	res := httptest.NewRecorder()

	server.ServeHTTP(res, req)

	if res.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", res.Code)
	}
}

func TestAPIErrorMessagesAreZhTW(t *testing.T) {
	server := NewServer("secret", NewMemoryStore())
	req := httptest.NewRequest(http.MethodGet, "/api/orders", nil)
	req.Header.Set("Authorization", "Token invalid")
	res := httptest.NewRecorder()

	server.ServeHTTP(res, req)

	if res.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d body=%s", res.Code, res.Body.String())
	}
	if strings.Contains(res.Body.String(), "missing bearer prefix") {
		t.Fatalf("expected pure zh-TW auth error, got %s", res.Body.String())
	}
	if !strings.Contains(res.Body.String(), "請先登入後再操作") {
		t.Fatalf("expected zh-TW auth error, got %s", res.Body.String())
	}
}

func TestSecurityHeadersUseConfiguredCORSOrigin(t *testing.T) {
	server := NewServerWithPublisherAndConfig("secret", NewMemoryStore(), NoopScheduleJobPublisher{}, ServerConfig{
		CORSAllowedOrigin: "https://woms.example.com",
	})
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	res := httptest.NewRecorder()

	server.ServeHTTP(res, req)

	if got := res.Header().Get("Access-Control-Allow-Origin"); got != "https://woms.example.com" {
		t.Fatalf("expected configured CORS origin, got %q", got)
	}
	if got := res.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Fatalf("expected X-Frame-Options DENY, got %q", got)
	}
}

func TestBusinessAPIsRequireBearerToken(t *testing.T) {
	server := NewServer("secret", NewMemoryStore())
	cases := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/api/orders", ""},
		{http.MethodPost, "/api/orders", `{"customer":"A","lineId":"A","quantity":100,"priority":"low","dueDate":"2026-05-06"}`},
		{http.MethodGet, "/api/lines", ""},
		{http.MethodGet, "/api/users", ""},
		{http.MethodPost, "/api/schedules/preview", `{"lineId":"A","startDate":"2026-05-01"}`},
		{http.MethodGet, "/api/schedules/calendar?lineId=A&month=2026-05", ""},
		{http.MethodPost, "/api/production/start", `{"orderId":"ORD-0000001"}`},
	}
	for _, tt := range cases {
		req := httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
		req.Header.Set("X-User-Role", "admin")
		res := httptest.NewRecorder()
		server.ServeHTTP(res, req)
		if res.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s expected 401, got %d body=%s", tt.method, tt.path, res.Code, res.Body.String())
		}
	}
}

func TestEdgeModeAcceptsSignedTokenButRejectsPlainHeaders(t *testing.T) {
	server := NewServerWithPublisherAndConfig("edge-secret", NewMemoryStore(), NoopScheduleJobPublisher{}, ServerConfig{
		AuthMode: "edge",
	})
	req := httptest.NewRequest(http.MethodGet, "/api/orders", nil)
	req.Header.Set("X-User-ID", "user-admin")
	req.Header.Set("X-User-Role", "admin")
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("expected unsigned edge headers to fail, got %d %s", res.Code, res.Body.String())
	}

	token, err := auth.CreateToken("edge-secret", auth.Claims{Subject: "user-sales", Role: domain.RoleSales}, time.Hour)
	if err != nil {
		t.Fatalf("create edge token: %v", err)
	}
	req = httptest.NewRequest(http.MethodGet, "/api/orders", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	res = httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("expected signed edge token to pass, got %d %s", res.Code, res.Body.String())
	}
}

func TestIngressAuthAcceptsValidToken(t *testing.T) {
	server := NewServerWithPublisherAndConfig("secret", NewMemoryStore(), NoopScheduleJobPublisher{}, ServerConfig{
		TokenSessions: NewMemoryTokenSessionStore(),
	})
	token := login(t, server, "sales", "demo")
	req := httptest.NewRequest(http.MethodGet, "/internal/auth/verify", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	res := httptest.NewRecorder()

	server.ServeHTTP(res, req)

	if res.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d body=%s", res.Code, res.Body.String())
	}
	if got := res.Header().Get("X-User-ID"); got != "user-sales" {
		t.Fatalf("expected ingress auth user id header, got %q", got)
	}
	if got := res.Header().Get("X-User-Role"); got != string(domain.RoleSales) {
		t.Fatalf("expected ingress auth role header, got %q", got)
	}
}

func TestLogoutRevokesTokenSession(t *testing.T) {
	sessions := NewMemoryTokenSessionStore()
	server := NewServerWithPublisherAndConfig("secret", NewMemoryStore(), NoopScheduleJobPublisher{}, ServerConfig{
		TokenSessions: sessions,
	})

	initialUsers := testutil.ToFloat64(metrics.CurrentOnlineUserCount)
	token := login(t, server, "sales", "demo")

	afterLoginUsers := testutil.ToFloat64(metrics.CurrentOnlineUserCount)
	if afterLoginUsers != initialUsers+1 {
		t.Fatalf("expected CurrentOnlineUserCount to increment; got %f, initial %f", afterLoginUsers, initialUsers)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("expected logout 200, got %d body=%s", res.Code, res.Body.String())
	}

	afterLogoutUsers := testutil.ToFloat64(metrics.CurrentOnlineUserCount)
	if afterLogoutUsers != initialUsers {
		t.Fatalf("expected CurrentOnlineUserCount to decrement back to %f; got %f", initialUsers, afterLogoutUsers)
	}

	req = httptest.NewRequest(http.MethodGet, "/internal/auth/verify", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	res = httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("expected revoked token to fail verify, got %d body=%s", res.Code, res.Body.String())
	}
}

func TestSessionStoreFailureReturnsServiceUnavailable(t *testing.T) {
	server := NewServerWithPublisherAndConfig("secret", NewMemoryStore(), NoopScheduleJobPublisher{}, ServerConfig{
		TokenSessions: failingVerifyTokenSessionStore{},
	})
	token := login(t, server, "sales", "demo")
	req := httptest.NewRequest(http.MethodGet, "/api/orders", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)

	if res.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected session store failure to return 503, got %d body=%s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), "登入狀態服務暫時無法使用") {
		t.Fatalf("expected session store unavailable response, got %s", res.Body.String())
	}
}

func TestSalesCannotCreateScheduleJob(t *testing.T) {
	server := NewServer("secret", NewMemoryStore())
	token := login(t, server, "sales", "demo")
	body := bytes.NewBufferString(`{"lineId":"A","startDate":"2026-05-01"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/schedules/jobs", body)
	req.Header.Set("Authorization", "Bearer "+token)
	res := httptest.NewRecorder()

	server.ServeHTTP(res, req)

	if res.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d body=%s", res.Code, res.Body.String())
	}
}

func TestSchedulerCannotCreateScheduleJobWithoutPreview(t *testing.T) {
	server := NewServer("secret", NewMemoryStore())
	token := login(t, server, "scheduler-a", "demo")
	body := bytes.NewBufferString(`{"lineId":"A","startDate":"2026-05-01"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/schedules/jobs", body)
	req.Header.Set("Authorization", "Bearer "+token)
	res := httptest.NewRecorder()

	server.ServeHTTP(res, req)

	if res.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", res.Code, res.Body.String())
	}
}

func TestScheduleJobRejectsStalePreviewRevision(t *testing.T) {
	server := NewServer("secret", NewMemoryStore())
	salesToken := login(t, server, "sales", "demo")
	createOrder(t, server, salesToken, "A")
	schedulerA := login(t, server, "scheduler-a", "demo")
	previewID := createSchedulePreview(t, server, schedulerA, "A")

	body := bytes.NewBufferString(`{"dueDate":"2026-05-08"}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/orders/ORD-0000001", body)
	req.Header.Set("Authorization", "Bearer "+schedulerA)
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("update order failed: %d %s", res.Code, res.Body.String())
	}

	body = bytes.NewBufferString(`{"lineId":"A","startDate":"2026-05-01","previewId":"` + previewID + `"}`)
	req = httptest.NewRequest(http.MethodPost, "/api/schedules/jobs", body)
	req.Header.Set("Authorization", "Bearer "+schedulerA)
	res = httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("expected stale preview rejection, got %d %s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), "排程資料已變更") {
		t.Fatalf("expected zh-TW stale preview error, got %s", res.Body.String())
	}
}

func TestScheduleJobPublishesQueuedJobAndCanExecuteLater(t *testing.T) {
	store := NewMemoryStore()
	publisher := &recordingPublisher{}
	server := NewServerWithPublisher("secret", store, publisher)
	salesToken := login(t, server, "sales", "demo")
	createOrder(t, server, salesToken, "A")
	schedulerA := login(t, server, "scheduler-a", "demo")
	previewID := createSchedulePreview(t, server, schedulerA, "A")

	body := bytes.NewBufferString(`{"lineId":"A","startDate":"2026-05-01","previewId":"` + previewID + `"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/schedules/jobs", body)
	req.Header.Set("Authorization", "Bearer "+schedulerA)
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusAccepted {
		t.Fatalf("create schedule job failed: %d %s", res.Code, res.Body.String())
	}
	var job domain.ScheduleJob
	if err := json.Unmarshal(res.Body.Bytes(), &job); err != nil {
		t.Fatalf("decode job: %v", err)
	}
	if job.Status != domain.JobQueued {
		t.Fatalf("expected queued async job, got %+v", job)
	}
	if len(publisher.jobs) != 1 || publisher.jobs[0].ID != job.ID {
		t.Fatalf("expected published job, got %+v", publisher.jobs)
	}
	if len(store.allocations) != 0 {
		t.Fatalf("queued async job should not persist allocations before worker executes, got %+v", store.allocations)
	}

	completed := store.ExecuteScheduleJob(job.ID)
	if completed.Status != domain.JobCompleted {
		t.Fatalf("expected completed job after execution, got %+v", completed)
	}
	if len(store.allocations) != 1 {
		t.Fatalf("expected allocation after execution, got %+v", store.allocations)
	}
}

func TestScheduleJobPublishFailureRollsBackQueuedJob(t *testing.T) {
	store := NewMemoryStore()
	server := NewServerWithPublisher("secret", store, failingPublisher{})
	salesToken := login(t, server, "sales", "demo")
	createOrder(t, server, salesToken, "A")
	schedulerA := login(t, server, "scheduler-a", "demo")
	previewID := createSchedulePreview(t, server, schedulerA, "A")

	body := bytes.NewBufferString(`{"lineId":"A","startDate":"2026-05-01","previewId":"` + previewID + `"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/schedules/jobs", body)
	req.Header.Set("Authorization", "Bearer "+schedulerA)
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusBadGateway {
		t.Fatalf("expected publish failure, got %d %s", res.Code, res.Body.String())
	}
	if len(store.jobs) != 0 {
		t.Fatalf("publish failure should rollback queued job, got %+v", store.jobs)
	}
	if !strings.Contains(res.Body.String(), "排程任務送出失敗") {
		t.Fatalf("expected zh-TW publish error, got %s", res.Body.String())
	}
}

func TestDemoConflictOrdersHandlerCreatesOrdersWithResponseShapeAndAudit(t *testing.T) {
	store := NewMemoryStore()
	server := NewServer("secret", store)
	schedulerA := login(t, server, "scheduler-a", "demo")

	body := bytes.NewBufferString(`{"count":5,"dueDate":"2026-05-02"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/demo/conflict-orders", body)
	req.Header.Set("Authorization", "Bearer "+schedulerA)
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)

	if res.Code != http.StatusCreated {
		t.Fatalf("expected conflict demo creation 201, got %d %s", res.Code, res.Body.String())
	}
	var payload struct {
		Orders []domain.Order `json:"orders"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode conflict demo response: %v", err)
	}
	if len(payload.Orders) != 5 {
		t.Fatalf("expected five demo orders, got %+v", payload.Orders)
	}
	for index, order := range payload.Orders {
		if order.ID == "" || order.Customer != "Conflict Demo "+strconv.Itoa(index+1) || order.LineID != "A" || order.Status != domain.StatusPending || order.CreatedBy != "user-scheduler-a" {
			t.Fatalf("unexpected conflict demo order response at %d: %+v", index, order)
		}
		if order.Quantity != 2500 || order.Priority != domain.PriorityLow || order.DueDate.Format(dateLayout) != "2026-05-02" {
			t.Fatalf("unexpected conflict demo order details at %d: %+v", index, order)
		}
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.orders) != 5 {
		t.Fatalf("expected five stored demo orders, got %+v", store.orders)
	}
	if len(store.audits) != 5 {
		t.Fatalf("expected one audit per demo order, got %+v", store.audits)
	}
	for _, audit := range store.audits {
		if audit.ActorID != "user-scheduler-a" || audit.Action != "order.create_demo_conflict" || audit.Reason != "2026-05-02" {
			t.Fatalf("unexpected conflict demo audit: %+v", audit)
		}
	}
}

func TestDemoConflictOrdersHandlerRejectsUnauthorizedRoleAndInvalidMethod(t *testing.T) {
	store := NewMemoryStore()
	server := NewServer("secret", store)
	sales := login(t, server, "sales", "demo")

	body := bytes.NewBufferString(`{"lineId":"A","count":5,"dueDate":"2026-05-02"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/demo/conflict-orders", body)
	req.Header.Set("Authorization", "Bearer "+sales)
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusForbidden {
		t.Fatalf("expected sales user forbidden, got %d %s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), "只有管理員或排程工程師可以建立衝突展示訂單") {
		t.Fatalf("expected zh-TW forbidden response, got %s", res.Body.String())
	}

	admin := login(t, server, "admin", "demo")
	req = httptest.NewRequest(http.MethodGet, "/api/demo/conflict-orders", nil)
	req.Header.Set("Authorization", "Bearer "+admin)
	res = httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected invalid method 405, got %d %s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), "不支援此 HTTP 方法") {
		t.Fatalf("expected zh-TW method response, got %s", res.Body.String())
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.orders) != 0 || len(store.audits) != 0 {
		t.Fatalf("rejected demo requests should not create orders or audits, orders=%+v audits=%+v", store.orders, store.audits)
	}
}

func TestMemoryStoreCreateDemoConflictOrdersEnforcesSchedulerLine(t *testing.T) {
	store := NewMemoryStore()
	orders, err := store.CreateDemoConflictOrders(demoConflictRequest{Count: 5, DueDate: "2026-05-02"}, auth.Claims{
		Subject: "user-scheduler-a",
		Role:    domain.RoleScheduler,
		LineID:  "A",
	})
	if err != nil {
		t.Fatalf("create scheduler default-line conflict orders: %v", err)
	}
	if len(orders) != 5 || orders[0].LineID != "A" {
		t.Fatalf("expected scheduler demo orders on assigned line, got %+v", orders)
	}

	_, err = store.CreateDemoConflictOrders(demoConflictRequest{LineID: "B", Count: 5, DueDate: "2026-05-02"}, auth.Claims{
		Subject: "user-scheduler-a",
		Role:    domain.RoleScheduler,
		LineID:  "A",
	})
	if err == nil || !strings.Contains(err.Error(), "another production line") {
		t.Fatalf("expected scheduler cross-line rejection, got %v", err)
	}
}

func TestHPAPeakDemoIsAdminOnlyAndReportsWebAutoscaling(t *testing.T) {
	server := NewServer("secret", NewMemoryStore())
	schedulerA := login(t, server, "scheduler-a", "demo")
	req := httptest.NewRequest(http.MethodPost, "/api/demo/hpa-peak", nil)
	req.Header.Set("Authorization", "Bearer "+schedulerA)
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusForbidden {
		t.Fatalf("expected scheduler forbidden, got %d %s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), "只有管理員可以查看 web autoscaling demo") {
		t.Fatalf("expected web autoscaling forbidden copy, got %s", res.Body.String())
	}

	admin := login(t, server, "admin", "demo")
	req = httptest.NewRequest(http.MethodPost, "/api/demo/hpa-peak", nil)
	req.Header.Set("Authorization", "Bearer "+admin)
	res = httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusAccepted {
		t.Fatalf("expected admin accepted, got %d %s", res.Code, res.Body.String())
	}
	var payload hpaPeakResponse
	if err := json.Unmarshal(res.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode hpa demo response: %v", err)
	}
	if payload.Summary.HPAName != "woms-woms-web-hpa" || payload.Summary.DeploymentName != "woms-woms-web" {
		t.Fatalf("expected web HPA target, got %+v", payload.Summary)
	}
	if payload.Summary.MetricName != "woms_web_nginx_requests_per_second_per_pod" {
		t.Fatalf("expected web nginx metric, got %+v", payload.Summary)
	}
	if !strings.Contains(payload.Summary.Reason, "NGINX Ingress") || !strings.Contains(payload.Summary.Reason, "per-pod req/s") {
		t.Fatalf("expected web traffic reason, got %q", payload.Summary.Reason)
	}
	if payload.Summary.LineCount != 0 || payload.Summary.OrderCount != 0 || payload.Summary.JobCount != 0 {
		t.Fatalf("web autoscaling status should not create scheduling workload, got %+v", payload.Summary)
	}

	req = httptest.NewRequest(http.MethodDelete, "/api/demo/hpa-peak", nil)
	req.Header.Set("Authorization", "Bearer "+admin)
	res = httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("expected clear accepted, got %d %s", res.Code, res.Body.String())
	}
	if err := json.Unmarshal(res.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode clear response: %v", err)
	}
	if payload.Summary.LineCount != 0 || payload.Summary.OrderCount != 0 {
		t.Fatalf("expected cleared hpa demo summary, got %+v", payload.Summary)
	}
}

func TestHPAPeakDemoPostDoesNotPublishScheduleJobs(t *testing.T) {
	store := NewMemoryStore()
	server := NewServerWithPublisher("secret", store, failingPublisher{})
	admin := login(t, server, "admin", "demo")

	req := httptest.NewRequest(http.MethodPost, "/api/demo/hpa-peak", nil)
	req.Header.Set("Authorization", "Bearer "+admin)
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusAccepted {
		t.Fatalf("expected accepted web status, got %d %s", res.Code, res.Body.String())
	}
	summary := store.HPAPeakSummary()
	if summary.Statuses[string(domain.JobQueued)] != 0 {
		t.Fatalf("web status endpoint should not create queued jobs, got %+v", summary.Statuses)
	}
	if summary.OrderCount != 0 || summary.LineCount != 0 {
		t.Fatalf("expected no demo workload orders and lines, got %+v", summary)
	}
}

func TestMemoryStoreHPAPeakDemoCreatesSortableJobsAndClearSummary(t *testing.T) {
	store := NewMemoryStore()
	claims := auth.Claims{Subject: "user-admin", Role: domain.RoleAdmin}

	summary, err := store.CreateHPAPeakDemo(claims)
	if err != nil {
		t.Fatalf("create hpa peak demo: %v", err)
	}
	assertHPAPeakCreatedSummary(t, summary)

	jobs := store.HPAPeakJobs()
	assertHPAPeakJobsSorted(t, jobs)

	cleared, err := store.ClearHPAPeakDemo(claims)
	if err != nil {
		t.Fatalf("clear hpa peak demo: %v", err)
	}
	assertHPAPeakClearedSummary(t, cleared)

	reset, err := store.CreateHPAPeakDemo(claims)
	if err != nil {
		t.Fatalf("recreate hpa peak demo after clear: %v", err)
	}
	assertHPAPeakResetSummary(t, reset)
}

func assertHPAPeakCreatedSummary(t *testing.T, summary hpaPeakSummary) {
	t.Helper()
	if summary.LineCount != hpaDemoLastLine || summary.OrderCount != hpaDemoLastLine*hpaDemoOrdersPerLine {
		t.Fatalf("unexpected hpa demo workload summary: %+v", summary)
	}
	if summary.JobCount != hpaDemoLastLine*hpaDemoJobsPerLine || summary.Statuses[string(domain.JobQueued)] != hpaDemoLastLine*hpaDemoJobsPerLine {
		t.Fatalf("unexpected hpa demo job summary: %+v", summary)
	}
	if len(summary.RecentJobs) != 10 || summary.RecentJobs[0].ID != "HPA-JOB-L001-001" {
		t.Fatalf("expected sorted recent HPA jobs, got %+v", summary.RecentJobs)
	}
}

func assertHPAPeakJobsSorted(t *testing.T, jobs []domain.ScheduleJob) {
	t.Helper()
	if len(jobs) != hpaDemoLastLine*hpaDemoJobsPerLine {
		t.Fatalf("expected all HPA jobs, got %d", len(jobs))
	}
	if jobs[0].ID != "HPA-JOB-L001-001" || jobs[len(jobs)-1].ID != "HPA-JOB-L200-002" {
		t.Fatalf("expected sorted HPA job list, first=%+v last=%+v", jobs[0], jobs[len(jobs)-1])
	}
}

func assertHPAPeakClearedSummary(t *testing.T, summary hpaPeakSummary) {
	t.Helper()
	if summary.LineCount != 0 || summary.OrderCount != 0 || summary.Statuses[string(domain.JobCancelled)] != hpaDemoLastLine*hpaDemoJobsPerLine {
		t.Fatalf("expected queued demo jobs to be cancelled and lines/orders cleared, got %+v", summary)
	}
}

func assertHPAPeakResetSummary(t *testing.T, summary hpaPeakSummary) {
	t.Helper()
	if summary.Statuses[string(domain.JobCancelled)] != 0 || summary.Statuses[string(domain.JobQueued)] != hpaDemoLastLine*hpaDemoJobsPerLine {
		t.Fatalf("reset should remove cancelled demo jobs before recreating, got %+v", summary.Statuses)
	}
}

func TestPublishHPAPeakJobsPublishesInOrderAndStopsOnFailure(t *testing.T) {
	server := NewServerWithPublisher("secret", NewMemoryStore(), &recordingPublisher{})
	jobs := []domain.ScheduleJob{
		{ID: "HPA-JOB-L001-001", LineID: "L001", Status: domain.JobQueued},
		{ID: "HPA-JOB-L002-001", LineID: "L002", Status: domain.JobQueued},
	}
	if err := server.publishHPAPeakJobs(context.Background(), jobs); err != nil {
		t.Fatalf("publish HPA jobs: %v", err)
	}
	publisher := server.publisher.(*recordingPublisher)
	if len(publisher.jobs) != 2 || publisher.jobs[0].ID != jobs[0].ID || publisher.jobs[1].ID != jobs[1].ID {
		t.Fatalf("unexpected published jobs: %+v", publisher.jobs)
	}

	failing := NewServerWithPublisher("secret", NewMemoryStore(), failingPublisher{})
	err := failing.publishHPAPeakJobs(context.Background(), jobs)
	if err == nil || !strings.Contains(err.Error(), "HPA-JOB-L001-001") {
		t.Fatalf("expected failing job id in publish error, got %v", err)
	}
	if err := failing.publishHPAPeakJobs(context.Background(), nil); err != nil {
		t.Fatalf("empty HPA publish should be a no-op, got %v", err)
	}
}

func TestKubernetesAutoscalingStateAndGetJSONEdges(t *testing.T) {
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	if state := loadHPAAutoscalingState("woms", "hpa", "deploy"); state != nil {
		t.Fatalf("expected no in-cluster state without Kubernetes env, got %+v", state)
	}

	t.Setenv("KUBERNETES_SERVICE_HOST", "127.0.0.1")
	hpaAutoscalingCache.Lock()
	hpaAutoscalingCache.key = strings.Join([]string{"127.0.0.1", "woms", "hpa", "deploy", "app.kubernetes.io/component=web"}, "\x00")
	hpaAutoscalingCache.expires = time.Now().Add(time.Minute)
	hpaAutoscalingCache.state = &hpaAutoscalingState{CurrentReplicas: 3, ReadyPods: 2}
	hpaAutoscalingCache.Unlock()
	if state := loadHPAAutoscalingState("woms", "hpa", "deploy"); state == nil || state.CurrentReplicas != 3 || state.ReadyPods != 2 {
		t.Fatalf("expected cached autoscaling state, got %+v", state)
	}
	hpaAutoscalingCache.Lock()
	hpaAutoscalingCache.key = ""
	hpaAutoscalingCache.expires = time.Time{}
	hpaAutoscalingCache.state = nil
	hpaAutoscalingCache.Unlock()
	if state := loadHPAAutoscalingState("woms", "hpa", "deploy"); state == nil || !strings.Contains(state.Error, "service account token") {
		t.Fatalf("expected service account token read error, got %+v", state)
	}
	t.Setenv("KUBERNETES_SERVICE_HOST", "")

	server := newAPITestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer token" {
			t.Fatalf("Authorization = %q", got)
		}
		switch r.URL.Path {
		case "/ok":
			_, _ = w.Write([]byte(`{"value":7}`))
		case "/bad-json":
			_, _ = w.Write([]byte(`{`))
		default:
			w.WriteHeader(http.StatusTeapot)
			_, _ = w.Write([]byte("not ready"))
		}
	}))
	defer server.Close()

	var payload struct {
		Value int `json:"value"`
	}
	if err := kubernetesGetJSON(context.Background(), server.Client(), server.URL, " token ", "/ok", &payload); err != nil {
		t.Fatalf("kubernetesGetJSON success: %v", err)
	}
	if payload.Value != 7 {
		t.Fatalf("decoded payload = %+v", payload)
	}
	if err := kubernetesGetJSON(context.Background(), server.Client(), server.URL, "token", "/missing", &payload); err == nil || !strings.Contains(err.Error(), "418") || !strings.Contains(err.Error(), "not ready") {
		t.Fatalf("expected HTTP status body error, got %v", err)
	}
	if err := kubernetesGetJSON(context.Background(), server.Client(), server.URL, "token", "/bad-json", &payload); err == nil {
		t.Fatal("expected invalid JSON error")
	}
}

func TestAPIHandlerErrorBranches(t *testing.T) {
	server := NewServerWithPublisherAndConfig("secret", NewMemoryStore(), NoopScheduleJobPublisher{}, ServerConfig{
		TokenSessions: failingSaveTokenSessionStore{},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"username":"sales","password":"demo"}`))
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected login session save failure, got %d %s", res.Code, res.Body.String())
	}

	server = NewServer("secret", NewMemoryStore())
	for _, tt := range []struct {
		name    string
		method  string
		path    string
		body    string
		want    int
		handler func(http.ResponseWriter, *http.Request)
		claims  auth.Claims
	}{
		{
			name: "orders method not allowed", method: http.MethodPut, path: "/api/orders", want: http.StatusMethodNotAllowed,
			handler: server.handleOrders, claims: auth.Claims{Subject: "user-sales", Role: domain.RoleSales},
		},
		{
			name: "orders post forbidden", method: http.MethodPost, path: "/api/orders", body: `{}`, want: http.StatusForbidden,
			handler: server.handleOrders, claims: auth.Claims{Subject: "user-scheduler-a", Role: domain.RoleScheduler, LineID: "A"},
		},
		{
			name: "orders post bad json", method: http.MethodPost, path: "/api/orders", body: `{`, want: http.StatusBadRequest,
			handler: server.handleOrders, claims: auth.Claims{Subject: "user-sales", Role: domain.RoleSales},
		},
		{
			name: "confirm preview forbidden", method: http.MethodPost, path: "/api/orders/preview-confirm", body: `{}`, want: http.StatusForbidden,
			handler: server.handleConfirmPreviewOrder, claims: auth.Claims{Subject: "user-scheduler-a", Role: domain.RoleScheduler, LineID: "A"},
		},
		{
			name: "confirm preview bad json", method: http.MethodPost, path: "/api/orders/preview-confirm", body: `{`, want: http.StatusBadRequest,
			handler: server.handleConfirmPreviewOrder, claims: auth.Claims{Subject: "user-sales", Role: domain.RoleSales},
		},
		{
			name: "reject orders forbidden", method: http.MethodPost, path: "/api/orders/reject", body: `{}`, want: http.StatusForbidden,
			handler: server.handleRejectOrders, claims: auth.Claims{Subject: "user-sales", Role: domain.RoleSales},
		},
		{
			name: "reject orders bad json", method: http.MethodPost, path: "/api/orders/reject", body: `{`, want: http.StatusBadRequest,
			handler: server.handleRejectOrders, claims: auth.Claims{Subject: "user-scheduler-a", Role: domain.RoleScheduler, LineID: "A"},
		},
		{
			name: "resubmit forbidden", method: http.MethodPost, path: "/api/orders/resubmit", body: `{}`, want: http.StatusForbidden,
			handler: server.handleResubmitOrder, claims: auth.Claims{Subject: "user-scheduler-a", Role: domain.RoleScheduler, LineID: "A"},
		},
		{
			name: "users method not allowed", method: http.MethodDelete, path: "/api/users", want: http.StatusMethodNotAllowed,
			handler: server.handleUsers, claims: auth.Claims{Subject: "user-admin", Role: domain.RoleAdmin},
		},
		{
			name: "users post bad json", method: http.MethodPost, path: "/api/users", body: `{`, want: http.StatusBadRequest,
			handler: server.handleUsers, claims: auth.Claims{Subject: "user-admin", Role: domain.RoleAdmin},
		},
		{
			name: "reset password forbidden", method: http.MethodPatch, path: "/api/users/password", body: `{}`, want: http.StatusForbidden,
			handler: server.handleResetUserPassword, claims: auth.Claims{Subject: "user-sales", Role: domain.RoleSales},
		},
		{
			name: "demo conflict bad json", method: http.MethodPost, path: "/api/demo/conflict-orders", body: `{`, want: http.StatusBadRequest,
			handler: server.handleDemoConflictOrders, claims: auth.Claims{Subject: "user-admin", Role: domain.RoleAdmin},
		},
		{
			name: "schedule preview bad json", method: http.MethodPost, path: "/api/schedules/preview", body: `{`, want: http.StatusBadRequest,
			handler: server.handleSchedulePreview, claims: auth.Claims{Subject: "user-scheduler-a", Role: domain.RoleScheduler, LineID: "A"},
		},
		{
			name: "hpa method not allowed", method: http.MethodPatch, path: "/api/demo/hpa-peak", want: http.StatusMethodNotAllowed,
			handler: server.handleHPAPeakDemo, claims: auth.Claims{Subject: "user-admin", Role: domain.RoleAdmin},
		},
		{
			name: "schedule jobs bad json", method: http.MethodPost, path: "/api/schedules/jobs", body: `{`, want: http.StatusBadRequest,
			handler: server.handleScheduleJobs, claims: auth.Claims{Subject: "user-scheduler-a", Role: domain.RoleScheduler, LineID: "A"},
		},
		{
			name: "production start forbidden", method: http.MethodPost, path: "/api/production/start", body: `{}`, want: http.StatusForbidden,
			handler: server.handleProductionStart, claims: auth.Claims{Subject: "user-sales", Role: domain.RoleSales},
		},
		{
			name: "production confirm bad json", method: http.MethodPost, path: "/api/production/confirm", body: `{`, want: http.StatusBadRequest,
			handler: server.handleProductionConfirm, claims: auth.Claims{Subject: "user-scheduler-a", Role: domain.RoleScheduler, LineID: "A"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
			req = req.WithContext(context.WithValue(req.Context(), claimsContextKey{}, tt.claims))
			res := httptest.NewRecorder()
			tt.handler(res, req)
			if res.Code != tt.want {
				t.Fatalf("expected %d, got %d body=%s", tt.want, res.Code, res.Body.String())
			}
		})
	}

	for _, tt := range []struct {
		name    string
		method  string
		path    string
		handler func(http.ResponseWriter, *http.Request)
	}{
		{"orders unauthorized", http.MethodGet, "/api/orders", server.handleOrders},
		{"lines unauthorized", http.MethodGet, "/api/lines", server.handleLines},
		{"confirm preview unauthorized", http.MethodPost, "/api/orders/preview-confirm", server.handleConfirmPreviewOrder},
		{"reject unauthorized", http.MethodPost, "/api/orders/reject", server.handleRejectOrders},
		{"resubmit unauthorized", http.MethodPost, "/api/orders/resubmit", server.handleResubmitOrder},
		{"users unauthorized", http.MethodGet, "/api/users", server.handleUsers},
		{"reset password unauthorized", http.MethodPatch, "/api/users/password", server.handleResetUserPassword},
		{"demo conflict unauthorized", http.MethodPost, "/api/demo/conflict-orders", server.handleDemoConflictOrders},
		{"schedule preview unauthorized", http.MethodPost, "/api/schedules/preview", server.handleSchedulePreview},
		{"hpa unauthorized", http.MethodGet, "/api/demo/hpa-peak", server.handleHPAPeakDemo},
		{"calendar unauthorized", http.MethodGet, "/api/schedules/calendar", server.handleScheduleCalendar},
		{"history unauthorized", http.MethodGet, "/api/schedules/history", server.handleScheduleHistory},
		{"jobs unauthorized", http.MethodPost, "/api/schedules/jobs", server.handleScheduleJobs},
		{"get job unauthorized", http.MethodGet, "/api/schedules/jobs/JOB-1", server.handleGetScheduleJob},
		{"production start unauthorized", http.MethodPost, "/api/production/start", server.handleProductionStart},
		{"production confirm unauthorized", http.MethodPost, "/api/production/confirm", server.handleProductionConfirm},
	} {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, nil)
			res := httptest.NewRecorder()
			tt.handler(res, req)
			if res.Code != http.StatusUnauthorized {
				t.Fatalf("expected unauthorized, got %d body=%s", res.Code, res.Body.String())
			}
		})
	}
}

func TestServeHTTPPublicRoutesAndRecorderWrite(t *testing.T) {
	server := NewServer("secret", NewMemoryStore())

	req := httptest.NewRequest(http.MethodOptions, "/api/orders", nil)
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusNoContent {
		t.Fatalf("expected options 204, got %d", res.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/metrics", nil)
	res = httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), "woms_http_requests_total") {
		t.Fatalf("expected metrics body, got %d %s", res.Code, res.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/missing", nil)
	res = httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusNotFound {
		t.Fatalf("expected missing route 404, got %d", res.Code)
	}

	rec := &statusRecorder{ResponseWriter: httptest.NewRecorder()}
	if _, err := rec.Write([]byte("ok")); err != nil {
		t.Fatalf("recorder write: %v", err)
	}
	if rec.status != http.StatusOK {
		t.Fatalf("expected implicit 200 from Write, got %d", rec.status)
	}
}

func TestScheduleJobCalendarAndProductionErrorBranches(t *testing.T) {
	store := NewMemoryStore()
	schedulerClaims := auth.Claims{Subject: "user-scheduler-a", Role: domain.RoleScheduler, LineID: "A"}

	assertMissingScheduleJob(t, store)
	store.jobs["JOB-MISSING-REQ"] = domain.ScheduleJob{ID: "JOB-MISSING-REQ", LineID: "A", Status: domain.JobQueued}
	assertScheduleJobFailure(t, store, "JOB-MISSING-REQ", "找不到排程任務內容")

	store.jobRequests["JOB-REVISION"] = scheduleRequest{LineID: "A", CurrentDate: "2026-04-30", StartDate: "2026-05-01"}
	store.jobs["JOB-REVISION"] = domain.ScheduleJob{ID: "JOB-REVISION", LineID: "A", Status: domain.JobQueued, LineRevision: -1}
	assertScheduleJobFailure(t, store, "JOB-REVISION", "排程資料已變更")

	assertScheduleCalendarError(t, store, "", "", auth.Claims{Role: domain.RoleSales}, "lineId is required")
	assertScheduleCalendarError(t, store, "missing", "2026-05", auth.Claims{Role: domain.RoleAdmin}, "production line does not exist")
	assertScheduleCalendarError(t, store, "A", "2026/05", schedulerClaims, "YYYY-MM")
	assertDefaultSchedulerCalendar(t, store, schedulerClaims)

	assertStartProductionError(t, store, productionStartRequest{OrderID: "missing"}, schedulerClaims, "order not found")
	store.orders["ORD-NO-ALLOC"] = domain.Order{ID: "ORD-NO-ALLOC", LineID: "A", Status: domain.StatusScheduled}
	assertStartProductionError(t, store, productionStartRequest{OrderID: "ORD-NO-ALLOC"}, schedulerClaims, "no allocation")

	assertConfirmProductionError(t, store, productionConfirmRequest{OrderID: "missing"}, schedulerClaims, "order not found")
	store.orders["ORD-INPROGRESS"] = domain.Order{ID: "ORD-INPROGRESS", LineID: "A", Status: domain.StatusInProgress, Quantity: 100}
	assertConfirmProductionError(t, store, productionConfirmRequest{OrderID: "ORD-INPROGRESS", ProducedQuantity: 0, ProductionDate: "2026-05-01"}, schedulerClaims, "greater than zero")
	assertConfirmProductionError(t, store, productionConfirmRequest{OrderID: "ORD-INPROGRESS", ProducedQuantity: 10, ProductionDate: "bad-date"}, schedulerClaims, "YYYY-MM-DD")
}

func assertMissingScheduleJob(t *testing.T, store *MemoryStore) {
	t.Helper()
	if job := store.ExecuteScheduleJob("missing"); job.ID != "" {
		t.Fatalf("missing job should return zero value, got %+v", job)
	}
}

func assertScheduleJobFailure(t *testing.T, store *MemoryStore, id, message string) {
	t.Helper()
	if job := store.ExecuteScheduleJob(id); job.Status != domain.JobFailed || !strings.Contains(job.Message, message) {
		t.Fatalf("expected schedule job failure containing %q, got %+v", message, job)
	}
}

func assertScheduleCalendarError(t *testing.T, store *MemoryStore, lineID, month string, claims auth.Claims, message string) {
	t.Helper()
	if _, err := store.ScheduleCalendar(lineID, month, claims); err == nil || !strings.Contains(err.Error(), message) {
		t.Fatalf("expected schedule calendar error containing %q, got %v", message, err)
	}
}

func assertDefaultSchedulerCalendar(t *testing.T, store *MemoryStore, claims auth.Claims) {
	t.Helper()
	defaulted, err := store.ScheduleCalendar("", "", claims)
	if err != nil {
		t.Fatalf("default scheduler calendar: %v", err)
	}
	if defaulted.LineID != "A" || defaulted.Month != "2026-04" || defaulted.Timezone == "" {
		t.Fatalf("expected scheduler default line/month calendar, got %+v", defaulted)
	}
}

func assertStartProductionError(t *testing.T, store *MemoryStore, req productionStartRequest, claims auth.Claims, message string) {
	t.Helper()
	if _, err := store.StartProduction(req, claims); err == nil || !strings.Contains(err.Error(), message) {
		t.Fatalf("expected start production error containing %q, got %v", message, err)
	}
}

func assertConfirmProductionError(t *testing.T, store *MemoryStore, req productionConfirmRequest, claims auth.Claims, message string) {
	t.Helper()
	if _, err := store.ConfirmProduction(req, claims); err == nil || !strings.Contains(err.Error(), message) {
		t.Fatalf("expected confirm production error containing %q, got %v", message, err)
	}
}

func newAPITestServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	defer func() {
		if recovered := recover(); recovered != nil {
			if strings.Contains(fmt.Sprint(recovered), "operation not permitted") {
				t.Skipf("httptest server is not permitted in this sandbox: %v", recovered)
			}
			panic(recovered)
		}
	}()
	return httptest.NewServer(handler)
}

func TestUserByUsernameHandlerEdges(t *testing.T) {
	server := NewServer("secret", NewMemoryStore())
	scheduler := login(t, server, "scheduler-a", "demo")
	req := httptest.NewRequest(http.MethodDelete, "/api/users/sales", nil)
	req.Header.Set("Authorization", "Bearer "+scheduler)
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusForbidden {
		t.Fatalf("expected scheduler forbidden, got %d %s", res.Code, res.Body.String())
	}

	admin := login(t, server, "admin", "demo")
	req = httptest.NewRequest(http.MethodGet, "/api/users/sales", nil)
	req.Header.Set("Authorization", "Bearer "+admin)
	res = httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected method rejection, got %d %s", res.Code, res.Body.String())
	}

	req = httptest.NewRequest(http.MethodDelete, "/api/users/missing", nil)
	req.Header.Set("Authorization", "Bearer "+admin)
	res = httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusBadRequest || !strings.Contains(res.Body.String(), "找不到使用者") {
		t.Fatalf("expected not-found delete response, got %d %s", res.Code, res.Body.String())
	}

	req = httptest.NewRequest(http.MethodDelete, "/api/users/", nil)
	req.Header.Set("Authorization", "Bearer "+admin)
	res = httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusNotFound {
		t.Fatalf("expected empty username route rejection, got %d %s", res.Code, res.Body.String())
	}
}

func TestScheduleLineResolutionHelpers(t *testing.T) {
	schedulerClaims := auth.Claims{Role: domain.RoleScheduler, LineID: "B"}
	salesClaims := auth.Claims{Role: domain.RoleSales}
	if got := scheduleLineID(scheduleRequest{}, schedulerClaims); got != "B" {
		t.Fatalf("scheduleLineID default scheduler line = %q", got)
	}
	if got := scheduleLineID(scheduleRequest{LineID: "C"}, schedulerClaims); got != "C" {
		t.Fatalf("scheduleLineID explicit line = %q", got)
	}
	if got := scheduleLineID(scheduleRequest{}, salesClaims); got != "" {
		t.Fatalf("scheduleLineID sales default = %q", got)
	}
	if got := scheduleRequestLineID(scheduleRequest{DraftOrder: &createOrderRequest{LineID: "D"}}, salesClaims); got != "D" {
		t.Fatalf("scheduleRequestLineID draft line = %q", got)
	}
	if got := scheduleRequestLineID(scheduleRequest{}, schedulerClaims); got != "B" {
		t.Fatalf("scheduleRequestLineID scheduler claim line = %q", got)
	}
	if hpaDemoLineID(1) != "L001" || hpaDemoLineID(200) != "L200" {
		t.Fatalf("unexpected HPA demo line ids")
	}
	if !isHPADemoLine("L001") || !isHPADemoLine("L200") || isHPADemoLine("L000") || isHPADemoLine("L201") || isHPADemoLine("A") {
		t.Fatalf("unexpected HPA demo line recognition")
	}
}

func TestProductionHelperCompletionAndOrderIDFromTime(t *testing.T) {
	store := NewMemoryStore()
	productionDay := mustAPIDate(t, "2026-05-02")
	store.allocations = []domain.ScheduleAllocation{
		{OrderID: "ORD-0000001", LineID: "A", Date: productionDay, Quantity: 1000, Status: domain.StatusInProgress},
		{OrderID: "ORD-0000001", LineID: "A", Date: mustAPIDate(t, "2026-05-03"), Quantity: 500, Status: domain.StatusInProgress},
		{OrderID: "ORD-0000002", LineID: "A", Date: productionDay, Quantity: 250, Status: domain.StatusCompleted},
	}

	store.completeProductionAllocationLocked("ORD-0000001", productionDay)
	if !store.allocations[0].Locked || store.allocations[0].Status != domain.StatusCompleted {
		t.Fatalf("expected selected allocation completed and locked, got %+v", store.allocations[0])
	}
	if store.allocations[1].Status != domain.StatusInProgress {
		t.Fatalf("other allocation should remain open, got %+v", store.allocations[1])
	}
	allocation, ok := store.productionAllocationLocked("ORD-0000002", productionDay)
	if !ok || allocation.Status != domain.StatusCompleted {
		t.Fatalf("expected completed allocation fallback, got %+v ok=%t", allocation, ok)
	}

	store.replaceOrderAllocationsWithCompletedLocked("ORD-0000001", productionDay)
	if len(store.allocations) != 2 {
		t.Fatalf("expected other order plus completed selected allocation, got %+v", store.allocations)
	}
	for _, allocation := range store.allocations {
		if allocation.OrderID == "ORD-0000001" && (allocation.Status != domain.StatusCompleted || !allocation.Locked || !truncateDate(allocation.Date).Equal(productionDay)) {
			t.Fatalf("unexpected replaced allocation: %+v", allocation)
		}
	}

	got := orderIDFromTime(time.Unix(0, 1234567890).UTC())
	if got != "ORD-4567890" {
		t.Fatalf("orderIDFromTime = %q", got)
	}
}

func TestExecuteScheduleJobRespectsLineLockAndCancelledStatus(t *testing.T) {
	store := NewMemoryStore()
	server := NewServerWithPublisher("secret", store, &recordingPublisher{})
	salesToken := login(t, server, "sales", "demo")
	createOrder(t, server, salesToken, "A")
	schedulerA := login(t, server, "scheduler-a", "demo")
	previewID := createSchedulePreview(t, server, schedulerA, "A")

	body := bytes.NewBufferString(`{"lineId":"A","startDate":"2026-05-01","previewId":"` + previewID + `"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/schedules/jobs", body)
	req.Header.Set("Authorization", "Bearer "+schedulerA)
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusAccepted {
		t.Fatalf("create schedule job failed: %d %s", res.Code, res.Body.String())
	}
	var job domain.ScheduleJob
	if err := json.Unmarshal(res.Body.Bytes(), &job); err != nil {
		t.Fatalf("decode job: %v", err)
	}

	store.mu.Lock()
	store.lineLocks["A"] = true
	store.mu.Unlock()
	failed := store.ExecuteScheduleJob(job.ID)
	if failed.Status != domain.JobFailed || !strings.Contains(failed.Message, "產線正在排程中") {
		t.Fatalf("expected line lock failure, got %+v", failed)
	}
	store.mu.Lock()
	delete(store.lineLocks, "A")
	store.mu.Unlock()

	createOrder(t, server, salesToken, "A")
	previewID = createSchedulePreview(t, server, schedulerA, "A")
	body = bytes.NewBufferString(`{"lineId":"A","startDate":"2026-05-01","previewId":"` + previewID + `"}`)
	req = httptest.NewRequest(http.MethodPost, "/api/schedules/jobs", body)
	req.Header.Set("Authorization", "Bearer "+schedulerA)
	res = httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusAccepted {
		t.Fatalf("create second schedule job failed: %d %s", res.Code, res.Body.String())
	}
	if err := json.Unmarshal(res.Body.Bytes(), &job); err != nil {
		t.Fatalf("decode second job: %v", err)
	}
	store.mu.Lock()
	job.Status = domain.JobCancelled
	store.jobs[job.ID] = job
	store.mu.Unlock()
	cancelled := store.ExecuteScheduleJob(job.ID)
	if cancelled.Status != domain.JobCancelled {
		t.Fatalf("expected cancelled job to stay cancelled, got %+v", cancelled)
	}
}

func TestDefaultScheduleCurrentDateUsesServerDayWhenMissing(t *testing.T) {
	req := defaultScheduleCurrentDate(scheduleRequest{
		LineID:    "A",
		StartDate: "2026-05-01",
	}, mustAPIDate(t, "2026-05-03"))

	if req.CurrentDate != "2026-05-03" {
		t.Fatalf("expected default current date from server day, got %q", req.CurrentDate)
	}
}

func TestLineTimezoneCurrentDateUsesPlantLocalDay(t *testing.T) {
	now := time.Date(2026, 5, 4, 16, 30, 0, 0, time.UTC)

	taipei, err := currentDateInLineTimezone(domain.ProductionLine{ID: "A", Timezone: "Asia/Taipei"}, now)
	if err != nil {
		t.Fatalf("taipei current date failed: %v", err)
	}
	newYork, err := currentDateInLineTimezone(domain.ProductionLine{ID: "B", Timezone: "America/New_York"}, now)
	if err != nil {
		t.Fatalf("new york current date failed: %v", err)
	}

	if taipei.Format(dateLayout) != "2026-05-05" {
		t.Fatalf("expected Asia/Taipei today to be 2026-05-05, got %s", taipei.Format(dateLayout))
	}
	if newYork.Format(dateLayout) != "2026-05-04" {
		t.Fatalf("expected America/New_York today to be 2026-05-04, got %s", newYork.Format(dateLayout))
	}
}

func TestLinesAPIIncludesTimezone(t *testing.T) {
	server := NewServer("secret", NewMemoryStore())
	token := login(t, server, "sales", "demo")
	req := httptest.NewRequest(http.MethodGet, "/api/lines", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	res := httptest.NewRecorder()

	server.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", res.Code, res.Body.String())
	}
	var payload struct {
		Lines []domain.ProductionLine `json:"lines"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode lines response: %v", err)
	}
	if len(payload.Lines) == 0 || payload.Lines[0].Timezone == "" {
		t.Fatalf("expected line timezone in response, got %+v", payload.Lines)
	}
	var lineD domain.ProductionLine
	for _, line := range payload.Lines {
		if line.ID == "D" {
			lineD = line
			break
		}
	}
	if lineD.Timezone != "Europe/London" {
		t.Fatalf("expected Line D timezone Europe/London, got %+v", lineD)
	}
}

func TestOnlyAdminCanAssignUsers(t *testing.T) {
	server := NewServer("secret", NewMemoryStore())
	salesToken := login(t, server, "sales", "demo")
	body := bytes.NewBufferString(`{"username":"scheduler-a","role":"scheduler","lineId":"B"}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/users", body)
	req.Header.Set("Authorization", "Bearer "+salesToken)
	res := httptest.NewRecorder()

	server.ServeHTTP(res, req)

	if res.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d body=%s", res.Code, res.Body.String())
	}

	adminToken := login(t, server, "admin", "demo")
	body = bytes.NewBufferString(`{"username":"scheduler-a","role":"scheduler","lineId":"B"}`)
	req = httptest.NewRequest(http.MethodPatch, "/api/users", body)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	res = httptest.NewRecorder()

	server.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", res.Code, res.Body.String())
	}
}

func TestAdminCanCreateResetAndDeleteUser(t *testing.T) {
	server := NewServer("secret", NewMemoryStore())
	admin := login(t, server, "admin", "demo")

	body := bytes.NewBufferString(`{"username":"new-sales","password":"temporary","role":"sales"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/users", body)
	req.Header.Set("Authorization", "Bearer "+admin)
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusCreated {
		t.Fatalf("expected create user 201, got %d %s", res.Code, res.Body.String())
	}
	if strings.Contains(res.Body.String(), "PasswordHash") || strings.Contains(res.Body.String(), "temporary") {
		t.Fatalf("create user response leaked password material: %s", res.Body.String())
	}

	newSales := login(t, server, "new-sales", "temporary")
	body = bytes.NewBufferString(`{"customer":"Class Demo","lineId":"A","quantity":100,"priority":"low","dueDate":"2026-05-06"}`)
	req = httptest.NewRequest(http.MethodPost, "/api/orders", body)
	req.Header.Set("Authorization", "Bearer "+newSales)
	res = httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusCreated {
		t.Fatalf("expected new sales user to create order, got %d %s", res.Code, res.Body.String())
	}

	body = bytes.NewBufferString(`{"username":"new-sales","password":"rotated"}`)
	req = httptest.NewRequest(http.MethodPatch, "/api/users/password", body)
	req.Header.Set("Authorization", "Bearer "+admin)
	res = httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("expected reset password 200, got %d %s", res.Code, res.Body.String())
	}
	_ = login(t, server, "new-sales", "rotated")

	req = httptest.NewRequest(http.MethodDelete, "/api/users/new-sales", nil)
	req.Header.Set("Authorization", "Bearer "+admin)
	res = httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("expected delete user 200, got %d %s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), `"disabled":true`) || strings.Contains(res.Body.String(), `"deleted":true`) {
		t.Fatalf("expected referenced user to be disabled, got %s", res.Body.String())
	}

	body = bytes.NewBufferString(`{"username":"new-sales","password":"rotated"}`)
	req = httptest.NewRequest(http.MethodPost, "/api/auth/login", body)
	res = httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("expected deleted/disabled user login to fail, got %d %s", res.Code, res.Body.String())
	}
}

func TestAdminDeleteUnreferencedUserReportsDeleted(t *testing.T) {
	server := NewServer("secret", NewMemoryStore())
	admin := login(t, server, "admin", "demo")

	body := bytes.NewBufferString(`{"username":"unused-sales","password":"temporary","role":"sales"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/users", body)
	req.Header.Set("Authorization", "Bearer "+admin)
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusCreated {
		t.Fatalf("expected create user 201, got %d %s", res.Code, res.Body.String())
	}

	req = httptest.NewRequest(http.MethodDelete, "/api/users/unused-sales", nil)
	req.Header.Set("Authorization", "Bearer "+admin)
	res = httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("expected delete user 200, got %d %s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), `"deleted":true`) || strings.Contains(res.Body.String(), `"disabled":true`) {
		t.Fatalf("expected unreferenced user to be deleted, got %s", res.Body.String())
	}
}

func TestAdminSelfDeleteDisablesAccount(t *testing.T) {
	server := NewServer("secret", NewMemoryStore())
	admin := login(t, server, "admin", "demo")

	body := bytes.NewBufferString(`{"username":"temporary-admin","password":"temporary","role":"admin"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/users", body)
	req.Header.Set("Authorization", "Bearer "+admin)
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusCreated {
		t.Fatalf("expected create admin 201, got %d %s", res.Code, res.Body.String())
	}

	self := login(t, server, "temporary-admin", "temporary")
	req = httptest.NewRequest(http.MethodDelete, "/api/users/temporary-admin", nil)
	req.Header.Set("Authorization", "Bearer "+self)
	res = httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("expected self delete 200, got %d %s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), `"disabled":true`) || strings.Contains(res.Body.String(), `"deleted":true`) {
		t.Fatalf("expected self delete to disable account, got %s", res.Body.String())
	}
}

func TestCreateUserRequiresAdminAndSchedulerLine(t *testing.T) {
	server := NewServer("secret", NewMemoryStore())
	sales := login(t, server, "sales", "demo")
	body := bytes.NewBufferString(`{"username":"new-scheduler","password":"temporary","role":"scheduler","lineId":"A"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/users", body)
	req.Header.Set("Authorization", "Bearer "+sales)
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusForbidden {
		t.Fatalf("expected non-admin create user 403, got %d %s", res.Code, res.Body.String())
	}

	admin := login(t, server, "admin", "demo")
	body = bytes.NewBufferString(`{"username":"new-scheduler","password":"temporary","role":"scheduler"}`)
	req = httptest.NewRequest(http.MethodPost, "/api/users", body)
	req.Header.Set("Authorization", "Bearer "+admin)
	res = httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("expected scheduler without line to fail, got %d %s", res.Code, res.Body.String())
	}
}

func TestOrderValidationRejectsInvalidQuantity(t *testing.T) {
	server := NewServer("secret", NewMemoryStore())
	token := login(t, server, "sales", "demo")
	body := bytes.NewBufferString(`{"customer":"ACME","lineId":"A","quantity":10,"priority":"low","dueDate":"2026-05-03"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/orders", body)
	req.Header.Set("Authorization", "Bearer "+token)
	res := httptest.NewRecorder()

	server.ServeHTTP(res, req)

	if res.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", res.Code, res.Body.String())
	}
}

func TestSalesCreateOrderRejectsTodayDueDate(t *testing.T) {
	server := NewServer("secret", NewMemoryStore())
	token := login(t, server, "sales", "demo")
	body := bytes.NewBufferString(`{"customer":"ACME","lineId":"A","quantity":2500,"priority":"low","dueDate":"2026-04-30"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/orders", body)
	req.Header.Set("Authorization", "Bearer "+token)
	res := httptest.NewRecorder()

	server.ServeHTTP(res, req)

	if res.Code != http.StatusBadRequest || !strings.Contains(res.Body.String(), unacceptableDueDateMessage) {
		t.Fatalf("expected unacceptable due date rejection, got %d body=%s", res.Code, res.Body.String())
	}
}

func TestSalesCreateOrderRejectsPastDueDate(t *testing.T) {
	server := NewServer("secret", NewMemoryStore())
	token := login(t, server, "sales", "demo")
	body := bytes.NewBufferString(`{"customer":"ACME","lineId":"A","quantity":2500,"priority":"high","dueDate":"2026-04-29"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/orders", body)
	req.Header.Set("Authorization", "Bearer "+token)
	res := httptest.NewRecorder()

	server.ServeHTTP(res, req)

	if res.Code != http.StatusBadRequest || !strings.Contains(res.Body.String(), unacceptableDueDateMessage) {
		t.Fatalf("expected unacceptable due date rejection, got %d body=%s", res.Code, res.Body.String())
	}
}

func TestSalesCreateOrderAcceptsTomorrowDueDate(t *testing.T) {
	server := NewServer("secret", NewMemoryStore())
	token := login(t, server, "sales", "demo")
	body := bytes.NewBufferString(`{"customer":"ACME","lineId":"A","quantity":2500,"priority":"low","dueDate":"2026-05-01"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/orders", body)
	req.Header.Set("Authorization", "Bearer "+token)
	res := httptest.NewRecorder()

	server.ServeHTTP(res, req)

	if res.Code != http.StatusCreated {
		t.Fatalf("expected created order, got %d body=%s", res.Code, res.Body.String())
	}
}

func TestSalesCreateOrderUsesLineTimezoneForDueDateValidation(t *testing.T) {
	originalNow := nowUTC
	nowUTC = func() time.Time {
		return time.Date(2026, 5, 4, 16, 30, 0, 0, time.UTC)
	}
	t.Cleanup(func() {
		nowUTC = originalNow
	})
	store := NewMemoryStore()
	store.lines["A"] = domain.ProductionLine{ID: "A", Name: "Line A", CapacityPerDay: 10000, Timezone: "Asia/Taipei"}
	store.lines["B"] = domain.ProductionLine{ID: "B", Name: "Line B", CapacityPerDay: 10000, Timezone: "America/New_York"}
	server := NewServer("secret", store)
	token := login(t, server, "sales", "demo")

	body := bytes.NewBufferString(`{"customer":"ACME","lineId":"A","quantity":2500,"priority":"low","dueDate":"2026-05-05"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/orders", body)
	req.Header.Set("Authorization", "Bearer "+token)
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusBadRequest || !strings.Contains(res.Body.String(), unacceptableDueDateMessage) {
		t.Fatalf("expected Taipei line to reject local-today due date, got %d body=%s", res.Code, res.Body.String())
	}

	body = bytes.NewBufferString(`{"customer":"ACME","lineId":"B","quantity":2500,"priority":"low","dueDate":"2026-05-05"}`)
	req = httptest.NewRequest(http.MethodPost, "/api/orders", body)
	req.Header.Set("Authorization", "Bearer "+token)
	res = httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusCreated {
		t.Fatalf("expected New York line to accept local-tomorrow due date, got %d body=%s", res.Code, res.Body.String())
	}
}

func TestSalesCreateOrderAcceptsFutureDueDate(t *testing.T) {
	server := NewServer("secret", NewMemoryStore())
	token := login(t, server, "sales", "demo")
	body := bytes.NewBufferString(`{"customer":"ACME","lineId":"A","quantity":2500,"priority":"high","dueDate":"2026-05-02"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/orders", body)
	req.Header.Set("Authorization", "Bearer "+token)
	res := httptest.NewRecorder()

	server.ServeHTTP(res, req)

	if res.Code != http.StatusCreated {
		t.Fatalf("expected created order, got %d body=%s", res.Code, res.Body.String())
	}
}

func TestSchedulerSeesOnlyAssignedLineOrders(t *testing.T) {
	server := NewServer("secret", NewMemoryStore())
	salesToken := login(t, server, "sales", "demo")
	createOrder(t, server, salesToken, "A")
	createOrder(t, server, salesToken, "B")

	schedulerA := login(t, server, "scheduler-a", "demo")
	req := httptest.NewRequest(http.MethodGet, "/api/orders", nil)
	req.Header.Set("Authorization", "Bearer "+schedulerA)
	res := httptest.NewRecorder()

	server.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", res.Code, res.Body.String())
	}
	var payload struct {
		Orders []struct {
			LineID string `json:"lineId"`
		} `json:"orders"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode orders response: %v", err)
	}
	if len(payload.Orders) != 1 || payload.Orders[0].LineID != "A" {
		t.Fatalf("expected only line A order, got %+v", payload.Orders)
	}
}

func TestSalesSeesOnlyOwnOrders(t *testing.T) {
	store := NewMemoryStore()
	server := NewServer("secret", store)
	salesToken := login(t, server, "sales", "demo")
	createOrder(t, server, salesToken, "A")
	store.mu.Lock()
	store.orders["ORD-0000002"] = domain.Order{
		ID:        "ORD-0000002",
		Customer:  "Other Sales",
		LineID:    "A",
		Quantity:  2500,
		Priority:  domain.PriorityLow,
		Status:    domain.StatusRejected,
		DueDate:   mustAPIDate(t, "2026-05-03"),
		CreatedBy: "user-other-sales",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	store.mu.Unlock()

	req := httptest.NewRequest(http.MethodGet, "/api/orders", nil)
	req.Header.Set("Authorization", "Bearer "+salesToken)
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", res.Code, res.Body.String())
	}
	var payload struct {
		Orders []domain.Order `json:"orders"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode orders response: %v", err)
	}
	if len(payload.Orders) != 1 || payload.Orders[0].ID != "ORD-0000001" {
		t.Fatalf("expected sales to see only own order, got %+v", payload.Orders)
	}
}

func TestSchedulerCannotReadAnotherLineJob(t *testing.T) {
	server := NewServer("secret", NewMemoryStore())
	salesToken := login(t, server, "sales", "demo")
	createOrder(t, server, salesToken, "B")

	schedulerB := login(t, server, "scheduler-b", "demo")
	jobID := createScheduleJob(t, server, schedulerB, "B")

	schedulerA := login(t, server, "scheduler-a", "demo")
	req := httptest.NewRequest(http.MethodGet, "/api/schedules/jobs/"+jobID, nil)
	req.Header.Set("Authorization", "Bearer "+schedulerA)
	res := httptest.NewRecorder()

	server.ServeHTTP(res, req)

	if res.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d body=%s", res.Code, res.Body.String())
	}
}

func TestScheduleJobPersistsAllocationsAndCalendar(t *testing.T) {
	server := NewServer("secret", NewMemoryStore())
	salesToken := login(t, server, "sales", "demo")
	createOrder(t, server, salesToken, "A")

	schedulerA := login(t, server, "scheduler-a", "demo")
	jobID := createScheduleJob(t, server, schedulerA, "A")
	if jobID == "" {
		t.Fatal("expected schedule job id")
	}

	req := httptest.NewRequest(http.MethodGet, "/api/schedules/calendar?lineId=A&month=2026-05", nil)
	req.Header.Set("Authorization", "Bearer "+schedulerA)
	res := httptest.NewRecorder()

	server.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", res.Code, res.Body.String())
	}
	var payload struct {
		Allocations []struct {
			OrderID string `json:"orderId"`
			Status  string `json:"status"`
		} `json:"allocations"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode calendar response: %v", err)
	}
	if len(payload.Allocations) != 1 {
		t.Fatalf("expected one allocation, got %+v", payload.Allocations)
	}
	if payload.Allocations[0].Status != string("已排程") {
		t.Fatalf("expected scheduled status, got %+v", payload.Allocations[0])
	}
}

func TestScheduleCalendarIncludesVisibleAdjacentMonthDays(t *testing.T) {
	server := NewServer("secret", NewMemoryStore())
	salesToken := login(t, server, "sales", "demo")
	createOrder(t, server, salesToken, "A")
	schedulerA := login(t, server, "scheduler-a", "demo")
	createScheduleJob(t, server, schedulerA, "A")

	req := httptest.NewRequest(http.MethodGet, "/api/schedules/calendar?lineId=A&month=2026-04", nil)
	req.Header.Set("Authorization", "Bearer "+schedulerA)
	res := httptest.NewRecorder()

	server.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", res.Code, res.Body.String())
	}
	var payload struct {
		Allocations []struct {
			OrderID string `json:"orderId"`
		} `json:"allocations"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode calendar response: %v", err)
	}
	if len(payload.Allocations) != 1 || payload.Allocations[0].OrderID != "ORD-0000001" {
		t.Fatalf("expected May 1 allocation on April calendar page, got %+v", payload.Allocations)
	}
}

func TestScheduleCalendarExcludesOtherMonths(t *testing.T) {
	server := NewServer("secret", NewMemoryStore())
	salesToken := login(t, server, "sales", "demo")
	createOrder(t, server, salesToken, "A")
	schedulerA := login(t, server, "scheduler-a", "demo")
	createScheduleJob(t, server, schedulerA, "A")

	req := httptest.NewRequest(http.MethodGet, "/api/schedules/calendar?lineId=A&month=2026-06", nil)
	req.Header.Set("Authorization", "Bearer "+schedulerA)
	res := httptest.NewRecorder()

	server.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", res.Code, res.Body.String())
	}
	var payload struct {
		Allocations []any `json:"allocations"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode calendar response: %v", err)
	}
	if len(payload.Allocations) != 0 {
		t.Fatalf("expected no allocations in other month, got %+v", payload.Allocations)
	}
}

func TestSchedulePreviewRespectsRequestedFutureStart(t *testing.T) {
	store := NewMemoryStore()
	server := NewServer("secret", store)
	salesToken := login(t, server, "sales", "demo")
	body := bytes.NewBufferString(`{"customer":"ACME","lineId":"A","quantity":2500,"priority":"low","dueDate":"2026-05-01"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/orders", body)
	req.Header.Set("Authorization", "Bearer "+salesToken)
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusCreated {
		t.Fatalf("create order failed: %d %s", res.Code, res.Body.String())
	}

	store.mu.Lock()
	store.allocations = append(store.allocations, domain.ScheduleAllocation{
		OrderID:  "EXISTING-APR30",
		LineID:   "A",
		Date:     mustAPIDate(t, "2026-04-30"),
		Quantity: 7710,
		Priority: domain.PriorityLow,
	})
	store.mu.Unlock()

	schedulerA := login(t, server, "scheduler-a", "demo")
	body = bytes.NewBufferString(`{"lineId":"A","startDate":"2026-05-01","currentDate":"2026-04-30","orderIds":["ORD-0000001"]}`)
	req = httptest.NewRequest(http.MethodPost, "/api/schedules/preview", body)
	req.Header.Set("Authorization", "Bearer "+schedulerA)
	res = httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("preview failed: %d %s", res.Code, res.Body.String())
	}
	var payload struct {
		Allocations []struct {
			Date     time.Time `json:"date"`
			Quantity int       `json:"quantity"`
		} `json:"allocations"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode preview response: %v", err)
	}
	if len(payload.Allocations) != 1 {
		t.Fatalf("expected one allocation on requested future start, got %+v", payload.Allocations)
	}
	if payload.Allocations[0].Date.Format(dateLayout) != "2026-05-01" || payload.Allocations[0].Quantity != 2500 {
		t.Fatalf("expected full allocation on 2026-05-01, got %+v", payload.Allocations[0])
	}
}

func TestSchedulePreviewDefaultsCurrentDateFromLineTimezone(t *testing.T) {
	originalNow := nowUTC
	nowUTC = func() time.Time {
		return time.Date(2026, 5, 4, 16, 30, 0, 0, time.UTC)
	}
	t.Cleanup(func() {
		nowUTC = originalNow
	})
	store := NewMemoryStore()
	store.lines["B"] = domain.ProductionLine{ID: "B", Name: "Line B", CapacityPerDay: 10000, Timezone: "America/New_York"}
	server := NewServer("secret", store)
	salesToken := login(t, server, "sales", "demo")
	createOrderWithPriorityAndDue(t, server, salesToken, "B", "low", "2026-05-06")

	schedulerB := login(t, server, "scheduler-b", "demo")
	body := bytes.NewBufferString(`{"lineId":"B","startDate":"2026-05-04","orderIds":["ORD-0000001"]}`)
	req := httptest.NewRequest(http.MethodPost, "/api/schedules/preview", body)
	req.Header.Set("Authorization", "Bearer "+schedulerB)
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("preview failed: %d %s", res.Code, res.Body.String())
	}
	var payload struct {
		CurrentDate string `json:"currentDate"`
		Allocations []struct {
			Date time.Time `json:"date"`
		} `json:"allocations"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode preview response: %v", err)
	}
	if payload.CurrentDate != "2026-05-04" {
		t.Fatalf("expected New York current date, got %q", payload.CurrentDate)
	}
	if len(payload.Allocations) != 1 || payload.Allocations[0].Date.Format(dateLayout) != "2026-05-05" {
		t.Fatalf("expected start after New York current date, got %+v", payload.Allocations)
	}
}

func TestSchedulerCannotReadAnotherLineCalendar(t *testing.T) {
	server := NewServer("secret", NewMemoryStore())
	salesToken := login(t, server, "sales", "demo")
	createOrder(t, server, salesToken, "B")
	schedulerB := login(t, server, "scheduler-b", "demo")
	createScheduleJob(t, server, schedulerB, "B")

	schedulerA := login(t, server, "scheduler-a", "demo")
	req := httptest.NewRequest(http.MethodGet, "/api/schedules/calendar?lineId=B&month=2026-05", nil)
	req.Header.Set("Authorization", "Bearer "+schedulerA)
	res := httptest.NewRecorder()

	server.ServeHTTP(res, req)

	if res.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", res.Code, res.Body.String())
	}
}

func TestSalesConfirmsDraftPreviewIntoPendingOrder(t *testing.T) {
	server := NewServer("secret", NewMemoryStore())
	salesToken := login(t, server, "sales", "demo")
	body := bytes.NewBufferString(`{"lineId":"A","startDate":"2026-05-01","currentDate":"2026-04-30","draftOrder":{"customer":"Draft Co","lineId":"A","quantity":2500,"priority":"low","dueDate":"2026-05-03"}}`)
	req := httptest.NewRequest(http.MethodPost, "/api/schedules/preview", body)
	req.Header.Set("Authorization", "Bearer "+salesToken)
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("preview failed: %d %s", res.Code, res.Body.String())
	}
	var preview struct {
		PreviewID string `json:"previewId"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &preview); err != nil {
		t.Fatalf("decode preview response: %v", err)
	}

	body = bytes.NewBufferString(`{"previewId":"` + preview.PreviewID + `"}`)
	req = httptest.NewRequest(http.MethodPost, "/api/orders/preview-confirm", body)
	req.Header.Set("Authorization", "Bearer "+salesToken)
	res = httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusCreated {
		t.Fatalf("confirm preview failed: %d %s", res.Code, res.Body.String())
	}
}

func TestSalesCanConfirmDraftPreviewWhenExistingOrdersFillStartDate(t *testing.T) {
	server := NewServer("secret", NewMemoryStore())
	salesToken := login(t, server, "sales", "demo")
	for range 4 {
		createOrderWithPriorityAndDue(t, server, salesToken, "A", "low", "2026-05-22")
	}
	schedulerA := login(t, server, "scheduler-a", "demo")
	createScheduleJob(t, server, schedulerA, "A")

	body := bytes.NewBufferString(`{"lineId":"A","startDate":"2026-05-22","currentDate":"2026-05-21","draftOrder":{"customer":"New Draft","lineId":"A","quantity":500,"priority":"low","dueDate":"2026-05-22"}}`)
	req := httptest.NewRequest(http.MethodPost, "/api/schedules/preview", body)
	req.Header.Set("Authorization", "Bearer "+salesToken)
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("preview failed: %d %s", res.Code, res.Body.String())
	}
	var preview struct {
		PreviewID string `json:"previewId"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &preview); err != nil {
		t.Fatalf("decode preview response: %v", err)
	}

	body = bytes.NewBufferString(`{"previewId":"` + preview.PreviewID + `"}`)
	req = httptest.NewRequest(http.MethodPost, "/api/orders/preview-confirm", body)
	req.Header.Set("Authorization", "Bearer "+salesToken)
	res = httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusCreated {
		t.Fatalf("confirm preview failed: %d %s", res.Code, res.Body.String())
	}
}

func getSalesDraftPreview(t *testing.T, server *Server, token string, bodyStr string) (string, []string) {
	req := httptest.NewRequest(http.MethodPost, "/api/schedules/preview", bytes.NewBufferString(bodyStr))
	req.Header.Set("Authorization", "Bearer "+token)
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("preview failed: %d %s", res.Code, res.Body.String())
	}
	var preview struct {
		PreviewID string `json:"previewId"`
		Conflicts []struct {
			OrderID string `json:"orderId"`
		} `json:"conflicts"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &preview); err != nil {
		t.Fatalf("decode preview response: %v", err)
	}
	conflicts := make([]string, 0, len(preview.Conflicts))
	for _, c := range preview.Conflicts {
		conflicts = append(conflicts, c.OrderID)
	}
	return preview.PreviewID, conflicts
}

func verifyOrderDeferredStatus(t *testing.T, server *Server, token string, deferredID string) {
	req := httptest.NewRequest(http.MethodGet, "/api/orders", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("list orders failed: %d %s", res.Code, res.Body.String())
	}
	var ordersResponse struct {
		Orders []domain.Order `json:"orders"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &ordersResponse); err != nil {
		t.Fatalf("decode orders response: %v", err)
	}
	var deferred domain.Order
	var found bool
	for _, order := range ordersResponse.Orders {
		if order.ID == deferredID {
			deferred = order
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("deferred order %q not found in order list", deferredID)
	}
	if deferred.Status != domain.StatusRejected || deferred.RejectionReason != salesConflictDeferredReason {
		t.Fatalf("expected deferred order moved to sales follow-up, got %+v", deferred)
	}
}

func verifyOrderNotInCalendarPending(t *testing.T, server *Server, token string, deferredID string) {
	req := httptest.NewRequest(http.MethodGet, "/api/schedules/calendar?lineId=A&month=2026-05", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("calendar failed: %d %s", res.Code, res.Body.String())
	}
	var calendar calendarResponse
	if err := json.Unmarshal(res.Body.Bytes(), &calendar); err != nil {
		t.Fatalf("decode calendar response: %v", err)
	}
	for _, allocation := range calendar.PendingAllocations {
		if allocation.OrderID == deferredID {
			t.Fatalf("deferred order should not remain in pending calendar: %+v", allocation)
		}
	}
}

func TestSalesConfirmDraftPreviewDefersConflictedPendingOrder(t *testing.T) {
	server := NewServer("secret", NewMemoryStore())
	salesToken := login(t, server, "sales", "demo")
	conflictedOrderIDs := []string{}
	for range 4 {
		conflictedOrderIDs = append(conflictedOrderIDs, createOrderWithPriorityAndDue(t, server, salesToken, "A", "low", "2026-05-02"))
	}

	bodyStr := `{"lineId":"A","startDate":"2026-05-02","currentDate":"2026-05-01","draftOrder":{"customer":"Priority Draft","lineId":"A","quantity":2500,"priority":"high","dueDate":"2026-05-02"}}`
	previewID, conflicts := getSalesDraftPreview(t, server, salesToken, bodyStr)
	if len(conflicts) == 0 {
		t.Fatalf("expected draft preview conflict, got 0 conflicts")
	}
	deferredID := conflicts[0]
	if deferredID == previewDraftOrderID || !slicesContains(conflictedOrderIDs, deferredID) {
		t.Fatalf("expected one existing pending order conflict, got %q", deferredID)
	}

	confirmBody := bytes.NewBufferString(`{"previewId":"` + previewID + `","deferredOrderIds":["` + deferredID + `"]}`)
	req := httptest.NewRequest(http.MethodPost, "/api/orders/preview-confirm", confirmBody)
	req.Header.Set("Authorization", "Bearer "+salesToken)
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusCreated {
		t.Fatalf("confirm preview failed: %d %s", res.Code, res.Body.String())
	}

	verifyOrderDeferredStatus(t, server, salesToken, deferredID)
	verifyOrderNotInCalendarPending(t, server, salesToken, deferredID)
}

func TestSalesDraftPreviewRejectsTodayDueDate(t *testing.T) {
	server := NewServer("secret", NewMemoryStore())
	salesToken := login(t, server, "sales", "demo")
	body := bytes.NewBufferString(`{"lineId":"A","startDate":"2026-05-01","currentDate":"2026-04-30","draftOrder":{"customer":"Draft Co","lineId":"A","quantity":2500,"priority":"low","dueDate":"2026-04-30"}}`)
	req := httptest.NewRequest(http.MethodPost, "/api/schedules/preview", body)
	req.Header.Set("Authorization", "Bearer "+salesToken)
	res := httptest.NewRecorder()

	server.ServeHTTP(res, req)

	if res.Code != http.StatusBadRequest || !strings.Contains(res.Body.String(), unacceptableDueDateMessage) {
		t.Fatalf("expected unacceptable due date rejection, got %d body=%s", res.Code, res.Body.String())
	}
}

func TestSalesDraftPreviewRejectsPastDueDate(t *testing.T) {
	server := NewServer("secret", NewMemoryStore())
	salesToken := login(t, server, "sales", "demo")
	body := bytes.NewBufferString(`{"lineId":"A","startDate":"2026-05-01","currentDate":"2026-04-30","draftOrder":{"customer":"Draft Co","lineId":"A","quantity":2500,"priority":"high","dueDate":"2026-04-29"}}`)
	req := httptest.NewRequest(http.MethodPost, "/api/schedules/preview", body)
	req.Header.Set("Authorization", "Bearer "+salesToken)
	res := httptest.NewRecorder()

	server.ServeHTTP(res, req)

	if res.Code != http.StatusBadRequest || !strings.Contains(res.Body.String(), unacceptableDueDateMessage) {
		t.Fatalf("expected unacceptable due date rejection, got %d body=%s", res.Code, res.Body.String())
	}
}

func TestSalesDraftPreviewAcceptsTomorrowDueDate(t *testing.T) {
	server := NewServer("secret", NewMemoryStore())
	salesToken := login(t, server, "sales", "demo")
	body := bytes.NewBufferString(`{"lineId":"A","startDate":"2026-05-01","currentDate":"2026-04-30","draftOrder":{"customer":"Draft Co","lineId":"A","quantity":2500,"priority":"low","dueDate":"2026-05-01"}}`)
	req := httptest.NewRequest(http.MethodPost, "/api/schedules/preview", body)
	req.Header.Set("Authorization", "Bearer "+salesToken)
	res := httptest.NewRecorder()

	server.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("expected draft preview, got %d body=%s", res.Code, res.Body.String())
	}
}

func TestSalesDraftPreviewIncludesPendingOrdersAsPreviewAllocations(t *testing.T) {
	server := NewServer("secret", NewMemoryStore())
	salesToken := login(t, server, "sales", "demo")
	for range 4 {
		createOrderWithPriorityAndDue(t, server, salesToken, "A", "low", "2026-05-03")
	}

	body := bytes.NewBufferString(`{"lineId":"A","startDate":"2026-05-01","currentDate":"2026-04-30","draftOrder":{"customer":"Draft Co","lineId":"A","quantity":2500,"priority":"low","dueDate":"2026-05-03"}}`)
	req := httptest.NewRequest(http.MethodPost, "/api/schedules/preview", body)
	req.Header.Set("Authorization", "Bearer "+salesToken)
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("preview failed: %d %s", res.Code, res.Body.String())
	}
	var payload struct {
		Allocations []struct {
			OrderID  string             `json:"orderId"`
			Customer string             `json:"customer"`
			Date     string             `json:"date"`
			Status   domain.OrderStatus `json:"status"`
		} `json:"allocations"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode preview response: %v", err)
	}
	if len(payload.Allocations) != 5 {
		t.Fatalf("expected existing pending orders plus draft allocation, got %+v", payload.Allocations)
	}
	pendingOnStartDate := 0
	for _, allocation := range payload.Allocations {
		if allocation.Status != domain.StatusPending {
			t.Fatalf("expected pending preview allocation status, got %+v", allocation)
		}
		if strings.HasPrefix(allocation.Date, "2026-05-01") {
			pendingOnStartDate++
		}
	}
	if pendingOnStartDate != 4 {
		t.Fatalf("expected pending backlog to fill start date capacity, got %+v", payload.Allocations)
	}
	draftOnSecondDay := false
	for _, allocation := range payload.Allocations {
		if allocation.OrderID == previewDraftOrderID && strings.HasPrefix(allocation.Date, "2026-05-02") {
			draftOnSecondDay = true
		}
	}
	if !draftOnSecondDay {
		t.Fatalf("expected draft allocation after pending backlog capacity, got %+v", payload.Allocations)
	}
	if payload.Allocations[len(payload.Allocations)-1].Customer != "Draft Co" {
		t.Fatalf("expected draft customer in preview allocation, got %+v", payload.Allocations)
	}
}

func TestSchedulerPreviewKeepsUnselectedPendingOrdersOutOfCapacity(t *testing.T) {
	server := NewServer("secret", NewMemoryStore())
	salesToken := login(t, server, "sales", "demo")
	for range 4 {
		createOrderWithPriorityAndDue(t, server, salesToken, "A", "low", "2026-05-03")
	}
	selectedOrderID := createOrderWithPriorityAndDue(t, server, salesToken, "A", "low", "2026-05-03")
	schedulerA := login(t, server, "scheduler-a", "demo")

	body := bytes.NewBufferString(`{"lineId":"A","startDate":"2026-05-01","currentDate":"2026-04-30","orderIds":["` + selectedOrderID + `"]}`)
	req := httptest.NewRequest(http.MethodPost, "/api/schedules/preview", body)
	req.Header.Set("Authorization", "Bearer "+schedulerA)
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("scheduler preview failed: %d %s", res.Code, res.Body.String())
	}
	var payload struct {
		Allocations []struct {
			OrderID string `json:"orderId"`
			Date    string `json:"date"`
		} `json:"allocations"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode scheduler preview response: %v", err)
	}
	if len(payload.Allocations) != 1 || payload.Allocations[0].OrderID != selectedOrderID || !strings.HasPrefix(payload.Allocations[0].Date, "2026-05-01") {
		t.Fatalf("scheduler preview should ignore unselected pending capacity, got %+v", payload.Allocations)
	}
}

func TestScheduleCalendarOrdersSameDayByPriorityDueDateAndCreatedTimestamp(t *testing.T) {
	store := NewMemoryStore()
	lineID := "A"
	allocationDate := mustAPIDate(t, "2026-05-10")
	dueDate := mustAPIDate(t, "2026-05-30")
	store.orders["ORD-A"] = domain.Order{ID: "ORD-A", Customer: "ACME", LineID: lineID, Quantity: 100, Priority: domain.PriorityLow, Status: domain.StatusScheduled, DueDate: dueDate, CreatedAt: time.Unix(1772271713, 0).UTC()}
	store.orders["ORD-B"] = domain.Order{ID: "ORD-B", Customer: "Beta", LineID: lineID, Quantity: 100, Priority: domain.PriorityLow, Status: domain.StatusScheduled, DueDate: dueDate, CreatedAt: time.Unix(1772271715, 0).UTC()}
	store.orders["ORD-HIGH"] = domain.Order{ID: "ORD-HIGH", Customer: "Core", LineID: lineID, Quantity: 100, Priority: domain.PriorityHigh, Status: domain.StatusScheduled, DueDate: mustAPIDate(t, "2026-06-01"), CreatedAt: time.Unix(1772271719, 0).UTC()}
	store.allocations = append(store.allocations,
		domain.ScheduleAllocation{OrderID: "ORD-B", LineID: lineID, Date: allocationDate, Quantity: 100, Priority: domain.PriorityLow, Status: domain.StatusScheduled},
		domain.ScheduleAllocation{OrderID: "ORD-HIGH", LineID: lineID, Date: allocationDate, Quantity: 100, Priority: domain.PriorityHigh, Status: domain.StatusScheduled},
		domain.ScheduleAllocation{OrderID: "ORD-A", LineID: lineID, Date: allocationDate, Quantity: 100, Priority: domain.PriorityLow, Status: domain.StatusScheduled},
	)

	calendar, err := store.ScheduleCalendar(lineID, "2026-05", auth.Claims{Subject: "admin", Role: domain.RoleAdmin})
	if err != nil {
		t.Fatalf("ScheduleCalendar failed: %v", err)
	}

	if got := []string{calendar.Allocations[0].OrderID, calendar.Allocations[1].OrderID, calendar.Allocations[2].OrderID}; !reflect.DeepEqual(got, []string{"ORD-HIGH", "ORD-A", "ORD-B"}) {
		t.Fatalf("unexpected calendar order: %+v", got)
	}
	if calendar.Allocations[1].CreatedAtTimestamp != 1772271713000 || calendar.Allocations[2].CreatedAtTimestamp != 1772271715000 {
		t.Fatalf("expected unix millisecond created timestamps, got %+v", calendar.Allocations)
	}
}

func TestScheduleCalendarSeparatesPersistedAndPendingPreviewAllocations(t *testing.T) {
	server := NewServer("secret", NewMemoryStore())
	salesToken := login(t, server, "sales", "demo")
	pendingOrderID := createOrderWithPriorityAndDue(t, server, salesToken, "A", "low", "2026-06-03")

	req := httptest.NewRequest(http.MethodGet, "/api/schedules/calendar?lineId=A&month=2026-05", nil)
	req.Header.Set("Authorization", "Bearer "+salesToken)
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("calendar failed: %d %s", res.Code, res.Body.String())
	}
	var payload struct {
		Allocations        []calendarAllocation `json:"allocations"`
		PendingAllocations []calendarAllocation `json:"pendingAllocations"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode calendar response: %v", err)
	}
	if len(payload.Allocations) != 0 {
		t.Fatalf("pending preview allocations should not affect monthly calendar, got %+v", payload.Allocations)
	}
	if len(payload.PendingAllocations) == 0 || payload.PendingAllocations[0].OrderID != pendingOrderID || payload.PendingAllocations[0].Status != domain.StatusPending {
		t.Fatalf("expected pending backlog preview allocation to be separate, got %+v", payload.PendingAllocations)
	}
}

func TestSalesDraftPreviewReportsPendingOrderConflictCausedByDraft(t *testing.T) {
	store := NewMemoryStore()
	server := NewServer("secret", store)
	salesToken := login(t, server, "sales", "demo")
	store.allocations = append(store.allocations, domain.ScheduleAllocation{
		OrderID:  "EXISTING-CAPACITY",
		LineID:   "A",
		Date:     mustAPIDate(t, "2026-05-01"),
		Quantity: 7500,
		Priority: domain.PriorityLow,
		Status:   domain.StatusScheduled,
	})
	pendingOrderID := createOrderWithPriorityAndDue(t, server, salesToken, "A", "low", "2026-05-01")

	body := bytes.NewBufferString(`{"lineId":"A","startDate":"2026-05-01","currentDate":"2026-04-30","draftOrder":{"customer":"Rush Draft","lineId":"A","quantity":2500,"priority":"high","dueDate":"2026-05-02"}}`)
	req := httptest.NewRequest(http.MethodPost, "/api/schedules/preview", body)
	req.Header.Set("Authorization", "Bearer "+salesToken)
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("preview failed: %d %s", res.Code, res.Body.String())
	}
	var payload struct {
		Conflicts []struct {
			OrderID            string `json:"orderId"`
			Reason             string `json:"reason"`
			EarliestFinishDate string `json:"earliestFinishDate"`
		} `json:"conflicts"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode preview response: %v", err)
	}
	if len(payload.Conflicts) != 1 {
		t.Fatalf("expected one pending order conflict caused by draft, got %+v", payload.Conflicts)
	}
	if payload.Conflicts[0].OrderID != pendingOrderID || payload.Conflicts[0].Reason != "capacity cannot satisfy order before due date" || !strings.HasPrefix(payload.Conflicts[0].EarliestFinishDate, "2026-05-02") {
		t.Fatalf("unexpected draft-caused pending conflict: %+v", payload.Conflicts)
	}
}

func TestSalesDraftPreviewReturnsSuccessfulAllocationsWhenDraftDisplacesPendingOrder(t *testing.T) {
	store := NewMemoryStore()
	server := NewServer("secret", store)
	salesToken := login(t, server, "sales", "demo")
	lineID := "A"
	dueDate := mustAPIDate(t, "2026-05-30")
	store.orders["ORD-A"] = domain.Order{ID: "ORD-A", Customer: "A", LineID: lineID, Quantity: 2500, Priority: domain.PriorityHigh, Status: domain.StatusPending, DueDate: dueDate, CreatedAt: time.UnixMilli(1772271711000).UTC()}
	store.orders["ORD-B"] = domain.Order{ID: "ORD-B", Customer: "B", LineID: lineID, Quantity: 2500, Priority: domain.PriorityHigh, Status: domain.StatusPending, DueDate: dueDate, CreatedAt: time.UnixMilli(1772271712000).UTC()}
	store.orders["ORD-C"] = domain.Order{ID: "ORD-C", Customer: "C", LineID: lineID, Quantity: 2500, Priority: domain.PriorityLow, Status: domain.StatusPending, DueDate: dueDate, CreatedAt: time.UnixMilli(1772271713000).UTC()}
	store.orders["ORD-D"] = domain.Order{ID: "ORD-D", Customer: "D", LineID: lineID, Quantity: 2500, Priority: domain.PriorityLow, Status: domain.StatusPending, DueDate: dueDate, CreatedAt: time.UnixMilli(1772271714000).UTC()}

	body := bytes.NewBufferString(`{"lineId":"A","startDate":"2026-05-30","currentDate":"2026-05-29","draftOrder":{"customer":"E","lineId":"A","quantity":2500,"priority":"high","dueDate":"2026-05-30"}}`)
	req := httptest.NewRequest(http.MethodPost, "/api/schedules/preview", body)
	req.Header.Set("Authorization", "Bearer "+salesToken)
	res := httptest.NewRecorder()

	server.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("preview failed: %d %s", res.Code, res.Body.String())
	}
	var preview schedulePreviewResponse
	if err := json.Unmarshal(res.Body.Bytes(), &preview); err != nil {
		t.Fatalf("decode preview response: %v", err)
	}
	got := make([]string, 0, len(preview.Allocations))
	for _, allocation := range preview.Allocations {
		got = append(got, allocation.OrderID)
	}
	if !reflect.DeepEqual(got, []string{"ORD-A", "ORD-B", previewDraftOrderID, "ORD-C"}) {
		t.Fatalf("expected successful draft allocations A, B, draft, C; got %+v", got)
	}
	if len(preview.Conflicts) != 1 || preview.Conflicts[0].OrderID != "ORD-D" {
		t.Fatalf("expected ORD-D conflict, got %+v", preview.Conflicts)
	}
}

func TestManualForceConflictCanCreateScheduleJobWithAudit(t *testing.T) {
	store := NewMemoryStore()
	server := NewServer("secret", store)
	salesToken := login(t, server, "sales", "demo")
	createOrderWithPriority(t, server, salesToken, "A", "low")
	schedulerA := login(t, server, "scheduler-a", "demo")
	createScheduleJob(t, server, schedulerA, "A")
	createOrderWithPriority(t, server, salesToken, "A", "high")

	body := bytes.NewBufferString(`{"lineId":"A","startDate":"2026-05-01","currentDate":"2026-04-30","orderIds":["ORD-0000002"],"manualForce":true,"reason":"customer escalation approved"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/schedules/preview", body)
	req.Header.Set("Authorization", "Bearer "+schedulerA)
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("manual preview failed: %d %s", res.Code, res.Body.String())
	}
	var preview struct {
		PreviewID string `json:"previewId"`
		Conflicts []struct {
			AffectedOrderIDs []string `json:"affectedOrderIds"`
		} `json:"conflicts"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &preview); err != nil {
		t.Fatalf("decode preview response: %v", err)
	}
	if len(preview.Conflicts) == 0 || len(preview.Conflicts[0].AffectedOrderIDs) == 0 {
		t.Fatalf("expected manual conflict with affected orders, got %+v", preview.Conflicts)
	}

	body = bytes.NewBufferString(`{"lineId":"A","startDate":"2026-05-01","currentDate":"2026-04-30","orderIds":["ORD-0000002"],"manualForce":true,"reason":"customer escalation approved","previewId":"` + preview.PreviewID + `"}`)
	req = httptest.NewRequest(http.MethodPost, "/api/schedules/jobs", body)
	req.Header.Set("Authorization", "Bearer "+schedulerA)
	res = httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusAccepted {
		t.Fatalf("manual job failed: %d %s", res.Code, res.Body.String())
	}
	var job domain.ScheduleJob
	if err := json.Unmarshal(res.Body.Bytes(), &job); err != nil {
		t.Fatalf("decode job response: %v", err)
	}
	if job.Status != domain.JobCompleted {
		t.Fatalf("expected completed manual job, got %+v", job)
	}
	foundAudit := false
	for _, audit := range store.audits {
		if audit.Action == "schedule.job.manual_force" && audit.Reason == "customer escalation approved" {
			foundAudit = true
		}
	}
	if !foundAudit {
		t.Fatalf("expected manual force audit, got %+v", store.audits)
	}
}

func TestConflictSolutionCanMoveScheduledLowPriorityOrder(t *testing.T) {
	store := NewMemoryStore()
	server := NewServer("secret", store)
	salesToken := login(t, server, "sales", "demo")
	for index := 0; index < 4; index++ {
		createOrderWithPriorityAndDue(t, server, salesToken, "A", "low", "2026-05-01")
	}
	schedulerA := login(t, server, "scheduler-a", "demo")
	createScheduleJob(t, server, schedulerA, "A")
	newOrderID := createOrderWithQuantityPriorityAndDue(t, server, salesToken, "A", 500, "high", "2026-05-01")

	body := bytes.NewBufferString(`{"lineId":"A","startDate":"2026-05-01","currentDate":"2026-04-30","orderIds":["` + newOrderID + `"]}`)
	req := httptest.NewRequest(http.MethodPost, "/api/schedules/preview", body)
	req.Header.Set("Authorization", "Bearer "+schedulerA)
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("conflict preview failed: %d %s", res.Code, res.Body.String())
	}
	var conflictPreview struct {
		Conflicts []struct {
			AffectedOrderIDs []string `json:"affectedOrderIds"`
		} `json:"conflicts"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &conflictPreview); err != nil {
		t.Fatalf("decode conflict preview: %v", err)
	}
	if len(conflictPreview.Conflicts) != 1 || len(conflictPreview.Conflicts[0].AffectedOrderIDs) == 0 {
		t.Fatalf("expected affected movable scheduled orders, got %+v", conflictPreview.Conflicts)
	}
	movableOrderID := conflictPreview.Conflicts[0].AffectedOrderIDs[0]

	body = bytes.NewBufferString(`{"lineId":"A","startDate":"2026-05-01","currentDate":"2026-04-30","orderIds":["` + newOrderID + `"],"resolutionOrderIds":["` + movableOrderID + `"],"allowLateCompletion":true}`)
	req = httptest.NewRequest(http.MethodPost, "/api/schedules/preview", body)
	req.Header.Set("Authorization", "Bearer "+schedulerA)
	res = httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("solution preview failed: %d %s", res.Code, res.Body.String())
	}
	var solutionPreview struct {
		PreviewID   string `json:"previewId"`
		Conflicts   []any  `json:"conflicts"`
		Allocations []struct {
			OrderID string `json:"orderId"`
			Date    string `json:"date"`
		} `json:"allocations"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &solutionPreview); err != nil {
		t.Fatalf("decode solution preview: %v", err)
	}
	if len(solutionPreview.Conflicts) != 0 {
		t.Fatalf("expected conflict-free solution preview, got %+v", solutionPreview.Conflicts)
	}
	splitOrderID := movableOrderID + "-1"
	if !hasAllocationOnDate(solutionPreview.Allocations, newOrderID, "2026-05-01") || !hasAllocationOnDate(solutionPreview.Allocations, movableOrderID, "2026-05-01") || !hasAllocationOnDate(solutionPreview.Allocations, splitOrderID, "2026-05-02") {
		t.Fatalf("expected high priority order on due date and split low priority order across two independent IDs, got %+v", solutionPreview.Allocations)
	}

	body = bytes.NewBufferString(`{"lineId":"A","startDate":"2026-05-01","currentDate":"2026-04-30","orderIds":["` + newOrderID + `"],"resolutionOrderIds":["` + movableOrderID + `"],"allowLateCompletion":true,"previewId":"` + solutionPreview.PreviewID + `"}`)
	req = httptest.NewRequest(http.MethodPost, "/api/schedules/jobs", body)
	req.Header.Set("Authorization", "Bearer "+schedulerA)
	res = httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusAccepted {
		t.Fatalf("solution job failed: %d %s", res.Code, res.Body.String())
	}
	var job domain.ScheduleJob
	if err := json.Unmarshal(res.Body.Bytes(), &job); err != nil {
		t.Fatalf("decode solution job: %v", err)
	}
	if job.Status != domain.JobCompleted {
		t.Fatalf("expected completed solution job, got %+v", job)
	}
	if allocationCountForOrder(store.allocations, movableOrderID) != 1 || allocationCountForOrder(store.allocations, splitOrderID) != 1 {
		t.Fatalf("expected moved order split allocations to use independent IDs, got %+v", store.allocations)
	}
	if store.orders[movableOrderID].Quantity != 2000 || store.orders[splitOrderID].Quantity != 500 || store.orders[splitOrderID].SourceOrder != movableOrderID {
		t.Fatalf("expected independent split order records, source=%+v split=%+v", store.orders[movableOrderID], store.orders[splitOrderID])
	}
}

func TestCancelOrdersRemovesScheduledAllocation(t *testing.T) {
	server := NewServer("secret", NewMemoryStore())
	salesToken := login(t, server, "sales", "demo")
	createOrder(t, server, salesToken, "A")
	schedulerA := login(t, server, "scheduler-a", "demo")
	createScheduleJob(t, server, schedulerA, "A")

	body := bytes.NewBufferString(`{"orderIds":["ORD-0000001"]}`)
	req := httptest.NewRequest(http.MethodDelete, "/api/orders", body)
	req.Header.Set("Authorization", "Bearer "+schedulerA)
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("cancel failed: %d %s", res.Code, res.Body.String())
	}
	if storeOrder, ok := server.store.(*MemoryStore).orders["ORD-0000001"]; ok {
		if storeOrder.Status != domain.StatusCancelled {
			t.Fatalf("expected cancelled order, got %+v", storeOrder)
		}
	}

	req = httptest.NewRequest(http.MethodGet, "/api/schedules/calendar?lineId=A&month=2026-05", nil)
	req.Header.Set("Authorization", "Bearer "+schedulerA)
	res = httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("calendar failed: %d %s", res.Code, res.Body.String())
	}
	var payload struct {
		Allocations []any `json:"allocations"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode calendar response: %v", err)
	}
	if len(payload.Allocations) != 0 {
		t.Fatalf("expected allocation removed, got %+v", payload.Allocations)
	}
}

func TestSchedulerCanUpdatePendingOrderDueDate(t *testing.T) {
	server := NewServer("secret", NewMemoryStore())
	salesToken := login(t, server, "sales", "demo")
	createOrder(t, server, salesToken, "A")
	schedulerA := login(t, server, "scheduler-a", "demo")

	body := bytes.NewBufferString(`{"dueDate":"2026-05-06"}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/orders/ORD-0000001", body)
	req.Header.Set("Authorization", "Bearer "+schedulerA)
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("update due date failed: %d %s", res.Code, res.Body.String())
	}
	var order domain.Order
	if err := json.Unmarshal(res.Body.Bytes(), &order); err != nil {
		t.Fatalf("decode order response: %v", err)
	}
	if order.DueDate.Format("2006-01-02") != "2026-05-06" {
		t.Fatalf("expected updated due date, got %s", order.DueDate)
	}
}

func TestUpdateOrderDueDateRejectsTodayOrPast(t *testing.T) {
	server := NewServer("secret", NewMemoryStore())
	salesToken := login(t, server, "sales", "demo")
	createOrder(t, server, salesToken, "A")
	schedulerA := login(t, server, "scheduler-a", "demo")

	for _, dueDate := range []string{"2026-04-30", "2026-04-29"} {
		body := bytes.NewBufferString(`{"dueDate":"` + dueDate + `"}`)
		req := httptest.NewRequest(http.MethodPatch, "/api/orders/ORD-0000001", body)
		req.Header.Set("Authorization", "Bearer "+schedulerA)
		res := httptest.NewRecorder()
		server.ServeHTTP(res, req)
		if res.Code != http.StatusBadRequest || !strings.Contains(res.Body.String(), unacceptableDueDateMessage) {
			t.Fatalf("expected unacceptable due date rejection for %s, got %d body=%s", dueDate, res.Code, res.Body.String())
		}
	}
}

func TestUpdateOrderDueDateAcceptsFuture(t *testing.T) {
	server := NewServer("secret", NewMemoryStore())
	salesToken := login(t, server, "sales", "demo")
	createOrder(t, server, salesToken, "A")

	body := bytes.NewBufferString(`{"dueDate":"2026-05-01"}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/orders/ORD-0000001", body)
	req.Header.Set("Authorization", "Bearer "+salesToken)
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("expected due date update, got %d body=%s", res.Code, res.Body.String())
	}
}

func TestOrderNoteCannotBeUpdatedAfterCreate(t *testing.T) {
	store := NewMemoryStore()
	server := NewServer("secret", store)
	salesToken := login(t, server, "sales", "demo")
	body := bytes.NewBufferString(`{"customer":"ACME","lineId":"A","quantity":2500,"priority":"low","dueDate":"2026-05-03","note":"original sales note"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/orders", body)
	req.Header.Set("Authorization", "Bearer "+salesToken)
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusCreated {
		t.Fatalf("create order failed: %d %s", res.Code, res.Body.String())
	}

	schedulerA := login(t, server, "scheduler-a", "demo")
	body = bytes.NewBufferString(`{"dueDate":"2026-05-06","note":"scheduler changed note"}`)
	req = httptest.NewRequest(http.MethodPatch, "/api/orders/ORD-0000001", body)
	req.Header.Set("Authorization", "Bearer "+schedulerA)
	res = httptest.NewRecorder()
	server.ServeHTTP(res, req)

	if res.Code != http.StatusBadRequest {
		t.Fatalf("expected note update rejection, got %d body=%s", res.Code, res.Body.String())
	}
	if store.orders["ORD-0000001"].Note != "original sales note" || store.orders["ORD-0000001"].DueDate.Format("2006-01-02") != "2026-05-03" {
		t.Fatalf("order should remain unchanged, got %+v", store.orders["ORD-0000001"])
	}
}

func TestStartProductionLocksScheduledAllocations(t *testing.T) {
	store := NewMemoryStore()
	server := NewServer("secret", store)
	salesToken := login(t, server, "sales", "demo")
	createOrder(t, server, salesToken, "A")
	schedulerA := login(t, server, "scheduler-a", "demo")
	createScheduleJob(t, server, schedulerA, "A")

	body := bytes.NewBufferString(`{"orderId":"ORD-0000001"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/production/start", body)
	req.Header.Set("Authorization", "Bearer "+schedulerA)
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("start production failed: %d %s", res.Code, res.Body.String())
	}
	if store.orders["ORD-0000001"].Status != domain.StatusInProgress {
		t.Fatalf("expected in-progress status, got %+v", store.orders["ORD-0000001"])
	}
	if len(store.allocations) != 1 || !store.allocations[0].Locked {
		t.Fatalf("expected locked allocation, got %+v", store.allocations)
	}
}

func TestPartialProductionReturnsRemainderToPendingQueue(t *testing.T) {
	store := NewMemoryStore()
	server := NewServer("secret", store)
	salesToken := login(t, server, "sales", "demo")
	createOrder(t, server, salesToken, "A")
	schedulerA := login(t, server, "scheduler-a", "demo")
	createScheduleJob(t, server, schedulerA, "A")

	store.mu.Lock()
	store.allocations = []domain.ScheduleAllocation{
		{
			OrderID:  "ORD-0000001",
			LineID:   "A",
			Date:     mustAPIDate(t, "2026-05-01"),
			Quantity: 900,
			Priority: domain.PriorityLow,
		},
		{
			OrderID:  "ORD-0000001",
			LineID:   "A",
			Date:     mustAPIDate(t, "2026-05-02"),
			Quantity: 1600,
			Priority: domain.PriorityLow,
		},
	}
	store.mu.Unlock()

	startProduction(t, server, schedulerA, "ORD-0000001")

	body := bytes.NewBufferString(`{"orderId":"ORD-0000001","productionDate":"2026-05-01","producedQuantity":800}`)
	req := httptest.NewRequest(http.MethodPost, "/api/production/confirm", body)
	req.Header.Set("Authorization", "Bearer "+schedulerA)
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("confirm production failed: %d %s", res.Code, res.Body.String())
	}
	var payload productionConfirmResponse
	if err := json.Unmarshal(res.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode production response: %v", err)
	}
	if payload.Order.ID != "ORD-0000001" || payload.Order.Status != domain.StatusCompleted || payload.Order.Quantity != 800 {
		t.Fatalf("expected original order to be completed with produced quantity, got %+v", payload.Order)
	}
	if payload.Remainder == nil || payload.Remainder.ID != "ORD-0000001-1" || payload.Remainder.Quantity != 1700 || payload.Remainder.Status != domain.StatusPending || payload.Remainder.SourceOrder != "ORD-0000001" {
		t.Fatalf("unexpected remainder: %+v", payload.Remainder)
	}
	if store.orders["ORD-0000001-1"].Quantity != 1700 || store.orders["ORD-0000001-1"].Status != domain.StatusPending {
		t.Fatalf("expected independent pending remainder order, got %+v", store.orders["ORD-0000001-1"])
	}
	if len(store.allocations) != 1 {
		t.Fatalf("expected partial production to keep one completed allocation, got %+v", store.allocations)
	}
	if store.allocations[0].OrderID != "ORD-0000001" || store.allocations[0].Quantity != 900 || store.allocations[0].Status != domain.StatusCompleted || !store.allocations[0].Date.Equal(mustAPIDate(t, "2026-05-01")) {
		t.Fatalf("expected completed May 1 allocation to keep scheduled quantity, got %+v", store.allocations[0])
	}

	req = httptest.NewRequest(http.MethodGet, "/api/schedules/calendar?lineId=A&month=2026-05", nil)
	req.Header.Set("Authorization", "Bearer "+schedulerA)
	res = httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("calendar failed: %d %s", res.Code, res.Body.String())
	}
	var calendar calendarResponse
	if err := json.Unmarshal(res.Body.Bytes(), &calendar); err != nil {
		t.Fatalf("decode calendar response: %v", err)
	}
	if len(calendar.Allocations) != 1 || calendar.Allocations[0].Quantity != 900 || calendar.Allocations[0].CompletedQuantity != 800 {
		t.Fatalf("expected calendar to keep scheduled quantity and expose completed quantity, got %+v", calendar.Allocations)
	}

}

func TestPartialProductionDoesNotFreeScheduledCapacity(t *testing.T) {
	store := NewMemoryStore()
	server := NewServer("secret", store)
	salesToken := login(t, server, "sales", "demo")
	for range 4 {
		createOrder(t, server, salesToken, "A")
	}
	schedulerA := login(t, server, "scheduler-a", "demo")
	createScheduleJob(t, server, schedulerA, "A")
	startProduction(t, server, schedulerA, "ORD-0000001")

	body := bytes.NewBufferString(`{"orderId":"ORD-0000001","productionDate":"2026-05-01","producedQuantity":2000}`)
	req := httptest.NewRequest(http.MethodPost, "/api/production/confirm", body)
	req.Header.Set("Authorization", "Bearer "+schedulerA)
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("confirm production failed: %d %s", res.Code, res.Body.String())
	}

	body = bytes.NewBufferString(`{"lineId":"A","startDate":"2026-05-01","currentDate":"2026-04-30","orderIds":["ORD-0000001-1"]}`)
	req = httptest.NewRequest(http.MethodPost, "/api/schedules/preview", body)
	req.Header.Set("Authorization", "Bearer "+schedulerA)
	res = httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("preview remainder failed: %d %s", res.Code, res.Body.String())
	}
	var preview schedulePreviewResponse
	if err := json.Unmarshal(res.Body.Bytes(), &preview); err != nil {
		t.Fatalf("decode preview response: %v", err)
	}
	if len(preview.Allocations) == 0 {
		t.Fatalf("expected remainder allocation, got none")
	}
	if got := preview.Allocations[0].Date.Format(dateLayout); got == "2026-05-01" {
		t.Fatalf("expected full scheduled capacity on 2026-05-01 to stay consumed, got remainder allocation on %s", got)
	}
}

func TestRemainderOrderIDIncrementsExistingSuffix(t *testing.T) {
	existing := map[string]bool{
		"ORD-0000001-2": true,
	}
	got := nextRemainderOrderID("ORD-0000001-1", true, func(id string) bool {
		return existing[id]
	})
	if got != "ORD-0000001-3" {
		t.Fatalf("expected next suffixed remainder ID, got %s", got)
	}
}

func TestProductionConfirmRejectsQuantityAboveOrderTotal(t *testing.T) {
	store := NewMemoryStore()
	server := NewServer("secret", store)
	salesToken := login(t, server, "sales", "demo")
	createOrder(t, server, salesToken, "A")
	schedulerA := login(t, server, "scheduler-a", "demo")
	createScheduleJob(t, server, schedulerA, "A")
	startProduction(t, server, schedulerA, "ORD-0000001")

	body := bytes.NewBufferString(`{"orderId":"ORD-0000001","productionDate":"2026-05-01","producedQuantity":2501}`)
	req := httptest.NewRequest(http.MethodPost, "/api/production/confirm", body)
	req.Header.Set("Authorization", "Bearer "+schedulerA)
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("expected bad request, got %d %s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), "完成片數不能超過本日排程量") {
		t.Fatalf("expected clear quantity error, got %s", res.Body.String())
	}
}

func TestSchedulerRejectsPendingOrdersAndSalesCanResubmit(t *testing.T) {
	store := NewMemoryStore()
	server := NewServer("secret", store)
	salesToken := login(t, server, "sales", "demo")

	body := bytes.NewBufferString(`{"customer":"ACME","lineId":"A","quantity":2500,"priority":"low","dueDate":"2026-05-03","note":"customer can accept split delivery"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/orders", body)
	req.Header.Set("Authorization", "Bearer "+salesToken)
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusCreated {
		t.Fatalf("create order failed: %d %s", res.Code, res.Body.String())
	}

	schedulerA := login(t, server, "scheduler-a", "demo")
	body = bytes.NewBufferString(`{"orderIds":["ORD-0000001"],"reason":"capacity unavailable before due date"}`)
	req = httptest.NewRequest(http.MethodPost, "/api/orders/reject", body)
	req.Header.Set("Authorization", "Bearer "+schedulerA)
	res = httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("reject failed: %d %s", res.Code, res.Body.String())
	}
	if store.orders["ORD-0000001"].Status != domain.StatusRejected || store.orders["ORD-0000001"].RejectionReason == "" {
		t.Fatalf("expected rejected order with reason, got %+v", store.orders["ORD-0000001"])
	}

	req = httptest.NewRequest(http.MethodGet, "/api/orders", nil)
	req.Header.Set("Authorization", "Bearer "+schedulerA)
	res = httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if !strings.Contains(res.Body.String(), "ORD-0000001") || !strings.Contains(res.Body.String(), string(domain.StatusRejected)) {
		t.Fatalf("rejected order should be visible to scheduler status filters: %s", res.Body.String())
	}

	body = bytes.NewBufferString(`{"orderId":"ORD-0000001","dueDate":"2026-05-05","quantity":2000}`)
	req = httptest.NewRequest(http.MethodPost, "/api/orders/resubmit", body)
	req.Header.Set("Authorization", "Bearer "+salesToken)
	res = httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("resubmit failed: %d %s", res.Code, res.Body.String())
	}
	if store.orders["ORD-0000001"].Status != domain.StatusPending || store.orders["ORD-0000001"].RejectionReason != "" {
		t.Fatalf("expected resubmitted pending order, got %+v", store.orders["ORD-0000001"])
	}
	if store.orders["ORD-0000001"].Quantity != 2000 || store.orders["ORD-0000001"].Note != "customer can accept split delivery" {
		t.Fatalf("expected sales edits to persist, got %+v", store.orders["ORD-0000001"])
	}
}

func TestSalesCanResubmitOwnPendingOrder(t *testing.T) {
	store := NewMemoryStore()
	server := NewServer("secret", store)
	salesToken := login(t, server, "sales", "demo")
	createOrder(t, server, salesToken, "A")

	body := bytes.NewBufferString(`{"orderId":"ORD-0000001","dueDate":"2026-05-05","quantity":2000}`)
	req := httptest.NewRequest(http.MethodPost, "/api/orders/resubmit", body)
	req.Header.Set("Authorization", "Bearer "+salesToken)
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("resubmit pending failed: %d %s", res.Code, res.Body.String())
	}
	if store.orders["ORD-0000001"].Status != domain.StatusPending {
		t.Fatalf("expected pending order to stay pending, got %+v", store.orders["ORD-0000001"])
	}
	if store.orders["ORD-0000001"].Quantity != 2000 || store.orders["ORD-0000001"].DueDate.Format(dateLayout) != "2026-05-05" {
		t.Fatalf("expected sales edits to persist, got %+v", store.orders["ORD-0000001"])
	}
}

func TestSalesCanCancelOwnPendingOrder(t *testing.T) {
	store := NewMemoryStore()
	server := NewServer("secret", store)
	salesToken := login(t, server, "sales", "demo")
	createOrder(t, server, salesToken, "A")

	body := bytes.NewBufferString(`{"orderIds":["ORD-0000001"]}`)
	req := httptest.NewRequest(http.MethodDelete, "/api/orders", body)
	req.Header.Set("Authorization", "Bearer "+salesToken)
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("cancel pending failed: %d %s", res.Code, res.Body.String())
	}
	if store.orders["ORD-0000001"].Status != domain.StatusCancelled {
		t.Fatalf("expected pending order to be cancelled, got %+v", store.orders["ORD-0000001"])
	}
}

func TestSalesResubmitRejectsTodayOrPastDueDate(t *testing.T) {
	store := NewMemoryStore()
	server := NewServer("secret", store)
	salesToken := login(t, server, "sales", "demo")
	createOrder(t, server, salesToken, "A")

	schedulerA := login(t, server, "scheduler-a", "demo")
	body := bytes.NewBufferString(`{"orderIds":["ORD-0000001"],"reason":"capacity unavailable before due date"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/orders/reject", body)
	req.Header.Set("Authorization", "Bearer "+schedulerA)
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("reject failed: %d %s", res.Code, res.Body.String())
	}

	for _, dueDate := range []string{"2026-04-30", "2026-04-29"} {
		body = bytes.NewBufferString(`{"orderId":"ORD-0000001","dueDate":"` + dueDate + `","quantity":2000}`)
		req = httptest.NewRequest(http.MethodPost, "/api/orders/resubmit", body)
		req.Header.Set("Authorization", "Bearer "+salesToken)
		res = httptest.NewRecorder()
		server.ServeHTTP(res, req)
		if res.Code != http.StatusBadRequest || !strings.Contains(res.Body.String(), unacceptableDueDateMessage) {
			t.Fatalf("expected unacceptable due date rejection for %s, got %d body=%s", dueDate, res.Code, res.Body.String())
		}
	}
}

func TestSalesCannotChangeNoteDuringResubmit(t *testing.T) {
	store := NewMemoryStore()
	server := NewServer("secret", store)
	salesToken := login(t, server, "sales", "demo")
	body := bytes.NewBufferString(`{"customer":"ACME","lineId":"A","quantity":2500,"priority":"low","dueDate":"2026-05-03","note":"original sales note"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/orders", body)
	req.Header.Set("Authorization", "Bearer "+salesToken)
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusCreated {
		t.Fatalf("create order failed: %d %s", res.Code, res.Body.String())
	}

	schedulerA := login(t, server, "scheduler-a", "demo")
	body = bytes.NewBufferString(`{"orderIds":["ORD-0000001"],"reason":"capacity unavailable before due date"}`)
	req = httptest.NewRequest(http.MethodPost, "/api/orders/reject", body)
	req.Header.Set("Authorization", "Bearer "+schedulerA)
	res = httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("reject failed: %d %s", res.Code, res.Body.String())
	}

	body = bytes.NewBufferString(`{"orderId":"ORD-0000001","dueDate":"2026-05-05","quantity":2000,"note":"changed note"}`)
	req = httptest.NewRequest(http.MethodPost, "/api/orders/resubmit", body)
	req.Header.Set("Authorization", "Bearer "+salesToken)
	res = httptest.NewRecorder()
	server.ServeHTTP(res, req)

	if res.Code != http.StatusBadRequest {
		t.Fatalf("expected note update rejection, got %d body=%s", res.Code, res.Body.String())
	}
	if store.orders["ORD-0000001"].Note != "original sales note" || store.orders["ORD-0000001"].Status != domain.StatusRejected {
		t.Fatalf("order should remain rejected with original note, got %+v", store.orders["ORD-0000001"])
	}
}

func TestScheduleHistoryReturnsWorkflowAuditsForSchedulerLine(t *testing.T) {
	server := NewServer("secret", NewMemoryStore())
	salesToken := login(t, server, "sales", "demo")
	createOrder(t, server, salesToken, "A")
	createOrder(t, server, salesToken, "B")

	schedulerA := login(t, server, "scheduler-a", "demo")
	createScheduleJob(t, server, schedulerA, "A")
	startProduction(t, server, schedulerA, "ORD-0000001")

	schedulerB := login(t, server, "scheduler-b", "demo")
	createScheduleJob(t, server, schedulerB, "B")

	req := httptest.NewRequest(http.MethodGet, "/api/schedules/history", nil)
	req.Header.Set("Authorization", "Bearer "+schedulerA)
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("history failed: %d %s", res.Code, res.Body.String())
	}
	var payload struct {
		History []domain.AuditEntry `json:"history"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode history response: %v", err)
	}
	actions := []string{}
	for _, entry := range payload.History {
		actions = append(actions, entry.Action)
		if entry.Resource == "JOB-2" {
			t.Fatalf("scheduler A should not see line B job history: %+v", payload.History)
		}
		if entry.Action == "order.create" {
			t.Fatalf("history should exclude non-workflow audits: %+v", payload.History)
		}
	}
	if !contains(actions, "schedule.job.create") || !contains(actions, "production.start") {
		t.Fatalf("expected scheduler workflow actions, got %+v", actions)
	}
}

func login(t *testing.T, server *Server, username, password string) string {
	t.Helper()
	body := bytes.NewBufferString(`{"username":"` + username + `","password":"` + password + `"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", body)
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("login failed: %d %s", res.Code, res.Body.String())
	}
	var payload struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode login response: %v", err)
	}
	return payload.Token
}

func createOrder(t *testing.T, server *Server, token, lineID string) {
	t.Helper()
	createOrderWithPriority(t, server, token, lineID, "low")
}

func createOrderWithPriority(t *testing.T, server *Server, token, lineID, priority string) string {
	t.Helper()
	return createOrderWithPriorityAndDue(t, server, token, lineID, priority, "2026-05-03")
}

func createOrderWithQuantityPriorityAndDue(t *testing.T, server *Server, token, lineID string, quantity int, priority, dueDate string) string {
	t.Helper()
	body := bytes.NewBufferString(`{"customer":"ACME","lineId":"` + lineID + `","quantity":` + strconv.Itoa(quantity) + `,"priority":"` + priority + `","dueDate":"` + dueDate + `"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/orders", body)
	req.Header.Set("Authorization", "Bearer "+token)
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusCreated {
		t.Fatalf("create order failed: %d %s", res.Code, res.Body.String())
	}
	var payload domain.Order
	if err := json.Unmarshal(res.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode order response: %v", err)
	}
	return payload.ID
}

func createOrderWithPriorityAndDue(t *testing.T, server *Server, token, lineID, priority, dueDate string) string {
	t.Helper()
	body := bytes.NewBufferString(`{"customer":"ACME","lineId":"` + lineID + `","quantity":2500,"priority":"` + priority + `","dueDate":"` + dueDate + `"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/orders", body)
	req.Header.Set("Authorization", "Bearer "+token)
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusCreated {
		t.Fatalf("create order failed: %d %s", res.Code, res.Body.String())
	}
	var payload domain.Order
	if err := json.Unmarshal(res.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode order response: %v", err)
	}
	return payload.ID
}

func startProduction(t *testing.T, server *Server, token, orderID string) {
	t.Helper()
	body := bytes.NewBufferString(`{"orderId":"` + orderID + `"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/production/start", body)
	req.Header.Set("Authorization", "Bearer "+token)
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("start production failed: %d %s", res.Code, res.Body.String())
	}
}

func createScheduleJob(t *testing.T, server *Server, token, lineID string) string {
	t.Helper()
	previewID := createSchedulePreview(t, server, token, lineID)
	body := bytes.NewBufferString(`{"lineId":"` + lineID + `","startDate":"2026-05-01","currentDate":"2026-04-30","previewId":"` + previewID + `"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/schedules/jobs", body)
	req.Header.Set("Authorization", "Bearer "+token)
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusAccepted {
		t.Fatalf("create schedule job failed: %d %s", res.Code, res.Body.String())
	}
	var payload struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode job response: %v", err)
	}
	return payload.ID
}

func createSchedulePreview(t *testing.T, server *Server, token, lineID string) string {
	t.Helper()
	body := bytes.NewBufferString(`{"lineId":"` + lineID + `","startDate":"2026-05-01","currentDate":"2026-04-30"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/schedules/preview", body)
	req.Header.Set("Authorization", "Bearer "+token)
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("create schedule preview failed: %d %s", res.Code, res.Body.String())
	}
	var payload struct {
		PreviewID string `json:"previewId"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode preview response: %v", err)
	}
	return payload.PreviewID
}

func hasAllocationOnDate(allocations []struct {
	OrderID string `json:"orderId"`
	Date    string `json:"date"`
}, orderID, date string) bool {
	for _, allocation := range allocations {
		if allocation.OrderID == orderID && strings.HasPrefix(allocation.Date, date) {
			return true
		}
	}
	return false
}

func allocationCountForOrder(allocations []domain.ScheduleAllocation, orderID string) int {
	count := 0
	for _, allocation := range allocations {
		if allocation.OrderID == orderID && allocation.Status != domain.StatusCompleted {
			count++
		}
	}
	return count
}

func mustAPIDate(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(dateLayout, value)
	if err != nil {
		t.Fatalf("parse date: %v", err)
	}
	return parsed
}

func contains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

type recordingPublisher struct {
	jobs []domain.ScheduleJob
}

func (p *recordingPublisher) PublishScheduleJob(_ context.Context, job domain.ScheduleJob) error {
	p.jobs = append(p.jobs, job)
	return nil
}

func (p *recordingPublisher) Close() error {
	return nil
}

type failingPublisher struct{}

func (failingPublisher) PublishScheduleJob(context.Context, domain.ScheduleJob) error {
	return errors.New("kafka unavailable")
}

func (failingPublisher) Close() error {
	return nil
}

type failingVerifyTokenSessionStore struct {
	NoopTokenSessionStore
}

func (failingVerifyTokenSessionStore) Verify(context.Context, string, auth.Claims) error {
	return errors.New("redis unavailable")
}

type failingSaveTokenSessionStore struct {
	NoopTokenSessionStore
}

func (failingSaveTokenSessionStore) Save(context.Context, string, auth.Claims) error {
	return errors.New("redis unavailable")
}
