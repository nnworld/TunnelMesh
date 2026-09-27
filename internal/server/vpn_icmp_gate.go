package server

import (
	"context"
	"fmt"
	"strings"

	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
)

// AgentCapabilityState is what the control plane knows about one agent's ability
// to serve a capability right now. It is deliberately not a boolean: "could not
// determine" is a third answer, and collapsing it into either of the other two
// would either refuse peers at random or promise something nobody checked.
type AgentCapabilityState string

const (
	// CapabilitySupported means a healthy session on this node advertised the
	// capability, so the answer is a fact about the agent that will serve it.
	CapabilitySupported AgentCapabilityState = "supported"
	// CapabilityUnsupported means the agent is reachable and did not advertise
	// the capability, or nothing anywhere holds a connection to it.
	CapabilityUnsupported AgentCapabilityState = "unsupported"
	// CapabilityUnverified means another server node holds the connection and
	// capabilities are not part of the cluster's connection leases, so this node
	// can neither confirm nor deny them.
	CapabilityUnverified AgentCapabilityState = "unverified"
)

// VPNAgentCapabilityProbe answers the capability question for one egress agent.
// It is an interface because the answer comes from live session state, which a
// service test has to be able to fix, and because the data plane will ask the
// same question before it opens a stream.
type VPNAgentCapabilityProbe interface {
	ProbeICMPEcho(ctx context.Context, agentID string) (AgentCapabilityState, error)
}

// probeAgentICMPEcho decides from live state whether an egress agent can answer
// ICMP echo.
//
// Local sessions come first because they are the only place a negotiated
// capability is recorded. Connection leases are consulted only when this node
// holds no healthy session: the registry knows which node an agent is connected
// to but not what it negotiated, so the honest answer for a remote connection is
// "could not verify" rather than "cannot".
//
// Unverified is a pass, not a refusal. A management API request in a cluster can
// land on any node, so refusing on "cannot see it" would make issuing an ICMP
// peer fail at random depending on which node answered, and a user would see an
// intermittent 409 that no amount of retrying fixes reliably. Passing and
// recording the fact in the audit trail turns that into a diagnosable state: the
// data plane still refuses per packet when the egress really cannot echo.
func probeAgentICMPEcho(ctx context.Context, sessions *AgentSessionManager, connections AgentConnectionLister, localNodeID, agentID string) (AgentCapabilityState, error) {
	connected := false
	for _, session := range sessions.List(agentID) {
		if !session.Healthy() {
			continue
		}
		connected = true
		if session.Supports(protocol.CapabilityStreamICMPEcho) {
			return CapabilitySupported, nil
		}
	}
	if connected {
		// A live session that did not advertise the capability is a fact about
		// this agent, not a gap in the cluster's visibility.
		return CapabilityUnsupported, nil
	}
	if connections == nil {
		return CapabilityUnsupported, nil
	}
	owners, err := connections.ListAgentConnections(ctx, agentID)
	if err != nil {
		return CapabilityUnsupported, fmt.Errorf("list agent connections: %w", err)
	}
	localNodeID = strings.TrimSpace(localNodeID)
	for _, owner := range owners {
		// A lease naming this node with no local session is a lease that has not
		// expired yet, not a connection, so it does not make the answer
		// "unverified".
		if owner.ServerNodeID != "" && owner.ServerNodeID != localNodeID {
			return CapabilityUnverified, nil
		}
	}
	return CapabilityUnsupported, nil
}

// clusterAgentICMPProbe binds the peer service to this API's live cluster state.
//
// The session manager and the connection lister are resolved on every call rather
// than captured at assembly time. SetVPN runs before the runtime installs the
// cluster lister, so capturing it would freeze a nil and every ICMP request would
// fail closed for the lifetime of the process.
type clusterAgentICMPProbe struct {
	api    *API
	nodeID string
}

func (p clusterAgentICMPProbe) ProbeICMPEcho(ctx context.Context, agentID string) (AgentCapabilityState, error) {
	if p.api == nil {
		return CapabilityUnsupported, nil
	}
	return probeAgentICMPEcho(ctx, p.api.agentSessions, p.api.clusterConnections, p.nodeID, agentID)
}

// vpnAgentCapabilityProbe is the probe the configuration-driven assembly
// installs. It is a method so the gate reads the same API fields every other
// cluster-aware handler reads.
func (a *API) vpnAgentCapabilityProbe(nodeID string) VPNAgentCapabilityProbe {
	return clusterAgentICMPProbe{api: a, nodeID: strings.TrimSpace(nodeID)}
}
