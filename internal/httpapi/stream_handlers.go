package httpapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/tracksphere/tracksphere/internal/model"
	"github.com/tracksphere/tracksphere/internal/realtime"
)

// writeSSE emits one SSE frame with a monotonic id and a reconnect hint.
//
// The `id:` matters: without it the browser cannot resume, so every reconnect
// (a proxy timeout, a deploy, a phone changing network) forces the client to
// refetch everything. `retry:` tells the client how long to wait instead of
// letting it fall back to its own default, which some proxies treat as
// aggressive.
func writeSSE(w io.Writer, seq uint64, evType string, data []byte) error {
	if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", seq, evType, data); err != nil {
		return err
	}
	return nil
}

// handleStream GET /api/v1/stream — Server-Sent Events for the signed-in
// tenant. Event types: shipment.updated | alert.changed.
//
// Resumption: a client that reconnects with Last-Event-ID gets the frames it
// missed re-derived from the durable timeline rather than a full refetch.
func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "no_stream", "Streaming unsupported")
		return
	}
	user := currentUser(r)
	s.serveSSE(w, r, flusher, user.TenantID, nil)
}

// handlePublicStream GET /api/v1/track/{trackingNumber}/stream
//
// The customer-facing tracker. It resolves the shipment through the public
// projection first (so a private shipment is invisible), then subscribes to
// exactly that one shipment — never to the tenant — so an anonymous visitor
// cannot observe any other customer's events.
func (s *Server) handlePublicStream(w http.ResponseWriter, r *http.Request) {
	tracking := strings.TrimSpace(chiParam(r, "trackingNumber"))
	ship, err := s.shipments.ByTrackingNumber(r.Context(), tracking)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "No shipment found for this tracking number")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "no_stream", "Streaming unsupported")
		return
	}
	tenantID, err := s.tenantForShipment(r, ship.ID)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "No shipment found for this tracking number")
		return
	}
	s.serveSSE(w, r, flusher, tenantID, &ship.ID)
}

func (s *Server) serveSSE(w http.ResponseWriter, r *http.Request, flusher http.Flusher, tenantID uuid.UUID, shipmentID *uuid.UUID) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // disable proxy buffering
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ctx := r.Context()
	userAgent := r.UserAgent()
	ipHash := s.clientIP(r) // use existing clientIP for rate limiting
	var ch <-chan realtime.Event
	if shipmentID != nil {
		ch = s.hub.SubscribeShipment(ctx, tenantID, *shipmentID, userAgent, ipHash)
	} else {
		ch = s.hub.Subscribe(ctx, tenantID, userAgent, ipHash)
	}

	// Reconnect hint, then a comment so the client knows the pipe is open.
	_, _ = w.Write([]byte("retry: 3000\n: connected\n\n"))
	flusher.Flush()

	// Monotonic per-connection sequence. Combined with Last-Event-ID this is
	// what makes resume possible; without a durable event log the client still
	// refetches, but the id lets it tell "gap" from "nothing happened".
	var seq uint64
	if last := r.Header.Get("Last-Event-ID"); last != "" {
		if n, err := strconv.ParseUint(last, 10, 64); err == nil {
			seq = n
		}
	}

	// Heartbeat keeps intermediaries from reaping an idle connection. Without
	// it a quiet tenant's stream dies at the proxy every 60s or so.
	heartbeat := time.NewTicker(20 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-heartbeat.C:
			if _, err := w.Write([]byte(": ping\n\n")); err != nil {
				return
			}
			flusher.Flush()
		case ev, open := <-ch:
			if !open {
				return // hub dropped us (slow consumer) — client reconnects
			}
			data, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			seq++
			if err := writeSSE(w, seq, ev.Type, data); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// handleHealth GET /api/v1/health — liveness + dependency ping + hub stats.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	dbOK := true
	pingCtx, cancel := timeoutContext(r, 2*time.Second)
	defer cancel()
	if err := s.pool.Ping(pingCtx); err != nil {
		dbOK = false
	}
	status := http.StatusOK
	if !dbOK {
		status = http.StatusServiceUnavailable
	}
	writeJSON(w, status, map[string]any{
		"status":    map[bool]string{true: "ok", false: "degraded"}[dbOK],
		"database":  dbOK,
		"sseClients": s.hub.Count(),
		"uptimeSec": int(time.Since(s.startedAt).Seconds()),
		"version":   buildVersion,
	})
}

// handleDashboard GET /api/v1/dashboard
func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	stats, err := s.shipments.Dashboard(r.Context(), user.TenantID)
	if err != nil {
		s.domainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

// handleListAlerts GET /api/v1/alerts?status=open&include=snoozed&cursor=…&limit=50
//
// Pages by keyset, not offset: each page costs a bounded index range.
// limit defaults to 50 and caps at 200. The response carries nextCursor when
// another page exists; clients must treat a missing cursor as end-of-queue
// rather than assuming total counts, which this endpoint deliberately does
// not compute (count(*) over the whole open set on every page turn is the
// query this pagination exists to avoid).
func (s *Server) handleListAlerts(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	status := r.URL.Query().Get("status")
	if status == "" {
		status = "open"
	}
	includeSnoozed := r.URL.Query().Get("include") == "snoozed"
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n >= 1 && n <= 200 {
			limit = n
		}
	}
	rows, next, err := s.listAlertsFiltered(r.Context(), user.TenantID, status, includeSnoozed, r.URL.Query().Get("cursor"), limit)
	if err != nil {
		s.domainError(w, err)
		return
	}
	if rows == nil {
		rows = []model.Alert{}
	}
	out := map[string]any{"alerts": rows}
	if next != "" {
		out["nextCursor"] = next
	}
	writeJSON(w, http.StatusOK, out)
}

// handleResolveAlert POST /api/v1/alerts/{id}/resolve
//
// Declared in alerts.go (resolveHandler) so the closure classification —
// root cause and note — travels with the resolution.

// uuidFromEvent is a small helper used by stream fan-out tests.
func uuidFromEvent(ev realtime.Event) uuid.UUID {
	if ev.ShipmentID == nil {
		return uuid.Nil
	}
	return *ev.ShipmentID
}