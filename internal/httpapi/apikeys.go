package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"strings"
	"time"

	"github.com/tracksphere/tracksphere/internal/db"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/tracksphere/tracksphere/internal/model"
)

// apiKeyPrefix identifies a key in UIs/logs without exposing it.
const apiKeyPrefixLen = 8

// newAPIKey mints a raw tsk_ token, its SHA-256 hash and display prefix.
func newAPIKey() (raw string, hash []byte, prefix string, err error) {
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return "", nil, "", err
	}
	raw = "tsk_" + base64.RawURLEncoding.EncodeToString(b)
	sum := sha256.Sum256([]byte(raw))
	prefix = raw[:4+apiKeyPrefixLen] // tsk_ + 8 chars
	return raw, sum[:], prefix, nil
}

// apiKeyUser authenticates `Authorization: Bearer tsk_...`. Returns nil when
// the header is absent/invalid (caller falls through to 401). Revoked and
// unknown keys are indistinguishable (uniform nil).
//
// The last_used_at stamp is a conditional UPDATE in the same statement, so it
// costs at most one write per minute per key instead of one per API call.
func (s *Server) apiKeyUser(r *http.Request) *model.User {
	header := r.Header.Get("Authorization")
	if header == "" || !strings.HasPrefix(strings.ToLower(header), "bearer ") {
		return nil
	}
	raw := strings.TrimSpace(header[len("Bearer "):])
	if !strings.HasPrefix(raw, "tsk_") {
		return nil
	}
	sum := sha256.Sum256([]byte(raw))

	// api_keys is RLS-protected (000015). Resolving a key by its hash happens
	// before any tenant is known — that is the entire point of a bearer key — so
	// this read legitimately needs the system flag. It reads exactly one row by
	// a 256-bit hash, so the system branch cannot enumerate anything.
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := db.SetSystem(ctx, tx); err != nil {
		return nil
	}
	var (
		u        model.User
		ownerID  *uuid.UUID
		prefix   string
		revoked  *time.Time
		email    string
		userName string
	)
	err = tx.QueryRow(ctx, `
		WITH touch AS (
		    UPDATE api_keys SET last_used_at = now()
		    WHERE key_hash = $1
		      AND (last_used_at IS NULL OR last_used_at < now() - interval '1 minute')
		    RETURNING 1
		)
		SELECT ak.tenant_id, ak.created_by, ak.role, ak.prefix,
		       ak.revoked_at, COALESCE(u.email, ''), COALESCE(u.name, '')
		FROM api_keys ak LEFT JOIN users u ON u.id = ak.created_by
		WHERE ak.key_hash = $1`, sum[:]).
		Scan(&u.TenantID, &ownerID, &u.Role, &prefix, &revoked, &email, &userName)
	if err != nil || revoked != nil {
		return nil // unknown or revoked: uniform nil, no enumeration
	}
	if err := tx.Commit(ctx); err != nil {
		return nil
	}
	if ownerID != nil {
		u.ID = *ownerID
	}
	u.Email = email
	u.Name = userName
	if u.Email == "" {
		u.Email = "api-key@" + prefix
	}
	u.CreatedAt = time.Now()
	return &u
}

type createKeyRequest struct {
	Name string `json:"name"`
	Role string `json:"role"`
}

// handleCreateAPIKey POST /api/v1/apikeys — mint a tenant API key.
// The raw token is returned ONCE; only its hash is stored.
func (s *Server) handleCreateAPIKey(w http.ResponseWriter, r *http.Request) {
	var req createKeyRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		writeError(w, http.StatusBadRequest, "invalid_input", "name is required")
		return
	}
	role := req.Role
	if role == "" {
		role = RoleMember
	}
	if roleRank(role) == 0 {
		writeError(w, http.StatusBadRequest, "invalid_input", "unknown role")
		return
	}
	// Only owners/admins may mint keys at or below their own rank.
	me := currentUser(r)
	if roleRank(role) > roleRank(me.Role) {
		writeError(w, http.StatusForbidden, "forbidden", "Cannot mint a key above your role")
		return
	}
	raw, hash, prefix, err := newAPIKey()
	if err != nil {
		s.domainError(w, err)
		return
	}
	var id uuid.UUID
	// api_keys is RLS-protected (000015): the insert needs the tenant pinned or
	// WITH CHECK rejects it.
	err = db.WithTenant(r.Context(), s.pool, me.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(r.Context(), `
			INSERT INTO api_keys (tenant_id, created_by, name, prefix, key_hash, role)
			VALUES ($1,$2,$3,$4,$5,$6) RETURNING id`,
			me.TenantID, me.ID, strings.TrimSpace(req.Name), prefix, hash, role).Scan(&id)
	})
	if err != nil {
		s.domainError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"id": id, "prefix": prefix, "role": role, "key": raw,
		"warning": "Copy the key now — it is never shown again",
	})
}

// handleListAPIKeys GET /api/v1/apikeys
func (s *Server) handleListAPIKeys(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	type view struct {
		ID       uuid.UUID  `json:"id"`
		Name     string     `json:"name"`
		Prefix   string     `json:"prefix"`
		Role     string     `json:"role"`
		Revoked  bool       `json:"revoked"`
		LastUsed *time.Time `json:"lastUsedAt,omitempty"`
		Created  time.Time  `json:"createdAt"`
	}
	out := []view{}
	// api_keys is RLS-protected (000015); this read needs the tenant pinned.
	err := db.WithTenant(r.Context(), s.pool, user.TenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `
			SELECT id, name, prefix, role, revoked_at IS NOT NULL AS revoked,
			       last_used_at, created_at
			FROM api_keys ORDER BY created_at DESC LIMIT 100`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var v view
			if err := rows.Scan(&v.ID, &v.Name, &v.Prefix, &v.Role, &v.Revoked, &v.LastUsed, &v.Created); err != nil {
				return err
			}
			out = append(out, v)
		}
		return rows.Err()
	})
	if err != nil {
		s.domainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleRevokeAPIKey POST /api/v1/apikeys/{id}/revoke
func (s *Server) handleRevokeAPIKey(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(chiParam(r, "id"))
	if !ok {
		writeError(w, http.StatusBadRequest, "bad_id", "Invalid key id")
		return
	}
	user := currentUser(r)
	var revoked bool
	err := db.WithTenant(r.Context(), s.pool, user.TenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(r.Context(),
			`UPDATE api_keys SET revoked_at=now() WHERE id=$1 AND revoked_at IS NULL`, id)
		if err != nil {
			return err
		}
		revoked = tag.RowsAffected() > 0
		return nil
	})
	if err != nil {
		s.domainError(w, err)
		return
	}
	if !revoked {
		writeError(w, http.StatusNotFound, "not_found", "API key not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
