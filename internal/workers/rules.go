// Package workers contains the queue job handlers: the exception rules
// engine, the notification dispatcher and the ETA recalculator. All handlers
// are pure enough to unit-test without a database where possible.
package workers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tracksphere/tracksphere/internal/db"
	"github.com/tracksphere/tracksphere/internal/intel"
	"github.com/tracksphere/tracksphere/internal/model"
	"github.com/tracksphere/tracksphere/internal/readmodel"
)

// jobPayload is the common envelope emitted by shipments.Service.IngestEvent.
type jobPayload struct {
	ShipmentID uuid.UUID `json:"shipmentId"`
	TenantID   uuid.UUID `json:"tenantId"`
	EventID    uuid.UUID `json:"eventId"`
	Code       string    `json:"code"`
	Status     string    `json:"status"`
}

func decodePayload(b []byte) (*jobPayload, error) {
	var p jobPayload
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, fmt.Errorf("decode payload: %w", err)
	}
	if p.ShipmentID == uuid.Nil || p.TenantID == uuid.Nil {
		return nil, fmt.Errorf("payload missing shipmentId/tenantId")
	}
	return &p, nil
}

// payloadBool reads an optional boolean flag from a job payload without
// re-decoding into the typed struct.
func payloadBool(b []byte, key string) (bool, error) {
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return false, err
	}
	v, ok := m[key].(bool)
	return ok && v, nil
}

// ── Exception rules engine ─────────────────────────────────────────────

// ruleOutcome describes an alert a rule wants to raise.
type ruleOutcome struct {
	Kind     string
	Severity string
	Title    string
	Message  string
}

// evaluateRules is a pure function: shipment state + latest event code →
// alerts to raise.
//
// IMPORTANT: only EVENT-driven conditions live here. Time-based detection
// (staleness, dwell over the lane norm, ETA slip) is evaluated by the sweep
// on a clock — see sweep.go. Evaluating an elapsed-time condition here produced
// time-based detection that fired at arbitrary moments, whenever some unrelated
// scan happened to land, with a detected_at an hour too late. Confident,
// mistimed criticals are worse than silence, because they teach a team to
// distrust the channel.
func evaluateRules(status, code string) []ruleOutcome {
	var out []ruleOutcome
	switch code {
	case "CUSTOMS_HOLD":
		out = append(out, ruleOutcome{
			Kind: "customs_hold", Severity: "critical",
			Title:   "Customs hold detected",
			Message: "Carrier reported a customs hold. Clearance documents may be required.",
		})
	case "PORT_CONGESTION":
		out = append(out, ruleOutcome{
			Kind: "port_congestion", Severity: "warning",
			Title:   "Port congestion",
			Message: "High dwell time reported at the terminal; arrival may slip.",
		})
	case "ETA_REVISED":
		// info: never interrupts, never reaches the customer — a timeline
		// entry. Recorded so the change is auditable.
		out = append(out, ruleOutcome{
			Kind: "eta_revised", Severity: "info",
			Title:   "ETA revised",
			Message: "Carrier published a new estimated time of arrival.",
		})
	case "BLANK_SAILING":
		out = append(out, ruleOutcome{
			Kind: "delay", Severity: "critical",
			Title:   "Blank sailing",
			Message: "Carrier cancelled the scheduled service. Rebooking is now urgent.",
		})
	case "DELIVERY_ATTEMPTED":
		out = append(out, ruleOutcome{
			Kind: "delivery_failed", Severity: "warning",
			Title:   "Delivery attempt failed",
			Message: "The courier could not complete delivery. Check the address and contact details.",
		})
	}
	if status == model.StatusException {
		out = append(out, ruleOutcome{
			Kind: "delay", Severity: "critical",
			Title:   "Shipment in exception",
			Message: "Carrier flagged this shipment as an exception.",
		})
	}
	return out
}

