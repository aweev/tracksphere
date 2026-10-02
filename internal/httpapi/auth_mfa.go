package httpapi

import (
	"net/http"

	"github.com/tracksphere/tracksphere/internal/auth"
)

// handleMe returns the current session's user (already loaded by requireAuth).
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"user": currentUser(r)})
}

// handleLogout revokes the current session and clears the cookie.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(SessionCookie); err == nil && cookie.Value != "" {
		_, _ = s.pool.Exec(r.Context(),
			`DELETE FROM sessions WHERE token_hash=$1`, auth.HashToken(cookie.Value))
	}
	http.SetCookie(w, &http.Cookie{
		Name: SessionCookie, Value: "", Path: "/",
		MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleMFAEnroll generates a TOTP secret, stores it sealed but DISABLED,
// and returns the provisioning URI for the authenticator app.
func (s *Server) handleMFAEnroll(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	secret, err := auth.NewTOTPSecret()
	if err != nil {
		s.domainError(w, err)
		return
	}
	sealed, err := auth.Seal(s.cfg.SecretKey, []byte(secret))
	if err != nil {
		s.domainError(w, err)
		return
	}
	_, err = s.pool.Exec(r.Context(), `
		UPDATE users SET totp_secret=$2, totp_enabled=false, updated_at=now()
		WHERE id=$1`, user.ID, sealed)
	if err != nil {
		s.domainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"secret":     secret,
		"otpauthURI": auth.TOTPProvisioningURI(secret, user.Email),
	})
}

type mfaCodeRequest struct {
	Code string `json:"code"`
}

// handleMFAEnable confirms a code against the stored secret and turns MFA on.
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
	plain, err := auth.Open(s.cfg.SecretKey, *sealed)
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

// handleMFADisable turns MFA off (requires a valid current code).
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
	plain, err := auth.Open(s.cfg.SecretKey, *sealed)
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
