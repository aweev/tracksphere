// Package httpclient provides HTTP clients with circuit breakers for outbound calls.
package httpclient

import (
	"crypto/tls"
	"net/http"
	"time"

	"github.com/tracksphere/tracksphere/internal/circuitbreaker"
	"github.com/tracksphere/tracksphere/internal/metrics"
)

// ClientConfig holds configuration for an outbound HTTP client
type ClientConfig struct {
	Name                  string
	Timeout               time.Duration
	MaxIdleConns          int
	MaxConnsPerHost       int
	IdleConnTimeout       time.Duration
	TLSHandshakeTimeout   time.Duration
	ExpectContinueTimeout time.Duration

	// Circuit breaker config
	CircuitBreaker CircuitBreakerConfig
}

// CircuitBreakerConfig holds circuit breaker specific settings
type CircuitBreakerConfig struct {
	Enabled       bool
	MaxRequests   uint32
	Interval      time.Duration
	Timeout       time.Duration
	ReadyToTrip   func(circuitbreaker.Counts) bool
	OnStateChange func(name string, from circuitbreaker.State, to circuitbreaker.State)
}

// DefaultClientConfig returns a sensible default configuration
func DefaultClientConfig(name string) ClientConfig {
	return ClientConfig{
		Name:                  name,
		Timeout:               30 * time.Second,
		MaxIdleConns:          100,
		MaxConnsPerHost:       10,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		CircuitBreaker: CircuitBreakerConfig{
			Enabled:     true,
			MaxRequests: 3,
			Interval:    10 * time.Second,
			Timeout:     30 * time.Second,
			ReadyToTrip: func(counts circuitbreaker.Counts) bool {
				return counts.TotalFailures >= 5
			},
		},
	}
}

// Client wraps http.Client with circuit breaker
type Client struct {
	*http.Client
	cb *circuitbreaker.CircuitBreaker
}

// NewClient creates a new HTTP client with circuit breaker.
//
// Every client built here is hardened against SSRF: the transport refuses to
// dial non-public addresses and revalidates each redirect hop. All of these
// clients are used to fetch tenant-supplied URLs (carrier base URLs, outbound
// webhook endpoints), so without the guard any tenant admin could aim the
// server at the cloud metadata service, the database, or an internal panel.
func NewClient(config ClientConfig) *Client {
	transport := hardenTransport(&http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		MaxIdleConns:          config.MaxIdleConns,
		MaxConnsPerHost:       config.MaxConnsPerHost,
		IdleConnTimeout:       config.IdleConnTimeout,
		TLSHandshakeTimeout:   config.TLSHandshakeTimeout,
		ExpectContinueTimeout: config.ExpectContinueTimeout,
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
		},
	})

	client := &http.Client{
		Transport:     transport,
		Timeout:       config.Timeout,
		CheckRedirect: safeCheckRedirect,
	}

	var cb *circuitbreaker.CircuitBreaker
	if config.CircuitBreaker.Enabled {
		cb = circuitbreaker.NewCircuitBreaker(circuitbreaker.Config{
			Name:        config.Name,
			MaxRequests: config.CircuitBreaker.MaxRequests,
			Interval:    config.CircuitBreaker.Interval,
			Timeout:     config.CircuitBreaker.Timeout,
			ReadyToTrip: config.CircuitBreaker.ReadyToTrip,
			OnStateChange: func(name string, from circuitbreaker.State, to circuitbreaker.State) {
				metrics.RecordCircuitBreakerState(name, int(to))
				if to == circuitbreaker.StateOpen {
					metrics.RecordCircuitBreakerTrip(name)
				}
				// Chain, don't replace: callers that passed their own hook in
				// config must still observe transitions.
				if config.CircuitBreaker.OnStateChange != nil {
					config.CircuitBreaker.OnStateChange(name, from, to)
				}
			},
		})
	}

	return &Client{
		Client: client,
		cb:     cb,
	}
}

// Do executes the request with circuit breaker protection
func (c *Client) Do(req *http.Request) (*http.Response, error) {
	if c.cb == nil {
		return c.Client.Do(req)
	}

	var resp *http.Response
	err := c.cb.Execute(req.Context(), func() error {
		var e error
		resp, e = c.Client.Do(req)
		return e
	})
	if err != nil {
		metrics.RecordCircuitBreakerFailure(c.cb.Name())
	}
	return resp, err
}

// DoWithFallback executes the request with circuit breaker, calling fallback if open
func (c *Client) DoWithFallback(req *http.Request, fallback func() (*http.Response, error)) (*http.Response, error) {
	if c.cb == nil {
		return c.Client.Do(req)
	}

	if c.cb.State() == circuitbreaker.StateOpen {
		return fallback()
	}

	return c.Do(req)
}

// CircuitBreaker returns the underlying circuit breaker for monitoring
func (c *Client) CircuitBreaker() *circuitbreaker.CircuitBreaker {
	return c.cb
}

// Predefined client configurations for common outbound services
var (
	// CarrierAPIClient for carrier webhook polling
	CarrierAPIClient = DefaultClientConfig("carrier-api")

	// NotificationProviderClient for email/SMS/WhatsApp providers
	NotificationProviderClient = DefaultClientConfig("notification-provider")

	// StripeClient for Stripe API calls
	StripeClient = DefaultClientConfig("stripe")

	// SSOClient for OIDC discovery, JWKS fetch, and code exchange
	SSOClient = DefaultClientConfig("sso")

	// EcommerceClient for Shopify/WooCommerce webhooks
	EcommerceClient = DefaultClientConfig("ecommerce")

	// WebhookDispatchClient for tenant outbound webhooks
	WebhookDispatchClient = DefaultClientConfig("webhook-dispatch")
)
