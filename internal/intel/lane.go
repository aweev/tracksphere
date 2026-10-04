package intel

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// LaneStatsFor learns transit stats for a lane from delivered shipments.
// Exact (origin, destination, carrier, mode) first, then carrier-agnostic
// (origin, destination, mode). Returns nil when fewer than 5 samples exist.
func LaneStatsFor(ctx context.Context, tx pgx.Tx, origin, dest, carrier, mode string) *LaneStats {
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`SELECT extract(epoch FROM (delivered_at - shipped_at))/86400
		  FROM shipments
		  WHERE origin=$1 AND destination=$2 AND carrier=$3 AND mode=$4
		    AND status='delivered' AND shipped_at IS NOT NULL AND delivered_at IS NOT NULL`,
			[]any{origin, dest, carrier, mode}},
		{`SELECT extract(epoch FROM (delivered_at - shipped_at))/86400
		  FROM shipments
		  WHERE origin=$1 AND destination=$2 AND mode=$3
		    AND status='delivered' AND shipped_at IS NOT NULL AND delivered_at IS NOT NULL`,
			[]any{origin, dest, mode}},
	} {
		rows, err := tx.Query(ctx, q.sql, q.args...)
		if err != nil {
			return nil
		}
		var days []float64
		for rows.Next() {
			var d *float64
			if err := rows.Scan(&d); err != nil {
				rows.Close()
				return nil
			}
			if d != nil && *d > 0 {
				days = append(days, *d)
			}
		}
		rows.Close()
		if rows.Err() != nil {
			return nil
		}
		if len(days) >= 5 {
			s := Summarize(days)
			return &s
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
