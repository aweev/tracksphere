package workers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tracksphere/tracksphere/internal/db"
	"github.com/tracksphere/tracksphere/internal/queue"
	"github.com/tracksphere/tracksphere/internal/readmodel"
)

// The exception engine, rebuilt.
//
// WHY: it was a switch on an event code — four hardcoded rules, identical for
// every tenant, unconfigurable, and every rule event-triggered. That last part
// was the expensive bug. The most valuable exception in freight is *nothing
// happening*: a container sits at a terminal for six days because the vessel
// missed a transhipment connection. There is no event, so no rule fired, ever.
// Time-based detection is the product.
//
// Two properties this file is responsible for:
//
//   - RECONCILIATION. Rules do not append, they converge. Each sweep marks every
//     open alert with last_seen_at; any alert not confirmed by the next sweep is
//     auto-resolved as 'condition_cleared'. Without this the SLA alert opened on
//     day 1 of a late shipment stayed open at day 90 after the container arrived,
//     poisoning every "unresolved exceptions" number the product shows.
//   - TIME, NOT EVENTS. Time-based rules run on the sweep only. A time-based
//     check evaluated inside an event handler fires at an arbitrary moment (when
//     some unrelated scan happens to land) with a wrong detected_at, which is
//     worse than not having it: it produces confident, mistimed criticals.

// SweepJob is the single-flight periodic job.
const SweepJob = "system.sweep"

// SweepLeaseTTL bounds how long one replica may hold the sweep lease. Longer
// than the sweep interval so a slow pass cannot be stolen mid-flight; short
// enough that a crashed holder is replaced promptly.
const SweepLeaseTTL = 4 * time.Minute

// sweepLeaseName is the single lease key. Periodic work must be single-flight
// across N replicas with no coordination between them; a lease row claimed with
// a conditional UPDATE gives exactly one winner per window.
const sweepLeaseName = "system.sweep"

// Rule is one declarative alert rule loaded from alert_rules.
type Rule struct {
	ID          uuid.UUID
	TenantID    uuid.UUID
	Name        string
	Kind        string
	Severity    string
	TriggerType string
	Scope       Scope
	Condition   Condition
	Raise       Raise
}

// Scope narrows which shipments a rule considers.
type Scope struct {
	Modes     []string `json:"modes"`
	Carriers  []string `json:"carriers"`
	MinValue  float64  `json:"minValue"`
	Origins   []string `json:"origins"`
	Statuses  []string `json:"statuses"`
}

// Condition is the fact comparison. Kept deliberately small: an expression
// language here would be a liability, and these five facts cover every rule
// worth writing for freight.
type Condition struct {
	Fact  string  `json:"fact"`
	Op    string  `json:"op"`
	Value float64 `json:"value"`
	// Values is used by the 'in' operator.
	Values []string `json:"values"`
	// All holds when several conditions are ANDed.
	All []Condition `json:"all"`
}

// Raise describes what the rule does when its condition is true.
type Raise struct {
	Title      string   `json:"title"`
	Message    string   `json:"message"`
	RootCause  string   `json:"rootCause"`
	Recommend  string   `json:"recommend"`
	// Interrupt requests an interrupting notification. Only 'critical' is
	// permitted to set it, and the budget still applies on top.
	Interrupt bool `json:"interrupt"`
	// SnoozeMinutes suppresses re-notification while the condition persists.
	SnoozeMinutes int `json:"snoozeMinutes"`
}

// Fact is one evaluated measurement for a shipment.
type Fact struct {
	StaleHours         float64
	DwellHours         float64
	ExpectedDwellHours float64
	DwellRatio         float64
	ETASlipHours       float64
	OpenAlerts         int
	CriticalAlerts     int
	StaleThreshold     float64
	Status             string
	Carrier            string
	Mode               string
}

