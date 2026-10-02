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
	JobNotifyShipment  = "shipment.notify"   // customer notification
	JobEvaluateRules   = "shipment.evaluate" // exception rules engine
	JobRecalculateETA  = "shipment.eta"      // ETA revision
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

// IngestEvent applies one carrier event atomically:
//   1. resolve shipment by tracking number (any tenant — carriers don't know ours)
//   2. insert timeline event (idempotent on dedup_key)
//   3. advance shipment status / ETA
//   4. enqueue evaluation + notification jobs (same tx = outbox)
//   5. pg_notify('tracksphere', ...) so SSE clients update instantly
//
// The tenant context is pinned midway so FORCE RLS applies to all writes.
func (s *Service) IngestEvent(ctx context.Context, carrier string, ev *model.CarrierEvent) (*IngestResult, error) {
	dedupKey := fmt.Sprintf("%s:%s", carrier, ev.EventID)

	var result *IngestResult
	err := s.ingestTx(ctx, func(tx pgx.Tx) error {
		// Resolve the shipment BEFORE a tenant is known: carriers only send the
		// tracking number. app.system='on' bypasses the tenant/public read
		// branch for this lookup only; FOR UPDATE serializes concurrent webhooks.
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
			WHERE tracking_number=$1 FOR UPDATE`,
			ev.TrackingNumber).Scan(&shipmentID, &tenantID, &curStatus)
		if err == pgx.ErrNoRows {
			return fmt.Errorf("no shipment for tracking number %q", ev.TrackingNumber)
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

		// 1. Timeline event, idempotent.
		var eventID uuid.UUID
		err = tx.QueryRow(ctx, `
			INSERT INTO shipment_events
				(tenant_id, shipment_id, carrier, code, description, location,
				 lat, lng, occurred_at, source, dedup_key)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,'webhook',$10)
			ON CONFLICT (tenant_id, dedup_key) DO NOTHING
			RETURNING id`,
			tenantID, shipmentID, carrier, ev.Code, ev.Description, ev.Location,
			ev.Lat, ev.Lng, ev.OccurredAt, dedupKey).Scan(&eventID)
		if err == pgx.ErrNoRows {
			result = &IngestResult{ShipmentID: shipmentID, Duplicate: true, Status: curStatus}
			return nil // duplicate delivery — already processed
		}
		if err != nil {
			return err
		}

		// 2. Advance shipment state.
		newStatus := curStatus
		if ev.Status != "" && model.ValidStatus(ev.Status) {
			newStatus = ev.Status
		}
		_, err = tx.Exec(ctx, `
			UPDATE shipments SET
				status=$2,
				eta=coalesce($3, eta),
				shipped_at = CASE WHEN $2 IN ('in_transit','delivered')
					THEN coalesce(shipped_at, $4) ELSE shipped_at END,
				delivered_at = CASE WHEN $2='delivered' THEN $4 ELSE delivered_at END,
				updated_at=now()
			WHERE id=$1`,
			shipmentID, newStatus, ev.ETA, ev.OccurredAt)
		if err != nil {
			return err
		}

		// 3. Outbox: same transaction as the data.
		jobPayload := map[string]any{
			"shipmentId": shipmentID.String(),
			"tenantId":   tenantID.String(),
			"eventId":    eventID.String(),
			"code":       ev.Code,
			"status":     newStatus,
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

		// 4. Broadcast over LISTEN/NOTIFY (delivered only after commit).
		notice, _ := json.Marshal(map[string]any{
			"type":       "shipment.updated",
			"tenant_id":  tenantID,
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