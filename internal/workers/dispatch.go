package workers

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5"

	"github.com/tracksphere/tracksphere/internal/db"
	"github.com/tracksphere/tracksphere/internal/httpclient"
)

// WebhookDispatchJob is the queue payload for outbound customer deliveries.
const WebhookDispatchJob = "webhook.dispatch"

type dispatchPayload struct {
	EndpointID uuid.UUID     `json:"endpointId"`
	EventType  string        `json:"eventType"`
	Payload    map[string]any `json:"payload"`
}

// HandleWebhookDispatch POSTs event payloads to tenant endpoints, HMAC-signed
// with the per-endpoint secret. Non-2xx and transport errors return error so
// the queue retries with backoff, then DLQs (visible in balance).
//
// The endpoint lookup happens under the system flag: a dispatch job is a
// background artifact that carries only an endpoint id, and its tenant is not
// known until the row is read. tenant_webhooks is RLS-protected (000015), so
// without this the read returned nothing and every outbound webhook silently
// stopped being delivered.
func HandleWebhookDispatch(pool *pgxpool.Pool, log *slog.Logger) func(context.Context, []byte) error {
	client := httpclient.NewClient(httpclient.WebhookDispatchClient)
	return func(ctx context.Context, body []byte) error {
		var p dispatchPayload
		if err := json.Unmarshal(body, &p); err != nil {
			return nil // poison payload — drop, don't DLQ-loop
		}
		var (
			url, secret string
			active      bool
			tenantID    uuid.UUID
		)
		readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		tx, err := pool.Begin(readCtx)
		if err != nil {
			return err
		}
		if err := db.SetSystem(readCtx, tx); err != nil {
			_ = tx.Rollback(readCtx)
			return err
		}
		err = tx.QueryRow(readCtx, `
			SELECT url, secret, active, tenant_id FROM tenant_webhooks WHERE id=$1`,
			p.EndpointID).Scan(&url, &secret, &active, &tenantID)
		_ = tx.Commit(readCtx)
		if err != nil {
			return nil // endpoint deleted — drop silently
		}
		if !active {
			return nil
		}
		raw, _ := json.Marshal(map[string]any{
			"event": p.EventType, "data": p.Payload,
		})
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(raw)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-TrackSphere-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
		req.Header.Set("X-TrackSphere-Event", p.EventType)

		status := "delivered"
		var lastErr *string
		resp, err := client.Do(req)
		if err != nil {
			status = "failed"
			msg := err.Error()
			lastErr = &msg
		} else {
			defer resp.Body.Close()
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				status = "failed"
				msg := fmt.Sprintf("endpoint returned %d", resp.StatusCode)
				lastErr = &msg
				err = fmt.Errorf("%s", msg)
			}
		}
		// The delivery record is written inside a pinned transaction: both
		// endpoint and delivery tables are RLS-protected, and this insert has a
		// tenant_id so no system flag is needed.
		if writeErr := db.WithTenant(ctx, pool, tenantID, func(dtx pgx.Tx) error {
			_, e := dtx.Exec(ctx, `
				INSERT INTO webhook_deliveries (endpoint_id, tenant_id, event_type, payload, status, attempts, last_error)
				VALUES ($1,$2,$3,$4,$5,1,$6)`,
				p.EndpointID, tenantID, p.EventType, raw, status, lastErr)
			return e
		}); writeErr != nil {
			log.Warn("delivery record write failed", "endpoint", p.EndpointID, "err", writeErr)
		}
		if err != nil {
			log.Warn("outbound webhook failed", "endpoint", p.EndpointID, "err", err)
			return err
		}
		return nil
	}
}