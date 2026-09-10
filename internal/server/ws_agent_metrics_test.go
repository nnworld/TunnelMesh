package server

import (
	"context"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/tunnelmesh/tunnelmesh/internal/observability"
)

func TestServeAgentSessionWithMetricsPublishesConnectionPoolFamilies(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics := observability.NewMetrics(registry)
	manager := NewAgentSessionManager(AgentSessionConfig{})
	registration := AgentRegistration{AgentID: "metrics-agent", NodeID: "node-1", Epoch: 1, InstanceID: "instance-1", ConnectionID: "conn-1"}
	if err := serveAgentSessionWithMetrics(context.Background(), manager, registration, NewWSFrameTransport(&metadataSessionWSConn{}), nil, nil, nil, metrics); err != nil {
		t.Fatal(err)
	}

	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	var exposition strings.Builder
	for _, family := range families {
		exposition.WriteString(family.GetName())
	}
	for _, family := range []string{
		"tunnelmesh_agent_connections",
		"tunnelmesh_agent_connection_capacity",
	} {
		if !strings.Contains(exposition.String(), family) {
			t.Fatalf("missing metric family %s in %s", family, exposition.String())
		}
	}
}
