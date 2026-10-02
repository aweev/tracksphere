// Package httpapi implements the REST API: routing, middleware (auth, CORS,
// logging, recovery), handlers and the SSE stream endpoint.
package httpapi

import (
	"log/slog"
	"net/http"
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
	}
}

// Router builds the chi router with the full route table.
func (s *Server) Router() http.Handler {
	r := chi.NewRouter()

	// ── Global middleware ──────────────────────────────────────────────
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(s.requestLogger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(30 * time.Second))
	r.Use(s.cors)
	r.Use(middleware.Compress(5))

	r.Route("/api/v1", func(r chi.Router) {
		// Public, unauthenticated
		r.Get("/health", s.handleHealth)
		r.Post("/auth/register", s.handleRegister)
		r.Post("/auth/login", s.handleLogin)
		r.Post("/auth/mfa/verify", s.handleMFAVerify)

		// Carrier webhooks (HMAC-signed, no session)
		r.Post("/webhooks/carriers/{carrier}", s.handleCarrierWebhook)

		// Public tracking portal
		r.Get("/track/{trackingNumber}", s.handlePublicTrack)

		// Authenticated area
		r.Group(func(r chi.Router) {
			r.Use(s.requireAuth)

			r.Get("/auth/me", s.handleMe)
			r.Post("/auth/logout", s.handleLogout)
			r.Post("/auth/mfa/enroll", s.handleMFAEnroll)
			r.Post("/auth/mfa/enable", s.handleMFAEnable)
			r.Post("/auth/mfa/disable", s.handleMFADisable)

			r.Get("/dashboard", s.handleDashboard)
			r.Get("/shipments", s.handleListShipments)
			r.Post("/shipments", s.handleCreateShipment)
			r.Get("/shipments/{id}", s.handleGetShipment)
			r.Get("/shipments/{id}/events", s.handleShipmentEvents)
			r.Post("/shipments/{id}/events", s.handleManualEvent)

			r.Get("/alerts", s.handleListAlerts)
			r.Post("/alerts/{id}/resolve", s.handleResolveAlert)

			r.Get("/stream", s.handleStream)
		})
	})

	return r
}

// parseID is a tiny helper shared by handlers.
func parseID(raw string) (uuid.UUID, bool) {
	id, err := uuid.Parse(raw)
	return id, err == nil
}