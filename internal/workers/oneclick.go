package workers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/tracksphere/tracksphere/internal/db"
	"github.com/tracksphere/tracksphere/internal/notify"
	"github.com/tracksphere/tracksphere/internal/queue"
)

const (
	NotifyCustomerJob = "shipment.notify_customer"
	EmailCarrierJob   = "shipment.email_carrier"
)

// HandleNotifyCustomer sends a custom customer notification for a shipment.
func HandleNotifyCustomer(q *queue.Queue, log *slog.Logger) func(context.Context, []byte) error {
	return func(ctx context.Context, body []byte) error {
		var p struct {
			ShipmentID  uuid.UUID `json:"shipmentId"`
			TenantID    uuid.UUID `json:"tenantId"`
			Title       string    `json:"title"`
			Message     string    `json:"message"`
			CustomerMsg string    `json:"customerMsg"`
			IsCustom    bool      `json:"isCustom"`
		}
		if err := json.Unmarshal(body, &p); err != nil {
			return nil // poison payload — drop
		}

		// Get the shipment details
		var trackingNumber, carrier, mode, origin, destination, status string
		var eta *string
		err := q.Pool().QueryRow(ctx, `
			SELECT tracking_number, carrier, mode, origin, destination, status, eta
			FROM shipments WHERE id=$1`, p.ShipmentID).
			Scan(&trackingNumber, &carrier, &mode, &origin, &destination, &status, &eta)
		if err != nil {
			log.Warn("notify_customer: shipment not found", "shipment", p.ShipmentID, "err", err)
			return nil
		}

		// Get active subscriptions for this shipment
		rows, err := q.Pool().Query(ctx, `
			SELECT channel, recipient FROM tracking_subscriptions
			WHERE shipment_id=$1 AND status='active'`, p.ShipmentID)
		if err != nil {
			return err
		}
		defer rows.Close()

		var subs []struct {
			Channel   string
			Recipient string
		}
		for rows.Next() {
			var s struct{ Channel, Recipient string }
			if err := rows.Scan(&s.Channel, &s.Recipient); err != nil {
				continue
			}
			subs = append(subs, s)
		}

		if len(subs) == 0 {
			log.Info("notify_customer: no active subscriptions", "shipment", p.ShipmentID)
			return nil
		}

		// Use the notification provider for this tenant
		sender := notify.Default(log)

		for _, sub := range subs {
			subject := p.Title
			body := p.CustomerMsg
			if body == "" {
				body = fmt.Sprintf(
					"Update on %s (%s %s): %s — %s\n\n%s → %s\nStatus: %s%s",
					trackingNumber, carrier, mode, p.Title, p.Message,
					origin, destination, status,
					func() string {
						if eta != nil {
							return "\nETA: " + *eta
						}
						return ""
					}(),
				)
			}
			if err := sender.Send(ctx, sub.Channel, sub.Recipient, subject, body); err != nil {
				log.Warn("notify_customer: send failed", "channel", sub.Channel, "err", err)
				continue
			}
			// Record notification
			if err := db.WithTenant(ctx, q.Pool(), p.TenantID, func(tx pgx.Tx) error {
				_, e := tx.Exec(ctx, `
					INSERT INTO notifications (tenant_id, channel, recipient, subject, body, provider)
					VALUES ($1,$2,$3,$4,$5,$6)`,
					p.TenantID, sub.Channel, sub.Recipient, subject, body, sender.Name())
				return e
			}); err != nil {
				log.Warn("notify_customer: record failed", "err", err)
			}
		}

		log.Info("notify_customer: sent", "shipment", p.ShipmentID, "subscriptions", len(subs))
		return nil
	}
}

