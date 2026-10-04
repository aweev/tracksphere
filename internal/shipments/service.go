package shipments

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tracksphere/tracksphere/internal/db"
	"github.com/tracksphere/tracksphere/internal/model"
	"github.com/tracksphere/tracksphere/internal/queue"
)

// Job kinds registered on the shared queue.
const (
	JobNotifyShipment = "shipment.notify"   // customer notification
	JobEvaluateRules  = "shipment.evaluate" // exception rules engine
	JobRecalculateETA = "shipment.eta"      // ETA revision
)

// Service exposes the write-side use cases. It shares the queue so jobs are
// enqueued transactionally with the data they reference.
type Service struct {
	pool  *pgxpool.Pool
	queue *queue.Queue
}

// NewService wires the service.
func NewService(pool *pgxpool.Pool, q *queue.Queue) *Service {
	return &Service{pool: pool, queue: q}
}

// IngestResult reports what a webhook did.
type IngestResult struct {
	ShipmentID uuid.UUID `json:"shipmentId"`
	Duplicate  bool      `json:"duplicate"`
	Status     string    `json:"status"`
}

// ErrAmbiguousTracking is returned when a carrier webhook matches more than
// one shipment (same carrier+tracking tracked by multiple tenants). The
// caller must resolve manually — mapped to HTTP 409.
var ErrAmbiguousTracking = fmt.Errorf("ambiguous tracking number for carrier")

// ErrUnknownTracking is returned when no shipment matches a carrier event.
// Mapped to HTTP 404 so carriers retry with backoff/alerting.
var ErrUnknownTracking = fmt.Errorf("unknown tracking number")

