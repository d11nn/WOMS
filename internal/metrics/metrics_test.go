package metrics

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// ────────────────────────────────────────────────────────────────────
// Test 1: /metrics endpoint returns valid Prometheus-format text
// ────────────────────────────────────────────────────────────────────

func TestMetricsEndpointReturnsPrometheusText(t *testing.T) {
	Register()

	handler := Handler()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	res := httptest.NewRecorder()

	handler.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("expected 200 from /metrics, got %d", res.Code)
	}

	body, _ := io.ReadAll(res.Body)
	text := string(body)

	// Should contain Go runtime metric families registered in init().
	if !strings.Contains(text, "go_goroutines") {
		t.Fatal("expected go runtime metrics in /metrics output")
	}

	// Re-scrape after initialization.
	req = httptest.NewRequest(http.MethodGet, "/metrics", nil)
	res = httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	body, _ = io.ReadAll(res.Body)
	text = string(body)

	// Should contain the custom woms metrics.
	if !strings.Contains(text, "woms_current_online_user_count") {
		t.Fatal("expected woms_current_online_user_count in /metrics output")
	}
}

// ────────────────────────────────────────────────────────────────────
// Test 2: Custom counters increment correctly
// ────────────────────────────────────────────────────────────────────

func TestCustomCountersIncrement(t *testing.T) {
	Register()

	// Reset counters for this test by gathering baseline.
	before := gatherGaugeValue(t, "woms_current_online_user_count")
	CurrentOnlineUserCount.Inc()
	CurrentOnlineUserCount.Inc()
	after := gatherGaugeValue(t, "woms_current_online_user_count")

	delta := after - before
	if delta != 2 {
		t.Fatalf("expected current_online_user_count to increase by 2, got delta %f", delta)
	}

	// Test labeled counter.
	HTTPRequestsTotal.WithLabelValues("GET", "/api/orders", "200").Inc()
	accessVal := gatherLabeledCounterValue(t, "woms_http_requests_total", map[string]string{
		"method": "GET",
		"path":   "/api/orders",
		"status": "200",
	})

	if accessVal < 1 {
		t.Fatalf("expected HTTPRequestsTotal >= 1, got %f", accessVal)
	}
}

// ────────────────────────────────────────────────────────────────────
// Test 3: Adding a new metric type is easy via Registry
// ────────────────────────────────────────────────────────────────────

func TestRegistrySupportsNewMetricTypes(t *testing.T) {
	Register()

	// Simulate a new metric type that an external package might add.
	customHistogram := prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: "woms",
		Name:      "test_request_duration_seconds",
		Help:      "Test request duration histogram.",
		Buckets:   prometheus.DefBuckets,
	})

	// Register should succeed without panic.
	Registry.MustRegister(customHistogram)
	t.Cleanup(func() {
		Registry.Unregister(customHistogram)
	})

	customHistogram.Observe(0.42)

	handler := Handler()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)

	body, _ := io.ReadAll(res.Body)
	text := string(body)

	if !strings.Contains(text, "woms_test_request_duration_seconds") {
		t.Fatal("expected newly registered histogram in /metrics output")
	}
}

// ────────────────────────────────────────────────────────────────────
// Helpers
// ────────────────────────────────────────────────────────────────────

func gatherGaugeValue(t *testing.T, name string) float64 {
	t.Helper()
	family := findMetricFamily(gatherMetricFamilies(t), name)
	if family == nil {
		return 0
	}

	for _, metric := range family.GetMetric() {
		if metric.GetGauge() != nil {
			return metric.GetGauge().GetValue()
		}
	}
	return 0
}

func gatherLabeledCounterValue(t *testing.T, name string, labels map[string]string) float64 {
	t.Helper()
	family := findMetricFamily(gatherMetricFamilies(t), name)
	if family == nil {
		return 0
	}

	for _, metric := range family.GetMetric() {
		if !metricMatchesLabels(metric, labels) {
			continue
		}
		if value, ok := counterValue(metric); ok {
			return value
		}
	}
	return 0
}

func gatherMetricFamilies(t *testing.T) []*dto.MetricFamily {
	t.Helper()
	families, err := Registry.Gather()
	if err != nil {
		t.Fatalf("gather failed: %v", err)
	}
	return families
}

func findMetricFamily(families []*dto.MetricFamily, name string) *dto.MetricFamily {
	for _, family := range families {
		if family.GetName() == name {
			return family
		}
	}
	return nil
}

func metricMatchesLabels(metric *dto.Metric, labels map[string]string) bool {
	for name, value := range labels {
		if !metricHasLabel(metric, name, value) {
			return false
		}
	}
	return true
}

func metricHasLabel(metric *dto.Metric, name, value string) bool {
	for _, label := range metric.GetLabel() {
		if label.GetName() == name && label.GetValue() == value {
			return true
		}
	}
	return false
}

func counterValue(metric *dto.Metric) (float64, bool) {
	counter := metric.GetCounter()
	if counter == nil {
		return 0, false
	}
	return counter.GetValue(), true
}
