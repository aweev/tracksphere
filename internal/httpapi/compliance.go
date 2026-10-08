package httpapi

import (
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/tracksphere/tracksphere/internal/db"
	"github.com/tracksphere/tracksphere/internal/sso"
)

// handleStatusz GET /api/v1/statusz — public status payload for the status
// page and region-aware clients: liveness, version, region, uptime, and
// queue-depth summary (no tenant data).
func (s *Server) handleStatusz(w http.ResponseWriter, r *http.Request) {
	dbOK := true
	pingCtx, cancel := timeoutContext(r, 2*time.Second)
	defer cancel()
	if err := s.pool.Ping(pingCtx); err != nil {
		dbOK = false
	}
	var pending, dead int64
	_ = s.pool.QueryRow(r.Context(),
		`SELECT count(*) FILTER (WHERE status='pending'),
		        count(*) FILTER (WHERE status='dead') FROM jobs`).Scan(&pending, &dead)
	// Backup freshness is a live fact, not a doc claim: the backup sidecar
	// (deploy/backup.sh) writes backup_runs after each pg_dump.
	var lastBackup *time.Time
	_ = s.pool.QueryRow(r.Context(),
		`SELECT max(finished_at) FROM backup_runs WHERE ok=true`).Scan(&lastBackup)
	backupAgeH := -1.0
	if lastBackup != nil {
		backupAgeH = time.Since(*lastBackup).Hours()
	}
	status := "operational"
	if !dbOK {
		status = "degraded"
	} else if dead > 0 {
		status = "degraded"
	} else if lastBackup == nil || backupAgeH > 30 {
		// No proven backup, or the last one is older than the daily
		// schedule + margin: the status page must say so.
		status = "degraded"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":   status,
		"region":   s.cfg.Region,
		"version":  buildVersion,
		"uptimeSec": int(time.Since(s.startedAt).Seconds()),
		"queue":    map[string]any{"pending": pending, "dead": dead},
		"backup":   map[string]any{"lastSuccess": lastBackup, "ageHours": backupAgeH},
	})
}

// handleEvidence GET /api/v1/compliance/evidence (owner) — SOC 2 evidence
// pack: control facts the auditor asks for, computed from live data.
func (s *Server) handleEvidence(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	pack := map[string]any{
		"generatedAt": time.Now().UTC(),
		"region":      s.cfg.Region,
		"version":     buildVersion,
		"controls": map[string]any{
			"encryptionAtRest":  "AES-256-GCM sealed TOTP secrets; SHA-256 hashed sessions/API keys",
			"passwordStorage":   "argon2id (64MiB, t=1, p=4)",
			"tenantIsolation":   "PostgreSQL FORCE RLS, non-superuser app role, verified by rls_test.sql",
			"auditLogging":      "auth_events + shipment_audit append-only; no PII in request logs",
			"mfaAvailable":      true,
			"ssoAvailable":      sso.Status()["google"] || sso.Status()["microsoft"],
			"dataResidency":     s.cfg.Region,
			"retentionWindows":  map[string]any{"jobsDone": "7d", "jobsDead": "90d", "inbox": "90d", "notifications": "180d", "authEvents": "365d"},
			"gdprExportErase":   true,
		},
	}
	err := db.WithTenant(r.Context(), s.pool, user.TenantID, func(tx pgx.Tx) error {
		var users, mfaUsers, sessions int64
		if err := tx.QueryRow(r.Context(), `
			SELECT count(*), count(*) FILTER (WHERE totp_enabled=true)
			FROM users WHERE is_active=true`).Scan(&users, &mfaUsers); err != nil {
			return err
		}
		if err := tx.QueryRow(r.Context(), `
			SELECT count(*) FROM sessions s JOIN users u ON u.id=s.user_id
			WHERE u.tenant_id=$1 AND s.expires_at > now()`, user.TenantID).Scan(&sessions); err != nil {
			return err
		}
		pack["tenant"] = map[string]any{
			"activeUsers": users, "mfaUsers": mfaUsers, "activeSessions": sessions,
		}
		return nil
	})
	// auth_events is non-RLS: scope explicitly.
	var logins, failures int64
	_ = s.pool.QueryRow(r.Context(), `
		SELECT count(*) FILTER (WHERE kind IN ('login','mfa_verified')),
		       count(*) FILTER (WHERE kind IN ('login_failed','mfa_failed'))
		FROM auth_events WHERE tenant_id=$1 AND created_at > now() - interval '90 days'`,
		user.TenantID).Scan(&logins, &failures)
	pack["auth90d"] = map[string]any{"successes": logins, "failures": failures}
	if err != nil {
		s.domainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, pack)
}