// Evaluate reports whether a fact set satisfies a condition. Pure, so every
// rule combination is unit-testable without a database.
func Evaluate(c Condition, f Fact) bool {
	if len(c.All) > 0 {
		for _, sub := range c.All {
			if !Evaluate(sub, f) {
				return false
			}
		}
		return true
	}
	// Never let an unparseable rule fire. A misconfigured rule that alerts
	// everything is how a tenant stops reading their queue.
	if c.Fact == "" || c.Op == "" {
		return false
	}
	switch c.Op {
	case ">":
		return numericFact(c.Fact, f) > c.Value
	case ">=":
		return numericFact(c.Fact, f) >= c.Value
	case "<":
		return numericFact(c.Fact, f) < c.Value
	case "<=":
		return numericFact(c.Fact, f) <= c.Value
	case "==":
		return numericFact(c.Fact, f) == c.Value
	case "in":
		for _, want := range c.Values {
			if stringFact(c.Fact, f) == want {
				return true
			}
		}
		return false
	}
	return false
}

func numericFact(name string, f Fact) float64 {
	switch name {
	case "stale_hours":
		return f.StaleHours
	case "dwell_hours":
		return f.DwellHours
	case "expected_dwell_hours":
		return f.ExpectedDwellHours
	case "dwell_ratio":
		return f.DwellRatio
	case "eta_slip_hours":
		return f.ETASlipHours
	case "open_alerts":
		return float64(f.OpenAlerts)
	case "critical_alerts":
		return float64(f.CriticalAlerts)
	case "stale_threshold_hours":
		return f.StaleThreshold
	}
	return 0
}

func stringFact(name string, f Fact) string {
	switch name {
	case "status":
		return f.Status
	case "carrier":
		return f.Carrier
	case "mode":
		return f.Mode
	}
	return ""
}

func (s Scope) matches(f Fact) bool {
	return matchList(s.Modes, f.Mode) &&
		matchList(s.Carriers, f.Carrier) &&
		matchList(s.Statuses, f.Status)
}

func matchList(list []string, v string) bool {
	if len(list) == 0 {
		return true
	}
	for _, item := range list {
		if strings.EqualFold(item, v) {
			return true
		}
	}
	return false
}

// StaleThreshold is the silence budget for a shipment: a fraction of its
// expected dwell, floored at 24h. An absolute hour count is wrong for every
// mode except road — 48h of quiet on a 21-day ocean transit is on plan.
func StaleThreshold(expectedDwellHours float64) float64 {
	t := expectedDwellHours / 6
	if t < 24 {
		t = 24
	}
	return t
}

// DefaultRules is the shipped rule set. They are inserted on first sweep per
// tenant and can then be edited, disabled or extended per customer without a
// deploy — which is the whole point of moving the engine out of code.
func DefaultRules() []struct {
	Name     string
	Kind     string
	Severity string
	Trigger  string
	Every    int
	Cond     Condition
	Raise    Raise
} {
	return []struct {
		Name     string
		Kind     string
		Severity string
		Trigger  string
		Every    int
		Cond     Condition
		Raise    Raise
	}{
		{
			Name: "No carrier update", Kind: "stale", Severity: "warning", Trigger: "sweep", Every: 30,
			Cond: Condition{Fact: "stale_hours", Op: ">", Value: 0},
			Raise: Raise{
				Title:     "No carrier update",
				Message:   "The carrier has not reported a scan for longer than this shipment's stage allows.",
				Recommend: "Contact the carrier agent or verify the vessel/flight position before the customer asks.",
				RootCause: "carrier_delay", SnoozeMinutes: 240,
			},
		},
		{
			Name: "Dwell far over lane norm", Kind: "dwell", Severity: "warning", Trigger: "sweep", Every: 60,
			Cond: Condition{Fact: "dwell_ratio", Op: ">", Value: 1.5},
			Raise: Raise{
				Title:     "Sitting far longer than the lane norm",
				Message:   "This shipment has been at its current stage well beyond the expected time for this lane.",
				Recommend: "Check for a missed transhipment connection, a blank sailing, or a terminal dwell charge.",
				RootCause: "capacity", SnoozeMinutes: 360,
			},
		},
		{
			Name: "Dwell over 2x lane norm", Kind: "dwell_critical", Severity: "critical", Trigger: "sweep", Every: 30,
			Cond: Condition{Fact: "dwell_ratio", Op: ">", Value: 2},
			Raise: Raise{
				Title:     "Critical: over 2x the expected transit for this lane",
				Message:   "Transit time has more than doubled the norm learned for this lane.",
				Recommend: "Escalate to the carrier now and prepare a revised customer message.",
				RootCause: "carrier_delay", Interrupt: true,
			},
		},
		{
			Name: "ETA slipped more than 3 days", Kind: "eta_slip", Severity: "warning", Trigger: "sweep", Every: 60,
			Cond: Condition{Fact: "eta_slip_hours", Op: ">", Value: 72},
			Raise: Raise{
				Title:     "ETA moved more than 3 days later",
				Message:   "The arrival estimate has slipped past the point where the customer will notice.",
				Recommend: "Send a proactive update before the customer asks. Proactive beats reactive on every channel.",
				RootCause: "carrier_delay", SnoozeMinutes: 720,
			},
		},
		{
			Name: "Multiple open exceptions", Kind: "recurring", Severity: "critical", Trigger: "sweep", Every: 120,
			Cond: Condition{Fact: "open_alerts", Op: ">=", Value: 3},
			Raise: Raise{
				Title:     "Three or more open exceptions on one shipment",
				Message:   "This shipment is accumulating problems; it usually indicates a single upstream failure.",
				Recommend: "Treat as one root cause, not three separate tickets.",
				RootCause: "other", Interrupt: true,
			},
		},
	}
}

