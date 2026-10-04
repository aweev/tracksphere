// Package db owns the PostgreSQL connection pool, the embedded migration
// runner and the multi-tenant row-level-security context helper.
package db

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Open builds a pool and verifies connectivity, retrying for up to ~30s so
// that API/worker can start while the database container is still booting.
// Pool size is tunable via TRACKSPHERE_DB_MAX_CONNS (default: calculated from
// worker/API concurrency); every connection sets statement_timeout=5s as a
// runaway-query guard (the role default in 000009 is belt-and-braces for
// non-pool clients).
func Open(ctx context.Context, dsn string, workerConcurrency, apiWorkers int) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	maxConns := 10
	if raw := os.Getenv("TRACKSPHERE_DB_MAX_CONNS"); raw != "" {
		if n, cerr := strconv.Atoi(raw); cerr == nil && n >= 1 && n <= 500 {
			maxConns = n
		}
	} else {
		// Auto-calculate recommended pool size
		maxConns = calculateRecommendedPoolSize(workerConcurrency, apiWorkers)
	}
	cfg.MaxConns = int32(maxConns)
	cfg.MinConns = 1
	cfg.MaxConnLifetime = time.Hour
	cfg.MaxConnIdleTime = 15 * time.Minute
	cfg.AfterConnect = func(ctx context.Context, c *pgx.Conn) error {
		_, err := c.Exec(ctx, `SET statement_timeout = '5s'`)
		return err
	}

	var pool *pgxpool.Pool
	deadline := time.Now().Add(30 * time.Second)
	for attempt := 1; ; attempt++ {
		pool, err = pgxpool.NewWithConfig(ctx, cfg)
		if err == nil {
			pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			err = pool.Ping(pingCtx)
			cancel()
		}
		if err == nil {
			return pool, nil
		}
		if pool != nil {
			pool.Close()
			pool = nil
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return nil, fmt.Errorf("connect to database: %w", err)
		}
		backoff := time.Duration(attempt*attempt) * 250 * time.Millisecond
		if backoff > 3*time.Second {
			backoff = 3 * time.Second
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(backoff):
		}
	}
}

// CalculateRecommendedPoolSize returns the recommended max connections based on
// the application's concurrency requirements.
// Formula: (API workers + worker pollers + background tasks) × safety factor
// Default safety factor = 2
func CalculateRecommendedPoolSize(workerConcurrency int, apiWorkers int) int {
	// API needs connections for HTTP handlers + SSE hub + metrics
	apiConns := apiWorkers + 2 // +2 for SSE hub, metrics
	
	// Worker needs connections for pollers + reaper + archiver + LISTEN + scheduler
	workerConns := workerConcurrency + 4 // +4 for background tasks
	
	// Total with safety factor
	total := (apiConns + workerConns) * 2
	
	// Cap at reasonable maximum
	if total > 100 {
		total = 100
	}
	if total < 10 {
		total = 10
	}
	return total
}

// PoolStats holds extended pool statistics for monitoring
type PoolStats struct {
	AcquiredConns  int32
	IdleConns      int32
	TotalConns     int32
	MaxConns       int32
	UtilizationPct float64
}

// GetPoolStats returns extended statistics for the pool
func GetPoolStats(pool *pgxpool.Pool) PoolStats {
	if pool == nil {
		return PoolStats{}
	}
	stats := pool.Stat()
	utilization := float64(0)
	maxConns := stats.MaxConns()
	if maxConns > 0 {
		utilization = float64(stats.AcquiredConns()) / float64(maxConns) * 100
	}
	return PoolStats{
		AcquiredConns:  stats.AcquiredConns(),
		IdleConns:      stats.IdleConns(),
		TotalConns:     stats.TotalConns(),
		MaxConns:       maxConns,
		UtilizationPct: utilization,
	}
}

