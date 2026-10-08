// Package queue implements a transactional job queue on plain PostgreSQL
// (FOR UPDATE SKIP LOCKED + outbox pattern). No Redis/NATS required —
// jobs are enqueued inside the same transaction as the business data they
// describe, so nothing can be lost between commit and dispatch.
package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Handler processes one job. Returning a non-nil error triggers retry with
// exponential backoff (up to job.max_attempts, then status='dead').
type Handler func(ctx context.Context, payload []byte) error

// Queue is safe for concurrent use.
type Queue struct {
	pool         *pgxpool.Pool
	pollInterval time.Duration
	workerID     string
	log          *slog.Logger

	mu       sync.RWMutex
	handlers map[string]Handler
}

// New creates a queue bound to pool.
func New(pool *pgxpool.Pool, poll time.Duration, log *slog.Logger) *Queue {
	if poll <= 0 {
		poll = 500 * time.Millisecond
	}
	if log == nil {
		log = slog.Default()
	}
	host, _ := os.Hostname()
	return &Queue{
		pool:         pool,
		pollInterval: poll,
		workerID:     fmt.Sprintf("%s-%d-%s", host, os.Getpid(), uuid.NewString()[:8]),
		log:          log,
		handlers:     map[string]Handler{},
	}
}

// Pool returns the underlying connection pool.
func (q *Queue) Pool() *pgxpool.Pool {
	return q.pool
}

// Register binds a handler to a job kind. Must be called before Run.
func (q *Queue) Register(kind string, h Handler) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.handlers[kind] = h
}

// WorkerID derives a distinct locked_by identity per in-process poller.
func (q *Queue) WorkerID(i int) string {
	return fmt.Sprintf("%s-p%d", q.workerID, i)
}

// EnqueueTx inserts a job within an existing transaction (outbox pattern).
// A pg_notify('jobs_added') rides in the same transaction so workers wake
// immediately on commit (poll interval remains as fallback).
// Tenant scoping: jobs that belong to a tenant MUST be enqueued via
// EnqueueTxTenant so jobs.tenant_id is populated and the dead-letter queue
// can be read/replayed per tenant. The legacy wrapper below leaves
// tenant_id NULL (system job) and must only be used for tenant-less work
// (sweep scheduler, digest flush).
func EnqueueTx(ctx context.Context, tx pgx.Tx, kind string, payload any, runAt time.Time) error {
	return EnqueueTxTenant(ctx, tx, nil, "", kind, payload, runAt)
}

// EnqueueTxTenant is EnqueueTx with tenant isolation. tenantID may be nil for
// system jobs. dedupKey is optional ("": no idempotency constraint).
func EnqueueTxTenant(ctx context.Context, tx pgx.Tx, tenantID *uuid.UUID, dedupKey string, kind string, payload any, runAt time.Time) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal job payload: %w", err)
	}
	if runAt.IsZero() {
		runAt = time.Now().UTC()
	}
	var tenantArg any
	if tenantID != nil {
		tenantArg = *tenantID
	}
	var dedupArg any
	if dedupKey != "" {
		dedupArg = dedupKey
	}
	_, err = tx.Exec(ctx,
		`INSERT INTO jobs (kind, payload, run_at, tenant_id, dedup_key) VALUES ($1, $2, $3, $4, $5)`,
		kind, body, runAt, tenantArg, dedupArg)
	if err != nil {
		return fmt.Errorf("enqueue %s: %w", kind, err)
	}
	// Fires only on commit — workers never see uncommitted jobs.
	_, _ = tx.Exec(ctx, `SELECT pg_notify('jobs_added', '')`)
	return nil
}

