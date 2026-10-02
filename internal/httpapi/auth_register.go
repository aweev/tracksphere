package httpapi

import (
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/tracksphere/tracksphere/internal/auth"
	"github.com/tracksphere/tracksphere/internal/db"
	"github.com/tracksphere/tracksphere/internal/model"
)

var emailRe = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

// slugify produces URL-safe tenant slugs (runs of separators collapse to one
// hyphen; non-ASCII is dropped).
func slugify(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	var b strings.Builder
	prevDash := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		case r == ' ' || r == '-' || r == '_':
			if !prevDash && b.Len() > 0 {
				b.WriteRune('-')
				prevDash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = "org"
	}
	return out
}

type registerRequest struct {
	OrgName  string `json:"orgName"`
	Name     string `json:"name"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

// handleRegister creates a tenant + owner user in one transaction and
// signs them in immediately.
func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	req.OrgName = strings.TrimSpace(req.OrgName)
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	req.Name = strings.TrimSpace(req.Name)

	switch {
	case req.OrgName == "":
		writeError(w, http.StatusBadRequest, "invalid_input", "orgName is required")
		return
	case !emailRe.MatchString(req.Email):
		writeError(w, http.StatusBadRequest, "invalid_input", "valid email is required")
		return
	case len(req.Password) < 8:
		writeError(w, http.StatusBadRequest, "invalid_input", "password must be at least 8 characters")
		return
	}

	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_input", err.Error())
		return
	}

	var (
		tenant model.Tenant
		user   model.User
	)
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		s.domainError(w, err)
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()

	// Tenants are bootstrapped under the system flag: the new row's id cannot
	// equal current_tenant() before it exists, so the tenants WITH CHECK only
	// admits this INSERT under app.system='on'. No business table is touched
	// in this state — users/sessions have no RLS, everything else is pinned.
	if err := db.SetSystem(r.Context(), tx); err != nil {
		s.domainError(w, err)
		return
	}

	// Unique slug: base + counter on conflict.
	base := slugify(req.OrgName)
	slug := base
	for i := 2; ; i++ {
		err = tx.QueryRow(r.Context(),
			`INSERT INTO tenants (name, slug) VALUES ($1,$2)
			 ON CONFLICT (slug) DO NOTHING RETURNING id, name, slug, plan, created_at`,
			req.OrgName, slug).Scan(&tenant.ID, &tenant.Name, &tenant.Slug, &tenant.Plan, &tenant.CreatedAt)
		if err == nil {
			break
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			s.domainError(w, err)
			return
		}
		slug = base + "-" + itoa(i)
		if i > 100 {
			writeError(w, http.StatusConflict, "slug_exhausted", "Could not allocate organization slug")
			return
		}
	}

	err = tx.QueryRow(r.Context(), `
		INSERT INTO users (tenant_id, email, name, password_hash, role)
		VALUES ($1,$2,$3,$4,'owner')
		RETURNING id, tenant_id, email, name, role, totp_enabled, created_at`,
		tenant.ID, req.Email, req.Name, hash).
		Scan(&user.ID, &user.TenantID, &user.Email, &user.Name, &user.Role,
			&user.TOTPEnabled, &user.CreatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "email_taken", "An account with this email already exists")
			return
		}
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
	s.setSessionCookie(w, token)
	writeJSON(w, http.StatusCreated, map[string]any{"user": user, "tenant": tenant})
}

// itoa avoids pulling strconv for a single use.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [8]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// isUniqueViolation reports whether err is a Postgres unique violation (23505).
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}