// WithTenant opens a transaction, pins the row-level-security tenant context
// (app.tenant_id) to that transaction, and runs fn. Every tenant-scoped query
// in the codebase MUST go through this helper: RLS policies are FORCE'd, so a
// connection without the setting fails closed (zero rows) instead of leaking
// data across tenants.
func WithTenant(ctx context.Context, pool *pgxpool.Pool, tenantID uuid.UUID, fn func(pgx.Tx) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tenant tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := SetTenant(ctx, tx, tenantID); err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// querier is the minimal surface SetTenant/SetSystem need (satisfied by
// pgx.Tx, *pgx.Conn and *pgxpool.Pool). pgx v5 exports no such interface.
type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// SetTenant pins app.tenant_id to the current transaction only (is_local=true),
// so the setting never leaks onto the pooled connection after commit/rollback.
func SetTenant(ctx context.Context, q querier, tenantID uuid.UUID) error {
	var unused string
	if err := q.QueryRow(ctx,
		`SELECT set_config('app.tenant_id', $1, true)`, tenantID.String(),
	).Scan(&unused); err != nil {
		return fmt.Errorf("set tenant context: %w", err)
	}
	return nil
}

// SetSystem raises a transaction-local read bypass (app.system='on') used
// ONLY by bootstrap paths that must see rows before a tenant context exists:
//   - the carrier-webhook resolver (lookup by tracking number), and
//   - tenant creation (register/seed): the new tenant's id cannot equal
//     current_tenant() before it exists, so the tenants policy admits the
//     INSERT only under this flag.
// Callers MUST pin the resolved/created tenant with SetTenant before any
// business-table write — WITH CHECK policies reject un-pinned writes.
func SetSystem(ctx context.Context, q querier) error {
	var unused string
	if err := q.QueryRow(ctx,
		`SELECT set_config('app.system', 'on', true)`).Scan(&unused); err != nil {
		return fmt.Errorf("set system context: %w", err)
	}
	return nil
}

// SetPublicAccess raises the transaction-local portal flag
// (app.public_access='on'): read-only visibility of is_public=true shipments
// and their timelines WITHOUT a tenant. Never combine with SetTenant — tenant
// queries must not unlock other tenants' public rows.
func SetPublicAccess(ctx context.Context, q querier) error {
	var unused string
	if err := q.QueryRow(ctx,
		`SELECT set_config('app.public_access', 'on', true)`).Scan(&unused); err != nil {
		return fmt.Errorf("set public access context: %w", err)
	}
	return nil
}

// ClearSystem drops the system bypass (transaction-local) after the caller
// has pinned a tenant, restoring least privilege for the rest of the tx.
func ClearSystem(ctx context.Context, q querier) error {
	var unused string
	if err := q.QueryRow(ctx,
		`SELECT set_config('app.system', 'off', true)`).Scan(&unused); err != nil {
		return fmt.Errorf("clear system context: %w", err)
	}
	return nil
}

// VerifyTenantCascade reports any tenant-scoped table still holding rows for
// tenantID. The table list is derived from the catalogue (every table in
// public with a tenant_id column), so a future migration that adds a tenant
// table cannot be silently forgotten the way the dropped 000018 function was:
// it hardcoded an array that named a table which does not exist, and raised
// on its first iteration.
//
// The caller must already hold a transaction with both app.system and
// app.tenant_id set. app.system admits the tenants-table read; app.tenant_id
// admits the child tables, because app.system is not a universal bypass —
// eight tenant tables have policies with no app.system branch at all, and a
// check relying on it alone would read zero rows from exactly those tables and
// pass falsely.
func VerifyTenantCascade(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) ([]string, error) {
	rows, err := tx.Query(ctx, `
		SELECT c.relname
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'public'
		  AND c.relkind = 'r'
		  AND EXISTS (
			SELECT 1 FROM pg_attribute a
			WHERE a.attrelid = c.oid AND a.attname = 'tenant_id'
			  AND NOT a.attisdropped AND a.attnum > 0
		  )
		ORDER BY c.relname`)
	if err != nil {
		return nil, fmt.Errorf("list tenant tables: %w", err)
	}
	var tables []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			rows.Close()
			return nil, fmt.Errorf("read tenant tables: %w", err)
		}
		tables = append(tables, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read tenant tables: %w", err)
	}

	var remaining []string
	for _, t := range tables {
		// Identifiers cannot be parameterised; t comes from pg_class, and
		// Sanitize quotes it regardless.
		q := fmt.Sprintf(`SELECT count(*) FROM %s WHERE tenant_id = $1`,
			pgx.Identifier{t}.Sanitize())
		var n int64
		if err := tx.QueryRow(ctx, q, tenantID).Scan(&n); err != nil {
			return nil, fmt.Errorf("count %s: %w", t, err)
		}
		if n > 0 {
			remaining = append(remaining, fmt.Sprintf("%s=%d", t, n))
		}
	}
	return remaining, nil
}

// calculateRecommendedPoolSize returns the recommended max connections based on
// the application's concurrency requirements.
// Formula: (API workers + worker pollers + background tasks) × safety factor
// Default safety factor = 2
func calculateRecommendedPoolSize(workerConcurrency, apiWorkers int) int {
	// API needs connections for HTTP handlers + SSE hub + metrics
	apiConns := apiWorkers + 2 // +2 for SSE hub, metrics
	
	// Worker needs connections for pollers + reaper + archiver + LISTEN + scheduler
	workerConns := workerConcurrency + 4 // +4 for background tasks
	
	// Total with safety factor
	total := (apiConns + workerConns) * 2
	
	// Cap at reasonable maximum
	if total > 100 {
		total = 100
	}
	if total < 10 {
		total = 10
	}
	return total
}