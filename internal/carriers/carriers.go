// Package carriers implements the P2 carrier SDK layer: a small interface,
// a registry of polling clients, per-carrier status normalization and
// tracking-number validation. Polling clients speak generic HTTPS JSON
// (tenant-configured base URL + sealed credentials), so real carrier
// endpoints plug in without code changes; the `fake` carrier backs tests,
// demos and staging smoke.
package carriers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/tracksphere/tracksphere/internal/httpclient"
)

// Event is one normalized tracking checkpoint from a carrier.
type Event struct {
	EventID     string
	Code        string // BOOKED | DEPARTED | ARRIVED | CUSTOMS_HOLD | ...
	Description string
	Location    string
	Lat         *float64
	Lng         *float64
	OccurredAt  time.Time
	Status      string // shipment status implied by the event (may be "")
	ETA         *time.Time
}

// Credentials are the tenant-supplied secrets for a carrier (unsealed).
type Credentials struct {
	APIKey    string
	APISecret string
	AccountID string
}

// Carrier fetches tracking checkpoints by polling.
type Carrier interface {
	// Name is the registry slug (maersk, dhl, fedex, ups, fake).
	Name() string
	// ValidateTracking reports whether the number looks plausible.
	ValidateTracking(tracking string) error
	// Track polls for checkpoints newer than since (zero = all).
	Track(ctx context.Context, baseURL string, creds Credentials, tracking string, since time.Time) ([]Event, error)
}

var registry = map[string]Carrier{}

// Register adds a carrier client (called from init below + tests).
func Register(c Carrier) { registry[c.Name()] = c }

// Get returns the client for slug, or nil.
func Get(slug string) Carrier { return registry[strings.ToLower(slug)] }

// Known reports whether slug is a supported polling carrier.
func Known(slug string) bool { return Get(slug) != nil }

// Slugs lists registered carriers (for UIs/docs).
func Slugs() []string {
	out := make([]string, 0, len(registry))
	for s := range registry {
		out = append(out, s)
	}
	return out
}

// NormalizeStatus maps carrier status words to TrackSphere statuses.
func NormalizeStatus(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "booked", "created", "label_created", "pending":
		return "booked"
	case "in_transit", "in transit", "departed", "shipped", "on_the_way":
		return "in_transit"
	case "at_customs", "customs_hold", "held":
		return "at_customs"
	case "out_for_delivery", "out for delivery", "with_courier":
		return "out_for_delivery"
	case "delivered", "completed", "signed":
		return "delivered"
	case "exception", "failed", "delayed", "damaged":
		return "exception"
	case "cancelled", "canceled", "void":
		return "cancelled"
	default:
		return ""
	}
}

// ── Generic HTTPS/JSON polling client with circuit breaker ────────────────
// Contract (documented for carrier onboarding):
//   GET {baseURL}/track?number={tracking}[&since={rfc3339}]
//   Authorization: Bearer {APIKey}        (when set)
//   → 200 [{"eventId","code","description","location","lat","lng",
//           "occurredAt","status","eta"}]
// Status words pass through NormalizeStatus; unknown codes are kept verbatim
// so the timeline never drops data.

type httpCarrier struct {
	name       string
	minLen     int
	httpClient *httpclient.Client
}

func (c *httpCarrier) Name() string { return c.name }

func (c *httpCarrier) ValidateTracking(tracking string) error {
	t := strings.TrimSpace(tracking)
	if len(t) < c.minLen {
		return fmt.Errorf("tracking number too short for %s (min %d chars)", c.name, c.minLen)
	}
	return nil
}

