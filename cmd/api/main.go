package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/d11nn/woms/internal/api"
	"github.com/d11nn/woms/internal/startup"
)

func main() {
	config := parseAPIConfig(os.Getenv)

	store, closeStore, err := buildStore(config)
	if err != nil {
		log.Fatalf("postgres store failed: %v", err)
	}
	defer closeStore()

	publisher, closePublisher, err := buildPublisher(config)
	if err != nil {
		log.Fatalf("kafka broker failed: %v", err)
	}
	defer closePublisher()

	tokenSessions, closeTokenSessions, err := buildTokenSessions(config)
	if err != nil {
		log.Fatal(err)
	}
	defer closeTokenSessions()

	server := &http.Server{
		Addr: config.HTTPAddr,
		Handler: api.NewServerWithPublisherAndConfig(config.JWTSecret, store, publisher, api.ServerConfig{
			TokenSessions:     tokenSessions,
			CORSAllowedOrigin: config.CORSAllowedOrigin,
			AuthMode:          config.AuthMode,
		}),
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Printf("woms api listening on %s", config.HTTPAddr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("api server failed: %v", err)
	}
}

type apiConfig struct {
	HTTPAddr            string
	JWTSecret           string
	StoreMode           string
	DatabaseURL         string
	DemoSeedData        bool
	KafkaPublishEnabled bool
	KafkaBrokers        []string
	KafkaScheduleTopic  string
	AuthSessionStore    string
	RedisAddr           string
	CORSAllowedOrigin   string
	AuthMode            string
	DependencyTimeout   time.Duration
	DependencyInterval  time.Duration
}

func buildStore(config apiConfig) (api.Store, func(), error) {
	if config.StoreMode == "postgres" {
		var postgresStore *api.PostgresStore
		err := retryPostgres(config, func(ctx context.Context) error {
			var err error
			postgresStore, err = api.NewPostgresStoreContext(ctx, config.DatabaseURL, config.DemoSeedData)
			return err
		})
		if err != nil {
			return nil, noopCleanup, err
		}
		return postgresStore, func() { postgresStore.Close() }, nil
	}

	memoryStore := api.NewMemoryStore()
	if config.DemoSeedData {
		memoryStore = api.NewDemoMemoryStore()
	}
	return memoryStore, noopCleanup, nil
}

func buildPublisher(config apiConfig) (api.ScheduleJobPublisher, func(), error) {
	publisher := api.ScheduleJobPublisher(api.NoopScheduleJobPublisher{})
	if config.KafkaPublishEnabled {
		if err := retryKafka(config, func(ctx context.Context) error {
			return startup.PingAnyTCP(ctx, config.KafkaBrokers)
		}); err != nil {
			return nil, noopCleanup, err
		}
		publisher = api.NewKafkaScheduleJobPublisher(config.KafkaBrokers, config.KafkaScheduleTopic)
		return publisher, func() { publisher.Close() }, nil
	}

	return publisher, noopCleanup, nil
}

func buildTokenSessions(config apiConfig) (api.TokenSessionStore, func(), error) {
	tokenSessions := api.TokenSessionStore(api.NoopTokenSessionStore{})
	if config.AuthSessionStore == "redis" {
		if config.RedisAddr == "" {
			return nil, noopCleanup, errors.New("AUTH_SESSION_STORE=redis requires REDIS_ADDR")
		}
		redisSessions := api.NewRedisTokenSessionStore(config.RedisAddr)
		if err := retryRedis(config, func(ctx context.Context) error {
			return redisSessions.Ping(ctx)
		}); err != nil {
			return nil, noopCleanup, fmt.Errorf("redis auth session store failed: %w", err)
		}
		tokenSessions = redisSessions
		return tokenSessions, func() { tokenSessions.Close() }, nil
	}

	return tokenSessions, noopCleanup, nil
}

func retryPostgres(config apiConfig, operation func(context.Context) error) error {
	return retryDependency(config, "postgres store", operation)
}

func retryKafka(config apiConfig, operation func(context.Context) error) error {
	return retryDependency(config, "kafka broker", operation)
}

func retryRedis(config apiConfig, operation func(context.Context) error) error {
	return retryDependency(config, "redis auth session store", operation)
}

func retryDependency(config apiConfig, name string, operation func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(context.Background(), config.DependencyTimeout)
	defer cancel()
	return startup.RetryDependency(ctx, name, config.DependencyInterval, log.Printf, operation)
}

func noopCleanup() {
	// No resources to clean up
}

func parseAPIConfig(lookup func(string) string) apiConfig {
	return apiConfig{
		HTTPAddr:            envLookup(lookup, "HTTP_ADDR", ":8080"),
		JWTSecret:           envLookup(lookup, "JWT_SECRET", "change-me-in-production"),
		StoreMode:           envLookup(lookup, "API_STORE", "memory"),
		DatabaseURL:         envLookup(lookup, "DATABASE_URL", ""),
		DemoSeedData:        envLookup(lookup, "DEMO_SEED_DATA", "true") != "false",
		KafkaPublishEnabled: envLookup(lookup, "KAFKA_PUBLISH_ENABLED", "true") != "false",
		KafkaBrokers:        startup.SplitCSV(envLookup(lookup, "KAFKA_BROKERS", "kafka:9092")),
		KafkaScheduleTopic:  envLookup(lookup, "KAFKA_SCHEDULE_TOPIC", "woms.schedule.jobs"),
		AuthSessionStore:    envLookup(lookup, "AUTH_SESSION_STORE", ""),
		RedisAddr:           envLookup(lookup, "REDIS_ADDR", ""),
		CORSAllowedOrigin:   envLookup(lookup, "CORS_ALLOWED_ORIGIN", "*"),
		AuthMode:            envLookup(lookup, "AUTH_MODE", "local"),
		DependencyTimeout:   envDurationLookup(lookup, "API_DEPENDENCY_RETRY_TIMEOUT_MS", 2*time.Minute),
		DependencyInterval:  envDurationLookup(lookup, "API_DEPENDENCY_RETRY_INTERVAL_MS", 2*time.Second),
	}
}

func env(key, fallback string) string {
	return envLookup(os.Getenv, key, fallback)
}

func envLookup(lookup func(string) string, key, fallback string) string {
	value := strings.TrimSpace(lookup(key))
	if value == "" {
		return fallback
	}
	return value
}

func envDuration(key string, fallback time.Duration) time.Duration {
	return envDurationLookup(os.Getenv, key, fallback)
}

func envDurationLookup(lookup func(string) string, key string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(lookup(key))
	if value == "" {
		return fallback
	}
	millis, err := strconv.Atoi(value)
	if err != nil || millis < 0 {
		return fallback
	}
	return time.Duration(millis) * time.Millisecond
}
