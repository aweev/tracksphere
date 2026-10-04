package httpapi

import (
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/tracksphere/tracksphere/internal/db"
)

// Multi-leg journeys: ocean + drayage + customs legs inside one shipment.

type legView struct {
	ID          uuid.UUID  `json:"id"`
	Seq         int        `json:"seq"`
	Carrier     string     `json:"carrier"`
	Mode        string     `json:"mode"`
	Origin      string     `json:"origin"`
	Destination string     `json:"destination"`
	Status      string     `json:"status"`
	ETA         *time.Time `json:"eta,omitempty"`
}

func scanLeg(row pgx.Row) (*legView, error) {
	var v legView
	if err := row.Scan(&v.ID, &v.Seq, &v.Carrier, &v.Mode, &v.Origin,
		&v.Destination, &v.Status, &v.ETA); err != nil {
		return nil, err
	}
	return &v, nil
}

// handleListLegs GET /api/v1/shipments/{id}/legs
func (s *Server) handleListLegs(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(chiParam(r, "id"))
	if !ok {
		writeError(w, http.StatusBadRequest, "bad_id", "Invalid shipment id")
		return
	}
	user := currentUser(r)
	if _, err := s.shipments.Get(r.Context(), user.TenantID, id); err != nil {
		s.domainError(w, err)
		return
	}
	out := []legView{}
	err := db.WithTenant(r.Context(), s.pool, user.TenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `
			SELECT id, seq, carrier, mode, origin, destination, status, eta
			FROM shipment_legs WHERE shipment_id=$1 ORDER BY seq`, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			v, err := scanLeg(rows)
			if err != nil {
				return err
			}
			out = append(out, *v)
		}
		return rows.Err()
	})
	if err != nil {
		s.domainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

type legInput struct {
	Carrier     string  `json:"carrier"`
	Mode        string  `json:"mode"`
	Origin      string  `json:"origin"`
	Destination string  `json:"destination"`
	Status      string  `json:"status"`
	ETA         *time.Time `json:"eta"`
}

// handleCreateLeg POST /api/v1/shipments/{id}/legs
func (s *Server) handleCreateLeg(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(chiParam(r, "id"))
	if !ok {
		writeError(w, http.StatusBadRequest, "bad_id", "Invalid shipment id")
		return
	}
	var in legInput
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.Mode == "" {
		in.Mode = "ocean"
	}
	user := currentUser(r)
	if _, err := s.shipments.Get(r.Context(), user.TenantID, id); err != nil {
		s.domainError(w, err)
		return
	}
	var v *legView
	err := db.WithTenant(r.Context(), s.pool, user.TenantID, func(tx pgx.Tx) error {
		var seq int
		_ = tx.QueryRow(r.Context(),
			`SELECT coalesce(max(seq),-1)+1 FROM shipment_legs WHERE shipment_id=$1`, id).Scan(&seq)
		var err error
		v, err = scanLeg(tx.QueryRow(r.Context(), `
			INSERT INTO shipment_legs (tenant_id, shipment_id, seq, carrier, mode, origin, destination, status, eta)
			VALUES ($1,$2,$3,$4,$5,$6,$7,coalesce(nullif($8,''),'booked'),$9)
			RETURNING id, seq, carrier, mode, origin, destination, status, eta`,
			user.TenantID, id, seq, in.Carrier, in.Mode, in.Origin, in.Destination, in.Status, in.ETA))
		return err
	})
	if err != nil {
		s.domainError(w, err)
		return
	}
	s.auditShipment(r, user, id, "leg.create", map[string]any{"seq": v.Seq})
	writeJSON(w, http.StatusCreated, v)
}

// handleDeleteLeg DELETE /api/v1/legs/{id}
func (s *Server) handleDeleteLeg(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(chiParam(r, "id"))
	if !ok {
		writeError(w, http.StatusBadRequest, "bad_id", "Invalid leg id")
		return
	}
	user := currentUser(r)
	var shipmentID uuid.UUID
	err := db.WithTenant(r.Context(), s.pool, user.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(r.Context(), `
			DELETE FROM shipment_legs WHERE id=$1 RETURNING shipment_id`, id).Scan(&shipmentID)
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Leg not found")
		return
	}
	s.auditShipment(r, user, shipmentID, "leg.delete", map[string]any{"legId": id.String()})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
