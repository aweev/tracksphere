package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/tracksphere/tracksphere/internal/db"
	"github.com/tracksphere/tracksphere/internal/notify"
)

// White-label branding + public "notify me" subscriptions.

type brandView struct {
	Company string `json:"company"`
	Color   string `json:"color"`
	Logo    string `json:"logo"`
	Support string `json:"support"`
	// CustomDomain is the tenant's branded origin for customer-facing links
	// (confirm/unsubscribe). One-click carrier emails already brand themselves
	// from this column; the public API was silent about it, so an admin could
	// never set one — the white-label promise stopped at the email header.
	CustomDomain string `json:"customDomain"`
}

// normalizeCustomDomain accepts a bare hostname only. A stored URL with a
// scheme or path would corrupt links ("https://x/y/track/..." plus one more
// origin) and invite header confusion; the browser-facing hostname is the
// only thing this field can honestly mean.
func normalizeCustomDomain(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return "", nil
	}
	if strings.ContainsAny(s, " /\\?#") || strings.Contains(s, "://") {
		return "", errors.New("custom domain must be a bare hostname like portal.example.com")
	}
	if len(s) < 5 || len(s) > 253 || !strings.Contains(s, ".") ||
		strings.HasPrefix(s, ".") || strings.HasSuffix(s, ".") ||
		strings.HasPrefix(s, "-") || strings.HasSuffix(s, "-") ||
		strings.Contains(s, "..") {
		return "", errors.New("custom domain must be a bare hostname like portal.example.com")
	}
	// Accept the legal hostname shape: letters/digits/hyphens/dots.
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '.':
		default:
			return "", errors.New("custom domain must be a bare hostname like portal.example.com")
		}
	}
	return s, nil
}

func (s *Server) brandFor(ctx context.Context, tenantID string) brandView {
	b := brandView{Color: "#ff6b00"}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return b
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := db.SetPublicAccess(ctx, tx); err != nil {
		return b
	}
	_ = tx.QueryRow(ctx, `
		SELECT company_name, primary_color, logo_url, support_email, custom_domain
		FROM tenant_branding WHERE tenant_id=$1`, tenantID).
		Scan(&b.Company, &b.Color, &b.Logo, &b.Support, &b.CustomDomain)
	if b.Color == "" {
		b.Color = "#ff6b00"
	}
	_ = tx.Commit(ctx)
	return b
}

// handleGetBranding GET /api/v1/branding — caller's tenant brand.
func (s *Server) handleGetBranding(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	var b brandView
	var updated time.Time
	err := db.WithTenant(r.Context(), s.pool, user.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(r.Context(), `
			SELECT company_name, primary_color, logo_url, support_email, custom_domain, updated_at
			FROM tenant_branding WHERE tenant_id=$1`, user.TenantID).
			Scan(&b.Company, &b.Color, &b.Logo, &b.Support, &b.CustomDomain, &updated)
	})
	if err != nil {
		b = brandView{Color: "#ff6b00"}
	}
	writeJSON(w, http.StatusOK, b)
}

type brandInput struct {
	Company      string `json:"company"`
	Color        string `json:"color"`
	Logo         string `json:"logo"`
	Support      string `json:"support"`
	CustomDomain string `json:"customDomain"`
}

// handleUpdateBranding PUT /api/v1/branding (admin+)
func (s *Server) handleUpdateBranding(w http.ResponseWriter, r *http.Request) {
	var in brandInput
	if !decodeJSON(w, r, &in) {
		return
	}
	color := strings.TrimSpace(in.Color)
	if color != "" && !(strings.HasPrefix(color, "#") && (len(color) == 4 || len(color) == 7)) {
		writeError(w, http.StatusBadRequest, "invalid_input", "color must be #rgb or #rrggbb")
		return
	}
	if color == "" {
		color = "#ff6b00"
	}
	domain, err := normalizeCustomDomain(in.CustomDomain)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_input", err.Error())
		return
	}
	user := currentUser(r)
	err = db.WithTenant(r.Context(), s.pool, user.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(r.Context(), `
			INSERT INTO tenant_branding (tenant_id, company_name, primary_color, logo_url, support_email, custom_domain, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,now())
			ON CONFLICT (tenant_id) DO UPDATE SET
				company_name=EXCLUDED.company_name, primary_color=EXCLUDED.primary_color,
				logo_url=EXCLUDED.logo_url, support_email=EXCLUDED.support_email,
				custom_domain=EXCLUDED.custom_domain, updated_at=now()`,
			user.TenantID, strings.TrimSpace(in.Company), color,
			strings.TrimSpace(in.Logo), strings.TrimSpace(in.Support), domain)
		return err
	})
	if err != nil {
		s.domainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, brandView{
		Company: strings.TrimSpace(in.Company), Color: color,
		Logo: strings.TrimSpace(in.Logo), Support: strings.TrimSpace(in.Support),
		CustomDomain: domain,
	})
}

