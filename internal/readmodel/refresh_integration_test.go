package readmodel

// Proves Refresh writes the server-computed breakdown and clears the dirty
// flag. Skips without a database, like the db package integration tests.
import (
	"context"
	"encoding/json"
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
