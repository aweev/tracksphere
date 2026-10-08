// Package config loads and validates process configuration from the environment.
// Every knob is prefixed TRACKSPHERE_ so it cannot collide with other software
// running on the same host. See .env.example for documentation of each value.
package config

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

type Config struct {
	Env     string // local | development | production
	Region  string // deployment region, e.g. us | eu (data-residency label)
	HTTPAddr string
	// PublicURL is the externally visible origin (e.g. https://track.acme.com).
	// Used for links we send to customers — confirmation emails, unsubscribe
	// links. The API sits behind Cloudflare and cannot infer its own origin.
	PublicURL string

	DatabaseURL       string
	MigrationsURL     string
	AutoMigrate       bool

	CORSOrigins []string
	// TrustedProxies are CIDRs or bare IPs whose forwarding headers
	// (X-Forwarded-For / X-Real-IP / CF-Connecting-IP) are believed. Requests
	// from anything else are keyed on the peer address, so a client cannot mint
	// a fresh rate-limit bucket per request by forging a header.
	// TRACKSPHERE_TRUSTED_PROXIES="10.0.0.0/8,172.16.0.0/12,127.0.0.1/32"
	TrustedProxies []*net.IPNet
	// TrustCloudflareHeaders additionally believes CF-Connecting-IP. Only set
	// this when Cloudflare is the ONLY thing that can reach the API.
	TrustCloudflareHeaders bool
	// ExposeMetrics gates GET /api/v1/metrics. Off by default: queue depth and
	// outage rate are operational reconnaissance, and the endpoint sits behind
	// the same public hostname as everything else.
	ExposeMetrics bool

	SecretKeys   [][]byte // Multi-key rotation: first is primary for encryption, all tried for decryption
	SecretKey    []byte   // Legacy single key (deprecated, use SecretKeys)
	SessionTTL   time.Duration
	WebhookSecret  []byte
	// Per-carrier HMAC secrets: TRACKSPHERE_CARRIER_WEBHOOK_SECRETS="maersk:s1,dhl:s2".
	// WebhookSecret remains the fallback/default. Rotate per carrier without
	// breaking the others.
	CarrierSecrets map[string]string

	WorkerConcurrency   int
	WorkerPollInterval  time.Duration
	// APIWorkers is the number of API worker processes (for pool sizing)
	APIWorkers int

	// WebhookFloodPerHour caps webhook_inbox rows per carrier per rolling
	// hour before the carrier webhook returns 429. Attackers mint inbox rows
	// (audit-before-verify), so the inbox needs a flood guard, not just a
	// per-IP rate limit. 0 disables the guard (not recommended).
	WebhookFloodPerHour int

	// MCPEnabled gates POST /api/v1/mcp (Model Context Protocol, agent
	// tool access). Off by default: an authenticated agent surface with no
	// named consumer must not listen. Enable only with a real integration.
	MCPEnabled bool

	LogLevel string
}

