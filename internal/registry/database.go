package registry

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

// DatabaseRegistry stores durable node metadata in server_nodes and ephemeral
// agent ownership in agent_runtime_leases. The lease repository performs the
// conditional epoch update, so stale owners cannot renew or release a lease.
type DatabaseRegistry struct {
	nodes    storage.NodeRepository
	leases   storage.LeaseRepository
	mu       sync.Mutex
	watchers map[string]map[chan RegistryEvent]struct{}
	closed   bool
}

// NewDatabaseRegistry accepts either a *storage.DB or a NodeRepository plus
// LeaseRepository. The latter keeps the adapter easy to unit-test with mocks.
func NewDatabaseRegistry(source any, lease ...storage.LeaseRepository) *DatabaseRegistry {
	switch v := source.(type) {
	case *storage.DB:
		if v == nil {
			return nil
		}
		return NewDatabaseRegistryWithRepositories(v.Nodes(), v.Leases())
	case storage.NodeRepository:
		if len(lease) == 0 {
			return nil
		}
		return NewDatabaseRegistryWithRepositories(v, lease[0])
	default:
		return nil
	}
}

func NewDatabaseRegistryWithRepositories(nodes storage.NodeRepository, leases storage.LeaseRepository) *DatabaseRegistry {
	return &DatabaseRegistry{nodes: nodes, leases: leases, watchers: make(map[string]map[chan RegistryEvent]struct{})}
}

func (r *DatabaseRegistry) Register(ctx context.Context, req NodeRegistration) (NodeOwner, error) {
	if r == nil || r.nodes == nil || r.leases == nil {
		return NodeOwner{}, errors.New("registry is not configured")
	}
	if strings.TrimSpace(req.NodeID) == "" || strings.TrimSpace(req.AgentID) == "" {
		return NodeOwner{}, errors.New("node_id and agent_id are required")
	}
	ttl := req.TTL
	if ttl <= 0 {
		ttl = time.Minute
	}
	serverNodeID := req.ServerNodeID
	if serverNodeID == "" {
		serverNodeID = req.NodeID
	}
	lease, err := r.leases.RegisterConnection(ctx, storage.AgentLease{
		AgentID: req.AgentID, NodeID: req.NodeID, InstanceID: req.InstanceID,
		ConnectionID: req.ConnectionID, ServerNodeID: serverNodeID, TTL: ttl,
	})
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "held by") {
			return NodeOwner{}, ErrLeaseHeld
		}
		return NodeOwner{}, err
	}
	now := time.Now().UTC()
	node, getErr := r.nodes.Get(ctx, req.NodeID)
	if errors.Is(getErr, sql.ErrNoRows) {
		node = storage.ServerNode{ID: req.NodeID, Address: req.Address, Metadata: req.Metadata, Epoch: lease.Epoch, LastSeenAt: &now, ExpiresAt: &lease.ExpiresAt}
		if err = r.nodes.Create(ctx, node); err != nil {
			_ = r.leases.ReleaseConnection(ctx, req.AgentID, lease.ConnectionID, lease.ConnectionEpoch)
			return NodeOwner{}, err
		}
	} else if getErr != nil {
		_ = r.leases.ReleaseConnection(ctx, req.AgentID, lease.ConnectionID, lease.ConnectionEpoch)
		return NodeOwner{}, getErr
	} else {
		node.Address, node.Metadata, node.Epoch, node.LastSeenAt, node.ExpiresAt = req.Address, req.Metadata, lease.Epoch, &now, &lease.ExpiresAt
		if err = r.nodes.Update(ctx, node); err != nil {
			_ = r.leases.ReleaseConnection(ctx, req.AgentID, lease.ConnectionID, lease.ConnectionEpoch)
			return NodeOwner{}, err
		}
	}
	owner := NodeOwner{NodeID: req.NodeID, Address: req.Address, Metadata: req.Metadata, AgentID: req.AgentID, InstanceID: lease.InstanceID, ConnectionID: lease.ConnectionID, ServerNodeID: lease.ServerNodeID, Epoch: lease.ConnectionEpoch, ConnectionEpoch: lease.ConnectionEpoch, ServerNodeEpoch: lease.Epoch, ActiveStreams: lease.ActiveStreams, HealthScore: lease.HealthScore, ExpiresAt: lease.ExpiresAt}
	r.publish(req.AgentID, RegistryEvent{Type: EventRegistered, Owner: owner})
	return owner, nil
}

