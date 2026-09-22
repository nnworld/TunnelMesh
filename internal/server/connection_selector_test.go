package server

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/tunnelmesh/tunnelmesh/internal/observability"
	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
	"github.com/tunnelmesh/tunnelmesh/internal/registry"
	"github.com/tunnelmesh/tunnelmesh/internal/relay"
)

type fixedConnectionRegistry struct {
	connections []registry.NodeOwner
	err         error
}

func (r *fixedConnectionRegistry) Register(context.Context, registry.NodeRegistration) (registry.NodeOwner, error) {
	return registry.NodeOwner{}, errors.New("not implemented")
}
func (r *fixedConnectionRegistry) KeepAlive(context.Context, registry.NodeOwner, time.Duration) (registry.NodeOwner, error) {
	return registry.NodeOwner{}, errors.New("not implemented")
}
func (r *fixedConnectionRegistry) UpdateConnectionStats(context.Context, registry.NodeOwner) error {
	return errors.New("not implemented")
}
func (r *fixedConnectionRegistry) ResolveAgent(context.Context, string) (registry.NodeOwner, error) {
	return registry.NodeOwner{}, errors.New("not implemented")
}
func (r *fixedConnectionRegistry) ListAgentConnections(context.Context, string) ([]registry.NodeOwner, error) {
	return r.connections, r.err
}
func (r *fixedConnectionRegistry) Watch(context.Context, string) (<-chan registry.RegistryEvent, error) {
	return nil, errors.New("not implemented")
}
func (r *fixedConnectionRegistry) Revoke(context.Context, registry.NodeOwner) error {
	return errors.New("not implemented")
}
func (r *fixedConnectionRegistry) Close() error { return nil }

