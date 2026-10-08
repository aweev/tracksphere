package httpapi

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/tracksphere/tracksphere/internal/config"
	"github.com/tracksphere/tracksphere/internal/metrics"
	"github.com/tracksphere/tracksphere/internal/model"
	"github.com/tracksphere/tracksphere/internal/shipments"
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
// X-TrackSphere-Signature header (per-carrier secret when configured, else
// the global fallback). Every delivery — valid or not — is written to
// webhook_inbox for audit/replay before any processing happens. raw_body
// keeps the exact bytes; payload holds parsed JSON or '{}'.
func (s *Server) handleCarrierWebhook(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	carrier := strings.ToLower(strings.TrimSpace(chiParam(r, "carrier")))
	if carrier == "" {
		writeError(w, http.StatusBadRequest, "bad_carrier", "Carrier segment required")
		return
	}
	if !config.KnownCarriers[carrier] {
		writeError(w, http.StatusBadRequest, "unknown_carrier", fmt.Sprintf("Unknown carrier %q", carrier))
		return
	}

	// Flood guard: audit-before-verify means anyone can mint inbox rows, so
	// the inbox needs a per-carrier hourly cap on top of the per-IP rate
	// limit. Served by webhook_inbox_carrier_hour_idx (000027).
	if cap := s.cfg.WebhookFloodPerHour; cap > 0 {
		var recent int64
		if err := s.pool.QueryRow(r.Context(),
			`SELECT count(*) FROM webhook_inbox
			  WHERE carrier=$1 AND received_at > now() - interval '1 hour'`,
			carrier).Scan(&recent); err == nil && recent >= int64(cap) {
			metrics.WebhookReceivedTotal.WithLabelValues(carrier, "flood").Inc()
			w.Header().Set("Retry-After", "300")
			writeError(w, http.StatusTooManyRequests, "rate_limited", "Carrier flood guard tripped, retry shortly")
			return
		}
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_body", "Could not read body")
		return
	}

	secret := s.cfg.WebhookSecretFor(carrier)
	sigOK := len(secret) > 0 && verifySignature(secret, body, r.Header.Get("X-TrackSphere-Signature"))

	// Audit first: never fail the audit insert on non-JSON bodies.
	var payload any
	payloadDoc := "{}"
	if json.Unmarshal(body, &payload) == nil {
		payloadDoc = string(body)
	}
	var inboxID int64
	if err := s.pool.QueryRow(r.Context(),
		`INSERT INTO webhook_inbox (carrier, signature_valid, payload, raw_body)
		 VALUES ($1,$2,$3::jsonb,$4)
		 RETURNING id`,
		carrier, sigOK, payloadDoc, string(body)).Scan(&inboxID); err != nil {
		s.log.Error("webhook inbox insert failed", "err", err)
	}

	if !sigOK {
		metrics.WebhookReceivedTotal.WithLabelValues(carrier, "invalid_signature").Inc()
		writeError(w, http.StatusUnauthorized, "bad_signature", "Signature verification failed")
		return
	}

	// Optional replay window (H5): checked only after the signature passed,
	// so the timestamp narrows freshness for authenticated deliveries instead
	// of authenticating anything itself (the HMAC covers the body only).
	// Carriers that send X-TrackSphere-Timestamp (unix seconds) get ±5min
	// enforcement; absent header = legacy path where dedup keeps replays
	// idempotent and the flood guard bounds them. Same error code as a bad
	// signature so the header is not an oracle.
	if tsRaw := strings.TrimSpace(r.Header.Get("X-TrackSphere-Timestamp")); tsRaw != "" {
		ts, err := strconv.ParseInt(tsRaw, 10, 64)
		if err != nil || time.Since(time.Unix(ts, 0)).Abs() > 5*time.Minute {
			metrics.WebhookReceivedTotal.WithLabelValues(carrier, "stale_timestamp").Inc()
			writeError(w, http.StatusUnauthorized, "bad_signature", "Signature verification failed")
			return
		}
	}

	ev, err := model.DecodeCarrierEvent(body)
	if err != nil {
		s.pool.Exec(r.Context(),
			`UPDATE webhook_inbox SET error=$2 WHERE id=$1`, inboxID, "bad_event: "+err.Error())
		metrics.WebhookReceivedTotal.WithLabelValues(carrier, "bad_event").Inc()
		writeError(w, http.StatusBadRequest, "bad_event", err.Error())
		return
	}

	result, err := s.svc.IngestEvent(r.Context(), carrier, ev, "webhook")
	if err != nil {
		s.pool.Exec(r.Context(),
			`UPDATE webhook_inbox SET error=$2 WHERE id=$1`, inboxID, err.Error())
		metrics.WebhookReceivedTotal.WithLabelValues(carrier, "ingest_failed").Inc()
		switch {
		case errors.Is(err, shipments.ErrAmbiguousTracking):
			// Same carrier+tracking in several tenants — needs manual resolution.
			writeError(w, http.StatusConflict, "ambiguous_tracking", err.Error())
		case errors.Is(err, shipments.ErrUnknownTracking):
			// Unknown tracking → 404 so carriers retry with backoff/alerting.
			writeError(w, http.StatusNotFound, "unknown_tracking", err.Error())
		default:
			// DB/serialization failures must not masquerade as missing
			// shipments — 500 + Retry-After so carriers back off correctly.
			w.Header().Set("Retry-After", "30")
			s.log.Error("webhook ingest failed", "err", err)
			writeError(w, http.StatusInternalServerError, "ingest_failed", "Temporary failure, retry shortly")
		}
		return
	}

	_, _ = s.pool.Exec(r.Context(),
		`UPDATE webhook_inbox SET processed=true, shipment_id=$2 WHERE id=$1`,
		inboxID, result.ShipmentID)

	metrics.WebhookReceivedTotal.WithLabelValues(carrier, "ok").Inc()
	metrics.IngestionDuration.WithLabelValues(carrier, "webhook").Observe(time.Since(start).Seconds())

	writeJSON(w, http.StatusOK, result)
}

// handleManualEvent POST /api/v1/shipments/{id}/events
// Lets ops agents append a scan through the same ingestion pipeline with
// source='manual' and a uuid event id (no same-second collisions).
func (s *Server) handleManualEvent(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(chiParam(r, "id"))
	if !ok {
		writeError(w, http.StatusBadRequest, "bad_id", "Invalid shipment id")
		return
	}
	var req struct {
		Code        string   `json:"code"`
		Description string   `json:"description"`
		Location    string   `json:"location"`
		Lat         *float64 `json:"lat"`
		Lng         *float64 `json:"lng"`
		Status      string   `json:"status"`
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
		EventID:        "ops-" + uuid.NewString(),
		Code:           req.Code,
		Description:    req.Description,
		Location:       req.Location,
		Lat:            req.Lat,
		Lng:            req.Lng,
		OccurredAt:     now,
		Status:         req.Status,
	}
	result, err := s.svc.IngestEvent(r.Context(), ship.Carrier, ev, "manual")
	if err != nil {
		s.domainError(w, err)
		return
	}
	s.auditShipment(r, user, id, "event.manual", map[string]any{"code": req.Code})
	writeJSON(w, http.StatusCreated, result)
}
