package shipments

import (
	"time"

	"github.com/tracksphere/tracksphere/internal/model"
)

// Status lifecycle ordering.
//
// Carrier webhooks are NOT ordered. Carriers retry, deliver out of band, and
// retrospectively correct themselves. A late IN_TRANSIT scan for a container
// that already scanned DELIVERED must not un-deliver it: without a guard the
// shipment flips back, delivered_at stays set, and the record is permanently
// incoherent (which then also raises a false "past ETA" alert forever).
//
// Terminal states are sticky, but a *newer* correction from the carrier is
// allowed through — a mis-scanned delivery that the carrier later retracts is a
// real event, not a replay.

// StatusRank orders the happy path. Higher means further along.
// `exception` is deliberately unranked: it is orthogonal (a shipment at any
// stage can be in exception and can leave it).
func StatusRank(s string) int {
	switch s {
	case model.StatusBooked:
		return 1
	case model.StatusInTransit:
		return 2
	case model.StatusAtCustoms:
		return 3
	case model.StatusOutForDelivery:
		return 4
	case model.StatusDelivered:
		return 5
	case model.StatusCancelled:
		return 0
	default:
		return 0
	}
}

// IsTerminal reports whether a status is an absorbing state.
func IsTerminal(s string) bool {
	return s == model.StatusDelivered || s == model.StatusCancelled
}

// Transition is the verdict for an incoming carrier status.
type Transition int

const (
	// TransitionNoChange: incoming is empty/invalid or equals current.
	TransitionNoChange Transition = iota
	// TransitionForward: normal progress.
	TransitionForward
	// TransitionRegression: moving backwards along the lifecycle.
	TransitionRegression
	// TransitionTerminalExit: leaving delivered/cancelled.
	TransitionTerminalExit
	// TransitionException: entering or leaving exception.
	TransitionException
)

// DecideTransition classifies moving from `current` to `incoming`.
//
// The caller is responsible for reconciling against event timestamps: a
// TransitionRegression or TransitionTerminalExit whose event is NEWER than the
// terminal timestamp is a legitimate correction and is applied; an older one is
// a replay and is ignored.
func DecideTransition(current, incoming string) Transition {
	if incoming == "" || !model.ValidStatus(incoming) {
		return TransitionNoChange
	}
	if incoming == current {
		return TransitionNoChange
	}
	if current == model.StatusException {
		// Exception is recoverable: leaving it for any other state is normal.
		return TransitionException
	}
	if incoming == model.StatusException {
		return TransitionException
	}
	if IsTerminal(current) {
		if incoming == model.StatusCancelled && current == model.StatusCancelled {
			return TransitionNoChange
		}
		return TransitionTerminalExit
	}
	if StatusRank(incoming) > StatusRank(current) {
		return TransitionForward
	}
	return TransitionRegression
}

// IngestState is the per-shipment state the ingest transaction must respect.
type IngestState struct {
	Status string
	// DeliveredAt is set when the shipment reached DELIVERED.
	DeliveredAt *time.Time
	// LastEventAt is the newest occurred_at already recorded on the timeline.
	// A carrier event older than this must not move status or ETA backwards.
	LastEventAt *time.Time
	// ETA is the estimate currently stored on the shipment.
	ETA *time.Time
}

// ApplyStatus decides the status this event should leave behind.
//
// Returns the status to persist, whether delivered_at must be cleared, and
// whether the event is stale for state purposes (timeline row is still stored).
func ApplyStatus(st IngestState, incoming string, occurredAt time.Time) (status string, clearDelivered bool, stale bool) {
	verdict := DecideTransition(st.Status, incoming)
	if verdict == TransitionNoChange {
		return st.Status, false, false
	}

	// Terminal states need a timestamp comparison: only a NEWER carrier event
	// may retract a delivery. An older one is a replay.
	if verdict == TransitionTerminalExit {
		if st.DeliveredAt == nil || !occurredAt.After(*st.DeliveredAt) {
			return st.Status, false, true // replay: ignore
		}
		// Legitimate retraction.
		return incoming, true, false
	}

	// Backwards along the lifecycle. If the shipment already has a newer event,
	// this one is out of order and must not rewind state.
	if verdict == TransitionRegression && st.LastEventAt != nil && !occurredAt.After(*st.LastEventAt) {
		return st.Status, false, true
	}

	return incoming, false, false
}

// ShouldApplyETA reports whether an incoming carrier ETA is fresh enough to
// overwrite the stored one. A stale event must never rewind the ETA. Strictly
// newer only: an equal timestamp is not evidence of progress, so it does not
// clobber what is already stored.
func ShouldApplyETA(st IngestState, occurredAt time.Time) bool {
	if st.LastEventAt == nil {
		return true
	}
	return occurredAt.After(*st.LastEventAt)
}