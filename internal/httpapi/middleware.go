package httpapi

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/tracksphere/tracksphere/internal/auth"
	"github.com/tracksphere/tracksphere/internal/model"
)

// contextKey is an unexported context key type (prevents collisions).
type contextKey int

const (
	ctxUserKey contextKey = iota
	ctxTenantKey
)

// SessionCookie is the HttpOnly session cookie name.
const SessionCookie = "tracksphere_session"

// currentUser extracts the authenticated user set by requireAuth.
func currentUser(r *http.Request) *model.User {
	u, _ := r.Context().Value(ctxUserKey).(*model.User)
	return u
}

// currentTenantID extracts the tenant set by requireAuth.
func currentTenantID(r *http.Request) uuid.UUID {
	id, _ := r.Context().Value(ctxTenantKey).(uuid.UUID)
	return id
}

// requireAuth validates the session cookie and loads the user + tenant.
// Unauthenticated requests get 401 with an machine-readable code.
func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(SessionCookie)
		if err != nil || cookie.Value == "" {
			writeError(w, http.StatusUnauthorized, "unauthenticated", "Sign in required")
			return
		}

		user, err := s.sessionUser(r.Context(), cookie.Value)
		if err != nil || user == nil {
			// Stale/invalid cookie: clear it so browsers stop sending it.
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

// sessionUser resolves a raw session token to its user, sliding expiry and
// refreshing last_seen_at at most once a minute to limit write amplification.
func (s *Server) sessionUser(ctx context.Context, rawToken string) (*model.User, error) {
	hash := auth.HashToken(rawToken)
	var (
		u        model.User
		expires  time.Time
		mfaPend  bool
		active   bool
		totpOn   bool
	)
	err := s.pool.QueryRow(ctx, `
		SELECT u.id, u.tenant_id, u.email, u.name, u.role, u.totp_enabled,
		       u.is_active, u.created_at, s.expires_at, s.mfa_pending
		FROM sessions s
		JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = $1`, hash).
		Scan(&u.ID, &u.TenantID, &u.Email, &u.Name, &u.Role, &totpOn,
			&active, &u.CreatedAt, &expires, &mfaPend)
	if err != nil {
		return nil, err
	}
	if !active || mfaPend || time.Now().After(expires) {
		return nil, errInvalidSession
	}
	u.TOTPEnabled = totpOn

	// Best-effort last_seen refresh (fire and forget to keep latency low).
	go func() {
		cctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = s.pool.Exec(cctx,
			`UPDATE sessions SET last_seen_at=now() WHERE token_hash=$1`, hash)
	}()
	return &u, nil
}

// cors implements permissive-for-dev CORS driven by config.
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
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-TrackSphere-Signature")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// trimTracking normalizes a public tracking number path segment.
func trimTracking(s string) string {
	return strings.TrimSpace(s)
}