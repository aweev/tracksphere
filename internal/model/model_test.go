package model

import (
	"testing"
	"time"
)

func TestDecodeCarrierEvent_Valid(t *testing.T) {
	body := []byte(`{
		"trackingNumber": "TS-8842-LAG",
		"eventId": "maersk-evt-1",
		"code": "DEPARTED",
		"description": "Vessel departed",
		"location": "Shanghai",
		"occurredAt": "2026-05-16T10:00:00Z",
		"status": "in_transit"
	}`)
	ev, err := DecodeCarrierEvent(body)
	if err != nil {
		t.Fatalf("DecodeCarrierEvent: %v", err)
	}
	if ev.TrackingNumber != "TS-8842-LAG" || ev.Code != "DEPARTED" || ev.Status != StatusInTransit {
		t.Fatalf("unexpected decode: %+v", ev)
	}
	if ev.OccurredAt.Year() != 2026 {
		t.Fatalf("occurredAt parse: %v", ev.OccurredAt)
	}
}

func TestDecodeCarrierEvent_MissingFields(t *testing.T) {
	cases := []string{
		`{"eventId":"x","code":"DEPARTED","occurredAt":"2026-05-16T10:00:00Z"}`, // no tracking
		`{"trackingNumber":"TS-1","code":"DEPARTED","occurredAt":"2026-05-16T10:00:00Z"}`, // no eventId
		`{"trackingNumber":"TS-1","eventId":"x","occurredAt":"2026-05-16T10:00:00Z"}`,     // no code
		`{"trackingNumber":"TS-1","eventId":"x","code":"DEPARTED"}`,                        // no occurredAt
	}
	for i, c := range cases {
		if _, err := DecodeCarrierEvent([]byte(c)); err == nil {
			t.Errorf("case %d: expected error", i)
		}
	}
}

func TestDecodeCarrierEvent_BadStatus(t *testing.T) {
	body := []byte(`{"trackingNumber":"TS-1","eventId":"x","code":"DEPARTED",
		"occurredAt":"2026-05-16T10:00:00Z","status":"warp_speed"}`)
	if _, err := DecodeCarrierEvent(body); err == nil {
		t.Fatal("unknown status must be rejected")
	}
}

func TestValidStatusAndMode(t *testing.T) {
	for _, s := range []string{StatusBooked, StatusInTransit, StatusDelivered, StatusException} {
		if !ValidStatus(s) {
			t.Errorf("ValidStatus(%s) = false", s)
		}
	}
	if ValidStatus("flying") {
		t.Error("bogus status accepted")
	}
	for _, m := range []string{"ocean", "air", "road", "rail"} {
		if !ValidMode(m) {
			t.Errorf("ValidMode(%s) = false", m)
		}
	}
	if ValidMode("submarine") {
		t.Error("bogus mode accepted")
	}
}

// Guard against accidental time import removal.
var _ = time.Now