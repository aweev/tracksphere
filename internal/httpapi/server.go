// Package httpapi implements the REST API: routing, middleware (auth, CORS,
// logging, recovery), handlers and the SSE stream endpoint.
package httpapi

import (
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tracksphere/tracksphere/internal/config"
	"github.com/tracksphere/tracksphere/internal/realtime"
	"github.com/tracksphere/tracksphere/internal/shipments"
)

// Server aggregates dependencies for all handlers.
type Server struct {
	cfg        *config.Config
	pool       *pgxpool.Pool
	shipments  *shipments.Repository
	svc        *shipments.Service
	hub        *realtime.Hub
	log        *slog.Logger
	startedAt  time.Time
	limiter    *RateLimiter
}

// NewServer constructs the API server.
func NewServer(
	cfg *config.Config,
	pool *pgxpool.Pool,
	repo *shipments.Repository,
	svc *shipments.Service,
	hub *realtime.Hub,
	log *slog.Logger,
) *Server {
	return &Server{
		cfg: cfg, pool: pool, shipments: repo, svc: svc,
		hub: hub, log: log, startedAt: time.Now(),
		limiter: NewRateLimiter(),
	}
}

// Router builds the chi router with the full route table.
func (s *Server) Router() http.Handler {
	r := chi.NewRouter()

	// ── Global middleware ──────────────────────────────────────────────
	//
	// NOTE: chi's middleware.RealIP is deliberately NOT used. It
	// unconditionally rewrites RemoteAddr from X-Forwarded-For / X-Real-IP
	// supplied by ANY client, which lets a caller mint a fresh rate-limit
	// bucket per request and defeats the limiter that protects the public
	// tracking endpoint from enumeration. Server.clientIP trusts forwarding
	// headers only when the peer is in TRACKSPHERE_TRUSTED_PROXIES.
	r.Use(middleware.RequestID)
	r.Use(s.requestLogger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(30 * time.Second))
	r.Use(s.cors)
	r.Use(s.cspNonceMiddleware)
	r.Use(s.versioningMiddleware)
	// Compression buffers, which turns live SSE events into a trickle. It is
	// applied everywhere except the event stream, where latency is the feature.
	r.Use(compressExceptSSE(5))

	r.Route("/api/v1", func(r chi.Router) {
		// Public, unauthenticated — all rate-limited per IP.
		r.Get("/health", s.handleHealth)
		r.Get("/statusz", s.handleStatusz)
		r.Get("/metrics", s.rateLimit("metrics", 30, time.Minute, s.handleMetrics))
		r.Post("/auth/register", s.rateLimit("register", 5, time.Minute, s.handleRegister))
		r.Post("/auth/login", s.rateLimit("login", 10, time.Minute, s.handleLogin))
		r.Post("/auth/mfa/verify", s.rateLimit("mfa", 10, time.Minute, s.handleMFAVerify))
		r.Get("/auth/sso/status", s.handleSSOStatus)
		r.Get("/auth/sso/{provider}", s.rateLimit("login", 10, time.Minute, s.handleSSOStart))
		r.Get("/auth/sso/{provider}/callback", s.rateLimit("login", 10, time.Minute, s.handleSSOCallback))

		// Carrier webhooks (HMAC-signed, no session)
		r.Post("/webhooks/carriers/{carrier}", s.rateLimit("webhook", 120, time.Minute, s.handleCarrierWebhook))

		// E-commerce order webhooks (per-connection HMAC, no session)
		r.Post("/webhooks/commerce/{provider}", s.rateLimit("webhook", 60, time.Minute, s.handleCommerceOrder))

		// Stripe webhooks (Stripe-Signature, no session)
		r.Post("/billing/webhook", s.rateLimit("stripe", 120, time.Minute, s.handleStripeWebhook))

		// Public tracking portal.
		//
		// Two independent budgets per route: one keyed on the source address,
		// one on a fingerprint of the tracking number. Tracking numbers are
		// low-entropy (MRKU + 6 digits + check), so the space is walkable; the
		// subject budget is what actually bounds an enumeration sweep, and the
		// IP budget is what bounds credential stuffing. Rotating source
		// addresses defeats the IP budget and nothing else.
		r.Get("/track/{trackingNumber}",
			s.rateLimit("track", 60, time.Minute,
				s.rateLimitSubject("track-space", 120, time.Hour, trackingOf,
					s.handlePublicTrack)))
		r.Post("/track/{trackingNumber}/subscribe",
			s.rateLimit("subscribe", 10, time.Minute,
				s.rateLimitSubject("subscribe-space", 30, time.Hour, trackingOf,
					s.handleSubscribe)))
		// Double opt-in completion and one-click opt-out. Public by necessity:
		// the recipient holds no account.
		r.Get("/subscribe/confirm", s.rateLimit("subscribe", 20, time.Minute, s.handleConfirmSubscribe))
		r.Post("/subscribe/unsubscribe", s.rateLimit("subscribe", 10, time.Minute, s.handleUnsubscribe))
		// Live updates for the tracker. The public portal is the surface the
		// customer actually watches, and it was a one-shot snapshot until this
		// existed — the one screen you demo was the only one not live.
		r.Get("/track/{trackingNumber}/stream", s.rateLimit("track", 30, time.Minute, s.handlePublicStream))

		r.Group(func(r chi.Router) {
			r.Use(s.requireAuth)

			r.Get("/auth/me", s.requireRole(RoleMember, s.handleMe))
			r.Post("/auth/logout", s.requireRole(RoleMember, s.handleLogout))
			r.Post("/auth/mfa/enroll", s.requireRole(RoleMember, s.handleMFAEnroll))
			r.Post("/auth/mfa/enable", s.requireRole(RoleMember, s.handleMFAEnable))
			r.Post("/auth/mfa/disable", s.requireRole(RoleMember, s.handleMFADisable))
			r.Get("/auth/sessions", s.requireRole(RoleMember, s.handleListSessions))
			r.Delete("/auth/sessions/{id}", s.requireRole(RoleMember, s.handleRevokeSession))
			r.Post("/auth/password/change", s.requireRole(RoleMember, s.handleChangePassword))

			r.Get("/team", s.requireRole(RoleAdmin, s.handleListTeam))
			r.Post("/team/invite", s.requireRole(RoleAdmin, s.handleInvite))
			r.Post("/team/{id}/deactivate", s.requireRole(RoleAdmin, s.handleDeactivate))

r.Get("/dashboard", s.requireRole(RoleMember, s.handleDashboard))
		r.Get("/billing", s.requireRole(RoleMember, s.handleBilling))
		r.Get("/shipments", s.requireRole(RoleMember, s.handleListShipments))
		// Mutations are admin+ : members are read-mostly, so a compromised
		// member seat cannot fabricate shipments or rewrite history.
		r.Post("/shipments", s.requireRole(RoleAdmin, s.handleCreateShipment))
		r.Post("/shipments:batch", s.requireRole(RoleAdmin, s.handleBatchCreate))
		r.Get("/shipments/{id}", s.requireRole(RoleMember, s.handleGetShipment))
		r.Patch("/shipments/{id}", s.requireRole(RoleAdmin, s.handleUpdateShipment))
		r.Get("/shipments/{id}/events", s.requireRole(RoleMember, s.handleShipmentEvents))
		r.Post("/shipments/{id}/events", s.requireRole(RoleAdmin, s.handleManualEvent))

		r.Get("/alerts", s.requireRole(RoleMember, s.handleListAlerts))
		r.Post("/alerts/{id}/resolve", s.requireRole(RoleAdmin, s.resolveHandler))
		r.Post("/alerts/{id}/assign", s.requireRole(RoleAdmin, s.handleAssignAlert))
		r.Post("/alerts/{id}/note", s.requireRole(RoleAdmin, s.handleNoteAlert))

			r.Get("/analytics", s.requireRole(RoleMember, s.handleAnalytics))
			r.Get("/analytics/digest", s.requireRole(RoleMember, s.handleDigest))
			r.Get("/notifications", s.requireRole(RoleMember, s.handleListNotifications))
			r.Get("/notify/status", s.requireRole(RoleMember, s.handleNotifyStatus))

			// P2 growth surfaces
			r.Get("/carriers", s.requireRole(RoleAdmin, s.handleListCarriers))
			r.Post("/carriers", s.requireRole(RoleAdmin, s.handleUpsertCarrier))
			r.Get("/carriers/detect", s.requireRole(RoleMember, s.handleDetectCarrier))
			r.Delete("/carriers/{carrier}", s.requireRole(RoleAdmin, s.handleDeleteCarrier))
			r.Post("/carriers/{carrier}/poll", s.requireRole(RoleAdmin, s.handleTriggerPoll))

r.Get("/shipments/{id}/legs", s.requireRole(RoleMember, s.handleListLegs))
		r.Post("/shipments/{id}/legs", s.requireRole(RoleAdmin, s.handleCreateLeg))
		r.Delete("/legs/{id}", s.requireRole(RoleAdmin, s.handleDeleteLeg))

		r.Get("/shipments/{id}/documents", s.requireRole(RoleMember, s.handleListDocuments))
		r.Post("/shipments/{id}/documents", s.requireRole(RoleAdmin, s.handleUploadDocument))
		r.Get("/documents/{id}/download", s.requireRole(RoleMember, s.handleDownloadDocument))
		r.Delete("/documents/{id}", s.requireRole(RoleAdmin, s.handleDeleteDocument))
		r.Get("/shipments/{id}/audit", s.requireRole(RoleMember, s.handleListShipmentAudit))

			r.Get("/branding", s.requireRole(RoleMember, s.handleGetBranding))
			r.Put("/branding", s.requireRole(RoleAdmin, s.handleUpdateBranding))

			r.Get("/integrations/commerce", s.requireRole(RoleAdmin, s.handleListEcommerce))
			r.Post("/integrations/commerce", s.requireRole(RoleAdmin, s.handleConnectEcommerce))
			r.Delete("/integrations/commerce/{id}", s.requireRole(RoleAdmin, s.handleDeleteEcommerce))

			r.Post("/billing/checkout", s.requireRole(RoleAdmin, s.handleCreateCheckout))

			r.Get("/account/export", s.requireRole(RoleAdmin, s.handleExportAccount))
			r.Patch("/account/theme", s.requireRole(RoleMember, s.handleUpdateTheme))
			r.Delete("/account", s.requireRole(RoleOwner, s.handleEraseAccount))
			r.Get("/compliance/evidence", s.requireRole(RoleOwner, s.handleEvidence))

			r.Post("/mcp", s.requireRole(RoleMember, s.handleMCP))

			r.Get("/webhooks/inbox", s.requireRole(RoleMember, s.handleListWebhookInbox))
			r.Get("/webhooks/out", s.requireRole(RoleAdmin, s.handleListWebhookEndpoints))
			r.Post("/webhooks/out", s.requireRole(RoleAdmin, s.handleCreateWebhookEndpoint))
			r.Delete("/webhooks/out/{id}", s.requireRole(RoleAdmin, s.handleDeleteWebhookEndpoint))
			r.Get("/webhooks/out/{id}/deliveries", s.requireRole(RoleAdmin, s.handleListWebhookDeliveries))

			r.Get("/apikeys", s.requireRole(RoleAdmin, s.handleListAPIKeys))
			r.Post("/apikeys", s.requireRole(RoleAdmin, s.handleCreateAPIKey))
			r.Post("/apikeys/{id}/revoke", s.requireRole(RoleAdmin, s.handleRevokeAPIKey))

			// Dead-letter queue is cross-tenant ops surface: admin+.
			r.Get("/jobs/dead", s.requireRole(RoleAdmin, s.handleListDeadJobs))
			r.Post("/jobs/dead/{id}/replay", s.requireRole(RoleAdmin, s.handleReplayDeadJob))

			r.Get("/stream", s.requireRole(RoleMember, s.handleStream))
		})
	})

	return r
}

// compressExceptSSE gzips ordinary responses but leaves the event stream alone.
// chi's Compress middleware buffers through a gzip writer, and a buffered
// event stream defeats the entire point of SSE: clients see updates in bursts
// whenever the compressor happens to flush.
func compressExceptSSE(level int) func(http.Handler) http.Handler {
	compress := middleware.Compress(level)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/stream") {
				next.ServeHTTP(w, r)
				return
			}
			compress(next).ServeHTTP(w, r)
		})
	}
}

// parseID is a tiny helper shared by handlers.
func parseID(raw string) (uuid.UUID, bool) {
	id, err := uuid.Parse(raw)
	return id, err == nil
}

// trackingOf extracts the tracking-number path segment for subject-keyed
// rate limiting.
func trackingOf(r *http.Request) string {
	return strings.TrimSpace(chiParam(r, "trackingNumber"))
}