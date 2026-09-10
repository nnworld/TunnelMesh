package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.etcd.io/etcd/api/v3/mvccpb"
	clientv3 "go.etcd.io/etcd/client/v3"
)

const defaultEtcdPrefix = "/tunnelmesh"

type etcdOwnerValue struct {
	NodeID          string    `json:"node_id"`
	Address         string    `json:"address"`
	Metadata        string    `json:"metadata,omitempty"`
	AgentID         string    `json:"agent_id"`
	InstanceID      string    `json:"instance_id,omitempty"`
	ConnectionID    string    `json:"connection_id"`
	ServerNodeID    string    `json:"server_node_id"`
	Epoch           int64     `json:"epoch"`
	ConnectionEpoch int64     `json:"connection_epoch"`
	ServerNodeEpoch int64     `json:"server_node_epoch"`
	ActiveStreams   int64     `json:"active_streams,omitempty"`
	HealthScore     int64     `json:"health_score,omitempty"`
	ExpiresAt       time.Time `json:"expires_at"`
	LeaseID         int64     `json:"lease_id"`
}

type EtcdRegistry struct {
	client *clientv3.Client
	prefix string
	owned  bool
}

func NewEtcdRegistry(client *clientv3.Client, prefix string) *EtcdRegistry {
	if strings.TrimSpace(prefix) == "" {
		prefix = defaultEtcdPrefix
	}
	return &EtcdRegistry{client: client, prefix: strings.TrimRight(prefix, "/")}
}

func NewEtcdRegistryFromEndpoints(ctx context.Context, endpoints []string, prefix string) (*EtcdRegistry, error) {
	if len(endpoints) == 0 {
		return nil, errors.New("etcd endpoints are required")
	}
	c, err := clientv3.New(clientv3.Config{Endpoints: endpoints, DialTimeout: 5 * time.Second, Context: ctx})
	if err != nil {
		return nil, err
	}
	r := NewEtcdRegistry(c, prefix)
	r.owned = true
	return r, nil
}

func (r *EtcdRegistry) nodeKey(id string) string { return r.prefix + "/nodes/" + id }
func (r *EtcdRegistry) agentConnectionsPrefix(id string) string {
	return r.prefix + "/agents/" + id + "/connections/"
}
func (r *EtcdRegistry) agentConnectionKey(id, connectionID string) string {
	return r.agentConnectionsPrefix(id) + connectionID
}
func (r *EtcdRegistry) agentConnectionEpochKey(id, connectionID string) string {
	return r.prefix + "/agents/" + id + "/connection-epochs/" + connectionID
}