// Subscription caps. A public, unauthenticated endpoint that queues outbound
// messages needs hard ceilings, or one caller can turn the platform into an
// unsolicited-message relay.
const (
	maxSubscribersPerShipment = 25
	maxPendingPerRecipient    = 5
)

// Channels that cost money per message and therefore demand verified consent.
var meteredChannels = map[string]bool{"sms": true, "whatsapp": true}

type subscribeRequest struct {
	Channel   string `json:"channel"`
	Recipient string `json:"recipient"`
	Digest    bool   `json:"digest"`
}

// handleSubscribe POST /api/v1/track/{trackingNumber}/subscribe — public
// "notify me", double opt-in.
//
// The recipient is NOT deliverable on this call. We record a 'pending'
// subscription plus an audit row and (when the channel is metered) send a
// confirmation link/code; nothing is delivered until GET /subscribe/confirm
// succeeds. Without this the endpoint accepted any phone number and fanned out
// to it on every event — harassment, a metered cost, and a WhatsApp Business
// policy violation that risks the sending account for every tenant.
func (s *Server) handleSubscribe(w http.ResponseWriter, r *http.Request) {
	tracking := strings.TrimSpace(chiParam(r, "trackingNumber"))
	var req subscribeRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	channel := strings.ToLower(strings.TrimSpace(req.Channel))
	recipient := strings.TrimSpace(req.Recipient)
	if channel != "email" && channel != "sms" && channel != "whatsapp" {
		writeError(w, http.StatusBadRequest, "invalid_input", "channel must be email|sms|whatsapp")
		return
	}
	if err := validateRecipient(channel, recipient); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_input", err.Error())
		return
	}

	// Resolve the shipment through the public projection: only explicitly
	// published shipments are subscribable, so a private shipment cannot be
	// probed or notified through this route.
	ship, err := s.shipments.ByTrackingNumber(r.Context(), tracking)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "No shipment found for this tracking number")
		return
	}

	tenantID, err := s.tenantForShipment(r, ship.ID)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "No shipment found for this tracking number")
		return
	}

	rhash := subjectHash(recipient)
	ipHash := subjectHash(s.clientIP(r))

	type result struct {
		status    string
		duplicate bool
		tenantID  uuid.UUID
		// confirmLink is collected inside the transaction but the actual
		// provider send happens AFTER commit: no transaction spans network
		// I/O — a tx hold on a remote SMTP send is how every other request
		// in the pool starves.
		confirmLink string
		confirmBody string
	}
	var res result

	err = db.WithTenant(r.Context(), s.pool, tenantID, func(tx pgx.Tx) error {
		// Caps are enforced inside the transaction so concurrent requests
		// cannot both pass the count and exceed the limit.
		var total, pending int
		if err := tx.QueryRow(r.Context(), `
			SELECT count(*) FILTER (WHERE status IN ('pending','active')),
			       count(*) FILTER (WHERE status = 'pending')
			FROM tracking_subscriptions
			WHERE shipment_id = $1`, ship.ID).Scan(&total, &pending); err != nil {
			return err
		}
		if total >= maxSubscribersPerShipment {
			s.consentAudit(r, tx, tenantID, &ship.ID, channel, recipient, rhash,
				"suppressed", "shipment_subscriber_cap", ipHash)
			return errSubscribeCap
		}
		// Do not let one address farm pending confirmations across shipments.
		if err := tx.QueryRow(r.Context(), `
			SELECT count(*) FROM tracking_subscriptions
			WHERE recipient_hash = $1 AND status = 'pending'`, rhash).Scan(&pending); err != nil {
			return err
		}
		if pending >= maxPendingPerRecipient {
			s.consentAudit(r, tx, tenantID, &ship.ID, channel, recipient, rhash,
				"suppressed", "recipient_pending_cap", ipHash)
			return errSubscribeCap
		}

		// Metered kill-switch (P2-4): fail honestly instead of recording a
		// subscription that can never deliver. notify.ChannelAllowed is the
		// same policy the worker send paths enforce.
		if meteredChannels[channel] && !notify.ChannelAllowed(r.Context(), tx, tenantID, channel) {
			s.consentAudit(r, tx, tenantID, &ship.ID, channel, recipient, rhash,
				"suppressed", "metered_disabled", ipHash)
			return errSubscribeDisabled
		}

		// Email is trusted enough to activate immediately: proving control of
		// an inbox the customer typed into is itself the consent signal, and
		// email is unmetered so the abuse cost is low. SMS/WhatsApp are metered
		// and must be confirmed.
		status := "active"
		var token *string
		if meteredChannels[channel] {
			status = "pending"
			t := newEndpointSecret()
			token = &t
		}

		var existing string
		err := tx.QueryRow(r.Context(), `
			INSERT INTO tracking_subscriptions
				(tenant_id, shipment_id, channel, recipient, recipient_hash, status,
				 confirm_token, consent_at, confirmed_at, source, digest_only)
			VALUES ($1,$2,$3,$4,$5,$6,$7, now(), CASE WHEN $6='active' THEN now() END, 'portal', $8)
			ON CONFLICT (shipment_id, channel, recipient) DO NOTHING
			RETURNING status`,
			tenantID, ship.ID, channel, recipient, rhash, status, token, req.Digest).
			Scan(&existing)
		if err == pgx.ErrNoRows {
			// Already subscribed. Re-sending a confirmation for a pending row
			// is legitimate; reactivating a revoked one is not.
			if err := tx.QueryRow(r.Context(), `
				SELECT status FROM tracking_subscriptions
				WHERE shipment_id=$1 AND channel=$2 AND recipient=$3`,
				ship.ID, channel, recipient).Scan(&existing); err != nil {
				return err
			}
			if existing == "revoked" {
				s.consentAudit(r, tx, tenantID, &ship.ID, channel, recipient, rhash,
					"suppressed", "previously_revoked", ipHash)
				return errSubscribeRevoked
			}
			// A re-request for an already-active subscription does not change
			// anything, so no new token is issued and the recipient is not
			// messaged again.
			res = result{status: existing, duplicate: true, tenantID: tenantID}
		} else if err != nil {
			return err
		} else {
			res = result{status: existing, tenantID: tenantID}
		}

		s.consentAudit(r, tx, tenantID, &ship.ID, channel, recipient, rhash,
			"requested", "", ipHash)

		// Metered channels: collect the confirmation for out-of-band send
		// after commit. The origin is the tenant's own white-label domain
		// when it has one — the customer must never see a foreign brand on
		// the consent link; consent is the trust moment.
		if res.status == "pending" && token != nil && !res.duplicate {
			base := s.publicBaseURL(r)
			if brand, err := brandForTenantRow(r.Context(), tx, tenantID); err == nil && brand.CustomDomain != "" {
				base = "https://" + brand.CustomDomain
			}
			link := fmt.Sprintf("%s/track/%s/subscribe/confirm?token=%s",
				base, url.PathEscape(tracking), *token)
			res.confirmLink = link
			res.confirmBody = fmt.Sprintf(
				"Confirm tracking updates for %s\n\n%s\n\n"+
					"If you did not request this, ignore this message and nothing will be sent.",
				tracking, link)
		}
		return nil
	})
	switch err {
	case nil:
	case errSubscribeCap:
		writeError(w, http.StatusTooManyRequests, "subscribe_limit",
			"This shipment has reached its subscriber limit")
		return
	case errSubscribeRevoked:
		writeError(w, http.StatusGone, "subscribe_revoked",
			"Updates for this address were previously declined")
		return
	case errSubscribeDisabled:
		// 409, not 400: the request is well-formed, the operator has the
		// channel switched off. The portal offers email instead.
		writeError(w, http.StatusConflict, "channel_disabled",
			"Updates over this channel are not enabled by the operator — choose email")
		return
	default:
		s.domainError(w, err)
		return
	}

	// Post-commit: the provider send lives OUTSIDE the transaction. A tx
	// holding an SMTP socket open blocks connection pool checkout for every
	// concurrent request — this is the invariant the alert dispatcher
	// already follows.
	if res.confirmBody != "" {
		sender := notify.ForChannel(s.log, channel)
		if err := sender.Send(r.Context(), channel, recipient,
			"Confirm tracking updates", res.confirmBody); err != nil {
			s.log.Warn("subscribe confirmation send failed",
				"channel", channel, "err", err)
		}
	}

	status := http.StatusCreated
	if res.duplicate {
		status = http.StatusOK
	}
	writeJSON(w, status, map[string]any{
		"ok":      true,
		"status":  res.status,
		"channel": channel,
		"message": confirmMessage(res.status),
	})
}

