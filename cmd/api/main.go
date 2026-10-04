// Command api runs the TrackSphere HTTP API server (handlers + SSE hub).
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/tracksphere/tracksphere/internal/config"
	"github.com/tracksphere/tracksphere/internal/db"
	"github.com/tracksphere/tracksphere/internal/httpapi"
	"github.com/tracksphere/tracksphere/internal/queue"
	"github.com/tracksphere/tracksphere/internal/realtime"
	"github.com/tracksphere/tracksphere/internal/shipments"
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
		log.Info("running migrations")
		if err := db.Migrate(ctx, cfg.MigrationsURL); err != nil {
			log.Error("migrations failed", "err", err)
			os.Exit(1)
		}
	}

	pool, err := db.Open(ctx, cfg.DatabaseURL, cfg.WorkerConcurrency, cfg.APIWorkers)
	if err != nil {
		log.Error("database", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	// Dependencies
	hub := realtime.NewHub(log, pool)
	q := queue.New(pool, cfg.WorkerPollInterval, log)
	repo := shipments.NewRepository(pool)
	svc := shipments.NewService(pool, q)
	srv := httpapi.NewServer(cfg, pool, repo, svc, hub, log)

	// Realtime listener (LISTEN/NOTIFY → SSE)
	go hub.Run(ctx, pool)

	httpServer := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           srv.Router(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      0, // SSE streams must not be cut off
		IdleTimeout:       120 * time.Second,
	}

	go func() {
		<-ctx.Done()
		log.Info("shutting down http server")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()

	log.Info("api listening", "addr", cfg.HTTPAddr, "env", cfg.Env)
	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("http server", "err", err)
		os.Exit(1)
	}
	log.Info("api stopped")
}

// newLogger builds the process logger.
func newLogger(level, env string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(level) {
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