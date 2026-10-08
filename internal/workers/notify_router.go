package workers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tracksphere/tracksphere/internal/db"
	"github.com/tracksphere/tracksphere/internal/metrics"
	"github.com/tracksphere/tracksphere/internal/notify"
)

// Notification routing with a hard interrupt budget.
//
// THIS IS THE MOST IMPORTANT PRODUCT CONSTRAINT IN THE CODEBASE.
//
// Every ingested event enqueued a notification, and every alert had severity
// only as a colour. A single port-congestion webhook touching 500 shipments
// therefore produced 500 alerts and 500 notifications. Alert fatigue is the
// documented number-one reason operations teams abandon visibility platforms,
// and the volume went UP as rules were added.
//
// The rule: an operator may never receive more than N actionable interruptions
// per hour. Everything else is channel-managed pull. Three mechanisms enforce it:
//
//	1. Severity → channel. info NEVER interrupts. It is a timeline entry. Only
//	   critical can request an interruption, and even then...
//	2. Per-recipient hourly ceiling, enforced against a ledger row count inside
//	   the transaction, not by policy.
//	3. Per-shipment cooldown, so five rules firing on one container produce one
//	   interruption, not five.
//
// Non-interrupting notifications are queued for a digest rather than dropped:
// suppressing noise must not mean losing information.

// AlertNotifyJob is the queue kind for ops notifications.
const AlertNotifyJob = "alert.notify"

// Prefs is a tenant's notification policy, with defaults for a tenant that has
// never configured anything.
type Prefs struct {
	Timezone          string
	QuietHoursStart   int
	QuietHoursEnd     int
	DigestHour        int
	InterruptCap      int
	ShipmentCooldownM int
}

func defaultPrefs() Prefs {
	return Prefs{
		Timezone: "UTC", QuietHoursStart: 22, QuietHoursEnd: 7,
		DigestHour: 8, InterruptCap: 5, ShipmentCooldownM: 360,
	}
}

func loadPrefs(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) Prefs {
	p := defaultPrefs()
	var tz string
	var qs, qe, dh, cap, cool int
	err := tx.QueryRow(ctx, `
		SELECT timezone, quiet_hours_start, quiet_hours_end, digest_hour,
		       interrupt_hourly_cap, per_shipment_cooldown_minutes
		FROM notification_prefs WHERE tenant_id=$1`, tenantID).
		Scan(&tz, &qs, &qe, &dh, &cap, &cool)
	if err != nil {
		// tenants.timezone is the fallback; defaults otherwise.
		_ = tx.QueryRow(ctx, `SELECT timezone FROM tenants WHERE id=$1`, tenantID).Scan(&tz)
		return p
	}
	p.Timezone, p.QuietHoursStart, p.QuietHoursEnd = tz, qs, qe
	p.DigestHour, p.InterruptCap, p.ShipmentCooldownM = dh, cap, cool
	return p
}

// InQuietHours reports whether t falls in the tenant's local quiet window.
// Handles windows that wrap midnight (22:00 → 07:00).
func (p Prefs) InQuietHours(loc *time.Location, t time.Time) bool {
	h := t.In(loc).Hour()
	start, end := p.QuietHoursStart, p.QuietHoursEnd
	if start == end {
		return false
	}
	if start < end {
		return h >= start && h < end
	}
	// Wraps midnight.
	return h >= start || h < end
}

// Route is the decision made for one notification.
type Route struct {
	// Interrupt sends now via a pushing channel.
	Interrupt bool
	// QueueDigest defers to the next digest.
	QueueDigest bool
	// Reason explains the routing, for the audit row and for tests.
	Reason string
}

// severityPolicy is the fixed severity → channel mapping. It is a constant
// rather than configuration because getting it wrong is how an ops team gets
// buried: allowing info to interrupt is never correct.
func severityPolicy(severity string) (mayInterrupt, tellsCustomer bool) {
	switch severity {
	case "critical":
		return true, true
	case "warning":
		return false, true
	default: // info
		return false, false
	}
}

