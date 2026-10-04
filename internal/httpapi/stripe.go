package httpapi

import (
	"context"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/tracksphere/tracksphere/internal/billing"
	"github.com/tracksphere/tracksphere/internal/db"
)

// handleCreateCheckout POST /api/v1/billing/checkout {plan} (admin+).
// Without STRIPE_SECRET_KEY the response is 501 with contact-sales copy.
func (s *Server) handleCreateCheckout(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Plan string `json:"plan"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	plan := strings.ToLower(strings.TrimSpace(req.Plan))
	if plan != "growth" && plan != "enterprise" {
		writeError(w, http.StatusBadRequest, "invalid_input", "plan must be growth|enterprise")
		return
	}
	if !billing.Enabled() || billing.PriceFor(plan) == "" {
		writeError(w, http.StatusNotImplemented, "billing_unconfigured",
			"Self-serve checkout is not enabled — contact sales to upgrade")
		return
	}
	user := currentUser(r)
	url, err := billing.CreateCheckoutSession(plan, user.TenantID.String(), user.Email)
	if err != nil {
		s.log.Error("stripe checkout failed", "err", err)
		writeError(w, http.StatusBadGateway, "billing_failed", "Could not start checkout, try again")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"url": url})
}

// handleStripeWebhook POST /api/v1/billing/webhook — Stripe-signed, no session.
// Handles checkout.session.completed (plan provisioning) and
// customer.subscription.updated/deleted (status sync).
func (s *Server) handleStripeWebhook(w http.ResponseWriter, r *http.Request) {
	secret := os.Getenv("STRIPE_WEBHOOK_SECRET")
	if secret == "" {
		writeError(w, http.StatusServiceUnavailable, "billing_unconfigured", "Webhooks not configured")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_body", "Could not read body")
		return
	}
	ev, err := billing.VerifyWebhook(body, r.Header.Get("Stripe-Signature"), secret)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "bad_signature", err.Error())
		return
	}
	switch ev.Type {
	case "checkout.session.completed":
		// client_reference_id carries our tenant id; plan comes from metadata.
		tenantID := ev.Data.Object.ClientRef
		plan := ev.Data.Object.Metadata["plan"]
		if plan == "" {
			plan = "growth"
		}
		sub := billing.SubscriptionOf(ev)
		cust := billing.CustomerOf(ev)
		if tenantID == "" {
			break
		}
		s.applyStripePlan(r.Context(), tenantID, cust, sub, plan, "active")
	case "customer.subscription.updated", "customer.subscription.deleted":
		sub := billing.SubscriptionOf(ev)
		status := ev.Data.Object.Status
		if sub == "" {
			break
		}
		tenantID, ok := s.tenantForStripeSub(r.Context(), sub)
		if !ok {
			break // unknown subscription — nothing to update
		}
		plan := "starter"
		if status != "canceled" && status != "unpaid" {
			// Keep the plan the customer chose; only a terminal status downgrades.
			if current := s.tenantPlan(r.Context(), mustUUID(tenantID)); current != "" {
				plan = current
			}
		}
		s.applyStripePlan(r.Context(), tenantID, "", sub, plan, status)
	}
	writeJSON(w, http.StatusOK, map[string]any{"received": true})
}

func mustUUID(s string) uuid.UUID {
	id, _ := uuid.Parse(s)
	return id
}

// applyStripePlan records the subscription state for a tenant.
//
// Stripe webhooks carry no session and, on subscription.updated, no tenant id
// either — only the Stripe subscription id. stripe_subscriptions is
// RLS-protected (000015), so the lookup runs under the system flag; the
// subsequent write pins the tenant it resolved.
func (s *Server) applyStripePlan(ctx context.Context, tenantIDStr, cust, sub, plan, status string) {
	if tenantIDStr == "" {
		return
	}
	tenantID, err := uuid.Parse(tenantIDStr)
	if err != nil {
		return
	}
	_ = db.WithTenant(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		if sub != "" {
			if _, err := tx.Exec(ctx, `
				INSERT INTO stripe_subscriptions (tenant_id, stripe_customer, stripe_sub, plan, status)
				VALUES ($1,$2,$3,$4,$5)
				ON CONFLICT (tenant_id) DO UPDATE SET
					stripe_customer=EXCLUDED.stripe_customer, stripe_sub=EXCLUDED.stripe_sub,
					plan=EXCLUDED.plan, status=EXCLUDED.status, updated_at=now()`,
				tenantID, cust, sub, plan, status); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `UPDATE tenants SET plan=$2 WHERE id=$1`, tenantID, plan)
		return err
	})
}

// tenantForStripeSub resolves the owning tenant from a Stripe subscription id.
// System read: there is no tenant context in a Stripe webhook.
func (s *Server) tenantForStripeSub(ctx context.Context, sub string) (string, bool) {
	var tenantID string
	err := s.querySystemRow(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT tenant_id::text FROM stripe_subscriptions WHERE stripe_sub=$1`,
			sub).Scan(&tenantID)
	})
	return tenantID, err == nil
}

// tenantPlan reads the caller's plan (tenant-pinned so RLS passes).
func (s *Server) tenantPlan(ctx context.Context, tenantID uuid.UUID) string {
	plan := "starter"
	_ = db.WithTenant(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT plan FROM tenants WHERE id=$1`, tenantID).Scan(&plan)
	})
	return plan
}

// overShipmentQuota enforces plan shipment limits on creation (402 payment
// required when over quota; unlimited = -1). Trial counts against starter.
func (s *Server) overShipmentQuota(ctx context.Context, tenantID uuid.UUID, plan string) bool {
	limits, ok := plans[plan]
	if !ok {
		limits = plans["starter"]
	}
	if limits.Shipments < 0 {
		return false
	}
	var n int64
	// shipments is RLS-protected: quota enforcement must read inside a pinned
	// transaction. At pool level this always counted 0, so no plan limit was
	// ever enforced.
	_ = db.WithTenant(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM shipments`).Scan(&n)
	})
	return n >= int64(limits.Shipments)
}