func (c *httpCarrier) Track(ctx context.Context, baseURL string, creds Credentials, tracking string, since time.Time) ([]Event, error) {
	if err := c.ValidateTracking(tracking); err != nil {
		return nil, err
	}
	if strings.TrimSpace(baseURL) == "" {
		return nil, fmt.Errorf("%s: no base URL configured", c.name)
	}
	url := strings.TrimRight(baseURL, "/") + "/track?number=" + tracking
	if !since.IsZero() {
		url += "&since=" + since.UTC().Format(time.RFC3339)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if creds.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+creds.APIKey)
	}
	
	// Use circuit breaker protected client
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("%s: poll returned %d", c.name, resp.StatusCode)
	}
	var raw []struct {
		EventID     string   `json:"eventId"`
		Code        string   `json:"code"`
		Description string   `json:"description"`
		Location    string   `json:"location"`
		Lat         *float64 `json:"lat"`
		Lng         *float64 `json:"lng"`
		OccurredAt  time.Time `json:"occurredAt"`
		Status      string   `json:"status"`
		ETA         *time.Time `json:"eta"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("%s: decode poll: %w", c.name, err)
	}
	out := make([]Event, 0, len(raw))
	for _, r := range raw {
		if r.EventID == "" || r.Code == "" || r.OccurredAt.IsZero() {
			continue
		}
		if st := NormalizeStatus(r.Status); st != "" {
			r.Status = st
		}
		out = append(out, Event{
			EventID: r.EventID, Code: strings.ToUpper(r.Code),
			Description: r.Description, Location: r.Location,
			Lat: r.Lat, Lng: r.Lng, OccurredAt: r.OccurredAt,
			Status: r.Status, ETA: r.ETA,
		})
	}
	return out, nil
}

// ── Fake carrier (tests, demos, staging smoke) ───────────────────────
// Serves Track() from an in-memory script when baseURL is "fake://...".

type fakeCarrier struct{}

func (fakeCarrier) Name() string { return "fake" }
func (fakeCarrier) ValidateTracking(t string) error {
	if len(strings.TrimSpace(t)) < 3 {
		return fmt.Errorf("tracking number too short for fake")
	}
	return nil
}
func (fakeCarrier) Track(_ context.Context, _ string, _ Credentials, tracking string, _ time.Time) ([]Event, error) {
	now := time.Now().UTC()
	return []Event{{
		EventID: "fake-1", Code: "DEPARTED",
		Description: "Fake carrier poll for " + tracking,
		Location:    "Fake Port", OccurredAt: now, Status: "in_transit",
	}}, nil
}

// ── Carrier detection from tracking-number shape ─────────────────────
// Heuristics, not proof: UPS 1Z, FedEx 12/15-digit, DHL 10-digit/JJD, ocean
// B/L (4-letter prefix + digits). Returns slugs ordered by confidence.

func onlyDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// DetectCarrier guesses the carrier from the tracking number's shape.
func DetectCarrier(tracking string) []map[string]any {
	t := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(tracking), " ", ""))
	var out []map[string]any
	add := func(slug string, conf float64) {
		if Known(slug) {
			out = append(out, map[string]any{"carrier": slug, "confidence": conf})
		}
	}
	if strings.HasPrefix(t, "1Z") {
		add("ups", 0.95)
	}
	digits := ""
	for _, r := range t {
		if r >= '0' && r <= '9' {
			digits += string(r)
		}
	}
	if onlyDigits(t) && (len(t) == 12 || len(t) == 15 || len(t) == 20) {
		add("fedex", 0.8)
	}
	if onlyDigits(t) && len(t) == 10 {
		add("dhl", 0.75)
	}
	if strings.HasPrefix(t, "JJD") || strings.HasPrefix(t, "GM") {
		add("dhl", 0.7)
	}
	if len(t) >= 8 {
		letters := 0
		for _, r := range t {
			if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
				if r >= 'A' && r <= 'Z' {
					letters++
				}
			} else {
				letters = -100
			}
		}
		if letters >= 4 && len(digits) >= 5 {
			add("maersk", 0.55)
			add("cma-cgm", 0.45)
		}
	}
	if out == nil {
		out = []map[string]any{}
	}
	return out
}

func init() {
	// Use circuit breaker protected HTTP client for carrier polling
	carrierClient := httpclient.NewClient(httpclient.CarrierAPIClient)
	
	Register(&httpCarrier{name: "maersk", minLen: 6, httpClient: carrierClient})
	Register(&httpCarrier{name: "dhl", minLen: 6, httpClient: carrierClient})
	Register(&httpCarrier{name: "fedex", minLen: 6, httpClient: carrierClient})
	Register(&httpCarrier{name: "ups", minLen: 6, httpClient: carrierClient})
	Register(&httpCarrier{name: "cma-cgm", minLen: 6, httpClient: carrierClient})
	Register(fakeCarrier{})
}