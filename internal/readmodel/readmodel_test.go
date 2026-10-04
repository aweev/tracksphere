package readmodel

import "testing"

func TestScore_QuietShipmentIsClear(t *testing.T) {
	score, tier := Score(Input{Status: "in_transit"})
	if tier != "clear" || score != 0 {
		t.Fatalf("quiet shipment scored %d/%s; want 0/clear", score, tier)
	}
}

func TestScore_TerminalStatesAreNeverAtRisk(t *testing.T) {
	for _, st := range []string{"delivered", "cancelled"} {
		score, tier := Score(Input{
			Status: st, OpenAlerts: 5, CriticalAlerts: 2,
			StaleHours: 500, DwellHours: 900, ExpectedDwellHours: 504,
			ValueAtRisk: 99999,
		})
		if score != 0 || tier != "clear" {
			t.Fatalf("%s scored %d/%s; a closed shipment is never at risk", st, score, tier)
		}
	}
}

func TestScore_DwellOverrunSaturates(t *testing.T) {
	// 2x the expected transit should reach the dwell ceiling but nothing more.
	score, _ := Score(Input{Status: "in_transit", DwellHours: 1008, ExpectedDwellHours: 504})
	if score > wDwellMax {
		t.Fatalf("dwell term contributed %d, above its %v ceiling", score, wDwellMax)
	}
	if float64(score) < wDwellMax*0.9 {
		t.Fatalf("2x overrun scored %d; dwell should be near its ceiling", score)
	}
	// A 100x overrun must not score higher than a 2x one.
	extreme, _ := Score(Input{Status: "in_transit", DwellHours: 50400, ExpectedDwellHours: 504})
	if extreme > score {
		t.Fatalf("unbounded dwell scored higher (%d vs %d); terms must saturate", extreme, score)
	}
}

func TestScore_StalenessIsRelativeToLaneNorm(t *testing.T) {
	// An air shipment (72h norm → 24h threshold) silent for 96h is an emergency.
	air, airTier := Score(Input{Status: "in_transit", StaleHours: 96, ExpectedDwellHours: 72})
	// The same 96h of silence on an ocean shipment (504h norm → 84h threshold)
	// is unremarkable, because nothing should be scanning it right now.
	ocean, oceanTier := Score(Input{Status: "in_transit", StaleHours: 96, ExpectedDwellHours: 504})
	if air <= ocean {
		t.Fatalf("stale air (%d) must outrank equally-stale ocean (%d)", air, ocean)
	}
	if airTier == "clear" {
		t.Fatalf("96h of silence on an air shipment scored %d/%s", air, airTier)
	}
	if oceanTier != "clear" {
		t.Fatalf("96h of silence on an ocean shipment scored %d/%s; that is normal transit", ocean, oceanTier)
	}
}

func TestScore_StalenessComposesWithAlerts(t *testing.T) {
	// The sweep raises a staleness alert AND the score rises. The two channels
	// must compose rather than duplicate: the alert drives the exception queue,
	// the score drives list ordering, and together they must rank higher than
	// either alone.
	silenceOnly, _ := Score(Input{
		Status: "in_transit", StaleHours: 72, ExpectedDwellHours: 72,
	})
	both, tier := Score(Input{
		Status: "in_transit", StaleHours: 72,
		ExpectedDwellHours: 72, InfoOrWarnAlerts: 1,
	})
	if both <= silenceOnly {
		t.Fatalf("silence+alert (%d) must outrank silence alone (%d)", both, silenceOnly)
	}
	if tier == "clear" {
		t.Fatalf("72h silence on air plus a warning alert scored %d/%s", both, tier)
	}
}

func TestScore_CriticalAlertsDominate(t *testing.T) {
	one, _ := Score(Input{Status: "in_transit", CriticalAlerts: 1})
	two, _ := Score(Input{Status: "in_transit", CriticalAlerts: 2})
	many, _ := Score(Input{Status: "in_transit", CriticalAlerts: 8})
	if !(one > 0 && two > one) {
		t.Fatalf("critical alerts should escalate: 1=%d 2=%d", one, two)
	}
	if many > 100 {
		t.Fatalf("score %d exceeds the 0-100 contract", many)
	}
	if many > int(2*wCriticalAlert+4*wAlert) {
		t.Fatalf("alert terms are not capped: %d", many)
	}
}

func TestScore_CustomerNotifiedReducesUrgency(t *testing.T) {
	without, _ := Score(Input{Status: "in_transit", CriticalAlerts: 1, StaleHours: 48})
	with, _ := Score(Input{Status: "in_transit", CriticalAlerts: 1, StaleHours: 48, CustomerNotified: true})
	if with >= without {
		t.Fatalf("telling the customer should lower urgency: %d with vs %d without", with, without)
	}
	if with < 0 {
		t.Fatalf("score %d must never go negative", with)
	}
}

func TestScore_Bounds(t *testing.T) {
	score, _ := Score(Input{
		Status: "in_transit", DwellHours: 1e6, ExpectedDwellHours: 1,
		ETASlipHours: 1e6, StaleHours: 1e6, OpenAlerts: 999,
		CriticalAlerts: 999, ValueAtRisk: 1e12,
	})
	if score < 0 || score > 100 {
		t.Fatalf("score %d outside the 0-100 contract", score)
	}
}

func TestTierFor(t *testing.T) {
	cases := map[int]string{0: "clear", 14: "clear", 15: "watch", 39: "watch",
		40: "at_risk", 69: "at_risk", 70: "critical", 100: "critical"}
	for score, want := range cases {
		if got := TierFor(score); got != want {
			t.Fatalf("TierFor(%d) = %s, want %s", score, got, want)
		}
	}
}

func TestExpectedDwellNorms(t *testing.T) {
	if h, ok := expectedDwellHours("ocean"); !ok || h != 504 {
		t.Fatalf("ocean norm = %v/%v, want 504/true", h, ok)
	}
	if h, ok := expectedDwellHours("unknown-mode"); !ok || h != 120 {
		t.Fatalf("unknown mode should fall back to road, got %v/%v", h, ok)
	}
}