// IngestEvent applies one carrier event atomically:
//   1. resolve shipment by tracking number (any tenant — carriers don't know ours)
//   2. insert timeline event (idempotent on dedup_key)
//   3. advance shipment status / ETA
//   4. enqueue evaluation + notification jobs (same tx = outbox)
//   5. pg_notify('tracksphere', ...) so SSE clients update instantly
//
// source is 'webhook' for carrier deliveries, 'manual' for operator-entered
// scans. The tenant context is pinned midway so FORCE RLS applies to all writes.
func (s *Service) IngestEvent(ctx context.Context, carrier string, ev *model.CarrierEvent, source string) (*IngestResult, error) {
	if source != "manual" {
		source = "webhook"
	}
	dedupKey := fmt.Sprintf("%s:%s", carrier, ev.EventID)

	var result *IngestResult
	err := s.ingestTx(ctx, func(tx pgx.Tx) error {
		// Resolve the shipment BEFORE a tenant is known: carriers only send the
		// tracking number. app.system='on' bypasses the tenant/public read
		// branch for this lookup only; FOR UPDATE serializes concurrent webhooks.
		// Tracking numbers are UNIQUE per (tenant_id, tracking_number) and
		// unique per carrier in practice, so resolve carrier-scoped first.
		if err := db.SetSystem(ctx, tx); err != nil {
			return err
		}
		var (
			shipmentID uuid.UUID
			tenantID   uuid.UUID
			curStatus  string
		)
		err := tx.QueryRow(ctx, `
			SELECT id, tenant_id, status FROM shipments
			WHERE tracking_number=$1 AND carrier=$2 FOR UPDATE`,
			ev.TrackingNumber, carrier).Scan(&shipmentID, &tenantID, &curStatus)
		if err == pgx.ErrNoRows {
			// Back-compat fallback: rows written before carrier normalization
			// or with different casing. If several tenants share the number,
			// fail loudly so ops resolves it instead of delivering to the
			// wrong tenant.
			var matches int
			if countErr := tx.QueryRow(ctx, `SELECT count(*) FROM shipments WHERE tracking_number=$1`, ev.TrackingNumber).Scan(&matches); countErr != nil {
				return countErr
			} else if matches > 1 {
				return fmt.Errorf("%w: %q", ErrAmbiguousTracking, ev.TrackingNumber)
			}
			err = tx.QueryRow(ctx, `SELECT id, tenant_id, status FROM shipments WHERE tracking_number=$1 FOR UPDATE`, ev.TrackingNumber).Scan(&shipmentID, &tenantID, &curStatus)
		}
		if err == pgx.ErrNoRows {
			return fmt.Errorf("%w %q", ErrUnknownTracking, ev.TrackingNumber)
		}
		if err != nil {
			return err
		}

		// Pin tenant: all subsequent writes satisfy FORCE RLS, then drop the
		// system bypass to restore least privilege for the rest of the tx.
		if err := db.SetTenant(ctx, tx, tenantID); err != nil {
			return err
		}
		if err := db.ClearSystem(ctx, tx); err != nil {
			return err
		}

		// 1. Read the current state needed for the transition guard, BEFORE
		// inserting. shipment_events has no system branch in its RLS policy, so
		// the timeline aggregate is only visible to the pinned tenant — and it
		// must exclude the event we are about to write, otherwise the new row
		// would make every incoming event look stale to itself.
		var cur IngestState
		if err := tx.QueryRow(ctx, `
			SELECT s.status, s.delivered_at, s.eta,
			       (SELECT max(e.occurred_at) FROM shipment_events e
			         WHERE e.shipment_id = s.id)
			FROM shipments s WHERE s.id = $1`, shipmentID).
			Scan(&cur.Status, &cur.DeliveredAt, &cur.ETA, &cur.LastEventAt); err != nil {
			return err
		}

		// 2. Timeline event, idempotent.
		var eventID uuid.UUID
		err = tx.QueryRow(ctx, `
			INSERT INTO shipment_events
				(tenant_id, shipment_id, carrier, code, description, location,
				 lat, lng, occurred_at, source, dedup_key)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
			ON CONFLICT (tenant_id, dedup_key) DO NOTHING
			RETURNING id`,
			tenantID, shipmentID, carrier, ev.Code, ev.Description, ev.Location,
			ev.Lat, ev.Lng, ev.OccurredAt, source, dedupKey).Scan(&eventID)
		if err == pgx.ErrNoRows {
			result = &IngestResult{ShipmentID: shipmentID, Duplicate: true, Status: curStatus}
			return nil // duplicate delivery — already processed
		}
		if err != nil {
			return err
		}

		// 3. Advance shipment state under the transition guard: an out-of-order
		// carrier event is still recorded on the timeline, but must not rewind
		// status, delivered_at or ETA.
		newStatus, clearDelivered, stale := ApplyStatus(cur, ev.Status, ev.OccurredAt)
		applyETA := cur.ETA
		if ev.ETA != nil && ShouldApplyETA(cur, ev.OccurredAt) {
			applyETA = ev.ETA
		}
		_, err = tx.Exec(ctx, `
			UPDATE shipments SET
				status=$2,
				eta=coalesce($3, eta),
				shipped_at = CASE WHEN $2 IN ('in_transit','delivered')
					THEN coalesce(shipped_at, $4) ELSE shipped_at END,
				delivered_at = CASE
					WHEN $5 THEN NULL
					WHEN $2='delivered' AND $6 THEN $4
					ELSE delivered_at END,
				updated_at = CASE WHEN $7 THEN now() ELSE updated_at END
			WHERE id=$1`,
			shipmentID, newStatus, applyETA, ev.OccurredAt,
			clearDelivered, !stale, stale)
		if err != nil {
			return err
		}

		// 4. Outbox: same transaction as the data.
		jobPayload := map[string]any{
			"shipmentId": shipmentID.String(),
			"tenantId":   tenantID.String(),
			"eventId":    eventID.String(),
			"code":       ev.Code,
			"status":     newStatus,
			"stale":      stale,
		}
		if err := queue.EnqueueTx(ctx, tx, JobEvaluateRules, jobPayload, time.Time{}); err != nil {
			return err
		}
		if err := queue.EnqueueTx(ctx, tx, JobNotifyShipment, jobPayload, time.Time{}); err != nil {
			return err
		}
		if ev.ETA != nil {
			if err := queue.EnqueueTx(ctx, tx, JobRecalculateETA, jobPayload, time.Time{}); err != nil {
				return err
			}
		}

		// 3b. Fan-out to tenant outbound webhooks (same tx = outbox; the
		// dispatcher POSTs after commit with per-endpoint HMAC).
		if err := enqueueWebhookFanout(ctx, tx, tenantID, "shipment.updated", map[string]any{
			"shipmentId":     shipmentID.String(),
			"trackingNumber": ev.TrackingNumber,
			"status":         newStatus,
			"code":           ev.Code,
		}); err != nil {
			return err
		}

		// 4. Broadcast over LISTEN/NOTIFY (delivered only after commit).
		notice, _ := json.Marshal(map[string]any{
			"type":        "shipment.updated",
			"tenant_id":   tenantID,
			"shipment_id": shipmentID,
		})
		if _, err := tx.Exec(ctx, `SELECT notify_tracksphere($1)`, string(notice)); err != nil {
			return err
		}

		result = &IngestResult{ShipmentID: shipmentID, Status: newStatus}
		return nil
	})
	return result, err
}

