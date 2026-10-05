package readmodel

// Scale proof that the read-model batch covers every dirty row regardless of
// count. Opt-in via TRACKSPHERE_SCALE_TEST=1 (with a live database); skipped
// otherwise so ordinary `go test` stays fast.
//
// The ceiling this guards was a LIMIT-only batch: at 50k shipments 96% of a
// tenant never refreshed. The batch is now a bounded queue driven by
// needs_refresh, so coverage must be total and passes must be exactly
// ceil(dirty/limit).
import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/tracksphere/tracksphere/internal/db"
)

const scaleShipments = 5000
const scaleWindow = 2000

func TestScaleRefreshCoversAllDirty(t *testing.T) {
	if os.Getenv("TRACKSPHERE_SCALE_TEST") == "" {
		t.Skip("opt-in scale test: TRACKSPHERE_SCALE_TEST=1 with a live database")
	}
	pool := refreshPool(t)
	ctx := context.Background()

	tenantID := uuid.New()
	slug := "sc-" + tenantID.String()[:8]
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
		tenantID, "Scale Probe", slug); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
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

	// Bulk seed via generate_series: one statement, no per-row round trips.
	// needs_refresh defaults true, so every row queues itself.
	start := time.Now()
	if err := db.WithTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, fmt.Sprintf(`
			INSERT INTO shipments (tenant_id, tracking_number, carrier, mode, shipped_at)
			SELECT $1, 'SCALE-%s-' || g, 'probe-carrier', 'road',
			       now() - (g || ' hours')::interval
			FROM generate_series(1, %d) g`, tenantID.String()[:4], scaleShipments),
			tenantID)
		return err
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	t.Logf("seeded %d shipments in %v", scaleShipments, time.Since(start))

	// Drain the queue in windows; expect exactly ceil(5000/2000) = 3 passes
	// of 2000, 2000, 1000, then a clean fourth.
	var passes []int
	for i := 0; i < 5; i++ {
		var n int
		if err := db.WithTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
			var err error
			n, err = RefreshBatch(ctx, tx, scaleWindow)
			return err
		}); err != nil {
			t.Fatalf("pass %d: %v", i, err)
		}
		passes = append(passes, n)
		if n == 0 {
			break
		}
	}
	want := []int{2000, 2000, 1000, 0}
	if fmt.Sprint(passes) != fmt.Sprint(want) {
		t.Fatalf("passes refreshed %v, want %v", passes, want)
	}

	// Every row must now have a current row and a cleared flag.
	var unrefreshed, dirty int
	if err := db.WithTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM shipments s
			LEFT JOIN shipment_current c ON c.shipment_id = s.id
			WHERE s.tenant_id=$1 AND c.shipment_id IS NULL`, tenantID).Scan(&unrefreshed); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `
			SELECT count(*) FROM shipments WHERE tenant_id=$1 AND needs_refresh`,
			tenantID).Scan(&dirty)
	}); err != nil {
		t.Fatal(err)
	}
	if unrefreshed != 0 || dirty != 0 {
		t.Fatalf("incomplete coverage: %d without current row, %d still dirty", unrefreshed, dirty)
	}
	t.Logf("covered %d shipments in %d passes", scaleShipments, len(passes)-1)
}
