package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/tracksphere/tracksphere/internal/db"
	"github.com/tracksphere/tracksphere/internal/notify"
)

// handleNotifyStatus GET /api/v1/notify/status — which providers are live plus
// the caller's interrupt-budget position (Alert-fatigue inoculation: the
// budget must be VISIBLE where triage happens, not buried in a worker log).
// Values are booleans/counts only (never secrets); staging shows log=true.
func (s *Server) handleNotifyStatus(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	cap := 5
	used := 0
	pending := 0
	if user != nil {
		sum := sha256.Sum256([]byte(user.Email))
		rhash := hex.EncodeToString(sum[:8])
		_ = db.WithTenant(r.Context(), s.pool, user.TenantID, func(tx pgx.Tx) error {
			_ = tx.QueryRow(r.Context(),
				`SELECT coalesce((SELECT interrupt_hourly_cap FROM notification_prefs WHERE tenant_id=$1), 5)`,
				user.TenantID).Scan(&cap)
			_ = tx.QueryRow(r.Context(), `
				SELECT count(*) FROM notification_interrupts
				WHERE tenant_id=$1 AND recipient_hash=$2
				  AND created_at > now() - interval '1 hour'`,
				user.TenantID, rhash).Scan(&used)
			_ = tx.QueryRow(r.Context(), `
				SELECT count(*) FROM notification_digest_queue
				WHERE tenant_id=$1 AND recipient_hash=$2 AND flushed_at IS NULL`,
				user.TenantID, rhash).Scan(&pending)
			return nil
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"providers":          notify.Configured(),
		"interruptHourlyCap": cap,
		"interruptsUsed":     used,
		"digestPending":      pending,
	})
}
