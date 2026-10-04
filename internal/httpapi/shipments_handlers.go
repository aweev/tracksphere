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
	IsPublic       *bool  `json:"isPublic"`
}

func chiParam(r *http.Request, name string) string {
	return chi.URLParam(r, name)
}

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
		IsPublic:       req.IsPublic,
	}
	if err := in.Validate(); err != nil {
		s.domainError(w, err)
		return
	}
	user := currentUser(r)
	if s.overShipmentQuota(r.Context(), user.TenantID, s.tenantPlan(r.Context(), user.TenantID)) {
		writeError(w, http.StatusPaymentRequired, "quota_exceeded", "Shipment quota reached for your plan — upgrade to continue")
		return
	}
	ship, err := s.shipments.Create(r.Context(), user.TenantID, user.ID, in)
	if err != nil {
		s.domainError(w, err)
		return
	}
	s.auditShipment(r, user, ship.ID, "shipment.create", map[string]any{"trackingNumber": ship.TrackingNumber})
	writeJSON(w, http.StatusCreated, ship)
}

func (s *Server) handleListShipments(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := 25
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 250 {
			writeError(w, http.StatusBadRequest, "invalid_input", "limit must be 1..250")
			return
		}
		limit = n
	}
	offset := 0
	if raw := q.Get("offset"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, "invalid_input", "offset must be >= 0")
			return
		}
		offset = n
	}
	status := q.Get("status")
	if status != "" && !model.ValidStatus(status) {
		writeError(w, http.StatusBadRequest, "invalid_input", "unknown status")
		return
	}
	sortKey := q.Get("sort")
	switch sortKey {
	case "", "risk", "recent", "stale", "eta":
	default:
		writeError(w, http.StatusBadRequest, "invalid_input", "sort must be risk|recent|stale|eta")
		return
	}
	riskTier := q.Get("riskTier")
	switch riskTier {
	case "", "clear", "watch", "at_risk", "critical":
	default:
		writeError(w, http.StatusBadRequest, "invalid_input", "unknown riskTier")
		return
	}
	hasPosition := q.Get("hasPosition") == "true"

	user := currentUser(r)
	rows, total, err := s.shipments.List(r.Context(), user.TenantID, shipments.ListInput{
		Status:      status,
		Carrier:     strings.ToLower(strings.TrimSpace(q.Get("carrier"))),
		Search:      q.Get("search"),
		Limit:       limit,
		Offset:      offset,
		Sort:        sortKey,
		RiskTier:    riskTier,
		HasPosition: hasPosition,
	})
	if err != nil {
		s.domainError(w, err)
		return
	}
	if rows == nil {
		rows = make([]model.Shipment, 0)
	}
	writeJSONMeta(w, http.StatusOK, rows, &model.Meta{
		Total: total, Limit: limit, Page: offset/limit + 1,
	})
}

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

func (s *Server) handleUpdateShipment(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(chiParam(r, "id"))
	if !ok {
		writeError(w, http.StatusBadRequest, "bad_id", "Invalid shipment id")
		return
	}
	var req struct {
		IsPublic *bool  `json:"isPublic"`
		Status   *string `json:"status"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	in := shipments.UpdateInput{IsPublic: req.IsPublic, Status: req.Status}
	if err := in.Validate(); err != nil {
		s.domainError(w, err)
		return
	}
	user := currentUser(r)
	ship, err := s.shipments.Update(r.Context(), user.TenantID, id, in)
	if err != nil {
		s.domainError(w, err)
		return
	}
	detail := map[string]any{}
	if req.IsPublic != nil {
		detail["isPublic"] = *req.IsPublic
	}
	if req.Status != nil {
		detail["status"] = *req.Status
	}
	s.auditShipment(r, user, id, "shipment.update", detail)
	writeJSON(w, http.StatusOK, ship)
}

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
		if _, err := s.shipments.Get(r.Context(), user.TenantID, id); err != nil {
			s.domainError(w, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, events)
}

type notifyCustomerRequest struct {
	Tracking    string `json:"tracking"`
	Title       string `json:"title"`
	Message     string `json:"message"`
	CustomerMsg string `json:"customerUpdate"`
}

func (s *Server) handleNotifyCustomer(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(chiParam(r, "id"))
	if !ok {
		writeError(w, http.StatusBadRequest, "bad_id", "Invalid shipment id")
		return
	}
	var req notifyCustomerRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	user := currentUser(r)
	ship, err := s.shipments.Get(r.Context(), user.TenantID, id)
	if err != nil {
		s.domainError(w, err)
		return
	}
	// Queue a customer notification for this shipment
	err = s.svc.NotifyCustomer(r.Context(), user.TenantID, ship.ID, req.Title, req.Message, req.CustomerMsg)
	if err != nil {
		s.domainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

type emailCarrierRequest struct {
	Tracking string `json:"tracking"`
	Title    string `json:"title"`
	Message  string `json:"message"`
	Note     string `json:"note"`
}

func (s *Server) handleEmailCarrier(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(chiParam(r, "id"))
	if !ok {
		writeError(w, http.StatusBadRequest, "bad_id", "Invalid shipment id")
		return
	}
	var req emailCarrierRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	user := currentUser(r)
	ship, err := s.shipments.Get(r.Context(), user.TenantID, id)
	if err != nil {
		s.domainError(w, err)
		return
	}
	err = s.svc.EmailCarrier(r.Context(), user.TenantID, ship.ID, req.Title, req.Message, req.Note)
	if err != nil {
		s.domainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}