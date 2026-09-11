package server

import (
	"context"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

const (
	DefaultClientConnectionHeartbeatInterval = 30 * time.Second
	DefaultClientConnectionLeaseTTL          = 90 * time.Second
)

// ClientConnectionLeaseController persists physical Client WebSocket leases.
// The in-memory manager is authoritative for immediate counters while each
// heartbeat writes a bounded durable snapshot for cross-node administration.
type ClientConnectionLeaseController struct {
	connections  storage.ClientConnectionRepository
	manager      *ClientSessionManager
	serverNodeID string
	ttl          time.Duration
}

func NewClientConnectionLeaseController(connections storage.ClientConnectionRepository, manager *ClientSessionManager, serverNodeID string, ttl time.Duration) *ClientConnectionLeaseController {
	if ttl <= 0 {
		ttl = DefaultClientConnectionLeaseTTL
	}
	return &ClientConnectionLeaseController{
		connections: connections, manager: manager,
		serverNodeID: serverNodeID, ttl: ttl,
	}
}

func (c *ClientConnectionLeaseController) Register(ctx context.Context, record ClientSessionRecord) (ClientSessionRecord, error) {
	if c == nil || c.connections == nil {
		return record, ErrSessionClosed
	}
	now := time.Now().UTC()
	acquired := record.StartedAt
	if acquired.IsZero() {
		acquired = now
	}
	expires := now.Add(c.ttl)
	lease := storage.ClientConnectionLease{
		ConnectionID: record.ConnectionID, ClientInstanceID: record.ClientInstanceID,
		TokenID: record.TokenID, OwnerUserID: record.OwnerUserID, ServerNodeID: c.serverNodeID,
		ConnectionEpoch: record.ConnectionEpoch, ActiveStreams: record.ActiveStreams,
		HealthScore: 100, AcquiredAt: acquired, ExpiresAt: expires, UpdatedAt: now,
	}
	persisted, err := c.connections.Register(ctx, lease)
	if err != nil {
		return record, err
	}
	record.ClientInstanceID = persisted.ClientInstanceID
	return record, nil
}

func (c *ClientConnectionLeaseController) Heartbeat(ctx context.Context, record ClientSessionRecord) error {
	if c == nil || c.connections == nil {
		return ErrSessionClosed
	}
	if current, ok := c.manager.Get(record.ConnectionID); ok {
		record = current
	}
	if err := c.connections.Renew(ctx, record.ConnectionID, record.ConnectionEpoch, c.ttl); err != nil {
		return err
	}
	return c.connections.UpdateStats(ctx, storage.ClientConnectionLease{
		ConnectionID: record.ConnectionID, ClientInstanceID: record.ClientInstanceID,
		TokenID: record.TokenID, OwnerUserID: record.OwnerUserID, ServerNodeID: c.serverNodeID,
		ConnectionEpoch: record.ConnectionEpoch, ActiveStreams: record.ActiveStreams,
		HealthScore: 100, AcquiredAt: record.StartedAt, ExpiresAt: time.Now().UTC().Add(c.ttl),
		UpdatedAt: time.Now().UTC(),
	})
}

func (c *ClientConnectionLeaseController) Release(ctx context.Context, connectionID string, epoch int64) error {
	if c == nil || c.connections == nil {
		return ErrSessionClosed
	}
	return c.connections.Release(ctx, connectionID, epoch)
}

func (c *ClientConnectionLeaseController) CloseConnection(connectionID string, epoch int64) error {
	if c == nil || c.manager == nil {
		return ErrSessionClosed
	}
	return c.manager.CloseConnection(connectionID, epoch)
}
