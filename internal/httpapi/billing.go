package httpapi

import (
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/tracksphere/tracksphere/internal/db"
)

// Plan limits until Stripe lands in P2. Generous enough that no real trial
// user hits them; explicit enough that the UI tells the truth.
type planLimits struct {
	Shipments int `json:"shipments"`
	Seats     int `json:"seats"`
	APIKeys   int `json:"apiKeys"`
	Endpoints int `json:"endpoints"`
}

var plans = map[string]planLimits{
	"starter":    {Shipments: 100, Seats: 3, APIKeys: 2, Endpoints: 2},
	"growth":     {Shipments: 2000, Seats: 25, APIKeys: 10, Endpoints: 10},
	"enterprise": {Shipments: -1, Seats: -1, APIKeys: -1, Endpoints: -1}, // unlimited
}

type billingUsage struct {
	Shipments int64 `json:"shipments"`
	Seats     int64 `json:"seats"`
	APIKeys   int64 `json:"apiKeys"`
	Endpoints int64 `json:"endpoints"`
}

// handleBilling GET /api/v1/billing — plan, trial countdown and usage vs
// limits for the caller's tenant. This is what makes "14-day trial · no
// credit card" a true statement instead of landing-page fiction.
func (s *Server) handleBilling(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	var (
		plan      string
		trialEnds time.Time
		tenantID  uuid.UUID
	)
	usage := billingUsage{}
	err := db.WithTenant(r.Context(), s.pool, user.TenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(r.Context(),
			`SELECT id, plan, trial_ends_at FROM tenants WHERE id=$1`,
			user.TenantID).Scan(&tenantID, &plan, &trialEnds); err != nil {
			return err
		}
		if err := tx.QueryRow(r.Context(),
			`SELECT count(*) FROM shipments`).Scan(&usage.Shipments); err != nil {
			return err
		}
		// api_keys and tenant_webhooks became RLS-protected in 000015. These
		// were previously "non-RLS tables, explicitly scoped by tenant", which
		// meant metering silently reported 0 API keys and 0 endpoints to every
		// customer — and quota decisions are made from these numbers.
		if err := tx.QueryRow(r.Context(),
			`SELECT count(*) FROM api_keys WHERE revoked_at IS NULL`).Scan(&usage.APIKeys); err != nil {
			return err
		}
		if err := tx.QueryRow(r.Context(),
			`SELECT count(*) FROM tenant_webhooks`).Scan(&usage.Endpoints); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		s.domainError(w, err)
		return
	}
	// users has no RLS by design (see 000002), so this read stays unscoped.
	_ = s.pool.QueryRow(r.Context(),
		`SELECT count(*) FROM users WHERE tenant_id=$1 AND is_active=true`, user.TenantID).Scan(&usage.Seats)

	limits, ok := plans[plan]
	if !ok {
		limits = plans["starter"]
		plan = "starter"
	}
	daysLeft := int(time.Until(trialEnds).Hours() / 24)
	trialActive := time.Now().Before(trialEnds)
	if daysLeft < 0 {
		daysLeft = 0
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"plan":         plan,
		"trialEndsAt":  trialEnds,
		"trialDaysLeft": daysLeft,
		"trialActive":  trialActive,
		"usage":        usage,
		"limits":       limits,
	})
}