// HandleEmailCarrier sends an email to the carrier for a shipment.
func HandleEmailCarrier(q *queue.Queue, log *slog.Logger) func(context.Context, []byte) error {
	return func(ctx context.Context, body []byte) error {
		var p struct {
			ShipmentID uuid.UUID `json:"shipmentId"`
			TenantID   uuid.UUID `json:"tenantId"`
			Title      string    `json:"title"`
			Message    string    `json:"message"`
			Note       string    `json:"note"`
		}
		if err := json.Unmarshal(body, &p); err != nil {
			return nil // poison payload — drop
		}

		// Get the shipment details and carrier credentials
		var trackingNumber, carrier, mode, origin, destination, status string
		var carrierCredsJSON *string
		err := q.Pool().QueryRow(ctx, `
			SELECT s.tracking_number, s.carrier, s.mode, s.origin, s.destination, s.status,
			       cc.credentials
			FROM shipments s
			LEFT JOIN carrier_credentials cc ON cc.carrier = s.carrier AND cc.tenant_id = s.tenant_id
			WHERE s.id=$1`, p.ShipmentID).
			Scan(&trackingNumber, &carrier, &mode, &origin, &destination, &status, &carrierCredsJSON)
		if err != nil {
			log.Warn("email_carrier: shipment not found", "shipment", p.ShipmentID, "err", err)
			return nil
		}

		// Get carrier contact info from credentials
		carrierEmail := ""
		if carrierCredsJSON != nil {
			var creds struct {
				Email string `json:"email"`
			}
			_ = json.Unmarshal([]byte(*carrierCredsJSON), &creds)
			carrierEmail = creds.Email
		}

		// Fallback: use a generic carrier email if available
		if carrierEmail == "" {
			// Try to get from tenant config or use a known pattern
			carrierEmail = fmt.Sprintf("support@%s.com", carrier)
		}

		sender := notify.Default(log)

		subject := fmt.Sprintf("TrackSphere: %s - %s (%s)", p.Title, trackingNumber, carrier)
		emailBody := fmt.Sprintf(
			"Dear %s,\n\n"+
				"TrackSphere exception notification for shipment %s:\n\n"+
				"Tracking: %s\nCarrier: %s\nMode: %s\nRoute: %s → %s\nStatus: %s\n\n"+
				"Exception: %s\n%s\n\n"+
				"Operator note: %s\n\n"+
				"Please investigate and respond via TrackSphere or email.\n\n"+
				"TrackSphere\nhttps://track.tracksphere.io/shipments/%s",
			carrier, p.Title, trackingNumber, carrier, mode, origin, destination, status,
			p.Title, p.Message, p.Note, p.ShipmentID,
		)

		if err := sender.Send(ctx, "email", carrierEmail, subject, emailBody); err != nil {
			log.Warn("email_carrier: send failed", "carrier", carrier, "err", err)
			return err
		}

		// Record the outbound notification
		if err := db.WithTenant(ctx, q.Pool(), p.TenantID, func(tx pgx.Tx) error {
			_, e := tx.Exec(ctx, `
				INSERT INTO notifications (tenant_id, channel, recipient, subject, body, provider)
				VALUES ($1,'email',$2,$3,$4,$5)`,
				p.TenantID, carrierEmail, subject, emailBody, sender.Name())
			return e
		}); err != nil {
			log.Warn("email_carrier: record failed", "err", err)
		}

		log.Info("email_carrier: sent", "shipment", p.ShipmentID, "carrier", carrier, "to", carrierEmail)
		return nil
	}
}

// EnqueueNotifyCustomer queues a custom customer notification
func EnqueueNotifyCustomer(q *queue.Queue, ctx context.Context, tenantID, shipmentID uuid.UUID, title, message, customerMsg string) error {
	return q.Enqueue(ctx, NotifyCustomerJob, map[string]any{
		"shipmentId":  shipmentID.String(),
		"tenantId":    tenantID.String(),
		"title":       title,
		"message":     message,
		"customerMsg": customerMsg,
		"isCustom":    true,
	}, time.Time{})
}

// EnqueueEmailCarrier queues a carrier email
func EnqueueEmailCarrier(q *queue.Queue, ctx context.Context, tenantID, shipmentID uuid.UUID, title, message, note string) error {
	return q.Enqueue(ctx, EmailCarrierJob, map[string]any{
		"shipmentId": shipmentID.String(),
		"tenantId":   tenantID.String(),
		"title":      title,
		"message":    message,
		"note":       note,
	}, time.Time{})
}