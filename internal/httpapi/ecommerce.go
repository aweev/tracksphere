package httpapi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/tracksphere/tracksphere/internal/auth"
	"github.com/tracksphere/tracksphere/internal/db"
	"github.com/tracksphere/tracksphere/internal/shipments"
)

// E-commerce (Shopify / WooCommerce): connect a store, then order webhooks
// auto-create shipments. Secrets sealed; order webhooks HMAC-verified.
//
// RLS: ecommerce_connections is FORCE ROW LEVEL SECURITY with a tenant-only
// policy (see 000013_p2_worker_reads.sql). Every authenticated handler must
// therefore run inside db.WithTenant — a bare s.pool.Query has no app.tenant_id
// set and fails closed (zero rows / rejected INSERT).

type ecommerceView struct {
	ID       uuid.UUID `json:"id"`
	Provider string    `json:"provider"`
	ShopURL  string    `json:"shopUrl"`
	Active   bool      `json:"active"`
}

// handleListEcommerce GET /api/v1/integrations/commerce
func (s *Server) handleListEcommerce(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	out := []ecommerceView{}
	err := db.WithTenant(r.Context(), s.pool, user.TenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `
			SELECT id, provider, shop_url, active FROM ecommerce_connections
			WHERE tenant_id=$1 ORDER BY created_at DESC`, user.TenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var v ecommerceView
			if err := rows.Scan(&v.ID, &v.Provider, &v.ShopURL, &v.Active); err != nil {
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

type connectEcommerceRequest struct {
	Provider string `json:"provider"`
	ShopURL  string `json:"shopUrl"`
	Token    string `json:"token"`
}

// handleConnectEcommerce POST /api/v1/integrations/commerce (admin+)
func (s *Server) handleConnectEcommerce(w http.ResponseWriter, r *http.Request) {
	var req connectEcommerceRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	provider := strings.ToLower(strings.TrimSpace(req.Provider))
	if provider != "shopify" && provider != "woocommerce" {
		writeError(w, http.StatusBadRequest, "invalid_input", "provider must be shopify|woocommerce")
		return
	}
	shop, err := normalizeShopURL(req.ShopURL)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_input", err.Error())
		return
	}
	user := currentUser(r)
	sealed := ""
	if req.Token != "" {
		s2, serr := auth.SealMulti(s.cfg.SecretKeys, []byte(req.Token))
		if serr != nil {
			s.domainError(w, serr)
			return
		}
		sealed = s2
	}
	hookSecret := newEndpointSecret()
	var id uuid.UUID
	err = db.WithTenant(r.Context(), s.pool, user.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(r.Context(), `
			INSERT INTO ecommerce_connections (tenant_id, provider, shop_url, sealed_token, webhook_secret, created_by)
			VALUES ($1,$2,$3,$4,$5,$6)
			ON CONFLICT (tenant_id, provider, shop_url) DO UPDATE SET
				sealed_token=CASE WHEN EXCLUDED.sealed_token='' THEN ecommerce_connections.sealed_token ELSE EXCLUDED.sealed_token END,
				webhook_secret=EXCLUDED.webhook_secret, active=true
			RETURNING id`,
			user.TenantID, provider, shop, sealed, hookSecret, user.ID).Scan(&id)
	})
	if err != nil {
		s.domainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id": id, "provider": provider, "shopUrl": shop,
		"webhookSecret": hookSecret,
		"warning":       "Configure this secret in the store's webhook settings",
	})
}

