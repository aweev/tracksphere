package workers

// Proves the sweep covers every active shipment regardless of count, and that
// auto-resolve cannot close alerts for rules that did not run. Skips without
// a database.
import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tracksphere/tracksphere/internal/db"
	"github.com/tracksphere/tracksphere/internal/readmodel"
)

func logForTest() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func sweepPool(t *testing.T) *pgxpool.Pool {
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

func seedSweepTenant(t *testing.T, ctx context.Context, pool *pgxpool.Pool, n int) uuid.UUID {
	t.Helper()
	tenantID := uuid.New()
	slug := "sw-" + tenantID.String()[:8]

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
		tenantID, "Sweep Probe", slug); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	if err := db.WithTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
		for i := 0; i < n; i++ {
			// New rows are dirty by default (migration 000020 backfill), so
			// no explicit Refresh here — the test proves the batch finds them.
			if _, err := tx.Exec(ctx, `
				INSERT INTO shipments (tenant_id, tracking_number, carrier, mode)
				VALUES ($1, $2, 'probe-carrier', 'road')`,
				tenantID, fmt.Sprintf("SW-%s-%04d", tenantID.String()[:4], i)); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		cctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		tx, err := pool.Begin(cctx)
		if err != nil {
			return
		}
		defer func() { _ = tx.Rollback(cctx) }()
		_ = db.SetSystem(cctx, tx)
		_ = db.SetTenant(cctx, tx, tenantID)
		_, _ = tx.Exec(cctx, `DELETE FROM tenants WHERE id=$1`, tenantID)
		_ = tx.Commit(cctx)
	})
	return tenantID
}

// TestSweepCoversBeyondLimit seeds more shipments than a tiny window and
// requires the batch to reach all of them across successive windows. Under the
// old LIMIT-only candidate query, anything past the limit was invisible to
// detection. New rows are dirty by default (migration 000020 backfill), so no
// explicit refresh is needed before the first batch.
func TestSweepCoversBeyondLimit(t *testing.T) {
	pool := sweepPool(t)
	ctx := context.Background()
	tenantID := seedSweepTenant(t, ctx, pool, 5)

	// Windows of 2 over 5 dirty rows must yield 2, 2, 1, then 0 — complete
	// coverage with no gaps and no repeats.
	want := []int{2, 2, 1, 0}
	for i, w := range want {
		var got int
		if err := db.WithTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
			var err error
			got, err = readmodel.RefreshBatch(ctx, tx, 2)
			return err
		}); err != nil {
			t.Fatalf("window %d: %v", i, err)
		}
		if got != w {
			t.Fatalf("window %d refreshed %d, want %d", i, got, w)
		}
	}
}

// TestAutoResolveIgnoresUnevaluatedKinds proves a rule skipped for cadence
// does not have its alerts closed. An alert of a kind with no due rule must
// survive autoResolve even when stale.
func TestAutoResolveIgnoresUnevaluatedKinds(t *testing.T) {
	pool := sweepPool(t)
	ctx := context.Background()
	tenantID := seedSweepTenant(t, ctx, pool, 1)

	var shipmentID uuid.UUID
	if err := db.WithTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT id FROM shipments WHERE tenant_id=$1 LIMIT 1`,
			tenantID).Scan(&shipmentID)
	}); err != nil {
		t.Fatal(err)
	}

	// Open a stale-kind alert with an old last_seen_at, as if a previous pass
	// raised it and the rule has not run since.
	if err := db.WithTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO alerts (tenant_id, shipment_id, kind, severity, status, title, detected_at, last_seen_at)
			VALUES ($1, $2, 'stale', 'warning', 'open', 'probe', now() - interval '2 hours', now() - interval '2 hours')`,
			tenantID, shipmentID)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	// autoResolve with no evaluated kinds must close nothing.
	if err := db.WithTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
		n, err := autoResolve(ctx, tx, logForTest(), tenantID, nil)
		if err != nil {
			return err
		}
		if n != 0 {
			t.Fatalf("autoResolve with no kinds closed %d alerts; want 0", n)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// And with the kind evaluated, the stale alert must close.
	if err := db.WithTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
		n, err := autoResolve(ctx, tx, logForTest(), tenantID, []string{"stale"})
		if err != nil {
			return err
		}
		if n != 1 {
			t.Fatalf("autoResolve with kind closed %d alerts; want 1", n)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
