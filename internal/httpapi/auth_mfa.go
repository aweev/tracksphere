package httpapi

import (
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

func (s *Server) handleMFAEnroll(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
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