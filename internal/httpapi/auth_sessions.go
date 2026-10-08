package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/tracksphere/tracksphere/internal/auth"
)

// auditEvent records a security-relevant event best-effort: it must never
// fail the request it describes. tenantID/userID may be nil for pre-auth
// failures (unknown email).
func (s *Server) auditEvent(ctx context.Context, tenantID, userID *uuid.UUID, email, kind string, r *http.Request) {
	_, _ = s.pool.Exec(ctx, `
		INSERT INTO auth_events (tenant_id, user_id, email, kind, ip, user_agent)
		VALUES ($1,$2,$3,$4,$5,$6)`,
		tenantID, userID, email, kind, s.clientIP(r), r.UserAgent())
}

type sessionView struct {
	ID        uuid.UUID `json:"id"`
	CreatedAt time.Time `json:"createdAt"`
	ExpiresAt time.Time `json:"expiresAt"`
	LastSeen  time.Time `json:"lastSeenAt"`
	IP        string    `json:"ip"`
	Current   bool      `json:"current"`
}

// handleListSessions GET /api/v1/auth/sessions — all active sessions for the
// caller, newest first. Token hashes never leave the server.
func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	var currentHash []byte
	if c, err := r.Cookie(SessionCookie); err == nil {
		currentHash = auth.HashToken(c.Value)
	}
	rows, err := s.pool.Query(r.Context(), `
		SELECT id, created_at, expires_at, last_seen_at, ip, token_hash
		FROM sessions WHERE user_id=$1 ORDER BY created_at DESC LIMIT 50`, user.ID)
	if err != nil {
		s.domainError(w, err)
		return
	}
	defer rows.Close()
	out := []sessionView{}
	for rows.Next() {
		var v sessionView
		var hash []byte
		if err := rows.Scan(&v.ID, &v.CreatedAt, &v.ExpiresAt, &v.LastSeen, &v.IP, &hash); err != nil {
			s.domainError(w, err)
			return
		}
		v.Current = string(hash) == string(currentHash)
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		s.domainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleRevokeSession DELETE /api/v1/auth/sessions/{id} — revoke one session.
// Foreign-user ids return 404 (no enumeration across users).
func (s *Server) handleRevokeSession(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(chiParam(r, "id"))
	if !ok {
		writeError(w, http.StatusBadRequest, "bad_id", "Invalid session id")
		return
	}
	user := currentUser(r)
	tag, err := s.pool.Exec(r.Context(),
		`DELETE FROM sessions WHERE id=$1 AND user_id=$2`, id, user.ID)
	if err != nil {
		s.domainError(w, err)
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "not_found", "Session not found")
		return
	}
	s.auditEvent(r.Context(), &user.TenantID, &user.ID, user.Email, "session_revoke", r)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

type changePasswordRequest struct {
	CurrentPassword string `json:"currentPassword"`
	NewPassword     string `json:"newPassword"`
}

// handleChangePassword POST /api/v1/auth/password/change — verify current,
// set new (min 8), revoke all OTHER sessions, audit. The caller stays signed
// in on the current session.
func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	var req changePasswordRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if len(req.NewPassword) < 8 {
		writeError(w, http.StatusBadRequest, "invalid_input", "New password must be at least 8 characters")
		return
	}
	user := currentUser(r)
	var passwordHash string
	if err := s.pool.QueryRow(r.Context(),
		`SELECT password_hash FROM users WHERE id=$1`, user.ID).Scan(&passwordHash); err != nil {
		s.domainError(w, err)
		return
	}
	if !auth.VerifyPassword(passwordHash, req.CurrentPassword) {
		writeError(w, http.StatusUnauthorized, "bad_credentials", "Current password is incorrect")
		return
	}
	newHash, err := auth.HashPassword(req.NewPassword)
	if err != nil {
		s.domainError(w, err)
		return
	}
	var currentHash []byte
	if c, cerr := r.Cookie(SessionCookie); cerr == nil {
		currentHash = auth.HashToken(c.Value)
	}
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		s.domainError(w, err)
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	if _, err := tx.Exec(r.Context(),
		`UPDATE users SET password_hash=$2, updated_at=now() WHERE id=$1`, user.ID, newHash); err != nil {
		s.domainError(w, err)
		return
	}
	// Revoke everything except the session performing the change.
	if _, err := tx.Exec(r.Context(),
		`DELETE FROM sessions WHERE user_id=$1 AND token_hash <> $2`, user.ID, currentHash); err != nil {
		s.domainError(w, err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		s.domainError(w, err)
		return
	}
	s.auditEvent(r.Context(), &user.TenantID, &user.ID, user.Email, "password_change", r)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
