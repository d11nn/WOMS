package main

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/d11nn/woms/internal/api"
	"github.com/d11nn/woms/internal/auth"
	"github.com/d11nn/woms/internal/domain"
)

func TestParseAPIConfigDefaults(t *testing.T) {
	config := parseAPIConfig(func(string) string { return "" })

	if config.HTTPAddr != ":8080" {
		t.Fatalf("HTTPAddr = %q, want default", config.HTTPAddr)
	}
	if config.JWTSecret != "change-me-in-production" {
		t.Fatalf("JWTSecret = %q, want default", config.JWTSecret)
	}
	if config.StoreMode != "memory" {
		t.Fatalf("StoreMode = %q, want memory", config.StoreMode)
	}
	if !config.DemoSeedData {
		t.Fatal("DemoSeedData = false, want true")
	}
	if !config.KafkaPublishEnabled {
		t.Fatal("KafkaPublishEnabled = false, want true")
	}
	if !reflect.DeepEqual(config.KafkaBrokers, []string{"kafka:9092"}) {
		t.Fatalf("KafkaBrokers = %#v, want default broker", config.KafkaBrokers)
	}
	if config.DependencyTimeout != 2*time.Minute {
		t.Fatalf("DependencyTimeout = %s, want 2m", config.DependencyTimeout)
	}
	if config.DependencyInterval != 2*time.Second {
		t.Fatalf("DependencyInterval = %s, want 2s", config.DependencyInterval)
	}
}

func TestParseAPIConfigTrimsAndParsesValues(t *testing.T) {
	values := map[string]string{
		"HTTP_ADDR":                        " :9090 ",
		"JWT_SECRET":                       " secret ",
		"API_STORE":                        " postgres ",
		"DATABASE_URL":                     " postgres://woms ",
		"DEMO_SEED_DATA":                   " false ",
		"KAFKA_PUBLISH_ENABLED":            " false ",
		"KAFKA_BROKERS":                    " kafka-0:9092, , kafka-1:9092 ",
		"KAFKA_SCHEDULE_TOPIC":             " jobs ",
		"AUTH_SESSION_STORE":               " redis ",
		"REDIS_ADDR":                       " redis:6379 ",
		"CORS_ALLOWED_ORIGIN":              " https://woms.example ",
		"AUTH_MODE":                        " ingress ",
		"API_DEPENDENCY_RETRY_TIMEOUT_MS":  "1500",
		"API_DEPENDENCY_RETRY_INTERVAL_MS": "250",
	}
	config := parseAPIConfig(func(key string) string { return values[key] })

	if config.HTTPAddr != ":9090" || config.JWTSecret != "secret" || config.StoreMode != "postgres" {
		t.Fatalf("string values were not trimmed: %+v", config)
	}
	if config.DatabaseURL != "postgres://woms" || config.AuthSessionStore != "redis" || config.RedisAddr != "redis:6379" {
		t.Fatalf("connection values were not parsed: %+v", config)
	}
	if config.DemoSeedData || config.KafkaPublishEnabled {
		t.Fatalf("boolean toggles were not parsed: demo=%t kafka=%t", config.DemoSeedData, config.KafkaPublishEnabled)
	}
	if !reflect.DeepEqual(config.KafkaBrokers, []string{"kafka-0:9092", "kafka-1:9092"}) {
		t.Fatalf("KafkaBrokers = %#v", config.KafkaBrokers)
	}
	if config.KafkaScheduleTopic != "jobs" || config.CORSAllowedOrigin != "https://woms.example" || config.AuthMode != "ingress" {
		t.Fatalf("server options were not parsed: %+v", config)
	}
	if config.DependencyTimeout != 1500*time.Millisecond || config.DependencyInterval != 250*time.Millisecond {
		t.Fatalf("durations = %s/%s, want 1500ms/250ms", config.DependencyTimeout, config.DependencyInterval)
	}
}

