package httpapi

import (
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/tracksphere/tracksphere/internal/auth"
	"github.com/tracksphere/tracksphere/internal/model"
)

// Team management (admin+). Invites are explicit-password for P1 (no mail
// provider yet): the response carries a one-time password the admin passes
// along; the member changes it on first login. Email delivery plugs in here.

// handleListTeam GET /api/v1/team
func (s *Server) handleListTeam(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	rows, err := s.pool.Query(r.Context(), `
		SELECT id, email, name, role, totp_enabled, is_active, created_at
		FROM users WHERE tenant_id=$1 ORDER BY created_at ASC LIMIT 100`, user.TenantID)
	if err != nil {
		s.domainError(w, err)
		return
	}
	defer rows.Close()
	type memberView struct {
		model.User
		Active bool `json:"active"`
	}
	out := []memberView{}
	for rows.Next() {
		var u memberView
		if err := rows.Scan(&u.ID, &u.Email, &u.Name, &u.Role, &u.TOTPEnabled, &u.Active, &u.CreatedAt); err != nil {
			s.domainError(w, err)
			return
		}
		u.TenantID = user.TenantID
		out = append(out, u)
	}
	if err := rows.Err(); err != nil {
		s.domainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

type inviteRequest struct {
	Email string `json:"email"`
	Name  string `json:"name"`
	Role  string `json:"role"`
}

// handleInvite POST /api/v1/team/invite
func (s *Server) handleInvite(w http.ResponseWriter, r *http.Request) {
	var req inviteRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	email := lowerTrim(req.Email)
	if email == "" || !strings.Contains(email, "@") {
		writeError(w, http.StatusBadRequest, "invalid_input", "valid email required")
		return
	}
	role := req.Role
	if role == "" {
		role = RoleMember
	}
	if role != RoleMember && role != RoleAdmin {
		writeError(w, http.StatusBadRequest, "invalid_input", "role must be member|admin")
		return
	}
	me := currentUser(r)
	if roleRank(role) >= roleRank(me.Role) && me.Role != RoleOwner {
		writeError(w, http.StatusForbidden, "forbidden", "Only owners can invite admins")
		return
	}
	// One-time password, shown once.
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		s.domainError(w, err)
		return
	}
	temp := "Tsp-" + base64.RawURLEncoding.EncodeToString(b)
	hash, err := auth.HashPassword(temp)
	if err != nil {
		s.domainError(w, err)
		return
	}
	var id uuid.UUID
	err = s.pool.QueryRow(r.Context(), `
		INSERT INTO users (tenant_id, email, name, password_hash, role)
		VALUES ($1,$2,$3,$4,$5) RETURNING id`,
		me.TenantID, email, strings.TrimSpace(req.Name), hash, role).Scan(&id)
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "email_taken", "An account with this email already exists")
			return
		}
		s.domainError(w, err)
		return
	}
	s.auditEvent(r.Context(), &me.TenantID, &me.ID, email, "register", r)
	writeJSON(w, http.StatusCreated, map[string]any{
		"id": id, "email": email, "role": role,
		"tempPassword": temp,
		"warning":      "Share this password once — it is never shown again",
	})
}

// handleDeactivate POST /api/v1/team/{id}/deactivate — also revokes sessions.
func (s *Server) handleDeactivate(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(chiParam(r, "id"))
	if !ok {
		writeError(w, http.StatusBadRequest, "bad_id", "Invalid user id")
		return
	}
	me := currentUser(r)
	if id == me.ID {
		writeError(w, http.StatusBadRequest, "invalid_input", "Cannot deactivate yourself")
		return
	}
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		s.domainError(w, err)
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	tag, err := tx.Exec(r.Context(),
		`UPDATE users SET is_active=false, updated_at=now()
		 WHERE id=$1 AND tenant_id=$2`, id, me.TenantID)
	if err != nil {
		s.domainError(w, err)
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "not_found", "User not found")
		return
	}
	_, _ = tx.Exec(r.Context(), `DELETE FROM sessions WHERE user_id=$1`, id)
	if err := tx.Commit(r.Context()); err != nil {
		s.domainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
