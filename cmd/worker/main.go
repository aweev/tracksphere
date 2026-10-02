// Command worker runs the background job consumers: exception rules engine,
// notifications, ETA recalculation. It shares the database queue with the API
// and can be scaled horizontally (SKIP LOCKED makes concurrent workers safe).
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/tracksphere/tracksphere/internal/config"
	"github.com/tracksphere/tracksphere/internal/db"
	"github.com/tracksphere/tracksphere/internal/queue"
	"github.com/tracksphere/tracksphere/internal/shipments"
	"github.com/tracksphere/tracksphere/internal/workers"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("config", "err", err)
		os.Exit(1)
	}
	log := newLogger(cfg.LogLevel, cfg.Env)

	ctx, stop := signal.NotifyContext(context.Background(),
		os.Interrupt, syscall.SIGTERM)
	defer stop()

	if cfg.AutoMigrate {
		if err := db.Migrate(ctx, cfg.MigrationsURL); err != nil {
			log.Error("migrations failed", "err", err)
			os.Exit(1)
		}
	}

	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Error("database", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	q := queue.New(pool, cfg.WorkerPollInterval, log)

	// Register all handlers. The API process enqueues; this process executes.
	q.Register(shipments.JobEvaluateRules, workers.HandleEvaluateRules(pool, log))
	q.Register(shipments.JobNotifyShipment, workers.HandleNotifyShipment(pool, log))
	q.Register(shipments.JobRecalculateETA, workers.HandleRecalculateETA(pool, log))

	// Horizontal scaling: N in-process pollers share the same queue.
	log.Info("worker starting", "concurrency", cfg.WorkerConcurrency)
	errCh := make(chan error, cfg.WorkerConcurrency)
	for i := 0; i < cfg.WorkerConcurrency; i++ {
		go func() { q.Run(ctx); errCh <- nil }()
	}
	<-ctx.Done()
	for i := 0; i < cfg.WorkerConcurrency; i++ {
		<-errCh
	}
	log.Info("worker stopped")
}

// newLogger builds the process logger (mirrors cmd/api).
func newLogger(level, env string) *slog.Logger {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: lvl}
	var h slog.Handler
	if env == "production" {
		h = slog.NewJSONHandler(os.Stdout, opts)
	} else {
		h = slog.NewTextHandler(os.Stdout, opts)
	}
	log := slog.New(h)
	slog.SetDefault(log)
	return log
}