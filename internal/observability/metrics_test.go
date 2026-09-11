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

func TestMetricsActiveGaugesDoNotGoNegative(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)
	m.ObserveConnection("server", "agent", "closed", "")
	m.ObserveStream("udp", "rejected", "authorization")
	if got := testutil.ToFloat64(m.connectionsActive.WithLabelValues("server", "agent")); got != 0 {
		t.Fatalf("inactive connection gauge = %v, want 0", got)
	}
	if got := testutil.ToFloat64(m.streamsActive.WithLabelValues("udp")); got != 0 {
		t.Fatalf("inactive stream gauge = %v, want 0", got)
	}
}

func TestNormalizeProtocolBoundsPeerControlledLabels(t *testing.T) {
	for _, value := range []string{"tcp", "UDP", "http", "websocket"} {
		if got := NormalizeProtocol(value); got == "unknown" {
			t.Fatalf("NormalizeProtocol(%q) unexpectedly unknown", value)
		}
	}
	if got := NormalizeProtocol("custom-extension-" + strings.Repeat("x", 100)); got != "unknown" {
		t.Fatalf("NormalizeProtocol(custom) = %q, want unknown", got)
	}
}

func TestMetricsClusterFamilies(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)
	m.ObserveRegistryLease("renew", "success", "")
	m.ObserveRelay("failed", "timeout")
	m.ObserveStorage("insert", "failure", "busy", time.Millisecond)
	m.ObserveConfigReload("failure", "parse")
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, family := range families {
		names[family.GetName()] = true
	}
	for _, name := range []string{"tunnelmesh_registry_lease_total", "tunnelmesh_relay_total", "tunnelmesh_storage_operation_duration_seconds", "tunnelmesh_storage_errors_total", "tunnelmesh_config_reload_total"} {
		if !names[name] {
			t.Fatalf("missing metric family %s", name)
		}
	}
}

func TestMetricsAgentConnectionPoolFamilies(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics := NewMetrics(reg)
	metrics.ObserveAgentConnection("agent", "instance", "connection", false)
	metrics.ObserveAgentConnection("agent", "instance", "connection", true)
	metrics.ObserveAgentConnection("agent", "instance", "connection-2", true)
	metrics.ObserveAgentConnectionError("agent", "instance", "connection", "timeout")
	metrics.ObserveAgentActiveStreams("agent", "instance", "connection", 3)
	metrics.ObserveAgentActiveStreams("agent", "instance", "connection-2", 4)
	metrics.ObserveAgentConnectionRTT("agent", "instance", "connection", 20*time.Millisecond)
	metrics.SetAgentConnectionCapacity("agent", "instance", 8)
	metrics.ObserveAgentScaleDecision("agent", "instance", "scale-up", "high-active-streams")
	metrics.ObserveAgentSelection("agent", "connection", "server", "local")
	metrics.ObserveAgentSelection("agent", "connection-2", "server", "local")

	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, family := range families {
		names[family.GetName()] = true
	}
	for _, name := range []string{
		"tunnelmesh_agent_connections",
		"tunnelmesh_agent_connection_capacity",
		"tunnelmesh_agent_active_streams",
		"tunnelmesh_agent_connection_rtt_seconds",
		"tunnelmesh_agent_connection_errors_total",
		"tunnelmesh_agent_connection_scale_decisions_total",
		"tunnelmesh_agent_selection_total",
	} {
		if !names[name] {
			t.Fatalf("missing metric family %s", name)
		}
	}
	if got := testutil.ToFloat64(metrics.agentConnectionErrors.WithLabelValues("agent", "instance", "timeout")); got != 1 {
		t.Fatalf("connection error count = %v", got)
	}
	if got := testutil.ToFloat64(metrics.agentConnections.WithLabelValues("agent", "instance")); got != 2 {
		t.Fatalf("agent connection count = %v, want 2", got)
	}
	if got := testutil.ToFloat64(metrics.agentActiveStreams.WithLabelValues("agent", "instance")); got != 7 {
		t.Fatalf("agent active streams = %v, want 7", got)
	}
	if got := testutil.ToFloat64(metrics.agentSelection.WithLabelValues("agent", "server", "local")); got != 2 {
		t.Fatalf("agent selection count = %v, want 2", got)
	}
	for _, family := range families {
		if family.GetName() != "tunnelmesh_agent_selection_total" {
			continue
		}
		for _, metric := range family.GetMetric() {
			for _, labelPair := range metric.GetLabel() {
				if labelPair.GetName() == "connection_id" {
					t.Fatal("agent selection metric must not use connection_id label")
				}
			}
		}
	}
}

func TestMetricsStreamLatencyAndCacheFamilies(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics := NewMetrics(reg)
	metrics.ObserveStreamStage("server", "authorization", "success", "", "tcp", 15*time.Millisecond)
	metrics.ObserveStreamOpen("client", "success", "", "strict")
	metrics.ObserveStreamQueueWait("agent", "success", 2*time.Millisecond)
	metrics.ObserveStreamWindowStall("client", "timeout", 5*time.Millisecond)
	metrics.ObserveStreamBackpressure("server", "reset")
	metrics.ObserveAuthorizationCache("hit", "allow")
	metrics.SetAuthorizationRevision(42)
	metrics.ObserveAuthorizationRevisionPoll("success", "")
	metrics.ObserveRemoteValidationCache("miss", "allow")

	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, family := range families {
		names[family.GetName()] = true
	}
	for _, name := range []string{
		"tunnelmesh_stream_stage_duration_seconds",
		"tunnelmesh_stream_open_total",
		"tunnelmesh_stream_queue_wait_seconds",
		"tunnelmesh_stream_window_stall_seconds",
		"tunnelmesh_stream_backpressure_total",
		"tunnelmesh_authorization_cache_requests_total",
		"tunnelmesh_authorization_revision",
		"tunnelmesh_authorization_revision_poll_total",
		"tunnelmesh_remote_validation_cache_requests_total",
	} {
		if !names[name] {
			t.Fatalf("missing metric family %s", name)
		}
	}
	if got := testutil.ToFloat64(metrics.streamOpenTotal.WithLabelValues("client", "success", "", "strict")); got != 1 {
		t.Fatalf("strict stream open count = %v, want 1", got)
	}
	if got := testutil.ToFloat64(metrics.authorizationRevision.WithLabelValues()); got != 42 {
		t.Fatalf("authorization revision = %v, want 42", got)
	}
}
