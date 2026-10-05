package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/tracksphere/tracksphere/internal/auth"
	"github.com/tracksphere/tracksphere/internal/db"
	"github.com/tracksphere/tracksphere/internal/model"
)

type contextKey int

const (
	ctxUserKey      contextKey = iota
	ctxTenantKey
	ctxCSPNonceKey
)

const SessionCookie = "tracksphere_session"

func currentUser(r *http.Request) *model.User {
	u, _ := r.Context().Value(ctxUserKey).(*model.User)
	return u
}

func currentTenantID(r *http.Request) uuid.UUID {
	id, _ := r.Context().Value(ctxTenantKey).(uuid.UUID)
	return id
}

func getCSPNonce(r *http.Request) string {
	nonce, _ := r.Context().Value(ctxCSPNonceKey).(string)
	return nonce
}

// cspNonceMiddleware generates a CSP nonce and adds CSP headers.
func (s *Server) cspNonceMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Generate a random nonce for this request
		nonceBytes := make([]byte, 16)
		if _, err := rand.Read(nonceBytes); err != nil {
			s.log.Error("failed to generate CSP nonce", "err", err)
			next.ServeHTTP(w, r)
			return
		}
		nonce := base64.RawStdEncoding.EncodeToString(nonceBytes)

		ctx := context.WithValue(r.Context(), ctxCSPNonceKey, nonce)

		// Build CSP header with nonce
		csp := s.buildCSP(nonce)
		w.Header().Set("Content-Security-Policy", csp)

		// Also set other security headers
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Permissions-Policy", "geolocation=(), microphone=(), camera=()")

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) buildCSP(nonce string) string {
	// Base directives
	directives := []string{
		"default-src 'self'",
		"script-src 'self' 'nonce-" + nonce + "'",
		"style-src 'self' 'unsafe-inline'", // Tailwind needs unsafe-inline for @apply
		"img-src 'self' data: https:",
		"font-src 'self' data:",
		"connect-src 'self' wss: https:",
		"frame-ancestors 'self' https:",
		"base-uri 'self'",
		"form-action 'self'",
		"object-src 'none'",
		"frame-src 'self' https:",
	}

	// In development, allow unsafe-eval for React DevTools / hot reload
	if s.cfg.Env != "production" {
		directives[1] = "script-src 'self' 'nonce-" + nonce + "' 'unsafe-eval'"
	} else {
		directives = append(directives, "upgrade-insecure-requests")
	}

	// Add tenant logo origins if configured
	// This would be populated from tenant_branding table in a real implementation
	// For now, we allow https: for img-src to support tenant logos

	return strings.Join(directives, "; ")
}

// versioningMiddleware adds API version headers and handles deprecation
func (s *Server) versioningMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Set API version headers
		w.Header().Set("X-API-Version", "v1")
		w.Header().Set("X-API-Deprecated", "false")
		
		// Check for deprecated version in Accept-Version header
		acceptVersion := r.Header.Get("Accept-Version")
		if acceptVersion != "" && acceptVersion != "v1" {
			w.Header().Set("X-API-Deprecated", "true")
			w.Header().Set("Deprecation", "true")
			w.Header().Set("Sunset", "Sat, 01 Jan 2027 00:00:00 GMT")
			w.Header().Set("Link", "<https://api.tracksphere.io/api/v1>; rel=\"successor-version\"")
		}
		
		next.ServeHTTP(w, r)
	})
}

func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u := s.apiKeyUser(r); u != nil {
			ctx := context.WithValue(r.Context(), ctxUserKey, u)
			ctx = context.WithValue(ctx, ctxTenantKey, u.TenantID)
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}
		cookie, err := r.Cookie(SessionCookie)
		if err != nil || cookie.Value == "" {
			writeError(w, http.StatusUnauthorized, "unauthenticated", "Sign in required")
			return
		}

		user, err := s.sessionUser(r.Context(), cookie.Value)
		if err != nil || user == nil {
			http.SetCookie(w, &http.Cookie{
				Name: SessionCookie, Value: "", Path: "/",
				Expires: time.Unix(0, 0), HttpOnly: true, SameSite: http.SameSiteLaxMode,
			})
			writeError(w, http.StatusUnauthorized, "invalid_session", "Session expired, sign in again")
			return
		}

		ctx := context.WithValue(r.Context(), ctxUserKey, user)
		ctx = context.WithValue(ctx, ctxTenantKey, user.TenantID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) sessionUser(ctx context.Context, rawToken string) (*model.User, error) {
	hash := auth.HashToken(rawToken)
	var (
		u       model.User
		expires time.Time
		mfaPend bool
		active  bool
		totpOn  bool
		stale   bool
	)
	err := s.pool.QueryRow(ctx, `
		WITH touch AS (
		    UPDATE sessions SET last_seen_at = now()
		    WHERE token_hash = $1
		      AND (last_seen_at IS NULL OR last_seen_at < now() - interval '1 minute')
		    RETURNING 1
		)
		SELECT u.id, u.tenant_id, u.email, u.name, u.role, u.totp_enabled, u.theme,
		       u.is_active, u.created_at, s.expires_at, s.mfa_pending,
		       NOT EXISTS (SELECT 1 FROM touch)
		FROM sessions s
		JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = $1`, hash).
		Scan(&u.ID, &u.TenantID, &u.Email, &u.Name, &u.Role, &totpOn, &u.Theme,
			&active, &u.CreatedAt, &expires, &mfaPend, &stale)
	if err != nil {
		return nil, err
	}
	_ = stale
	if !active || mfaPend || time.Now().After(expires) {
		return nil, errInvalidSession
	}
	u.TOTPEnabled = totpOn
	return &u, nil
}

func (s *Server) cors(next http.Handler) http.Handler {
	allowed := map[string]bool{}
	for _, o := range s.cfg.CORSOrigins {
		allowed[o] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && allowed[origin] {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Max-Age", "600")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, PUT, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers",
				"Authorization, Content-Type, X-TrackSphere-Signature, Idempotency-Key, X-Shopify-Hmac-Sha256, X-WC-Webhook-Signature, X-Shop-Domain, X-WC-Shop")
			w.Header().Set("Access-Control-Expose-Headers", "Retry-After, X-Request-Id")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func trimTracking(s string) string {
	return strings.TrimSpace(s)
}

// querySystemRow runs a callback inside an explicit transaction that has the
// app.system bypass raised, for the two bootstrap lookups that cannot be
// tenant-pinned because the tenant is the thing being resolved (SSO subject ->
// user, Stripe subscription -> tenant).
//
// It replaces a helper with three defects, any one of which was fatal:
//
//  1. It acquired a pooled connection and ran set_config(..., true) on it in
//     autocommit mode. is_local only survives for the current transaction, so
//     the setting was discarded before the query ran — the bypass was never
//     active and both callers silently failed to resolve.
//  2. It released the connection before the caller invoked Scan, and pgx.Row is
//     lazy: the scan then happened on an arbitrary connection, possibly after
//     this one had been handed to another request.
//  3. "Fixing" it by flipping is_local to false would have been strictly worse:
//     the GUC would persist on a pooled connection and pass the RLS bypass to
//     whoever borrowed it next.
//
// Taking a callback keeps the Scan inside the transaction, which is the only
// shape that is actually correct here.
func (s *Server) querySystemRow(ctx context.Context, fn func(tx pgx.Tx) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if err := db.SetSystem(ctx, tx); err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}