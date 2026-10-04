package httpapi

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/tracksphere/tracksphere/internal/auth"
	"github.com/tracksphere/tracksphere/internal/model"
)

// lowerTrim normalizes an email input.
func lowerTrim(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// handleLogin authenticates credentials. With MFA enabled the response is
// {mfaRequired:true, challenge} and NO cookie is set until MFA verification.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	req.Email = lowerTrim(req.Email)

	var (
		user         model.User
		passwordHash string
		totpSecret   *string
		active       bool
	)
	err := s.pool.QueryRow(r.Context(), `
		SELECT id, tenant_id, email, name, role, password_hash, totp_secret,
		       totp_enabled, is_active, created_at
		FROM users WHERE lower(email)=$1`, req.Email).
		Scan(&user.ID, &user.TenantID, &user.Email, &user.Name, &user.Role,
			&passwordHash, &totpSecret, &user.TOTPEnabled, &active, &user.CreatedAt)
	// Uniform error for unknown email vs bad password (no user enumeration).
	if err != nil || !active || !auth.VerifyPassword(passwordHash, req.Password) {
		s.auditEvent(r.Context(), nil, nil, req.Email, "login_failed", r)
		writeError(w, http.StatusUnauthorized, "bad_credentials", "Invalid email or password")
		return
	}

	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		s.domainError(w, err)
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()

	// MFA pending → challenge token, no cookie yet.
	if user.TOTPEnabled && totpSecret != nil {
		challenge, err := s.issueSession(r.Context(), tx, user, user.TenantID, true, r)
		if err != nil {
			s.domainError(w, err)
			return
		}
		if err := tx.Commit(r.Context()); err != nil {
			s.domainError(w, err)
			return
		}
		s.auditEvent(r.Context(), &user.TenantID, &user.ID, user.Email, "mfa_challenge", r)
		writeJSON(w, http.StatusOK, map[string]any{
			"mfaRequired": true,
			"challenge":   challenge,
		})
		return
	}

	token, err := s.issueSession(r.Context(), tx, user, user.TenantID, false, r)
	if err != nil {
		s.domainError(w, err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		s.domainError(w, err)
		return
	}
	s.auditEvent(r.Context(), &user.TenantID, &user.ID, user.Email, "login", r)
	s.setSessionCookie(w, token)
	writeJSON(w, http.StatusOK, map[string]any{"user": user})
}

type mfaVerifyRequest struct {
	Challenge string `json:"challenge"`
	Code      string `json:"code"`
}

// handleMFAVerify upgrades an MFA-pending challenge into a full session.
func (s *Server) handleMFAVerify(w http.ResponseWriter, r *http.Request) {
	var req mfaVerifyRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	// The password flow returns the challenge in the response body; the SSO
	// flow cannot (it is a browser redirect), so it delivers it in an HttpOnly
	// cookie scoped to this endpoint. Accept either, never both.
	if req.Challenge == "" {
		if c, cerr := r.Cookie(MFAChallengeCookie); cerr == nil {
			req.Challenge = c.Value
		}
	}
	if req.Challenge == "" || req.Code == "" {
		writeError(w, http.StatusBadRequest, "invalid_input", "challenge and code are required")
		return
	}
	s.clearMFAChallengeCookie(w)

	hash := auth.HashToken(req.Challenge)
	var (
		user         model.User
		mfaPending   bool
		expires      time.Time
		secretSealed string
	)
	err := s.pool.QueryRow(r.Context(), `
		SELECT u.id, u.tenant_id, u.email, u.name, u.role, u.totp_enabled,
		       u.totp_secret, s.mfa_pending, s.expires_at
		FROM sessions s JOIN users u ON u.id=s.user_id
		WHERE s.token_hash=$1`, hash).
		Scan(&user.ID, &user.TenantID, &user.Email, &user.Name, &user.Role,
			&user.TOTPEnabled, &secretSealed, &mfaPending, &expires)
	if err != nil || !mfaPending || time.Now().After(expires) || secretSealed == "" {
		writeError(w, http.StatusUnauthorized, "bad_challenge", "Challenge invalid or expired")
		return
	}

	secretBytes, err := auth.OpenMulti(s.cfg.SecretKeys, secretSealed)
	if err != nil || !auth.VerifyTOTP(string(secretBytes), req.Code) {
		s.auditEvent(r.Context(), &user.TenantID, &user.ID, user.Email, "mfa_failed", r)
		writeError(w, http.StatusUnauthorized, "bad_code", "Incorrect verification code")
		return
	}

	// Consume the challenge (one-time use), mint the real session.
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		s.domainError(w, err)
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	if _, err := tx.Exec(r.Context(),
		`DELETE FROM sessions WHERE token_hash=$1`, hash); err != nil {
		s.domainError(w, err)
		return
	}
	token, err := s.issueSession(r.Context(), tx, user, user.TenantID, false, r)
	if err != nil {
		s.domainError(w, err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		s.domainError(w, err)
		return
	}
	s.auditEvent(r.Context(), &user.TenantID, &user.ID, user.Email, "mfa_verified", r)
	s.setSessionCookie(w, token)
	writeJSON(w, http.StatusOK, map[string]any{"user": user})
}

// issueSession inserts a session row inside tx and returns the raw token.
// mfaPending=true sessions become challenge tokens only (never cookies).
func (s *Server) issueSession(ctx context.Context, tx pgx.Tx, user model.User, tenantID uuid.UUID, mfaPending bool, r *http.Request) (string, error) {
	raw, hash, err := auth.NewSessionToken()
	if err != nil {
		return "", err
	}
	ttl := s.cfg.SessionTTL
	if mfaPending {
		ttl = 5 * time.Minute // short-lived MFA challenge
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO sessions (user_id, tenant_id, token_hash, mfa_pending, expires_at, ip, user_agent)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		user.ID, tenantID, hash, mfaPending, time.Now().Add(ttl),
		clientIP(r), r.UserAgent())
	if err != nil {
		return "", err
	}
	return raw, nil
}

// clientIP extracts the caller address for the audit trail.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		return xff
	}
	return r.RemoteAddr
}

// setSessionCookie attaches the session cookie (Secure in production).
func (s *Server) setSessionCookie(w http.ResponseWriter, token string) {
	secure := s.cfg.Env == "production"
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    token,
		Path:     "/",
		Expires:  time.Now().Add(s.cfg.SessionTTL),
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}