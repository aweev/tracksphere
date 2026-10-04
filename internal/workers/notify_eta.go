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
	"github.com/tracksphere/tracksphere/internal/intel"
	"github.com/tracksphere/tracksphere/internal/notify"
)

// NotificationSender delivers a message. Kept as a seam for tests; the
// provider registry in internal/notify selects the real implementation
// (smtp/twilio/webhook/log) per channel.
type NotificationSender func(ctx context.Context, channel, recipient, subject, body string) error

// logSender is the test seam: it writes to the structured log. The
// notifications table is written regardless, giving a full audit trail.
func logSender(log *slog.Logger) NotificationSender {
	return func(_ context.Context, channel, recipient, subject, _ string) error {
		log.Info("notification",
			"channel", channel, "recipient", recipient, "subject", subject)
		return nil
	}
}

// HandleNotifyShipment sends the customer-facing notification for an event.
// The provider is chosen per channel from the environment (smtp for email,
// twilio for sms/whatsapp, else the configured default); the used provider
// is recorded on the audit row.
func HandleNotifyShipment(pool *pgxpool.Pool, log *slog.Logger) func(context.Context, []byte) error {
	return func(ctx context.Context, body []byte) error {
		p, err := decodePayload(body)
		if err != nil {
			return err
		}
		// Fan out to "notify me" subscribers as well as the tenant owner.
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

			recipients := []struct{ channel, to string }{}
			if ownerEmail != "" {
				recipients = append(recipients, struct{ channel, to string }{"email", ownerEmail})
			}
			// Only confirmed, non-digest subscribers may be pushed to.
			//
			// Without the status filter this query returned pending and revoked
			// rows too, so an anonymous caller who POSTed a phone number to the
			// public subscribe endpoint — and never confirmed it — received an
			// SMS on every subsequent carrier event. That is the exact
			// unsolicited-messaging hole the double opt-in in migration
			// 000016 exists to close, and it bypassed the consent ledger
			// entirely: nothing here checked notification_consent.
			//
			// digest_only is excluded too: it is the subscriber's explicit
			// request to be batched rather than pushed.
			subs, err := tx.Query(ctx, `
				SELECT channel, recipient FROM tracking_subscriptions
				WHERE shipment_id=$1
				  AND status='active'
				  AND NOT digest_only`, p.ShipmentID)
			if err != nil {
				return err
			}
			for subs.Next() {
				var ch, to string
				if err := subs.Scan(&ch, &to); err != nil {
					subs.Close()
					return err
				}
				recipients = append(recipients, struct{ channel, to string }{ch, to})
			}
			subs.Close()
			if err := subs.Err(); err != nil {
				return err
			}
			if len(recipients) == 0 {
				recipients = append(recipients, struct{ channel, to string }{"system", "ops@localhost"})
			}

			for _, rc := range recipients {
				sender := notify.ForChannel(log, rc.channel)
				_, err = tx.Exec(ctx, `
					INSERT INTO notifications (tenant_id, shipment_id, channel, recipient, subject, body, provider)
					VALUES ($1,$2,$3,$4,$5,$6,$7)`,
					p.TenantID, p.ShipmentID, rc.channel, rc.to, subject, msgBody, sender.Name())
				if err != nil {
					return err
				}
				// Delivery inside the tenant tx for auditability; providers
				// must be idempotent-safe (all built-ins are).
				if err := sender.Send(ctx, rc.channel, rc.to, subject, msgBody); err != nil {
					return err
				}
			}
			return nil
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
// lane-learned estimate (P3): same-lane p50 from your delivered shipments at
// up to 85% confidence, else the mode baseline. Carrier ETAs always win.
func HandleRecalculateETA(pool *pgxpool.Pool, log *slog.Logger) func(context.Context, []byte) error {
	return func(ctx context.Context, body []byte) error {
		p, err := decodePayload(body)
		if err != nil {
			return err
		}
		return db.WithTenant(ctx, pool, p.TenantID, func(tx pgx.Tx) error {
			var (
				mode, origin, dest, carrier string
				createdAt                   time.Time
				eta                         *time.Time
			)
			if err := tx.QueryRow(ctx,
				`SELECT mode, origin, destination, carrier, created_at, eta FROM shipments WHERE id=$1`,
				p.ShipmentID).Scan(&mode, &origin, &dest, &carrier, &createdAt, &eta); err != nil {
				return err
			}
			newETA := eta
			if newETA == nil {
				e, conf := intel.EstimateETA(mode, createdAt,
					intel.LaneStatsFor(ctx, tx, origin, dest, carrier, mode))
				newETA = &e
				log.Info("eta estimated", "shipment", p.ShipmentID,
					"confidence", conf, "lane", origin+"→"+dest)
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