package httpapi

import (
	"net/http"

	"github.com/tracksphere/tracksphere/internal/notify"
)

// handleNotifyStatus GET /api/v1/notify/status — which providers are live.
// Values are booleans only (never secrets); staging shows log=true.
func (s *Server) handleNotifyStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"providers": notify.Configured()})
}