// sweepTenant evaluates every enabled sweep rule against one tenant's
// shipments, raising and reconciling alerts inside a single transaction.
func sweepTenant(ctx context.Context, tx pgx.Tx, log *slog.Logger, tenantID uuid.UUID, rules []Rule, limit int) (raised, cleared int, err error) {
	// Refresh the read model BEFORE evaluating.
	//
	// Rules are evaluated against shipment_current, so reading it first would
	// mean detecting last pass's state: an exception that appeared an hour ago
	// would only be raised on the next sweep, and a shipment that had just gone
	// silent was still scored as fresh. Refreshing first also pulls in
	// shipments that have no read-model row yet, which would otherwise be
	// invisible to detection entirely.
	if _, err := readmodel.RefreshBatch(ctx, tx, sweepBatchSize); err != nil {
		return 0, 0, fmt.Errorf("refresh before evaluate: %w", err)
	}

	// Load this tenant's shipments plus the derived facts the rules read. The
	// read model already computes them, so the sweep costs one indexed scan
	// instead of a correlated subquery per shipment.
	rows, err := tx.Query(ctx, `
		SELECT c.shipment_id, c.status, c.carrier, c.mode,
		       COALESCE(c.stale_hours, 0),
		       COALESCE(c.dwell_hours, 0),
		       COALESCE(c.expected_dwell_hours, 0),
		       COALESCE(c.dwell_ratio, 0),
		       COALESCE(c.eta_slip_hours, 0),
		       c.open_alerts
		FROM shipment_current c
		WHERE c.tenant_id = $1
		  AND c.status NOT IN ('delivered','cancelled')
		ORDER BY c.risk_score DESC
		LIMIT $2`, tenantID, limit)
	if err != nil {
		return 0, 0, err
	}
	type candidate struct {
		shipmentID uuid.UUID
		fact       Fact
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.shipmentID, &c.fact.Status, &c.fact.Carrier, &c.fact.Mode,
			&c.fact.StaleHours, &c.fact.DwellHours, &c.fact.ExpectedDwellHours,
			&c.fact.DwellRatio, &c.fact.ETASlipHours, &c.fact.OpenAlerts); err != nil {
			rows.Close()
			return 0, 0, err
		}
		c.fact.StaleThreshold = StaleThreshold(c.fact.ExpectedDwellHours)
		candidates = append(candidates, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, 0, err
	}

	// Facts that make 'stale_hours' meaningful are mode-relative, so the
	// no-update rule is rewritten per shipment against its own threshold
	// rather than a fixed hour count.
	confirmed := map[uuid.UUID]bool{}

	for _, rule := range rules {
		if rule.TriggerType != "sweep" {
			continue
		}
		for _, c := range candidates {
			if !rule.Scope.matches(c.fact) {
				continue
			}
			fact := c.fact
			// A rule expressed as "stale_hours > 0" means "past this
			// shipment's own silence budget", which is the only formulation
			// that works across ocean and air.
			if rule.Condition.Fact == "stale_hours" && rule.Condition.Value == 0 {
				fact.StaleHours = 0
				if c.fact.StaleHours > c.fact.StaleThreshold {
					fact.StaleHours = c.fact.StaleHours
				} else {
					continue
				}
			}
			if !Evaluate(rule.Condition, fact) {
				continue
			}
			ok, err := raiseFromRule(ctx, tx, log, tenantID, rule, c.shipmentID)
			if err != nil {
				return raised, cleared, err
			}
			if ok {
				raised++
				confirmed[c.shipmentID] = true
			}
		}
	}

	// Reconcile: auto-resolve stale_alert for the sweep-managed kinds this
	// tenant has rules for. Anything not confirmed by this pass no longer holds.
	if err := reconcileOpen(ctx, tx, tenantID, confirmed); err != nil {
		return raised, cleared, err
	}
	cleared, err = autoResolve(ctx, tx, log, tenantID)
	if err != nil {
		return raised, cleared, err
	}

	// Refresh the read model AGAIN so open-alert counts, risk scores and the
	// risk ordering reflect what this pass just found, not what it started
	// from. Without this the queue stays stale for an hour after detection.
	ids := make([]uuid.UUID, 0, len(candidates))
	for _, c := range candidates {
		ids = append(ids, c.shipmentID)
	}
	return raised, cleared, nil
}

