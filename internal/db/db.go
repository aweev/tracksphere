// Package db owns the PostgreSQL connection pool, the embedded migration
// runner and the multi-tenant row-level-security context helper.
package db

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Open builds a pool and verifies connectivity, retrying for up to ~30s so
// that API/worker can start while the database container is still booting.
func Open(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	cfg.MaxConns = 10
	cfg.MinConns = 1
	cfg.MaxConnLifetime = time.Hour
	cfg.MaxConnIdleTime = 15 * time.Minute

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
