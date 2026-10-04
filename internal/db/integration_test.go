package db_test

// Integration tests for the erasure and audit paths. These run against a real
// PostgreSQL and skip gracefully when none is reachable, so `go test ./...`
// stays green on machines without a database while CI (which provisions one)
// exercises them.
//
// Each test uses a probe tenant with a random slug and removes it afterwards,
// so they are safe to run against a dev database.

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tracksphere/tracksphere/internal/db"
)

// testPool connects to the dev database, skipping when unreachable.
func testPool(t *testing.T) *pgxpool.Pool {
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

// seedProbeTenant creates a tenant with a shipment, an event, an alert, a
// notification and a subscription — enough rows across the cascade to prove
// that an erase either removes everything or is reported as incomplete.
func seedProbeTenant(t *testing.T, ctx context.Context, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	tenantID := uuid.New()
	slug := "probe-" + tenantID.String()[:8]

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
		tenantID, "Probe Tenant", slug); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	if err := db.WithTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
		var shipmentID uuid.UUID
		if err := tx.QueryRow(ctx, `
			INSERT INTO shipments (tenant_id, tracking_number, carrier, mode, origin, destination)
			VALUES ($1, $2, 'probe-carrier', 'road', 'Lagos', 'Douala')
			RETURNING id`,
			tenantID, "PROBE-"+tenantID.String()[:8]).Scan(&shipmentID); err != nil {
			return fmt.Errorf("seed shipment: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO shipment_events
				(tenant_id, shipment_id, carrier, code, occurred_at, dedup_key)
			VALUES ($1, $2, 'probe-carrier', 'DEPARTED', now(), $3)`,
			tenantID, shipmentID, "probe:"+tenantID.String()); err != nil {
			return fmt.Errorf("seed event: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO alerts (tenant_id, shipment_id, kind, severity, status, title, detected_at)
			VALUES ($1, $2, 'stale', 'warning', 'open', 'probe', now())`,
			tenantID, shipmentID); err != nil {
			return fmt.Errorf("seed alert: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO notifications (tenant_id, shipment_id, channel, recipient, subject, body, provider)
			VALUES ($1, $2, 'email', 'probe@example.com', 'probe', 'probe', 'log')`,
			tenantID, shipmentID); err != nil {
			return fmt.Errorf("seed notification: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO tracking_subscriptions
				(tenant_id, shipment_id, channel, recipient, recipient_hash, status)
			VALUES ($1, $2, 'email', 'probe@example.com',
				'0000000000000000000000000000000000000000000000000000000000000000',
				'active')`,
			tenantID, shipmentID); err != nil {
			return fmt.Errorf("seed subscription: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO shipment_audit (tenant_id, shipment_id, action, detail)
			VALUES ($1, $2, 'probe', '{}')`,
			tenantID, shipmentID); err != nil {
			return fmt.Errorf("seed audit: %w", err)
		}
		return nil
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
	return tenantID
}

// eraseTenant replicates handleEraseAccount's exact sequence: SetSystem and
// SetTenant, DELETE, assert one row, verify the cascade in-transaction.
func eraseTenant(t *testing.T, ctx context.Context, pool *pgxpool.Pool, tenantID uuid.UUID) ([]string, error) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := db.SetSystem(ctx, tx); err != nil {
		return nil, err
	}
	if err := db.SetTenant(ctx, tx, tenantID); err != nil {
		return nil, err
	}
	tag, err := tx.Exec(ctx, `DELETE FROM tenants WHERE id=$1`, tenantID)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() != 1 {
		return nil, fmt.Errorf("expected 1 deleted tenant row, got %d", tag.RowsAffected())
	}
	remaining, err := db.VerifyTenantCascade(ctx, tx, tenantID)
	if err != nil {
		return nil, err
	}
	if len(remaining) > 0 {
		return remaining, fmt.Errorf("leftover rows: %v", remaining)
	}
	return nil, tx.Commit(ctx)
}

// TestUnpinnedDeleteTouchesNothing documents the original erasure bug: a
// DELETE issued without app.system or app.tenant_id matches zero rows and
// raises no error, so a handler that does not check RowsAffected reports
// success having erased nothing.
func TestUnpinnedDeleteTouchesNothing(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tenantID := seedProbeTenant(t, ctx, pool)

	tag, err := pool.Exec(ctx, `DELETE FROM tenants WHERE id=$1`, tenantID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tag.RowsAffected() != 0 {
		t.Fatalf("expected the unpinned DELETE to touch 0 rows, got %d", tag.RowsAffected())
	}

	var stillThere int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM tenants WHERE id=$1`, tenantID).Scan(&stillThere); err != nil {
		// tenants has RLS; without context the count itself must read 0.
		t.Logf("unpinned count read: %v (also fail-closed, as designed)", err)
		return
	}
	if stillThere != 0 {
		t.Fatalf("unpinned read saw the tenant; RLS may be off: %d", stillThere)
	}
}

// TestEraseDeletesEverythingAndVerifiesClean is the regression test for the
// GDPR erasure fix: the full handler sequence must delete the tenant row,
// cascade to every child table, and verify empty.
func TestEraseDeletesEverythingAndVerifiesClean(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tenantID := seedProbeTenant(t, ctx, pool)

	if _, err := eraseTenant(t, ctx, pool, tenantID); err != nil {
		t.Fatalf("erase failed: %v", err)
	}

	// Confirm from the catalogue side: no table holds this tenant's rows.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := db.SetSystem(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if err := db.SetTenant(ctx, tx, tenantID); err != nil {
		t.Fatal(err)
	}
	remaining, err := db.VerifyTenantCascade(ctx, tx, tenantID)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) > 0 {
		t.Fatalf("rows survived erasure: %v", remaining)
	}
}

// TestCascadeFinderSeesInternalTables guards the catalogue-driven check
// itself: the tables whose policies lack an app.system branch
// (tracking_subscriptions, shipment_audit and friends) must still be counted,
// or verification would pass falsely.
func TestCascadeFinderSeesInternalTables(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tenantID := seedProbeTenant(t, ctx, pool)

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := db.SetSystem(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if err := db.SetTenant(ctx, tx, tenantID); err != nil {
		t.Fatal(err)
	}
	remaining, err := db.VerifyTenantCascade(ctx, tx, tenantID)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, r := range remaining {
		for _, want := range []string{"shipments", "shipment_events", "alerts",
			"notifications", "tracking_subscriptions", "shipment_audit"} {
			if len(r) >= len(want) && r[:len(want)] == want {
				found[want] = true
			}
		}
	}
	for _, want := range []string{"shipments", "shipment_events", "alerts",
		"notifications", "tracking_subscriptions", "shipment_audit"} {
		if !found[want] {
			t.Errorf("cascade check missed %s (got %v); a system-only check would false-pass here", want, remaining)
		}
	}
}
