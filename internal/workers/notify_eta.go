package workers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tracksphere/tracksphere/internal/db"
)

// NotificationSender delivers a message. Production plugs in SES/Twilio here;
// the default logs and records to the notifications table (feature-flagged
// providers are a Growth/Enterprise concern — see docs/architecture.md).
type NotificationSender func(ctx context.Context, channel, recipient, subject, body string) error

// logSender is the built-in sender: it writes to the structured log. The
// notifications table is written regardless, giving a full audit trail.
func logSender(log *slog.Logger) NotificationSender {
	return func(_ context.Context, channel, recipient, subject, _ string) error {
		log.Info("notification",
			"channel", channel, "recipient", recipient, "subject", subject)
		return nil
	}
}

// HandleNotifyShipment sends the customer-facing notification for an event.
func HandleNotifyShipment(pool *pgxpool.Pool, log *slog.Logger) func(context.Context, []byte) error {
	return handleNotify(pool, logSender(log))
}

func handleNotify(pool *pgxpool.Pool, send NotificationSender) func(context.Context, []byte) error {
	return func(ctx context.Context, body []byte) error {
		p, err := decodePayload(body)
		if err != nil {
			return err
		}
		return db.WithTenant(ctx, pool, p.TenantID, func(tx pgx.Tx) error {
			// Resolve shipment + its primary contact (tenant owner).
			var (
				track, carrier, origin, dest, status string
				ownerEmail                           string
			)
			err := tx.QueryRow(ctx, `
				SELECT s.tracking_number, s.carrier, s.origin, s.destination, s.status,
				       u.email
				FROM shipments s
				LEFT JOIN users u ON u.tenant_id = s.tenant_id AND u.role = 'owner'
				WHERE s.id = $1
				ORDER BY u.created_at ASC NULLS LAST
				LIMIT 1`, p.ShipmentID).Scan(&track, &carrier, &origin, &dest, &status, &ownerEmail)
			if err == pgx.ErrNoRows {
				return nil // shipment deleted since enqueue — drop silently
			}
			if err != nil {
				return err
			}

			subject := fmt.Sprintf("TrackSphere: %s is %s", track, status)
			msgBody := fmt.Sprintf(
				"Shipment %s (%s) from %s to %s is now: %s.\nEvent code: %s",
				track, carrier, origin, dest, status, p.Code)

			channel, recipient := "email", ownerEmail
			if recipient == "" {
				channel, recipient = "system", "ops@localhost"
			}

			_, err = tx.Exec(ctx, `
				INSERT INTO notifications (tenant_id, shipment_id, channel, recipient, subject, body)
				VALUES ($1,$2,$3,$4,$5,$6)`,
				p.TenantID, p.ShipmentID, channel, recipient, subject, msgBody)
			if err != nil {
				return err
			}
			// Delivery happens inside the tenant tx for auditability; senders
			// must be idempotent-safe (log sender is).
			return send(ctx, channel, recipient, subject, msgBody)
		})
	}
}

// ── ETA recalculation ──────────────────────────────────────────────────

// baselineTransit is the seed table for mode-based ETA estimation, used when
// a carrier provides no explicit revised ETA (Phase-1 heuristic; the Premium
// ML model replaces EstimateETA later — interface stays identical).
var baselineTransit = map[string]time.Duration{
	"ocean": 21 * 24 * time.Hour,
	"air":   3 * 24 * time.Hour,
	"road":  5 * 24 * time.Hour,
	"rail":  10 * 24 * time.Hour,
}

// EstimateETA computes an ETA from creation time + mode when none exists.
func EstimateETA(mode string, createdAt time.Time) time.Time {
	d, ok := baselineTransit[mode]
	if !ok {
		d = baselineTransit["road"]
	}
	return createdAt.Add(d)
}

// HandleRecalculateETA applies a carrier-provided ETA or falls back to the
// heuristic, then notifies the dashboard.
func HandleRecalculateETA(pool *pgxpool.Pool, log *slog.Logger) func(context.Context, []byte) error {
	return func(ctx context.Context, body []byte) error {
		p, err := decodePayload(body)
		if err != nil {
			return err
		}
		return db.WithTenant(ctx, pool, p.TenantID, func(tx pgx.Tx) error {
			var (
				mode      string
				createdAt time.Time
				eta       *time.Time
			)
			if err := tx.QueryRow(ctx,
				`SELECT mode, created_at, eta FROM shipments WHERE id=$1`,
				p.ShipmentID).Scan(&mode, &createdAt, &eta); err != nil {
				return err
			}
			newETA := eta
			if newETA == nil {
				e := EstimateETA(mode, createdAt)
				newETA = &e
			}
			if _, err := tx.Exec(ctx,
				`UPDATE shipments SET eta=$2, updated_at=now() WHERE id=$1`,
				p.ShipmentID, newETA); err != nil {
				return err
			}
			notice, _ := json.Marshal(map[string]any{
				"type":       "shipment.updated",
				"tenant_id":  p.TenantID,
				"shipment_id": p.ShipmentID,
				"data":       map[string]any{"eta": newETA},
			})
			_, err := tx.Exec(ctx, `SELECT notify_tracksphere($1)`, string(notice))
			return err
		})
	}
}