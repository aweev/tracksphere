package httpapi

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/tracksphere/tracksphere/internal/auth"
)

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"user": currentUser(r)})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(SessionCookie); err == nil && cookie.Value != "" {
		_, _ = s.pool.Exec(r.Context(),
			`DELETE FROM sessions WHERE token_hash=$1`, auth.HashToken(cookie.Value))
	}
	if u := currentUser(r); u != nil {
		s.auditEvent(r.Context(), &u.TenantID, &u.ID, u.Email, "logout", r)
	}
	http.SetCookie(w, &http.Cookie{
		Name: SessionCookie, Value: "", Path: "/",
		MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// MFAChallengeCookie carries the MFA challenge for browser-redirect flows (SSO),
// where there is no JSON response in which to return a challenge token.
//
// It is HttpOnly so the token is never reachable from script — strictly better
// than the password flow, which hands the challenge to the client in JSON —
// and Path-scoped so it is only ever transmitted to the verify endpoint. The
// server-side session row still carries the 5-minute expiry, so the cookie
// cannot outlive the challenge it names.
const MFAChallengeCookie = "tracksphere_mfa"

func (s *Server) setMFAChallengeCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     MFAChallengeCookie,
		Value:    token,
		Path:     "/api/v1/auth/mfa",
		MaxAge:   300,
		HttpOnly: true,
		Secure:   s.cfg.Env == "production",
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) clearMFAChallengeCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     MFAChallengeCookie,
		Value:    "",
		Path:     "/api/v1/auth/mfa",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   s.cfg.Env == "production",
		SameSite: http.SameSiteLaxMode,
	})
}

type mfaEnrollRequest struct {
	// Either the current TOTP code or the account password is required when
	// replacing an already-active factor.
	Password string `json:"password"`
	Code     string `json:"code"`
}

// handleMFAEnroll issues a fresh TOTP secret.
//
// Re-authentication is mandatory when the account already has an active factor.
// This endpoint used to overwrite totp_secret and set totp_enabled=false with
// nothing but a valid session, so a stolen session cookie — or any XSS — could
// silently strip MFA from a fully protected account. The downgrade completed
// silently because every later session check reads totp_enabled, finds it
// false, and accepts the attacker's session with no second factor.
//
// Note the new secret is always left disabled: replacing a factor must not let
// a new one become authoritative until the caller has proven they hold it,
// which is what /auth/mfa/enable is for.
func (s *Server) handleMFAEnroll(w http.ResponseWriter, r *http.Request) {
	var req mfaEnrollRequest
	// Tolerate an empty body. Enrollment has no required fields when the
	// account has no active factor, and decodeJSON rejects EOF, so using it
	// here would 400 every existing bodiless enroll call.
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil &&
		err != io.EOF {
		writeError(w, http.StatusBadRequest, "bad_json", "Invalid request body")
		return
	}
	user := currentUser(r)

	var (
		passwordHash string
		totpEnabled  bool
		secretSealed *string
	)
	if err := s.pool.QueryRow(r.Context(),
		`SELECT password_hash, totp_enabled, totp_secret FROM users WHERE id=$1`,
		user.ID).Scan(&passwordHash, &totpEnabled, &secretSealed); err != nil {
		s.domainError(w, err)
		return
	}

	if totpEnabled {
		proven := false
		if req.Code != "" && secretSealed != nil && *secretSealed != "" {
			if plain, derr := auth.OpenMulti(s.cfg.SecretKeys, *secretSealed); derr == nil {
				proven = auth.VerifyTOTP(string(plain), req.Code)
			}
		}
		if !proven && req.Password != "" {
			proven = auth.VerifyPassword(passwordHash, req.Password)
		}
		if !proven {
			s.auditEvent(r.Context(), &user.TenantID, &user.ID, user.Email,
				"mfa_enroll_denied", r)
			writeError(w, http.StatusUnauthorized, "reauth_required",
				"Confirm your current verification code or password to replace MFA")
			return
		}
	}

	secret, err := auth.NewTOTPSecret()
	if err != nil {
		s.domainError(w, err)
		return
	}
	sealed, err := auth.SealMulti(s.cfg.SecretKeys, []byte(secret))
	if err != nil {
		s.domainError(w, err)
		return
	}

	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		s.domainError(w, err)
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()

	if _, err := tx.Exec(r.Context(), `
		UPDATE users SET totp_secret=$2, totp_enabled=false, updated_at=now()
		WHERE id=$1`, user.ID, sealed); err != nil {
		s.domainError(w, err)
		return
	}
	// Changing a factor invalidates every other session: a credential an
	// attacker may already hold must not survive the rotation.
	if _, err := tx.Exec(r.Context(),
		`DELETE FROM sessions WHERE user_id=$1`, user.ID); err != nil {
		s.domainError(w, err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		s.domainError(w, err)
		return
	}
	s.auditEvent(r.Context(), &user.TenantID, &user.ID, user.Email, "mfa_enrolled", r)

	writeJSON(w, http.StatusOK, map[string]any{
		"secret":     secret,
		"otpauthURI": auth.TOTPProvisioningURI(secret, user.Email),
	})
}

type mfaCodeRequest struct {
	Code string `json:"code"`
}

func (s *Server) handleMFAEnable(w http.ResponseWriter, r *http.Request) {
	var req mfaCodeRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	user := currentUser(r)
	var sealed *string
	err := s.pool.QueryRow(r.Context(),
		`SELECT totp_secret FROM users WHERE id=$1`, user.ID).Scan(&sealed)
	if err != nil || sealed == nil || *sealed == "" {
		writeError(w, http.StatusBadRequest, "not_enrolled", "Run MFA enrollment first")
		return
	}
	plain, err := auth.OpenMulti(s.cfg.SecretKeys, *sealed)
	if err != nil || !auth.VerifyTOTP(string(plain), req.Code) {
		writeError(w, http.StatusUnauthorized, "bad_code", "Incorrect verification code")
		return
	}
	if _, err := s.pool.Exec(r.Context(),
		`UPDATE users SET totp_enabled=true, updated_at=now() WHERE id=$1`, user.ID); err != nil {
		s.domainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"totpEnabled": true})
}

func (s *Server) handleMFADisable(w http.ResponseWriter, r *http.Request) {
	var req mfaCodeRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	user := currentUser(r)
	var sealed *string
	err := s.pool.QueryRow(r.Context(),
		`SELECT totp_secret FROM users WHERE id=$1`, user.ID).Scan(&sealed)
	if err != nil || sealed == nil || *sealed == "" {
		writeError(w, http.StatusBadRequest, "not_enrolled", "MFA is not enrolled")
		return
	}
	plain, err := auth.OpenMulti(s.cfg.SecretKeys, *sealed)
	if err != nil || !auth.VerifyTOTP(string(plain), req.Code) {
		writeError(w, http.StatusUnauthorized, "bad_code", "Incorrect verification code")
		return
	}
	_, err = s.pool.Exec(r.Context(), `
		UPDATE users SET totp_enabled=false, totp_secret=NULL, updated_at=now()
		WHERE id=$1`, user.ID)
	if err != nil {
		s.domainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"totpEnabled": false})
}