// handleDeleteEcommerce DELETE /api/v1/integrations/commerce/{id} (admin+)
func (s *Server) handleDeleteEcommerce(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(chiParam(r, "id"))
	if !ok {
		writeError(w, http.StatusBadRequest, "bad_id", "Invalid connection id")
		return
	}
	user := currentUser(r)
	var deleted bool
	err := db.WithTenant(r.Context(), s.pool, user.TenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(r.Context(),
			`DELETE FROM ecommerce_connections WHERE id=$1 AND tenant_id=$2`, id, user.TenantID)
		if err != nil {
			return err
		}
		deleted = tag.RowsAffected() > 0
		return nil
	})
	if err != nil {
		s.domainError(w, err)
		return
	}
	if !deleted {
		writeError(w, http.StatusNotFound, "not_found", "Connection not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// normalizeShopURL canonicalises a store URL to a bare lowercased host so the
// value stored matches the value the webhook resolver computes. Substring
// matching was previously used, which allowed one tenant's shop hint to select
// another tenant's connection.
func normalizeShopURL(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", errShopURLRequired
	}
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Hostname() == "" {
		return "", errShopURLInvalid
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if host == "" || !strings.Contains(host, ".") {
		return "", errShopURLInvalid
	}
	return host, nil
}

type shopURLError struct{ msg string }

func (e shopURLError) Error() string { return e.msg }

var (
	errShopURLRequired = shopURLError{"shopUrl required (e.g. acme.myshopify.com)"}
	errShopURLInvalid  = shopURLError{"shopUrl must be a valid store hostname"}
)

// handleCommerceOrder POST /api/v1/webhooks/commerce/{provider}
// Shopify HMAC (X-Shopify-Hmac-Sha256, base64) or Woo (X-WC-Webhook-Signature,
// hex) over the raw body, using the connection's webhook_secret. One shipment
// is created per fulfillment that carries a tracking number.
func (s *Server) handleCommerceOrder(w http.ResponseWriter, r *http.Request) {
	provider := strings.ToLower(strings.TrimSpace(chiParam(r, "provider")))
	if provider != "shopify" && provider != "woocommerce" {
		writeError(w, http.StatusBadRequest, "invalid_input", "provider must be shopify|woocommerce")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_body", "Could not read body")
		return
	}

	// The shop hint is REQUIRED. Falling back to "newest connection" would let a
	// request without a shop header be routed at an arbitrary tenant.
	shopHint := r.Header.Get("X-Shop-Domain")
	if shopHint == "" {
		shopHint = r.Header.Get("X-WC-Shop")
	}
	shop, err := normalizeShopURL(shopHint)
	if err != nil {
		writeError(w, http.StatusNotFound, "unknown_shop", "Missing or invalid shop header")
		return
	}

	// Connections are RLS tenant-scoped and order webhooks arrive with no
	// session, so resolve under the system flag. This transaction is
	// read-only but still committed explicitly so the intent is clear and a
	// future write cannot be silently rolled back.
	var (
		connID   uuid.UUID
		tenantID uuid.UUID
		secret   string
	)
	systx, err := s.pool.Begin(r.Context())
	if err != nil {
		s.domainError(w, err)
		return
	}
	if err := db.SetSystem(r.Context(), systx); err != nil {
		_ = systx.Rollback(r.Context())
		s.domainError(w, err)
		return
	}
	err = systx.QueryRow(r.Context(), `
		SELECT id, tenant_id, webhook_secret FROM ecommerce_connections
		WHERE provider=$1 AND active=true AND shop_url=$2
		ORDER BY created_at DESC LIMIT 1`, provider, shop).
		Scan(&connID, &tenantID, &secret)
	if err != nil {
		_ = systx.Rollback(r.Context())
		// Uniform 404 for unknown shop and unresolvable connection: do not let
		// the response distinguish "no such store" from "wrong signature".
		writeError(w, http.StatusNotFound, "unknown_shop", "No active connection for this shop")
		return
	}
	if cerr := systx.Commit(r.Context()); cerr != nil {
		s.domainError(w, cerr)
		return
	}

	if !verifyCommerceSignature(provider, secret, body, r) {
		writeError(w, http.StatusUnauthorized, "bad_signature", "Signature verification failed")
		return
	}

	// Minimal order shape. Unknown shapes → 400.
	var order struct {
		ID           any    `json:"id"`
		Email        string `json:"email"` // customer email → auto-subscription
		Fulfillments []struct {
			TrackingNumber  string `json:"tracking_number"`
			TrackingCompany string `json:"tracking_company"`
		} `json:"fulfillments"`
		ShippingAddress *struct {
			Name    string `json:"name"`
			City    string `json:"city"`
			Country string `json:"country"`
		} `json:"shipping_address"`
	}
	if err := json.Unmarshal(body, &order); err != nil {
		writeError(w, http.StatusBadRequest, "bad_event", "Invalid order payload")
		return
	}
	if len(order.Fulfillments) == 0 {
		writeError(w, http.StatusBadRequest, "bad_event", "Order has no fulfillments with tracking")
		return
	}
	// Destination joins only the parts we were given: "<city>, <country>" with
	// a missing city previously rendered a leading ", " — cosmetic, but the
	// customer sees it verbatim in every update message.
	dest := ""
	if order.ShippingAddress != nil {
		parts := make([]string, 0, 2)
		for _, p := range []string{order.ShippingAddress.City, order.ShippingAddress.Country} {
			if p = strings.TrimSpace(p); p != "" {
				parts = append(parts, p)
			}
		}
		dest = strings.Join(parts, ", ")
	}

	created, skipped, subscribed := 0, 0, 0
	for _, f := range order.Fulfillments {
		tracking := strings.TrimSpace(f.TrackingNumber)
		if tracking == "" {
			continue
		}
		carrier := strings.ToLower(strings.TrimSpace(f.TrackingCompany))
		if carrier == "" {
			// No tracking company in the fulfillment: detection later by
			// providers.DetectCarrier is better than a lie. "unknown" is an
			// honest placeholder — writing the literal provider slug made the
			// carrier scorecard report a "shopify" carrier.
			carrier = "unknown"
		}
		// Reference carries the order id so the operator can trace the shipment
		// back to the sale. Shopify ids are numeric OR string depending on API
		// version; fmt.Sprint handles both (a type assert dropped numbers and
		// silently degraded the reference to "order").
		ref := "order"
		if order.ID != nil {
			switch v := order.ID.(type) {
			case string:
				if v != "" {
					ref = "order " + v
				}
			case float64:
				if v > 0 {
					ref = fmt.Sprintf("order %.0f", v)
				}
			}
		}
		ship, err := s.shipments.Create(r.Context(), tenantID, uuid.Nil, shipments.CreateInput{
			TrackingNumber: tracking,
			Reference:      ref,
			Carrier:        carrier,
			// Mode follows the carrier profile (parcel companies move air/road;
			// e-commerce orders default to the short-haul norm). Hardcoding
			// "road" for a FedEx box priced the staleness threshold from the
			// 5-day road norm — 10× too slow to surface a silent parcel.
			Mode:        modeForCarrier(carrier),
			Destination: dest,
		})
		if err != nil {
			// Duplicate tracking (replay) or validation: count and continue.
			skipped++
			continue
		}
		created++

		// Auto-subscribe the customer end-to-end when the order carries an
		// email: the e-commerce promise is "customer gets updates without the
		// forwarder lifting a finger". Email is the low-friction, unmetered
		// channel so consent goes straight to active; the event is auditable
		// in notification_consent like every portal subscription (ADR 0009).
		// Best-effort: a failed subscribe must not fail the ingestion the
		// forwarder's WISMO is bleeding on.
		if email := strings.TrimSpace(order.Email); email != "" {
			ok, err := s.subscribeEmailForCommerce(r.Context(), tenantID, ship.ID, email)
			if err == nil && ok {
				subscribed++
			}
		}
	}
	_ = connID
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "created": created, "skipped": skipped, "subscribed": subscribed,
	})
}

