package shipments

import (
	"testing"
	"time"

	"github.com/tracksphere/tracksphere/internal/model"
)

func ts(sec int) time.Time {
	return time.Date(2026, 3, 1, 12, 0, sec, 0, time.UTC)
}

func TestDecideTransition(t *testing.T) {
	cases := []struct {
		name              string
		current, incoming string
		want              Transition
	}{
		{"empty incoming is a no-op", model.StatusInTransit, "", TransitionNoChange},
		{"invalid incoming is a no-op", model.StatusInTransit, "teleported", TransitionNoChange},
		{"same status is a no-op", model.StatusInTransit, model.StatusInTransit, TransitionNoChange},
		{"booked to in_transit", model.StatusBooked, model.StatusInTransit, TransitionForward},
		{"in_transit to at_customs", model.StatusInTransit, model.StatusAtCustoms, TransitionForward},
		{"at_customs to out_for_delivery", model.StatusAtCustoms, model.StatusOutForDelivery, TransitionForward},
		{"out_for_delivery to delivered", model.StatusOutForDelivery, model.StatusDelivered, TransitionForward},
		{"in_transit backwards to booked", model.StatusInTransit, model.StatusBooked, TransitionRegression},
		{"at_customs backwards to in_transit", model.StatusAtCustoms, model.StatusInTransit, TransitionRegression},
		{"entering exception", model.StatusInTransit, model.StatusException, TransitionException},
		{"leaving exception", model.StatusException, model.StatusInTransit, TransitionException},
		{"exception is recoverable to delivered", model.StatusException, model.StatusDelivered, TransitionException},
		{"leaving delivered", model.StatusDelivered, model.StatusInTransit, TransitionTerminalExit},
		{"leaving cancelled", model.StatusCancelled, model.StatusInTransit, TransitionTerminalExit},
		{"cancelled stays cancelled", model.StatusCancelled, model.StatusCancelled, TransitionNoChange},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DecideTransition(tc.current, tc.incoming); got != tc.want {
				t.Fatalf("DecideTransition(%q,%q) = %v, want %v", tc.current, tc.incoming, got, tc.want)
			}
		})
	}
}

// The regression this module exists to prevent: a late carrier scan must never
// un-deliver a shipment.
func TestApplyStatus_LateScanDoesNotUndeliver(t *testing.T) {
	deliveredAt := ts(30)
	st := IngestState{Status: model.StatusDelivered, DeliveredAt: &deliveredAt, LastEventAt: &deliveredAt}

	status, clearDelivered, stale := ApplyStatus(st, model.StatusInTransit, ts(10))
	if status != model.StatusDelivered {
		t.Fatalf("status regressed to %q; a late IN_TRANSIT scan must not un-deliver", status)
	}
	if clearDelivered {
		t.Fatal("delivered_at must not be cleared by a stale replay")
	}
	if !stale {
		t.Fatal("an older event than the delivery timestamp must be flagged stale")
	}
}

func TestApplyStatus_NewerCarrierRetractionIsHonoured(t *testing.T) {
	deliveredAt := ts(10)
	st := IngestState{Status: model.StatusDelivered, DeliveredAt: &deliveredAt, LastEventAt: &deliveredAt}

	// The carrier itself retracts the delivery 20s later — a real event.
	status, clearDelivered, stale := ApplyStatus(st, model.StatusInTransit, ts(30))
	if status != model.StatusInTransit {
		t.Fatalf("status = %q, want the carrier's newer correction to apply", status)
	}
	if !clearDelivered {
		t.Fatal("a legitimate retraction must clear delivered_at")
	}
	if stale {
		t.Fatal("a newer event must not be flagged stale")
	}
}

func TestApplyStatus_BackwardsOlderThanTimelineIsStale(t *testing.T) {
	last := ts(30)
	st := IngestState{Status: model.StatusAtCustoms, LastEventAt: &last}

	status, _, stale := ApplyStatus(st, model.StatusInTransit, ts(10))
	if status != model.StatusAtCustoms || !stale {
		t.Fatalf("status=%q stale=%v; an out-of-order backwards move must be ignored", status, stale)
	}
}

func TestApplyStatus_ForwardAlwaysApplies(t *testing.T) {
	last := ts(30)
	st := IngestState{Status: model.StatusInTransit, LastEventAt: &last}

	status, clearDelivered, stale := ApplyStatus(st, model.StatusDelivered, ts(40))
	if status != model.StatusDelivered || clearDelivered || stale {
		t.Fatalf("forward move broken: status=%q clear=%v stale=%v", status, clearDelivered, stale)
	}
}

func TestShouldApplyETA(t *testing.T) {
	last := ts(30)
	withHistory := IngestState{LastEventAt: &last}
	if !ShouldApplyETA(withHistory, ts(40)) {
		t.Fatal("a newer event's ETA must apply")
	}
	if ShouldApplyETA(withHistory, ts(10)) {
		t.Fatal("an older event must not rewind the ETA")
	}
	if ShouldApplyETA(withHistory, ts(30)) {
		t.Fatal("an equal-timestamp event must not clobber the ETA")
	}
	if !ShouldApplyETA(IngestState{}, ts(10)) {
		t.Fatal("with no timeline history every ETA is fresh")
	}
}

func TestStatusRankOrdering(t *testing.T) {
	ordered := []string{
		model.StatusBooked, model.StatusInTransit,
		model.StatusAtCustoms, model.StatusOutForDelivery, model.StatusDelivered,
	}
	for i := 1; i < len(ordered); i++ {
		if StatusRank(ordered[i]) <= StatusRank(ordered[i-1]) {
			t.Fatalf("rank not increasing at %s -> %s", ordered[i-1], ordered[i])
		}
	}
	if !IsTerminal(model.StatusDelivered) || !IsTerminal(model.StatusCancelled) {
		t.Fatal("delivered and cancelled must be terminal")
	}
	if IsTerminal(model.StatusInTransit) {
		t.Fatal("in_transit must not be terminal")
	}
}