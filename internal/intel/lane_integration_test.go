package intel

// Proves lane stats are pre-aggregated and read as one row. Skips without a
// database.
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

func lanePool(t *testing.T) *pgxpool.Pool {
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

// TestLaneStatsPreAggregated seeds six delivered shipments on one lane,
// refreshes, and requires LaneStatsFor to return their percentiles from the
// table — without scanning the lane. A seventh shipment on a different lane
// with too few samples must yield nil.
func TestLaneStatsPreAggregated(t *testing.T) {
	pool := lanePool(t)
	ctx := context.Background()

	tenantID := uuid.New()
	slug := "ln-" + tenantID.String()[:8]
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
		tenantID, "Lane Probe", slug); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
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

	// Six delivered shipments, 10..15 day transits.
	if err := db.WithTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
		for i := 0; i < 6; i++ {
			days := 10 + i
			if _, err := tx.Exec(ctx, `
				INSERT INTO shipments
					(tenant_id, tracking_number, carrier, mode, origin, destination,
					 status, shipped_at, delivered_at)
				VALUES ($1, $2, 'probe-carrier', 'ocean', 'Lagos', 'Rotterdam',
				        'delivered', now() - make_interval(days => $3), now())`,
				tenantID, "LN-"+tenantID.String()[:4]+"-"+fmt.Sprintf("%02d", i), days); err != nil {
				return err
			}
		}
		// One lonely shipment on another lane.
		_, err := tx.Exec(ctx, `
			INSERT INTO shipments
				(tenant_id, tracking_number, carrier, mode, origin, destination,
				 status, shipped_at, delivered_at)
			VALUES ($1, $2, 'probe-carrier', 'ocean', 'Lagos', 'Nowhere',
			        'delivered', now() - interval '10 days', now())`,
			tenantID, "LN-"+tenantID.String()[:4]+"-solo")
		return err
	}); err != nil {
		t.Fatal(err)
	}

	if err := db.WithTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
		return RefreshLaneStats(ctx, tx, tenantID, "Lagos", "Rotterdam", "probe-carrier", "ocean")
	}); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	var got *LaneStats
	if err := db.WithTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
		got = LaneStatsFor(ctx, tx, "Lagos", "Rotterdam", "probe-carrier", "ocean")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatalf("expected lane stats for 6-sample lane, got nil")
	}
	if got.Samples != 6 {
		t.Errorf("samples=%d, want 6", got.Samples)
	}
	// 10..15 days: p50 ≈ 12.5, p90 ≈ 14.5 (SQL percentile_cont interpolates).
	if got.P50Days < 12 || got.P50Days > 13 {
		t.Errorf("p50=%v, want ~12.5", got.P50Days)
	}
	if got.P90Days < 14 || got.P90Days > 15 {
		t.Errorf("p90=%v, want ~14.5", got.P90Days)
	}

	// Carrier-agnostic row must also exist (same lane, empty carrier).
	var agnostic *LaneStats
	if err := db.WithTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
		agnostic = LaneStatsFor(ctx, tx, "Lagos", "Rotterdam", "other-carrier", "ocean")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if agnostic == nil || agnostic.Samples != 6 {
		t.Errorf("carrier-agnostic fallback missing or wrong: %+v", agnostic)
	}

	// Solo lane: too few samples, must be nil.
	var solo *LaneStats
	if err := db.WithTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
		solo = LaneStatsFor(ctx, tx, "Lagos", "Nowhere", "probe-carrier", "ocean")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if solo != nil {
		t.Errorf("single-sample lane must yield nil, got %+v", solo)
	}
}
