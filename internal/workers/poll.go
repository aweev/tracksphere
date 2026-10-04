package workers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tracksphere/tracksphere/internal/auth"
	"github.com/tracksphere/tracksphere/internal/carriers"
	"github.com/tracksphere/tracksphere/internal/db"
	"github.com/tracksphere/tracksphere/internal/model"
	"github.com/tracksphere/tracksphere/internal/queue"
	"github.com/tracksphere/tracksphere/internal/shipments"
)

// ShipmentPollJob is enqueued per due credential by the scheduler below.
const ShipmentPollJob = "shipment.poll"

// pollBatch caps how many tracking numbers one job will poll, so a single job
// can never outlive the queue's per-job context budget. The remainder is picked
// up by the next scheduled poll for the same credential.
const pollBatch = 200

// pollConcurrency bounds simultaneous outbound carrier HTTP calls. Carrier APIs
// are shared infrastructure; a tenant with thousands of shipments must not turn
// into a self-inflicted DDoS against Maersk's API.
const pollConcurrency = 4

// carrierCallTimeout bounds one Track() call. Slower than this, we log and skip
// rather than block the poller.
const carrierCallTimeout = 8 * time.Second

type pollPayload struct {
	CredentialID uuid.UUID `json:"credentialId"`
	TenantID     uuid.UUID `json:"tenantId"`
	Carrier      string    `json:"carrier"`
}

type pollTarget struct {
	Tracking string
	Since    *time.Time
}

