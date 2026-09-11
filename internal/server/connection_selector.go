package server

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/observability"
	"github.com/tunnelmesh/tunnelmesh/internal/registry"
	"github.com/tunnelmesh/tunnelmesh/internal/relay"
)

var ErrNoAgentConnections = errors.New("server: no healthy agent connections")

type ConnectionSelectionPolicy uint8

const (
	SelectionLeastStreams ConnectionSelectionPolicy = iota
	SelectionLocalPreferred
)

type AgentConnectionCandidate struct {
	AgentID       string
	InstanceID    string
	ConnectionID  string
	ServerNodeID  string
	ActiveStreams int
	HealthScore   int
	RTT           time.Duration
	Local         bool
}

// SelectAgentConnection is a pure authorization-independent ordering policy.
// Local preference only affects performance; remote candidates remain a
// fallback and never bypass policy or capability checks performed by callers.
func SelectAgentConnection(policy ConnectionSelectionPolicy, currentServerNodeID string, candidates []AgentConnectionCandidate) (AgentConnectionCandidate, error) {
	healthy := make([]AgentConnectionCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.ConnectionID != "" && candidate.HealthScore > 0 {
			healthy = append(healthy, candidate)
		}
	}
	if len(healthy) == 0 {
		return AgentConnectionCandidate{}, ErrNoAgentConnections
	}
	if policy == SelectionLocalPreferred {
		local := make([]AgentConnectionCandidate, 0, len(healthy))
		for _, candidate := range healthy {
			if candidate.ServerNodeID == currentServerNodeID {
				local = append(local, candidate)
			}
		}
		if len(local) > 0 {
			healthy = local
		}
	}
	sort.SliceStable(healthy, func(i, j int) bool {
		if healthy[i].ActiveStreams != healthy[j].ActiveStreams {
			return healthy[i].ActiveStreams < healthy[j].ActiveStreams
		}
		if healthy[i].HealthScore != healthy[j].HealthScore {
			return healthy[i].HealthScore > healthy[j].HealthScore
		}
		return healthy[i].RTT < healthy[j].RTT
	})
	return healthy[0], nil
}

// AgentConnectionSelector implements locality-first, least-connections routing
// for a logical Agent. Local WebSocket sessions are preferred; remote Server
// nodes are used only when no healthy local connection remains.
type AgentConnectionSelector struct {
	manager     *AgentSessionManager
	localRelay  *AgentRelayTransport
	connections interface {
		ListAgentConnections(context.Context, string) ([]registry.NodeOwner, error)
	}
	localNodeID string
	metrics     *observability.Metrics
}

func (s *AgentConnectionSelector) SetMetrics(metrics *observability.Metrics) {
	if s == nil {
		return
	}
	s.metrics = metrics
}

func NewAgentConnectionSelector(manager *AgentSessionManager, localRelay *AgentRelayTransport, connections interface {
	ListAgentConnections(context.Context, string) ([]registry.NodeOwner, error)
}, localNodeID string) *AgentConnectionSelector {
	return &AgentConnectionSelector{manager: manager, localRelay: localRelay, connections: connections, localNodeID: localNodeID}
}

func (s *AgentConnectionSelector) Select(ctx context.Context, agentID, protocol string) (relay.AgentConnectionTarget, error) {
	if s == nil || s.manager == nil {
		return relay.AgentConnectionTarget{}, relay.ErrNodeDisconnected
	}
	var best *AgentSession
	bestActive := 0
	for _, session := range s.manager.List(agentID) {
		if !session.Healthy() || (protocol != "" && len(session.Capabilities) > 0 && !session.Supports(protocol)) {
			continue
		}
		active := 0
		if s.localRelay != nil {
			active = s.localRelay.ActiveStreams(agentID, session.ConnectionID)
		}
		if best == nil || active < bestActive ||
			(active == bestActive && session.LastHeartbeat().After(best.LastHeartbeat())) {
			best, bestActive = session, active
		}
	}
	if best != nil {
		if s.metrics != nil {
			s.metrics.ObserveAgentSelection(best.AgentID, best.ConnectionID, s.localNodeID, "local")
		}
		return relay.AgentConnectionTarget{
			AgentID: best.AgentID, ConnectionID: best.ConnectionID, ConnectionEpoch: best.ConnectionEpoch,
			Local: true, ActiveStreams: int64(bestActive), HealthScore: 100,
		}, nil
	}
	if s.connections == nil {
		return relay.AgentConnectionTarget{}, relay.ErrNodeDisconnected
	}
	owners, err := s.connections.ListAgentConnections(ctx, agentID)
	if err != nil {
		return relay.AgentConnectionTarget{}, err
	}
	var bestRemote *registry.NodeOwner
	for i := range owners {
		owner := owners[i]
		if owner.ServerNodeID == "" || owner.ServerNodeID == s.localNodeID {
			continue
		}
		if bestRemote == nil ||
			owner.ActiveStreams < bestRemote.ActiveStreams ||
			(owner.ActiveStreams == bestRemote.ActiveStreams && owner.HealthScore > bestRemote.HealthScore) {
			bestRemote = &owners[i]
		}
	}
	if bestRemote == nil {
		return relay.AgentConnectionTarget{}, relay.ErrNodeDisconnected
	}
	if s.metrics != nil {
		s.metrics.ObserveAgentSelection(bestRemote.AgentID, bestRemote.ConnectionID, bestRemote.ServerNodeID, "remote")
	}
	return relay.AgentConnectionTarget{
		AgentID: bestRemote.AgentID, ConnectionID: bestRemote.ConnectionID, ConnectionEpoch: bestRemote.ConnectionEpoch,
		ServerNodeID: bestRemote.ServerNodeID, ServerNodeEpoch: bestRemote.ServerNodeEpoch,
		ActiveStreams: bestRemote.ActiveStreams, HealthScore: bestRemote.HealthScore,
	}, nil
}