func TestParseAPIConfigFallsBackForMalformedAndNegativeDurations(t *testing.T) {
	values := map[string]string{
		"API_DEPENDENCY_RETRY_TIMEOUT_MS":  "not-an-int",
		"API_DEPENDENCY_RETRY_INTERVAL_MS": "-1",
	}
	config := parseAPIConfig(func(key string) string { return values[key] })

	if config.DependencyTimeout != 2*time.Minute {
		t.Fatalf("DependencyTimeout = %s, want fallback", config.DependencyTimeout)
	}
	if config.DependencyInterval != 2*time.Second {
		t.Fatalf("DependencyInterval = %s, want fallback", config.DependencyInterval)
	}
}

func TestBuildStoreSelectsMemoryStore(t *testing.T) {
	store, cleanup, err := buildStore(apiConfig{
		StoreMode:    "memory",
		DemoSeedData: false,
	})
	if err != nil {
		t.Fatalf("buildStore returned error: %v", err)
	}
	defer cleanup()

	if _, ok := store.(*api.MemoryStore); !ok {
		t.Fatalf("store type = %T, want *api.MemoryStore", store)
	}
	orders := store.ListOrders(auth.Claims{Role: domain.RoleAdmin})
	if len(orders) != 0 {
		t.Fatalf("memory store seeded %d orders, want none", len(orders))
	}
}

func TestBuildStoreSelectsDemoMemoryStore(t *testing.T) {
	store, cleanup, err := buildStore(apiConfig{
		StoreMode:    "memory",
		DemoSeedData: true,
	})
	if err != nil {
		t.Fatalf("buildStore returned error: %v", err)
	}
	defer cleanup()

	orders := store.ListOrders(auth.Claims{Role: domain.RoleAdmin})
	if len(orders) != 9 {
		t.Fatalf("demo memory store seeded %d orders, want 9", len(orders))
	}
}

func TestBuildTokenSessionsRejectsRedisWithoutAddress(t *testing.T) {
	_, cleanup, err := buildTokenSessions(apiConfig{AuthSessionStore: "redis"})
	defer cleanup()

	if err == nil {
		t.Fatal("buildTokenSessions returned nil error, want missing REDIS_ADDR error")
	}
	if !strings.Contains(err.Error(), "AUTH_SESSION_STORE=redis requires REDIS_ADDR") {
		t.Fatalf("buildTokenSessions error = %q, want REDIS_ADDR validation", err)
	}
}

