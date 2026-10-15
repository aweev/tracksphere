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

	"github.com/tracksphere/tracksphere/internal/db"
	"github.com/tracksphere/tracksphere/internal/notify"
	"github.com/tracksphere/tracksphere/internal/queue"
)

const (
	NotifyCustomerJob = "shipment.notify_customer"
	EmailCarrierJob   = "shipment.email_carrier"
)

// getTenantBranding fetches the tenant's branding configuration.
// tx must already be tenant-pinned (callers run under WithTenant): branding
// reads fail closed without it, same as every other tenant table.
func getTenantBranding(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) notify.BrandConfig {
	var brand notify.BrandConfig
	err := tx.QueryRow(ctx, `
		SELECT company_name, primary_color, logo_url, support_email, custom_domain
		FROM tenant_branding WHERE tenant_id=$1`, tenantID).
		Scan(&brand.CompanyName, &brand.PrimaryColor, &brand.LogoURL, &brand.SupportEmail, &brand.CustomDomain)
	if err != nil {
		// Return defaults
		brand.PrimaryColor = "#ff6b00"
	}
	return brand
}

// HandleNotifyCustomer sends a custom customer notification for a shipment.
//
// Transaction discipline mirrors the alert dispatcher: the tenant
// transaction is DB-ONLY (claims + consent), provider Sends happen AFTER
// commit via flushSends. The job nonce keys idempotency — a retry replays
// the same nonce and re-claims instead of duplicating; two distinct
// operator clicks carry distinct nonces and both deliver.
func HandleNotifyCustomer(q *queue.Queue, log *slog.Logger) func(context.Context, []byte) error {
	return func(ctx context.Context, body []byte) error {
		var p struct {
			ShipmentID  uuid.UUID `json:"shipmentId"`
			TenantID    uuid.UUID `json:"tenantId"`
			Title       string    `json:"title"`
			Message     string    `json:"message"`
			CustomerMsg string    `json:"customerMsg"`
			IsCustom    bool      `json:"isCustom"`
			Nonce       string    `json:"nonce"`
		}
		if err := json.Unmarshal(body, &p); err != nil {
			return nil // poison payload — drop
		}
		if p.Nonce == "" {
			// Jobs enqueued before nonces existed: the message text is the
			// key. Same retry semantics, coarser grain.
			p.Nonce = sendKey("notify.customer.legacy", p.TenantID.String(),
				p.ShipmentID.String(), p.Title, p.Message, p.CustomerMsg)
		}

		// All reads run tenant-pinned: shipments, subscriptions and branding
		// are FORCE RLS and fail closed (zero rows) without the pin, which
		// used to make one-click sends silently no-op.
		var sends []pendingSend
		err := db.WithTenant(ctx, q.Pool(), p.TenantID, func(tx pgx.Tx) error {
			// Get the shipment details
			var trackingNumber, carrier, mode, origin, destination, status string
			var eta *string
			err := tx.QueryRow(ctx, `
				SELECT tracking_number, carrier, mode, origin, destination, status, eta
				FROM shipments WHERE id=$1`, p.ShipmentID).
				Scan(&trackingNumber, &carrier, &mode, &origin, &destination, &status, &eta)
			if err != nil {
				log.Warn("notify_customer: shipment not found", "shipment", p.ShipmentID, "err", err)
				return nil
			}

			// Get tenant branding for template rendering
			brand := getTenantBranding(ctx, tx, p.TenantID)
			templateEngine := notify.NewTemplateEngine(brand)

		// Get active subscriptions for this shipment. digest_only subscribers
		// wait for the digest by promise — a one-click action is still an
		// explicit human interrupt (ADR 0009 bypass), but it must not
		// surprise a subscriber who asked never to be interrupted. The
		// explicit per-recipient one-click path (HandleEmailCarrier) is the
		// bypass; broadcast fan-out respects digest_only.
		rows, err := tx.Query(ctx, `
			SELECT channel, recipient FROM tracking_subscriptions
			WHERE shipment_id=$1 AND status='active' AND NOT digest_only`, p.ShipmentID)
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
			rows.Close()

			if len(subs) == 0 {
				log.Info("notify_customer: no active subscriptions", "shipment", p.ShipmentID)
				return nil
			}

			// Use the notification provider for this tenant
			sender := notify.Default(log)

			for _, sub := range subs {
				// Metered-channel kill-switch (P2-4): skip without opt-in.
				if !meteredAllowed(ctx, tx, p.TenantID, sub.Channel) {
					log.Warn("notify_customer: metered channel disabled, skipped",
						"channel", sub.Channel, "shipment", p.ShipmentID)
					continue
				}
				// end metered-channel skip
				subject := p.Title
				msgBody := p.CustomerMsg
				if msgBody == "" {
					msgBody = fmt.Sprintf(
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

				// Render branded message based on channel
				var renderedBody string
				switch sub.Channel {
				case "email":
					renderedBody = templateEngine.RenderEmail(subject, msgBody, trackingNumber, carrier, mode, origin, destination, status, eta)
				case "sms":
					renderedBody = templateEngine.RenderSMS(subject, msgBody, trackingNumber, carrier, status)
				case "whatsapp":
					renderedBody = templateEngine.RenderWhatsApp(subject, msgBody, trackingNumber, carrier, origin, destination, status, eta)
				default:
					renderedBody = msgBody
				}

				// Claim in-transaction, transmit after commit. The nonce makes
				// each operator click distinct and each retry identical.
				ps := pendingSend{
					TenantID: p.TenantID, ShipmentID: p.ShipmentID,
					Channel: sub.Channel, To: sub.Recipient,
					Subject: subject, Body: renderedBody,
					Provider: sender.Name(),
					DedupeKey: sendKey("notify.customer", p.Nonce, sub.Channel, sub.Recipient),
				}
				owned, _, err := claimSendTx(ctx, tx, ps)
				if err != nil {
					log.Warn("notify_customer: claim failed", "channel", sub.Channel, "err", err)
					continue
				}
				if owned {
					sends = append(sends, ps)
				}
			}

			log.Info("notify_customer: claimed", "shipment", p.ShipmentID, "subscriptions", len(sends))
			return nil
		})
		if err != nil {
			return err
		}
		return flushSends(ctx, q.Pool(), log, sends)
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
			To         string    `json:"to"`
			Nonce      string    `json:"nonce"`
		}
		if err := json.Unmarshal(body, &p); err != nil {
			return nil // poison payload — drop
		}
		if strings.TrimSpace(p.To) == "" {
			log.Warn("email_carrier: missing recipient, dropping", "shipment", p.ShipmentID)
			return nil // malformed payload — retrying cannot produce an address
		}
		if p.Nonce == "" {
			p.Nonce = sendKey("email.carrier.legacy", p.TenantID.String(),
				p.ShipmentID.String(), p.Title, p.Message, p.Note, p.To)
		}

		// Tenant-pinned like HandleNotifyCustomer: shipments is FORCE RLS
		// and reads zero rows without the pin. Claim in-tx, Send after
		// commit: the old code returned the Send error from inside the tx,
		// so a delivered email with a failed commit retried into a duplicate.
		var sends []pendingSend
		err := db.WithTenant(ctx, q.Pool(), p.TenantID, func(tx pgx.Tx) error {
			// Get the shipment details. pgx.ErrNoRows means the shipment is
			// gone (nothing to do); any other error propagates so the job
			// retries with the REAL error instead of a misleading log line
			// followed by a poisoned-commit failure.
			var trackingNumber, carrier, mode, origin, destination, status string
			err := tx.QueryRow(ctx, `
				SELECT tracking_number, carrier, mode, origin, destination, status
				FROM shipments WHERE id=$1`, p.ShipmentID).
				Scan(&trackingNumber, &carrier, &mode, &origin, &destination, &status)
			if err == pgx.ErrNoRows {
				log.Warn("email_carrier: shipment gone, dropping", "shipment", p.ShipmentID)
				return nil
			}
			if err != nil {
				return err
			}

			// Recipient is operator-typed (request-validated): sealed_creds
			// holds API keys, not contacts, and fabricated support@ addresses
			// would send real mail to guessed inboxes.
			carrierEmail := strings.TrimSpace(p.To)

			sender := notify.Default(log)

			// Get tenant branding for template rendering
			brand := getTenantBranding(ctx, tx, p.TenantID)
			templateEngine := notify.NewTemplateEngine(brand)

			subject := fmt.Sprintf("TrackSphere: %s - %s (%s)", p.Title, trackingNumber, carrier)
			emailBody := templateEngine.RenderEmail(
				p.Title,
				fmt.Sprintf(
					"TrackSphere exception notification for shipment %s:\n\n"+
						"Tracking: %s\nCarrier: %s\nMode: %s\nRoute: %s → %s\nStatus: %s\n\n"+
						"Exception: %s\n%s\n\n"+
						"Operator note: %s\n\n",
					p.Title, trackingNumber, carrier, mode, origin, destination, status,
					p.Title, p.Message, p.Note,
				),
				trackingNumber, carrier, mode, origin, destination, status, nil,
			)

			ps := pendingSend{
				TenantID: p.TenantID, ShipmentID: p.ShipmentID,
				Channel: "email", To: carrierEmail,
				Subject: subject, Body: emailBody,
				Provider: sender.Name(),
				DedupeKey: sendKey("email.carrier", p.Nonce, carrierEmail),
			}
			owned, _, err := claimSendTx(ctx, tx, ps)
			if err != nil {
				log.Warn("email_carrier: claim failed", "err", err)
				return err
			}
			if owned {
				sends = append(sends, ps)
			}

			log.Info("email_carrier: claimed", "shipment", p.ShipmentID, "carrier", carrier, "to", carrierEmail)
			return nil
		})
		if err != nil {
			return err
		}
		return flushSends(ctx, q.Pool(), log, sends)
	}
}

// EnqueueNotifyCustomer queues a custom customer notification. Each call
// mints a nonce so the worker's idempotency key distinguishes two operator
// clicks (both deliver) from two attempts of one job (one delivers).
func EnqueueNotifyCustomer(q *queue.Queue, ctx context.Context, tenantID, shipmentID uuid.UUID, title, message, customerMsg string) error {
	return q.EnqueueTenant(ctx, tenantID, NotifyCustomerJob, map[string]any{
		"shipmentId":  shipmentID.String(),
		"tenantId":    tenantID.String(),
		"title":       title,
		"message":     message,
		"customerMsg": customerMsg,
		"isCustom":    true,
		"nonce":       uuid.NewString(),
	}, time.Time{})
}

// EnqueueEmailCarrier queues a carrier email (nonce: see above).
func EnqueueEmailCarrier(q *queue.Queue, ctx context.Context, tenantID, shipmentID uuid.UUID, title, message, note, to string) error {
	return q.EnqueueTenant(ctx, tenantID, EmailCarrierJob, map[string]any{
		"shipmentId": shipmentID.String(),
		"tenantId":   tenantID.String(),
		"title":      title,
		"message":    message,
		"note":       note,
		"to":         to,
		"nonce":      uuid.NewString(),
	}, time.Time{})
}