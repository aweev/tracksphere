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
	"time"

	"github.com/tracksphere/tracksphere/internal/config"
	"github.com/tracksphere/tracksphere/internal/db"
	"github.com/tracksphere/tracksphere/internal/queue"
	"github.com/tracksphere/tracksphere/internal/shipments"
	"github.com/tracksphere/tracksphere/internal/workers"

	"github.com/jackc/pgx/v5/pgxpool"
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

	pool, err := db.Open(ctx, cfg.DatabaseURL, cfg.WorkerConcurrency, cfg.APIWorkers)
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
	q.Register(workers.WebhookDispatchJob, workers.HandleWebhookDispatch(pool, log))
	q.Register(workers.ShipmentPollJob, workers.HandlePoll(pool, log, cfg.SecretKeys,
		func(p *pgxpool.Pool) *shipments.Service { return shipments.NewService(p, q) }))
	q.Register(workers.ReportWeeklyJob, workers.HandleDigest(pool, log))
	q.Register(workers.ReportDailyJob, workers.HandleDailyReport(pool, log))
	q.Register("shipment.notify_customer", workers.HandleNotifyCustomer(q, log))
	q.Register("shipment.email_carrier", workers.HandleEmailCarrier(q, log))
	// Time-based exception detection, reconciliation, escalation, read model.
	q.Register(workers.SweepJob, workers.HandleSweep(pool, log, "worker-"+hostname()))
	// Notification routing with the interrupt budget, and digest flushing.
	q.Register(workers.AlertNotifyJob, workers.HandleAlertNotify(pool, log))
	q.Register(workers.FlushDigestsJob, workers.HandleFlushDigests(pool, log))

	// Horizontal scaling: N in-process pollers share the same queue.
	// Each poller claims under its own workerID (locked_by is debuggable);
	// ONE reaper + ONE archiver run per deployment (not per poller).
	log.Info("worker starting", "concurrency", cfg.WorkerConcurrency)
	go func() {
		reapTicker := time.NewTicker(time.Minute)
		defer reapTicker.Stop()
		archiveTicker := time.NewTicker(24 * time.Hour)
		defer archiveTicker.Stop()
		pollTicker := time.NewTicker(time.Minute)
		defer pollTicker.Stop()
		sweepTicker := time.NewTicker(time.Hour)
		defer sweepTicker.Stop()
		digestTicker := time.NewTicker(7 * 24 * time.Hour)
		defer digestTicker.Stop()
		dailyReportTicker := time.NewTicker(24 * time.Hour)
		defer dailyReportTicker.Stop()
		flushTicker := time.NewTicker(15 * time.Minute)
		defer flushTicker.Stop()
		retentionTicker := time.NewTicker(24 * time.Hour)
		defer retentionTicker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-reapTicker.C:
				q.ReapStuck(ctx)
			case <-archiveTicker.C:
				if n, err := q.ArchiveOld(ctx); err != nil {
					log.Error("archive old jobs", "err", err)
				} else if n > 0 {
					log.Info("archived old jobs", "count", n)
				}
			case <-pollTicker.C:
				if n, err := workers.PollDueCredentials(ctx, pool); err != nil {
					log.Error("schedule carrier polls", "err", err)
				} else if n > 0 {
					log.Info("scheduled carrier polls", "count", n)
				}
			case <-sweepTicker.C:
				// Time-based detection has to be scheduled, not event-driven:
				// the exceptions it exists to catch produce no event at all.
				// enqueue is idempotent by job id, so a restart mid-window
				// cannot double-run.
				if err := workers.EnqueueSweep(ctx, pool); err != nil {
					log.Error("schedule sweep", "err", err)
				}
			case <-flushTicker.C:
				if err := workers.EnqueueDigestFlush(ctx, pool); err != nil {
					log.Error("schedule digest flush", "err", err)
				}
			case <-digestTicker.C:
				if n, err := workers.EnqueueDigests(ctx, pool); err != nil {
					log.Error("schedule digests", "err", err)
				} else if n > 0 {
					log.Info("scheduled digests", "count", n)
				}
			case <-dailyReportTicker.C:
				if n, err := workers.EnqueueDailyReports(ctx, pool); err != nil {
					log.Error("schedule daily reports", "err", err)
				} else if n > 0 {
					log.Info("scheduled daily reports", "count", n)
				}
			case <-retentionTicker.C:
				if n, err := workers.RetentionSweep(ctx, pool); err != nil {
					log.Error("retention sweep", "err", err)
				} else if n > 0 {
					log.Info("retention sweep deleted rows", "count", n)
				}
			}
		}
	}()
	errCh := make(chan error, cfg.WorkerConcurrency)
	for i := 0; i < cfg.WorkerConcurrency; i++ {
		id := q.WorkerID(i)
		go func() { q.RunAs(ctx, id); errCh <- nil }()
	}
	<-ctx.Done()
	for i := 0; i < cfg.WorkerConcurrency; i++ {
		<-errCh
	}
	log.Info("worker stopped")
}

// hostname returns a short identifier for this replica, used to name the sweep
// lease holder so an operator can see which process owns a window.
func hostname() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "unknown"
	}
	return h
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