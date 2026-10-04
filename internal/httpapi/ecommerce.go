package httpapi

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
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
		ID           any `json:"id"`
		Fulfillments []struct {
			TrackingNumber  string `json:"tracking_number"`
			TrackingCompany string `json:"tracking_company"`
		} `json:"fulfillments"`
		ShippingAddress *struct {
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
	dest := ""
	if order.ShippingAddress != nil {
		dest = strings.TrimSpace(order.ShippingAddress.City + ", " + order.ShippingAddress.Country)
	}

	created, skipped := 0, 0
	for _, f := range order.Fulfillments {
		tracking := strings.TrimSpace(f.TrackingNumber)
		if tracking == "" {
			continue
		}
		carrier := strings.ToLower(strings.TrimSpace(f.TrackingCompany))
		if carrier == "" {
			carrier = provider
		}
		// Reference carries the order id so the operator can trace the shipment
		// back to the sale.
		ref := "order"
		if order.ID != nil {
			if s, ok := order.ID.(string); ok && s != "" {
				ref = "order " + s
			}
		}
		if _, err := s.shipments.Create(r.Context(), tenantID, uuid.Nil, shipments.CreateInput{
			TrackingNumber: tracking,
			Reference:      ref,
			Carrier:        carrier,
			Mode:           "road",
			Destination:    dest,
		}); err != nil {
			// Duplicate tracking (replay) or validation: count and continue.
			skipped++
			continue
		}
		created++
	}
	_ = connID
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "created": created, "skipped": skipped,
	})
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