package intel

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// LaneStatsFor returns learned transit stats for a lane: exact
// (origin, destination, carrier, mode) first, then carrier-agnostic.
// Reads one pre-aggregated row per attempt. Returns nil when fewer than 5
// samples exist. The live scan it replaced loaded every delivered shipment of
// the lane into Go memory on each call — unbounded and unindexed, once per
// ETA recalculation.
func LaneStatsFor(ctx context.Context, tx pgx.Tx, origin, dest, carrier, mode string) *LaneStats {
	for _, c := range []string{carrier, ""} {
		var s LaneStats
		err := tx.QueryRow(ctx, `
			SELECT samples, p50_days, p90_days FROM lane_stats
			WHERE origin=$1 AND destination=$2 AND carrier=$3 AND mode=$4`,
			origin, dest, c, mode).Scan(&s.Samples, &s.P50Days, &s.P90Days)
		if err == nil && s.Samples >= 5 && s.P50Days > 0 {
			return &s
		}
	}
	return nil
}

// RefreshLaneStats recomputes one lane's percentiles from delivered shipments
// and upserts both the carrier-exact and the carrier-agnostic rows. Called
// when a shipment is delivered — cold path, once per shipment lifetime — so
// the hot ETA path never scans. Set-based: no rows enter Go memory.
func RefreshLaneStats(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, origin, dest, carrier, mode string) error {
	for _, c := range []string{carrier, ""} {
		var (
			n   int
			p50 *float64
			p90 *float64
		)
		args := []any{tenantID, origin, dest, mode}
		carrierFilter := ""
		if c != "" {
			carrierFilter = "AND carrier=$5"
			args = append(args, c)
		}
		q := `
			SELECT count(*),
			       percentile_cont(0.5) WITHIN GROUP (ORDER BY extract(epoch FROM (delivered_at - shipped_at))/86400),
			       percentile_cont(0.9) WITHIN GROUP (ORDER BY extract(epoch FROM (delivered_at - shipped_at))/86400)
			FROM shipments
			WHERE tenant_id=$1 AND origin=$2 AND destination=$3 AND mode=$4
			  AND status='delivered' AND shipped_at IS NOT NULL
			  AND delivered_at IS NOT NULL AND delivered_at > shipped_at
			  ` + carrierFilter
		if err := tx.QueryRow(ctx, q, args...).Scan(&n, &p50, &p90); err != nil {
			return err
		}
		if n < 5 || p50 == nil || *p50 <= 0 {
			// Not enough history: remove any stale row rather than serve it.
			if _, err := tx.Exec(ctx, `
				DELETE FROM lane_stats
				WHERE tenant_id=$1 AND origin=$2 AND destination=$3 AND carrier=$4 AND mode=$5`,
				tenantID, origin, dest, c, mode); err != nil {
				return err
			}
			continue
		}
		p90v := *p50
		if p90 != nil && *p90 > 0 {
			p90v = *p90
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO lane_stats (tenant_id, origin, destination, carrier, mode, samples, p50_days, p90_days)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
			ON CONFLICT (tenant_id, origin, destination, carrier, mode) DO UPDATE SET
				samples=EXCLUDED.samples, p50_days=EXCLUDED.p50_days,
				p90_days=EXCLUDED.p90_days, updated_at=now()`,
			tenantID, origin, dest, c, mode, n, *p50, p90v); err != nil {
			return err
		}
	}
	return nil
}

// DisruptionMatch returns active zones whose terms hit the haystack.
func DisruptionMatch(ctx context.Context, tx pgx.Tx, haystack string) []Zone {
	hay := strings.ToLower(haystack)
	rows, err := tx.Query(ctx, `
		SELECT name, match_terms, severity, message FROM disruption_zones
		WHERE active = true`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []Zone
	for rows.Next() {
		var z Zone
		if err := rows.Scan(&z.Name, &z.Terms, &z.Severity, &z.Message); err != nil {
			return out
		}
		for _, t := range z.Terms {
			if t != "" && strings.Contains(hay, strings.ToLower(t)) {
				out = append(out, z)
				break
			}
		}
	}
	return out
}

// Zone is one disruption row.
type Zone struct {
	Name     string   `json:"name"`
	Terms    []string `json:"-"`
	Severity string   `json:"severity"`
	Message  string   `json:"message"`
}

// ShipmentContext is what the rules engine needs for lane-aware checks.
type ShipmentContext struct {
	Origin      string
	Destination string
	Carrier     string
	Mode        string
	Status      string
	CreatedAt   time.Time
	TenantID    uuid.UUID
	ShipmentID  uuid.UUID
}
