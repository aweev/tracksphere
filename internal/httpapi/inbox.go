package httpapi

import (
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/tracksphere/tracksphere/internal/db"
)

// handleListWebhookInbox GET /api/v1/webhooks/inbox — audit trail of inbound
// carrier deliveries attached to this tenant's shipments (plus unresolvable
// ones are hidden: they carry no tenant). Newest first, max 50.
func (s *Server) handleListWebhookInbox(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	type view struct {
		ID             int64      `json:"id"`
		Carrier        string     `json:"carrier"`
		SignatureValid bool       `json:"signatureValid"`
		Processed      bool       `json:"processed"`
		Error          *string    `json:"error,omitempty"`
		ShipmentID     *uuid.UUID `json:"shipmentId,omitempty"`
		ReceivedAt     time.Time  `json:"receivedAt"`
	}
	out := []view{}
	err := db.WithTenant(r.Context(), s.pool, user.TenantID, func(tx pgx.Tx) error {
		// Query shape matters here. webhook_inbox has no tenant_id and no RLS,
		// so the previous `FROM webhook_inbox LEFT JOIN shipments WHERE
		// s.tenant_id=$1 ORDER BY w.id DESC LIMIT 50` scanned every tenant's
		// webhooks on every page view and only then applied the LIMIT. Driving
		// from shipments — which IS tenant-scoped and indexed — lets the LIMIT
		// apply to this tenant's rows.
		rows, err := tx.Query(r.Context(), `
			SELECT w.id, w.carrier, w.signature_valid, w.processed, w.error,
			       w.shipment_id, w.received_at
			FROM shipments s
			JOIN webhook_inbox w ON w.shipment_id = s.id
			ORDER BY w.received_at DESC, w.id DESC
			LIMIT 50`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var v view
			if err := rows.Scan(&v.ID, &v.Carrier, &v.SignatureValid, &v.Processed, &v.Error, &v.ShipmentID, &v.ReceivedAt); err != nil {
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
