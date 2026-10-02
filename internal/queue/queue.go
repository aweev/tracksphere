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

// Register binds a handler to a job kind. Must be called before Run.
func (q *Queue) Register(kind string, h Handler) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.handlers[kind] = h
}

// EnqueueTx inserts a job within an existing transaction (outbox pattern).
func EnqueueTx(ctx context.Context, tx pgx.Tx, kind string, payload any, runAt time.Time) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal job payload: %w", err)
	}
	if runAt.IsZero() {
		runAt = time.Now().UTC()
	}
	_, err = tx.Exec(ctx,
		`INSERT INTO jobs (kind, payload, run_at) VALUES ($1, $2, $3)`,
		kind, body, runAt)
	if err != nil {
		return fmt.Errorf("enqueue %s: %w", kind, err)
	}
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

type jobRow struct {
	ID           int64
	Kind         string
	Payload      []byte
	Attempts     int
	MaxAttempts  int
}

// claim atomically grabs the next pending job. Returns nil when idle.
func (q *Queue) claim(ctx context.Context) (*jobRow, error) {
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
		q.workerID)

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

// reapStuck returns jobs locked by a crashed worker to the pool.
func (q *Queue) reapStuck(ctx context.Context) {
	_, err := q.pool.Exec(ctx, `
		UPDATE jobs SET status='pending', locked_by=NULL, locked_at=NULL, updated_at=now()
		WHERE status='running' AND locked_at < now() - interval '5 minutes'`)
	if err != nil {
		q.log.Error("reap stuck jobs", "err", err)
	}
}

// Run polls for jobs until ctx is cancelled. Blocks.
func (q *Queue) Run(ctx context.Context) {
	q.log.Info("queue worker started", "worker_id", q.workerID,
		"poll_interval", q.pollInterval)

	reapTicker := time.NewTicker(time.Minute)
	defer reapTicker.Stop()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-reapTicker.C:
				q.reapStuck(ctx)
			}
		}
	}()

	for {
		if ctx.Err() != nil {
			q.log.Info("queue worker stopping", "worker_id", q.workerID)
			return
		}
		job, err := q.claim(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			q.log.Error("claim job", "err", err)
			time.Sleep(q.pollInterval)
			continue
		}
		if job == nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(q.pollInterval):
			}
			continue
		}

		q.mu.RLock()
		handler, ok := q.handlers[job.Kind]
		q.mu.RUnlock()

		var herr error
		if !ok {
			herr = fmt.Errorf("no handler registered for kind %q", job.Kind)
		} else {
			hctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			herr = handler(hctx, job.Payload)
			cancel()
		}
		q.complete(ctx, job, herr)
	}
}
