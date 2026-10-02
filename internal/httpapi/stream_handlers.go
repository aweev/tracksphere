package httpapi

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/tracksphere/tracksphere/internal/realtime"
)

// handleStream GET /api/v1/stream — Server-Sent Events for the signed-in
// tenant. Event types: shipment.updated | alert.changed | heartbeat.
func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "no_stream", "Streaming unsupported")
		return
	}
	user := currentUser(r)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // disable proxy buffering
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ctx := r.Context()
	ch := s.hub.Subscribe(ctx, user.TenantID)

	// Immediate comment so the client knows the pipe is open.
	_, _ = w.Write([]byte(": connected\n\n"))
	flusher.Flush()

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
			if _, err := w.Write([]byte("event: " + ev.Type + "\n")); err != nil {
				return
			}
			if _, err := w.Write([]byte("data: " + string(data) + "\n\n")); err != nil {
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

// handleListAlerts GET /api/v1/alerts?status=open
func (s *Server) handleListAlerts(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	status := r.URL.Query().Get("status")
	if status == "" {
		status = "open"
	}
	rows, err := s.listAlerts(r.Context(), user.TenantID, status)
	if err != nil {
		s.domainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

// handleResolveAlert POST /api/v1/alerts/{id}/resolve
func (s *Server) handleResolveAlert(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(chiParam(r, "id"))
	if !ok {
		writeError(w, http.StatusBadRequest, "bad_id", "Invalid alert id")
		return
	}
	user := currentUser(r)
	if err := s.resolveAlert(r.Context(), user.TenantID, user.ID, id); err != nil {
		s.domainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// uuidFromEvent is a small helper used by stream fan-out tests.
func uuidFromEvent(ev realtime.Event) uuid.UUID {
	if ev.ShipmentID == nil {
		return uuid.Nil
	}
	return *ev.ShipmentID
}