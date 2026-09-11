package server

import (
	"context"
	"database/sql"
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/tunnelmesh/tunnelmesh/internal/relay"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

// ErrClientConnectionNodeUnavailable means the owning Server node could not
// be read or reached; the durable Client lease remains in place.
var ErrClientConnectionNodeUnavailable = errors.New("client connection owner node unavailable")

// ClientConnectionRelayClient is the narrow authenticated control client used
// by the cluster close service.
type ClientConnectionRelayClient interface {
	CloseClientConnection(context.Context, relay.CloseClientConnectionRequest) error
	Close() error
}

// ClusterClientConnectionCloseService routes an exact Client lease close to
// its owning Server node.
type ClusterClientConnectionCloseService struct {
	connections   storage.ClientConnectionRepository
	nodes         storage.NodeRepository
	local         *ClientConnectionLeaseController
	localNodeID   string
	dialRelayNode func(context.Context, string, int64) (ClientConnectionRelayClient, error)
}

func NewClusterClientConnectionCloseService(connections storage.ClientConnectionRepository, nodes storage.NodeRepository, local *ClientConnectionLeaseController, localNodeID string, dialRelayNode func(context.Context, string, int64) (ClientConnectionRelayClient, error)) *ClusterClientConnectionCloseService {
	return &ClusterClientConnectionCloseService{
		connections: connections, nodes: nodes, local: local, localNodeID: localNodeID,
		dialRelayNode: dialRelayNode,
	}
}

func (s *ClusterClientConnectionCloseService) Close(ctx context.Context, request ClientConnectionCloseRequest) error {
	if s == nil || s.connections == nil {
		return ErrClientConnectionNodeUnavailable
	}
	if request.ConnectionID == "" || request.ConnectionEpoch <= 0 || request.ClientInstanceID == "" {
		return ErrSessionClosed
	}
	lease, err := s.connections.Get(ctx, request.ConnectionID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrSessionClosed
		}
		return err
	}
	if lease.ClientInstanceID != request.ClientInstanceID {
		return ErrSessionClosed
	}
	if lease.ConnectionEpoch != request.ConnectionEpoch {
		return ErrEpoch
	}
	if lease.ServerNodeID == "" || lease.ServerNodeID == s.localNodeID {
		if s.local == nil {
			return ErrClientConnectionNodeUnavailable
		}
		return s.local.CloseConnection(request.ConnectionID, request.ConnectionEpoch)
	}
	if s.nodes == nil {
		return ErrClientConnectionNodeUnavailable
	}
	node, err := s.nodes.Get(ctx, lease.ServerNodeID)
	if err != nil || !node.Enabled || node.DeletedAt != nil || node.Epoch <= 0 || node.Address == "" {
		return ErrClientConnectionNodeUnavailable
	}
	if s.dialRelayNode == nil {
		return ErrClientConnectionNodeUnavailable
	}
	client, err := s.dialRelayNode(ctx, node.Address, node.Epoch)
	if err != nil {
		return ErrClientConnectionNodeUnavailable
	}
	defer client.Close()
	err = client.CloseClientConnection(ctx, relay.CloseClientConnectionRequest{
		ConnectionID: request.ConnectionID, ConnectionEpoch: request.ConnectionEpoch,
		RequestedByNodeID: s.localNodeID,
	})
	switch status.Code(err) {
	case codes.OK:
		return nil
	case codes.FailedPrecondition:
		return ErrEpoch
	case codes.NotFound:
		return ErrSessionClosed
	default:
		return ErrClientConnectionNodeUnavailable
	}
}