func (r *EtcdRegistry) Register(ctx context.Context, req NodeRegistration) (NodeOwner, error) {
	if r == nil || r.client == nil {
		return NodeOwner{}, errors.New("etcd registry is not configured")
	}
	if strings.TrimSpace(req.NodeID) == "" || strings.TrimSpace(req.AgentID) == "" {
		return NodeOwner{}, errors.New("node_id and agent_id are required")
	}
	connectionID := strings.TrimSpace(req.ConnectionID)
	if connectionID == "" {
		connectionID = "legacy"
	}
	serverNodeID := strings.TrimSpace(req.ServerNodeID)
	if serverNodeID == "" {
		serverNodeID = req.NodeID
	}
	ttl := req.TTL
	if ttl <= 0 {
		ttl = time.Minute
	}
	ttlSec := int64(ttl / time.Second)
	if ttlSec < 1 {
		ttlSec = 1
	}
	leaseResp, err := r.client.Grant(ctx, ttlSec)
	if err != nil {
		return NodeOwner{}, err
	}
	connectionKey := r.agentConnectionKey(req.AgentID, connectionID)
	epochKey := r.agentConnectionEpochKey(req.AgentID, connectionID)
	var existingKV *mvccpb.KeyValue
	if resp, e := r.client.Get(ctx, connectionKey); e == nil && len(resp.Kvs) > 0 {
		existingKV = resp.Kvs[0]
		var current etcdOwnerValue
		if json.Unmarshal(existingKV.Value, &current) == nil && current.ExpiresAt.After(time.Now().UTC()) {
			_, _ = r.client.Revoke(ctx, leaseResp.ID)
			return NodeOwner{}, ErrLeaseHeld
		}
	} else if e != nil {
		_, _ = r.client.Revoke(ctx, leaseResp.ID)
		return NodeOwner{}, e
	}

	var oldEpoch int64
	var epochKV *mvccpb.KeyValue
	if resp, e := r.client.Get(ctx, epochKey); e == nil && len(resp.Kvs) > 0 {
		epochKV = resp.Kvs[0]
		_, _ = fmt.Sscanf(string(epochKV.Value), "%d", &oldEpoch)
	} else if e != nil {
		_, _ = r.client.Revoke(ctx, leaseResp.ID)
		return NodeOwner{}, e
	}
	epoch := oldEpoch + 1
	now := time.Now().UTC()
	owner := NodeOwner{
		NodeID: req.NodeID, Address: req.Address, Metadata: req.Metadata, AgentID: req.AgentID,
		InstanceID: req.InstanceID, ConnectionID: connectionID, ServerNodeID: serverNodeID,
		Epoch: epoch, ConnectionEpoch: epoch, ServerNodeEpoch: epoch, ExpiresAt: now.Add(ttl), LeaseID: int64(leaseResp.ID),
	}
	value, _ := json.Marshal(etcdOwnerValue{
		NodeID: owner.NodeID, Address: owner.Address, Metadata: owner.Metadata, AgentID: owner.AgentID,
		InstanceID: owner.InstanceID, ConnectionID: owner.ConnectionID, ServerNodeID: owner.ServerNodeID,
		Epoch: owner.Epoch, ConnectionEpoch: owner.ConnectionEpoch, ServerNodeEpoch: owner.ServerNodeEpoch, ExpiresAt: owner.ExpiresAt, LeaseID: owner.LeaseID,
	})
	compares := []clientv3.Cmp{}
	thenOps := []clientv3.Op{}
	if existingKV == nil {
		compares = append(compares, clientv3.Compare(clientv3.Version(connectionKey), "=", 0))
	} else {
		compares = append(compares, clientv3.Compare(clientv3.ModRevision(connectionKey), "=", existingKV.ModRevision))
	}
	if epochKV == nil {
		compares = append(compares, clientv3.Compare(clientv3.Version(epochKey), "=", 0))
	} else {
		compares = append(compares, clientv3.Compare(clientv3.ModRevision(epochKey), "=", epochKV.ModRevision))
	}
	thenOps = append(thenOps,
		clientv3.OpPut(epochKey, fmt.Sprintf("%d", epoch)),
		clientv3.OpPut(r.nodeKey(serverNodeID), string(value)),
		clientv3.OpPut(connectionKey, string(value), clientv3.WithLease(leaseResp.ID)),
	)
	resp, err := r.client.Txn(ctx).If(compares...).Then(thenOps...).Commit()
	if err != nil {
		_, _ = r.client.Revoke(ctx, leaseResp.ID)
		return NodeOwner{}, err
	}
	if !resp.Succeeded {
		_, _ = r.client.Revoke(ctx, leaseResp.ID)
		return NodeOwner{}, ErrLeaseHeld
	}
	return owner, nil
}

