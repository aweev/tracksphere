package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestUpdateDBPoolMetrics(t *testing.T) {
	UpdateDBPoolMetrics(5, 3, 8, 16)

	if got := testutil.ToFloat64(DBPoolConnsActive); got != 5 {
		t.Fatalf("DBPoolConnsActive = %v, want 5", got)
	}
	if got := testutil.ToFloat64(DBPoolConnsIdle); got != 3 {
		t.Fatalf("DBPoolConnsIdle = %v, want 3", got)
	}
	if got := testutil.ToFloat64(DBPoolConnsMax); got != 16 {
		t.Fatalf("DBPoolConnsMax = %v, want 16", got)
	}
}
