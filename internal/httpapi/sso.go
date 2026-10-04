package httpapi

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/tracksphere/tracksphere/internal/auth"
	"github.com/tracksphere/tracksphere/internal/db"
	"github.com/tracksphere/tracksphere/internal/model"
	"github.com/tracksphere/tracksphere/internal/sso"
)

// ssoStates tracks in-flight OAuth state tokens (CSRF). In-memory is fine:
// a dropped state only fails that login, and instances share nothing else
// security-critical here.
var ssoStates = struct {
	sync.Mutex
	m map[string]time.Time
}{m: map[string]time.Time{}}

func newSSOState() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	state := hex.EncodeToString(b)
	ssoStates.Lock()
	ssoStates.m[state] = time.Now().Add(10 * time.Minute)
	// Opportunistic cleanup.
	for k, exp := range ssoStates.m {
		if time.Now().After(exp) {
			delete(ssoStates.m, k)
		}
	}
	ssoStates.Unlock()
	return state
}

func consumeSSOState(state string) bool {
	ssoStates.Lock()
	defer ssoStates.Unlock()
	exp, ok := ssoStates.m[state]
	if !ok || time.Now().After(exp) {
		return false
	}
	delete(ssoStates.m, state)
	return true
}

// handleSSOStatus GET /api/v1/auth/sso/status — which providers are live.
func (s *Server) handleSSOStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, sso.Status())
}

