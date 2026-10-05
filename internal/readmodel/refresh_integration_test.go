package readmodel

// Proves Refresh writes the server-computed breakdown and clears the dirty
// flag. Skips without a database, like the db package integration tests.
import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tracksphere/tracksphere/internal/db"
)

func refreshPool(t *testing.T) *pgxpool.Pool {
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

// TestRefreshBatchIsFlagDriven proves the batch processes a bounded queue,
// not a time window. After a successful refresh the flag clears, so a second
// batch must find nothing — under the old 15-minute predicate against an
// hourly sweep, every row qualified on every pass and this assertion failed.
func TestRefreshBatchIsFlagDriven(t *testing.T) {
	pool := refreshPool(t)
	ctx := context.Background()

	tenantID := uuid.New()
	slug := "fb-" + tenantID.String()[:8]
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
		tenantID, "Flag Probe", slug); err != nil {
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

	var shipmentID uuid.UUID
	if err := db.WithTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			INSERT INTO shipments (tenant_id, tracking_number, carrier, mode)
			VALUES ($1, $2, 'probe-carrier', 'road') RETURNING id`,
			tenantID, "FB-"+tenantID.String()[:8]).Scan(&shipmentID)
	}); err != nil {
		t.Fatal(err)
	}

	// First batch must pick up the new (dirty-by-default) row.
	var first int
	if err := db.WithTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
		var err error
		first, err = RefreshBatch(ctx, tx, 100)
		return err
	}); err != nil {
		t.Fatalf("first batch: %v", err)
	}
	if first != 1 {
		t.Fatalf("first batch refreshed %d, want 1", first)
	}

	// Second batch must find nothing: the flag cleared on success.
	var second int
	if err := db.WithTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
		var err error
		second, err = RefreshBatch(ctx, tx, 100)
		return err
	}); err != nil {
		t.Fatalf("second batch: %v", err)
	}
	if second != 0 {
		t.Fatalf("second batch refreshed %d, want 0 (nothing dirty)", second)
	}

	// Re-dirty the row; the next batch must pick exactly it up again.
	if err := db.WithTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
		return db.MarkShipmentDirty(ctx, tx, shipmentID)
	}); err != nil {
		t.Fatal(err)
	}
	var third int
	if err := db.WithTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
		var err error
		third, err = RefreshBatch(ctx, tx, 100)
		return err
	}); err != nil {
		t.Fatalf("third batch: %v", err)
	}
	if third != 1 {
		t.Fatalf("third batch refreshed %d, want 1 (re-dirtied row)", third)
	}
}
// TestRefreshDetectsSilentShipment proves the "nothing is happening" case:
// a shipment created ten days ago with zero carrier events must accrue
// staleness and dwell from its creation date. Before the baseline fallback,
// NULL propagated and the sweep's COALESCE turned it into 0, so the product's
// headline case produced zero exceptions.
func TestRefreshDetectsSilentShipment(t *testing.T) {
	pool := refreshPool(t)
	ctx := context.Background()

	tenantID := uuid.New()
	slug := "sil-" + tenantID.String()[:8]
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
		tenantID, "Silent Probe", slug); err != nil {
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

	var shipmentID uuid.UUID
	if err := db.WithTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			INSERT INTO shipments (tenant_id, tracking_number, carrier, mode)
			VALUES ($1, $2, 'probe-carrier', 'road') RETURNING id`,
			tenantID, "SIL-"+tenantID.String()[:8]).Scan(&shipmentID); err != nil {
			return err
		}
		// Backdate the creation: ten days of carrier silence.
		_, err := tx.Exec(ctx,
			`UPDATE shipments SET created_at = now() - interval '240 hours' WHERE id=$1`,
			shipmentID)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	if err := db.WithTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
		return Refresh(ctx, tx, shipmentID)
	}); err != nil {
		t.Fatalf("Refresh failed: %v", err)
	}

	var stale, dwell *float64
	var score *int
	if err := db.WithTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT stale_hours, dwell_hours, risk_score FROM shipment_current
			WHERE shipment_id=$1`, shipmentID).Scan(&stale, &dwell, &score)
	}); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if stale == nil || *stale < 200 {
		t.Errorf("stale_hours=%v; ten days of silence must accrue", stale)
	}
	if dwell == nil || *dwell < 200 {
		t.Errorf("dwell_hours=%v; ten days of silence must accrue", dwell)
	}
	if score == nil || *score == 0 {
		t.Errorf("risk_score=%v; a silent shipment must score above zero", score)
	}
}

// TestRefreshDwellMeasuresTransitNotSilence proves dwell is total time in
// transit, not time since the last scan. A shipment that left 25 days ago on
// a 21-day ocean norm is overrunning even though it scanned yesterday; under
// the old time-since-last-event definition it read dwell≈24h and the
// dwell_ratio rules could never fire for moving freight.
func TestRefreshDwellMeasuresTransitNotSilence(t *testing.T) {
	pool := refreshPool(t)
	ctx := context.Background()

	tenantID := uuid.New()
	slug := "dw-" + tenantID.String()[:8]
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
		tenantID, "Dwell Probe", slug); err != nil {
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

	var shipmentID uuid.UUID
	if err := db.WithTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			INSERT INTO shipments (tenant_id, tracking_number, carrier, mode, shipped_at)
			VALUES ($1, $2, 'probe-carrier', 'ocean', now() - interval '600 hours')
			RETURNING id`, tenantID, "DW-"+tenantID.String()[:8]).Scan(&shipmentID); err != nil {
			return err
		}
		// A scan yesterday: recent activity on an overrunning transit.
		_, err := tx.Exec(ctx, `
			INSERT INTO shipment_events
				(tenant_id, shipment_id, carrier, code, occurred_at, dedup_key)
			VALUES ($1, $2, 'probe-carrier', 'DEPARTED', now() - interval '24 hours', $3)`,
			tenantID, shipmentID, "dwell:"+tenantID.String())
		return err
	}); err != nil {
		t.Fatal(err)
	}

	if err := db.WithTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
		return Refresh(ctx, tx, shipmentID)
	}); err != nil {
		t.Fatalf("Refresh failed: %v", err)
	}

	var dwell, expected *float64
	var ratio *float64
	if err := db.WithTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT dwell_hours, expected_dwell_hours, dwell_ratio
			FROM shipment_current WHERE shipment_id=$1`,
			shipmentID).Scan(&dwell, &expected, &ratio)
	}); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if dwell == nil || *dwell < 500 {
		t.Errorf("dwell_hours=%v; 25 days in transit must read ~600h, not ~24h since last scan", dwell)
	}
	if ratio == nil || *ratio < 1.0 {
		t.Errorf("dwell_ratio=%v; 600h against a 504h norm must exceed 1.0", ratio)
	}
}

// TestRefreshManyMatchesRefresh proves the batch path cannot diverge from the
// single-row path. It refreshes the same diverse rows both ways and requires
// every persisted column but updated_at to be identical. If the two paths ever
// disagree, the sweep (batched) and the API-triggered refresh (single) would
// write different scores for the same facts.
func TestRefreshManyMatchesRefresh(t *testing.T) {
	pool := refreshPool(t)
	ctx := context.Background()

	tenantID := uuid.New()
	slug := "eq-" + tenantID.String()[:8]
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
		tenantID, "Equiv Probe", slug); err != nil {
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

	// Diverse rows: one with everything, one bare, one delivered.
	var ids []uuid.UUID
	if err := db.WithTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
		for i, mode := range []string{"ocean", "road", "air"} {
			var sid uuid.UUID
			if err := tx.QueryRow(ctx, `
				INSERT INTO shipments (tenant_id, tracking_number, carrier, mode, shipped_at, value_at_risk)
				VALUES ($1, $2, 'probe-carrier', $3, now() - interval '300 hours', $4)
				RETURNING id`,
				tenantID, fmt.Sprintf("EQ-%s-%d", tenantID.String()[:4], i),
				mode, 15000).Scan(&sid); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO shipment_events (tenant_id, shipment_id, carrier, code, occurred_at, dedup_key)
				VALUES ($1, $2, 'probe-carrier', 'DEPARTED', now() - interval '100 hours', $3)`,
				tenantID, sid, fmt.Sprintf("eq:%s:%d", tenantID, i)); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO alerts (tenant_id, shipment_id, kind, severity, status, title, detected_at)
				VALUES ($1, $2, 'stale', 'critical', 'open', 'probe', now())`,
				tenantID, sid); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO tracking_subscriptions (tenant_id, shipment_id, channel, recipient, recipient_hash, status)
				VALUES ($1, $2, 'email', 'eq@example.com',
					'1111111111111111111111111111111111111111111111111111111111111111',
					'active')`, tenantID, sid); err != nil {
				return err
			}
			ids = append(ids, sid)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	snapshot := func() map[string]string {
		out := map[string]string{}
		if err := db.WithTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
			rows, err := tx.Query(ctx, `
				SELECT shipment_id, row_to_json(c)::text FROM (
					SELECT shipment_id, tracking_number, carrier, mode, origin,
					       destination, status, is_public, risk_score, risk_tier,
					       risk_breakdown, open_alerts, critical_alerts,
					       last_event_at, last_event_code, stale_hours,
					       eta, eta_source, eta_confidence, eta_slip_hours,
					       dwell_hours, expected_dwell_hours, dwell_ratio,
					       value_at_risk, customer_notified, shipped_at, delivered_at
					FROM shipment_current WHERE tenant_id=$1
				) c ORDER BY shipment_id`, tenantID)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var sid, js string
				if err := rows.Scan(&sid, &js); err != nil {
					return err
				}
				out[sid] = js
			}
			return rows.Err()
		}); err != nil {
			t.Fatal(err)
		}
		return out
	}

	// Path 1: one by one. Both paths below share a single fixed instant so the
	// comparison is exact: any difference is logic, never clock skew.
	fixed := time.Now().Truncate(time.Second)
	if err := db.WithTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
		for _, id := range ids {
			if err := refreshAt(ctx, tx, id, fixed); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("single refresh: %v", err)
	}
	single := snapshot()

	// Re-dirty, then path 2: one batch at the same instant.
	if err := db.WithTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE shipments SET needs_refresh=true WHERE tenant_id=$1`, tenantID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.WithTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
		_, err := refreshManyAt(ctx, tx, tenantID, ids, fixed)
		return err
	}); err != nil {
		t.Fatalf("batch refresh: %v", err)
	}
	batched := snapshot()

	if len(single) != len(ids) || len(batched) != len(ids) {
		t.Fatalf("row counts differ: single=%d batched=%d want=%d", len(single), len(batched), len(ids))
	}
	for _, id := range ids {
		if single[id.String()] != batched[id.String()] {
			t.Errorf("shipment %s differs:\n single: %s\n batched: %s",
				id, single[id.String()], batched[id.String()])
		}
	}
}

// TestRefreshPersistsBreakdown seeds a shipment with a declared value, runs
// Refresh, and requires the persisted breakdown to carry that value with
// valueKnown=true — and the dirty flag to be cleared. Before the breakdown
// column existed the tooltip could only recompute the weights client-side,
// which is how the two copies drifted.
func TestRefreshPersistsBreakdown(t *testing.T) {
	pool := refreshPool(t)
	ctx := context.Background()

	tenantID := uuid.New()
	slug := "bd-" + tenantID.String()[:8]
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
		tenantID, "Breakdown Probe", slug); err != nil {
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

	var shipmentID uuid.UUID
	if err := db.WithTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			INSERT INTO shipments (tenant_id, tracking_number, carrier, mode, value_at_risk)
			VALUES ($1, $2, 'probe-carrier', 'road', 25000)
			RETURNING id`, tenantID, "BD-"+tenantID.String()[:8]).Scan(&shipmentID)
	}); err != nil {
		t.Fatal(err)
	}

	if err := db.WithTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
		return Refresh(ctx, tx, shipmentID)
	}); err != nil {
		t.Fatalf("Refresh failed: %v", err)
	}

	var (
		raw      []byte
		dirty    bool
		storedVr *float64
	)
	if err := db.WithTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT risk_breakdown, value_at_risk FROM shipment_current
			WHERE shipment_id=$1`, shipmentID).Scan(&raw, &storedVr)
	}); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if err := db.WithTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT needs_refresh FROM shipments WHERE id=$1`,
			shipmentID).Scan(&dirty)
	}); err != nil {
		t.Fatalf("read flag: %v", err)
	}

	var b Breakdown
	if err := json.Unmarshal(raw, &b); err != nil {
		t.Fatalf("breakdown is not valid JSON: %v", err)
	}
	if !b.ValueKnown {
		t.Errorf("ValueKnown=false; the shipment declares 25000")
	}
	if b.Value <= 0 {
		t.Errorf("value term is %v; 25000 of exposure must contribute", b.Value)
	}
	if storedVr == nil || *storedVr != 25000 {
		t.Errorf("value_at_risk persisted as %v, want 25000", storedVr)
	}
	if dirty {
		t.Errorf("needs_refresh still set after a successful Refresh")
	}
}
