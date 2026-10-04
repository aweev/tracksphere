package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/tracksphere/tracksphere/internal/auth"
	"github.com/tracksphere/tracksphere/internal/carriers"
	"github.com/tracksphere/tracksphere/internal/db"
	"github.com/tracksphere/tracksphere/internal/httpclient"
	"github.com/tracksphere/tracksphere/internal/queue"
)

// Carrier polling credentials (admin+). Secrets are sealed with the server
// key; reads never return them.

type carrierCredView struct {
	ID          uuid.UUID  `json:"id"`
	Carrier     string     `json:"carrier"`
	BaseURL     string     `json:"baseUrl"`
	PollMinutes int        `json:"pollMinutes"`
	Active      bool       `json:"active"`
	LastPolled  *time.Time `json:"lastPolledAt,omitempty"`
	LastError   *string    `json:"lastError,omitempty"`
}

// handleListCarriers GET /api/v1/carriers — supported slugs + tenant creds.
func (s *Server) handleListCarriers(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	creds := []carrierCredView{}
	err := db.WithTenant(r.Context(), s.pool, user.TenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `
			SELECT id, carrier, base_url, poll_interval_minutes, active,
			       last_polled_at, last_error
			FROM carrier_credentials WHERE tenant_id=$1 ORDER BY carrier`, user.TenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var v carrierCredView
			if err := rows.Scan(&v.ID, &v.Carrier, &v.BaseURL, &v.PollMinutes, &v.Active, &v.LastPolled, &v.LastError); err != nil {
				return err
			}
			creds = append(creds, v)
		}
		return rows.Err()
	})
	if err != nil {
		s.domainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"supported": carriers.Slugs(), "credentials": creds,
	})
}

type upsertCarrierRequest struct {
	Carrier     string `json:"carrier"`
	BaseURL     string `json:"baseUrl"`
	APIKey      string `json:"apiKey"`
	APISecret   string `json:"apiSecret"`
	AccountID   string `json:"accountId"`
	PollMinutes *int   `json:"pollMinutes"`
	Active      *bool  `json:"active"`
}

// handleUpsertCarrier POST /api/v1/carriers — create or replace credentials.
func (s *Server) handleUpsertCarrier(w http.ResponseWriter, r *http.Request) {
	var req upsertCarrierRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	carrier := strings.ToLower(strings.TrimSpace(req.Carrier))
	if !carriers.Known(carrier) {
		writeError(w, http.StatusBadRequest, "unknown_carrier", "Supported: "+strings.Join(carriers.Slugs(), ", "))
		return
	}
	user := currentUser(r)
	sealed := ""
	if req.APIKey != "" || req.APISecret != "" || req.AccountID != "" {
		raw, _ := json.Marshal(map[string]string{
			"apiKey": req.APIKey, "apiSecret": req.APISecret, "accountId": req.AccountID,
		})
		s2, err := auth.SealMulti(s.cfg.SecretKeys, raw)
		if err != nil {
			s.domainError(w, err)
			return
		}
		sealed = s2
	}
	pollMinutes := 60
	if req.PollMinutes != nil && *req.PollMinutes >= 5 && *req.PollMinutes <= 1440 {
		pollMinutes = *req.PollMinutes
	}
	active := true
	if req.Active != nil {
		active = *req.Active
	}

	// base_url is fetched by the worker with the tenant's sealed credentials
	// attached as a bearer token. It was previously stored with only a
	// TrimSpace, so any tenant admin could point the poller at the cloud
	// metadata service, the database, or an internal admin panel and have the
	// server request it on a schedule — a full SSRF proxy that also leaked the
	// carrier credential to the attacker's host.
	baseURL := strings.TrimSpace(req.BaseURL)
	if baseURL != "" {
		if err := httpclient.ValidateOutboundURL(baseURL); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_base_url", err.Error())
			return
		}
	}

	var id uuid.UUID
	err := db.WithTenant(r.Context(), s.pool, user.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(r.Context(), `
			INSERT INTO carrier_credentials
				(tenant_id, carrier, base_url, sealed_creds, poll_interval_minutes, active, created_by)
			VALUES ($1,$2,$3,$4,$5,$6,$7)
			ON CONFLICT (tenant_id, carrier) DO UPDATE SET
				base_url=EXCLUDED.base_url,
				sealed_creds=CASE WHEN EXCLUDED.sealed_creds='' THEN carrier_credentials.sealed_creds ELSE EXCLUDED.sealed_creds END,
				poll_interval_minutes=EXCLUDED.poll_interval_minutes,
				active=EXCLUDED.active,
				last_error=NULL
			RETURNING id`,
			user.TenantID, carrier, baseURL, sealed, pollMinutes, active, user.ID).Scan(&id)
	})
	if err != nil {
		s.domainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "carrier": carrier})
}

// handleDeleteCarrier DELETE /api/v1/carriers/{carrier}
func (s *Server) handleDeleteCarrier(w http.ResponseWriter, r *http.Request) {
	carrier := strings.ToLower(strings.TrimSpace(chiParam(r, "carrier")))
	user := currentUser(r)
	var deleted bool
	err := db.WithTenant(r.Context(), s.pool, user.TenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(r.Context(),
			`DELETE FROM carrier_credentials WHERE tenant_id=$1 AND carrier=$2`,
			user.TenantID, carrier)
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
		writeError(w, http.StatusNotFound, "not_found", "No credentials for carrier")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleDetectCarrier GET /api/v1/carriers/detect?tracking=... — shape-based
// guess (UPS 1Z, FedEx digits, DHL, ocean B/L). Heuristic, not proof.
func (s *Server) handleDetectCarrier(w http.ResponseWriter, r *http.Request) {
	tracking := r.URL.Query().Get("tracking")
	if strings.TrimSpace(tracking) == "" {
		writeError(w, http.StatusBadRequest, "invalid_input", "tracking required")
		return
	}
	writeJSON(w, http.StatusOK, carriers.DetectCarrier(tracking))
}

// handleTriggerPoll POST /api/v1/carriers/{carrier}/poll — enqueue a poll now.
func (s *Server) handleTriggerPoll(w http.ResponseWriter, r *http.Request) {
	carrier := strings.ToLower(strings.TrimSpace(chiParam(r, "carrier")))
	if !carriers.Known(carrier) {
		writeError(w, http.StatusBadRequest, "unknown_carrier", "Unknown carrier")
		return
	}
	user := currentUser(r)
	var credID uuid.UUID
	err := db.WithTenant(r.Context(), s.pool, user.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(r.Context(), `
			SELECT id FROM carrier_credentials WHERE tenant_id=$1 AND carrier=$2 AND active=true`,
			user.TenantID, carrier).Scan(&credID)
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "No active credentials — save them first")
		return
	}
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		s.domainError(w, err)
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	if err := queue.EnqueueTx(r.Context(), tx, "shipment.poll", map[string]any{
		"credentialId": credID.String(),
		"tenantId":     user.TenantID.String(),
		"carrier":      carrier,
	}, time.Time{}); err != nil {
		s.domainError(w, err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		s.domainError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "carrier": carrier})
}
