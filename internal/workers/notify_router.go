package workers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
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
//
// Transaction discipline: the tenant transaction is DB-ONLY (budget ledger,
// digest queue, audit claims). Provider Sends happen AFTER commit via
// flushSends — no transaction ever spans network I/O. A retry re-claims:
// transmitted rows are skipped, untransmitted rows are re-owned.
func HandleAlertNotify(pool *pgxpool.Pool, log *slog.Logger) func(context.Context, []byte) error {
	return func(ctx context.Context, body []byte) error {
		var p struct {
			TenantID   string `json:"tenantId"`
			ShipmentID string `json:"shipmentId"`
			AlertID    string `json:"alertId"`
			AlertKey   string `json:"alertKey"`
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
		// alertId keys idempotency; jobs enqueued before it existed fall back
		// to the message text (same retry semantics, coarser grain).
		// alertKey (e.g. "…-esc" for an SLA-escalation page) overrides it so
		// a second notification about the same alert keeps its own dedupe
		// claim instead of collapsing into the raise-time one.
		alertID := strings.ReplaceAll(p.AlertID, "-", "")
		if p.AlertKey != "" {
			alertID = p.AlertKey
		}

		var sends []pendingSend
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
			// An escalation page must read differently from the raise-time
			// notification: same subject invites "seen that" dismissal.
			if p.Kind == "sla_escalation" {
				subject = fmt.Sprintf("[SLA breached] %s — %s", tracking, title)
				bodyText += "\n\nThis exception is past its response SLA and still open. Escalate with the carrier or re-plan with the customer."
			}

			for _, t := range targets {
				rhash := recipientHash(t.email)
				// Slice 5 — close the budget race. count-then-insert without
				// mutual exclusion lets N concurrent alert.notify workers
				// all read thisHour=4 and all interrupt: cap overrun with
				// no DB constraint to catch it. A transaction-scoped
				// advisory lock per (tenant, recipient) serializes the
				// check+insert across replicas; it auto-releases on
				// commit/rollback, so a crashed holder blocks nobody.
				// Namespace 787312097 = 'ts.notify' hashed once, offline.
				if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(787312097, hashtext($1))`,
					tenantID.String()+":"+rhash); err != nil {
					return err
				}
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
					// Metered-channel kill-switch (P2-4): fail closed. A
					// disabled channel skips THIS recipient and continues
					// with the rest — the old code aborted the whole batch
					// (return err), which poison-retried a permanent
					// condition forever.
					if !meteredAllowed(ctx, tx, tenantID, "email") {
						metrics.NotificationsFailedTotal.WithLabelValues("email", tenantID.String(), "metered_disabled").Inc()
						continue
					}
					sender := notify.ForChannel(log, "email")
					key := alertID
					if key == "" {
						key = sendKey("alert.notify", tenantID.String(), shipmentID.String(),
							t.email, p.Kind, subject, bodyText)
					} else {
						key = sendKey("alert.notify", tenantID.String(), key, t.email)
					}
					ps := pendingSend{
						TenantID: tenantID, ShipmentID: shipmentID,
						Channel: "email", To: t.email,
						Subject: subject, Body: bodyText,
						Provider: sender.Name(), DedupeKey: key,
					}
					owned, fresh, err := claimSendTx(ctx, tx, ps)
					if err != nil {
						return err
					}
					if owned {
						sends = append(sends, ps)
					}
					// The ledger enforces the ceiling for the next sender in
					// this same sweep pass — but only on fresh claims. A
					// retry re-owning an unsent row must not double-count.
					if fresh {
						if _, err := tx.Exec(ctx, `
						INSERT INTO notification_interrupts
							(tenant_id, recipient_hash, shipment_id, alert_id, severity)
						VALUES ($1,$2,$3,NULL,$4)`, tenantID, rhash, shipmentID, severity); err != nil {
							return err
						}
						metrics.InterruptsSentTotal.WithLabelValues(tenantID.String(), severity).Inc()
						shipped++
					}
				} else {
					if err := queueDigest(ctx, tx, tenantID, t.email, rhash, shipmentID,
						severity, p.Kind, subject, bodyText, route.Reason); err != nil {
						return err
					}
					metrics.InterruptsQueuedTotal.WithLabelValues(tenantID.String(), route.Reason).Inc()
				}
			}

			// Customer-facing side: only critical and warning ever tell a
			// customer, and only subscribers who actually confirmed consent.
			// Claims are collected here and transmitted after commit.
			_, queueCustomer := severityPolicy(severity)
			if queueCustomer {
				if err := notifySubscribers(ctx, tx, log, tenantID, shipmentID,
					tracking, severity, title, message, &sends); err != nil {
					log.Warn("customer notification fan-out", "err", err)
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		// Post-commit: transmit everything claimed. No transaction spans
		// network I/O anymore; retries re-claim (sent rows skipped).
		if err := flushSends(ctx, pool, log, sends); err != nil {
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

// queueDigest defers a non-interrupting notification into the digest queue.
// Suppressing noise must never mean losing information. The recipient
// address is stored alongside its hash: the hash drives the hourly ledger
// lookup, the address drives delivery (a hash is not deliverable).
func queueDigest(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, recipient, rhash string, shipmentID uuid.UUID, severity, kind, subject, body, reason string) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO notification_digest_queue
			(tenant_id, recipient_hash, recipient, shipment_id, severity, kind, subject, body)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		tenantID, rhash, recipient, shipmentID, severity, kind, subject,
		body+"\n\n(routed to digest: "+reason+")")
	return err
}

// notifySubscribers claims customer messages for confirmed, active
// subscribers only. Claims append to sends for post-commit transmission;
// the consent + customer_notified writes stay in-transaction (DB-only).
//
// 'pending' rows exist purely as an un-consented intent record and are never
// deliverable — that is the whole point of the double opt-in. digest_only
// subscribers wait for the digest.
func notifySubscribers(ctx context.Context, tx pgx.Tx, log *slog.Logger, tenantID, shipmentID uuid.UUID, tracking, severity, title, message string, sends *[]pendingSend) error {
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
	claimed := 0
	for _, v := range subs {
		// Best-effort per recipient: one bad claim must not stop the rest.
		sender := notify.ForChannel(log, v.channel)
		ps := pendingSend{
			TenantID: tenantID, ShipmentID: shipmentID,
			Channel: v.channel, To: v.to,
			Subject: subject, Body: body, Provider: sender.Name(),
			DedupeKey: sendKey("customer", tenantID.String(), shipmentID.String(),
				v.channel, v.to, subject, body),
		}
		owned, _, err := claimSendTx(ctx, tx, ps)
		if err != nil {
			log.Warn("customer notification claim failed",
				"channel", v.channel, "err", err)
			continue
		}
		if !owned {
			continue // already transmitted by an earlier attempt
		}
		*sends = append(*sends, ps)
		if _, err := tx.Exec(ctx, `
			INSERT INTO notification_consent
				(tenant_id, shipment_id, channel, recipient, recipient_hash, action)
			VALUES ($1,$2,$3,$4,$5,'delivered')`,
			tenantID, shipmentID, v.channel, v.to, recipientHash(v.to)); err != nil {
			return err
		}
		claimed++
	}
	if claimed > 0 {
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
			email  string
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
		// Only rows with a stored recipient are deliverable. Pre-migration
		// rows carry a hash alone (undeliverable by design) and are skipped
		// fail-closed — never sent to a hash.
		rows, err := tx.Query(ctx, `
			SELECT tenant_id, recipient_hash, min(recipient), count(*), count(DISTINCT kind)
			FROM notification_digest_queue
			WHERE flushed_at IS NULL
			  AND created_at < now() - interval '15 minutes'
			  AND recipient IS NOT NULL
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
				email  string
				count  int
				kinds  int
			}
			if err := rows.Scan(&b.tenant, &b.rhash, &b.email, &b.count, &b.kinds); err != nil {
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
			// Claim-then-send: stamp flushed_at FIRST so a concurrent flush
			// replica cannot claim the same rows (the UPDATE is the mutex),
			// transmit AFTER commit, and release the claim (flushed_at=NULL)
			// if transmission fails — so a failed digest stays pending for
			// the next flush instead of being marked delivered-but-lost.
			// Claimed row IDs travel with the batch: the release touches
			// ONLY rows this attempt claimed, never digests an earlier run
			// already delivered. No transaction spans network I/O.
			var subject, body string
			var claimedIDs []int64
			if err := db.WithTenant(ctx, pool, b.tenant, func(tx pgx.Tx) error {
				// Grouped, not enumerated: one digest that says how many, not
				// one message per event.
				subject = fmt.Sprintf("TrackSphere digest: %d updates across %d categories", b.count, b.kinds)
				queueURL := publicBaseURL() + "/exceptions"
				if publicBaseURL() == "" {
					queueURL = "/exceptions"
				}
				body = fmt.Sprintf(
					"You have %d open updates across %d exception categories.\n\n"+
						"Open the exception queue to triage them: %s",
					b.count, b.kinds, queueURL)
				rows, err := tx.Query(ctx, `
					SELECT id FROM notification_digest_queue
					WHERE tenant_id=$1 AND recipient_hash=$2 AND flushed_at IS NULL
					  AND recipient IS NOT NULL
					FOR UPDATE SKIP LOCKED`, b.tenant, b.rhash)
				if err != nil {
					return err
				}
				for rows.Next() {
					var id int64
					if err := rows.Scan(&id); err != nil {
						rows.Close()
						return err
					}
					claimedIDs = append(claimedIDs, id)
				}
				rows.Close()
				if err := rows.Err(); err != nil {
					return err
				}
				if len(claimedIDs) == 0 {
					return nil
				}
				_, err = tx.Exec(ctx, `
					UPDATE notification_digest_queue SET flushed_at=now()
					WHERE id = ANY($1)`, claimedIDs)
				return err
			}); err != nil {
				log.Warn("digest flush claim", "tenant", b.tenant, "err", err)
				continue
			}
			if len(claimedIDs) == 0 {
				continue // lost the race to a concurrent flush replica
			}
			if err := sender.Send(ctx, "email", b.email, subject, body); err != nil {
				log.Warn("digest flush send failed, claim released", "tenant", b.tenant, "err", err)
				_ = db.WithTenant(ctx, pool, b.tenant, func(tx pgx.Tx) error {
					_, err := tx.Exec(ctx, `
						UPDATE notification_digest_queue SET flushed_at=NULL
						WHERE id = ANY($1)`, claimedIDs)
					return err
				})
				continue
			}
			_ = db.WithTenant(ctx, pool, b.tenant, func(tx pgx.Tx) error {
				_, _ = tx.Exec(ctx, `
					INSERT INTO notification_consent
						(tenant_id, channel, recipient, recipient_hash, action, reason)
					VALUES ($1,'email',$2,$3,'delivered','digest')`,
					b.tenant, b.email, b.rhash)
				return nil
			})
			metrics.DigestsFlushedTotal.WithLabelValues(b.tenant.String()).Inc()
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
