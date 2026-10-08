package httpapi

import (
	"net/http"
	"testing"
	"time"

	"github.com/tracksphere/tracksphere/internal/config"
)

func TestRateLimiterFixedWindow(t *testing.T) {
	l := NewRateLimiter()
	for i := 0; i < 3; i++ {
		if ok, _ := l.Check("k", 3, time.Minute); !ok {
			t.Fatalf("hit %d should allow", i)
		}
	}
	if ok, retry := l.Check("k", 3, time.Minute); ok {
		t.Fatal("4th hit should block")
	} else if retry <= 0 {
		t.Fatal("blocked hit should report retryAfter")
	}
	if ok, _ := l.Check("other", 3, time.Minute); !ok {
		t.Fatal("different key must not be affected")
	}
}

func TestRateLimiterWindowReset(t *testing.T) {
	l := NewRateLimiter()
	if ok, _ := l.Check("k", 1, 20*time.Millisecond); !ok {
		t.Fatal("first hit should allow")
	}
	if ok, _ := l.Check("k", 1, 20*time.Millisecond); ok {
		t.Fatal("second hit should block")
	}
	time.Sleep(30 * time.Millisecond)
	if ok, _ := l.Check("k", 1, 20*time.Millisecond); !ok {
		t.Fatal("hit after window should allow")
	}
}

// TestClientIPSpoofsIgnored proves the ADR-0010 trust boundary: forwarding
// headers from an UNTRUSTED peer must not move the rate-limit bucket, or any
// caller mints a fresh bucket per request and the public-track enumeration
// defense is decorative.
func TestClientIPSpoofsIgnored(t *testing.T) {
	s := &Server{cfg: &config.Config{}} // no trusted proxies
	req, _ := http.NewRequest("GET", "/api/v1/track/ABC", nil)
	req.RemoteAddr = "198.51.100.7:1234"
	req.Header.Set("X-Forwarded-For", "203.0.113.99")
	req.Header.Set("X-Real-IP", "203.0.113.99")
	req.Header.Set("CF-Connecting-IP", "203.0.113.99")
	if got := s.clientIP(req); got != "198.51.100.7" {
		t.Fatalf("untrusted peer headers must be ignored, got %q", got)
	}

	// Trusted peer: outermost XFF is believed.
	cfg := &config.Config{}
	if err := cfg.SetTestProxies("198.51.100.7/32"); err != nil {
		t.Fatal(err)
	}
	s2 := &Server{cfg: cfg}
	if got := s2.clientIP(req); got != "203.0.113.99" {
		t.Fatalf("trusted peer should believe XFF, got %q", got)
	}
}

func TestRoleRank(t *testing.T) {
	if !(roleRank(RoleOwner) > roleRank(RoleAdmin) && roleRank(RoleAdmin) > roleRank(RoleMember)) {
		t.Fatal("owner > admin > member expected")
	}
	if roleRank("superadmin") != 0 {
		t.Fatal("unknown roles must rank 0 (fail closed)")
	}
}
