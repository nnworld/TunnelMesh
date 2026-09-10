package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/tunnelmesh/tunnelmesh/internal/auth"
	"github.com/tunnelmesh/tunnelmesh/internal/observability"
	"github.com/tunnelmesh/tunnelmesh/internal/registry"
	"github.com/tunnelmesh/tunnelmesh/internal/relay"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

// ErrAgentConnectionNodeUnavailable means the owning Server node could not be
// reached. The durable lease is intentionally left in place.
var ErrAgentConnectionNodeUnavailable = errors.New("agent connection owner node unavailable")

// AgentConnectionClusterView is the cross-node management view. ConnectionEpoch
// is the registry lease epoch used for cross-node fencing, not the process
// local protocol epoch.
type AgentConnectionClusterView struct {
	AgentID           string     `json:"agentId"`
	InstanceID        string     `json:"instanceId"`
	ConnectionID      string     `json:"connectionId"`
	ConnectionEpoch   int64      `json:"connectionEpoch"`
	ServerNodeID      string     `json:"serverNodeId"`
	ServerNodeEpoch   int64      `json:"serverNodeEpoch"`
	ServerNodeAddress string     `json:"serverNodeAddress"`
	Healthy           bool       `json:"healthy"`
	ActiveStreams     int        `json:"activeStreams"`
	HealthScore       int64      `json:"healthScore"`
	LastHeartbeatAt   *time.Time `json:"lastHeartbeatAt,omitempty"`
	LeaseExpiresAt    time.Time  `json:"leaseExpiresAt"`
	Local             bool       `json:"local"`
}

// AgentConnectionCloseRequest identifies one exact durable Agent connection.
type AgentConnectionCloseRequest struct {
	AgentID         string
	ConnectionID    string
	ConnectionEpoch int64
}

// AgentConnectionLister reads durable cluster ownership without exposing the
// full registry contract to HTTP handlers.
type AgentConnectionLister interface {
	ListAgentConnections(context.Context, string) ([]registry.NodeOwner, error)
}

// AgentConnectionCloseService closes an exact connection locally or through
// the authenticated relay control channel.
type AgentConnectionCloseService interface {
	Close(ctx context.Context, agentID, connectionID string, epoch int64) error
}

// AgentConnectionRelayClient is the authenticated inter-Server control client
// needed by the close service. Keeping it narrow makes the routing policy
// testable without opening arbitrary relay streams.
type AgentConnectionRelayClient interface {
	CloseAgentConnection(context.Context, relay.CloseAgentConnectionRequest) error
	Close() error
}

// ClusterAgentConnectionCloseService routes an exact lease close to the owning
// Server node. Local sessions are closed directly; remote sessions use the
// authenticated relay control RPC.
type ClusterAgentConnectionCloseService struct {
	connections   AgentConnectionLister
	local         *AgentConnectionLeaseController
	localNodeID   string
	dialRelayNode func(context.Context, string, int64) (AgentConnectionRelayClient, error)
}

func NewClusterAgentConnectionCloseService(connections AgentConnectionLister, local *AgentConnectionLeaseController, localNodeID string, dialRelayNode func(context.Context, string, int64) (AgentConnectionRelayClient, error)) *ClusterAgentConnectionCloseService {
	return &ClusterAgentConnectionCloseService{
		connections: connections, local: local, localNodeID: localNodeID,
		dialRelayNode: dialRelayNode,
	}
}

