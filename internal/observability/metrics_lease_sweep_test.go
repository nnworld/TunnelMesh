package observability

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func gatherLabels(t *testing.T, reg *prometheus.Registry, family string) []string {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, got := range families {
		if got.GetName() != family {
			continue
		}
		names := make([]string, 0, 2)
		for _, metric := range got.GetMetric() {
			for _, pair := range metric.GetLabel() {
				names = append(names, pair.GetName())
			}
			break
		}
		return names
	}
	t.Fatalf("missing metric family %s", family)
	return nil
}

// The reaper is the only thing standing between a long-lived deployment and an
// unbounded lease table, so "it ran", "it reaped nothing" and "it failed" have
// to be three distinguishable signals rather than one silent goroutine.
func TestMetricsClientConnectionLeaseSweepFamily(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics := NewMetrics(reg)

	metrics.ObserveClientConnectionLeaseSweep("ok", "")
	metrics.ObserveClientConnectionLeaseSweep("error", "timeout")

	if got := gatherLabels(t, reg, "tunnelmesh_client_connection_lease_sweep_total"); len(got) != 2 {
		t.Fatalf("labels = %v, want result and error_class", got)
	}
	// A nil receiver is a deployment without a registry, not a crash.
	var absent *Metrics
	absent.ObserveClientConnectionLeaseSweep("ok", "")
}

func TestMetricsClientConnectionLeaseSweepCounterValue(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics := NewMetrics(reg)
	metrics.ObserveClientConnectionLeaseSweep("ok", "")
	metrics.ObserveClientConnectionLeaseSweep("ok", "")
	metrics.ObserveClientConnectionLeaseSweep("error", "timeout")

	// ToFloat64 needs one series, so each label combination is read on its own:
	// a clean pass and a failed pass must not share a counter.
	if got := testutil.ToFloat64(metrics.clientConnectionLeaseSweepTotal.WithLabelValues("ok", "")); got != 2 {
		t.Fatalf("ok samples = %v, want 2", got)
	}
	if got := testutil.ToFloat64(metrics.clientConnectionLeaseSweepTotal.WithLabelValues("error", "timeout")); got != 1 {
		t.Fatalf("error samples = %v, want 1", got)
	}
}
