// Package queue — typed job payloads.
//
// Psychology/quality note: untyped map[string]any payloads fail silently
// (misspelled keys → missing IDs → dropped jobs). Typed payloads make the
// contract compiler-checked, so "nothing happening" (the most expensive
// failure) surfaces at build time, not in a dead queue.
package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ── Typed payloads (one per job kind; JSON names are the wire contract) ──

// EvaluateRulesPayload is emitted by shipments.Service.IngestEvent.
type EvaluateRulesPayload struct {
	ShipmentID uuid.UUID `json:"shipmentId"`
	TenantID   uuid.UUID `json:"tenantId"`
	EventID    uuid.UUID `json:"eventId"`
	Code       string    `json:"code"`
	Status     string    `json:"status"`
	Stale      bool      `json:"stale,omitempty"`
}

// AlertNotifyPayload notifies ops of a new/updated alert.
type AlertNotifyPayload struct {
	TenantID   uuid.UUID `json:"tenantId"`
	ShipmentID uuid.UUID `json:"shipmentId"`
	AlertID    uuid.UUID `json:"alertId"`
	// AlertKey overrides the send-idempotency key derived from AlertID.
	// A second notification about the SAME alert (e.g. the SLA-escalation
	// page) must not share the raise-time dedupe key, or the sent-ledger
	// would silently skip the escalation email. Empty = default behaviour.
	AlertKey  string `json:"alertKey,omitempty"`
	Severity  string `json:"severity"`
	Interrupt bool   `json:"interrupt"`
	Kind      string `json:"kind"`
}

// ShipmentPollPayload triggers a carrier poll for one credential.
type ShipmentPollPayload struct {
	CredentialID uuid.UUID `json:"credentialId"`
	TenantID     uuid.UUID `json:"tenantId"`
	Carrier      string    `json:"carrier"`
}

// TenantOnlyPayload covers digest/daily/weekly jobs (tenant is the payload).
type TenantOnlyPayload struct {
	TenantID uuid.UUID `json:"tenantId"`
}

// NotifyCustomerPayload is an explicit operator action (bypasses interrupt
// budget by design — a human clicked, so it is never throttled).
type NotifyCustomerPayload struct {
	ShipmentID  uuid.UUID `json:"shipmentId"`
	TenantID    uuid.UUID `json:"tenantId"`
	Title       string    `json:"title"`
	Message     string    `json:"message"`
	CustomerMsg string    `json:"customerMsg"`
	IsCustom    bool      `json:"isCustom"`
	Nonce       string    `json:"nonce"`
}

// EmailCarrierPayload is an explicit operator action (same bypass rationale).
type EmailCarrierPayload struct {
	ShipmentID uuid.UUID `json:"shipmentId"`
	TenantID   uuid.UUID `json:"tenantId"`
	Title      string    `json:"title"`
	Message    string    `json:"message"`
	Note       string    `json:"note"`
	To         string    `json:"to"`
	Nonce      string    `json:"nonce"`
}

// WebhookDispatchPayload fans out to one tenant endpoint. Payload is open
// JSON by design (tenant event data), so it stays map-shaped; the envelope
// (endpoint/event identity) is typed.
type WebhookDispatchPayload struct {
	EndpointID uuid.UUID      `json:"endpointId"`
	TenantID   uuid.UUID      `json:"tenantId"`
	EventType  string         `json:"eventType"`
	Payload    map[string]any `json:"payload"`
}

// EnqueueTxTenantTyped is the typed door for tenant jobs: payload is
// marshalled once, centrally, so callers cannot mint divergent key spellings.
// T is constrained to structs (not any) so map[string]any cannot slip back in.
func EnqueueTxTenantTyped[T any](ctx context.Context, tx pgx.Tx, tenantID *uuid.UUID, dedupKey, kind string, payload T, runAt time.Time) error {
	return EnqueueTxTenant(ctx, tx, tenantID, dedupKey, kind, payload, runAt)
}

// Decode unmarshals a claimed job body into T with a kind-aware error.
func Decode[T any](kind string, body []byte) (T, error) {
	var p T
	if err := json.Unmarshal(body, &p); err != nil {
		return p, fmt.Errorf("decode %s: %w", kind, err)
	}
	return p, nil
}