func (s *ClusterAgentConnectionCloseService) Close(ctx context.Context, agentID, connectionID string, epoch int64) error {
	if s == nil || s.connections == nil {
		return ErrAgentConnectionNodeUnavailable
	}
	owners, err := s.connections.ListAgentConnections(ctx, agentID)
	if err != nil {
		return err
	}
	var exact *registry.NodeOwner
	sameConnection := false
	for i := range owners {
		if owners[i].AgentID != agentID || owners[i].ConnectionID != connectionID {
			continue
		}
		sameConnection = true
		if owners[i].ConnectionEpoch == epoch {
			owner := owners[i]
			exact = &owner
			break
		}
	}
	if exact == nil {
		if sameConnection {
			return ErrEpoch
		}
		return nil
	}
	if exact.ServerNodeID == "" || exact.ServerNodeID == s.localNodeID {
		if s.local == nil {
			return errors.New("local agent connection controller unavailable")
		}
		return s.local.CloseConnection(ctx, agentID, connectionID, epoch)
	}
	if s.dialRelayNode == nil || exact.Address == "" || exact.ServerNodeEpoch <= 0 {
		return ErrAgentConnectionNodeUnavailable
	}
	client, err := s.dialRelayNode(ctx, exact.Address, exact.ServerNodeEpoch)
	if err != nil {
		return ErrAgentConnectionNodeUnavailable
	}
	defer client.Close()
	err = client.CloseAgentConnection(ctx, relay.CloseAgentConnectionRequest{
		AgentID: agentID, ConnectionID: connectionID, ConnectionEpoch: epoch,
		RequestedByNodeID: s.localNodeID,
	})
	switch status.Code(err) {
	case codes.OK:
		return nil
	case codes.FailedPrecondition:
		return ErrEpoch
	case codes.NotFound:
		return nil
	default:
		return ErrAgentConnectionNodeUnavailable
	}
}

// SetClusterAgentConnections injects cluster-wide connection state and the
// runtime close service. Embedders and tests may omit it; the API then reports
// the capability as unavailable instead of guessing from local state.
func (a *API) SetClusterAgentConnections(connections AgentConnectionLister, closer AgentConnectionCloseService, localNodeID string) {
	if a == nil {
		return
	}
	a.clusterConnections = connections
	a.connectionCloser = closer
	a.localNodeID = localNodeID
}

func (a *API) handleAgentConnections(w http.ResponseWriter, r *http.Request, p auth.Principal, agentID string, parts []string) {
	agent, err := a.service.GetAgent(r.Context(), agentID)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	if !isAdmin(p) && agent.OwnerUserID != p.UserID {
		writeAPIError(w, http.StatusForbidden, "forbidden")
		return
	}
	if len(parts) == 0 {
		if r.Method != http.MethodGet {
			writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		a.listAgentConnections(w, r, agentID)
		return
	}
	if len(parts) != 1 || r.Method != http.MethodDelete {
		writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	a.closeAgentConnection(w, r, p, agentID, parts[0])
}

func (a *API) listAgentConnections(w http.ResponseWriter, r *http.Request, agentID string) {
	if a.clusterConnections == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "cluster connections unavailable")
		return
	}
	owners, err := a.clusterConnections.ListAgentConnections(r.Context(), agentID)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	now := time.Now().UTC()
	views := make([]AgentConnectionClusterView, 0, len(owners))
	for _, owner := range owners {
		if owner.AgentID != agentID {
			continue
		}
		view := AgentConnectionClusterView{
			AgentID: owner.AgentID, InstanceID: owner.InstanceID, ConnectionID: owner.ConnectionID,
			ConnectionEpoch: owner.ConnectionEpoch, ServerNodeID: owner.ServerNodeID,
			ServerNodeEpoch: owner.ServerNodeEpoch, ServerNodeAddress: owner.Address,
			Healthy: owner.ExpiresAt.After(now), ActiveStreams: int(owner.ActiveStreams),
			HealthScore: owner.HealthScore, LeaseExpiresAt: owner.ExpiresAt,
			Local: owner.ServerNodeID == a.localNodeID,
		}
		if view.Local && a.agentSessions != nil {
			if session, ok := a.agentSessions.GetConnection(agentID, owner.ConnectionID); ok {
				view.InstanceID = session.InstanceID
				view.Healthy = session.Healthy()
				view.ActiveStreams = 0
				if a.localAgentRelay != nil {
					view.ActiveStreams = a.localAgentRelay.ActiveStreams(agentID, owner.ConnectionID)
				}
				view.HealthScore = 100
				lastHeartbeat := session.LastHeartbeat()
				view.LastHeartbeatAt = &lastHeartbeat
			}
		}
		views = append(views, view)
	}
	sort.SliceStable(views, func(i, j int) bool {
		if views[i].Local != views[j].Local {
			return views[i].Local
		}
		return views[i].ConnectionID < views[j].ConnectionID
	})
	writeJSON(w, http.StatusOK, map[string]any{"connections": views})
}

