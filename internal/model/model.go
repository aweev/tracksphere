// Package model defines the domain types shared across the API, workers and
// persistence layer. JSON tags are the public API contract (camelCase).
package model

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// Shipment statuses (mirror the CHECK constraint in 000001_init.sql).
const (
	StatusBooked        = "booked"
	StatusInTransit     = "in_transit"
	StatusAtCustoms     = "at_customs"
	StatusOutForDelivery = "out_for_delivery"
	StatusDelivered     = "delivered"
	StatusException     = "exception"
	StatusCancelled     = "cancelled"
)

// ValidStatus reports whether s is a known shipment status.
func ValidStatus(s string) bool {
	switch s {
	case StatusBooked, StatusInTransit, StatusAtCustoms, StatusOutForDelivery,
		StatusDelivered, StatusException, StatusCancelled:
		return true
	}
	return false
}

// ValidMode reports whether m is a known transport mode.
func ValidMode(m string) bool {
	return m == "ocean" || m == "air" || m == "road" || m == "rail"
}

// Shipment is the core aggregate.
type Shipment struct {
	ID             uuid.UUID  `json:"id"`
	TenantID       uuid.UUID  `json:"-"`
	TrackingNumber string     `json:"trackingNumber"`
	Reference      string     `json:"reference"`
	Carrier        string     `json:"carrier"`
	Mode           string     `json:"mode"`
	Origin         string     `json:"origin"`
	Destination    string     `json:"destination"`
	Status         string     `json:"status"`
	ETA            *time.Time `json:"eta,omitempty"`
	ShippedAt      *time.Time `json:"shippedAt,omitempty"`
	DeliveredAt    *time.Time `json:"deliveredAt,omitempty"`
	IsPublic       bool       `json:"isPublic"`
	CreatedAt      time.Time  `json:"createdAt"`
	UpdatedAt      time.Time  `json:"updatedAt"`

	// ── Read-model fields (shipment_current) ────────────────────────────
	// Present on list views. These are the numbers that decide what an
	// operator works next, and they are computed once in the read model rather
	// than per request, so the list can be sorted by consequence.

	// RiskScore 0-100, higher is worse. RiskTier is the display bucket.
	RiskScore *int    `json:"riskScore,omitempty"`
	RiskTier  *string `json:"riskTier,omitempty"`

	// RiskBreakdown is the per-term explanation exactly as Score() computed it,
	// passed through from shipment_current. Render it; never recompute the
	// weights client-side. RawMessage (not a struct) so model does not import
	// the readmodel package it would otherwise cycle with.
	RiskBreakdown json.RawMessage `json:"riskBreakdown,omitempty"`

	// StaleHours is how long the carrier has been silent. This is the exception
	// nobody can report by eye and it produces no event at all.
	StaleHours *float64 `json:"staleHours,omitempty"`

	OpenAlerts     *int `json:"openAlerts,omitempty"`
	CriticalAlerts *int `json:"criticalAlerts,omitempty"`

	// ETA provenance. A carrier-published ETA and our own estimate must never
	// render identically, or operators stop believing every number on screen.
	ETASource     string   `json:"etaSource,omitempty"`
	ETAConfidence *float64 `json:"etaConfidence,omitempty"`

	ValueAtRisk      *float64 `json:"valueAtRisk,omitempty"`
	CustomerNotified *bool    `json:"customerNotified,omitempty"`

	DwellHours         *float64 `json:"dwellHours,omitempty"`
	ExpectedDwellHours *float64 `json:"expectedDwellHours,omitempty"`
	DwellRatio         *float64 `json:"dwellRatio,omitempty"`

	// Latest known position, from the newest geocoded checkpoint.
	Lat *float64 `json:"lat,omitempty"`
	Lng *float64 `json:"lng,omitempty"`
}

// ShipmentEvent is one point on the timeline.
type ShipmentEvent struct {
	ID          uuid.UUID  `json:"id"`
	ShipmentID  uuid.UUID  `json:"shipmentId"`
	Carrier     string     `json:"carrier"`
	Code        string     `json:"code"`
	Description string     `json:"description"`
	Location    string     `json:"location"`
	Lat         *float64   `json:"lat,omitempty"`
	Lng         *float64   `json:"lng,omitempty"`
	OccurredAt  time.Time  `json:"occurredAt"`
	ReceivedAt  time.Time  `json:"receivedAt"`
	Source      string     `json:"source"`
}