func TestRetryDependencyWrapperUsesInjectedOperation(t *testing.T) {
	attempts := 0
	err := retryKafka(apiConfig{
		DependencyTimeout:  time.Second,
		DependencyInterval: time.Nanosecond,
	}, func(context.Context) error {
		attempts++
		if attempts == 1 {
			return errors.New("not ready")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("retryKafka returned error: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
}

func TestMainExitsOnPostgresStoreFailure(t *testing.T) {
	if os.Getenv("BE_CRASHY_API") == "1" {
		t.Setenv("API_STORE", "postgres")
		t.Setenv("DATABASE_URL", "postgres://localhost:9999/invalid")
		t.Setenv("API_DEPENDENCY_RETRY_TIMEOUT_MS", "1")
		t.Setenv("API_DEPENDENCY_RETRY_INTERVAL_MS", "1")
		main()
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=TestMainExitsOnPostgresStoreFailure")
	cmd.Env = append(os.Environ(), "BE_CRASHY_API=1")
	err := cmd.Run()
	if err == nil {
		t.Fatalf("expected command to exit with error")
	}
}

func TestMainExitsOnKafkaFailure(t *testing.T) {
	if os.Getenv("BE_CRASHY_API") == "2" {
		t.Setenv("API_STORE", "memory")
		t.Setenv("KAFKA_PUBLISH_ENABLED", "true")
		t.Setenv("KAFKA_BROKERS", "localhost:9999")
		t.Setenv("API_DEPENDENCY_RETRY_TIMEOUT_MS", "1")
		t.Setenv("API_DEPENDENCY_RETRY_INTERVAL_MS", "1")
		main()
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=TestMainExitsOnKafkaFailure")
	cmd.Env = append(os.Environ(), "BE_CRASHY_API=2")
	err := cmd.Run()
	if err == nil {
		t.Fatalf("expected command to exit with error")
	}
}

func TestMainExitsOnRedisSessionMissingAddr(t *testing.T) {
	if os.Getenv("BE_CRASHY_API") == "3" {
		t.Setenv("API_STORE", "memory")
		t.Setenv("KAFKA_PUBLISH_ENABLED", "false")
		t.Setenv("AUTH_SESSION_STORE", "redis")
		t.Setenv("REDIS_ADDR", "") // missing REDIS_ADDR
		main()
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=TestMainExitsOnRedisSessionMissingAddr")
	cmd.Env = append(os.Environ(), "BE_CRASHY_API=3")
	err := cmd.Run()
	if err == nil {
		t.Fatalf("expected command to exit with error")
	}
}

func TestMainExitsOnRedisSessionPingFailure(t *testing.T) {
	if os.Getenv("BE_CRASHY_API") == "4" {
		t.Setenv("API_STORE", "memory")
		t.Setenv("KAFKA_PUBLISH_ENABLED", "false")
		t.Setenv("AUTH_SESSION_STORE", "redis")
		t.Setenv("REDIS_ADDR", "localhost:9999")
		t.Setenv("API_DEPENDENCY_RETRY_TIMEOUT_MS", "1")
		t.Setenv("API_DEPENDENCY_RETRY_INTERVAL_MS", "1")
		main()
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=TestMainExitsOnRedisSessionPingFailure")
	cmd.Env = append(os.Environ(), "BE_CRASHY_API=4")
	err := cmd.Run()
	if err == nil {
		t.Fatalf("expected command to exit with error")
	}
}

func TestMainExitsOnHTTPListenFailure(t *testing.T) {
	if os.Getenv("BE_CRASHY_API") == "5" {
		t.Setenv("API_STORE", "memory")
		t.Setenv("DEMO_SEED_DATA", "false")
		t.Setenv("KAFKA_PUBLISH_ENABLED", "false")
		t.Setenv("HTTP_ADDR", "9999.9999.9999.9999:80") // invalid address to fail listen
		main()
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=TestMainExitsOnHTTPListenFailure")
	cmd.Env = append(os.Environ(), "BE_CRASHY_API=5")
	err := cmd.Run()
	if err == nil {
		t.Fatalf("expected command to exit with error")
	}
}

func TestMainRunsWithMemoryStore(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		if strings.Contains(err.Error(), "operation not permitted") {
			t.Skip("tcp listen blocked in sandbox")
		}
		t.Fatalf("listen: %v", err)
	}
	addr := l.Addr().String()
	l.Close()

	if os.Getenv("BE_RUNNING_API") == "1" {
		t.Setenv("API_STORE", "memory")
		t.Setenv("DEMO_SEED_DATA", "true")
		t.Setenv("KAFKA_PUBLISH_ENABLED", "false")
		t.Setenv("HTTP_ADDR", addr)
		main()
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=TestMainRunsWithMemoryStore")
	cmd.Env = append(os.Environ(), "BE_RUNNING_API=1")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}

	time.Sleep(200 * time.Millisecond)
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
}

func TestEnvHelpers(t *testing.T) {
	t.Setenv("TEST_ENV_VAR", "value")
	if got := env("TEST_ENV_VAR", "fallback"); got != "value" {
		t.Errorf("env = %q, want value", got)
	}
	if got := env("TEST_NON_EXISTENT", "fallback"); got != "fallback" {
		t.Errorf("env = %q, want fallback", got)
	}

	t.Setenv("TEST_DURATION_VAR", "500")
	if got := envDuration("TEST_DURATION_VAR", time.Second); got != 500*time.Millisecond {
		t.Errorf("envDuration = %s, want 500ms", got)
	}
	if got := envDuration("TEST_NON_EXISTENT_DURATION", time.Second); got != time.Second {
		t.Errorf("envDuration = %s, want 1s", got)
	}
}
