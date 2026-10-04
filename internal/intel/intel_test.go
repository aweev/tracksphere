package intel

import (
	"testing"
	"time"
)

func TestSummarize(t *testing.T) {
	s := Summarize([]float64{10, 20, 30, 40, 50})
	if s.Samples != 5 {
		t.Fatalf("samples = %d", s.Samples)
	}
	if s.P50Days != 30 {
		t.Errorf("p50 = %v, want 30", s.P50Days)
	}
	if s.P90Days < 40 || s.P90Days > 50 {
		t.Errorf("p90 = %v, want in [40,50]", s.P90Days)
	}
}

func TestEstimateETAFallback(t *testing.T) {
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	got, conf := EstimateETA("ocean", created, nil)
	if !got.Equal(created.Add(21 * 24 * time.Hour)) {
		t.Errorf("fallback = %v", got)
	}
	if conf != 0.4 {
		t.Errorf("conf = %v, want 0.4", conf)
	}
}

func TestEstimateETALane(t *testing.T) {
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	lane := &LaneStats{Samples: 25, P50Days: 12, P90Days: 15}
	got, conf := EstimateETA("ocean", created, lane)
	if !got.Equal(created.Add(12 * 24 * time.Hour)) {
		t.Errorf("lane eta = %v", got)
	}
	if conf != 0.85 {
		t.Errorf("conf = %v, want 0.85", conf)
	}
	few := &LaneStats{Samples: 3, P50Days: 12, P90Days: 15}
	if _, conf := EstimateETA("ocean", created, few); conf != 0.4 {
		t.Errorf("few samples must fall back, conf = %v", conf)
	}
}

func TestIsAnomaly(t *testing.T) {
	lane := &LaneStats{Samples: 10, P50Days: 10, P90Days: 12}
	if IsAnomaly(5*24*time.Hour, lane) {
		t.Error("5d on a p90=12 lane is not anomalous")
	}
	if !IsAnomaly(20*24*time.Hour, lane) {
		t.Error("20d on a p90=12 lane must be anomalous")
	}
	if IsAnomaly(100*24*time.Hour, &LaneStats{Samples: 2, P90Days: 1}) {
		t.Error("few samples must never flag")
	}
	if IsAnomaly(time.Hour, nil) {
		t.Error("nil lane must never flag")
	}
}