// PollDueCredentials finds active credentials past their poll interval and
// enqueues one shipment.poll job each (transactional outbox). Runs with the
// system flag: the scheduler has no tenant. Call every minute from the worker.
func PollDueCredentials(ctx context.Context, pool *pgxpool.Pool) (int, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := db.SetSystem(ctx, tx); err != nil {
		return 0, err
	}
	rows, err := tx.Query(ctx, `
		SELECT id, tenant_id, carrier FROM carrier_credentials
		WHERE active = true
		  AND (last_polled_at IS NULL
		       OR last_polled_at < now() - (poll_interval_minutes || ' minutes')::interval)`)
	if err != nil {
		return 0, err
	}
	var jobs []pollPayload
	for rows.Next() {
		var p pollPayload
		if err := rows.Scan(&p.CredentialID, &p.TenantID, &p.Carrier); err != nil {
			rows.Close()
			return 0, err
		}
		jobs = append(jobs, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, j := range jobs {
		if err := queue.EnqueueTx(ctx, tx, ShipmentPollJob, j, time.Time{}); err != nil {
			return 0, err
		}
		// Stamp under the system flag: this is a scheduler-owned column and
		// the row's tenant_id is unchanged, so WITH CHECK still holds.
		if _, err := tx.Exec(ctx,
			`UPDATE carrier_credentials SET last_polled_at=now() WHERE id=$1`, j.CredentialID); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return len(jobs), nil
}

// HandlePoll polls one carrier credential.
//
// Transaction discipline (ADR 0007): the handler runs in THREE phases and holds
// NO transaction across network I/O.
//
//	phase 1  read credentials + targets inside db.WithTenant, collect to
//	         slices, COMMIT and return
//	phase 2  outbound carrier HTTP — no database connection is held, bounded
//	         concurrency, per-call timeout
//	phase 3  ingest, each event through IngestEvent (which owns its own
//	         transaction)
//
// Holding a pooled connection across HTTP is what previously made this unsafe:
// each concurrent job consumed two connections from a ten-connection pool, and
// the 5s statement_timeout aborted the whole transaction whenever a carrier
// responded slowly.
func HandlePoll(pool *pgxpool.Pool, log *slog.Logger, secretKeys [][]byte, newSvc func(*pgxpool.Pool) *shipments.Service) func(context.Context, []byte) error {
	return func(ctx context.Context, body []byte) error {
		var p pollPayload
		if err := json.Unmarshal(body, &p); err != nil {
			return nil // poison — drop
		}
		if p.TenantID == uuid.Nil || p.CredentialID == uuid.Nil {
			return nil
		}
		carrier := carriers.Get(p.Carrier)
		if carrier == nil {
			return fmt.Errorf("unknown carrier %q", p.Carrier)
		}

		// ── Phase 1: read under a tenant-pinned transaction ──────────────
		var (
			baseURL, sealed string
			active          bool
			targets         []pollTarget
		)
		err := db.WithTenant(ctx, pool, p.TenantID, func(tx pgx.Tx) error {
			if err := tx.QueryRow(ctx, `
				SELECT base_url, sealed_creds, active FROM carrier_credentials
				WHERE id=$1`, p.CredentialID).Scan(&baseURL, &sealed, &active); err != nil {
				// Credential deleted since enqueue — nothing to do.
				return nil
			}
			if !active {
				return nil
			}
			rows, err := tx.Query(ctx, `
				SELECT tracking_number,
				       (SELECT max(occurred_at) FROM shipment_events e
				         WHERE e.shipment_id = s.id)
				FROM shipments s
				WHERE carrier=$1 AND status NOT IN ('delivered','cancelled')
				ORDER BY updated_at ASC
				LIMIT $2`, p.Carrier, pollBatch)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var t pollTarget
				var since *time.Time
				if err := rows.Scan(&t.Tracking, &since); err != nil {
					return err
				}
				t.Since = since
				targets = append(targets, t)
			}
			return rows.Err()
		})
		if err != nil {
			return err
		}
		if len(targets) == 0 {
			return nil // no active shipments, or credential inactive
		}

var creds carriers.Credentials
	if sealed != "" {
		plain, err := auth.OpenMulti(secretKeys, sealed)
		if err != nil {
			return err
		}
		_ = json.Unmarshal(plain, &creds)
	}

		// ── Phase 2: network I/O with no database connection held ─────────
		type fetched struct {
			target  pollTarget
			events  []model.CarrierEvent
			skipped bool
		}
		results := make([]fetched, len(targets))
		sem := make(chan struct{}, pollConcurrency)
		var wg sync.WaitGroup
		for i, t := range targets {
			wg.Add(1)
			go func(i int, t pollTarget) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()

				callCtx, cancel := context.WithTimeout(ctx, carrierCallTimeout)
				defer cancel()
				var since time.Time
				if t.Since != nil {
					since = *t.Since
				}
				evs, err := carrier.Track(callCtx, baseURL, creds, t.Tracking, since)
				if err != nil {
					log.Warn("carrier poll failed", "carrier", p.Carrier,
						"tenant", p.TenantID, "tracking", t.Tracking, "err", err)
					results[i] = fetched{target: t, skipped: true}
					return
				}
				out := make([]model.CarrierEvent, 0, len(evs))
				for _, e := range evs {
					out = append(out, model.CarrierEvent{
						TrackingNumber: t.Tracking,
						EventID:        e.EventID,
						Code:           e.Code,
						Description:    e.Description,
						Location:       e.Location,
						Lat:            e.Lat,
						Lng:            e.Lng,
						OccurredAt:     e.OccurredAt,
						Status:         e.Status,
						ETA:            e.ETA,
					})
				}
				results[i] = fetched{target: t, events: out}
			}(i, t)
		}
		wg.Wait()

		// ── Phase 3: ingest (each event owns its transaction) ─────────────
		svc := newSvc(pool)
		ingested, failed := 0, 0
		for _, res := range results {
			if res.skipped {
				failed++
				continue
			}
			for _, ev := range res.events {
				e := ev
				if _, err := svc.IngestEvent(ctx, p.Carrier, &e, "poll"); err != nil {
					// A failure on one tracking number must not abort the rest
					// of the batch; log and continue.
					log.Warn("poll ingest failed", "carrier", p.Carrier,
						"tracking", res.target.Tracking, "err", err)
					failed++
					continue
				}
				ingested++
			}
		}
		log.Info("carrier poll complete", "carrier", p.Carrier, "tenant", p.TenantID,
			"targets", len(targets), "ingested", ingested, "failed", failed)
		return nil
	}
}