func TestAgentConnectionSelectorPrefersHealthyLeastLoadedLocalConnection(t *testing.T) {
	manager := NewAgentSessionManager(AgentSessionConfig{})
	var err error
	_, err = manager.Register(context.Background(), AgentRegistration{
		AgentID: "agent-select", NodeID: "node-a", ConnectionID: "conn-a", ConnectionEpoch: 1, Epoch: 1,
	}, newFakeTransport())
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.Register(context.Background(), AgentRegistration{
		AgentID: "agent-select", NodeID: "node-b", ConnectionID: "conn-b", ConnectionEpoch: 1, Epoch: 1,
	}, newFakeTransport())
	if err != nil {
		t.Fatal(err)
	}
	mux := NewAgentRelayTransport(manager)
	defer mux.Close()
	stream, err := mux.OpenStream(context.Background(), relay.StreamRequest{
		AgentID: "agent-select", TargetConnectionID: "conn-a", Protocol: "tcp", TargetHost: "10.0.0.1", TargetPort: 22,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()

	selector := NewAgentConnectionSelector(manager, mux, &fixedConnectionRegistry{}, "server-local")
	target, err := selector.Select(context.Background(), "agent-select", "tcp")
	if err != nil {
		t.Fatal(err)
	}
	if !target.Local || target.ConnectionID != "conn-b" {
		t.Fatalf("target=%+v, want healthy least-loaded local conn-b", target)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	target, err = selector.Select(context.Background(), "agent-select", "tcp")
	if err != nil {
		t.Fatal(err)
	}
	if !target.Local || target.ConnectionID != "conn-a" {
		t.Fatalf("target=%+v, want only healthy local conn-a", target)
	}
}

func TestStreamProtocolCapabilityMapsOnlyProtocolsThatNeedOne(t *testing.T) {
	cases := []struct {
		proto string
		want  string
	}{
		{proto: "tcp", want: ""},
		{proto: "udp", want: ""},
		{proto: "http", want: ""},
		{proto: "", want: ""},
		{proto: protocol.StreamProtocolICMPEcho, want: protocol.CapabilityStreamICMPEcho},
		{proto: "ICMP-Echo", want: protocol.CapabilityStreamICMPEcho},
		{proto: "unknown-protocol", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.proto, func(t *testing.T) {
			if got := streamProtocolCapability(tc.proto); got != tc.want {
				t.Fatalf("streamProtocolCapability(%q) = %q, want %q", tc.proto, got, tc.want)
			}
		})
	}
}

// Select takes a stream protocol and a session advertises capabilities. Matching
// one against the other rejected every session that advertised anything, which
// is every session a real agent registers, and sent the selector down the remote
// path where it reported a connected agent as disconnected.
func TestAgentConnectionSelectorMatchesCapabilitiesNotProtocolNames(t *testing.T) {
	manager := NewAgentSessionManager(AgentSessionConfig{})
	if _, err := manager.Register(context.Background(), AgentRegistration{
		AgentID: "agent-capability", NodeID: "node-a", ConnectionID: "conn-plain",
		ConnectionEpoch: 1, Epoch: 1,
		Capabilities: []string{protocol.CapabilityStreamOpenResult},
	}, newFakeTransport()); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Register(context.Background(), AgentRegistration{
		AgentID: "agent-capability", NodeID: "node-a", ConnectionID: "conn-echo",
		ConnectionEpoch: 1, Epoch: 1,
		Capabilities: []string{protocol.CapabilityStreamOpenResult, protocol.CapabilityStreamICMPEcho},
	}, newFakeTransport()); err != nil {
		t.Fatal(err)
	}

	selector := NewAgentConnectionSelector(manager, nil, &fixedConnectionRegistry{}, "server-local")

	// tcp, udp and http are baseline: a session that advertises a capability is
	// still expected to serve them.
	target, err := selector.Select(context.Background(), "agent-capability", "tcp")
	if err != nil {
		t.Fatalf(`Select(agent, "tcp") error = %v, want a local connection`, err)
	}
	if !target.Local {
		t.Fatalf(`Select(agent, "tcp") = %+v, want a local target`, target)
	}

	// icmp-echo does need the negotiated capability, so only conn-echo qualifies
	// and the answer is deterministic even though both sessions are equally
	// loaded.
	target, err = selector.Select(context.Background(), "agent-capability", protocol.StreamProtocolICMPEcho)
	if err != nil {
		t.Fatalf(`Select(agent, "icmp-echo") error = %v, want the session that negotiated it`, err)
	}
	if target.ConnectionID != "conn-echo" {
		t.Fatalf(`Select(agent, "icmp-echo") = %+v, want conn-echo`, target)
	}
}

func TestSelectAgentConnectionLocalPreferred(t *testing.T) {
	got, err := SelectAgentConnection(SelectionLocalPreferred, "server-local", []AgentConnectionCandidate{
		{AgentID: "agent", InstanceID: "instance", ConnectionID: "remote", ServerNodeID: "server-remote", ActiveStreams: 1, HealthScore: 100, RTT: time.Millisecond},
		{AgentID: "agent", InstanceID: "instance", ConnectionID: "local", ServerNodeID: "server-local", ActiveStreams: 9, HealthScore: 90, RTT: time.Second},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.ConnectionID != "local" {
		t.Fatalf("selected=%+v, want healthy local connection", got)
	}
}

func TestSelectAgentConnectionFallsBackToHealthyRemote(t *testing.T) {
	got, err := SelectAgentConnection(SelectionLocalPreferred, "server-local", []AgentConnectionCandidate{
		{AgentID: "agent", ConnectionID: "unhealthy-local", ServerNodeID: "server-local", ActiveStreams: 1, HealthScore: 0},
		{AgentID: "agent", ConnectionID: "remote", ServerNodeID: "server-remote", ActiveStreams: 2, HealthScore: 80, RTT: 10 * time.Millisecond},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.ConnectionID != "remote" {
		t.Fatalf("selected=%+v, want healthy remote fallback", got)
	}
}

func TestSelectAgentConnectionUsesLeastStreamsAndTieBreaks(t *testing.T) {
	got, err := SelectAgentConnection(SelectionLeastStreams, "server-local", []AgentConnectionCandidate{
		{AgentID: "agent", ConnectionID: "more-streams", ServerNodeID: "server-local", ActiveStreams: 3, HealthScore: 100},
		{AgentID: "agent", ConnectionID: "least-streams", ServerNodeID: "server-remote", ActiveStreams: 1, HealthScore: 90, RTT: time.Millisecond},
		{AgentID: "agent", ConnectionID: "same-streams", ServerNodeID: "server-remote", ActiveStreams: 1, HealthScore: 80, RTT: time.Millisecond},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.ConnectionID != "least-streams" {
		t.Fatalf("selected=%+v, want least streams then higher health", got)
	}
}

func TestSelectAgentConnectionRejectsUnhealthyAndEmptyCandidates(t *testing.T) {
	if _, err := SelectAgentConnection(SelectionLocalPreferred, "server-local", nil); !errors.Is(err, ErrNoAgentConnections) {
		t.Fatalf("empty error=%v, want ErrNoAgentConnections", err)
	}
	if _, err := SelectAgentConnection(SelectionLocalPreferred, "server-local", []AgentConnectionCandidate{
		{AgentID: "agent", ConnectionID: "local", ServerNodeID: "server-local", HealthScore: 0},
	}); !errors.Is(err, ErrNoAgentConnections) {
		t.Fatalf("unhealthy error=%v, want ErrNoAgentConnections", err)
	}
}

func TestSelectAgentConnectionEqualCandidatesPreservesInputOrder(t *testing.T) {
	got, err := SelectAgentConnection(SelectionLocalPreferred, "server-local", []AgentConnectionCandidate{
		{AgentID: "agent", ConnectionID: "first", ServerNodeID: "server-local", ActiveStreams: 2, HealthScore: 90, RTT: time.Millisecond},
		{AgentID: "agent", ConnectionID: "second", ServerNodeID: "server-local", ActiveStreams: 2, HealthScore: 90, RTT: time.Millisecond},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.ConnectionID != "first" {
		t.Fatalf("selected=%+v, want stable first candidate", got)
	}
}

func TestAgentConnectionSelectorFallsBackToRemote(t *testing.T) {
	manager := NewAgentSessionManager(AgentSessionConfig{})
	closed, err := manager.Register(context.Background(), AgentRegistration{
		AgentID: "agent-remote", NodeID: "node-a", ConnectionID: "conn-a", ConnectionEpoch: 1, Epoch: 1,
	}, newFakeTransport())
	if err != nil {
		t.Fatal(err)
	}
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	mux := NewAgentRelayTransport(manager)
	defer mux.Close()
	remote := &fixedConnectionRegistry{connections: []registry.NodeOwner{{
		AgentID: "agent-remote", ConnectionID: "conn-remote", ServerNodeID: "server-remote",
		ServerNodeEpoch: 8, ConnectionEpoch: 2, ActiveStreams: 1, HealthScore: 90,
	}}}
	selector := NewAgentConnectionSelector(manager, mux, remote, "server-local")
	target, err := selector.Select(context.Background(), "agent-remote", "tcp")
	if err != nil {
		t.Fatal(err)
	}
	if target.Local || target.ConnectionID != "conn-remote" || target.ServerNodeID != "server-remote" || target.ServerNodeEpoch != 8 {
		t.Fatalf("target=%+v, want remote connection", target)
	}
}

func TestAgentConnectionSelectorObservesSelection(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics := observability.NewMetrics(registry)
	manager := NewAgentSessionManager(AgentSessionConfig{})
	if _, err := manager.Register(context.Background(), AgentRegistration{AgentID: "agent-selection", NodeID: "node-a", ConnectionID: "conn-a", ConnectionEpoch: 1, Epoch: 1}, newFakeTransport()); err != nil {
		t.Fatal(err)
	}
	selector := NewAgentConnectionSelector(manager, NewAgentRelayTransport(manager), &fixedConnectionRegistry{}, "server-local")
	selector.SetMetrics(metrics)
	if _, err := selector.Select(context.Background(), "agent-selection", "tcp"); err != nil {
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
	if !strings.Contains(exposition.String(), "tunnelmesh_agent_selection_total") {
		t.Fatalf("selection metric not published: %s", exposition.String())
	}
}
