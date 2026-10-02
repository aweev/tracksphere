package httpapi

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/tracksphere/tracksphere/internal/model"
	"github.com/tracksphere/tracksphere/internal/shipments"
)

type createShipmentRequest struct {
	TrackingNumber string `json:"trackingNumber"`
	Reference      string `json:"reference"`
	Carrier        string `json:"carrier"`
	Mode           string `json:"mode"`
	Origin         string `json:"origin"`
	Destination    string `json:"destination"`
}

// chiParam reads a chi URL parameter.
func chiParam(r *http.Request, name string) string {
	return chi.URLParam(r, name)
}

// handleCreateShipment POST /api/v1/shipments
func (s *Server) handleCreateShipment(w http.ResponseWriter, r *http.Request) {
	var req createShipmentRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	in := shipments.CreateInput{
		TrackingNumber: strings.TrimSpace(req.TrackingNumber),
		Reference:      strings.TrimSpace(req.Reference),
		Carrier:        strings.ToLower(strings.TrimSpace(req.Carrier)),
		Mode:           req.Mode,
		Origin:         strings.TrimSpace(req.Origin),
		Destination:    strings.TrimSpace(req.Destination),
	}
	if err := in.Validate(); err != nil {
		s.domainError(w, err)
		return
	}
	user := currentUser(r)
	ship, err := s.shipments.Create(r.Context(), user.TenantID, user.ID, in)
	if err != nil {
		s.domainError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, ship)
}

// handleListShipments GET /api/v1/shipments?status=&carrier=&search=&limit=&offset=
func (s *Server) handleListShipments(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	user := currentUser(r)
	rows, total, err := s.shipments.List(r.Context(), user.TenantID, shipments.ListInput{
		Status:  q.Get("status"),
		Carrier: q.Get("carrier"),
		Search:  q.Get("search"),
		Limit:   limit,
		Offset:  offset,
	})
	if err != nil {
		s.domainError(w, err)
		return
	}
	if rows == nil {
		rows = make([]model.Shipment, 0) // JSON [] rather than null
	}
	if limit <= 0 {
		limit = 25
	}
	writeJSONMeta(w, http.StatusOK, rows, &model.Meta{
		Total: total, Limit: limit, Page: offset/limit + 1,
	})
}

// handleGetShipment GET /api/v1/shipments/{id}
func (s *Server) handleGetShipment(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(chiParam(r, "id"))
	if !ok {
		writeError(w, http.StatusBadRequest, "bad_id", "Invalid shipment id")
		return
	}
	user := currentUser(r)
	ship, err := s.shipments.Get(r.Context(), user.TenantID, id)
	if err != nil {
		s.domainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ship)
}

// handleShipmentEvents GET /api/v1/shipments/{id}/events
//
// Returns 404 (not an empty 200) when the shipment is not visible to the
// caller: RLS already withholds the rows, but a 200 would still confirm that
// the given UUID exists in another tenant (resource enumeration). Existence
// checks and the timeline read share one transaction so the result is atomic.
func (s *Server) handleShipmentEvents(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(chiParam(r, "id"))
	if !ok {
		writeError(w, http.StatusBadRequest, "bad_id", "Invalid shipment id")
		return
	}
	user := currentUser(r)
	events, err := s.shipments.Events(r.Context(), user.TenantID, id)
	if err != nil {
		s.domainError(w, err)
		return
	}
	if len(events) == 0 {
		// No timeline rows → confirm the parent shipment is actually visible to
		// this tenant before returning an empty list.
		if _, err := s.shipments.Get(r.Context(), user.TenantID, id); err != nil {
			s.domainError(w, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, events)
}