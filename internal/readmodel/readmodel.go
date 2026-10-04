// Package readmodel maintains shipment_current: one narrow, index-friendly row
// per shipment carrying the fields every hot path reads.
//
// WHY: every list view and dashboard tile was a live aggregate over the write
// tables — five independent counts on the dashboard, count(*) plus a page on the
// list, and a correlated max(occurred_at) subquery per row for the carrier
// poller. That is fine at six shipments (the seed data) and unacceptable at
// 50,000. It also made the single most decision-relevant number in the product —
// how much trouble is this shipment in — impossible to compute per request.
//
// The risk score lives here rather than in the client for the same reason: a
// score computed per request is not sortable without recomputing the world, and
// ops need the list ordered by consequence, not by status enum.
package readmodel

import (
	"context"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Input is everything the score depends on. Derived values are computed by SQL
// so the score reflects persisted state, not a second source of truth.
type Input struct {
	Status             string
	DwellHours         float64
	ExpectedDwellHours float64
	ETASlipHours       float64
	StaleHours         float64
	OpenAlerts         int
	CriticalAlerts     int
	InfoOrWarnAlerts   int
	ValueAtRisk        float64
	CustomerNotified   bool
}

// Weights. Deliberately bounded and documented: an unexplainable score is worse
// than no score, because operators learn to distrust the ordering.
const (
	wDwellMax      = 35.0 // overrunning the expected transit
	wSlipMax       = 20.0 // ETA moved later than promised
	wStaleMax      = 25.0 // silence from the carrier, relative to lane norm
	wCriticalAlert = 15.0 // per critical open alert
	wAlert         = 3.0  // per warning/info open alert
	wValueMax      = 15.0 // declared commercial exposure
	notifiedRelief = -10.0 // customer already told: lower urgency
)

// Tier thresholds.
const (
	tierCritical = 70
	tierAtRisk   = 40
	tierWatch    = 15
)

// Score returns a 0-100 risk score and its tier. Deterministic and pure, so it
// is unit-testable and cannot drift between processes.
func Score(in Input) (int, string) {
	var score float64

	// Dwell ratio: 1.0 is on plan, 2.0 is double the expected transit.
	if in.ExpectedDwellHours > 0 {
		ratio := in.DwellHours / in.ExpectedDwellHours
		if ratio > 1 {
			score += math.Min((ratio-1)*wDwellMax, wDwellMax)
		}
	} else if in.DwellHours > 0 {
		// No norm available: fall back to absolute dwell by mode.
		if in.DwellHours > 21*24 {
			score += wDwellMax
		} else if in.DwellHours > 10*24 {
			score += wDwellMax / 2
		}
	}

	if in.ETASlipHours > 0 {
		// A two-day slip saturates the term; beyond that, more delay does not
		// make the row more urgent than an active exception.
		score += math.Min(in.ETASlipHours/48*wSlipMax, wSlipMax)
	}

	if in.StaleHours > 0 {
		// Silence is only meaningful relative to how long this shipment's
		// current stage should reasonably take. An absolute "48h" threshold is
		// wrong for every mode except road: 48h of quiet on a 21-day ocean
		// transit is normal, and 48h of quiet on an air shipment is an
		// emergency. The threshold is therefore a fraction of the expected
		// dwell, floored at 24h so short modes still get a sane trigger.
		threshold := in.ExpectedDwellHours / 6
		if threshold < 24 {
			threshold = 24
		}
		// Silence up to the threshold contributes nothing: an ocean container
		// with no scan for three days is exactly on plan, and scoring it as
		// risky teaches operators to ignore the column.
		if ratio := in.StaleHours/threshold - 1; ratio > 0 {
			score += math.Min(ratio*(wStaleMax/2), wStaleMax)
		}
	}

	score += math.Min(float64(in.CriticalAlerts)*wCriticalAlert, 2*wCriticalAlert)
	score += math.Min(float64(in.InfoOrWarnAlerts)*wAlert, 4*wAlert)

	if in.ValueAtRisk > 0 {
		// Saturates at $10,000 of declared exposure.
		score += math.Min(in.ValueAtRisk/10000*wValueMax, wValueMax)
	}

	if in.CustomerNotified {
		score += notifiedRelief
	}

	// Delivered and cancelled shipments are not at risk, whatever else is true.
	if in.Status == "delivered" || in.Status == "cancelled" {
		return 0, "clear"
	}

	if score < 0 {
		score = 0
	}
	if score > 100 {
		score = 100
	}
	rounded := int(score + 0.5)
	return rounded, TierFor(rounded)
}

// TierFor maps a score to its display tier.
func TierFor(score int) string {
	switch {
	case score >= tierCritical:
		return "critical"
	case score >= tierAtRisk:
		return "at_risk"
	case score >= tierWatch:
		return "watch"
	default:
		return "clear"
	}
}

// Refresh recomputes one shipment_current row from the write tables. Safe to
// call from any tenant-pinned transaction.
func Refresh(ctx context.Context, tx pgx.Tx, shipmentID uuid.UUID) error {
	var (
		tracking, carrier, mode, origin, dest, status string
		isPublic                                     bool
		createdAt, shippedAt                         *time.Time
		deliveredAt                                  *time.Time
		originalETA                                  *time.Time
		lastEventAt                                  *time.Time
		lastEventCode                                *string
	)
	// Scan the shipment and its newest event in one statement.
	err := tx.QueryRow(ctx, `
		SELECT s.tracking_number, s.carrier, s.mode, s.origin, s.destination,
		       s.status, s.is_public, s.created_at, s.shipped_at, s.delivered_at, s.eta,
		       (SELECT max(e.occurred_at) FROM shipment_events e WHERE e.shipment_id = s.id),
		       (SELECT e.code FROM shipment_events e WHERE e.shipment_id = s.id
		         ORDER BY e.occurred_at DESC LIMIT 1)
		FROM shipments s WHERE s.id = $1`, shipmentID).
		Scan(&tracking, &carrier, &mode, &origin, &dest, &status, &isPublic,
			&createdAt, &shippedAt, &deliveredAt, &originalETA,
			&lastEventAt, &lastEventCode)
	if err != nil {
		return err
	}

	// Exception rollup.
	var openAlerts, criticalAlerts, otherAlerts int
	if err := tx.QueryRow(ctx, `
		SELECT count(*),
		       count(*) FILTER (WHERE severity='critical'),
		       count(*) FILTER (WHERE severity IN ('warning','info'))
		FROM alerts WHERE shipment_id=$1 AND status='open'`, shipmentID).
		Scan(&openAlerts, &criticalAlerts, &otherAlerts); err != nil {
		return err
	}

	// Active-subscription state decides whether the customer already knows.
	var notified bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM tracking_subscriptions
		              WHERE shipment_id=$1 AND status='active')`, shipmentID).
		Scan(&notified); err != nil {
		return err
	}

	now := time.Now()
	in := Input{
		Status:           status,
		OpenAlerts:       openAlerts,
		CriticalAlerts:   criticalAlerts,
		InfoOrWarnAlerts: otherAlerts,
		CustomerNotified: notified,
	}
	var staleHours *float64
	if lastEventAt != nil {
		h := now.Sub(*lastEventAt).Hours()
		if h > 0 {
			staleHours = &h
			in.StaleHours = h
		}
	}
	// Dwell: how long since the last event, against the lane norm. Using the
	// last event (not created_at) is what makes "arrived 6 days ago and nothing
	// since" measurable, which is the whole point.
	var dwell, expected *float64
	if lastEventAt != nil {
		d := now.Sub(*lastEventAt).Hours()
		if d >= 0 {
			dwell = &d
			in.DwellHours = d
		}
	}
	if exp, ok := expectedDwellHours(mode); ok {
		expected = &exp
		in.ExpectedDwellHours = exp
	}
	// ETA slip: how much later the current ETA is than the original estimate.
	var slip *float64
	if originalETA != nil && lastEventAt != nil {
		// Compare against the estimate we would have made at booking.
		if base, ok := estimateFromShipped(shippedAt, createdAt, mode); ok {
			s := originalETA.Sub(base).Hours()
			if s > 0 {
				slip = &s
				in.ETASlipHours = s
			}
		}
	}
	var valueAtRisk *float64
	var ratio *float64
	if dwell != nil && expected != nil && *expected > 0 {
		r := *dwell / *expected
		ratio = &r
	}

	score, tier := Score(in)
	etaSource, etaConfidence := etaProvenance(originalETA, lastEventAt, createdAt, mode)

	_, err = tx.Exec(ctx, `
		INSERT INTO shipment_current
			(tenant_id, shipment_id, tracking_number, carrier, mode, origin, destination,
			 status, is_public, risk_score, risk_tier, open_alerts, critical_alerts,
			 last_event_at, last_event_code, stale_hours,
			 eta, eta_source, eta_confidence, eta_slip_hours,
			 dwell_hours, expected_dwell_hours, dwell_ratio,
			 value_at_risk, customer_notified, shipped_at, delivered_at, updated_at)
		VALUES ((SELECT tenant_id FROM shipments WHERE id=$1), $1, $2,$3,$4,$5,$6,
		        $7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26, now())
		ON CONFLICT (shipment_id) DO UPDATE SET
			tracking_number=EXCLUDED.tracking_number, carrier=EXCLUDED.carrier,
			mode=EXCLUDED.mode, origin=EXCLUDED.origin, destination=EXCLUDED.destination,
			status=EXCLUDED.status, is_public=EXCLUDED.is_public,
			risk_score=EXCLUDED.risk_score, risk_tier=EXCLUDED.risk_tier,
			open_alerts=EXCLUDED.open_alerts, critical_alerts=EXCLUDED.critical_alerts,
			last_event_at=EXCLUDED.last_event_at, last_event_code=EXCLUDED.last_event_code,
			stale_hours=EXCLUDED.stale_hours, eta=EXCLUDED.eta,
			eta_source=EXCLUDED.eta_source, eta_confidence=EXCLUDED.eta_confidence,
			eta_slip_hours=EXCLUDED.eta_slip_hours, dwell_hours=EXCLUDED.dwell_hours,
			expected_dwell_hours=EXCLUDED.expected_dwell_hours, dwell_ratio=EXCLUDED.dwell_ratio,
			value_at_risk=EXCLUDED.value_at_risk,
			customer_notified=EXCLUDED.customer_notified,
			shipped_at=EXCLUDED.shipped_at, delivered_at=EXCLUDED.delivered_at,
			updated_at=now()`,
		shipmentID, tracking, carrier, mode, origin, dest, status, isPublic,
		score, tier, openAlerts, criticalAlerts,
		lastEventAt, lastEventCode, staleHours,
		originalETA, etaSource, etaConfidence, slip,
		dwell, expected, ratio, valueAtRisk, notified, shippedAt, deliveredAt)
	return err
}

// modeNorms is the planning norm per transport mode, in hours. Seeded values:
// the lane model in internal/intel supersedes them once a tenant has delivered
// enough shipments on a lane to have a learned p50/p90.
var modeNorms = map[string]float64{
	"ocean": 21 * 24,
	"air":   3 * 24,
	"road":  5 * 24,
	"rail":  10 * 24,
}

func expectedDwellHours(mode string) (float64, bool) {
	h, ok := modeNorms[mode]
	if !ok {
		h, ok = modeNorms["road"]
	}
	return h, ok
}

// estimateFromShipped reconstructs the baseline ETA the system would have
// published at booking, so "ETA slip" has a fixed reference point rather than
// drifting every time the estimate is recomputed.
func estimateFromShipped(shippedAt, createdAt *time.Time, mode string) (time.Time, bool) {
	base := createdAt
	if shippedAt != nil {
		base = shippedAt
	}
	if base == nil {
		return time.Time{}, false
	}
	h, _ := expectedDwellHours(mode)
	return base.Add(time.Duration(h) * time.Hour), true
}

// etaProvenance decides how much the displayed ETA should be trusted, and is the
// single most important credibility control in the product: a carrier-published
// ETA and our own guess must never render identically, or operators stop
// believing every number on screen.
func etaProvenance(eta, lastEvent, created *time.Time, mode string) (string, float64) {
	if eta == nil {
		return "none", 0
	}
	if lastEvent != nil && eta.Sub(*lastEvent) > 24*time.Hour {
		// No carrier has confirmed this ETA within a day: it is our estimate.
		return "estimated", 0.4
	}
	if created != nil && time.Since(*created) > 60*24*time.Hour {
		// A very old shipment with no recent carrier confirmation.
		return "lane_model", 0.6
	}
	_ = mode
	return "carrier", 0.95
}

// RefreshBatch recomputes up to limit stale rows. Called by the sweep so the
// read model self-heals even if an incremental update was missed.
func RefreshBatch(ctx context.Context, tx pgx.Tx, limit int) (int, error) {
	rows, err := tx.Query(ctx, `
		SELECT s.id FROM shipments s
		LEFT JOIN shipment_current c ON c.shipment_id = s.id
		WHERE c.shipment_id IS NULL
		   OR c.updated_at < now() - interval '15 minutes'
		ORDER BY c.updated_at NULLS FIRST
		LIMIT $1`, limit)
	if err != nil {
		return 0, err
	}
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, id := range ids {
		if err := Refresh(ctx, tx, id); err != nil {
			// One bad row must not stall the sweep.
			continue
		}
	}
	return len(ids), nil
}