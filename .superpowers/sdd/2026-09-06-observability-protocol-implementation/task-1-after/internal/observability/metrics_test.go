package observability

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestMetricsUsesInjectedRegistryAndBoundedLabels(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)
	m.ObserveConnection("server", "agent", "success", "")
	m.ObserveStage("server", "websocket_upgrade", "success", "", 25*time.Millisecond)
	m.ObserveHeartbeat("agent", "pong", 10*time.Millisecond)
	m.ObserveBytes("server", "inbound", "tcp", 42)
	m.ObserveStream("tcp", "accepted", "")
	m.ObserveProbe("tcp", "success", "", 5*time.Millisecond)
	m.SetReady("server", true)

	if got := testutil.ToFloat64(m.connectionsTotal.WithLabelValues("server", "agent", "success", "")); got != 1 {
		t.Fatalf("connections_total = %v, want 1", got)
	}

	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("collect metrics: %v", err)
	}
	var exposition strings.Builder
	for _, family := range families {
		fmt.Fprintln(&exposition, family.GetName())
		for _, metric := range family.GetMetric() {
			for _, label := range metric.GetLabel() {
				fmt.Fprintf(&exposition, "%s=%s ", label.GetName(), label.GetValue())
			}
		}
	}
	text := exposition.String()
	for _, forbidden := range []string{"token_id", "connection_id", "stream_id", "target", "secret", "token"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("metric exposition contains forbidden label/token %q:\n%s", forbidden, text)
		}
	}
	if !strings.Contains(text, "tunnelmesh_connections_total") ||
		!strings.Contains(text, "tunnelmesh_connection_stage_duration_seconds") ||
		!strings.Contains(text, "tunnelmesh_ready") {
		t.Fatalf("metric exposition is missing expected metric families:\n%s", text)
	}
}

func TestMetricsRegistryIsolation(t *testing.T) {
	first := prometheus.NewRegistry()
	second := prometheus.NewRegistry()
	m1 := NewMetrics(first)
	_ = NewMetrics(second)
	m1.ObserveBytes("server", "outbound", "http", 7)

	if err := testutil.GatherAndCompare(second, strings.NewReader("")); err != nil {
		t.Fatalf("second registry unexpectedly contains first registry metrics: %v", err)
	}
}

func TestMetricsObserveDurationsAndActiveState(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)
	m.ObserveConnection("client", "client", "started", "")
	m.ObserveConnection("client", "client", "closed", "")
	m.ObserveStage("client", "auth", "failure", "authentication", 100*time.Millisecond)
	m.ObserveStream("tcp", "accepted", "")
	m.ObserveStream("tcp", "reset", "reset")

	if got := testutil.ToFloat64(m.connectionsActive.WithLabelValues("client", "client")); got != 0 {
		t.Fatalf("connections_active = %v, want 0", got)
	}
	if got := testutil.ToFloat64(m.streamsActive.WithLabelValues("tcp")); got != 0 {
		t.Fatalf("streams_active = %v, want 0", got)
	}
	if got := testutil.ToFloat64(m.connectionsTotal.WithLabelValues("client", "client", "started", "")); got != 1 {
		t.Fatalf("started connection count = %v, want 1", got)
	}
	if got := testutil.ToFloat64(m.streamsTotal.WithLabelValues("tcp", "reset", "reset")); got != 1 {
		t.Fatalf("reset stream count = %v, want 1", got)
	}
}
