package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRunHealthcheckSucceedsOn2xx(t *testing.T) {
	server := newHealthcheckTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("method = %s, want GET", r.Method)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	var stderr bytes.Buffer
	code := runHealthcheck(lookup(map[string]string{"HEALTHCHECK_URL": server.URL}), server.Client(), &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestRunHealthcheckRejectsInvalidTimeout(t *testing.T) {
	var stderr bytes.Buffer
	code := runHealthcheck(lookup(map[string]string{"HEALTHCHECK_TIMEOUT": "fast"}), http.DefaultClient, &stderr)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "invalid HEALTHCHECK_TIMEOUT") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestRunHealthcheckRejectsInvalidURL(t *testing.T) {
	var stderr bytes.Buffer
	code := runHealthcheck(lookup(map[string]string{"HEALTHCHECK_URL": "://bad"}), http.DefaultClient, &stderr)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "invalid HEALTHCHECK_URL") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestRunHealthcheckReportsRequestError(t *testing.T) {
	expected := errors.New("dial failed")
	var stderr bytes.Buffer
	code := runHealthcheck(lookup(map[string]string{"HEALTHCHECK_URL": "http://example.test"}), errorClient{err: expected}, &stderr)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "healthcheck request failed") || !strings.Contains(stderr.String(), expected.Error()) {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestRunHealthcheckReportsNon2xxStatus(t *testing.T) {
	server := newHealthcheckTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	var stderr bytes.Buffer
	code := runHealthcheck(lookup(map[string]string{"HEALTHCHECK_URL": server.URL}), server.Client(), &stderr)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "healthcheck returned status 503") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestCheckHealthAcceptsBoundary2xxAndClosesBody(t *testing.T) {
	body := &trackingReadCloser{}
	err := checkHealth(t.Context(), responseClient{response: &http.Response{
		StatusCode: http.StatusMultipleChoices - 1,
		Body:       body,
	}}, "http://example.test/readyz", time.Second)
	if err != nil {
		t.Fatalf("checkHealth returned error: %v", err)
	}
	if !body.closed {
		t.Fatal("expected response body to be closed")
	}
}

func TestEnvUsesFallbackAndOverride(t *testing.T) {
	t.Setenv("WOMS_HEALTHCHECK_TEST", "")
	if got := env("WOMS_HEALTHCHECK_TEST", "fallback"); got != "fallback" {
		t.Fatalf("expected fallback, got %q", got)
	}
	t.Setenv("WOMS_HEALTHCHECK_TEST", "configured")
	if got := env("WOMS_HEALTHCHECK_TEST", "fallback"); got != "configured" {
		t.Fatalf("expected configured env, got %q", got)
	}
}

func lookup(values map[string]string) func(string) string {
	return func(key string) string {
		return values[key]
	}
}

type errorClient struct {
	err error
}

func (c errorClient) Do(*http.Request) (*http.Response, error) {
	return nil, c.err
}

type responseClient struct {
	response *http.Response
}

func (c responseClient) Do(*http.Request) (*http.Response, error) {
	return c.response, nil
}

type trackingReadCloser struct {
	closed bool
}

func (b *trackingReadCloser) Read([]byte) (int, error) {
	return 0, io.EOF
}

func (b *trackingReadCloser) Close() error {
	b.closed = true
	return nil
}

func newHealthcheckTestServer(t *testing.T, handler http.Handler) *httptest.Server {
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