// modeForCarrier maps a carrier slug to a transport mode for e-commerce
// fulfilments. Container lines get the ocean norm, parcel networks air, and
// everything unknown stays road — the ETA/staleness norms are per-mode, so
// the mode determines how fast silence becomes an exception.
func modeForCarrier(carrier string) string {
	switch carrier {
	case "maersk", "msc", "cma-cgm", "cma_cgm", "hapag", "cosco", "one", "evergreen":
		return "ocean"
	case "fedex", "dhl", "ups":
		return "air"
	default:
		return "road"
	}
}

// subscribeEmailForCommerce activates an email subscription for a shipment
// created by an order webhook. Emails arrive through an HMAC-verified store
// webhook — consent at checkout — so the email path skips the double
// opt-in portal loop (SMS/WhatsApp are never touched here: metered, and
// requiring interactive consent). Caps mirror handleSubscribe.
func (s *Server) subscribeEmailForCommerce(ctx context.Context, tenantID, shipmentID uuid.UUID, email string) (bool, error) {
	rhash := subjectHash(email)
	subscribed := false
	err := db.WithTenant(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		var total int
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM tracking_subscriptions
			WHERE shipment_id = $1 AND status IN ('pending','active')`, shipmentID).Scan(&total); err != nil {
			return err
		}
		if total >= maxSubscribersPerShipment {
			return errSubscribeCap
		}
		tag, err := tx.Exec(ctx, `
			INSERT INTO tracking_subscriptions
				(tenant_id, shipment_id, channel, recipient, recipient_hash, status,
				 consent_at, confirmed_at, source, digest_only)
			VALUES ($1,$2,'email',$3,$4,'active', now(), now(), 'commerce', false)
			ON CONFLICT (shipment_id, channel, recipient) DO NOTHING`,
			tenantID, shipmentID, email, rhash)
		if err != nil {
			return err
		}
		subscribed = tag.RowsAffected() > 0
		// Retain the compliance record in the SAME shape and table every
		// portal path uses (audit joins on recipient_hash), with the store's
		// HMAC as the reason. 'requested'+'confirmed' together: consent at
		// checkout, delivered by the webhook's signature.
		_, _ = tx.Exec(ctx, `
			INSERT INTO notification_consent
				(tenant_id, shipment_id, channel, recipient, recipient_hash, action, reason, ip_hash)
			VALUES ($1,$2,'email',$3,$4,'confirmed','commerce_order_webhook','')`,
			tenantID, shipmentID, email, rhash)
		return nil
	})
	return subscribed, err
}

func verifyCommerceSignature(provider, secret string, body []byte, r *http.Request) bool {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	sum := mac.Sum(nil)
	if provider == "shopify" {
		got, err := base64.StdEncoding.DecodeString(
			strings.TrimSpace(r.Header.Get("X-Shopify-Hmac-Sha256")))
		return err == nil && hmac.Equal(got, sum)
	}
	want := hex.EncodeToString(sum)
	return hmac.Equal([]byte(strings.TrimSpace(r.Header.Get("X-WC-Webhook-Signature"))), []byte(want))
}
