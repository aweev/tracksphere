package httpapi

import (
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/tracksphere/tracksphere/internal/db"
)

// handleListNotifications GET /api/v1/notifications — delivery history for
// the caller's tenant (the notification center reads this).
func (s *Server) handleListNotifications(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	type view struct {
		ID         int64     `json:"id"`
		ShipmentID *uuid.UUID `json:"shipmentId,omitempty"`
		Channel    string    `json:"channel"`
		Recipient  string    `json:"recipient"`
		Subject    string    `json:"subject"`
		Status     string    `json:"status"`
		CreatedAt  time.Time `json:"createdAt"`
	}
	out := []view{}
	err := db.WithTenant(r.Context(), s.pool, user.TenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `
			SELECT id, shipment_id, channel, recipient, subject, status, created_at
			FROM notifications ORDER BY created_at DESC LIMIT 50`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var v view
			if err := rows.Scan(&v.ID, &v.ShipmentID, &v.Channel, &v.Recipient, &v.Subject, &v.Status, &v.CreatedAt); err != nil {
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
