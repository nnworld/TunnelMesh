package server

import (
	"context"
	"database/sql"
	"errors"
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
	// Renew is fenced on connection_epoch, so it matches zero rows whenever the
	// stored token differs from the authoritative in-memory one. That is the
	// state a lease row is left in when MySQL clamped the 32-bit column and the
	// row was written with a truncated token. The live WebSocket is the authority
	// on whether the connection exists and on its true token, so re-register
	// instead of letting the lease expire underneath a connected client; that is
	// what lets an already-connected client recover without a reconnect.
	// Register already writes the fresh expiry and counters, which makes the
	// separate stats update redundant on that path.
	//
	// The instance-ID guard keeps the invariant that every lease row identifies a
	// known client instance. Before CLIENT_HELLO (or the legacy downgrade) there
	// is no row to heal yet, and creating one would leave an orphan that no
	// instance-scoped query can attribute.
	if err := c.connections.Renew(ctx, record.ConnectionID, record.ConnectionEpoch, c.ttl); err != nil {
		if !errors.Is(err, sql.ErrNoRows) || record.ClientInstanceID == "" {
			return err
		}
		_, err = c.Register(ctx, record)
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