// DecideRoute applies severity, quiet hours, the hourly ceiling and the
// per-shipment cooldown. Pure, so the whole policy is testable without a
// database or a clock dependency.
func DecideRoute(severity string, interruptRequested bool, p Prefs, loc *time.Location, now time.Time, interruptsThisHour int, lastShipmentInterrupt *time.Time) Route {
	mayInterrupt, _ := severityPolicy(severity)

	// Rule 1: info can never interrupt. Not a preference — a guarantee.
	if !mayInterrupt {
		return Route{Interrupt: false, QueueDigest: true, Reason: "severity_" + severity}
	}
	// Rule 2: only a critical alert can ask to interrupt. The rule's request
	// is necessary but never sufficient.
	if !interruptRequested {
		return Route{Interrupt: false, QueueDigest: true, Reason: "not_requested"}
	}
	// Rule 3: quiet hours. A 3am page about a container nobody is waiting on is
	// how a team stops reading pages.
	if p.InQuietHours(loc, now) {
		return Route{Interrupt: false, QueueDigest: true, Reason: "quiet_hours"}
	}
	// Rule 4: per-shipment cooldown. Five rules on one container is one problem.
	if lastShipmentInterrupt != nil &&
		now.Sub(*lastShipmentInterrupt) < time.Duration(p.ShipmentCooldownM)*time.Minute {
		return Route{Interrupt: false, QueueDigest: true, Reason: "shipment_cooldown"}
	}
	// Rule 5: the ceiling. When the budget is spent the most severe already
	// went out; the rest queue, and the operator is told they were protected.
	// A cap of zero means digest-only delivery, so the comparison is not
	// guarded by cap > 0.
	if interruptsThisHour >= p.InterruptCap {
		return Route{Interrupt: false, QueueDigest: true, Reason: "hourly_cap_reached"}
	}
	return Route{Interrupt: true, Reason: "interrupted"}
}

// recipientHash fingerprints a recipient without storing it in the ledger.
func recipientHash(recipient string) string {
	sum := sha256.Sum256([]byte(recipient))
	return hex.EncodeToString(sum[:8])
}