func (r *EtcdRegistry) KeepAlive(ctx context.Context, owner NodeOwner, ttl time.Duration) (NodeOwner, error) {
	key := r.agentConnectionKey(owner.AgentID, owner.ConnectionID)
	currentResp, err := r.client.Get(ctx, key)
	if err != nil || len(currentResp.Kvs) == 0 {
		return NodeOwner{}, ErrFencing
	}
	var current etcdOwnerValue
	if json.Unmarshal(currentResp.Kvs[0].Value, &current) != nil ||
		current.Epoch != owner.Epoch || current.NodeID != owner.NodeID || current.ConnectionID != owner.ConnectionID {
		return NodeOwner{}, ErrFencing
	}
	if owner.LeaseID == 0 {
		owner.LeaseID = current.LeaseID
	}
	if current.LeaseID != owner.LeaseID {
		return NodeOwner{}, ErrFencing
	}
	resp, err := r.client.KeepAliveOnce(ctx, clientv3.LeaseID(owner.LeaseID))
	if err != nil || resp == nil || resp.TTL <= 0 {
		return NodeOwner{}, ErrLeaseExpired
	}
	owner.ExpiresAt = time.Now().UTC().Add(time.Duration(resp.TTL) * time.Second)
	value, _ := json.Marshal(etcdOwnerValue{
		NodeID: owner.NodeID, Address: owner.Address, Metadata: owner.Metadata, AgentID: owner.AgentID,
		InstanceID: owner.InstanceID, ConnectionID: owner.ConnectionID, ServerNodeID: owner.ServerNodeID,
		Epoch: owner.Epoch, ConnectionEpoch: owner.ConnectionEpoch, ServerNodeEpoch: owner.ServerNodeEpoch, ActiveStreams: owner.ActiveStreams, HealthScore: owner.HealthScore,
		ExpiresAt: owner.ExpiresAt, LeaseID: owner.LeaseID,
	})
	updated, err := r.client.Txn(ctx).
		If(clientv3.Compare(clientv3.ModRevision(key), "=", currentResp.Kvs[0].ModRevision)).
		Then(
			clientv3.OpPut(key, string(value), clientv3.WithLease(clientv3.LeaseID(owner.LeaseID))),
			clientv3.OpPut(r.nodeKey(owner.ServerNodeID), string(value)),
		).Commit()
	if err != nil || !updated.Succeeded {
		return NodeOwner{}, ErrFencing
	}
	return owner, nil
}

// UpdateConnectionStats writes local relay load without changing ownership.
// The transaction fences the update on the current ModRevision.
func (r *EtcdRegistry) UpdateConnectionStats(ctx context.Context, owner NodeOwner) error {
	key := r.agentConnectionKey(owner.AgentID, owner.ConnectionID)
	currentResp, err := r.client.Get(ctx, key)
	if err != nil || len(currentResp.Kvs) == 0 {
		return ErrFencing
	}
	var current etcdOwnerValue
	if json.Unmarshal(currentResp.Kvs[0].Value, &current) != nil ||
		current.Epoch != owner.Epoch || current.NodeID != owner.NodeID || current.ConnectionID != owner.ConnectionID {
		return ErrFencing
	}
	current.ActiveStreams = owner.ActiveStreams
	current.HealthScore = owner.HealthScore
	value, err := json.Marshal(current)
	if err != nil {
		return err
	}
	updated, err := r.client.Txn(ctx).
		If(clientv3.Compare(clientv3.ModRevision(key), "=", currentResp.Kvs[0].ModRevision)).
		Then(
			clientv3.OpPut(key, string(value), clientv3.WithLease(clientv3.LeaseID(current.LeaseID))),
			clientv3.OpPut(r.nodeKey(current.ServerNodeID), string(value)),
		).Commit()
	if err != nil {
		return err
	}
	if !updated.Succeeded {
		return ErrFencing
	}
	return nil
}

func (r *EtcdRegistry) ResolveAgent(ctx context.Context, agentID string) (NodeOwner, error) {
	owners, err := r.ListAgentConnections(ctx, agentID)
	if err != nil {
		return NodeOwner{}, err
	}
	if len(owners) == 0 {
		return NodeOwner{}, ErrNotFound
	}
	return owners[0], nil
}