// Alert is an exception raised by the rules engine.
type Alert struct {
	ID         uuid.UUID  `json:"id"`
	ShipmentID uuid.UUID  `json:"shipmentId"`
	Kind       string     `json:"kind"`
	Severity   string     `json:"severity"`
	Title      string     `json:"title"`
	Message    string     `json:"message"`
	Status     string     `json:"status"`
	CreatedAt  time.Time  `json:"createdAt"`
	ResolvedAt *time.Time `json:"resolvedAt,omitempty"`

	// Ownership loop. An alert with no assignee belongs to nobody, so in a
	// team nobody works it — unowned work is invisible work.
	AssignedTo    *uuid.UUID `json:"assignedTo,omitempty"`
	AssignedToName string    `json:"assignedToName,omitempty"`
	AssignedAt    *time.Time `json:"assignedAt,omitempty"`
	AcknowledgedAt *time.Time `json:"acknowledgedAt,omitempty"`

	// SLA pressure. due_at is set from the tenant's per-severity target when
	// the alert is raised; overrunning it escalates.
	DueAt       *time.Time `json:"dueAt,omitempty"`
	EscalatedAt *time.Time `json:"escalatedAt,omitempty"`
	SnoozedUntil *time.Time `json:"snoozedUntil,omitempty"`

	// Closure feedback. The root cause is what makes carrier scorecards and
	// ETA calibration possible, so closing an alert without one is recorded as
	// "other" rather than silently discarding the signal.
	RootCause   string  `json:"rootCause,omitempty"`
	Note        string  `json:"note,omitempty"`
	Resolution  string  `json:"resolution,omitempty"`
	ResolvedBy  *uuid.UUID `json:"resolvedBy,omitempty"`
	ValueAtRisk *float64 `json:"valueAtRisk,omitempty"`

	// Detection window. detected_at is when the condition first became true;
	// last_seen_at is the sweep that most recently confirmed it still is. That
	// gap is what distinguishes a live exception from a stale one.
	DetectedAt time.Time `json:"detectedAt"`
	LastSeenAt time.Time `json:"lastSeenAt"`

	// Parent-shipment context (list views only; omitted when empty).
	TrackingNumber string     `json:"trackingNumber,omitempty"`
	ShipmentStatus string     `json:"shipmentStatus,omitempty"`
	ShipmentETA    *time.Time `json:"shipmentEta,omitempty"`
	// Derived read-model context so the queue can render the seven questions
	// an exception card must answer without a second round trip.
	RiskScore       *int     `json:"riskScore,omitempty"`
	RiskTier        string   `json:"riskTier,omitempty"`
	StaleHours      *float64 `json:"staleHours,omitempty"`
	OpenAlertCount  int      `json:"openAlertCount,omitempty"`
	CustomerNotified bool    `json:"customerNotified,omitempty"`
}

// User is the authenticated principal.
type User struct {
	ID          uuid.UUID `json:"id"`
	TenantID    uuid.UUID `json:"tenantId"`
	Email       string    `json:"email"`
	Name        string    `json:"name"`
	Role        string    `json:"role"`
	TOTPEnabled bool      `json:"totpEnabled"`
	Theme       string    `json:"theme"` // system | light | dark
	CreatedAt   time.Time `json:"createdAt"`
}

// Tenant is the isolation boundary (a customer organization).
type Tenant struct {
	ID        uuid.UUID `json:"id"`
	Name      string    `json:"name"`
	Slug      string    `json:"slug"`
	Plan      string    `json:"plan"`
	CreatedAt time.Time `json:"createdAt"`
}

// DashboardStats powers the operations dashboard.
type DashboardStats struct {
	ActiveShipments   int64 `json:"activeShipments"`
	DeliveredShipments int64 `json:"deliveredShipments"`
	ExceptionShipments int64 `json:"exceptionShipments"`
	OverdueShipments  int64 `json:"overdueShipments"`
	OpenAlerts        int64 `json:"openAlerts"`
}

// CarrierEvent is the normalized inbound webhook contract
// (POST /api/v1/webhooks/carriers/{carrier}).
type CarrierEvent struct {
	TrackingNumber string     `json:"trackingNumber"`
	EventID        string     `json:"eventId"`        // carrier-unique; builds dedup key
	Code           string     `json:"code"`           // BOOKED | DEPARTED | ...
	Description     string     `json:"description"`
	Location       string     `json:"location"`
	Lat            *float64   `json:"lat,omitempty"`
	Lng            *float64   `json:"lng,omitempty"`
	OccurredAt     time.Time  `json:"occurredAt"`
	Status         string     `json:"status"`         // resulting shipment status
	ETA            *time.Time `json:"eta,omitempty"`  // revised ETA, if any
}

// DecodeCarrierEvent parses and validates a webhook body.
func DecodeCarrierEvent(body []byte) (*CarrierEvent, error) {
	var ev CarrierEvent
	if err := json.Unmarshal(body, &ev); err != nil {
		return nil, err
	}
	if ev.TrackingNumber == "" || ev.EventID == "" || ev.Code == "" || ev.OccurredAt.IsZero() {
		return nil, errMissingFields
	}
	if ev.Status != "" && !ValidStatus(ev.Status) {
		return nil, errBadStatus
	}
	return &ev, nil
}

// Envelope is the standard API response wrapper.
type Envelope struct {
	Data  any        `json:"data,omitempty"`
	Error *APIError  `json:"error,omitempty"`
	Meta  *Meta      `json:"meta,omitempty"`
}

// Meta carries pagination info. Total is always emitted (even 0) so clients
// can distinguish "empty page" from "field absent".
type Meta struct {
	Total int64 `json:"total"`
	Limit int   `json:"limit,omitempty"`
	Page  int   `json:"page,omitempty"`
}

// APIError is the machine-readable error body.
type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