// HandleAlertNotify routes an ops notification for a newly raised exception.
func HandleAlertNotify(pool *pgxpool.Pool, log *slog.Logger) func(context.Context, []byte) error {
	return func(ctx context.Context, body []byte) error {
		var p struct {
			TenantID   string `json:"tenantId"`
			ShipmentID string `json:"shipmentId"`
			Severity   string `json:"severity"`
			Interrupt  bool   `json:"interrupt"`
			Kind       string `json:"kind"`
		}
		if err := decodeStrict(body, &p); err != nil {
			return nil // poison — drop rather than retry forever
		}
		tenantID, err := uuid.Parse(p.TenantID)
		if err != nil {
			return nil
		}
		shipmentID, err := uuid.Parse(p.ShipmentID)
		if err != nil {
			return nil
		}

		var shipped int
		err = db.WithTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
			prefs := loadPrefs(ctx, tx, tenantID)
			loc := resolveLocation(prefs.Timezone)
			now := time.Now()

			// Ops recipients: every admin and owner. Members are deliberately
			// excluded — they are read-mostly and should not be paged.
			rows, err := tx.Query(ctx, `
				SELECT id, email FROM users
				WHERE tenant_id=$1 AND role IN ('owner','admin') AND is_active`, tenantID)
			if err != nil {
				return err
			}
			type recipient struct {
				id    uuid.UUID
				email string
			}
			var targets []recipient
			for rows.Next() {
				var t recipient
				if err := rows.Scan(&t.id, &t.email); err != nil {
					rows.Close()
					return err
				}
				targets = append(targets, t)
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return err
			}

			var tracking, title, message, severity string
			if err := tx.QueryRow(ctx, `
				SELECT c.tracking_number, a.title, a.message, a.severity
				FROM alerts a JOIN shipment_current c ON c.shipment_id = a.shipment_id
				WHERE a.shipment_id=$1 AND a.status='open'
				ORDER BY CASE a.severity WHEN 'critical' THEN 0 WHEN 'warning' THEN 1 ELSE 2 END
				LIMIT 1`, shipmentID).
				Scan(&tracking, &title, &message, &severity); err != nil {
				if err == pgx.ErrNoRows {
					return nil // resolved before we got here
				}
				return err
			}

			subject := fmt.Sprintf("[%s] %s — %s", severity, tracking, title)
			bodyText := fmt.Sprintf("%s\n\nShipment: %s\n%s", message, tracking, dashboardURL(tenantID, shipmentID))

			for _, t := range targets {
				rhash := recipientHash(t.email)
				var thisHour int
				if err := tx.QueryRow(ctx, `
					SELECT count(*) FROM notification_interrupts
					WHERE tenant_id=$1 AND recipient_hash=$2
					  AND created_at > now() - interval '1 hour'`,
					tenantID, rhash).Scan(&thisHour); err != nil {
					return err
				}
				var lastShip *time.Time
				_ = tx.QueryRow(ctx, `
					SELECT max(created_at) FROM notification_interrupts
					WHERE tenant_id=$1 AND recipient_hash=$2 AND shipment_id=$3`,
					tenantID, rhash, shipmentID).Scan(&lastShip)

				route := DecideRoute(severity, p.Interrupt, prefs, loc, now, thisHour, lastShip)
				if route.Interrupt {
					if err := deliver(ctx, tx, log, tenantID, shipmentID, "email",
						t.email, rhash, subject, bodyText, severity, p.Kind, route.Reason); err != nil {
						return err
					}
					// Record the interrupt so the ledger enforces the ceiling
					// for the next sender in this same sweep pass.
					if _, err := tx.Exec(ctx, `
					INSERT INTO notification_interrupts
						(tenant_id, recipient_hash, shipment_id, alert_id, severity)
					VALUES ($1,$2,$3,NULL,$4)`, tenantID, rhash, shipmentID, severity); err != nil {
						return err
					}
					metrics.InterruptsSentTotal.WithLabelValues(tenantID.String(), severity).Inc()
					shipped++
				} else {
					if err := queueDigest(ctx, tx, tenantID, rhash, shipmentID,
						severity, subject, bodyText, route.Reason); err != nil {
						return err
					}
					metrics.InterruptsQueuedTotal.WithLabelValues(tenantID.String(), route.Reason).Inc()
				}
			}

			// Customer-facing side: only critical and warning ever tell a
			// customer, and only subscribers who actually confirmed consent.
			_, queueCustomer := severityPolicy(severity)
			if queueCustomer {
				if err := notifySubscribers(ctx, tx, log, tenantID, shipmentID,
					tracking, severity, title, message); err != nil {
					log.Warn("customer notification fan-out", "err", err)
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		log.Info("alert notification routed", "tenant", tenantID,
			"severity", p.Severity, "interrupts", shipped)
		return nil
	}
}

func resolveLocation(tz string) *time.Location {
	if tz == "" {
		return time.UTC
	}
	if loc, err := time.LoadLocation(tz); err == nil {
		return loc
	}
	return time.UTC
}

func dashboardURL(tenantID, shipmentID uuid.UUID) string {
	if base := publicBaseURL(); base != "" {
		return fmt.Sprintf("%s/shipments/%s", base, shipmentID)
	}
	return fmt.Sprintf("/shipments/%s", shipmentID)
}

// deliver writes the audit row and sends through the provider.
func deliver(ctx context.Context, tx pgx.Tx, log *slog.Logger, tenantID, shipmentID uuid.UUID, channel, to, rhash, subject, body, severity, kind, reason string) error {
	// Metered-channel kill-switch (P2-4): fail closed so no Twilio spend and
	// no consent exposure happens without an explicit tenant opt-in. The
	// caller treats this like a send failure (warn + continue, no ledger row).
	if !meteredAllowed(ctx, tx, tenantID, channel) {
		metrics.NotificationsFailedTotal.WithLabelValues(channel, tenantID.String(), "metered_disabled").Inc()
		return fmt.Errorf("metered channel %q disabled for tenant", channel)
	}
	sender := notify.ForChannel(log, channel)
	if _, err := tx.Exec(ctx, `
		INSERT INTO notifications (tenant_id, shipment_id, channel, recipient, subject, body, provider)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		tenantID, shipmentID, channel, to, subject, body, sender.Name()); err != nil {
		return err
	}
	// Send AFTER the row so a provider failure leaves an auditable record
	// rather than a silent loss.
	metrics.NotificationsAttemptedTotal.WithLabelValues(channel, tenantID.String()).Inc()
	if err := sender.Send(ctx, channel, to, subject, body); err != nil {
		metrics.NotificationsFailedTotal.WithLabelValues(channel, tenantID.String(), "send_failed").Inc()
		return err
	}
	metrics.NotificationsSentTotal.WithLabelValues(channel, tenantID.String()).Inc()
	return nil
}

// queueDigest defers a non-interrupting notification into the digest queue.
// Suppressing noise must never mean losing information.
func queueDigest(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, rhash string, shipmentID uuid.UUID, severity, subject, body, reason string) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO notification_digest_queue
			(tenant_id, recipient_hash, shipment_id, severity, subject, body)
		VALUES ($1,$2,$3,$4,$5,$6)`,
		tenantID, rhash, shipmentID, severity, subject,
		body+"\n\n(routed to digest: "+reason+")")
	return err
}

// notifySubscribers sends to confirmed, active subscribers only.
//
// 'pending' rows exist purely as an un-consented intent record and are never
// deliverable — that is the whole point of the double opt-in. digest_only
// subscribers wait for the digest.
func notifySubscribers(ctx context.Context, tx pgx.Tx, log *slog.Logger, tenantID, shipmentID uuid.UUID, tracking, severity, title, message string) error {
	rows, err := tx.Query(ctx, `
		SELECT channel, recipient FROM tracking_subscriptions
		WHERE shipment_id=$1 AND status='active' AND NOT digest_only`, shipmentID)
	if err != nil {
		return err
	}
	type sub struct{ channel, to string }
	var subs []sub
	for rows.Next() {
		var v sub
		if err := rows.Scan(&v.channel, &v.to); err != nil {
			rows.Close()
			return err
		}
		subs = append(subs, v)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	subject := fmt.Sprintf("%s: %s is %s", tracking, tracking, severity)
	body := fmt.Sprintf("%s\n\n%s", title, message)
	sent := 0
	for _, v := range subs {
		if err := deliver(ctx, tx, log, tenantID, shipmentID, v.channel, v.to,
			recipientHash(v.to), subject, body, severity, "customer", "subscriber"); err != nil {
			// One failed recipient must not stop the rest, and must not fail
			// the job: the customer notification is best-effort by design.
			log.Warn("customer notification failed",
				"channel", v.channel, "err", err)
			continue
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO notification_consent
				(tenant_id, shipment_id, channel, recipient, recipient_hash, action)
			VALUES ($1,$2,$3,$4,$5,'delivered')`,
			tenantID, shipmentID, v.channel, v.to, recipientHash(v.to)); err != nil {
			return err
		}
		sent++
	}
	if sent > 0 {
		if _, err := tx.Exec(ctx,
			`UPDATE shipment_current SET customer_notified=true WHERE shipment_id=$1`, shipmentID); err != nil {
			return err
		}
	}
	return nil
}

// HandleFlushDigests rolls queued non-interrupting notifications into one
// grouped message per recipient. Grouping is by kind, not by event, so forty
// customs holds become one line instead of forty interruptions.
func HandleFlushDigests(pool *pgxpool.Pool, log *slog.Logger) func(context.Context, []byte) error {
	return func(ctx context.Context, _ []byte) error {
		sender := notify.ForChannel(log, "email")
		// notification_digest_queue is RLS-protected, so the batch discovery
		// query must run under the system flag. Without it the queue silently
		// reads empty and nothing is ever delivered.
		var batches []struct {
			tenant uuid.UUID
			rhash  string
			count  int
			kinds  int
		}
		tx, err := pool.Begin(ctx)
		if err != nil {
			return err
		}
		if err := db.SetSystem(ctx, tx); err != nil {
			_ = tx.Rollback(ctx)
			return err
		}
		rows, err := tx.Query(ctx, `
			SELECT tenant_id, recipient_hash, count(*), count(DISTINCT kind)
			FROM (
			  SELECT q.tenant_id, q.recipient_hash, q.created_at,
			         split_part(q.subject, ' ', 1) AS kind
			  FROM notification_digest_queue q
			  WHERE q.flushed_at IS NULL AND q.created_at < now() - interval '15 minutes'
			) t
			GROUP BY tenant_id, recipient_hash
			LIMIT 500`)
		if err != nil {
			_ = tx.Rollback(ctx)
			return err
		}
		for rows.Next() {
			var b struct {
				tenant uuid.UUID
				rhash  string
				count  int
				kinds  int
			}
			if err := rows.Scan(&b.tenant, &b.rhash, &b.count, &b.kinds); err != nil {
				rows.Close()
				_ = tx.Rollback(ctx)
				return err
			}
			batches = append(batches, b)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			_ = tx.Rollback(ctx)
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}

		for _, b := range batches {
			if err := db.WithTenant(ctx, pool, b.tenant, func(tx pgx.Tx) error {
				// Grouped, not enumerated: one digest that says how many, not
				// one message per event.
				subject := fmt.Sprintf("TrackSphere digest: %d updates across %d categories", b.count, b.kinds)
				queueURL := publicBaseURL() + "/exceptions"
				if publicBaseURL() == "" {
					queueURL = "/exceptions"
				}
				body := fmt.Sprintf(
					"You have %d open updates across %d exception categories.\n\n"+
						"Open the exception queue to triage them: %s",
					b.count, b.kinds, queueURL)
				if _, err := tx.Exec(ctx, `
					UPDATE notification_digest_queue SET flushed_at=now()
					WHERE tenant_id=$1 AND recipient_hash=$2 AND flushed_at IS NULL`,
					b.tenant, b.rhash); err != nil {
					return err
				}
				if err := sender.Send(ctx, "email", b.rhash, subject, body); err != nil {
					return err
				}
				_, _ = tx.Exec(ctx, `
					INSERT INTO notification_consent
						(tenant_id, channel, recipient, recipient_hash, action, reason)
					VALUES ($1,'email',$2,$3,'delivered','digest')`,
					b.tenant, b.rhash, b.rhash)
				metrics.DigestsFlushedTotal.WithLabelValues(b.tenant.String()).Inc()
				return nil
			}); err != nil {
				log.Warn("digest flush", "tenant", b.tenant, "err", err)
			}
		}
		if len(batches) > 0 {
			log.Info("digests flushed", "batches", len(batches))
		}
		return nil
	}
}

func decodeStrict(b []byte, v any) error {
	return json.Unmarshal(b, v)
}
