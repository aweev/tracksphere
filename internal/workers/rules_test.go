package workers

import (
	"testing"
	"time"

	"github.com/tracksphere/tracksphere/internal/model"
)

func TestEvaluateRules_CustomsHold(t *testing.T) {
	got := evaluateRules(model.StatusAtCustoms, "CUSTOMS_HOLD", nil, time.Now())
	if len(got) != 1 || got[0].Kind != "customs_hold" || got[0].Severity != "critical" {
		t.Fatalf("customs hold rule: %+v", got)
	}
}

func TestEvaluateRules_PortCongestion(t *testing.T) {
	got := evaluateRules(model.StatusInTransit, "PORT_CONGESTION", nil, time.Now())
	if len(got) != 1 || got[0].Kind != "port_congestion" {
		t.Fatalf("port congestion rule: %+v", got)
	}
}

func TestEvaluateRules_ExceptionStatusRaisesDelay(t *testing.T) {
	got := evaluateRules(model.StatusException, "ETA_REVISED", nil, time.Now())
	kinds := map[string]bool{}
	for _, o := range got {
		kinds[o.Kind] = true
	}
	if !kinds["eta_revised"] || !kinds["delay"] {
		t.Fatalf("expected eta_revised + delay, got %v", got)
	}
}

func TestEvaluateRules_PastETABreach(t *testing.T) {
	past := time.Now().Add(-2 * time.Hour)
	got := evaluateRules(model.StatusInTransit, "DEPARTED", &past, time.Now())
	found := false
	for _, o := range got {
		if o.Kind == "delay" && o.Severity == "warning" {
			found = true
		}
	}
	if !found {
		t.Fatalf("past-ETA should raise delay warning: %+v", got)
	}
}

func TestEvaluateRules_DeliveredPastETASilent(t *testing.T) {
	past := time.Now().Add(-48 * time.Hour)
	if got := evaluateRules(model.StatusDelivered, "DELIVERED", &past, time.Now()); len(got) != 0 {
		t.Fatalf("delivered shipment must not raise alerts: %+v", got)
	}
}

func TestEvaluateRules_RoutineEventsSilent(t *testing.T) {
	future := time.Now().Add(48 * time.Hour)
	got := evaluateRules(model.StatusInTransit, "DEPARTED", &future, time.Now())
	if len(got) != 0 {
		t.Fatalf("routine transit event raised alerts: %+v", got)
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