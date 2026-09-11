package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/auth"
	"github.com/tunnelmesh/tunnelmesh/internal/observability"
	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

// ClientConnectionCloseRequest identifies one exact physical Client WS lease.
type ClientConnectionCloseRequest struct {
	ClientInstanceID string
	ConnectionID     string
	ConnectionEpoch  int64
}

// ClientConnectionCloseService closes a Client connection locally or through
// the authenticated Server-node relay channel.
type ClientConnectionCloseService interface {
	Close(context.Context, ClientConnectionCloseRequest) error
}

// ClientView is the owner-scoped management projection of a logical Client.
// Metadata is decoded from the bounded payload and never participates in authorization.
type ClientView struct {
	ID                string                    `json:"id"`
	InstanceID        string                    `json:"instanceId"`
	OwnerUserID       string                    `json:"ownerUserId"`
	TokenIDs          []string                  `json:"tokenIds"`
	AgentIDs          []string                  `json:"agentIds"`
	Version           string                    `json:"version"`
	Commit            string                    `json:"commit"`
	Platform          string                    `json:"platform"`
	Hostname          string                    `json:"hostname"`
	ProcessStartAt    time.Time                 `json:"processStartAt"`
	Status            string                    `json:"status"`
	ActiveConnections int                       `json:"activeConnections"`
	ActiveStreams     int                       `json:"activeStreams"`
	ServerNodeIDs     []string                  `json:"serverNodeIds"`
	LastSeenAt        time.Time                 `json:"lastSeenAt"`
	Metadata          map[string]string         `json:"metadata"`
	Capabilities      []string                  `json:"capabilities"`
	Listeners         []protocol.ClientListener `json:"listeners"`
}

// ClientConnectionView is a durable physical WebSocket lease snapshot.
type ClientConnectionView struct {
	ConnectionID     string    `json:"connectionId"`
	ClientInstanceID string    `json:"clientInstanceId"`
	TokenID          string    `json:"tokenId"`
	OwnerUserID      string    `json:"ownerUserId"`
	ServerNodeID     string    `json:"serverNodeId"`
	ConnectionEpoch  int64     `json:"connectionEpoch"`
	ActiveStreams    int       `json:"activeStreams"`
	HealthScore      int64     `json:"healthScore"`
	AcquiredAt       time.Time `json:"acquiredAt"`
	LastHeartbeatAt  time.Time `json:"lastHeartbeatAt"`
	ExpiresAt        time.Time `json:"expiresAt"`
	Local            bool      `json:"local"`
}

// SetClusterClientConnections injects the cross-node Client close service.
func (a *API) SetClusterClientConnections(closer ClientConnectionCloseService) {
	if a == nil {
		return
	}
	a.clientCloser = closer
}

