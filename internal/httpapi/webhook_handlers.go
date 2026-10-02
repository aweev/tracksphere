package httpapi

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"strings"

	"github.com/tracksphere/tracksphere/internal/model"
)

// verifySignature checks X-TrackSphere-Signature: sha256=<hex hmac of body>.
func verifySignature(secret, body []byte, header string) bool {
	header = strings.TrimSpace(header)
	header = strings.TrimPrefix(header, "sha256=")
	want, err := hex.DecodeString(header)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return hmac.Equal(want, mac.Sum(nil))
}

// handleCarrierWebhook POST /api/v1/webhooks/carriers/{carrier}
//
// Contract: HMAC-SHA256 over the raw body, hex-encoded, in the
// X-TrackSphere-Signature header. Every delivery — valid or not — is written
// to webhook_inbox for audit/replay before any processing happens.
func (s *Server) handleCarrierWebhook(w http.ResponseWriter, r *http.Request) {
	carrier := strings.ToLower(chiParam(r, "carrier"))
	if carrier == "" {
		writeError(w, http.StatusBadRequest, "bad_carrier", "Carrier segment required")
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_body", "Could not read body")
		return
	}

	sigOK := verifySignature(s.cfg.WebhookSecret, body, r.Header.Get("X-TrackSphere-Signature"))

	// Audit first: insert before validation so rejected deliveries are visible.
	var inboxID int64
	if err := s.pool.QueryRow(r.Context(),
		`INSERT INTO webhook_inbox (carrier, signature_valid, payload)
		 VALUES ($1,$2, coalesce($3::jsonb, '{}'::jsonb))
		 RETURNING id`,
		carrier, sigOK, string(body)).Scan(&inboxID); err != nil {
		s.log.Error("webhook inbox insert failed", "err", err)
	}

	if !sigOK {
		writeError(w, http.StatusUnauthorized, "bad_signature", "Signature verification failed")
		return
	}

	ev, err := model.DecodeCarrierEvent(body)
	if err != nil {
		s.pool.Exec(r.Context(),
			`UPDATE webhook_inbox SET error=$2 WHERE id=$1`, inboxID, err.Error())
		writeError(w, http.StatusBadRequest, "bad_event", err.Error())
		return
	}

	result, err := s.svc.IngestEvent(r.Context(), carrier, ev)
	if err != nil {
		s.pool.Exec(r.Context(),
			`UPDATE webhook_inbox SET error=$2 WHERE id=$1`, inboxID, err.Error())
		// Unknown tracking number → 404 so carriers retry with backoff/alerting.
		writeError(w, http.StatusNotFound, "unknown_tracking", err.Error())
		return
	}

	_, _ = s.pool.Exec(r.Context(),
		`UPDATE webhook_inbox SET processed=true, shipment_id=$2 WHERE id=$1`,
		inboxID, result.ShipmentID)

	writeJSON(w, http.StatusOK, result)
}

// handleManualEvent POST /api/v1/shipments/{id}/events
// Lets ops agents append a scan through the same ingestion pipeline
// (source becomes 'webhook' with a synthetic ops event id — noted in API docs
// as a known simplification; a dedicated 'manual' source lands with the
// exception-queue UI in Phase 2).
func (s *Server) handleManualEvent(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(chiParam(r, "id"))
	if !ok {
		writeError(w, http.StatusBadRequest, "bad_id", "Invalid shipment id")
		return
	}
	var req struct {
		Code        string  `json:"code"`
		Description string  `json:"description"`
		Location    string  `json:"location"`
		Lat         *float64 `json:"lat"`
		Lng         *float64 `json:"lng"`
		Status      string  `json:"status"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Code == "" {
		writeError(w, http.StatusBadRequest, "invalid_input", "code is required")
		return
	}
	if req.Status != "" && !model.ValidStatus(req.Status) {
		writeError(w, http.StatusBadRequest, "invalid_input", "unknown status")
		return
	}

	// Need the tracking number to reuse IngestEvent.
	user := currentUser(r)
	ship, err := s.shipments.Get(r.Context(), user.TenantID, id)
	if err != nil {
		s.domainError(w, err)
		return
	}

	now := timeNowUTC()
	ev := &model.CarrierEvent{
		TrackingNumber: ship.TrackingNumber,
		EventID:        "ops-" + id.String()[:8] + "-" + now.Format("20060102150405"),
		Code:           req.Code,
		Description:     req.Description,
		Location:       req.Location,
		Lat:            req.Lat,
		Lng:            req.Lng,
		OccurredAt:     now,
		Status:         req.Status,
	}
	result, err := s.svc.IngestEvent(r.Context(), ship.Carrier, ev)
	if err != nil {
		s.domainError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, result)
}