// HandleEvaluateRules persists alerts raised by evaluateRules.
//
// This handler is EVENT-driven only. Time-based rules live in sweep.go and run
// on a lease-held clock, which is the only way "nothing has happened for 48
// hours" can ever be detected. The partial unique index dedupes replayed
// webhooks; a replayed event therefore cannot re-raise an existing exception.
func HandleEvaluateRules(pool *pgxpool.Pool, log *slog.Logger) func(context.Context, []byte) error {
	return func(ctx context.Context, body []byte) error {
		p, err := decodePayload(body)
		if err != nil {
			return err
		}
		// A stale event was recorded on the timeline for completeness but did
		// not move shipment state; it must not raise fresh exceptions either,
		// or a replayed scan would re-alert hours after the fact.
		if stale, _ := payloadBool(body, "stale"); stale {
			return nil
		}
		return db.WithTenant(ctx, pool, p.TenantID, func(tx pgx.Tx) error {
			var status, origin, dest, carrier, mode string
			if err := tx.QueryRow(ctx, `
				SELECT status, origin, destination, carrier, mode
				FROM shipments WHERE id=$1`, p.ShipmentID).
				Scan(&status, &origin, &dest, &carrier, &mode); err != nil {
				return err
			}
			outcomes := evaluateRules(status, p.Code)

			// P3 disruption corridors are event-shaped (a port or lane reports
			// disruption; shipments on it are affected immediately), so they
			// are evaluated here rather than on the sweep.
			haystack := origin + " " + dest
			if legs, err := tx.Query(ctx,
				`SELECT origin, destination FROM shipment_legs WHERE shipment_id=$1`, p.ShipmentID); err == nil {
				for legs.Next() {
					var o, d string
					if err := legs.Scan(&o, &d); err == nil {
						haystack += " " + o + " " + d
					}
				}
				legs.Close()
			}
			for _, z := range intel.DisruptionMatch(ctx, tx, haystack) {
				outcomes = append(outcomes, ruleOutcome{
					Kind: "disruption", Severity: z.Severity,
					Title:   z.Name,
					Message: z.Message,
				})
			}

			for _, o := range outcomes {
				var inserted bool
				var dueAt *time.Time
				if err := tx.QueryRow(ctx, `
					INSERT INTO alerts
						(tenant_id, shipment_id, kind, severity, title, message,
						 detected_at, last_seen_at, due_at, value_at_risk)
					VALUES ($1,$2,$3,$4,$5,$6, now(), now(),
					        now() + make_interval(mins => coalesce(sla_minutes_for($1, $4), 1440)),
					        (SELECT sc.value_at_risk FROM shipment_current sc WHERE sc.shipment_id=$2))
					ON CONFLICT (shipment_id, kind) WHERE status='open' DO UPDATE
						SET last_seen_at = now(),
						    acknowledged_at = COALESCE(alerts.acknowledged_at, now())
					RETURNING (xmax = 0), due_at`,
					p.TenantID, p.ShipmentID, o.Kind, o.Severity, o.Title, o.Message).
					Scan(&inserted, &dueAt); err != nil {
					return err
				}
				if !inserted {
					continue
				}
				log.Info("exception raised", "tenant", p.TenantID, "shipment", p.ShipmentID,
					"kind", o.Kind, "severity", o.Severity)
				// Only a critical alert may request an interruption, and even
				// then the dispatcher's budget decides.
				interrupt := o.Severity == "critical"
				if err := enqueueAlertNotify(ctx, tx, p.TenantID, p.ShipmentID,
					o.Severity, interrupt, o.Kind); err != nil {
					return err
				}
			}

			// Read model must reflect the new exceptions before anyone sorts by
			// risk, or the queue ordering is stale until the next event.
			if err := readmodel.Refresh(ctx, tx, p.ShipmentID); err != nil {
				log.Warn("refresh read model", "shipment", p.ShipmentID, "err", err)
			}

			notice, _ := json.Marshal(map[string]any{
				"type":        "alert.changed",
				"tenant_id":   p.TenantID,
				"shipment_id": p.ShipmentID,
			})
			_, err := tx.Exec(ctx, `SELECT notify_tracksphere($1)`, string(notice))
			return err
		})
	}
}