func (r *EtcdRegistry) ListAgentConnections(ctx context.Context, agentID string) ([]NodeOwner, error) {
	resp, err := r.client.Get(ctx, r.agentConnectionsPrefix(agentID), clientv3.WithPrefix())
	if err != nil {
		return nil, err
	}
	owners := make([]NodeOwner, 0, len(resp.Kvs))
	now := time.Now().UTC()
	for _, kv := range resp.Kvs {
		var v etcdOwnerValue
		if json.Unmarshal(kv.Value, &v) != nil || v.AgentID != agentID || !v.ExpiresAt.After(now) {
			continue
		}
		owners = append(owners, NodeOwner{
			NodeID: v.NodeID, Address: v.Address, Metadata: v.Metadata, AgentID: v.AgentID,
			InstanceID: v.InstanceID, ConnectionID: v.ConnectionID, ServerNodeID: v.ServerNodeID,
			Epoch: v.Epoch, ConnectionEpoch: v.ConnectionEpoch, ServerNodeEpoch: v.ServerNodeEpoch, ActiveStreams: v.ActiveStreams, HealthScore: v.HealthScore,
			ExpiresAt: v.ExpiresAt, LeaseID: v.LeaseID,
		})
	}
	return owners, nil
}

func (r *EtcdRegistry) Watch(ctx context.Context, agentID string) (<-chan RegistryEvent, error) {
	if strings.TrimSpace(agentID) == "" {
		return nil, errors.New("agent_id is required")
	}
	out := make(chan RegistryEvent, 16)
	wch := r.client.Watch(ctx, r.agentConnectionsPrefix(agentID), clientv3.WithPrevKV())
	go func() {
		defer close(out)
		for resp := range wch {
			if resp.Err() != nil {
				return
			}
			for _, ev := range resp.Events {
				var raw []byte
				if ev.Type == mvccpb.PUT {
					raw = ev.Kv.Value
				} else if ev.PrevKv != nil {
					raw = ev.PrevKv.Value
				} else {
					continue
				}
				var v etcdOwnerValue
				if json.Unmarshal(raw, &v) != nil || v.AgentID != agentID {
					continue
				}
				typ := EventUpdated
				if ev.Type == mvccpb.DELETE {
					typ = EventRevoked
				} else if ev.IsCreate() {
					typ = EventRegistered
				}
				owner := NodeOwner{
					NodeID: v.NodeID, Address: v.Address, Metadata: v.Metadata, AgentID: v.AgentID,
					InstanceID: v.InstanceID, ConnectionID: v.ConnectionID, ServerNodeID: v.ServerNodeID,
					Epoch: v.Epoch, ConnectionEpoch: v.ConnectionEpoch, ServerNodeEpoch: v.ServerNodeEpoch, ActiveStreams: v.ActiveStreams, HealthScore: v.HealthScore,
					ExpiresAt: v.ExpiresAt, LeaseID: v.LeaseID,
				}
				select {
				case out <- RegistryEvent{Type: typ, Owner: owner}:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out, nil
}

func (r *EtcdRegistry) Revoke(ctx context.Context, owner NodeOwner) error {
	key := r.agentConnectionKey(owner.AgentID, owner.ConnectionID)
	resp, err := r.client.Get(ctx, key)
	if err != nil {
		return err
	}
	if len(resp.Kvs) == 0 {
		return ErrNotFound
	}
	var current etcdOwnerValue
	if json.Unmarshal(resp.Kvs[0].Value, &current) != nil ||
		current.Epoch != owner.Epoch || current.NodeID != owner.NodeID || current.ConnectionID != owner.ConnectionID {
		return ErrFencing
	}
	deleted, err := r.client.Txn(ctx).
		If(clientv3.Compare(clientv3.ModRevision(key), "=", resp.Kvs[0].ModRevision)).
		Then(clientv3.OpDelete(key)).Commit()
	if err != nil {
		return err
	}
	if !deleted.Succeeded {
		return ErrFencing
	}
	if current.LeaseID != 0 {
		_, _ = r.client.Revoke(ctx, clientv3.LeaseID(current.LeaseID))
	}
	return nil
}

func (r *EtcdRegistry) Close() error {
	if r.owned && r.client != nil {
		return r.client.Close()
	}
	return nil
}
