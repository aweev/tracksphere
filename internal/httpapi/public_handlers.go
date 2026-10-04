package httpapi

import (
	"net/http"
	"strings"

	"github.com/tracksphere/tracksphere/internal/model"
)

// handlePublicTrack GET /api/v1/track/{trackingNumber}
// Unauthenticated customer-facing endpoint: returns a SAFE projection of the
// shipment plus its timeline. Internal fields (tenant id, reference, ids of
// ops artifacts) are stripped here — RLS guarantees visibility, this handler
// guarantees minimality.
func (s *Server) handlePublicTrack(w http.ResponseWriter, r *http.Request) {
	tracking := strings.TrimSpace(chiParam(r, "trackingNumber"))
	if tracking == "" {
		writeError(w, http.StatusBadRequest, "bad_tracking", "Tracking number required")
		return
	}

	ship, err := s.shipments.ByTrackingNumber(r.Context(), tracking)
	if err != nil {
		// Uniform 404 — never reveal whether a private shipment exists.
		writeError(w, http.StatusNotFound, "not_found", "No shipment found for this tracking number")
		return
	}

	events, err := s.shipments.PublicEvents(r.Context(), ship.ID)
	if err != nil {
		s.domainError(w, err)
		return
	}
	if events == nil {
		events = make([]model.ShipmentEvent, 0)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"trackingNumber": ship.TrackingNumber,
		"carrier":        ship.Carrier,
		"mode":           ship.Mode,
		"origin":         ship.Origin,
		"destination":    ship.Destination,
		"status":         ship.Status,
		"eta":            ship.ETA,
		// ETA provenance is a trust feature, and it matters more to the
		// customer planning around the date than to the operator reading the
		// same row. Without this the portal could not distinguish a
		// carrier-confirmed date from our own heuristic, which is exactly the
		// ambiguity that makes customers stop trusting a tracker.
		"etaSource":      ship.ETASource,
		"shippedAt":      ship.ShippedAt,
		"deliveredAt":    ship.DeliveredAt,
		"lastUpdate":     ship.UpdatedAt,
		"events":         events,
		"brand":          s.brandFor(r.Context(), ship.TenantID.String()),
	})
}