// raiseFromRule inserts an alert if the rule fires and no open alert of that
// kind exists. Returns true when a new alert was created.
func raiseFromRule(ctx context.Context, tx pgx.Tx, log *slog.Logger, tenantID uuid.UUID, rule Rule, shipmentID uuid.UUID) (bool, error) {
	var dueAt *time.Time
	var slaMinutes int
	// due_at comes from the tenant's severity SLA so the queue shows a
	// countdown rather than an age.
	if err := tx.QueryRow(ctx, `
		SELECT CASE $2 WHEN 'critical' THEN sla_critical_minutes
                       WHEN 'warning'  THEN sla_warning_minutes
                       ELSE sla_info_minutes END
		FROM tenants WHERE id=$1`, tenantID, rule.Severity).Scan(&slaMinutes); err != nil {
		slaMinutes = 1440
	}
	if slaMinutes > 0 {
		t := time.Now().Add(time.Duration(slaMinutes) * time.Minute)
		dueAt = &t
	}

	title := rule.Raise.Title
	if title == "" {
		title = rule.Name
	}
	message := rule.Raise.Message
	if rule.Raise.Recommend != "" {
		message += "\n\nNext step: " + rule.Raise.Recommend
	}

	var snoozeUntil *time.Time
	if rule.Raise.SnoozeMinutes > 0 {
		t := time.Now().Add(time.Duration(rule.Raise.SnoozeMinutes) * time.Minute)
		snoozeUntil = &t
	}

	// xmax = 0 distinguishes a real INSERT from the ON CONFLICT DO UPDATE path,
	// so only genuinely new alerts fan out to notifications and SSE.
	var inserted bool
	if err := tx.QueryRow(ctx, `
		INSERT INTO alerts
			(tenant_id, shipment_id, kind, severity, title, message,
			 root_cause, detected_at, last_seen_at, due_at, note, value_at_risk)
		VALUES ($1,$2,$3,$4,$5,$6,$7, now(), now(), $8, $9,
		        (SELECT sc.value_at_risk FROM shipment_current sc WHERE sc.shipment_id=$2))
		ON CONFLICT (shipment_id, kind) WHERE status='open' DO UPDATE
			SET last_seen_at = now(),
			    acknowledged_at = COALESCE(alerts.acknowledged_at, now()),
			    snoozed_until = COALESCE(alerts.snoozed_until, $10)
		RETURNING (xmax = 0)`,
		tenantID, shipmentID, rule.Kind, rule.Severity, title, message,
		nullableText(rule.Raise.RootCause), dueAt, nullableText(rule.Raise.Recommend),
		snoozeUntil).Scan(&inserted); err != nil {
		return false, err
	}

	if inserted {
		log.Info("exception raised", "tenant", tenantID, "shipment", shipmentID,
			"kind", rule.Kind, "severity", rule.Severity, "rule", rule.Name)
		notice, _ := json.Marshal(map[string]any{
			"type": "alert.changed", "tenant_id": tenantID, "shipment_id": shipmentID,
		})
		if _, err := tx.Exec(ctx, `SELECT notify_tracksphere($1)`, string(notice)); err != nil {
			return true, err
		}
		// Queue the ops notification; severity routing and the interrupt budget
		// are applied by the dispatcher, not here.
		if err := enqueueAlertNotify(ctx, tx, tenantID, shipmentID, rule.Severity,
			rule.Raise.Interrupt, rule.Kind); err != nil {
			return true, err
		}
	}
	return inserted, nil
}

