// Package config loads and validates process configuration from the environment.
// Every knob is prefixed TRACKSPHERE_ so it cannot collide with other software
// running on the same host. See .env.example for documentation of each value.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

type Config struct {
	Env     string // local | development | production
	HTTPAddr string

	DatabaseURL       string
	MigrationsURL     string
	AutoMigrate       bool

	CORSOrigins []string

	SecretKey      []byte
	SessionTTL     time.Duration
	WebhookSecret  []byte

	WorkerConcurrency   int
	WorkerPollInterval  time.Duration

	LogLevel string
}

// Load reads .env (if present, non-fatal when missing) then the process
// environment, applies defaults and validates the result.
func Load() (*Config, error) {
	_ = godotenv.Load() // missing file is fine — production uses real env vars

	cfg := &Config{
		Env:            get("TRACKSPHERE_ENV", "local"),
		HTTPAddr:       get("TRACKSPHERE_HTTP_ADDR", ":8080"),
		DatabaseURL:    get("TRACKSPHERE_DATABASE_URL", ""),
		MigrationsURL:  get("TRACKSPHERE_MIGRATIONS_URL", ""),
		CORSOrigins:    splitCSV(get("TRACKSPHERE_CORS_ORIGINS", "http://localhost:5173")),
		LogLevel:       get("TRACKSPHERE_LOG_LEVEL", "info"),
	}

	var err error
	if cfg.AutoMigrate, err = getBool("TRACKSPHERE_AUTO_MIGRATE", true); err != nil {
		return nil, err
	}
	if cfg.WorkerConcurrency, err = getInt("TRACKSPHERE_WORKER_CONCURRENCY", 4, 1, 64); err != nil {
		return nil, err
	}
	pollMS, err := getInt("TRACKSPHERE_WORKER_POLL_INTERVAL_MS", 500, 50, 10_000)
	if err != nil {
		return nil, err
	}
	cfg.WorkerPollInterval = time.Duration(pollMS) * time.Millisecond

	ttlHours, err := getInt("TRACKSPHERE_SESSION_TTL_HOURS", 168, 1, 24*90)
	if err != nil {
		return nil, err
	}
	cfg.SessionTTL = time.Duration(ttlHours) * time.Hour

	cfg.SecretKey = []byte(get("TRACKSPHERE_SECRET_KEY", ""))
	cfg.WebhookSecret = []byte(get("TRACKSPHERE_CARRIER_WEBHOOK_SECRET", ""))

	if cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("TRACKSPHERE_DATABASE_URL is required")
	}
	if cfg.MigrationsURL == "" {
		cfg.MigrationsURL = cfg.DatabaseURL
	}
	if len(cfg.SecretKey) == 0 {
		return nil, fmt.Errorf("TRACKSPHERE_SECRET_KEY is required")
	}
	if len(cfg.WebhookSecret) == 0 {
		return nil, fmt.Errorf("TRACKSPHERE_CARRIER_WEBHOOK_SECRET is required")
	}
	switch cfg.Env {
	case "local", "development", "production":
	default:
		return nil, fmt.Errorf("TRACKSPHERE_ENV must be local|development|production, got %q", cfg.Env)
	}
	return cfg, nil
}

func get(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func getBool(key string, def bool) (bool, error) {
	v := get(key, "")
	if v == "" {
		return def, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("%s: %w", key, err)
	}
	return b, nil
}

func getInt(key string, def, min, max int) (int, error) {
	v := get(key, "")
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	if n < min || n > max {
		return 0, fmt.Errorf("%s must be between %d and %d, got %d", key, min, max, n)
	}
	return n, nil
}

func splitCSV(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}
