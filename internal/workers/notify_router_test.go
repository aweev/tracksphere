package workers

import (
	"testing"
	"time"
)

func utc(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("UTC")
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func testPrefs() Prefs {
	return Prefs{Timezone: "UTC", QuietHoursStart: 22, QuietHoursEnd: 7,
		DigestHour: 8, InterruptCap: 5, ShipmentCooldownM: 360}
}

func TestSeverityPolicy_InfoNeverInterrupts(t *testing.T) {
	for _, sev := range []string{"info", "warning", "critical"} {
		mayInterrupt, tellsCustomer := severityPolicy(sev)
		if sev == "info" && mayInterrupt {
			t.Fatal("info must never be allowed to interrupt")
		}
		if sev == "info" && tellsCustomer {
			t.Fatal("info must never be pushed to a customer")
		}
		if sev == "critical" && !mayInterrupt {
			t.Fatal("critical must be interruptible")
		}
	}
}

func TestDecideRoute_InfoIsNeverAnInterrupt(t *testing.T) {
	// Even with a rule explicitly asking to interrupt and no budget spent, info
	// goes to the digest. This is the guarantee that keeps the queue readable.
	for _, requested := range []bool{true, false} {
		r := DecideRoute("info", requested, testPrefs(), utc(t),
			time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC), 0, nil)
		if r.Interrupt {
			t.Fatalf("info interrupted (requested=%v); must never", requested)
		}
		if !r.QueueDigest {
			t.Fatal("suppressed info must still reach the digest, not be dropped")
		}
	}
}

func TestDecideRoute_WarningNeverInterrupts(t *testing.T) {
	r := DecideRoute("warning", true, testPrefs(), utc(t),
		time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC), 0, nil)
	if r.Interrupt {
		t.Fatal("warning must not interrupt even when a rule asks for it")
	}
}

func TestDecideRoute_CriticalInterruptsWhenAllClear(t *testing.T) {
	r := DecideRoute("critical", true, testPrefs(), utc(t),
		time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC), 0, nil)
	if !r.Interrupt || r.Reason != "interrupted" {
		t.Fatalf("critical at midday with budget free should interrupt, got %+v", r)
	}
}

func TestDecideRoute_HourlyCeilingIsEnforced(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	for spent := 0; spent < 5; spent++ {
		r := DecideRoute("critical", true, testPrefs(), utc(t), now, spent, nil)
		if !r.Interrupt {
			t.Fatalf("with %d/5 spent an interrupt should still go out", spent)
		}
	}
	r := DecideRoute("critical", true, testPrefs(), utc(t), now, 5, nil)
	if r.Interrupt {
		t.Fatal("the 6th interrupt in an hour must be refused")
	}
	if !r.QueueDigest {
		t.Fatal("a refused interrupt must still reach the digest")
	}
	if r.Reason != "hourly_cap_reached" {
		t.Fatalf("reason = %q, want hourly_cap_reached", r.Reason)
	}
}

func TestDecideRoute_QuietHoursWrapMidnight(t *testing.T) {
	p := testPrefs() // 22:00 → 07:00 local
	loc := utc(t)
	cases := []struct {
		hour    int
		quiet   bool
		comment string
	}{
		{23, true, "23:00 is inside a 22-07 window"},
		{3, true, "03:00 is inside a window that wraps midnight"},
		{6, true, "06:59 is still inside"},
		{7, false, "07:00 is the end of the window"},
		{12, false, "midday is outside"},
		{21, false, "21:00 is before the window opens"},
		{22, true, "22:00 is the start"},
	}
	for _, c := range cases {
		got := p.InQuietHours(loc, time.Date(2026, 3, 1, c.hour, 0, 0, 0, time.UTC))
		if got != c.quiet {
			t.Fatalf("%s: InQuietHours at %02d:00 = %v, want %v",
				c.comment, c.hour, got, c.quiet)
		}
	}
}

func TestDecideRoute_CriticalDuringQuietHoursIsDeferred(t *testing.T) {
	r := DecideRoute("critical", true, testPrefs(), utc(t),
		time.Date(2026, 3, 1, 3, 0, 0, 0, time.UTC), 0, nil)
	if r.Interrupt {
		t.Fatal("a 3am page is exactly what the budget exists to prevent")
	}
	if r.Reason != "quiet_hours" {
		t.Fatalf("reason = %q, want quiet_hours", r.Reason)
	}
}

func TestDecideRoute_ShipmentCooldownCollapsesRuleBursts(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	fiveMinAgo := now.Add(-5 * time.Minute)
	r := DecideRoute("critical", true, testPrefs(), utc(t), now, 0, &fiveMinAgo)
	if r.Interrupt {
		t.Fatal("five rules firing on one shipment within the cooldown must yield one interrupt")
	}
	if r.Reason != "shipment_cooldown" {
		t.Fatalf("reason = %q, want shipment_cooldown", r.Reason)
	}
	// Past the cooldown it may interrupt again.
	sixHoursAgo := now.Add(-361 * time.Minute)
	r2 := DecideRoute("critical", true, testPrefs(), utc(t), now, 0, &sixHoursAgo)
	if !r2.Interrupt {
		t.Fatal("past the cooldown a shipment may interrupt again")
	}
}

func TestDecideRoute_ZeroCapMutesEverything(t *testing.T) {
	p := testPrefs()
	p.InterruptCap = 0
	r := DecideRoute("critical", true, p, utc(t),
		time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC), 0, nil)
	if r.Interrupt {
		t.Fatal("a cap of zero means digest-only delivery")
	}
}

func TestStaleThresholdIsModeRelative(t *testing.T) {
	// Air (72h norm) → 24h floor. Ocean (504h norm) → 84h.
	if got := StaleThreshold(72); got != 24 {
		t.Fatalf("air stale threshold = %v, want 24", got)
	}
	if got := StaleThreshold(504); got != 84 {
		t.Fatalf("ocean stale threshold = %v, want 84", got)
	}
	// An absolute threshold would call 48h of ocean silence an exception; this
	// must not.
	if 48 >= StaleThreshold(504) {
		t.Fatal("48h of ocean silence must be below the threshold")
	}
}