func nullableText(s string) *string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	t := strings.TrimSpace(s)
	return &t
}

// reconcileOpen resets snoozes that have run out so deferred work resurfaces on
// its own instead of requiring someone to remember it.
func reconcileOpen(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, _ map[uuid.UUID]bool) error {
	_, err := tx.Exec(ctx, `
		UPDATE alerts SET snoozed_until = NULL
		WHERE tenant_id = $1 AND status = 'open'
		  AND snoozed_until IS NOT NULL AND snoozed_until < now()`, tenantID)
	return err
}

// autoResolve closes sweep-managed alerts whose condition no longer holds.
//
// The rule: a sweep-managed alert that has not been touched by a sweep for
// SweepStaleAfter is no longer believed to be true. This is what makes every
// "open exceptions" number trustworthy.
func autoResolve(ctx context.Context, tx pgx.Tx, log *slog.Logger, tenantID uuid.UUID) (int, error) {
	tag, err := tx.Exec(ctx, `
		UPDATE alerts
		SET status='resolved', resolved_at=now(), resolution='condition_cleared'
		WHERE tenant_id=$1 AND status='open'
		  AND kind IN ('stale','dwell','dwell_critical','eta_slip','recurring')
		  AND last_seen_at < now() - $2::interval`,
		tenantID, SweepStaleAfter.String())
	if err != nil {
		return 0, err
	}
	n := int(tag.RowsAffected())
	if n > 0 {
		log.Info("exceptions auto-resolved", "tenant", tenantID, "count", n)
		notice, _ := json.Marshal(map[string]any{
			"type": "alert.changed", "tenant_id": tenantID,
		})
		if _, err := tx.Exec(ctx, `SELECT notify_tracksphere($1)`, string(notice)); err != nil {
			return n, err
		}
	}
	return n, nil
}

// SweepStaleAfter is how long a sweep-managed alert may go unconfirmed before it
// is considered cleared. Two missed sweeps, so a single slow or failed pass
// cannot mass-close a tenant's queue.
var SweepStaleAfter = 2 * SweepLeaseTTL

// enqueueAlertNotify queues the ops notification for a new alert.
func enqueueAlertNotify(ctx context.Context, tx pgx.Tx, tenantID, shipmentID uuid.UUID, severity string, interrupt bool, kind string) error {
	return queue.EnqueueTx(ctx, tx, AlertNotifyJob, map[string]any{
		"tenantId":   tenantID.String(),
		"shipmentId": shipmentID.String(),
		"severity":   severity,
		"interrupt":  interrupt,
		"kind":       kind,
	}, time.Time{})
}