// Load reads .env (if present, non-fatal when missing) then the process
// environment, applies defaults and validates the result.
func Load() (*Config, error) {
	_ = godotenv.Load() // missing file is fine — production uses real env vars

	cfg := &Config{
		Env:            get("TRACKSPHERE_ENV", "local"),
		Region:         get("TRACKSPHERE_REGION", "us"),
		HTTPAddr:       get("TRACKSPHERE_HTTP_ADDR", ":8080"),
		PublicURL:      strings.TrimRight(get("TRACKSPHERE_PUBLIC_URL", ""), "/"),
		DatabaseURL:    get("TRACKSPHERE_DATABASE_URL", ""),
		MigrationsURL:  get("TRACKSPHERE_MIGRATIONS_URL", ""),
		CORSOrigins:    splitCSV(get("TRACKSPHERE_CORS_ORIGINS", "http://localhost:5173")),
		LogLevel:       get("TRACKSPHERE_LOG_LEVEL", "info"),
	}

	var err error
	if cfg.AutoMigrate, err = getBool("TRACKSPHERE_AUTO_MIGRATE", true); err != nil {
		return nil, err
	}
	if cfg.WorkerConcurrency, err = getInt("TRACKSPHERE_WORKER_CONCURRENCY", 4, 1, 64); err != nil {
		return nil, err
	}
	if cfg.APIWorkers, err = getInt("TRACKSPHERE_API_WORKERS", 1, 1, 32); err != nil {
		return nil, err
	}
	pollMS, err := getInt("TRACKSPHERE_WORKER_POLL_INTERVAL_MS", 500, 50, 10_000)
	if err != nil {
		return nil, err
	}
	cfg.WorkerPollInterval = time.Duration(pollMS) * time.Millisecond
	if cfg.WebhookFloodPerHour, err = getInt("TRACKSPHERE_WEBHOOK_FLOOD_PER_HOUR", 10000, 0, 1_000_000); err != nil {
		return nil, err
	}
	if cfg.MCPEnabled, err = getBool("TRACKSPHERE_MCP_ENABLED", false); err != nil {
		return nil, err
	}

	ttlHours, err := getInt("TRACKSPHERE_SESSION_TTL_HOURS", 168, 1, 24*90)
	if err != nil {
		return nil, err
	}
	cfg.SessionTTL = time.Duration(ttlHours) * time.Hour

	cfg.SecretKeys = parseSecretKeys(get("TRACKSPHERE_SECRET_KEYS", ""))
	// Backward compatibility: if TRACKSPHERE_SECRET_KEY is set but SECRET_KEYS is not, use it
	if len(cfg.SecretKeys) == 0 {
		cfg.SecretKey = []byte(get("TRACKSPHERE_SECRET_KEY", ""))
		if len(cfg.SecretKey) > 0 {
			cfg.SecretKeys = [][]byte{cfg.SecretKey}
		}
	}
	cfg.WebhookSecret = []byte(get("TRACKSPHERE_CARRIER_WEBHOOK_SECRET", ""))
	cfg.CarrierSecrets = parseCarrierSecrets(get("TRACKSPHERE_CARRIER_WEBHOOK_SECRETS", ""))
	cfg.TrustedProxies, err = parseCIDRs(get("TRACKSPHERE_TRUSTED_PROXIES", ""))
	if err != nil {
		return nil, err
	}
	if cfg.TrustCloudflareHeaders, err = getBool("TRACKSPHERE_TRUST_CLOUDFLARE_HEADERS", false); err != nil {
		return nil, err
	}
	if cfg.ExposeMetrics, err = getBool("TRACKSPHERE_EXPOSE_METRICS", false); err != nil {
		return nil, err
	}

	if cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("TRACKSPHERE_DATABASE_URL is required")
	}
	if cfg.MigrationsURL == "" {
		cfg.MigrationsURL = cfg.DatabaseURL
	}
	if len(cfg.SecretKeys) == 0 {
		return nil, fmt.Errorf("TRACKSPHERE_SECRET_KEYS or TRACKSPHERE_SECRET_KEY is required")
	}
	if len(cfg.WebhookSecret) == 0 {
		return nil, fmt.Errorf("TRACKSPHERE_CARRIER_WEBHOOK_SECRET is required")
	}
	if cfg.Env == "production" {
		// Validate all secret keys
		for i, k := range cfg.SecretKeys {
			if len(k) < 32 {
				return nil, fmt.Errorf("TRACKSPHERE_SECRET_KEYS[%d] must be >= 32 chars in production", i)
			}
		}
		if len(cfg.WebhookSecret) < 32 {
			return nil, fmt.Errorf("TRACKSPHERE_CARRIER_WEBHOOK_SECRET must be >= 32 chars in production")
		}
		for carrier, s := range cfg.CarrierSecrets {
			if len(s) < 16 {
				return nil, fmt.Errorf("per-carrier webhook secret for %q must be >= 16 chars in production", carrier)
			}
		}
	}
	switch cfg.Env {
	case "local", "development", "production":
	default:
		return nil, fmt.Errorf("TRACKSPHERE_ENV must be local|development|production, got %q", cfg.Env)
	}
	return cfg, nil
}