func (a *API) closeAgentConnection(w http.ResponseWriter, r *http.Request, p auth.Principal, agentID, connectionID string) {
	epoch, err := strconv.ParseInt(r.URL.Query().Get("connectionEpoch"), 10, 64)
	if err != nil || epoch <= 0 {
		writeAPIError(w, http.StatusBadRequest, "connectionEpoch is required")
		return
	}
	if a.clusterConnections == nil || a.connectionCloser == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "cluster connections unavailable")
		return
	}
	owners, err := a.clusterConnections.ListAgentConnections(r.Context(), agentID)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	var exact *registry.NodeOwner
	sameConnection := false
	for i := range owners {
		if owners[i].AgentID != agentID || owners[i].ConnectionID != connectionID {
			continue
		}
		sameConnection = true
		if owners[i].ConnectionEpoch == epoch {
			owner := owners[i]
			exact = &owner
			break
		}
	}
	if exact == nil {
		if sameConnection {
			a.writeAgentConnectionCloseAudit(r.Context(), p, agentID, registry.NodeOwner{ConnectionID: connectionID, ConnectionEpoch: epoch}, "stale_epoch", ErrEpoch)
			writeAPIError(w, http.StatusConflict, "connection epoch is stale")
			return
		}
		a.writeAgentConnectionCloseAudit(r.Context(), p, agentID, registry.NodeOwner{ConnectionID: connectionID, ConnectionEpoch: epoch}, "already_closed", nil)
		writeJSON(w, http.StatusOK, map[string]any{"agentId": agentID, "connectionId": connectionID, "connectionEpoch": epoch, "closed": true})
		return
	}
	if err := a.connectionCloser.Close(r.Context(), agentID, connectionID, epoch); err != nil {
		a.writeAgentConnectionCloseAudit(r.Context(), p, agentID, *exact, "failed", err)
		switch {
		case errors.Is(err, ErrEpoch):
			writeAPIError(w, http.StatusConflict, "connection epoch is stale")
		case errors.Is(err, ErrAgentConnectionNodeUnavailable):
			writeAPIError(w, http.StatusServiceUnavailable, "owner server node unavailable")
		default:
			writeAPIError(w, http.StatusInternalServerError, "connection close failed")
		}
		return
	}
	a.writeAgentConnectionCloseAudit(r.Context(), p, agentID, *exact, "closed", nil)
	writeJSON(w, http.StatusOK, map[string]any{"agentId": agentID, "connectionId": connectionID, "connectionEpoch": epoch, "closed": true})
}

// writeAgentConnectionCloseAudit records only non-secret connection identity
// and outcome; bearer tokens, target addresses, and stream payloads are never
// included.
func (a *API) writeAgentConnectionCloseAudit(ctx context.Context, p auth.Principal, agentID string, owner registry.NodeOwner, result string, failure error) {
	if a.service == nil {
		return
	}
	action := "agent.connection.closed_by_admin"
	if result != "closed" && result != "already_closed" {
		action = "agent.connection.close_failed"
	}
	details := map[string]any{
		"agentId": agentID, "instanceId": owner.InstanceID, "connectionId": owner.ConnectionID,
		"connectionEpoch": owner.ConnectionEpoch, "serverNodeId": owner.ServerNodeID,
		"serverNodeEpoch": owner.ServerNodeEpoch, "activeStreams": owner.ActiveStreams,
		"result": result,
	}
	if failure != nil {
		details["errorClass"] = observability.NormalizeErrorClass(failure)
	}
	encoded, err := json.Marshal(details)
	if err != nil {
		encoded = []byte("{}")
	}
	_ = a.service.CreateAudit(ctx, storage.AuditLog{
		ActorUserID: p.UserID, Action: action, ResourceType: "agent",
		ResourceID: agentID, Details: string(encoded),
	})
}
