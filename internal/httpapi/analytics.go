package httpapi

import (
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/tracksphere/tracksphere/internal/db"
	"github.com/tracksphere/tracksphere/internal/workers"
)

// handleDigest GET /api/v1/analytics/digest — this week's ops summary.
func (s *Server) handleDigest(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	d, err := workers.DigestFor(r.Context(), s.pool, user.TenantID)
	if err != nil {
		s.domainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

// Analytics v1 (Postgres-native; ClickHouse comes with measured volume):
// on-time %, avg transit, carrier benchmark, lane stats, ETA accuracy.

type carrierStat struct {
	Carrier   string   `json:"carrier"`
	Total     int64    `json:"total"`
	OnTime    int64    `json:"onTime"`
	OnTimePct *float64 `json:"onTimePct,omitempty"`
	AvgDays   *float64 `json:"avgTransitDays,omitempty"`
}

type laneStat struct {
	Origin      string `json:"origin"`
	Destination string `json:"destination"`
	Total       int64  `json:"total"`
	Exceptions  int64  `json:"exceptions"`
}

// handleAnalytics GET /api/v1/analytics
func (s *Server) handleAnalytics(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	var (
		onTime, delivered, total, exceptions int64
		avgDays                              *float64
		accurate, etaSamples                 int64
		carriers                             []carrierStat
		lanes                                []laneStat
	)
	err := db.WithTenant(r.Context(), s.pool, user.TenantID, func(tx pgx.Tx) error {
if err := tx.QueryRow(r.Context(), `
			SELECT count(*),
			       count(*) FILTER (WHERE status='delivered'),
			       count(*) FILTER (WHERE status='delivered'
			         AND (delivered_at IS NULL OR eta IS NULL OR delivered_at <= eta)),
			       count(*) FILTER (WHERE status='exception')
			FROM shipments`,
		).Scan(&total, &delivered, &onTime, &exceptions); err != nil {
			return err
		}
		if err := tx.QueryRow(r.Context(), `
			SELECT avg(extract(epoch FROM (delivered_at - shipped_at))/86400)
			FROM shipments WHERE status='delivered' AND shipped_at IS NOT NULL
			  AND delivered_at IS NOT NULL`).Scan(&avgDays); err != nil {
			return err
		}
		// ETA accuracy: delivered within ±6h of the ETA (the unicorn bar).
		if err := tx.QueryRow(r.Context(), `
			SELECT count(*) FILTER (WHERE abs(extract(epoch FROM (delivered_at - eta))) <= 21600),
			       count(*)
			FROM shipments WHERE status='delivered' AND eta IS NOT NULL
			  AND delivered_at IS NOT NULL`).Scan(&accurate, &etaSamples); err != nil {
			return err
		}
		rows, err := tx.Query(r.Context(), `
			SELECT carrier, count(*),
			       count(*) FILTER (WHERE status='delivered'
			         AND (delivered_at IS NULL OR eta IS NULL OR delivered_at <= eta)),
			       avg(extract(epoch FROM (delivered_at - shipped_at))/86400)
			         FILTER (WHERE status='delivered' AND shipped_at IS NOT NULL AND delivered_at IS NOT NULL)
			FROM shipments GROUP BY carrier ORDER BY count(*) DESC LIMIT 20`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var c carrierStat
			if err := rows.Scan(&c.Carrier, &c.Total, &c.OnTime, &c.AvgDays); err != nil {
				rows.Close()
				return err
			}
			if c.Total > 0 {
				p := float64(c.OnTime) / float64(c.Total) * 100
				// On-time defined over ALL shipments would mislead; compute over
				// delivered only when every shipment delivered.
				c.OnTimePct = &p
			}
			carriers = append(carriers, c)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		lrows, err := tx.Query(r.Context(), `
			SELECT origin, destination, count(*),
			       count(*) FILTER (WHERE status='exception')
			FROM shipments GROUP BY origin, destination
			ORDER BY count(*) DESC LIMIT 20`)
		if err != nil {
			return err
		}
		defer lrows.Close()
		for lrows.Next() {
			var l laneStat
			if err := lrows.Scan(&l.Origin, &l.Destination, &l.Total, &l.Exceptions); err != nil {
				return err
			}
			lanes = append(lanes, l)
		}
		return lrows.Err()
	})
	if err != nil {
		s.domainError(w, err)
		return
	}
	if carriers == nil {
		carriers = []carrierStat{}
	}
	if lanes == nil {
		lanes = []laneStat{}
	}
	var onTimePct *float64
	if delivered > 0 {
		p := float64(onTime) / float64(delivered) * 100
		onTimePct = &p
	}
	var etaAccuracy *float64
	if etaSamples > 0 {
		p := float64(accurate) / float64(etaSamples) * 100
		etaAccuracy = &p
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"total": total, "delivered": delivered,
		"onTime": onTime, "onTimePct": onTimePct,
		"exceptions": exceptions, "avgTransitDays": avgDays,
		"etaAccuracyPct": etaAccuracy, "etaSamples": etaSamples,
		"carriers": carriers, "lanes": lanes,
	})
}
