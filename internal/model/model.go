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
}

// User is the authenticated principal.
type User struct {
	ID          uuid.UUID `json:"id"`
	TenantID    uuid.UUID `json:"tenantId"`
	Email       string    `json:"email"`
	Name        string    `json:"name"`
	Role        string    `json:"role"`
	TOTPEnabled bool      `json:"totpEnabled"`
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
