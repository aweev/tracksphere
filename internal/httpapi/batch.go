package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"github.com/google/uuid"

	"github.com/jackc/pgx/v5"

	"github.com/tracksphere/tracksphere/internal/db"

	"github.com/tracksphere/tracksphere/internal/model"
	"github.com/tracksphere/tracksphere/internal/shipments"
)

// bulkItem is one shipment in POST /shipments:batch.
type bulkItem struct {
	TrackingNumber string `json:"trackingNumber"`
	Reference      string `json:"reference"`
	Carrier        string `json:"carrier"`
	Mode           string `json:"mode"`
	Origin         string `json:"origin"`
	Destination    string `json:"destination"`
	IsPublic       *bool  `json:"isPublic"`
}

type bulkResult struct {
	Index          int              `json:"index"`
	Shipment       *model.Shipment  `json:"shipment,omitempty"`
	Error          *model.APIError  `json:"error,omitempty"`
}

// handleBatchCreate POST /api/v1/shipments:batch
//
// Creates up to 50 shipments atomically-ish (per-item results; valid items
// persist even when siblings fail). Idempotency-Key header makes retries
// safe: the first response is replayed verbatim for 24h per (tenant, key).
func (s *Server) handleBatchCreate(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	user := currentUser(r)
	if key != "" {
		if status, raw, ok := s.idempotencyReplay(r, user.TenantID, key); ok {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(status)
			_, _ = w.Write(raw)
			return
		}
	}

	var items []bulkItem
	if !decodeJSON(w, r, &items) {
		return
	}
	if len(items) == 0 || len(items) > 50 {
		writeError(w, http.StatusBadRequest, "invalid_input", "batch must contain 1..50 shipments")
		return
	}

	results := make([]bulkResult, 0, len(items))
	created := 0
	for i, it := range items {
		in := shipments.CreateInput{
			TrackingNumber: strings.TrimSpace(it.TrackingNumber),
			Reference:      strings.TrimSpace(it.Reference),
			Carrier:        strings.ToLower(strings.TrimSpace(it.Carrier)),
			Mode:           it.Mode,
			Origin:         strings.TrimSpace(it.Origin),
			Destination:    strings.TrimSpace(it.Destination),
			IsPublic:       it.IsPublic,
		}
		if err := in.Validate(); err != nil {
			results = append(results, bulkResult{Index: i,
				Error: &model.APIError{Code: "invalid_input", Message: err.Error()}})
			continue
		}
		ship, err := s.shipments.Create(r.Context(), user.TenantID, user.ID, in)
		if err != nil {
			code, msg := "internal", "Internal server error"
			switch {
			case errors.Is(err, shipments.ErrDuplicateTracking):
				code, msg = "duplicate_tracking", "Tracking number already exists in your organization"
			case errors.Is(err, shipments.ErrInvalidInput):
				code, msg = "invalid_input", err.Error()
			}
			results = append(results, bulkResult{Index: i,
				Error: &model.APIError{Code: code, Message: msg}})
			continue
		}
		created++
		results = append(results, bulkResult{Index: i, Shipment: ship})
	}

	body := map[string]any{"created": created, "results": results}
	raw, _ := json.Marshal(map[string]any{"data": body})
	if key != "" {
		// idempotency_keys is RLS-protected (000015): the write needs the
		// tenant pinned or WITH CHECK rejects it, and the error was previously
		// discarded — so no replay record was ever written and a retried batch
		// created duplicate shipments.
		_ = db.WithTenant(r.Context(), s.pool, user.TenantID, func(tx pgx.Tx) error {
			_, err := tx.Exec(r.Context(), `
				INSERT INTO idempotency_keys (tenant_id, key, status, response)
				VALUES ($1,$2,207,$3)
				ON CONFLICT (tenant_id, key) DO NOTHING`,
				user.TenantID, key, raw)
			return err
		})
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusMultiStatus)
	_, _ = w.Write(raw)
}

// idempotencyReplay returns a stored response when this (tenant, key) was
// seen before.
func (s *Server) idempotencyReplay(r *http.Request, tenantID uuid.UUID, key string) (int, []byte, bool) {
	var status int
	var raw []byte
	// Same RLS requirement as the write above; without the pin this always
	// returned zero rows and idempotency was silently inert.
	err := db.WithTenant(r.Context(), s.pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(r.Context(), `
			SELECT status, response FROM idempotency_keys
			WHERE tenant_id=$1 AND key=$2 AND created_at > now() - interval '24 hours'`,
			tenantID, key).Scan(&status, &raw)
	})
	if err != nil || status == 0 {
		return 0, nil, false
	}
	return status, raw, true
}
