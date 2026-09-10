package server

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/registry"
)

const (
	defaultAgentConnectionLeaseTTL = 90 * time.Second
	agentConnectionLeaseTimeout    = time.Second
)

// AgentConnectionLeaseController owns the durable lifecycle for one Server
// node's process-local Agent connections. The registry remains the authority
// for cross-node ownership while the session manager remains the execution
// authority for the WebSocket itself.
type AgentConnectionLeaseController struct {
	reg               registry.NodeRegistry
	manager           *AgentSessionManager
	localRelay        *AgentRelayTransport
	serverNodeID      string
	serverNodeAddress string
	ttl               time.Duration

	mu     sync.Mutex
	owners map[*AgentSession]registry.NodeOwner
}

func NewAgentConnectionLeaseController(reg registry.NodeRegistry, manager *AgentSessionManager, localRelay *AgentRelayTransport, serverNodeID, serverNodeAddress string, ttl time.Duration) *AgentConnectionLeaseController {
	if ttl <= 0 {
		ttl = defaultAgentConnectionLeaseTTL
	}
	return &AgentConnectionLeaseController{
		reg: reg, manager: manager, localRelay: localRelay,
		serverNodeID: serverNodeID, serverNodeAddress: serverNodeAddress, ttl: ttl,
		owners: make(map[*AgentSession]registry.NodeOwner),
	}
}

func (c *AgentConnectionLeaseController) Register(ctx context.Context, session *AgentSession) (registry.NodeOwner, error) {
	if c == nil || c.reg == nil || session == nil {
		return registry.NodeOwner{}, errors.New("agent connection lease controller is not configured")
	}
	if c.manager != nil {
		if current, ok := c.manager.GetConnection(session.AgentID, session.ConnectionID); !ok || current != session {
			return registry.NodeOwner{}, errors.New("agent session is not current")
		}
	}
	owner, err := c.reg.Register(ctx, registry.NodeRegistration{
		NodeID: c.serverNodeID, AgentID: session.AgentID,
		Address: c.serverNodeAddress, InstanceID: session.InstanceID, ConnectionID: session.ConnectionID,
		ServerNodeID: c.serverNodeID, TTL: c.ttl,
	})
	if err != nil {
		return registry.NodeOwner{}, err
	}
	c.mu.Lock()
	c.owners[session] = owner
	c.mu.Unlock()
	return owner, nil
}

func (c *AgentConnectionLeaseController) Heartbeat(ctx context.Context, owner registry.NodeOwner, session *AgentSession) error {
	if c == nil || c.reg == nil || session == nil {
		return errors.New("agent connection lease controller is not configured")
	}
	leaseCtx, cancel := context.WithTimeout(ctx, agentConnectionLeaseTimeout)
	defer cancel()
	updated, err := c.reg.KeepAlive(leaseCtx, owner, c.ttl)
	if err != nil {
		return err
	}
	owner.ExpiresAt = updated.ExpiresAt
	if c.localRelay != nil {
		owner.ActiveStreams = int64(c.localRelay.ActiveStreams(session.AgentID, session.ConnectionID))
	}
	owner.HealthScore = 100
	if err := c.reg.UpdateConnectionStats(leaseCtx, owner); err != nil {
		return err
	}
	c.mu.Lock()
	c.owners[session] = owner
	c.mu.Unlock()
	return nil
}

func (c *AgentConnectionLeaseController) HeartbeatForSession(ctx context.Context, session *AgentSession) error {
	if c == nil || session == nil {
		return errors.New("agent connection lease controller is not configured")
	}
	c.mu.Lock()
	owner, ok := c.owners[session]
	c.mu.Unlock()
	if !ok {
		return registry.ErrNotFound
	}
	return c.Heartbeat(ctx, owner, session)
}

func (c *AgentConnectionLeaseController) Release(ctx context.Context, owner registry.NodeOwner) error {
	if c == nil || c.reg == nil {
		return errors.New("agent connection lease controller is not configured")
	}
	releaseCtx, cancel := context.WithTimeout(ctx, agentConnectionLeaseTimeout)
	defer cancel()
	err := c.reg.Revoke(releaseCtx, owner)
	c.mu.Lock()
	for session, current := range c.owners {
		if current.AgentID == owner.AgentID && current.ConnectionID == owner.ConnectionID && current.Epoch == owner.Epoch {
			delete(c.owners, session)
		}
	}
	c.mu.Unlock()
	return err
}

// CloseConnection resolves a registry lease epoch to the current local Agent
// session epoch. Cross-node callers only know the durable lease epoch; the
// owning Server performs the final local fencing before closing transport.
func (c *AgentConnectionLeaseController) CloseConnection(_ context.Context, agentID, connectionID string, leaseConnectionEpoch int64) error {
	if c == nil || c.manager == nil {
		return ErrSessionClosed
	}
	c.mu.Lock()
	var (
		session *AgentSession
		owner   registry.NodeOwner
	)
	for currentSession, currentOwner := range c.owners {
		if currentOwner.AgentID == agentID && currentOwner.ConnectionID == connectionID {
			session, owner = currentSession, currentOwner
			break
		}
	}
	c.mu.Unlock()
	if session == nil {
		return ErrSessionClosed
	}
	if owner.ConnectionEpoch != leaseConnectionEpoch {
		return ErrEpoch
	}
	return c.manager.CloseConnection(agentID, connectionID, session.ConnectionEpoch)
}
