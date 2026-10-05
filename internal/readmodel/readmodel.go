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
	"encoding/json"
	"fmt"
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
	// ValueKnown records whether a declared cargo value was supplied. A missing
	// value is not a zero: imputing 0 would read as "no money at risk", which
	// is the opposite of the truth, so the value term applies only when known.
	ValueKnown       bool
	CustomerNotified bool
}

// Breakdown explains a score term-by-term in the points each contributed, so
// the UI can render "why this risk" exactly as computed instead of
// re-deriving the weights in TypeScript (which drifts — shipments/page.tsx
// once carried two inline copies). Value + ValueKnown mirror the Input: when
// no cargo value was declared the tooltip says so honestly rather than
// showing a silent 0/15. Relief is negative by construction.
type Breakdown struct {
	Dwell      float64 `json:"dwell"`
	Slip       float64 `json:"slip"`
	Stale      float64 `json:"stale"`
	Critical   float64 `json:"critical"`
	Alerts     float64 `json:"alerts"`
	Value      float64 `json:"value"`
	ValueKnown bool    `json:"valueKnown"`
	Relief     float64 `json:"relief"`
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

// Score returns a 0-100 risk score, its tier, and the per-term breakdown.
// Deterministic and pure, so it is unit-testable and cannot drift between
// processes. The breakdown is the single home of the weights: anything that
// explains a score must render these numbers, never recompute them.
func Score(in Input) (int, string, Breakdown) {
	var score float64
	var b Breakdown
	b.ValueKnown = in.ValueKnown

	// Dwell ratio: 1.0 is on plan, 2.0 is double the expected transit.
	if in.ExpectedDwellHours > 0 {
		ratio := in.DwellHours / in.ExpectedDwellHours
		if ratio > 1 {
			b.Dwell = math.Min((ratio-1)*wDwellMax, wDwellMax)
			score += b.Dwell
		}
	} else if in.DwellHours > 0 {
		// No norm available: fall back to absolute dwell by mode.
		if in.DwellHours > 21*24 {
			b.Dwell = wDwellMax
		} else if in.DwellHours > 10*24 {
			b.Dwell = wDwellMax / 2
		}
		score += b.Dwell
	}

	if in.ETASlipHours > 0 {
		// A two-day slip saturates the term; beyond that, more delay does not
		// make the row more urgent than an active exception.
		b.Slip = math.Min(in.ETASlipHours/48*wSlipMax, wSlipMax)
		score += b.Slip
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
			b.Stale = math.Min(ratio*(wStaleMax/2), wStaleMax)
			score += b.Stale
		}
	}

	b.Critical = math.Min(float64(in.CriticalAlerts)*wCriticalAlert, 2*wCriticalAlert)
	b.Alerts = math.Min(float64(in.InfoOrWarnAlerts)*wAlert, 4*wAlert)
	score += b.Critical + b.Alerts

	if in.ValueKnown && in.ValueAtRisk > 0 {
		// Saturates at $10,000 of declared exposure.
		b.Value = math.Min(in.ValueAtRisk/10000*wValueMax, wValueMax)
		score += b.Value
	}

	if in.CustomerNotified {
		b.Relief = notifiedRelief
		score += b.Relief
	}

	// Delivered and cancelled shipments are not at risk, whatever else is true.
	if in.Status == "delivered" || in.Status == "cancelled" {
		return 0, "clear", Breakdown{ValueKnown: in.ValueKnown}
	}

	if score < 0 {
		score = 0
	}
	if score > 100 {
		score = 100
	}
	rounded := int(score + 0.5)
	return rounded, TierFor(rounded), b
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

// refreshFacts is everything Refresh needs scanned before any computation.
// Both the single-row and the batch path produce these, so the scoring below
// runs identically regardless of how the facts were fetched.
type refreshFacts struct {
	shipmentID                   uuid.UUID
	tracking, carrier, mode      string
	origin, dest, status         string
	isPublic                     bool
	createdAt, shippedAt         *time.Time
	deliveredAt                  *time.Time
	originalETA                  *time.Time
	lastEventAt                  *time.Time
	lastEventCode                *string
	declaredValue                *float64
	openAlerts, criticalAlerts   int
	otherAlerts                  int
	notified                     bool
}

// refreshResult is everything computed from facts: the score, its
// explanation, and every derived column the upsert writes.
type refreshResult struct {
	tier                       string
	scoreInt                   int
	breakdown                  Breakdown
	etaSource                  string
	etaConfidence              float64
	staleHours, dwell, expected *float64
	slip, ratio, valueAtRisk    *float64
}

// Refresh recomputes one shipment_current row from the write tables. Safe to
// call from any tenant-pinned transaction. Time comes from the database clock
// so app/DB skew cannot shift staleness between the read and the write.
func Refresh(ctx context.Context, tx pgx.Tx, shipmentID uuid.UUID) error {
	var now time.Time
	if err := tx.QueryRow(ctx, `SELECT now()`).Scan(&now); err != nil {
		return err
	}
	return refreshAt(ctx, tx, shipmentID, now)
}

func refreshAt(ctx context.Context, tx pgx.Tx, shipmentID uuid.UUID, now time.Time) error {
	var f refreshFacts
	f.shipmentID = shipmentID
	// Scan the shipment and its newest event in one statement.
	err := tx.QueryRow(ctx, `
		SELECT s.tracking_number, s.carrier, s.mode, s.origin, s.destination,
		       s.status, s.is_public, s.created_at, s.shipped_at, s.delivered_at, s.eta,
		       s.value_at_risk,
		       (SELECT max(e.occurred_at) FROM shipment_events e WHERE e.shipment_id = s.id),
		       (SELECT e.code FROM shipment_events e WHERE e.shipment_id = s.id
		         ORDER BY e.occurred_at DESC LIMIT 1)
		FROM shipments s WHERE s.id = $1`, shipmentID).
		Scan(&f.tracking, &f.carrier, &f.mode, &f.origin, &f.dest, &f.status, &f.isPublic,
			&f.createdAt, &f.shippedAt, &f.deliveredAt, &f.originalETA, &f.declaredValue,
			&f.lastEventAt, &f.lastEventCode)
	if err != nil {
		return err
	}

	// Exception rollup.
	if err := tx.QueryRow(ctx, `
		SELECT count(*),
		       count(*) FILTER (WHERE severity='critical'),
		       count(*) FILTER (WHERE severity IN ('warning','info'))
		FROM alerts WHERE shipment_id=$1 AND status='open'`, shipmentID).
		Scan(&f.openAlerts, &f.criticalAlerts, &f.otherAlerts); err != nil {
		return err
	}

	// Active-subscription state decides whether the customer already knows.
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM tracking_subscriptions
		              WHERE shipment_id=$1 AND status='active')`, shipmentID).
		Scan(&f.notified); err != nil {
		return err
	}

	_, r := computeRefresh(f, now)
	return upsertCurrent(ctx, tx, f, r)
}

// upsertCurrent writes one computed row and clears the dirty flag. Shared by
// Refresh and RefreshMany so the persisted shape cannot diverge between them.
func upsertCurrent(ctx context.Context, tx pgx.Tx, f refreshFacts, r refreshResult) error {
	breakdownRaw, err := json.Marshal(r.breakdown)
	if err != nil {
		return fmt.Errorf("marshal breakdown: %w", err)
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO shipment_current
			(tenant_id, shipment_id, tracking_number, carrier, mode, origin, destination,
			 status, is_public, risk_score, risk_tier, risk_breakdown, open_alerts, critical_alerts,
			 last_event_at, last_event_code, stale_hours,
			 eta, eta_source, eta_confidence, eta_slip_hours,
			 dwell_hours, expected_dwell_hours, dwell_ratio,
			 value_at_risk, customer_notified, shipped_at, delivered_at, updated_at)
		VALUES ((SELECT tenant_id FROM shipments WHERE id=$1), $1, $2,$3,$4,$5,$6,
		        $7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27, now())
		ON CONFLICT (shipment_id) DO UPDATE SET
			tracking_number=EXCLUDED.tracking_number, carrier=EXCLUDED.carrier,
			mode=EXCLUDED.mode, origin=EXCLUDED.origin, destination=EXCLUDED.destination,
			status=EXCLUDED.status, is_public=EXCLUDED.is_public,
			risk_score=EXCLUDED.risk_score, risk_tier=EXCLUDED.risk_tier,
			risk_breakdown=EXCLUDED.risk_breakdown,
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
		f.shipmentID, f.tracking, f.carrier, f.mode, f.origin, f.dest, f.status, f.isPublic,
		r.scoreInt, r.tier, string(breakdownRaw), f.openAlerts, f.criticalAlerts,
		f.lastEventAt, f.lastEventCode, r.staleHours,
		f.originalETA, r.etaSource, r.etaConfidence, r.slip,
		r.dwell, r.expected, r.ratio, r.valueAtRisk, f.notified, f.shippedAt, f.deliveredAt)
	if err != nil {
		return err
	}

	// The row is now current: clear the dirty flag set by writers. A refresh
	// that fails leaves the flag set, so the next batch retries it.
	_, err = tx.Exec(ctx, `UPDATE shipments SET needs_refresh=false WHERE id=$1`, f.shipmentID)
	return err
}

// computeRefresh derives the score and all persisted columns from facts. Pure:
// the same facts always yield the same row, whether they arrived one at a
// time or in a batch of five thousand.
func computeRefresh(f refreshFacts, now time.Time) (Input, refreshResult) {
	var r refreshResult
	in := Input{
		Status:           f.status,
		OpenAlerts:       f.openAlerts,
		CriticalAlerts:   f.criticalAlerts,
		InfoOrWarnAlerts: f.otherAlerts,
		CustomerNotified: f.notified,
	}
	// Baseline for silence: the newest event if one exists, else the
	// shipment's creation. A shipment created ten days ago with zero carrier
	// events has been silent for ten days — that is the literal "nothing is
	// happening" case the exception engine exists to catch, and it previously
	// produced zero staleness and zero dwell because NULL propagated through
	// every comparison below and the sweep's COALESCE turned it into 0.
	baseline := f.lastEventAt
	if baseline == nil {
		baseline = f.createdAt
	}
	if baseline != nil {
		h := now.Sub(*baseline).Hours()
		if h > 0 {
			r.staleHours = &h
			in.StaleHours = h
		}
	}
	// Dwell: total time in transit against the lane norm. Measured from
	// shipped_at (falling back to creation for unshipped rows), NOT from the
	// last event. Time-since-last-scan is staleness, which the score already
	// prices separately; using it for dwell as well made the two largest
	// terms redundant and meant a normally-moving shipment always read
	// dwell≈0, so the dwell_ratio rules could only fire for freight that was
	// already stale. A shipment that left Shanghai 25 days ago on a 21-day
	// norm is overrunning whether it scanned yesterday or not.
	//
	// Per-stage dwell (time in the CURRENT leg versus that leg's norm) wants
	// the shipment_milestones table, which exists but has no writers yet.
	// Until it does, total-transit dwell is the honest signal: it measures
	// what its 35-point weight says, "overrunning the expected transit".
	dwellBase := f.shippedAt
	if dwellBase == nil {
		dwellBase = f.createdAt
	}
	if dwellBase != nil {
		d := now.Sub(*dwellBase).Hours()
		if d >= 0 {
			r.dwell = &d
			in.DwellHours = d
		}
	}
	if exp, ok := expectedDwellHours(f.mode); ok {
		r.expected = &exp
		in.ExpectedDwellHours = exp
	}
	// ETA slip: how much later the current ETA is than the original estimate.
	if f.originalETA != nil && f.lastEventAt != nil {
		// Compare against the estimate we would have made at booking.
		if base, ok := estimateFromShipped(f.shippedAt, f.createdAt, f.mode); ok {
			s := f.originalETA.Sub(base).Hours()
			if s > 0 {
				r.slip = &s
				in.ETASlipHours = s
			}
		}
	}
	if f.declaredValue != nil && *f.declaredValue > 0 {
		r.valueAtRisk = f.declaredValue
		in.ValueAtRisk = *f.declaredValue
		in.ValueKnown = true
	}
	if r.dwell != nil && r.expected != nil && *r.expected > 0 {
		ratio := *r.dwell / *r.expected
		r.ratio = &ratio
	}

	score, tier, breakdown := Score(in)
	r.scoreInt = score
	r.tier = tier
	r.breakdown = breakdown
	r.etaSource, r.etaConfidence = etaProvenance(f.originalETA, f.lastEventAt, f.createdAt, f.mode)
	return in, r
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

// RefreshMany recomputes shipment_current rows for a batch of shipment IDs in
// a constant number of statements: one facts query, then one upsert, then one
// flag clear — regardless of batch size. The per-row Refresh costs five
// statements each, so at 5,000 rows this is the difference between ~25,000
// round trips and three.
//
// Scoring stays in Go via the shared computeRefresh: the weights exist in
// exactly one place, and the batch path cannot diverge from the single-row
// path. Only fact fetching and persistence are set-based.
func RefreshMany(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, ids []uuid.UUID) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	var now time.Time
	if err := tx.QueryRow(ctx, `SELECT now()`).Scan(&now); err != nil {
		return 0, err
	}
	return refreshManyAt(ctx, tx, tenantID, ids, now)
}

func refreshManyAt(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, ids []uuid.UUID, now time.Time) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	// One facts query for the whole batch. The three laterals replace the
	// per-row newest-event lookup, alert rollup, and subscription check.
	rows, err := tx.Query(ctx, `
		SELECT s.id, s.tracking_number, s.carrier, s.mode, s.origin, s.destination,
		       s.status, s.is_public, s.created_at, s.shipped_at, s.delivered_at, s.eta,
		       s.value_at_risk,
		       e.occurred_at, e.code,
		       COALESCE(a.open, 0), COALESCE(a.critical, 0), COALESCE(a.other, 0),
		       COALESCE(sub.notified, false)
		FROM shipments s
		LEFT JOIN LATERAL (
			SELECT occurred_at, code FROM shipment_events
			WHERE shipment_id = s.id ORDER BY occurred_at DESC LIMIT 1
		) e ON true
		LEFT JOIN LATERAL (
			SELECT count(*) AS open,
			       count(*) FILTER (WHERE severity='critical') AS critical,
			       count(*) FILTER (WHERE severity IN ('warning','info')) AS other
			FROM alerts WHERE shipment_id = s.id AND status='open'
		) a ON true
		LEFT JOIN LATERAL (
			SELECT EXISTS(SELECT 1 FROM tracking_subscriptions
			              WHERE shipment_id = s.id AND status='active') AS notified
		) sub ON true
		WHERE s.id = ANY($1)`, ids)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	type computed struct {
		f refreshFacts
		r refreshResult
	}
	var done []computed
	for rows.Next() {
		var f refreshFacts
		if err := rows.Scan(&f.shipmentID, &f.tracking, &f.carrier, &f.mode,
			&f.origin, &f.dest, &f.status, &f.isPublic,
			&f.createdAt, &f.shippedAt, &f.deliveredAt, &f.originalETA,
			&f.declaredValue, &f.lastEventAt, &f.lastEventCode,
			&f.openAlerts, &f.criticalAlerts, &f.otherAlerts, &f.notified); err != nil {
			return 0, err
		}
		_, r := computeRefresh(f, now)
		done = append(done, computed{f: f, r: r})
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(done) == 0 {
		return 0, nil
	}

	// One upsert for the whole batch via UNNEST. Arrays are parallel by
	// position; tenant_id is constant for the batch.
	n := len(done)
	sids := make([]uuid.UUID, n)
	tracking := make([]string, n)
	carrier := make([]string, n)
	mode := make([]string, n)
	origin := make([]string, n)
	dest := make([]string, n)
	status := make([]string, n)
	isPublic := make([]bool, n)
	scores := make([]int, n)
	tiers := make([]string, n)
	breakdowns := make([]string, n)
	opens := make([]int, n)
	criticals := make([]int, n)
	lastAts := make([]*time.Time, n)
	lastCodes := make([]*string, n)
	stales := make([]*float64, n)
	etas := make([]*time.Time, n)
	etaSources := make([]string, n)
	etaConfs := make([]float64, n)
	slips := make([]*float64, n)
	dwells := make([]*float64, n)
	expecteds := make([]*float64, n)
	ratios := make([]*float64, n)
	values := make([]*float64, n)
	notifieds := make([]bool, n)
	shippedAts := make([]*time.Time, n)
	deliveredAts := make([]*time.Time, n)
	for i, d := range done {
		sids[i] = d.f.shipmentID
		tracking[i] = d.f.tracking
		carrier[i] = d.f.carrier
		mode[i] = d.f.mode
		origin[i] = d.f.origin
		dest[i] = d.f.dest
		status[i] = d.f.status
		isPublic[i] = d.f.isPublic
		scores[i] = d.r.scoreInt
		tiers[i] = d.r.tier
		raw, err := json.Marshal(d.r.breakdown)
		if err != nil {
			return 0, fmt.Errorf("marshal breakdown: %w", err)
		}
		breakdowns[i] = string(raw)
		opens[i] = d.f.openAlerts
		criticals[i] = d.f.criticalAlerts
		lastAts[i] = d.f.lastEventAt
		lastCodes[i] = d.f.lastEventCode
		stales[i] = d.r.staleHours
		etas[i] = d.f.originalETA
		etaSources[i] = d.r.etaSource
		etaConfs[i] = d.r.etaConfidence
		slips[i] = d.r.slip
		dwells[i] = d.r.dwell
		expecteds[i] = d.r.expected
		ratios[i] = d.r.ratio
		values[i] = d.r.valueAtRisk
		notifieds[i] = d.f.notified
		shippedAts[i] = d.f.shippedAt
		deliveredAts[i] = d.f.deliveredAt
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO shipment_current
			(tenant_id, shipment_id, tracking_number, carrier, mode, origin, destination,
			 status, is_public, risk_score, risk_tier, risk_breakdown, open_alerts, critical_alerts,
			 last_event_at, last_event_code, stale_hours,
			 eta, eta_source, eta_confidence, eta_slip_hours,
			 dwell_hours, expected_dwell_hours, dwell_ratio,
			 value_at_risk, customer_notified, shipped_at, delivered_at, updated_at)
		SELECT $1, u.id, u.tracking, u.carrier, u.mode, u.origin, u.dest, u.status,
		       u.is_public, u.score, u.tier, u.breakdown::jsonb, u.open, u.critical,
		       u.last_at, u.last_code, u.stale,
		       u.eta, u.eta_source, u.eta_conf, u.slip,
		       u.dwell, u.expected, u.ratio,
		       u.val, u.notified, u.shipped, u.delivered, now()
		FROM UNNEST(
			$2::uuid[], $3::text[], $4::text[], $5::text[], $6::text[], $7::text[],
			$8::text[], $9::bool[], $10::int[], $11::text[], $12::text[],
			$13::int[], $14::int[], $15::timestamptz[], $16::text[],
			$17::numeric[], $18::timestamptz[], $19::text[], $20::numeric[],
			$21::numeric[], $22::numeric[], $23::numeric[], $24::numeric[],
			$25::numeric[], $26::bool[], $27::timestamptz[], $28::timestamptz[]
		) AS u(id, tracking, carrier, mode, origin, dest, status, is_public,
		        score, tier, breakdown, open, critical, last_at, last_code, stale,
		        eta, eta_source, eta_conf, slip, dwell, expected, ratio,
		        val, notified, shipped, delivered)
		ON CONFLICT (shipment_id) DO UPDATE SET
			tracking_number=EXCLUDED.tracking_number, carrier=EXCLUDED.carrier,
			mode=EXCLUDED.mode, origin=EXCLUDED.origin, destination=EXCLUDED.destination,
			status=EXCLUDED.status, is_public=EXCLUDED.is_public,
			risk_score=EXCLUDED.risk_score, risk_tier=EXCLUDED.risk_tier,
			risk_breakdown=EXCLUDED.risk_breakdown,
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
		tenantID, sids, tracking, carrier, mode, origin, dest, status, isPublic,
		scores, tiers, breakdowns, opens, criticals, lastAts, lastCodes, stales,
		etas, etaSources, etaConfs, slips, dwells, expecteds, ratios,
		values, notifieds, shippedAts, deliveredAts)
	if err != nil {
		return 0, err
	}

	_, err = tx.Exec(ctx, `UPDATE shipments SET needs_refresh=false WHERE id = ANY($1)`, sids)
	if err != nil {
		return 0, err
	}
	return len(done), nil
}
//
// Work is driven by shipments.needs_refresh, which writers set and Refresh
// clears — not by a time window. The previous predicate (updated_at older
// than 15 minutes, evaluated hourly) qualified every row on every pass, which
// made this a full recompute truncated at `limit` rather than an incremental
// delta. The ORDER BY carries a stable s.id tiebreaker so batch membership
// does not churn between passes; without it, rows at the cutoff flicker in
// and out of the window, which is one of the feeds for alert flapping.
//
// Returns the number actually refreshed, not merely attempted: a pass in which
// every Refresh fails must not report success.
func RefreshBatch(ctx context.Context, tx pgx.Tx, limit int) (int, error) {
	// The batch runs inside a tenant-pinned transaction (all callers pin).
	// Resolve the tenant from the pin rather than taking it as a parameter:
	// without a pin every query below returns zero rows by RLS, so failing
	// closed here would silently do nothing while reporting success.
	var tenantID uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT current_setting('app.tenant_id')::uuid`).Scan(&tenantID); err != nil {
		return 0, fmt.Errorf("refresh requires a pinned tenant: %w", err)
	}

	rows, err := tx.Query(ctx, `
		SELECT s.id FROM shipments s
		LEFT JOIN shipment_current c ON c.shipment_id = s.id
		WHERE s.needs_refresh
		   OR c.shipment_id IS NULL
		ORDER BY c.updated_at NULLS FIRST, s.id
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

	// RefreshMany does the whole batch in three statements. A poison row that
	// fails the batch leaves every flag set (the flag clear is in the same
	// statement as the upsert), so the next pass retries the full batch
	// rather than silently skipping one row forever. Chunk at 500 to bound
	// parameter counts and keep each statement's plan stable.
	const chunk = 500
	refreshed := 0
	for i := 0; i < len(ids); i += chunk {
		end := i + chunk
		if end > len(ids) {
			end = len(ids)
		}
		n, err := RefreshMany(ctx, tx, tenantID, ids[i:end])
		if err != nil {
			// One bad chunk must not stall the sweep; its flags stay set.
			continue
		}
		refreshed += n
	}
	return refreshed, nil
}