func (r *DatabaseRegistry) KeepAlive(ctx context.Context, owner NodeOwner, ttl time.Duration) (NodeOwner, error) {
	if err := r.leases.RenewConnection(ctx, owner.AgentID, owner.ConnectionID, owner.Epoch, ttl); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return NodeOwner{}, ErrFencing
		}
		return NodeOwner{}, err
	}
	leases, err := r.leases.ListActiveByAgent(ctx, owner.AgentID)
	if err != nil {
		return NodeOwner{}, err
	}
	var lease storage.AgentLease
	found := false
	for _, candidate := range leases {
		if candidate.ConnectionID == owner.ConnectionID {
			lease = candidate
			found = true
			break
		}
	}
	if !found || lease.ConnectionEpoch != owner.Epoch || lease.NodeID != owner.NodeID {
		return NodeOwner{}, ErrFencing
	}
	updated := owner
	updated.ExpiresAt = lease.ExpiresAt
	now := time.Now().UTC()
	if node, e := r.nodes.Get(ctx, owner.NodeID); e == nil {
		node.LastSeenAt, node.ExpiresAt = &now, &updated.ExpiresAt
		_ = r.nodes.Update(ctx, node)
	}
	r.publish(owner.AgentID, RegistryEvent{Type: EventUpdated, Owner: updated})
	return updated, nil
}

// UpdateConnectionStats persists local relay load for one fenced connection
// lease. Ownership fields are not modified.
func (r *DatabaseRegistry) UpdateConnectionStats(ctx context.Context, owner NodeOwner) error {
	if r == nil || r.leases == nil {
		return errors.New("registry is not configured")
	}
	return r.leases.UpdateConnectionStats(ctx, storage.AgentLease{
		AgentID: owner.AgentID, ConnectionID: owner.ConnectionID,
		ConnectionEpoch: owner.ConnectionEpoch,
		ActiveStreams:   owner.ActiveStreams, HealthScore: owner.HealthScore,
	})
}

func (r *DatabaseRegistry) ResolveAgent(ctx context.Context, agentID string) (NodeOwner, error) {
	lease, err := r.leases.Get(ctx, agentID)
	if errors.Is(err, sql.ErrNoRows) {
		return NodeOwner{}, ErrNotFound
	}
	if err != nil {
		return NodeOwner{}, err
	}
	if !lease.ExpiresAt.After(time.Now().UTC()) {
		return NodeOwner{}, ErrLeaseExpired
	}
	owners, err := r.ListAgentConnections(ctx, agentID)
	if err != nil {
		return NodeOwner{}, err
	}
	if len(owners) == 0 {
		return NodeOwner{}, ErrNotFound
	}
	return owners[0], nil
}

func (r *DatabaseRegistry) ListAgentConnections(ctx context.Context, agentID string) ([]NodeOwner, error) {
	leases, err := r.leases.ListActiveByAgent(ctx, agentID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	owners := make([]NodeOwner, 0, len(leases))
	for _, lease := range leases {
		if !lease.ExpiresAt.After(time.Now().UTC()) {
			continue
		}
		nodeID := lease.ServerNodeID
		if nodeID == "" {
			nodeID = lease.NodeID
		}
		node, err := r.nodes.Get(ctx, nodeID)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		owners = append(owners, NodeOwner{NodeID: node.ID, Address: node.Address, Metadata: node.Metadata, AgentID: lease.AgentID, InstanceID: lease.InstanceID, ConnectionID: lease.ConnectionID, ServerNodeID: nodeID, Epoch: lease.ConnectionEpoch, ConnectionEpoch: lease.ConnectionEpoch, ServerNodeEpoch: node.Epoch, ActiveStreams: lease.ActiveStreams, HealthScore: lease.HealthScore, ExpiresAt: lease.ExpiresAt})
	}
	return owners, nil
}

func (r *DatabaseRegistry) Watch(ctx context.Context, agentID string) (<-chan RegistryEvent, error) {
	if strings.TrimSpace(agentID) == "" {
		return nil, errors.New("agent_id is required")
	}
	ch := make(chan RegistryEvent, 16)
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		close(ch)
		return ch, ErrRevoked
	}
	if r.watchers[agentID] == nil {
		r.watchers[agentID] = make(map[chan RegistryEvent]struct{})
	}
	r.watchers[agentID][ch] = struct{}{}
	r.mu.Unlock()
	go func() {
		<-ctx.Done()
		r.mu.Lock()
		if ws := r.watchers[agentID]; ws != nil {
			if _, ok := ws[ch]; ok {
				delete(ws, ch)
				close(ch)
			}
		}
		r.mu.Unlock()
	}()
	return ch, nil
}

func (r *DatabaseRegistry) Revoke(ctx context.Context, owner NodeOwner) error {
	if err := r.leases.ReleaseConnection(ctx, owner.AgentID, owner.ConnectionID, owner.Epoch); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrFencing
		}
		return err
	}
	r.publish(owner.AgentID, RegistryEvent{Type: EventRevoked, Owner: owner})
	return nil
}

func (r *DatabaseRegistry) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	for agent, ws := range r.watchers {
		for ch := range ws {
			close(ch)
		}
		delete(r.watchers, agent)
	}
	return nil
}

func (r *DatabaseRegistry) publish(agentID string, ev RegistryEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for ch := range r.watchers[agentID] {
		select {
		case ch <- ev:
		default:
		}
	}
}
