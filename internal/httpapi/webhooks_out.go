package httpapi

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/tracksphere/tracksphere/internal/db"
)

// Outbound webhooks: tenant endpoints receiving shipment.updated /
// alert.changed, HMAC-signed with their per-endpoint secret.
//
// RLS: tenant_webhooks and webhook_deliveries became RLS-protected in migration
// 000015. Every query in this file therefore runs inside db.WithTenant. They
// previously used the pool directly with an explicit `WHERE tenant_id=$1`, which
// reads as correct and fails closed the moment a policy exists: the list came
// back empty, creates were rejected by WITH CHECK, and the delivery log was
// invisible. That is exactly the class of bug a database guarantee is supposed to
// catch — it did, and the handler had to be fixed to match.

var outboundEvents = map[string]bool{"shipment.updated": true, "alert.changed": true}

func newEndpointSecret() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return uuid.NewString()
	}
	return hex.EncodeToString(b)
}

type createEndpointRequest struct {
	URL    string   `json:"url"`
	Events []string `json:"events"`
}

// handleCreateWebhookEndpoint POST /api/v1/webhooks/out
func (s *Server) handleCreateWebhookEndpoint(w http.ResponseWriter, r *http.Request) {
	var req createEndpointRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	url := strings.TrimSpace(req.URL)
	if !strings.HasPrefix(url, "https://") {
		writeError(w, http.StatusBadRequest, "invalid_input", "url must be https://")
		return
	}
	events := []string{"shipment.updated", "alert.changed"}
	if len(req.Events) > 0 {
		events = nil
		for _, e := range req.Events {
			if !outboundEvents[e] {
				writeError(w, http.StatusBadRequest, "invalid_input", "unknown event "+e)
				return
			}
			events = append(events, e)
		}
	}
	user := currentUser(r)
	secret := newEndpointSecret()
	var id uuid.UUID
	err := db.WithTenant(r.Context(), s.pool, user.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(r.Context(), `
			INSERT INTO tenant_webhooks (tenant_id, url, secret, events, created_by)
			VALUES ($1,$2,$3,$4,$5) RETURNING id`,
			user.TenantID, url, secret, events, user.ID).Scan(&id)
	})
	if err != nil {
		s.domainError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"id": id, "url": url, "events": events, "secret": secret,
		"warning": "Copy the secret now — it is never shown again",
	})
}

type endpointView struct {
	ID     uuid.UUID `json:"id"`
	URL    string    `json:"url"`
	Events []string  `json:"events"`
	Active bool      `json:"active"`
}

// handleListWebhookEndpoints GET /api/v1/webhooks/out (secrets never listed)
func (s *Server) handleListWebhookEndpoints(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	out := []endpointView{}
	err := db.WithTenant(r.Context(), s.pool, user.TenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `
			SELECT id, url, events, active FROM tenant_webhooks
			ORDER BY created_at DESC LIMIT 100`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var v endpointView
			if err := rows.Scan(&v.ID, &v.URL, &v.Events, &v.Active); err != nil {
				return err
			}
			out = append(out, v)
		}
		return rows.Err()
	})
	if err != nil {
		s.domainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleDeleteWebhookEndpoint DELETE /api/v1/webhooks/out/{id}
func (s *Server) handleDeleteWebhookEndpoint(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(chiParam(r, "id"))
	if !ok {
		writeError(w, http.StatusBadRequest, "bad_id", "Invalid endpoint id")
		return
	}
	user := currentUser(r)
	var deleted bool
	err := db.WithTenant(r.Context(), s.pool, user.TenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(r.Context(),
			`DELETE FROM tenant_webhooks WHERE id=$1`, id)
		if err != nil {
			return err
		}
		deleted = tag.RowsAffected() > 0
		return nil
	})
	if err != nil {
		s.domainError(w, err)
		return
	}
	if !deleted {
		writeError(w, http.StatusNotFound, "not_found", "Endpoint not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleListWebhookDeliveries GET /api/v1/webhooks/out/{id}/deliveries
func (s *Server) handleListWebhookDeliveries(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(chiParam(r, "id"))
	if !ok {
		writeError(w, http.StatusBadRequest, "bad_id", "Invalid endpoint id")
		return
	}
	user := currentUser(r)
	type view struct {
		ID       int64     `json:"id"`
		Event    string    `json:"event"`
		Status   string    `json:"status"`
		Attempts int       `json:"attempts"`
		Error    *string   `json:"lastError,omitempty"`
		Created  time.Time `json:"createdAt"`
	}
	out := []view{}
	var owned bool
	err := db.WithTenant(r.Context(), s.pool, user.TenantID, func(tx pgx.Tx) error {
		// Ownership is checked inside the pinned transaction, so a foreign
		// endpoint id simply is not visible rather than being compared against
		// a predicate on an unfiltered read.
		if err := tx.QueryRow(r.Context(),
			`SELECT EXISTS(SELECT 1 FROM tenant_webhooks WHERE id=$1)`,
			id).Scan(&owned); err != nil {
			return err
		}
		if !owned {
			return nil
		}
		rows, err := tx.Query(r.Context(), `
			SELECT id, event_type, status, attempts, last_error, created_at
			FROM webhook_deliveries WHERE endpoint_id=$1
			ORDER BY created_at DESC LIMIT 50`, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var v view
			if err := rows.Scan(&v.ID, &v.Event, &v.Status, &v.Attempts, &v.Error, &v.Created); err != nil {
				return err
			}
			out = append(out, v)
		}
		return rows.Err()
	})
	if err != nil {
		s.domainError(w, err)
		return
	}
	if !owned {
		writeError(w, http.StatusNotFound, "not_found", "Endpoint not found")
		return
	}
	writeJSON(w, http.StatusOK, out)
}