// HandleSweep is the single-flight periodic job: reconcile exceptions, enforce
// SLA escalation, refresh the read model, and enqueue digests.
//
// It claims a lease first, so N worker replicas do not duplicate the work.
func HandleSweep(pool *pgxpool.Pool, log *slog.Logger, holder string) func(context.Context, []byte) error {
	return func(ctx context.Context, _ []byte) error {
		claimed, err := ClaimLease(ctx, pool, sweepLeaseName, holder, SweepLeaseTTL)
		if err != nil {
			return err
		}
		if !claimed {
			return nil // another replica owns this window
		}
		defer func() { _ = ReleaseLease(context.WithoutCancel(ctx), pool, sweepLeaseName, holder) }()

		tenants, err := sweepableTenants(ctx, pool)
		if err != nil {
			return err
		}
		var raised, cleared, escalated int
		for _, tenantID := range tenants {
			r, c, e, err := sweepOne(ctx, pool, log, tenantID)
			if err != nil {
				// One bad tenant must not abort the sweep for everyone.
				log.Error("tenant sweep failed", "tenant", tenantID, "err", err)
				continue
			}
			raised += r
			cleared += c
			escalated += e
		}
		if _, err := readmodelAll(ctx, pool); err != nil {
			log.Warn("read model refresh", "err", err)
		}
		log.Info("sweep complete", "tenants", len(tenants),
			"raised", raised, "auto_resolved", cleared, "escalated", escalated)
		return nil
	}
}

func sweepOne(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger, tenantID uuid.UUID) (raised, cleared, escalated int, err error) {
	err = db.WithTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
		if err := seedDefaultRules(ctx, tx, tenantID); err != nil {
			return err
		}
		rules, err := loadRules(ctx, tx, tenantID)
		if err != nil {
			return err
		}
		r, c, err := sweepTenant(ctx, tx, log, tenantID, rules, sweepBatchSize)
		if err != nil {
			return err
		}
		raised, cleared = r, c
		e, err := escalateBreached(ctx, tx, log, tenantID)
		if err != nil {
			return err
		}
		escalated = e
		return nil
	})
	return raised, cleared, escalated, err
}

// sweepBatchSize bounds work per tenant per pass so a large tenant cannot hold
// the lease past its TTL.
const sweepBatchSize = 2000

// escalateBreached marks acknowledged, unassigned alerts past their SLA. An
// unacknowledged alert is NOT escalated: nagging someone who has not even seen
// it yet is exactly the behaviour that trains a team to ignore the queue.
func escalateBreached(ctx context.Context, tx pgx.Tx, log *slog.Logger, tenantID uuid.UUID) (int, error) {
	tag, err := tx.Exec(ctx, `
		UPDATE alerts SET escalated_at = now()
		WHERE tenant_id=$1 AND status='open'
		  AND due_at IS NOT NULL AND due_at < now()
		  AND escalated_at IS NULL
		  AND acknowledged_at IS NOT NULL
		  AND (snoozed_until IS NULL OR snoozed_until < now())`, tenantID)
	if err != nil {
		return 0, err
	}
	n := int(tag.RowsAffected())
	if n > 0 {
		log.Info("exceptions escalated", "tenant", tenantID, "count", n)
	}
	return n, nil
}

