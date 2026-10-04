package workers

import (
	"strings"
	"testing"
	"time"

	"github.com/tracksphere/tracksphere/internal/model"
)

func TestEvaluateRules_CustomsHold(t *testing.T) {
	got := evaluateRules(model.StatusAtCustoms, "CUSTOMS_HOLD")
	if len(got) != 1 || got[0].Kind != "customs_hold" || got[0].Severity != "critical" {
		t.Fatalf("customs hold rule: %+v", got)
	}
}

func TestEvaluateRules_PortCongestion(t *testing.T) {
	got := evaluateRules(model.StatusInTransit, "PORT_CONGESTION")
	if len(got) != 1 || got[0].Kind != "port_congestion" {
		t.Fatalf("port congestion rule: %+v", got)
	}
}

func TestEvaluateRules_ExceptionStatusRaisesDelay(t *testing.T) {
	got := evaluateRules(model.StatusException, "ETA_REVISED")
	kinds := map[string]bool{}
	for _, o := range got {
		kinds[o.Kind] = true
	}
	if !kinds["eta_revised"] || !kinds["delay"] {
		t.Fatalf("expected eta_revised + delay, got %v", got)
	}
}

// The past-ETA check moved out of the event handler entirely. It was the
// clearest example of a time-based condition evaluated on an event: it fired
// whenever some unrelated scan landed, and once open it never closed, so a day-1
// SLA breach was still "open" at day 90 after the container arrived. It is now
// a sweep rule that reconciles.
func TestEvaluateRules_NoTimeBasedConditionsRemain(t *testing.T) {
	// A late in-transit shipment with a long-past ETA, evaluated against every
	// event code, must never produce a delay warning from this handler.
	for _, code := range []string{"DEPARTED", "ARRIVED", "IN_TRANSIT", "DEPARTED_ORIGIN"} {
		got := evaluateRules(model.StatusInTransit, code)
		for _, o := range got {
			if o.Kind == "delay" || strings.Contains(o.Title, "SLA") {
				t.Fatalf("event handler raised a time-based alert for %q: %+v", code, o)
			}
		}
	}
}

func TestEvaluateRules_ETARevisedIsInfo(t *testing.T) {
	// info must never reach a customer or interrupt: DecideRoute enforces that,
	// and severity has to be right for it to work.
	got := evaluateRules(model.StatusInTransit, "ETA_REVISED")
	if len(got) != 1 || got[0].Severity != "info" {
		t.Fatalf("ETA_REVISED must be info, got %+v", got)
	}
}

func TestEvaluateRules_BlankSailingAndFailedDelivery(t *testing.T) {
	blank := evaluateRules(model.StatusInTransit, "BLANK_SAILING")
	if len(blank) != 1 || blank[0].Severity != "critical" {
		t.Fatalf("blank sailing is a cancellation and must be critical: %+v", blank)
	}
	failed := evaluateRules(model.StatusOutForDelivery, "DELIVERY_ATTEMPTED")
	if len(failed) != 1 || failed[0].Severity != "warning" {
		t.Fatalf("failed delivery attempt: %+v", failed)
	}
}

func TestEvaluateRules_RoutineEventsSilent(t *testing.T) {
	got := evaluateRules(model.StatusInTransit, "DEPARTED")
	if len(got) != 0 {
		t.Fatalf("routine transit event raised alerts: %+v", got)
	}
}

func TestEvaluateRules_DeliveredIsSilent(t *testing.T) {
	if got := evaluateRules(model.StatusDelivered, "DELIVERED"); len(got) != 0 {
		t.Fatalf("delivered shipment must not raise alerts: %+v", got)
	}
}

func TestEstimateETA_Modes(t *testing.T) {
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cases := map[string]time.Duration{
		"ocean": 21 * 24 * time.Hour,
		"air":   3 * 24 * time.Hour,
		"road":  5 * 24 * time.Hour,
		"rail":  10 * 24 * time.Hour,
	}
	for mode, want := range cases {
		got := EstimateETA(mode, created)
		if !got.Equal(created.Add(want)) {
			t.Errorf("EstimateETA(%s) = %v, want %v", mode, got, created.Add(want))
		}
	}
	// Unknown mode falls back to road.
	if !EstimateETA("hyperloop", created).Equal(created.Add(5*24*time.Hour)) {
		t.Error("unknown mode should fall back to road baseline")
	}
}

func TestDecodePayload_MissingIDs(t *testing.T) {
	if _, err := decodePayload([]byte(`{"code":"DEPARTED"}`)); err == nil {
		t.Fatal("expected error for payload without shipmentId/tenantId")
	}
	if _, err := decodePayload([]byte(`not json`)); err == nil {
		t.Fatal("expected error for invalid json")
	}
}