// brandForTenantRow reads the branding row inside an already tenant-pinned
// transaction. Best-effort callers treat its error as "keep the default".
func brandForTenantRow(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) (brandView, error) {
	var b brandView
	err := tx.QueryRow(ctx, `
		SELECT company_name, primary_color, logo_url, support_email, custom_domain
		FROM tenant_branding WHERE tenant_id=$1`, tenantID).
		Scan(&b.Company, &b.Color, &b.Logo, &b.Support, &b.CustomDomain)
	if b.Color == "" {
		b.Color = "#ff6b00"
	}
	return b, err
}

func confirmMessage(status string) string {
	if status == "active" {
		return "You will receive updates for this shipment."
	}
	return "Check your messages to confirm. Updates start once you confirm."
}

var (
	errSubscribeCap      = errors.New("subscription cap reached")
	errSubscribeRevoked  = errors.New("previously revoked")
	errSubscribeDisabled = errors.New("metered channel disabled by operator")
)

// handleConfirmSubscribe GET /api/v1/subscribe/confirm?token=...
// The recipient proves control of the channel; only then is the subscription
// deliverable.
func (s *Server) handleConfirmSubscribe(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(r.URL.Query().Get("token"))
	if token == "" {
		writeError(w, http.StatusBadRequest, "invalid_token", "Confirmation token required")
		return
	}
	var (
		tenantID  uuid.UUID
		shipment  uuid.UUID
		channel   string
		recipient string
		found     bool
	)
	// The token is a high-entropy unique key, so a system read is acceptable
	// here: no tenant can be enumerated, and the row is pinned before use.
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
		SELECT tenant_id, shipment_id, channel, recipient
		FROM tracking_subscriptions
		WHERE confirm_token = $1 AND status = 'pending'`, token).
		Scan(&tenantID, &shipment, &channel, &recipient)
	if err != nil {
		_ = systx.Rollback(r.Context())
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "invalid_token",
				"This confirmation link is invalid or already used")
			return
		}
		s.domainError(w, err)
		return
	}
	if err := systx.Rollback(r.Context()); err != nil {
		s.domainError(w, err)
		return
	}

	err = db.WithTenant(r.Context(), s.pool, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(r.Context(), `
			UPDATE tracking_subscriptions
			SET status='active', confirmed_at=now(), consent_at=now(),
			    confirm_token=NULL
			WHERE tenant_id=$1 AND confirm_token=$2 AND status='pending'`,
			tenantID, token)
		if err != nil {
			return err
		}
		found = tag.RowsAffected() > 0
		if !found {
			return nil
		}
		if err := s.consentAudit(r, tx, tenantID, &shipment, channel, recipient,
			subjectHash(recipient), "confirmed", "", subjectHash(s.clientIP(r))); err != nil {
			return err
		}
		// Confirming flips customer_notified, which feeds the score's relief
		// term. Flag the row so the next batch recomputes it.
		return db.MarkShipmentDirty(r.Context(), tx, shipment)
	})
	if err != nil {
		s.domainError(w, err)
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "invalid_token",
			"This confirmation link is invalid or already used")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "message": "Confirmed — you will receive updates for this shipment.",
	})
}

type uuidOrStr = string

// publicBaseURL returns the externally visible base URL for links we send to
// customers. Configured explicitly (TRACKSPHERE_PUBLIC_URL) because the API is
// behind Cloudflare in every real deployment and cannot infer its own origin.
func (s *Server) publicBaseURL(r *http.Request) string {
	if s.cfg.PublicURL != "" {
		return strings.TrimRight(s.cfg.PublicURL, "/")
	}
	scheme := "https"
	if r.TLS == nil {
		if p := r.Header.Get("X-Forwarded-Proto"); p != "" {
			scheme = p
		} else {
			scheme = "http"
		}
	}
	return strings.TrimRight(scheme+"://"+r.Host, "/")
}

// handleUnsubscribe POST /api/v1/subscribe/unsubscribe — public opt-out.
// Opting out must never require an account, and must always succeed silently
// so a one-click unsubscribe cannot leak whether an address was subscribed.
func (s *Server) handleUnsubscribe(w http.ResponseWriter, r *http.Request) {
	var req subscribeRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	channel := strings.ToLower(strings.TrimSpace(req.Channel))
	recipient := strings.TrimSpace(req.Recipient)
	if err := validateRecipient(channel, recipient); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_input", err.Error())
		return
	}
	rhash := subjectHash(recipient)

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
	var tenantID uuid.UUID
	err = systx.QueryRow(r.Context(),
		`SELECT DISTINCT tenant_id FROM tracking_subscriptions WHERE recipient_hash=$1`,
		rhash).Scan(&tenantID)
	if err != nil {
		_ = systx.Rollback(r.Context())
		// Always 200: never confirm whether an address is subscribed.
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	_ = systx.Rollback(r.Context())

	_ = db.WithTenant(r.Context(), s.pool, tenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(r.Context(), `
			UPDATE tracking_subscriptions
			SET status='revoked', revoked_at=now(), confirm_token=NULL
			WHERE tenant_id=$1 AND recipient_hash=$2 AND status IN ('pending','active')`,
			tenantID, rhash); err != nil {
			return err
		}
		return s.consentAudit(r, tx, tenantID, nil, channel, recipient, rhash,
			"revoked", "user_request", subjectHash(s.clientIP(r)))
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// consentAudit appends to the immutable consent ledger. Failures are returned
// so a subscription is never created without its audit row.
func (s *Server) consentAudit(r *http.Request, tx pgx.Tx, tenantID uuid.UUID, shipmentID *uuid.UUID, channel, recipient, rhash, action, reason, ipHash string) error {
	_, err := tx.Exec(r.Context(), `
		INSERT INTO notification_consent
			(tenant_id, shipment_id, channel, recipient, recipient_hash, action, reason, ip_hash)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		tenantID, shipmentID, channel, recipient, rhash, action, reason, ipHash)
	return err
}

