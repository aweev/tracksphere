package httpapi

import (
	"net/http"
	"strconv"

	"github.com/tracksphere/tracksphere/internal/queue"
)

// handleListDeadJobs GET /api/v1/jobs/dead — dead-letter queue (admin+).
// Tenant-scoped: a tenant admin sees only their own tenant's dead jobs.
// System jobs (tenant_id IS NULL) are invisible here by design.
func (s *Server) handleListDeadJobs(w http.ResponseWriter, r *http.Request) {
	limit := 25
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n >= 1 && n <= 100 {
			limit = n
		}
	}
	q := queue.New(s.pool, 0, s.log)
	dead, err := q.ListDead(r.Context(), currentTenantID(r), limit)
	if err != nil {
		s.domainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dead)
}

// handleReplayDeadJob POST /api/v1/jobs/dead/{id}/replay (admin+)
func (s *Server) handleReplayDeadJob(w http.ResponseWriter, r *http.Request) {
	raw := chiParam(r, "id")
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "bad_id", "Invalid job id")
		return
	}
	q := queue.New(s.pool, 0, s.log)
	if err := q.ReplayDead(r.Context(), currentTenantID(r), id); err != nil {
		writeError(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
