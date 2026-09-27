package server

import (
	"context"
	"errors"
	"testing"

	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
	"github.com/tunnelmesh/tunnelmesh/internal/registry"
)

func registerProbeSession(t *testing.T, manager *AgentSessionManager, connectionID string, capabilities ...string) *AgentSession {
	t.Helper()
	session, err := manager.Register(context.Background(), AgentRegistration{
		AgentID: "agent-probe", NodeID: "node-a", ConnectionID: connectionID,
		ConnectionEpoch: 1, Epoch: 1, Capabilities: capabilities,
	}, newFakeTransport())
	if err != nil {
		t.Fatalf("register %s: %v", connectionID, err)
	}
	return session
}

// The gate answers from local session state first and from cluster leases only
// when this node holds no healthy session. A registry that always fails is passed
// on purpose wherever the session answer must be final: consulting it would turn
// a definitive answer into an error.
func TestProbeAgentICMPEchoDecidesFromSessionsThenLeases(t *testing.T) {
	unavailable := &fixedConnectionRegistry{err: errors.New("registry unavailable")}

	t.Run("a healthy local session that negotiated the capability", func(t *testing.T) {
		manager := NewAgentSessionManager(AgentSessionConfig{})
		registerProbeSession(t, manager, "conn-echo",
			protocol.CapabilityStreamOpenResult, protocol.CapabilityStreamICMPEcho)
		state, err := probeAgentICMPEcho(context.Background(), manager, unavailable, "node-local", "agent-probe")
		if err != nil {
			t.Fatalf("probe error = %v, want the session answer without touching the registry", err)
		}
		if state != CapabilitySupported {
			t.Fatalf("state = %q, want supported", state)
		}
	})

	t.Run("a healthy local session without the capability", func(t *testing.T) {
		manager := NewAgentSessionManager(AgentSessionConfig{})
		registerProbeSession(t, manager, "conn-plain", protocol.CapabilityStreamOpenResult)
		state, err := probeAgentICMPEcho(context.Background(), manager, unavailable, "node-local", "agent-probe")
		if err != nil {
			t.Fatalf("probe error = %v, want the session answer without touching the registry", err)
		}
		if state != CapabilityUnsupported {
			t.Fatalf("state = %q, want unsupported", state)
		}
	})

	t.Run("a closed local session defers to the cluster", func(t *testing.T) {
		manager := NewAgentSessionManager(AgentSessionConfig{})
		session := registerProbeSession(t, manager, "conn-closed", protocol.CapabilityStreamICMPEcho)
		if err := session.Close(); err != nil {
			t.Fatalf("close the session: %v", err)
		}
		leases := &fixedConnectionRegistry{connections: []registry.NodeOwner{{
			AgentID: "agent-probe", ConnectionID: "conn-remote", ServerNodeID: "node-remote", HealthScore: 100,
		}}}
		state, err := probeAgentICMPEcho(context.Background(), manager, leases, "node-local", "agent-probe")
		if err != nil {
			t.Fatalf("probe error = %v, want nil", err)
		}
		if state != CapabilityUnverified {
			t.Fatalf("state = %q, want unverified because another node holds the connection", state)
		}
	})

	t.Run("no connection anywhere", func(t *testing.T) {
		manager := NewAgentSessionManager(AgentSessionConfig{})
		state, err := probeAgentICMPEcho(context.Background(), manager, &fixedConnectionRegistry{}, "node-local", "agent-probe")
		if err != nil {
			t.Fatalf("probe error = %v, want nil", err)
		}
		if state != CapabilityUnsupported {
			t.Fatalf("state = %q, want unsupported for an agent that is not connected", state)
		}
	})

	t.Run("only a stale lease on this node", func(t *testing.T) {
		manager := NewAgentSessionManager(AgentSessionConfig{})
		leases := &fixedConnectionRegistry{connections: []registry.NodeOwner{{
			AgentID: "agent-probe", ConnectionID: "conn-gone", ServerNodeID: "node-local", HealthScore: 100,
		}}}
		state, err := probeAgentICMPEcho(context.Background(), manager, leases, "node-local", "agent-probe")
		if err != nil {
			t.Fatalf("probe error = %v, want nil", err)
		}
		if state != CapabilityUnsupported {
			t.Fatalf("state = %q, want unsupported: a lease for this node with no session is a stale lease", state)
		}
	})

	t.Run("a registry failure is reported rather than guessed away", func(t *testing.T) {
		manager := NewAgentSessionManager(AgentSessionConfig{})
		if _, err := probeAgentICMPEcho(context.Background(), manager, unavailable, "node-local", "agent-probe"); err == nil {
			t.Fatal("probe error = nil, want the registry failure surfaced")
		}
	})

	t.Run("nothing wired fails closed", func(t *testing.T) {
		state, err := probeAgentICMPEcho(context.Background(), nil, nil, "node-local", "agent-probe")
		if err != nil {
			t.Fatalf("probe error = %v, want nil", err)
		}
		if state != CapabilityUnsupported {
			t.Fatalf("state = %q, want unsupported when no session state is available", state)
		}
	})
}

// The API-level adapter resolves the session manager and the cluster lister on
// every call, because SetVPN runs before the runtime installs the lister.
func TestAPIAgentICMPProbeResolvesClusterStateLate(t *testing.T) {
	api := &API{localNodeID: "node-late"}
	probe := api.vpnAgentCapabilityProbe("node-late")

	state, err := probe.ProbeICMPEcho(context.Background(), "agent-probe")
	if err != nil {
		t.Fatalf("probe error = %v, want nil", err)
	}
	if state != CapabilityUnsupported {
		t.Fatalf("state = %q, want unsupported before anything is wired", state)
	}

	api.SetAgentConnections(NewAgentSessionManager(AgentSessionConfig{}), nil)
	manager := api.agentSessions
	registerProbeSession(t, manager, "conn-late", protocol.CapabilityStreamICMPEcho)
	api.SetClusterAgentConnections(&fixedConnectionRegistry{}, nil, "node-late")

	state, err = probe.ProbeICMPEcho(context.Background(), "agent-probe")
	if err != nil {
		t.Fatalf("probe error = %v, want nil", err)
	}
	if state != CapabilitySupported {
		t.Fatalf("state = %q, want supported once the runtime has wired the session manager", state)
	}
}