// tenantForShipment resolves the owning tenant of a shipment the public portal
// can already see. Read under the system flag, then the caller pins it.
func (s *Server) tenantForShipment(r *http.Request, shipmentID uuid.UUID) (uuid.UUID, error) {
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		return uuid.Nil, err
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	if err := db.SetSystem(r.Context(), tx); err != nil {
		return uuid.Nil, err
	}
	var tenantID uuid.UUID
	if err := tx.QueryRow(r.Context(),
		`SELECT tenant_id FROM shipments WHERE id=$1`, shipmentID).Scan(&tenantID); err != nil {
		return uuid.Nil, err
	}
	return tenantID, tx.Commit(r.Context())
}

func validateRecipient(channel, recipient string) error {
	if recipient == "" {
		return errors.New("recipient required")
	}
	switch channel {
	case "email":
		at := strings.LastIndex(recipient, "@")
		if at <= 0 || at == len(recipient)-1 || strings.Contains(recipient, " ") {
			return errors.New("valid email address required")
		}
		if len(recipient) > 254 {
			return errors.New("email address too long")
		}
	case "sms", "whatsapp":
		digits := 0
		for _, r := range recipient {
			if r >= '0' && r <= '9' {
				digits++
			}
		}
		if digits < 7 || digits > 15 {
			return errors.New("valid phone number in E.164 form required")
		}
	}
	return nil
}
