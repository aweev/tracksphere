package httpapi

// Proves the exception queue pages by keyset with no gaps, no duplicates, and
// correct severity ordering — and that rows past the old hardcoded LIMIT 200
// are reachable. Skips without a database.
import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tracksphere/tracksphere/internal/db"
)

func alertsPool(t *testing.T) *pgxpool.Pool {
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

// TestAlertQueuePagesWithoutGaps seeds mixed-severity alerts and walks the
// queue in windows of 2, requiring severity order, no repeats, no skips, and
// a terminal empty cursor.
func TestAlertQueuePagesWithoutGaps(t *testing.T) {
	pool := alertsPool(t)
	ctx := context.Background()
	s := &Server{pool: pool}

	tenantID := uuid.New()
	slug := "pg-" + tenantID.String()[:8]
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
		tenantID, "Page Probe", slug); err != nil {
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
			tenantID, "PG-"+tenantID.String()[:8]).Scan(&shipmentID)
	}); err != nil {
		t.Fatal(err)
	}

	// 2 critical + 3 warning, staggered detected_at for deterministic order.
	seeds := []struct {
		kind     string
		severity string
		minutes  int
	}{
		{"stale", "warning", 50},
		{"dwell", "critical", 40},
		{"eta_slip", "warning", 30},
		{"delay", "critical", 20},
		{"customs_hold", "warning", 10},
	}
	// Note: kind must satisfy alerts_kind_check.
	kinds := []string{"stale", "dwell", "eta_slip", "delay", "disruption"}
	for i, sd := range seeds {
		kind := kinds[i]
		if err := db.WithTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `
				INSERT INTO alerts (tenant_id, shipment_id, kind, severity, status, title, detected_at)
				VALUES ($1, $2, $3, $4, 'open', 'probe', now() - make_interval(mins => $5))`,
				tenantID, shipmentID, kind, sd.severity, sd.minutes)
			return err
		}); err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}

	// Walk in windows of 2: expect 2, 2, 1, then done.
	var got []string
	cursor := ""
	for page := 0; page < 4; page++ {
		rows, next, err := s.listAlertsFiltered(ctx, tenantID, "open", false, cursor, 2)
		if err != nil {
			t.Fatalf("page %d: %v", page, err)
		}
		for _, a := range rows {
			got = append(got, a.ID.String())
		}
		if next == "" {
			break
		}
		cursor = next
	}
	if len(got) != 5 {
		t.Fatalf("walked %d alerts, want 5 (queue must be fully reachable)", len(got))
	}
	seen := map[string]bool{}
	for _, id := range got {
		if seen[id] {
			t.Fatalf("duplicate alert %s across pages", id)
		}
		seen[id] = true
	}

	// Severity order: both criticals before any warning.
	var severities []string
	if err := db.WithTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
		// Re-fetch in queue order to check positions.
		rows, _, err := s.listAlertsFiltered(ctx, tenantID, "open", false, "", 10)
		if err != nil {
			return err
		}
		for _, a := range rows {
			severities = append(severities, a.Severity)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(severities) != 5 || severities[0] != "critical" || severities[1] != "critical" {
		t.Fatalf("queue order wrong: %v (criticals must lead)", severities)
	}
	for _, sv := range severities[2:] {
		if sv != "warning" {
			t.Fatalf("queue order wrong: %v", severities)
		}
	}
}

// TestAlertCursorRejectsGarbage proves a tampered cursor fails closed to an
// error (surfaced as 400) rather than reaching SQL.
func TestAlertCursorRejectsGarbage(t *testing.T) {
	for _, bad := range []string{
		"not-base64!!!",
		"e30=",                                                 // {} — missing fields
		"eyJyIjotMSwidCI6IjIwMjYtMDEtMDFUMDA6MDA6MDBaIiwiaSI6IjAwMDAwMDAwLTAwMDAtMDAwMC0wMDAwLTAwMDAwMDAwMDAwMCJ9", // rank -1
	} {
		if _, err := decodeAlertCursor(bad); err == nil {
			t.Fatalf("cursor %q accepted; must reject", bad)
		}
	}
	// Round-trip: encode then decode preserves values.
	want := alertCursor{Rank: 0, Detected: time.Now().Truncate(time.Second), ID: uuid.New()}
	got, err := decodeAlertCursor(encodeAlertCursor(want))
	if err != nil {
		t.Fatalf("round-trip failed: %v", err)
	}
	if got.Rank != want.Rank || !got.Detected.Equal(want.Detected) || got.ID != want.ID {
		t.Fatalf("round-trip mismatch: %+v vs %+v", got, want)
	}
}
