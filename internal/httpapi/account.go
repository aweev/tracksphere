package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/tracksphere/tracksphere/internal/db"
	"github.com/tracksphere/tracksphere/internal/model"
)

// auditShipment appends a best-effort row to the shipment audit trail.
// Never fails the request it describes.
//
// This MUST go through db.WithTenant. shipment_audit is FORCE'd with
// WITH CHECK (tenant_id = current_tenant()), so a raw pool.Exec carries no
// app.tenant_id, the insert is rejected by RLS, and — because the error used to
// be discarded — every audit row silently vanished while the endpoint still
// answered 200 with an empty list.
func (s *Server) auditShipment(r *http.Request, user *model.User, shipmentID uuid.UUID, action string, detail map[string]any) {
	raw, err := json.Marshal(detail)
	if err != nil {
		s.log.Error("audit marshal failed", "action", action, "err", err)
		return
	}
	err = db.WithTenant(r.Context(), s.pool, user.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(r.Context(), `
			INSERT INTO shipment_audit (tenant_id, shipment_id, actor_id, actor_email, action, detail)
			VALUES ($1,$2,$3,$4,$5,$6)`,
			user.TenantID, shipmentID, user.ID, user.Email, action, string(raw))
		return err
	})
	if err != nil {
		// Never fail the caller's request, but never lose the failure either:
		// a silently empty audit trail is worse than a logged error.
		s.log.Error("audit insert failed", "action", action,
			"tenant", user.TenantID, "shipment", shipmentID, "err", err)
	}
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
//
// Two context flags are required and they are not interchangeable:
//
//   - app.system must be on for the DELETE itself. The tenants policy is
//     USING (id = current_tenant() OR current_setting('app.system',true)='on'),
//     and current_tenant() is NULL here, so without it the DELETE matches zero
//     rows and raises nothing. That was the original bug: the handler reported
//     {"ok":true} having erased nothing.
//   - app.tenant_id is also set, because app.system is NOT a universal bypass.
//     Eight tenant tables (the seven from 000012 plus sse_subscriptions from
//     000017) have policies with no app.system branch at all, so a verification
//     relying on it alone would read zero rows from exactly those tables and
//     pass falsely. Every tenant policy does admit tenant_id =
//     current_tenant(), so pinning the tenant is what makes the cascade check
//     trustworthy.
//
// The cascade is verified inside the same transaction, before commit, against
// a table list derived from the catalogue rather than a hardcoded array — the
// hardcoded list in the dropped 000018 function named a table that does not
// exist, which is why it always threw.
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

	if err := db.SetSystem(r.Context(), tx); err != nil {
		s.domainError(w, err)
		return
	}
	if err := db.SetTenant(r.Context(), tx, user.TenantID); err != nil {
		s.domainError(w, err)
		return
	}

	tag, err := tx.Exec(r.Context(), `DELETE FROM tenants WHERE id=$1`, user.TenantID)
	if err != nil {
		s.domainError(w, err)
		return
	}
	if tag.RowsAffected() != 1 {
		// Fail loudly. Returning success here is how a no-op erasure became
		// invisible: the request reported the data subject's data was deleted
		// when not a single row was touched.
		s.log.Error("erase deleted no tenant row", "tenant", user.TenantID,
			"rows", tag.RowsAffected())
		writeError(w, http.StatusInternalServerError, "erase_failed",
			"Erasure did not complete. Contact support; nothing was reported as deleted.")
		return
	}

	remaining, err := verifyTenantCascade(r.Context(), tx, user.TenantID)
	if err != nil {
		s.log.Error("erase cascade verification failed", "tenant", user.TenantID, "err", err)
		writeError(w, http.StatusInternalServerError, "erase_verify_failed",
			"Erasure could not be verified. No data was reported as deleted.")
		return
	}
	if len(remaining) > 0 {
		s.log.Error("erase left tenant rows behind", "tenant", user.TenantID, "tables", remaining)
		writeError(w, http.StatusInternalServerError, "erase_incomplete",
			"Erasure left data behind and was rolled back. Contact support.")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		s.domainError(w, err)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name: SessionCookie, Value: "", Path: "/",
		MaxAge: -1, HttpOnly: true, Secure: s.cfg.Env == "production",
		SameSite: http.SameSiteLaxMode,
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// verifyTenantCascade reports any tenant-scoped table still holding rows for
// tenantID. The table list comes from the catalogue (any table in public with
// a tenant_id column) so a future migration that adds a tenant table cannot be
// silently forgotten, which is precisely how the dropped 000018 function broke.
func verifyTenantCascade(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) ([]string, error) {
	rows, err := tx.Query(ctx, `
		SELECT c.relname
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'public'
		  AND c.relkind = 'r'
		  AND EXISTS (
			SELECT 1 FROM pg_attribute a
			WHERE a.attrelid = c.oid AND a.attname = 'tenant_id'
			  AND NOT a.attisdropped AND a.attnum > 0
		  )
		ORDER BY c.relname`)
	if err != nil {
		return nil, err
	}
	var tables []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			rows.Close()
			return nil, err
		}
		tables = append(tables, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var remaining []string
	for _, t := range tables {
		// Identifiers cannot be parameterised; t comes from pg_class, and
		// Sanitize quotes it anyway.
		q := fmt.Sprintf(`SELECT count(*) FROM %s WHERE tenant_id = $1`,
			pgx.Identifier{t}.Sanitize())
		var n int64
		if err := tx.QueryRow(ctx, q, tenantID).Scan(&n); err != nil {
			return nil, fmt.Errorf("count %s: %w", t, err)
		}
		if n > 0 {
			remaining = append(remaining, fmt.Sprintf("%s=%d", t, n))
		}
	}
	return remaining, nil
}