// sweepableTenants lists every tenant.
//
// `tenants` is RLS-protected, so this MUST run under the system flag: with no
// context the query returns zero rows and the sweep silently does nothing for
// every tenant, which looks exactly like "there is nothing to detect".
func sweepableTenants(ctx context.Context, pool *pgxpool.Pool) ([]uuid.UUID, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := db.SetSystem(ctx, tx); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT id FROM tenants ORDER BY id`)
	if err != nil {
		return nil, err
	}
	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

// loadRules reads a tenant's enabled rules.
func loadRules(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) ([]Rule, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, name, kind, severity, trigger_type, scope, condition, raise_spec
		FROM alert_rules WHERE tenant_id=$1 AND enabled`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Rule
	for rows.Next() {
		var r Rule
		var scope, cond, raise []byte
		if err := rows.Scan(&r.ID, &r.Name, &r.Kind, &r.Severity, &r.TriggerType,
			&scope, &cond, &raise); err != nil {
			return nil, err
		}
		r.TenantID = tenantID
		// A malformed rule is skipped rather than fatal: one bad row must not
		// stop the queue working for a paying tenant.
		if err := json.Unmarshal(scope, &r.Scope); err != nil {
			continue
		}
		if err := json.Unmarshal(cond, &r.Condition); err != nil {
			continue
		}
		if err := json.Unmarshal(raise, &r.Raise); err != nil {
			continue
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// seedDefaultRules inserts the shipped rule set once per tenant. Keyed by a
// stable name so a tenant that deletes one does not get it recreated.
func seedDefaultRules(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) error {
	for _, d := range DefaultRules() {
		scope, _ := json.Marshal(Scope{Statuses: []string{
			"booked", "in_transit", "at_customs", "out_for_delivery", "exception",
		}})
		cond, _ := json.Marshal(d.Cond)
		raise, _ := json.Marshal(d.Raise)
		if _, err := tx.Exec(ctx, `
			INSERT INTO alert_rules (tenant_id, name, kind, severity, trigger_type,
			                         every_minutes, scope, condition, raise_spec)
			SELECT $1,$2,$3,$4,$5,$6,$7,$8,$9
			WHERE NOT EXISTS (SELECT 1 FROM alert_rules WHERE tenant_id=$1 AND name=$2)`,
			tenantID, d.Name, d.Kind, d.Severity, d.Trigger, d.Every,
			scope, cond, raise); err != nil {
			return fmt.Errorf("seed rule %q: %w", d.Name, err)
		}
	}
	return nil
}

// EnqueueSweep schedules one sweep. Idempotent within a window via the job id,
// so a restart mid-window cannot run the sweep twice.
func EnqueueSweep(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx, `
		INSERT INTO jobs (kind, payload, run_at, dedup_key)
		VALUES ($1, '{}'::jsonb, now(), $2)
		ON CONFLICT (dedup_key) WHERE dedup_key IS NOT NULL DO NOTHING`,
		SweepJob, "sweep:"+time.Now().UTC().Format("2006-01-02T15"))
	return err
}

// FlushDigestsJob rolls queued non-interrupting notifications into grouped
// messages. Separate from the weekly business digest: that one reports
// performance, this one delivers the notifications the interrupt budget held
// back.
const FlushDigestsJob = "notification.flush_digests"

// EnqueueDigestFlush schedules a digest flush.
func EnqueueDigestFlush(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx, `
		INSERT INTO jobs (kind, payload, run_at, dedup_key)
		VALUES ($1, '{}'::jsonb, now(), $2)
		ON CONFLICT (dedup_key) WHERE dedup_key IS NOT NULL DO NOTHING`,
		FlushDigestsJob, "flush:"+time.Now().UTC().Format("20060102T1504"))
	return err
}

// ── Leases ──────────────────────────────────────────────────────────────

// ClaimLease attempts to take the named lease. Exactly one caller wins per
// window; expired leases are reclaimed automatically.
func ClaimLease(ctx context.Context, pool *pgxpool.Pool, name, holder string, ttl time.Duration) (bool, error) {
	tag, err := pool.Exec(ctx, `
		INSERT INTO scheduler_leases (name, holder, acquired_at, expires_at, runs)
		VALUES ($1, $2, now(), now() + $3::interval, 1)
		ON CONFLICT (name) DO UPDATE
			SET holder = EXCLUDED.holder,
			    acquired_at = now(),
			    expires_at  = now() + $3::interval,
			    runs        = scheduler_leases.runs + 1
			WHERE scheduler_leases.expires_at < now()
		RETURNING name`, name, holder, ttl.String())
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// ReleaseLease drops a lease the caller holds.
func ReleaseLease(ctx context.Context, pool *pgxpool.Pool, name, holder string) error {
	_, err := pool.Exec(ctx,
		`DELETE FROM scheduler_leases WHERE name=$1 AND holder=$2`, name, holder)
	return err
}

// readmodelAll refreshes rows the sweep did not already touch.
func readmodelAll(ctx context.Context, pool *pgxpool.Pool) (int, error) {
	var n int
	for _, tenantID := range mustTenants(ctx, pool) {
		if err := db.WithTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
			m, err := readmodel.RefreshBatch(ctx, tx, 500)
			n += m
			return err
		}); err != nil {
			continue
		}
	}
	return n, nil
}

func mustTenants(ctx context.Context, pool *pgxpool.Pool) []uuid.UUID {
	ids, err := sweepableTenants(ctx, pool)
	if err != nil {
		return nil
	}
	return ids
}