package server

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/tunnelmesh/tunnelmesh/internal/relay"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

// ErrWebSSHConnectionNodeUnavailable means the Server process owning a live
// WebSSH connection could not be reached. The durable row is intentionally
// left unchanged for the sweeper to converge later.
var ErrWebSSHConnectionNodeUnavailable = errors.New("webssh connection owner node unavailable")

// WebSSHConnectionCloseService closes a process-local or remote WebSSH
// connection after its durable session state has already been closed.
type WebSSHConnectionCloseService interface {
	Close(ctx context.Context, sessionID string) error
}

// WebSSHConnectionRelayClient is the narrow authenticated inter-Server control
// client required by the cluster close service.
type WebSSHConnectionRelayClient interface {
	CloseWebSSHConnection(context.Context, relay.CloseWebSSHConnectionRequest) error
	Close() error
}

// ClusterWebSSHConnectionCloseService routes close requests to the Server node
// that owns the browser WebSocket process state.
type ClusterWebSSHConnectionCloseService struct {
	sessions      storage.WebSSHSessionRepository
	nodes         storage.NodeRepository
	local         *WebSSHBroker
	localNodeID   string
	dialRelayNode func(context.Context, string, int64) (WebSSHConnectionRelayClient, error)
}

func NewClusterWebSSHConnectionCloseService(
	sessions storage.WebSSHSessionRepository,
	nodes storage.NodeRepository,
	local *WebSSHBroker,
	localNodeID string,
	dialRelayNode func(context.Context, string, int64) (WebSSHConnectionRelayClient, error),
) *ClusterWebSSHConnectionCloseService {
	return &ClusterWebSSHConnectionCloseService{
		sessions: sessions, nodes: nodes, local: local, localNodeID: strings.TrimSpace(localNodeID),
		dialRelayNode: dialRelayNode,
	}
}

func (s *ClusterWebSSHConnectionCloseService) Close(ctx context.Context, sessionID string) error {
	if s == nil || s.sessions == nil {
		return ErrWebSSHConnectionNodeUnavailable
	}
	session, err := s.sessions.Get(ctx, sessionID)
	if err != nil {
		return err
	}
	if session.OwnerNodeID == "" || session.OwnerNodeID == s.localNodeID {
		if s.local == nil {
			return errors.New("local webssh broker unavailable")
		}
		if err := s.local.CloseLocal(sessionID); err != nil && !errors.Is(err, ErrWebSSHBrokerClosed) {
			return err
		}
		return nil
	}
	if s.nodes == nil || s.dialRelayNode == nil {
		return ErrWebSSHConnectionNodeUnavailable
	}
	node, err := s.nodes.Get(ctx, session.OwnerNodeID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrWebSSHConnectionNodeUnavailable
		}
		return err
	}
	if node.DeletedAt != nil || !node.Enabled || node.Address == "" || node.Epoch <= 0 {
		return ErrWebSSHConnectionNodeUnavailable
	}
	client, err := s.dialRelayNode(ctx, node.Address, node.Epoch)
	if err != nil {
		return ErrWebSSHConnectionNodeUnavailable
	}
	defer client.Close()
	err = client.CloseWebSSHConnection(ctx, relay.CloseWebSSHConnectionRequest{
		SessionID: sessionID, RequestedByNodeID: s.localNodeID,
	})
	switch status.Code(err) {
	case codes.OK:
		return nil
	case codes.NotFound:
		// The process may have restarted; durable closed state remains true.
		return nil
	default:
		return ErrWebSSHConnectionNodeUnavailable
	}
}