// enqueueWebhookFanout enqueues one webhook.dispatch job per active tenant
// endpoint subscribed to eventType. Best-effort: endpoint errors never fail
// the ingestion transaction (the dispatcher retries/DLQs on its own).
func enqueueWebhookFanout(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, eventType string, data map[string]any) error {
	rows, err := tx.Query(ctx, `
		SELECT id, events FROM tenant_webhooks
		WHERE tenant_id=$1 AND active=true`, tenantID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		var events []string
		if err := rows.Scan(&id, &events); err != nil {
			return err
		}
		subscribed := false
		for _, e := range events {
			if e == eventType {
				subscribed = true
				break
			}
		}
		if !subscribed {
			continue
		}
		if err := queue.EnqueueTx(ctx, tx, "webhook.dispatch", map[string]any{
			"endpointId": id.String(),
			"eventType":  eventType,
			"payload":    data,
		}, time.Time{}); err != nil {
			return err
		}
	}
	return rows.Err()
}

// ingestTx runs fn in a transaction that initially has NO tenant context
// (needed for the cross-tenant tracking-number lookup above).
func (s *Service) ingestTx(ctx context.Context, fn func(pgx.Tx) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// NotifyCustomer sends a custom notification to the customer for a shipment.
// This is used for one-off customer updates from the exceptions triage panel.
func (s *Service) NotifyCustomer(ctx context.Context, tenantID, shipmentID uuid.UUID, title, message, customerMsg string) error {
	// Verify shipment exists and belongs to tenant
	var exists bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM shipments WHERE id=$1 AND tenant_id=$2)`,
		shipmentID, tenantID).Scan(&exists)
	if err != nil || !exists {
		return fmt.Errorf("shipment not found")
	}
	
	// Queue a customer notification job
	jobPayload := map[string]any{
		"shipmentId":   shipmentID.String(),
		"tenantId":     tenantID.String(),
		"title":        title,
		"message":      message,
		"customerMsg":  customerMsg,
		"isCustom":     true,
	}
	return s.queue.Enqueue(ctx, "shipment.notify_customer", jobPayload, time.Time{})
}

// EmailCarrier queues an email to the carrier for a shipment.
// This is used for one-click carrier communication from the exceptions triage panel.
func (s *Service) EmailCarrier(ctx context.Context, tenantID, shipmentID uuid.UUID, title, message, note string) error {
	var exists bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM shipments WHERE id=$1 AND tenant_id=$2)`,
		shipmentID, tenantID).Scan(&exists)
	if err != nil || !exists {
		return fmt.Errorf("shipment not found")
	}
	
	jobPayload := map[string]any{
		"shipmentId":   shipmentID.String(),
		"tenantId":     tenantID.String(),
		"title":        title,
		"message":      message,
		"note":         note,
	}
	return s.queue.Enqueue(ctx, "shipment.email_carrier", jobPayload, time.Time{})
}
