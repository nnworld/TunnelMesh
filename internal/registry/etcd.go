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
	NodeID    string    `json:"node_id"`
	Address   string    `json:"address"`
	Metadata  string    `json:"metadata,omitempty"`
	AgentID   string    `json:"agent_id"`
	Epoch     int64     `json:"epoch"`
	ExpiresAt time.Time `json:"expires_at"`
	LeaseID   int64     `json:"lease_id"`
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

func (r *EtcdRegistry) nodeKey(id string) string       { return r.prefix + "/nodes/" + id }
func (r *EtcdRegistry) agentKey(id string) string      { return r.prefix + "/agents/" + id }
func (r *EtcdRegistry) agentEpochKey(id string) string { return r.prefix + "/agents/" + id + "/epoch" }

func (r *EtcdRegistry) Register(ctx context.Context, req NodeRegistration) (NodeOwner, error) {
	if r == nil || r.client == nil {
		return NodeOwner{}, errors.New("etcd registry is not configured")
	}
	if strings.TrimSpace(req.NodeID) == "" || strings.TrimSpace(req.AgentID) == "" {
		return NodeOwner{}, errors.New("node_id and agent_id are required")
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
	var existingKV *mvccpb.KeyValue
	if resp, e := r.client.Get(ctx, r.agentKey(req.AgentID)); e == nil && len(resp.Kvs) > 0 {
		existingKV = resp.Kvs[0]
		var current etcdOwnerValue
		if json.Unmarshal(existingKV.Value, &current) == nil && !current.ExpiresAt.After(time.Now().UTC()) {
			// The lease server may take a short while to deliver its delete
			// event. Treat an expired value as reclaimable, guarded by modrev.
		} else {
			_, _ = r.client.Revoke(ctx, leaseResp.ID)
			return NodeOwner{}, ErrLeaseHeld
		}
	}
	var oldEpoch int64
	var epochKV *mvccpb.KeyValue
	if resp, e := r.client.Get(ctx, r.agentEpochKey(req.AgentID)); e == nil && len(resp.Kvs) > 0 {
		epochKV = resp.Kvs[0]
		_, _ = fmt.Sscanf(string(epochKV.Value), "%d", &oldEpoch)
	}
	if oldEpoch == 0 {
		if resp, e := r.client.Get(ctx, r.nodeKey(req.NodeID)); e == nil && len(resp.Kvs) > 0 {
			var old etcdOwnerValue
			_ = json.Unmarshal(resp.Kvs[0].Value, &old)
			oldEpoch = old.Epoch
		}
	}
	epoch := oldEpoch + 1
	now := time.Now().UTC()
	owner := NodeOwner{NodeID: req.NodeID, Address: req.Address, Metadata: req.Metadata, AgentID: req.AgentID, Epoch: epoch, ExpiresAt: now.Add(ttl), LeaseID: int64(leaseResp.ID)}
	b, _ := json.Marshal(etcdOwnerValue{NodeID: owner.NodeID, Address: owner.Address, Metadata: owner.Metadata, AgentID: owner.AgentID, Epoch: owner.Epoch, ExpiresAt: owner.ExpiresAt, LeaseID: owner.LeaseID})
	// Keep the node record persistent so its epoch survives an expired agent
	// lease; the agent key itself is the ephemeral ownership marker.
	// The marker comparison closes the read/CAS gap: a contender that read an
	// older epoch cannot overwrite a newer marker after the previous lease
	// expires and is replaced.
	compares := []clientv3.Cmp{}
	thenOps := []clientv3.Op{}
	if existingKV == nil {
		compares = append(compares, clientv3.Compare(clientv3.Version(r.agentKey(req.AgentID)), "=", 0))
	} else {
		compares = append(compares, clientv3.Compare(clientv3.ModRevision(r.agentKey(req.AgentID)), "=", existingKV.ModRevision))
		thenOps = append(thenOps, clientv3.OpDelete(r.agentKey(req.AgentID)))
	}
	if epochKV == nil {
		compares = append(compares, clientv3.Compare(clientv3.Version(r.agentEpochKey(req.AgentID)), "=", 0))
	} else {
		compares = append(compares, clientv3.Compare(clientv3.ModRevision(r.agentEpochKey(req.AgentID)), "=", epochKV.ModRevision))
	}
	thenOps = append(thenOps, clientv3.OpPut(r.agentEpochKey(req.AgentID), fmt.Sprintf("%d", epoch)), clientv3.OpPut(r.nodeKey(req.NodeID), string(b)), clientv3.OpPut(r.agentKey(req.AgentID), string(b), clientv3.WithLease(leaseResp.ID)))
	txn := r.client.Txn(ctx).If(compares...).Then(thenOps...)
	resp, err := txn.Commit()
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
	currentResp, err := r.client.Get(ctx, r.agentKey(owner.AgentID))
	if err != nil || len(currentResp.Kvs) == 0 {
		return NodeOwner{}, ErrFencing
	}
	var current etcdOwnerValue
	if json.Unmarshal(currentResp.Kvs[0].Value, &current) != nil || current.Epoch != owner.Epoch || current.NodeID != owner.NodeID {
		return NodeOwner{}, ErrFencing
	}
	if owner.LeaseID == 0 {
		owner.LeaseID = current.LeaseID
	}
	if current.LeaseID != owner.LeaseID {
		return NodeOwner{}, ErrFencing
	}
	resp, err := r.client.KeepAliveOnce(ctx, clientv3.LeaseID(owner.LeaseID))
	if err != nil || resp == nil {
		return NodeOwner{}, ErrFencing
	}
	if resp.TTL <= 0 {
		return NodeOwner{}, ErrLeaseExpired
	}
	owner.ExpiresAt = time.Now().UTC().Add(time.Duration(resp.TTL) * time.Second)
	// Keep the value's expiry in sync with the etcd lease. ResolveAgent reads
	// the value, while the lease controls key liveness; updating both avoids a
	// renewed owner being mistaken for an expired one after its first TTL.
	b, _ := json.Marshal(etcdOwnerValue{NodeID: owner.NodeID, Address: owner.Address, Metadata: owner.Metadata, AgentID: owner.AgentID, Epoch: owner.Epoch, ExpiresAt: owner.ExpiresAt, LeaseID: owner.LeaseID})
	updated, err := r.client.Txn(ctx).If(clientv3.Compare(clientv3.ModRevision(r.agentKey(owner.AgentID)), "=", currentResp.Kvs[0].ModRevision)).Then(clientv3.OpPut(r.agentEpochKey(owner.AgentID), fmt.Sprintf("%d", owner.Epoch)), clientv3.OpPut(r.agentKey(owner.AgentID), string(b), clientv3.WithLease(clientv3.LeaseID(owner.LeaseID))), clientv3.OpPut(r.nodeKey(owner.NodeID), string(b))).Commit()
	if err != nil || !updated.Succeeded {
		return NodeOwner{}, ErrFencing
	}
	return owner, nil
}

func (r *EtcdRegistry) ResolveAgent(ctx context.Context, agentID string) (NodeOwner, error) {
	resp, err := r.client.Get(ctx, r.agentKey(agentID))
	if err != nil {
		return NodeOwner{}, err
	}
	if len(resp.Kvs) == 0 {
		return NodeOwner{}, ErrNotFound
	}
	var v etcdOwnerValue
	if err := json.Unmarshal(resp.Kvs[0].Value, &v); err != nil {
		return NodeOwner{}, fmt.Errorf("decode owner: %w", err)
	}
	if v.ExpiresAt.Before(time.Now().UTC()) {
		return NodeOwner{}, ErrLeaseExpired
	}
	return NodeOwner{NodeID: v.NodeID, Address: v.Address, Metadata: v.Metadata, AgentID: v.AgentID, Epoch: v.Epoch, ExpiresAt: v.ExpiresAt, LeaseID: v.LeaseID}, nil
}

func (r *EtcdRegistry) Watch(ctx context.Context, agentID string) (<-chan RegistryEvent, error) {
	if strings.TrimSpace(agentID) == "" {
		return nil, errors.New("agent_id is required")
	}
	out := make(chan RegistryEvent, 16)
	wch := r.client.Watch(ctx, r.agentKey(agentID), clientv3.WithPrevKV())
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
				if json.Unmarshal(raw, &v) != nil {
					continue
				}
				typ := EventUpdated
				if ev.Type == mvccpb.DELETE {
					typ = EventRevoked
				} else if ev.IsCreate() {
					typ = EventRegistered
				}
				owner := NodeOwner{NodeID: v.NodeID, Address: v.Address, Metadata: v.Metadata, AgentID: v.AgentID, Epoch: v.Epoch, ExpiresAt: v.ExpiresAt, LeaseID: v.LeaseID}
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
	resp, err := r.client.Get(ctx, r.agentKey(owner.AgentID))
	if err != nil {
		return err
	}
	if len(resp.Kvs) == 0 {
		return ErrNotFound
	}
	var current etcdOwnerValue
	if json.Unmarshal(resp.Kvs[0].Value, &current) != nil {
		return ErrFencing
	}
	if current.Epoch != owner.Epoch || current.NodeID != owner.NodeID {
		return ErrFencing
	}
	deleted, err := r.client.Txn(ctx).If(clientv3.Compare(clientv3.ModRevision(r.agentKey(owner.AgentID)), "=", resp.Kvs[0].ModRevision)).Then(clientv3.OpDelete(r.agentKey(owner.AgentID))).Commit()
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
