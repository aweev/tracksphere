// Package intel implements P3 lane intelligence: transit statistics learned
// from a tenant's own delivered shipments, lane-aware ETA estimation with
// confidence, anomaly detection (slower than lane p90) and the weekly digest
// payload. Pure functions stay testable; SQL lives in small helpers.
package intel

import (
	"sort"
	"time"
)

// LaneKey identifies a trade lane.
type LaneKey struct {
	Origin      string
	Destination string
	Carrier     string
	Mode        string
}

// LaneStats are learned transit durations (days) for one lane.
type LaneStats struct {
	Samples int
	P50Days float64
	P90Days float64
}

// quantile returns the q-quantile of sorted days (linear interpolation).
func quantile(sorted []float64, q float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if len(sorted) == 1 {
		return sorted[0]
	}
	pos := q * float64(len(sorted)-1)
	lo, frac := int(pos), pos-float64(int(pos))
	if lo+1 >= len(sorted) {
		return sorted[len(sorted)-1]
	}
	return sorted[lo] + frac*(sorted[lo+1]-sorted[lo])
}

// Summarize builds stats from delivered transit durations (days).
func Summarize(days []float64) LaneStats {
	cp := append([]float64(nil), days...)
	sort.Float64s(cp)
	return LaneStats{
		Samples: len(cp),
		P50Days: quantile(cp, 0.5),
		P90Days: quantile(cp, 0.9),
	}
}

// EstimateETA returns a lane-aware ETA and a 0..1 confidence. With too few
// samples it falls back to the mode baseline (same numbers the P1 heuristic
// used) at low confidence — the interface never changes, only the learning.
func EstimateETA(mode string, createdAt time.Time, lane *LaneStats) (time.Time, float64) {
	if lane != nil && lane.Samples >= 5 {
		conf := 0.6
		if lane.Samples >= 20 {
			conf = 0.85
		} else if lane.Samples >= 10 {
			conf = 0.75
		}
		return createdAt.Add(time.Duration(lane.P50Days*24) * time.Hour), conf
	}
	return createdAt.Add(baselineTransit(mode)), 0.4
}

func baselineTransit(mode string) time.Duration {
	switch mode {
	case "ocean":
		return 21 * 24 * time.Hour
	case "air":
		return 3 * 24 * time.Hour
	case "rail":
		return 10 * 24 * time.Hour
	default:
		return 5 * 24 * time.Hour
	}
}

// IsAnomaly reports whether an in-flight shipment is unusually slow for its
// lane: elapsed transit beyond max(p90 * 1.25, p90 + 1 day). Needs ≥5 samples.
func IsAnomaly(elapsed time.Duration, lane *LaneStats) bool {
	if lane == nil || lane.Samples < 5 {
		return false
	}
	threshold := lane.P90Days * 1.25
	if lane.P90Days+1 > threshold {
		threshold = lane.P90Days + 1
	}
	return elapsed.Hours()/24 > threshold
}

// Digest is the weekly ops summary payload (endpoint + owner notification).
type Digest struct {
	WeekStart      time.Time      `json:"weekStart"`
	Delivered      int64          `json:"delivered"`
	OnTimePct      *float64       `json:"onTimePct,omitempty"`
	Exceptions     int64          `json:"exceptions"`
	OpenAlerts     int64          `json:"openAlerts"`
	TopCarriers    []CarrierLine  `json:"topCarriers"`
	StaleShipments int64          `json:"staleShipments"`
}

type CarrierLine struct {
	Carrier string `json:"carrier"`
	Total   int64  `json:"total"`
}

// DailySummary is the daily ops summary payload (email + Slack/Teams webhook).
type DailySummary struct {
	Date               time.Time  `json:"date"`
	ShipmentsCreated   int64      `json:"shipmentsCreated"`
	ShipmentsDelivered int64      `json:"shipmentsDelivered"`
	ActiveShipments    int64      `json:"activeShipments"`
	ExceptionsRaised   int64      `json:"exceptionsRaised"`
	CriticalAlertsOpen int64      `json:"criticalAlertsOpen"`
	AlertsResolved     int64      `json:"alertsResolved"`
	AvgResolutionHours *float64   `json:"avgResolutionHours,omitempty"`
	TopCarrier         string     `json:"topCarrier,omitempty"`
}
