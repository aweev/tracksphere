package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"net/http"
	"strings"

	"github.com/tracksphere/tracksphere/internal/auth"
)

// Recovery codes are the lockout lifeline for MFA: 10 single-use codes shown
// once at issuance, stored as SHA-256 hashes (80-bit entropy each — hashing,
// not encryption, is correct here). Without them every lost authenticator is
// a support ticket with no self-serve path.

// handleMFARecoveryCodes POST /api/v1/auth/mfa/recovery-codes — issue a fresh
// set of 10 codes. Requires an enrolled factor; revokes prior unused codes so
// only one active set exists. The plaintext codes are returned exactly once.
func (s *Server) handleMFARecoveryCodes(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	var enabled bool
	if err := s.pool.QueryRow(r.Context(),
		`SELECT totp_enabled FROM users WHERE id=$1`, user.ID).Scan(&enabled); err != nil {
		s.domainError(w, err)
		return
	}
	if !enabled {
		writeError(w, http.StatusBadRequest, "not_enrolled", "Enable MFA before issuing recovery codes")
		return
	}

	codes := make([]string, 0, 10)
	hashes := make([][]byte, 0, 10)
	for i := 0; i < 10; i++ {
		raw := make([]byte, 10)
		if _, err := rand.Read(raw); err != nil {
			s.domainError(w, err)
			return
		}
		code := strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw))
		code = code[0:4] + "-" + code[4:8] + "-" + code[8:12] + "-" + code[12:16]
		codes = append(codes, code)
		hashes = append(hashes, auth.HashToken(code))
	}

	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		s.domainError(w, err)
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	// One active set: revoke prior unused codes before issuing.
	if _, err := tx.Exec(r.Context(),
		`DELETE FROM mfa_recovery_codes WHERE user_id=$1 AND used_at IS NULL`, user.ID); err != nil {
		s.domainError(w, err)
		return
	}
	for _, h := range hashes {
		if _, err := tx.Exec(r.Context(),
			`INSERT INTO mfa_recovery_codes (user_id, code_hash) VALUES ($1,$2)`, user.ID, h); err != nil {
			s.domainError(w, err)
			return
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		s.domainError(w, err)
		return
	}
	s.auditEvent(r.Context(), &user.TenantID, &user.ID, user.Email, "mfa_recovery_issued", r)
	writeJSON(w, http.StatusOK, map[string]any{"codes": codes})
}

// consumeRecoveryCode marks one unused code as used. Returns true on success.
// Constant-shape query: unknown code vs used code both return false (no oracle).
func (s *Server) consumeRecoveryCode(ctx context.Context, userID any, code string) bool {
	var ok bool
	_ = s.pool.QueryRow(ctx, `
		UPDATE mfa_recovery_codes SET used_at=now()
		WHERE user_id=$1 AND code_hash=$2 AND used_at IS NULL
		RETURNING true`, userID, auth.HashToken(normalizeRecoveryCode(code))).Scan(&ok)
	return ok
}

func normalizeRecoveryCode(code string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(code), " ", ""))
}
