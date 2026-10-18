package httpapi

import (
	"testing"

	"github.com/google/uuid"
)

// validResolution must accept exactly the set the 000016 CHECK allows —
// a mismatch either way is a 500 in production (constraint rejects at write
// time after the handler passed).
func TestValidResolution(t *testing.T) {
	valid := []string{
		"",
		"condition_cleared",
		"customer_notified",
		"carrier_contacted",
		"documents_filed",
		"delivered",
		"duplicate",
		"false_positive",
		"wont_fix",
	}
	for _, s := range valid {
		if !validResolution(s) {
			t.Errorf("validResolution(%q) = false, want true", s)
		}
	}
	invalid := []string{
		"FALSE_POSITIVE",      // case-sensitive, matches the DB CHECK
		"fals_positive",       // typo guard
		"customer notified",   // space instead of underscore
		"resolved",            // looks right, is not in the enum
		"'; DROP TABLE users", // injection-shaped input
	}
	for _, s := range invalid {
		if validResolution(s) {
			t.Errorf("validResolution(%q) = true, want false", s)
		}
	}
}

// normalizeCustomDomain accepts bare hostnames only: a stored scheme or path
// would corrupt every link built on this column.
func TestNormalizeCustomDomain(t *testing.T) {
	ok := map[string]string{
		"":                     "", // clearing the domain is allowed
		"  ":                   "",
		"portal.example.com":   "portal.example.com",
		"Portal.Example.COM":   "portal.example.com", // lowercased
		" track.example.com  ": "track.example.com",
		"a-b.example.co.uk":    "a-b.example.co.uk",
	}
	for in, want := range ok {
		got, err := normalizeCustomDomain(in)
		if err != nil || got != want {
			t.Errorf("normalizeCustomDomain(%q) = %q, %v; want %q, nil", in, got, err, want)
		}
	}
	bad := []string{
		"https://portal.example.com", // scheme
		"portal.example.com/track",   // path
		"portal.example.com#f",       // fragment
		"not-a-domain",               // no dot
		"a.b",                        // no dot after trim… dot present, but 3 chars — blocked by length
		"portal exämple.com",         // non-ASCII
		"portal.example .com",        // space
		"..bad..",                    // malformed
	}
	for _, in := range bad {
		if _, err := normalizeCustomDomain(in); err == nil {
			t.Errorf("normalizeCustomDomain(%q) = nil error, want rejection", in)
		}
	}
}

// Unknown quota kinds must deny closed WITHOUT touching the DB — a typo in a
// future call site must fail the request, not silently lift the limit.
func TestOverCountQuotaUnknownKindDeniesClosed(t *testing.T) {
	var s *Server // nil: unknown kinds return before any field deref; a DB
	// touch would panic and fail the test, which is exactly the property we
	// want to pin.
	if !s.overCountQuota(t.Context(), uuid.New(), "growth", "webhooks") {
		t.Fatal("overCountQuota unknown kind = false, want true (deny closed)")
	}
}

// modeForCarrier drives the staleness/sla norms per mode; a parcel routed as
// "road" would wait 5 days before surfacing as an exception.
func TestModeForCarrier(t *testing.T) {
	cases := map[string]string{
		"maersk": "ocean", "msc": "ocean", "cma-cgm": "ocean",
		"cma_cgm": "ocean", "hapag": "ocean", "cosco": "ocean",
		"one": "ocean", "evergreen": "ocean",
		"fedex": "air", "dhl": "air", "ups": "air",
		"unknown": "road", "": "road", "MAERSK": "road", // handler lowercases before calling
	}
	for carrier, want := range cases {
		if got := modeForCarrier(carrier); got != want {
			t.Errorf("modeForCarrier(%q) = %q, want %q", carrier, got, want)
		}
	}
}
