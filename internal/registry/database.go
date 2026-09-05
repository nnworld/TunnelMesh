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
	lease, err := r.leases.Acquire(ctx, storage.AgentLease{AgentID: req.AgentID, NodeID: req.NodeID, TTL: ttl})
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
			_ = r.leases.Release(ctx, req.AgentID, lease.Epoch)
			return NodeOwner{}, err
		}
	} else if getErr != nil {
		_ = r.leases.Release(ctx, req.AgentID, lease.Epoch)
		return NodeOwner{}, getErr
	} else {
		node.Address, node.Metadata, node.Epoch, node.LastSeenAt, node.ExpiresAt = req.Address, req.Metadata, lease.Epoch, &now, &lease.ExpiresAt
		if err = r.nodes.Update(ctx, node); err != nil {
			_ = r.leases.Release(ctx, req.AgentID, lease.Epoch)
			return NodeOwner{}, err
		}
	}
	owner := NodeOwner{NodeID: req.NodeID, Address: req.Address, Metadata: req.Metadata, AgentID: req.AgentID, Epoch: lease.Epoch, ExpiresAt: lease.ExpiresAt}
	r.publish(req.AgentID, RegistryEvent{Type: EventRegistered, Owner: owner})
	return owner, nil
}

func (r *DatabaseRegistry) KeepAlive(ctx context.Context, owner NodeOwner, ttl time.Duration) (NodeOwner, error) {
	if err := r.leases.Renew(ctx, owner.AgentID, owner.Epoch, ttl); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return NodeOwner{}, ErrFencing
		}
		return NodeOwner{}, err
	}
	lease, err := r.leases.Get(ctx, owner.AgentID)
	if err != nil {
		return NodeOwner{}, err
	}
	if lease.Epoch != owner.Epoch || lease.NodeID != owner.NodeID {
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
	node, err := r.nodes.Get(ctx, lease.NodeID)
	if errors.Is(err, sql.ErrNoRows) {
		return NodeOwner{}, ErrNotFound
	}
	if err != nil {
		return NodeOwner{}, err
	}
	return NodeOwner{NodeID: node.ID, Address: node.Address, Metadata: node.Metadata, AgentID: agentID, Epoch: lease.Epoch, ExpiresAt: lease.ExpiresAt}, nil
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
	if err := r.leases.Release(ctx, owner.AgentID, owner.Epoch); err != nil {
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
