// Package billing implements P2 Stripe billing with the standard library
// only (no SDK): Checkout Sessions via form-POST, webhook verification via
// the Stripe-Signature scheme (t=...,v1=... HMAC-SHA256 over "t.payload").
//
//   STRIPE_SECRET_KEY            → enables checkout (else "contact sales")
//   STRIPE_WEBHOOK_SECRET        → verifies POST /billing/webhook
//   STRIPE_PRICE_GROWTH          → price ID for the growth plan
//   STRIPE_PRICE_ENTERPRISE      → price ID for the enterprise plan
//   TRACKSPHERE_BILLING_SUCCESS_URL / CANCEL_URL → checkout redirects
package billing

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/tracksphere/tracksphere/internal/httpclient"
)

// Enabled reports whether Stripe checkout can run.
func Enabled() bool { return os.Getenv("STRIPE_SECRET_KEY") != "" }

// PriceFor maps a plan to its configured price ID ("" when unconfigured).
func PriceFor(plan string) string {
	switch plan {
	case "growth":
		return os.Getenv("STRIPE_PRICE_GROWTH")
	case "enterprise":
		return os.Getenv("STRIPE_PRICE_ENTERPRISE")
	default:
		return ""
	}
}

// CreateCheckoutSession opens a Stripe subscription checkout for tenant.
func CreateCheckoutSession(plan, tenantID, customerEmail string) (string, error) {
	price := PriceFor(plan)
	if price == "" || !Enabled() {
		return "", fmt.Errorf("stripe not configured for plan %q", plan)
	}
	form := url.Values{
		"mode":                    {"subscription"},
		"line_items[0][price]":    {price},
		"line_items[0][quantity]": {"1"},
		"client_reference_id":     {tenantID},
		"customer_email":          {customerEmail},
		"success_url":             {os.Getenv("TRACKSPHERE_BILLING_SUCCESS_URL")},
		"cancel_url":              {os.Getenv("TRACKSPHERE_BILLING_CANCEL_URL")},
	}
	if form.Get("success_url") == "" {
		form.Set("success_url", "https://example.com/billing/success")
	}
	if form.Get("cancel_url") == "" {
		form.Set("cancel_url", "https://example.com/billing/cancel")
	}
	req, err := http.NewRequest(http.MethodPost,
		"https://api.stripe.com/v1/checkout/sessions",
		strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(os.Getenv("STRIPE_SECRET_KEY"), "")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// Hardened client (M3): fixed-host vendor call, but it still gets the
	// SSRF-safe transport, TLS 1.2+ floor, and the stripe circuit breaker
	// instead of an unbounded raw client that can wedge the checkout path.
	resp, err := httpclient.NewClient(httpclient.StripeClient).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("stripe: %s", resp.Status)
	}
	var out struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.URL == "" {
		return "", fmt.Errorf("stripe: bad checkout response")
	}
	return out.URL, nil
}

// WebhookEvent is the subset of Stripe events we act on.
type WebhookEvent struct {
	Type string `json:"type"`
	Data struct {
		Object struct {
			ID             string `json:"id"`
			Customer       any    `json:"customer"`
			ClientRef      string `json:"client_reference_id"`
			Subscription   any    `json:"subscription"`
			Status         string `json:"status"`
			CurrentPeriodEnd int64 `json:"current_period_end"`
			Metadata       map[string]string `json:"metadata"`
		} `json:"object"`
	} `json:"data"`
}

// VerifyWebhook checks Stripe-Signature ("t=..,v1=..") over the raw body.
func VerifyWebhook(body []byte, header, secret string) (*WebhookEvent, error) {
	var ts string
	sigs := []string{}
	for _, part := range strings.Split(header, ",") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) != 2 {
			continue
		}
		if kv[0] == "t" {
			ts = kv[1]
		} else if kv[0] == "v1" {
			sigs = append(sigs, kv[1])
		}
	}
	if ts == "" || len(sigs) == 0 {
		return nil, fmt.Errorf("bad stripe signature header")
	}
	if n, err := strconv.ParseInt(ts, 10, 64); err != nil || time.Since(time.Unix(n, 0)).Abs() > 5*time.Minute {
		return nil, fmt.Errorf("stripe signature timestamp outside tolerance")
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "."))
	mac.Write(body)
	want := hex.EncodeToString(mac.Sum(nil))
	ok := false
	for _, s := range sigs {
		if hmac.Equal([]byte(s), []byte(want)) {
			ok = true
			break
		}
	}
	if !ok {
		return nil, fmt.Errorf("stripe signature mismatch")
	}
	var ev WebhookEvent
	if err := json.Unmarshal(body, &ev); err != nil {
		return nil, err
	}
	return &ev, nil
}

func strOf(v any) string {
	switch t := v.(type) {
	case string:
		return t
	default:
		return fmt.Sprintf("%v", v)
	}
}

// CustomerOf extracts the customer id from polymorphic Stripe fields.
func CustomerOf(ev *WebhookEvent) string { return strOf(ev.Data.Object.Customer) }

// SubscriptionOf extracts the subscription id.
func SubscriptionOf(ev *WebhookEvent) string { return strOf(ev.Data.Object.Subscription) }
