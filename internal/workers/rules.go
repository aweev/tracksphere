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
	"github.com/tracksphere/tracksphere/internal/model"
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

// ── Exception rules engine ─────────────────────────────────────────────

// ruleOutcome describes an alert a rule wants to raise.
type ruleOutcome struct {
	Kind     string
	Severity string
	Title    string
	Message  string
}

// evaluateRules is a pure function: shipment state + latest event code →
// alerts to raise. Keeping it pure makes the rules table-driven and testable.
func evaluateRules(status, code string, eta *time.Time, now time.Time) []ruleOutcome {
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
		out = append(out, ruleOutcome{
			Kind: "eta_revised", Severity: "info",
			Title:   "ETA revised",
			Message: "Carrier published a new estimated time of arrival.",
		})
	}
	if status == model.StatusException {
		out = append(out, ruleOutcome{
			Kind: "delay", Severity: "critical",
			Title:   "Shipment in exception",
			Message: "Carrier flagged this shipment as an exception.",
		})
	}
	if eta != nil && now.After(*eta) &&
		status != model.StatusDelivered && status != model.StatusCancelled {
		out = append(out, ruleOutcome{
			Kind: "delay", Severity: "warning",
			Title:   "SLA breach: past ETA",
			Message: fmt.Sprintf("Estimated arrival %s has passed without delivery.", eta.UTC().Format(time.RFC3339)),
		})
	}
	return out
}

// HandleEvaluateRules persists alerts raised by evaluateRules. The partial
// unique index alerts_open_uniq dedupes replayed webhooks.
func HandleEvaluateRules(pool *pgxpool.Pool, log *slog.Logger) func(context.Context, []byte) error {
	return func(ctx context.Context, body []byte) error {
		p, err := decodePayload(body)
		if err != nil {
			return err
		}
		return db.WithTenant(ctx, pool, p.TenantID, func(tx pgx.Tx) error {
			var eta *time.Time
			var status string
			if err := tx.QueryRow(ctx,
				`SELECT eta, status FROM shipments WHERE id=$1`, p.ShipmentID).
				Scan(&eta, &status); err != nil {
				return err
			}
			for _, o := range evaluateRules(status, p.Code, eta, time.Now()) {
				_, err := tx.Exec(ctx, `
					INSERT INTO alerts (tenant_id, shipment_id, kind, severity, title, message)
					VALUES ($1,$2,$3,$4,$5,$6)
					ON CONFLICT (shipment_id, kind) WHERE status='open' DO NOTHING`,
					p.TenantID, p.ShipmentID, o.Kind, o.Severity, o.Title, o.Message)
				if err != nil {
					return err
				}
			}
			// Broadcast alert changes (deduped inserts may add zero rows — the
			// client re-fetching is harmless).
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