func get(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func getBool(key string, def bool) (bool, error) {
	v := get(key, "")
	if v == "" {
		return def, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("%s: %w", key, err)
	}
	return b, nil
}

func getInt(key string, def, min, max int) (int, error) {
	v := get(key, "")
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	if n < min || n > max {
		return 0, fmt.Errorf("%s must be between %d and %d, got %d", key, min, max, n)
	}
	return n, nil
}

func splitCSV(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// parseSecretKeys parses TRACKSPHERE_SECRET_KEYS="key1,key2,key3" into byte slices.
// First key is primary (encryption), all keys are tried for decryption.
func parseSecretKeys(s string) [][]byte {
	if s == "" {
		return nil
	}
	var keys [][]byte
	for _, part := range splitCSV(s) {
		if part == "" {
			continue
		}
		keys = append(keys, []byte(part))
	}
	return keys
}

// parseCarrierSecrets parses "carrier:secret,carrier2:secret2" (carrier
// lowercased, secret kept verbatim). Malformed entries are ignored.
func parseCarrierSecrets(s string) map[string]string {
	out := map[string]string{}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		kv := strings.SplitN(part, ":", 2)
		if len(kv) != 2 {
			continue
		}
		carrier := strings.ToLower(strings.TrimSpace(kv[0]))
		secret := strings.TrimSpace(kv[1])
		if carrier == "" || secret == "" {
			continue
		}
		out[carrier] = secret
	}
	return out
}

// WebhookSecretFor returns the per-carrier secret when configured, else the
// global fallback. Callers must still reject empty secrets in production
// (see Load guards).
func (c *Config) WebhookSecretFor(carrier string) []byte {
	if c == nil {
		return nil
	}
	if s, ok := c.CarrierSecrets[strings.ToLower(carrier)]; ok && s != "" {
		return []byte(s)
	}
	return c.WebhookSecret
}

// parseCIDRs parses a comma-separated list of IPs or CIDR blocks. A bare IP is
// promoted to /32 (or /128). Unparseable entries are an error: silently
// ignoring one would quietly disable the rate-limit bypass protection it gates.
func parseCIDRs(s string) ([]*net.IPNet, error) {
	var out []*net.IPNet
	for _, part := range splitCSV(s) {
		if strings.Contains(part, "/") {
			_, n, err := net.ParseCIDR(part)
			if err != nil {
				return nil, fmt.Errorf("TRACKSPHERE_TRUSTED_PROXIES: %w", err)
			}
			out = append(out, n)
			continue
		}
		ip := net.ParseIP(part)
		if ip == nil {
			return nil, fmt.Errorf("TRACKSPHERE_TRUSTED_PROXIES: %q is not an IP or CIDR", part)
		}
		bits := 32
		if ip.To4() == nil {
			bits = 128
		}
		out = append(out, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
	}
	return out, nil
}

// SetTestProxies replaces TrustedProxies from CIDR/IP strings. Test helper
// so httpapi tests can build trust configs without env indirection.
func (c *Config) SetTestProxies(cidrs ...string) error {
	nets, err := parseCIDRs(strings.Join(cidrs, ","))
	if err != nil {
		return err
	}
	c.TrustedProxies = nets
	return nil
}

// IsTrustedProxy reports whether addr belongs to a configured trusted proxy.
func (c *Config) IsTrustedProxy(addr string) bool {
	if c == nil || len(c.TrustedProxies) == 0 {
		return false
	}
	ip := net.ParseIP(addr)
	if ip == nil {
		return false
	}
	for _, n := range c.TrustedProxies {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// KnownCarriers is the allow-list for the {carrier} path segment. Unknown
// slugs get 400 instead of silently creating a divergent dedup namespace.
var KnownCarriers = map[string]bool{
	"maersk": true, "dhl": true, "fedex": true, "ups": true,
	"cma-cgm": true, "cma_cgm": true, "hapag": true, "msc": true,
	"cosco": true, "one": true, "evergreen": true,
}

// RecommendedDBPoolSize returns the recommended max connections based on
// the application's concurrency requirements.
// Formula: (API workers + worker pollers + background tasks) × safety factor
// Default safety factor = 2
func (c *Config) RecommendedDBPoolSize() int {
	// API needs connections for HTTP handlers + SSE hub + metrics
	apiConns := c.APIWorkers + 2 // +2 for SSE hub, metrics
	
	// Worker needs connections for pollers + reaper + archiver + LISTEN + scheduler
	workerConns := c.WorkerConcurrency + 4 // +4 for background tasks
	
	// Total with safety factor
	total := (apiConns + workerConns) * 2
	
	// Cap at reasonable maximum
	if total > 100 {
		total = 100
	}
	if total < 10 {
		total = 10
	}
	return total
}
