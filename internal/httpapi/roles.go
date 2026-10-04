package httpapi

import (
	"net/http"

	"github.com/tracksphere/tracksphere/internal/model"
)

// Role hierarchy: owner > admin > member. Members are daily ops agents —
// they can work shipments and resolve exceptions. Admins manage team-level
// resources (invites, carriers, integrations). Owners own billing/dangerous
// actions. Every authenticated route declares its minimum role so new
// endpoints fail closed by default.
const (
	RoleMember = "member"
	RoleAdmin  = "admin"
	RoleOwner  = "owner"
)

func roleRank(role string) int {
	switch role {
	case RoleOwner:
		return 3
	case RoleAdmin:
		return 2
	case RoleMember:
		return 1
	default:
		return 0
	}
}

// requireRole rejects authenticated callers below minRole with 403.
// Unknown roles fail closed (rank 0).
func (s *Server) requireRole(minRole string, h http.HandlerFunc) http.HandlerFunc {
	min := roleRank(minRole)
	return func(w http.ResponseWriter, r *http.Request) {
		u := currentUser(r)
		if u == nil {
			writeError(w, http.StatusUnauthorized, "unauthenticated", "Sign in required")
			return
		}
		if roleRank(u.Role) < min {
			writeError(w, http.StatusForbidden, "forbidden", "Insufficient role")
			return
		}
		h(w, r)
	}
}

var _ = model.User{}