// handleSSOStart GET /api/v1/auth/sso/{provider} — redirect to the IdP.
func (s *Server) handleSSOStart(w http.ResponseWriter, r *http.Request) {
	p := sso.Get(chiParam(r, "provider"))
	if p == nil {
		writeError(w, http.StatusNotFound, "unknown_provider", "SSO provider not configured")
		return
	}
	state := newSSOState()
	http.SetCookie(w, &http.Cookie{
		Name: "tracksphere_oauth_state", Value: state, Path: "/",
		MaxAge: 600, HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, p.StartURL(state), http.StatusFound)
}

// handleSSOCallback GET /api/v1/auth/sso/{provider}/callback
func (s *Server) handleSSOCallback(w http.ResponseWriter, r *http.Request) {
	p := sso.Get(chiParam(r, "provider"))
	if p == nil {
		writeError(w, http.StatusNotFound, "unknown_provider", "SSO provider not configured")
		return
	}
	q := r.URL.Query()
	if q.Get("error") != "" {
		writeError(w, http.StatusUnauthorized, "sso_denied", "Identity provider denied the login")
		return
	}
	state := q.Get("state")
	cookie, _ := r.Cookie("tracksphere_oauth_state")
	if state == "" || cookie == nil || cookie.Value != state || !consumeSSOState(state) {
		writeError(w, http.StatusBadRequest, "bad_state", "Login session expired, try again")
		return
	}
	id, err := p.Exchange(r.Context(), q.Get("code"))
	if err != nil {
		s.log.Warn("sso exchange failed", "provider", p.Slug, "err", err)
		writeError(w, http.StatusUnauthorized, "sso_failed", "Could not verify with the identity provider")
		return
	}
	if !id.EmailVerified || id.Email == "" {
		writeError(w, http.StatusUnauthorized, "sso_unverified", "Verified email required for SSO")
		return
	}

	// 1. Known subject → its user (no email lookup: subject pinned at link).
	//
	// sso_accounts became RLS-protected in 000015. Resolving an SSO subject is
	// inherently a pre-tenant lookup — the caller is not authenticated yet — so
	// this runs under the system flag and reads exactly one row keyed on
	// (provider, subject). The alternative, joining users inside a pinned
	// transaction, is impossible because the tenant is the thing being resolved.
	var user model.User
	err = s.querySystemRow(r.Context(), func(tx pgx.Tx) error {
		return tx.QueryRow(r.Context(), `
		SELECT u.id, u.tenant_id, u.email, u.name, u.role, u.totp_enabled, u.created_at
		FROM sso_accounts a JOIN users u ON u.id = a.user_id
		WHERE a.provider=$1 AND a.subject=$2 AND u.is_active=true`,
			id.Provider, id.Subject).Scan(
			&user.ID, &user.TenantID, &user.Email, &user.Name,
			&user.Role, &user.TOTPEnabled, &user.CreatedAt)
	})
	if err == nil {
		s.finishSSOLogin(w, r, user, id)
		return
	}

	// 2. Verified-email match → link subject, then login (first link only).
	err = s.pool.QueryRow(r.Context(), `
		SELECT id, tenant_id, email, name, role, totp_enabled, created_at
		FROM users WHERE lower(email)=$1 AND is_active=true`,
		id.Email).Scan(
		&user.ID, &user.TenantID, &user.Email, &user.Name,
		&user.Role, &user.TOTPEnabled, &user.CreatedAt)
	if err == nil {
		// Link the subject to the resolved user. The tenant is known now, so
		// this uses a normal pinned transaction rather than the system flag.
		lerr := db.WithTenant(r.Context(), s.pool, user.TenantID, func(tx pgx.Tx) error {
			_, e := tx.Exec(r.Context(), `
				INSERT INTO sso_accounts (user_id, provider, subject, email)
				VALUES ($1,$2,$3,$4) ON CONFLICT DO NOTHING`,
				user.ID, id.Provider, id.Subject, id.Email)
			return e
		})
		if lerr != nil {
			s.domainError(w, lerr)
			return
		}
		s.finishSSOLogin(w, r, user, id)
		return
	}

	// 3. Optional autoprovision: fresh tenant + owner.
	if !sso.Autoprovision() {
		writeError(w, http.StatusUnauthorized, "sso_no_account",
			"No TrackSphere account for this email — ask your admin for an invite")
		return
	}
	name := id.Name
	if strings.TrimSpace(name) == "" {
		name = strings.Split(id.Email, "@")[0]
	}
	org := strings.Split(id.Email, "@")
	orgName := "SSO Org"
	if len(org) == 2 {
		orgName = org[1] + " SSO"
	}
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		s.domainError(w, err)
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	if err := db.SetSystem(r.Context(), tx); err != nil {
		s.domainError(w, err)
		return
	}
	var tenant model.Tenant
	slug := slugify(orgName)
	if err := tx.QueryRow(r.Context(),
		`INSERT INTO tenants (name, slug) VALUES ($1,$2)
		 ON CONFLICT (slug) DO NOTHING RETURNING id, name, slug, plan, created_at`,
		orgName, slug).Scan(&tenant.ID, &tenant.Name, &tenant.Slug, &tenant.Plan, &tenant.CreatedAt); err != nil {
		// Slug race: suffix once (register has the full loop; SSO keeps one retry).
		slug += "-" + uuid.NewString()[:6]
		if err := tx.QueryRow(r.Context(),
			`INSERT INTO tenants (name, slug) VALUES ($1,$2)
			 RETURNING id, name, slug, plan, created_at`,
			orgName, slug).Scan(&tenant.ID, &tenant.Name, &tenant.Slug, &tenant.Plan, &tenant.CreatedAt); err != nil {
			s.domainError(w, err)
			return
		}
	}
	// Random unusable password (SSO-only account).
	pw, _ := auth.HashPassword(uuid.NewString() + uuid.NewString())
	if err := tx.QueryRow(r.Context(), `
		INSERT INTO users (tenant_id, email, name, password_hash, role)
		VALUES ($1,$2,$3,$4,'owner')
		RETURNING id, tenant_id, email, name, role, totp_enabled, created_at`,
		tenant.ID, id.Email, name, pw).Scan(
		&user.ID, &user.TenantID, &user.Email, &user.Name,
		&user.Role, &user.TOTPEnabled, &user.CreatedAt); err != nil {
		s.domainError(w, err)
		return
	}
	// The tenant exists now, so drop the system bypass and pin it: the
	// remaining writes (sso_accounts, sessions) have WITH CHECK policies that
	// require a tenant context, and under app.system='on' current_tenant() is
	// NULL so they would be rejected.
	if err := db.SetTenant(r.Context(), tx, tenant.ID); err != nil {
		s.domainError(w, err)
		return
	}
	if err := db.ClearSystem(r.Context(), tx); err != nil {
		s.domainError(w, err)
		return
	}
	if _, err := tx.Exec(r.Context(), `
		INSERT INTO sso_accounts (user_id, provider, subject, email)
		VALUES ($1,$2,$3,$4)`, user.ID, id.Provider, id.Subject, id.Email); err != nil {
		s.domainError(w, err)
		return
	}
	token, err := s.issueSession(r.Context(), tx, user, tenant.ID, false, r)
	if err != nil {
		s.domainError(w, err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		s.domainError(w, err)
		return
	}
	s.auditEvent(r.Context(), &tenant.ID, &user.ID, user.Email, "register", r)
	s.setSessionCookie(w, token)
	http.Redirect(w, r, "/", http.StatusFound)
}

// finishSSOLogin mints a session for a linked user and lands on the app.
//
// MFA is enforced here exactly as it is on the password path. It previously
// issued a full session with mfaPending hardcoded to false, so for any tenant
// using SSO the second factor was decorative: a phished IdP password was a
// complete account compromise despite MFA being switched on and advertised as
// a control.
func (s *Server) finishSSOLogin(w http.ResponseWriter, r *http.Request, user model.User, id *sso.Identity) {
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		s.domainError(w, err)
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()

	mfaPending := user.TOTPEnabled
	token, err := s.issueSession(r.Context(), tx, user, user.TenantID, mfaPending, r)
	if err != nil {
		s.domainError(w, err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		s.domainError(w, err)
		return
	}
	s.auditEvent(r.Context(), &user.TenantID, &user.ID, user.Email, "login", r)

	if mfaPending {
		// No session cookie yet — only the challenge, and only to the verify
		// endpoint.
		s.setMFAChallengeCookie(w, token)
		s.auditEvent(r.Context(), &user.TenantID, &user.ID, user.Email, "mfa_challenge", r)
		http.Redirect(w, r, "/login?mfa=1", http.StatusFound)
		return
	}

	s.setSessionCookie(w, token)
	http.Redirect(w, r, "/", http.StatusFound)
}