// Enqueue is a convenience wrapper opening its own transaction.
func (q *Queue) Enqueue(ctx context.Context, kind string, payload any, runAt time.Time) error {
	tx, err := q.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := EnqueueTx(ctx, tx, kind, payload, runAt); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// EnqueueTenant is Enqueue with tenant isolation: the job row carries
// tenant_id so DLQ reads stay per-tenant. Prefer this for all tenant work.
func (q *Queue) EnqueueTenant(ctx context.Context, tenantID uuid.UUID, kind string, payload any, runAt time.Time) error {
	tx, err := q.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := EnqueueTxTenant(ctx, tx, &tenantID, "", kind, payload, runAt); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

type jobRow struct {
	ID           int64
	Kind         string
	Payload      []byte
	Attempts     int
	MaxAttempts  int
}

// claim atomically grabs the next pending job as workerID. Returns nil when idle.
func (q *Queue) claim(ctx context.Context, workerID string) (*jobRow, error) {
	tx, err := q.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	row := tx.QueryRow(ctx, `
		UPDATE jobs SET
			status = 'running',
			locked_by = $1,
			locked_at = now(),
			attempts = attempts + 1,
			updated_at = now()
		WHERE id = (
			SELECT id FROM jobs
			WHERE status = 'pending' AND run_at <= now()
			ORDER BY run_at, id
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		RETURNING id, kind, payload, attempts, max_attempts`,
		workerID)

	var j jobRow
	if err := row.Scan(&j.ID, &j.Kind, &j.Payload, &j.Attempts, &j.MaxAttempts); err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil // idle
		}
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &j, nil
}

// complete marks a job done/failed/dead with backoff calculation.
func (q *Queue) complete(ctx context.Context, j *jobRow, handlerErr error) {
	if handlerErr == nil {
		_, err := q.pool.Exec(ctx,
			`UPDATE jobs SET status='done', locked_by=NULL, locked_at=NULL,
				updated_at=now() WHERE id=$1`, j.ID)
		if err != nil {
			q.log.Error("mark job done", "job_id", j.ID, "err", err)
		}
		return
	}

	dead := j.Attempts >= j.MaxAttempts
	// Exponential backoff: 2^attempts seconds, capped at 5 minutes.
	backoff := time.Duration(1<<uint(min(j.Attempts, 8))) * time.Second
	if backoff > 5*time.Minute {
		backoff = 5 * time.Minute
	}
	status := "pending"
	if dead {
		status = "dead"
	}
	_, err := q.pool.Exec(ctx,
		`UPDATE jobs SET status=$2, run_at=now()+$3::interval,
			last_error=$4, locked_by=NULL, locked_at=NULL, updated_at=now()
		 WHERE id=$1`,
		j.ID, status, backoff.String(), handlerErr.Error())
	if err != nil {
		q.log.Error("mark job failed", "job_id", j.ID, "err", err)
	}
	q.log.Warn("job failed", "job_id", j.ID, "kind", j.Kind,
		"attempt", j.Attempts, "dead", dead, "err", handlerErr)
}

// ReapStuck returns jobs locked by a crashed worker to the pool.
// Run ONE reaper per deployment (see cmd/worker): per-poller reapers would
// stampede the jobs table every minute.
func (q *Queue) ReapStuck(ctx context.Context) {
	_, err := q.pool.Exec(ctx, `
		UPDATE jobs SET status='pending', locked_by=NULL, locked_at=NULL, updated_at=now()
		WHERE status='running' AND locked_at < now() - interval '5 minutes'`)
	if err != nil {
		q.log.Error("reap stuck jobs", "err", err)
	}
}

// ArchiveOld deletes terminal jobs past retention (done>7d, dead>90d).
// jobs grows forever otherwise — the claim index bloats and autovacuum lags.
// Done rows carry no forensic value past a week (dead rows keep 90d for DLQ).
func (q *Queue) ArchiveOld(ctx context.Context) (int64, error) {
	tag, err := q.pool.Exec(ctx, `
		DELETE FROM jobs
		WHERE (status='done' AND updated_at < now() - interval '7 days')
		   OR (status='dead' AND updated_at < now() - interval '90 days')`)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// DeadJob is one row of the dead-letter queue.
type DeadJob struct {
	ID        int64     `json:"id"`
	Kind      string    `json:"kind"`
	Attempts  int       `json:"attempts"`
	LastError *string   `json:"lastError,omitempty"`
	UpdatedAt time.Time `json:"updatedAt"`
	CreatedAt time.Time `json:"createdAt"`
}

// ListDead returns the most recent dead jobs for one tenant (ops DLQ view).
// Tenant scoping is mandatory: a tenant admin must never see another tenant's
// payloads. System jobs (tenant_id IS NULL) are invisible here.
func (q *Queue) ListDead(ctx context.Context, tenantID uuid.UUID, limit int) ([]DeadJob, error) {
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	rows, err := q.pool.Query(ctx, `
		SELECT id, kind, attempts, last_error, updated_at, created_at
		FROM jobs WHERE status='dead' AND tenant_id=$1 ORDER BY updated_at DESC LIMIT $2`, tenantID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DeadJob{}
	for rows.Next() {
		var j DeadJob
		if err := rows.Scan(&j.ID, &j.Kind, &j.Attempts, &j.LastError, &j.UpdatedAt, &j.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// ReplayDead returns one dead job to pending (fresh attempts, immediate run).
// Scoped by tenant: replaying another tenant's job returns not-found.
func (q *Queue) ReplayDead(ctx context.Context, tenantID uuid.UUID, id int64) error {
	tag, err := q.pool.Exec(ctx, `
		UPDATE jobs SET status='pending', attempts=0, run_at=now(),
			locked_by=NULL, locked_at=NULL, last_error=NULL, updated_at=now()
		WHERE id=$1 AND status='dead' AND tenant_id=$2`, id, tenantID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("dead job %d not found", id)
	}
	_, _ = q.pool.Exec(ctx, `SELECT pg_notify('jobs_added', '')`)
	return nil
}

// Run polls for jobs until ctx is cancelled using the queue's workerID.
// Blocks. Prefer RunAs for per-poller IDs (see cmd/worker). No reaper here:
// run ONE reaper per deployment via ReapStuck (worker main owns it).
func (q *Queue) Run(ctx context.Context) {
	q.RunAs(ctx, q.workerID)
}

// RunAs polls until ctx is cancelled as workerID. In-flight jobs finish
// (handler + completion run detached from cancellation with their own
// timeouts) — SIGTERM never loses a claimed job's completion write.
func (q *Queue) RunAs(ctx context.Context, workerID string) {
	q.log.Info("queue worker started", "worker_id", workerID,
		"poll_interval", q.pollInterval)

	wake := make(chan struct{}, 1)
	go q.listenJobs(ctx, wake)

	for {
		if ctx.Err() != nil {
			q.log.Info("queue worker stopping", "worker_id", workerID)
			return
		}
		job, err := q.claim(ctx, workerID)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			q.log.Error("claim job", "err", err)
			sleepOrWake(ctx, wake, q.pollInterval)
			continue
		}
		if job == nil {
			sleepOrWake(ctx, wake, q.pollInterval)
			continue
		}

		q.mu.RLock()
		handler, ok := q.handlers[job.Kind]
		q.mu.RUnlock()

		var herr error
		if !ok {
			herr = fmt.Errorf("no handler registered for kind %q", job.Kind)
		} else {
			// Detached from shutdown: the job was claimed, it must complete.
			hctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			herr = handler(hctx, job.Payload)
			cancel()
		}
		// Completion must survive shutdown too.
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		q.complete(cctx, job, herr)
		cancel()
	}
}

// listenJobs LISTENs for jobs_added and nudges wake (non-blocking).
// Reconnects with backoff; the poll interval remains the fallback so a missed
// notify only costs latency, never a job.
func (q *Queue) listenJobs(ctx context.Context, wake chan struct{}) {
	notify := func() { select { case wake <- struct{}{}: default: } }
	for ctx.Err() == nil {
		conn, err := q.pool.Acquire(ctx)
		if err != nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
				continue
			}
		}
		_, err = conn.Exec(ctx, `LISTEN jobs_added`)
		if err != nil {
			conn.Release()
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
				continue
			}
		}
		func() {
			defer conn.Release()
			for ctx.Err() == nil {
				_, err := conn.Conn().WaitForNotification(ctx)
				if err != nil {
					return // reconnect
				}
				notify()
			}
		}()
	}
}

func sleepOrWake(ctx context.Context, wake chan struct{}, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-wake:
	case <-time.After(d):
	}
}