func (a *API) handleClients(w http.ResponseWriter, r *http.Request, p auth.Principal, parts []string) {
	if len(parts) == 0 {
		if r.Method != http.MethodGet {
			writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		a.listClients(w, r, p)
		return
	}
	clientInstanceID := parts[0]
	instance, err := a.clientInstances.Get(r.Context(), clientInstanceID)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	if !clientInstanceReadable(p, instance) {
		writeAPIError(w, http.StatusForbidden, "forbidden")
		return
	}
	if len(parts) == 1 {
		if r.Method != http.MethodGet {
			writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		connections, err := a.clientConnections.ListByInstance(r.Context(), clientInstanceID)
		if err != nil {
			writeStorageError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, newClientView(instance, connections))
		return
	}
	if len(parts) == 2 && parts[1] == "connections" {
		if r.Method != http.MethodGet {
			writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		connections, err := a.clientConnections.ListByInstance(r.Context(), clientInstanceID)
		if err != nil {
			writeStorageError(w, err)
			return
		}
		views := make([]ClientConnectionView, 0, len(connections))
		for _, connection := range connections {
			views = append(views, newClientConnectionView(connection, a.localNodeID))
		}
		writeJSON(w, http.StatusOK, map[string]any{"connections": views})
		return
	}
	if len(parts) == 3 && parts[1] == "connections" {
		if r.Method != http.MethodDelete {
			writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		a.closeClientConnection(w, r, p, instance, parts[2])
		return
	}
	writeAPIError(w, http.StatusNotFound, "not found")
}

func (a *API) listClients(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	filter := storage.ClientInstanceFilter{
		TokenID:      strings.TrimSpace(r.URL.Query().Get("tokenId")),
		ServerNodeID: strings.TrimSpace(r.URL.Query().Get("serverNodeId")),
		AgentID:      strings.TrimSpace(r.URL.Query().Get("agentId")),
		Keyword:      strings.TrimSpace(r.URL.Query().Get("keyword")),
	}
	if owner := strings.TrimSpace(r.URL.Query().Get("ownerUserId")); owner != "" {
		if !isAdmin(p) {
			writeAPIError(w, http.StatusForbidden, "admin role required")
			return
		}
		filter.OwnerUserID = owner
	} else if !isAdmin(p) {
		filter.OwnerUserID = p.UserID
	}
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	if status != "" {
		switch status {
		case "online", "offline", "stale", "metadata_unavailable":
			filter.Status = status
		default:
			writeAPIError(w, http.StatusBadRequest, "invalid client status")
			return
		}
	}
	page, err := a.clientInstances.List(r.Context(), filter, r.URL.Query().Get("cursor"), queryLimit(r))
	if err != nil {
		writeStorageError(w, err)
		return
	}
	ids := make([]string, 0, len(page.Items))
	for _, instance := range page.Items {
		ids = append(ids, instance.ID)
	}
	connections, err := a.clientConnections.ListByInstances(r.Context(), ids)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	connectionByInstance := make(map[string][]storage.ClientConnectionLease, len(page.Items))
	for _, connection := range connections {
		connectionByInstance[connection.ClientInstanceID] = append(connectionByInstance[connection.ClientInstanceID], connection)
	}
	items := make([]any, 0, len(page.Items))
	for _, instance := range page.Items {
		items = append(items, newClientView(instance, connectionByInstance[instance.ID]))
	}
	writeJSON(w, http.StatusOK, pageData(items, page.NextCursor, page.HasMore))
}

func (a *API) closeClientConnection(w http.ResponseWriter, r *http.Request, p auth.Principal, instance storage.ClientInstance, connectionID string) {
	epoch, err := strconv.ParseInt(r.URL.Query().Get("connectionEpoch"), 10, 64)
	if err != nil || epoch <= 0 {
		writeAPIError(w, http.StatusBadRequest, "connectionEpoch is required")
		return
	}
	request := ClientConnectionCloseRequest{
		ClientInstanceID: instance.ID, ConnectionID: connectionID, ConnectionEpoch: epoch,
	}
	lease, err := a.clientConnections.Get(r.Context(), connectionID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			a.writeClientConnectionCloseAudit(r.Context(), p, request, "already_closed", nil)
			writeJSON(w, http.StatusOK, clientConnectionClosedResponse(request))
			return
		}
		writeStorageError(w, err)
		return
	}
	if lease.ClientInstanceID != instance.ID {
		a.writeClientConnectionCloseAudit(r.Context(), p, request, "failed", ErrSessionClosed)
		writeAPIError(w, http.StatusNotFound, "client connection not found")
		return
	}
	if lease.ConnectionEpoch != epoch {
		a.writeClientConnectionCloseAudit(r.Context(), p, request, "failed", ErrEpoch)
		writeAPIError(w, http.StatusConflict, "connection epoch is stale")
		return
	}
	if a.clientCloser == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "cluster client connections unavailable")
		return
	}
	if err := a.clientCloser.Close(r.Context(), request); err != nil {
		a.writeClientConnectionCloseAudit(r.Context(), p, request, "failed", err)
		switch {
		case errors.Is(err, ErrEpoch):
			writeAPIError(w, http.StatusConflict, "connection epoch is stale")
		case errors.Is(err, ErrClientConnectionNodeUnavailable):
			writeAPIError(w, http.StatusServiceUnavailable, "owner server node unavailable")
		case errors.Is(err, ErrSessionClosed), errors.Is(err, sql.ErrNoRows):
			a.writeClientConnectionCloseAudit(r.Context(), p, request, "already_closed", nil)
			writeJSON(w, http.StatusOK, clientConnectionClosedResponse(request))
		default:
			writeAPIError(w, http.StatusInternalServerError, "client connection close failed")
		}
		return
	}
	a.writeClientConnectionCloseAudit(r.Context(), p, request, "closed", nil)
	writeJSON(w, http.StatusOK, clientConnectionClosedResponse(request))
}

func clientInstanceReadable(p auth.Principal, instance storage.ClientInstance) bool {
	return isAdmin(p) || instance.OwnerUserID == p.UserID
}

func newClientView(instance storage.ClientInstance, connections []storage.ClientConnectionLease) ClientView {
	var payload protocol.ClientMetadataPayload
	_ = json.Unmarshal([]byte(instance.Metadata), &payload)
	metadata := make(map[string]string, len(payload.Items))
	for _, item := range payload.Items {
		metadata[item.Name] = item.Value
	}
	now := time.Now().UTC()
	activeConnections := 0
	activeStreams := 0
	tokenIDs := make([]string, 0, len(connections))
	serverNodeIDs := make([]string, 0, len(connections))
	for _, connection := range connections {
		if connection.ExpiresAt.After(now) {
			activeConnections++
			activeStreams += int(connection.ActiveStreams)
			tokenIDs = appendUniqueString(tokenIDs, connection.TokenID)
			serverNodeIDs = appendUniqueString(serverNodeIDs, connection.ServerNodeID)
		}
	}
	sort.Strings(tokenIDs)
	sort.Strings(serverNodeIDs)
	status := "offline"
	if instance.Stale {
		status = "stale"
	} else if decodeStrings(instance.Capabilities) == nil {
		status = "metadata_unavailable"
	} else if activeConnections > 0 {
		status = "online"
	}
	return ClientView{
		ID: instance.ID, InstanceID: instance.InstanceID, OwnerUserID: instance.OwnerUserID,
		TokenIDs: tokenIDs, AgentIDs: payload.AgentIDs, Version: payload.Version, Commit: payload.Commit,
		Platform: payload.Platform, Hostname: payload.Hostname, ProcessStartAt: payload.ProcessStartAt,
		Status: status, ActiveConnections: activeConnections, ActiveStreams: activeStreams,
		ServerNodeIDs: serverNodeIDs, LastSeenAt: instance.LastSeenAt, Metadata: metadata,
		Capabilities: decodeStrings(instance.Capabilities), Listeners: payload.Listeners,
	}
}

func newClientConnectionView(connection storage.ClientConnectionLease, localNodeID string) ClientConnectionView {
	return ClientConnectionView{
		ConnectionID: connection.ConnectionID, ClientInstanceID: connection.ClientInstanceID,
		TokenID: connection.TokenID, OwnerUserID: connection.OwnerUserID, ServerNodeID: connection.ServerNodeID,
		ConnectionEpoch: connection.ConnectionEpoch, ActiveStreams: int(connection.ActiveStreams),
		HealthScore: connection.HealthScore, AcquiredAt: connection.AcquiredAt,
		LastHeartbeatAt: connection.UpdatedAt, ExpiresAt: connection.ExpiresAt,
		Local: connection.ServerNodeID == localNodeID,
	}
}

func clientConnectionClosedResponse(request ClientConnectionCloseRequest) map[string]any {
	return map[string]any{
		"clientInstanceId": request.ClientInstanceID, "connectionId": request.ConnectionID,
		"connectionEpoch": request.ConnectionEpoch, "closed": true,
	}
}

func (a *API) writeClientConnectionCloseAudit(ctx context.Context, p auth.Principal, request ClientConnectionCloseRequest, result string, failure error) {
	if a.service == nil {
		return
	}
	details := map[string]any{
		"clientInstanceId": request.ClientInstanceID, "connectionId": request.ConnectionID,
		"connectionEpoch": request.ConnectionEpoch, "result": result,
	}
	if failure != nil {
		details["errorClass"] = observability.NormalizeErrorClass(failure)
	}
	encoded, err := json.Marshal(details)
	if err != nil {
		encoded = []byte("{}")
	}
	_ = a.service.CreateAudit(ctx, storage.AuditLog{
		ActorUserID: p.UserID, Action: "client.connection.close", ResourceType: "client",
		ResourceID: request.ClientInstanceID, Details: string(encoded),
	})
}

func appendUniqueString(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}
