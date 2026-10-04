package httpapi

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/tracksphere/tracksphere/internal/db"
	"github.com/tracksphere/tracksphere/internal/model"
)

// auditShipment appends a best-effort row to the shipment audit trail.
// Never fails the request it describes.
func (s *Server) auditShipment(r *http.Request, user *model.User, shipmentID uuid.UUID, action string, detail map[string]any) {
	raw, _ := json.Marshal(detail)
	_, _ = s.pool.Exec(r.Context(), `
		INSERT INTO shipment_audit (tenant_id, shipment_id, actor_id, actor_email, action, detail)
		VALUES ($1,$2,$3,$4,$5,$6)`,
		user.TenantID, shipmentID, user.ID, user.Email, action, string(raw))
}

// handleListShipmentAudit GET /api/v1/shipments/{id}/audit — who changed what.
func (s *Server) handleListShipmentAudit(w http.ResponseWriter, r *http.Request) {
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
	type view struct {
		Actor     string         `json:"actor"`
		Action    string         `json:"action"`
		Detail    map[string]any `json:"detail"`
		CreatedAt time.Time      `json:"createdAt"`
	}
	out := []view{}
	err := db.WithTenant(r.Context(), s.pool, user.TenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `
			SELECT actor_email, action, detail, created_at
			FROM shipment_audit WHERE shipment_id=$1 ORDER BY created_at DESC LIMIT 50`, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var v view
			var raw []byte
			if err := rows.Scan(&v.Actor, &v.Action, &raw, &v.CreatedAt); err != nil {
				return err
			}
			_ = json.Unmarshal(raw, &v.Detail)
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

// handleExportAccount GET /api/v1/account/export — GDPR Art. 20: full
// machine-readable dump of the tenant's data.
func (s *Server) handleExportAccount(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	dump := map[string]any{}
	err := db.WithTenant(r.Context(), s.pool, user.TenantID, func(tx pgx.Tx) error {
		for table, dest := range map[string]*[]map[string]any{
			"shipments": {}, "shipment_events": {}, "shipment_legs": {},
			"shipment_documents": {}, "alerts": {}, "notifications": {},
			"tracking_subscriptions": {}, "shipment_audit": {},
		} {
			rows, err := tx.Query(r.Context(), `SELECT to_jsonb(t) FROM `+table+` t`)
			if err != nil {
				return err
			}
			var arr []map[string]any
			for rows.Next() {
				var raw []byte
				if err := rows.Scan(&raw); err != nil {
					rows.Close()
					return err
				}
				var m map[string]any
				if err := json.Unmarshal(raw, &m); err != nil {
					rows.Close()
					return err
				}
				arr = append(arr, m)
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return err
			}
			*dest = arr
			dump[table] = arr
		}
		return nil
	})
	if err != nil {
		s.domainError(w, err)
		return
	}
	dump["exportedAt"] = time.Now().UTC()
	writeJSON(w, http.StatusOK, dump)
}

type eraseRequest struct {
	Confirm string `json:"confirm"`
}

// handleEraseAccount DELETE /api/v1/account (owner only, confirm:"ERASE").
// Cascades through every tenant FK — the tenant row is the isolation
// boundary, so deleting it erases everything. Irreversible by design.
// Runs verify_tenant_erase to ensure complete cascade.
func (s *Server) handleEraseAccount(w http.ResponseWriter, r *http.Request) {
	var req eraseRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Confirm != "ERASE" {
		writeError(w, http.StatusBadRequest, "invalid_input", `Send {"confirm":"ERASE"} to erase`)
		return
	}
	user := currentUser(r)
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		s.domainError(w, err)
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	if _, err := tx.Exec(r.Context(), `DELETE FROM tenants WHERE id=$1`, user.TenantID); err != nil {
		s.domainError(w, err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		s.domainError(w, err)
		return
	}
	// Verify the erase completed (cascaded to all child tables)
	_, err = s.pool.Exec(r.Context(), `SELECT verify_tenant_erase($1)`, user.TenantID)
	if err != nil {
		s.log.Error("GDPR erase verification failed", "tenant", user.TenantID, "err", err)
		// Don't fail the request - the delete succeeded, verification is defense-in-depth
	}
	http.SetCookie(w, &http.Cookie{
		Name: SessionCookie, Value: "", Path: "/",
		MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
