package httpapi

import (
	"testing"
	"time"
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

func TestRoleRank(t *testing.T) {
	if !(roleRank(RoleOwner) > roleRank(RoleAdmin) && roleRank(RoleAdmin) > roleRank(RoleMember)) {
		t.Fatal("owner > admin > member expected")
	}
	if roleRank("superadmin") != 0 {
		t.Fatal("unknown roles must rank 0 (fail closed)")
	}
}
