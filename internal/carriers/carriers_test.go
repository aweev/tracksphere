package carriers

import (
	"context"
	"testing"
	"time"
)

func TestNormalizeStatus(t *testing.T) {
	cases := map[string]string{
		"booked": "booked", "CREATED": "booked",
		"in_transit": "in_transit", "Shipped": "in_transit",
		"customs_hold": "at_customs", "out for delivery": "out_for_delivery",
		"delivered": "delivered", "exception": "exception",
		"cancelled": "cancelled", "mystery": "",
	}
	for in, want := range cases {
		if got := NormalizeStatus(in); got != want {
			t.Errorf("NormalizeStatus(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFakeCarrier(t *testing.T) {
	c := Get("fake")
	if c == nil {
		t.Fatal("fake carrier not registered")
	}
	evs, err := c.Track(context.Background(), "fake://x", Credentials{}, "TS-1", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].Code != "DEPARTED" {
		t.Fatalf("unexpected fake events: %+v", evs)
	}
	if err := c.ValidateTracking("ab"); err == nil {
		t.Fatal("short tracking should fail validation")
	}
}

func TestDetectCarrier(t *testing.T) {
	ups := DetectCarrier("1Z9999W99999999999")
	if len(ups) == 0 || ups[0]["carrier"] != "ups" {
		t.Errorf("1Z should detect ups: %v", ups)
	}
	fedex := DetectCarrier("123456789012")
	found := false
	for _, g := range fedex {
		if g["carrier"] == "fedex" {
			found = true
		}
	}
	if !found {
		t.Errorf("12-digit should suggest fedex: %v", fedex)
	}
	if got := DetectCarrier("!!!"); len(got) != 0 {
		t.Errorf("garbage should yield no guesses: %v", got)
	}
}

func TestKnownSlugs(t *testing.T) {
	for _, s := range []string{"maersk", "dhl", "fedex", "ups", "fake"} {
		if !Known(s) {
			t.Errorf("expected %s to be known", s)
		}
	}
	if Known("acme-express") {
		t.Error("unknown carrier must not be known")
	}
}
