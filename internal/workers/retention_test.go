package workers_test

// Regression test for the retention sweep. The three RLS tables used to run
// unpinned on the pool and therefore delete zero rows while the compliance
// document advertised the windows as live. This test seeds rows on both sides
// of every window and asserts exactly the expired ones disappear — a sweep
// that deletes nothing and a sweep that deletes everything both fail here.

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tracksphere/tracksphere/internal/db"
	"github.com/tracksphere/tracksphere/internal/workers"
)

func retentionPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TRACKSPHERE_TEST_DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://tracksphere_app:tracksphere_app@localhost:5433/tracksphere?sslmode=disable"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skipf("no test database: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("no test database: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func seedRetentionTenant(t *testing.T, ctx context.Context, pool *pgxpool.Pool) (uuid.UUID, uuid.UUID) {
	t.Helper()
	tenantID := uuid.New()
	slug := "ret-" + tenantID.String()[:8]

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := db.SetSystem(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO tenants (id, name, slug) VALUES ($1, $2, $3)`,
		tenantID, "Retention Probe", slug); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	var shipmentID uuid.UUID
	if err := db.WithTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			INSERT INTO shipments (tenant_id, tracking_number, carrier, mode)
			VALUES ($1, $2, 'probe-carrier', 'road') RETURNING id`,
			tenantID, "RET-"+tenantID.String()[:8]).Scan(&shipmentID)
	}); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		tx, err := pool.Begin(cleanupCtx)
		if err != nil {
			return
		}
		defer func() { _ = tx.Rollback(cleanupCtx) }()
		_ = db.SetSystem(cleanupCtx, tx)
		_ = db.SetTenant(cleanupCtx, tx, tenantID)
		_, _ = tx.Exec(cleanupCtx, `DELETE FROM tenants WHERE id=$1`, tenantID)
		_ = tx.Commit(cleanupCtx)
	})
	return tenantID, shipmentID
}

func mustExec(t *testing.T, ctx context.Context, pool *pgxpool.Pool, tenantID uuid.UUID, q string, args ...any) {
	t.Helper()
	if err := db.WithTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, q, args...)
		return err
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

func countWhere(t *testing.T, ctx context.Context, pool *pgxpool.Pool, tenantID uuid.UUID, table, cond string) int {
	t.Helper()
	var n int
	if err := db.WithTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT count(*) FROM `+table+` WHERE `+cond).Scan(&n)
	}); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// TestRetentionSweepPrunesExpiredOnly seeds one row on each side of every
// window and requires the sweep to remove exactly the expired half.
func TestRetentionSweepPrunesExpiredOnly(t *testing.T) {
	pool := retentionPool(t)
	ctx := context.Background()
	tenantID, shipmentID := seedRetentionTenant(t, ctx, pool)

	old := func(days int) time.Time { return time.Now().AddDate(0, 0, -days) }

	mustExec(t, ctx, pool, tenantID, `
		INSERT INTO notifications (tenant_id, shipment_id, channel, recipient, subject, body, provider, created_at)
		VALUES ($1, $2, 'email', 'old@example.com', 'old', 'old', 'log', $3)`,
		tenantID, shipmentID, old(200))
	mustExec(t, ctx, pool, tenantID, `
		INSERT INTO notifications (tenant_id, shipment_id, channel, recipient, subject, body, provider, created_at)
		VALUES ($1, $2, 'email', 'fresh@example.com', 'fresh', 'fresh', 'log', $3)`,
		tenantID, shipmentID, old(10))

	mustExec(t, ctx, pool, tenantID, `
		INSERT INTO idempotency_keys (tenant_id, key, created_at)
		VALUES ($1, 'old-key', $2)`, tenantID, old(30))
	mustExec(t, ctx, pool, tenantID, `
		INSERT INTO idempotency_keys (tenant_id, key, created_at)
		VALUES ($1, 'fresh-key', $2)`, tenantID, old(1))

	// Global (non-RLS) tables seed at pool level.
	if _, err := pool.Exec(ctx, `
		INSERT INTO webhook_inbox (carrier, signature_valid, payload, received_at, processed)
		VALUES ('probe', true, '{}', $1, true)`, old(100)); err != nil {
		t.Fatalf("seed inbox old: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO webhook_inbox (carrier, signature_valid, payload, received_at, processed)
		VALUES ('probe', true, '{}', $1, false)`, old(100)); err != nil {
		t.Fatalf("seed inbox unprocessed: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO auth_events (kind, created_at) VALUES ('login_failed', $1)`, old(400)); err != nil {
		t.Fatalf("seed auth old: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO auth_events (kind, created_at) VALUES ('login', $1)`, old(10)); err != nil {
		t.Fatalf("seed auth fresh: %v", err)
	}

	pruned, err := workers.RetentionSweep(ctx, pool)
	if err != nil {
		t.Fatalf("sweep failed: %v", err)
	}
	if pruned < 3 {
		t.Fatalf("expected at least 3 pruned rows (2 RLS + 1 inbox + 1 auth), got %d", pruned)
	}

	// Expired rows must be gone.
	if n := countWhere(t, ctx, pool, tenantID, "notifications", "recipient='old@example.com'"); n != 0 {
		t.Errorf("old notification survived: %d", n)
	}
	if n := countWhere(t, ctx, pool, tenantID, "idempotency_keys", "key='old-key'"); n != 0 {
		t.Errorf("old idempotency key survived: %d", n)
	}

	// Fresh rows must be kept. A sweep that deletes everything would pass a
	// naive expired-only assertion, so this half is load-bearing.
	if n := countWhere(t, ctx, pool, tenantID, "notifications", "recipient='fresh@example.com'"); n != 1 {
		t.Errorf("fresh notification wrongfully pruned or duplicated: %d", n)
	}
	if n := countWhere(t, ctx, pool, tenantID, "idempotency_keys", "key='fresh-key'"); n != 1 {
		t.Errorf("fresh idempotency key wrongfully pruned or duplicated: %d", n)
	}

	var inboxOld, inboxUnprocessed, authOld, authFresh int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM webhook_inbox WHERE received_at < now() - interval '90 days' AND processed=true`).Scan(&inboxOld)
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM webhook_inbox WHERE processed=false`).Scan(&inboxUnprocessed)
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM auth_events WHERE kind='login_failed' AND created_at < now() - interval '365 days'`).Scan(&authOld)
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM auth_events WHERE kind='login'`).Scan(&authFresh)
	if inboxOld != 0 {
		t.Errorf("old processed inbox row survived")
	}
	if inboxUnprocessed == 0 {
		t.Errorf("unprocessed inbox row was pruned; only processed=true rows may go")
	}
	if authOld != 0 {
		t.Errorf("old auth event survived")
	}
	if authFresh == 0 {
		t.Errorf("fresh auth event was pruned")
	}
}
