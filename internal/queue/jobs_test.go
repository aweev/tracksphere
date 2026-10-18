package queue

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
)

// Typed payloads must round-trip through the wire: producers marshal once,
// consumers decode. A field rename that compiles but changes JSON would
// silently drop jobs (most expensive failure = nothing happening).
func TestTypedPayloadsRoundTrip(t *testing.T) {
	tenant, ship, alert, cred, event := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()

	cases := []struct {
		name string
		in   any
		keys []string
	}{
		{"evaluate", EvaluateRulesPayload{ShipmentID: ship, TenantID: tenant, EventID: event, Code: "X", Status: "s", Stale: true}, []string{"shipmentId", "tenantId", "eventId", "code", "status", "stale"}},
		{"alert", AlertNotifyPayload{TenantID: tenant, ShipmentID: ship, AlertID: alert, Severity: "critical", Interrupt: true, Kind: "k"}, []string{"tenantId", "shipmentId", "alertId", "severity", "interrupt", "kind"}},
		{"poll", ShipmentPollPayload{CredentialID: cred, TenantID: tenant, Carrier: "maersk"}, []string{"credentialId", "tenantId", "carrier"}},
		{"tenant", TenantOnlyPayload{TenantID: tenant}, []string{"tenantId"}},
		{"notify", NotifyCustomerPayload{ShipmentID: ship, TenantID: tenant, Nonce: "n"}, []string{"shipmentId", "tenantId", "nonce"}},
		{"email", EmailCarrierPayload{ShipmentID: ship, TenantID: tenant, Nonce: "n"}, []string{"shipmentId", "tenantId", "nonce"}},
		{"webhook", WebhookDispatchPayload{EndpointID: cred, TenantID: tenant, EventType: "shipment.updated"}, []string{"endpointId", "tenantId", "eventType"}},
	}
	for _, c := range cases {
		raw, err := json.Marshal(c.in)
		if err != nil {
			t.Fatalf("%s marshal: %v", c.name, err)
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("%s re-decode: %v", c.name, err)
		}
		for _, k := range c.keys {
			if _, ok := m[k]; !ok {
				t.Errorf("%s: missing wire key %q in %s", c.name, k, raw)
			}
		}
	}

	// Generic Decode helper accepts the same bytes handlers receive.
	raw, _ := json.Marshal(EvaluateRulesPayload{ShipmentID: ship, TenantID: tenant, EventID: event})
	got, err := Decode[EvaluateRulesPayload]("shipment.evaluate", raw)
	if err != nil || got.ShipmentID != ship || got.TenantID != tenant {
		t.Fatalf("Decode[EvaluateRulesPayload] = %+v, %v", got, err)
	}
}
