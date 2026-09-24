package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/auth"
	"github.com/tunnelmesh/tunnelmesh/internal/registry"
	"github.com/tunnelmesh/tunnelmesh/internal/relay"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

type staticAgentConnectionRegistry struct {
	owners    []registry.NodeOwner
	listErr   error
	listCalls int
}

func (r *staticAgentConnectionRegistry) ListAgentConnections(context.Context, string) ([]registry.NodeOwner, error) {
	r.listCalls++
	return r.owners, r.listErr
}

type recordingAgentConnectionCloseService struct {
	requests []struct {
		AgentID         string
		ConnectionID    string
		ConnectionEpoch int64
	}
	err error
}

type fakeAgentConnectionRelayClient struct {
	requests []relay.CloseAgentConnectionRequest
	err      error
	closed   bool
}

func (c *fakeAgentConnectionRelayClient) CloseAgentConnection(_ context.Context, request relay.CloseAgentConnectionRequest) error {
	c.requests = append(c.requests, request)
	return c.err
}

func (c *fakeAgentConnectionRelayClient) Close() error {
	c.closed = true
	return nil
}

func (s *recordingAgentConnectionCloseService) Close(_ context.Context, agentID, connectionID string, epoch int64) error {
	s.requests = append(s.requests, struct {
		AgentID         string
		ConnectionID    string
		ConnectionEpoch int64
	}{AgentID: agentID, ConnectionID: connectionID, ConnectionEpoch: epoch})
	return s.err
}

type agentConnectionAPIFixture struct {
	api          *API
	adminToken   string
	ownerToken   string
	foreignToken string
	agentID      string
	registry     *staticAgentConnectionRegistry
	closeService *recordingAgentConnectionCloseService
	manager      *AgentSessionManager
	session      *AgentSession
}

func newAgentConnectionAPIFixture(t *testing.T) *agentConnectionAPIFixture {
	t.Helper()
	api, admin, owner := apiTestServer(t)
	authn := auth.NewAuthService(api.DB)
	foreign, err := authn.CreateUser(context.Background(), "foreign", "foreign-pass", "user")
	if err != nil {
		t.Fatal(err)
	}
	agentID := "agent-connections"
	if err := api.DB.Agents().Create(context.Background(), storage.Agent{
		ID: agentID, Name: "connections", OwnerUserID: owner.ID, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	manager := NewAgentSessionManager(AgentSessionConfig{ServerNodeID: "server-a"})
	session, err := manager.Register(context.Background(), AgentRegistration{
		AgentID: agentID, NodeID: "node-agent", Epoch: 7,
		InstanceID: "instance-local", ConnectionID: "conn-local", ConnectionEpoch: 22,
	}, newFakeTransport())
	if err != nil {
		t.Fatal(err)
	}
	session.mu.Lock()
	session.lastHeartbeat = time.Now().UTC().Add(-time.Second)
	session.mu.Unlock()
	api.SetAgentConnections(manager, NewAgentRelayTransport(manager, AgentRelayWindowConfig{}))
	connections := &staticAgentConnectionRegistry{}
	closeService := &recordingAgentConnectionCloseService{}
	api.SetClusterAgentConnections(connections, closeService, "server-a")
	adminLogin, err := authn.Login(context.Background(), admin.Username, "admin-pass")
	if err != nil {
		t.Fatal(err)
	}
	ownerLogin, err := authn.Login(context.Background(), owner.Username, "alice-pass")
	if err != nil {
		t.Fatal(err)
	}
	foreignLogin, err := authn.Login(context.Background(), foreign.Username, "foreign-pass")
	if err != nil {
		t.Fatal(err)
	}
	return &agentConnectionAPIFixture{
		api: api, adminToken: adminLogin.Token, ownerToken: ownerLogin.Token, foreignToken: foreignLogin.Token,
		agentID: agentID, registry: connections, closeService: closeService,
		manager: manager, session: session,
	}
}

func (f *agentConnectionAPIFixture) localOwner() registry.NodeOwner {
	return registry.NodeOwner{
		NodeID: "server-a", Address: "127.0.0.1:9443", AgentID: f.agentID,
		InstanceID: "instance-local", ConnectionID: "conn-local", ServerNodeID: "server-a",
		ConnectionEpoch: 21, ServerNodeEpoch: 7, ActiveStreams: 3, HealthScore: 100,
		ExpiresAt: time.Now().UTC().Add(time.Minute),
	}
}

func (f *agentConnectionAPIFixture) remoteOwner() registry.NodeOwner {
	return registry.NodeOwner{
		NodeID: "server-b", Address: "10.0.0.2:9443", AgentID: f.agentID,
		InstanceID: "instance-remote", ConnectionID: "conn-remote", ServerNodeID: "server-b",
		ConnectionEpoch: 11, ServerNodeEpoch: 8, ActiveStreams: 1, HealthScore: 90,
		ExpiresAt: time.Now().UTC().Add(time.Minute),
	}
}

type agentConnectionsAPIResponse struct {
	Data struct {
		Connections []AgentConnectionClusterView `json:"connections"`
	} `json:"data"`
}

func TestAgentConnectionsListMergesLocalAndRemoteLeases(t *testing.T) {
	fixture := newAgentConnectionAPIFixture(t)
	fixture.registry.owners = []registry.NodeOwner{fixture.remoteOwner(), fixture.localOwner()}

	response := apiJSON(t, fixture.api.Handler(), http.MethodGet, "/api/v1/agents/"+fixture.agentID+"/connections", fixture.ownerToken, "", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("list status = %d: %s", response.Code, response.Body.String())
	}
	var data agentConnectionsAPIResponse
	if err := json.Unmarshal(response.Body.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	var raw struct {
		Data struct {
			Connections []map[string]any `json:"connections"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if len(raw.Data.Connections) != 2 {
		t.Fatalf("raw connections = %#v", raw.Data.Connections)
	}
	if _, exists := raw.Data.Connections[1]["lastHeartbeatAt"]; exists {
		t.Fatalf("remote lastHeartbeatAt = %v, want omitted", raw.Data.Connections[1]["lastHeartbeatAt"])
	}
	if _, exists := raw.Data.Connections[0]["lastHeartbeatAt"]; !exists {
		t.Fatal("local lastHeartbeatAt is missing")
	}
	if len(data.Data.Connections) != 2 {
		t.Fatalf("connections = %#v", data.Data.Connections)
	}
	local := data.Data.Connections[0]
	if !local.Local || local.AgentID != fixture.agentID || local.InstanceID != "instance-local" || local.ConnectionID != "conn-local" ||
		local.ConnectionEpoch != 21 || local.ServerNodeID != "server-a" || local.ServerNodeEpoch != 7 ||
		local.ServerNodeAddress != "127.0.0.1:9443" || !local.Healthy || local.ActiveStreams != 0 ||
		local.HealthScore != 100 || local.LastHeartbeatAt.IsZero() || local.LeaseExpiresAt.IsZero() {
		t.Fatalf("local connection = %#v", local)
	}
	remote := data.Data.Connections[1]
	if remote.Local || remote.ServerNodeID != "server-b" || remote.ServerNodeEpoch != 8 || remote.ServerNodeAddress != "10.0.0.2:9443" ||
		remote.ActiveStreams != 1 || remote.HealthScore != 90 || remote.ConnectionEpoch != 11 || !remote.Healthy {
		t.Fatalf("remote connection = %#v", remote)
	}
}

func TestAgentConnectionsListEnforcesOwnership(t *testing.T) {
	fixture := newAgentConnectionAPIFixture(t)
	fixture.registry.owners = []registry.NodeOwner{fixture.localOwner()}

	for _, token := range []string{fixture.ownerToken, fixture.adminToken} {
		response := apiJSON(t, fixture.api.Handler(), http.MethodGet, "/api/v1/agents/"+fixture.agentID+"/connections", token, "", nil)
		if response.Code != http.StatusOK {
			t.Fatalf("authorized list status = %d: %s", response.Code, response.Body.String())
		}
	}
	fixture.registry.listCalls = 0
	response := apiJSON(t, fixture.api.Handler(), http.MethodGet, "/api/v1/agents/"+fixture.agentID+"/connections", fixture.foreignToken, "", nil)
	if response.Code != http.StatusForbidden {
		t.Fatalf("foreign list status = %d: %s", response.Code, response.Body.String())
	}
	if fixture.registry.listCalls != 0 {
		t.Fatalf("foreign list queried registry %d times", fixture.registry.listCalls)
	}
}

func TestAgentConnectionsCloseLocalExactConnection(t *testing.T) {
	fixture := newAgentConnectionAPIFixture(t)
	fixture.registry.owners = []registry.NodeOwner{fixture.localOwner()}

	response := apiJSON(t, fixture.api.Handler(), http.MethodDelete, "/api/v1/agents/"+fixture.agentID+"/connections/conn-local?connectionEpoch=21", fixture.ownerToken, "", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("close status = %d: %s", response.Code, response.Body.String())
	}
	if len(fixture.closeService.requests) != 1 {
		t.Fatalf("close requests = %#v", fixture.closeService.requests)
	}
	request := fixture.closeService.requests[0]
	if request.AgentID != fixture.agentID || request.ConnectionID != "conn-local" || request.ConnectionEpoch != 21 {
		t.Fatalf("close request = %#v", request)
	}
}

func TestAgentConnectionsCloseRemoteThroughControlService(t *testing.T) {
	fixture := newAgentConnectionAPIFixture(t)
	fixture.registry.owners = []registry.NodeOwner{fixture.remoteOwner()}

	response := apiJSON(t, fixture.api.Handler(), http.MethodDelete, "/api/v1/agents/"+fixture.agentID+"/connections/conn-remote?connectionEpoch=11", fixture.adminToken, "", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("close status = %d: %s", response.Code, response.Body.String())
	}
	if len(fixture.closeService.requests) != 1 || fixture.closeService.requests[0].ConnectionID != "conn-remote" || fixture.closeService.requests[0].ConnectionEpoch != 11 {
		t.Fatalf("close requests = %#v", fixture.closeService.requests)
	}
}

func TestAgentConnectionsCloseRejectsStaleEpoch(t *testing.T) {
	fixture := newAgentConnectionAPIFixture(t)
	fixture.registry.owners = []registry.NodeOwner{fixture.localOwner()}
	fixture.closeService.err = ErrEpoch

	response := apiJSON(t, fixture.api.Handler(), http.MethodDelete, "/api/v1/agents/"+fixture.agentID+"/connections/conn-local?connectionEpoch=20", fixture.ownerToken, "", nil)
	if response.Code != http.StatusConflict {
		t.Fatalf("stale close status = %d: %s", response.Code, response.Body.String())
	}
	if len(fixture.closeService.requests) != 0 {
		t.Fatalf("stale close reached service: %#v", fixture.closeService.requests)
	}
}

func TestAgentConnectionsCloseIsIdempotent(t *testing.T) {
	fixture := newAgentConnectionAPIFixture(t)
	fixture.registry.owners = nil

	response := apiJSON(t, fixture.api.Handler(), http.MethodDelete, "/api/v1/agents/"+fixture.agentID+"/connections/conn-local?connectionEpoch=21", fixture.ownerToken, "", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("idempotent close status = %d: %s", response.Code, response.Body.String())
	}
	if len(fixture.closeService.requests) != 0 {
		t.Fatalf("idempotent close reached service: %#v", fixture.closeService.requests)
	}
}

func TestAgentConnectionsCloseReturnsUnavailableForRemoteNode(t *testing.T) {
	fixture := newAgentConnectionAPIFixture(t)
	fixture.registry.owners = []registry.NodeOwner{fixture.remoteOwner()}
	fixture.closeService.err = ErrAgentConnectionNodeUnavailable

	response := apiJSON(t, fixture.api.Handler(), http.MethodDelete, "/api/v1/agents/"+fixture.agentID+"/connections/conn-remote?connectionEpoch=11", fixture.adminToken, "", nil)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("unavailable close status = %d: %s", response.Code, response.Body.String())
	}
}

func TestAgentConnectionsCloseWritesAudit(t *testing.T) {
	fixture := newAgentConnectionAPIFixture(t)
	fixture.registry.owners = []registry.NodeOwner{fixture.remoteOwner()}

	response := apiJSON(t, fixture.api.Handler(), http.MethodDelete, "/api/v1/agents/"+fixture.agentID+"/connections/conn-remote?connectionEpoch=11", fixture.adminToken, "", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("close status = %d: %s", response.Code, response.Body.String())
	}
	page, err := fixture.api.DB.Audits().List(context.Background(), storage.AuditFilter{}, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, audit := range page.Items {
		if audit.Action != "agent.connection.closed_by_admin" || audit.ResourceID != fixture.agentID {
			continue
		}
		found = true
		var details map[string]any
		if err := json.Unmarshal([]byte(audit.Details), &details); err != nil {
			t.Fatal(err)
		}
		if details["connectionId"] != "conn-remote" || details["connectionEpoch"].(float64) != 11 || details["serverNodeId"] != "server-b" || details["result"] != "closed" {
			t.Fatalf("audit details = %#v", details)
		}
	}
	if !found {
		t.Fatalf("close audit missing: %#v", page.Items)
	}

	fixture.closeService.err = errors.New("close failed")
	fixture.closeService.requests = nil
	failed := apiJSON(t, fixture.api.Handler(), http.MethodDelete, "/api/v1/agents/"+fixture.agentID+"/connections/conn-remote?connectionEpoch=11", fixture.adminToken, "", nil)
	if failed.Code != http.StatusInternalServerError {
		t.Fatalf("failed close status = %d: %s", failed.Code, failed.Body.String())
	}
	page, err = fixture.api.DB.Audits().List(context.Background(), storage.AuditFilter{}, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	failureFound := false
	for _, audit := range page.Items {
		if audit.Action == "agent.connection.close_failed" && audit.ResourceID == fixture.agentID {
			failureFound = true
		}
	}
	if !failureFound {
		t.Fatalf("failed close audit missing: %#v", page.Items)
	}
}

func TestClusterAgentConnectionCloseServiceRoutesLocalAndRemote(t *testing.T) {
	ctx := context.Background()
	manager := NewAgentSessionManager(AgentSessionConfig{})
	transport := newFakeTransport()
	session, err := manager.Register(ctx, AgentRegistration{
		AgentID: "agent-close-service", NodeID: "node-agent", Epoch: 7,
		InstanceID: "instance-local", ConnectionID: "conn-local", ConnectionEpoch: 22,
	}, transport)
	if err != nil {
		t.Fatal(err)
	}
	controller := NewAgentConnectionLeaseController(nil, manager, nil, "server-a", "", time.Minute)
	controller.owners[session] = registry.NodeOwner{
		AgentID: session.AgentID, ConnectionID: session.ConnectionID,
		ConnectionEpoch: 21, ServerNodeID: "server-a",
	}
	localRegistry := &staticAgentConnectionRegistry{owners: []registry.NodeOwner{{
		NodeID: "server-a", Address: "127.0.0.1:9443", AgentID: session.AgentID,
		InstanceID: "instance-local", ConnectionID: "conn-local", ServerNodeID: "server-a",
		ConnectionEpoch: 21, ServerNodeEpoch: 7,
	}}}
	service := NewClusterAgentConnectionCloseService(localRegistry, controller, "server-a", nil)
	if err := service.Close(ctx, session.AgentID, "conn-local", 21); err != nil {
		t.Fatalf("local close error = %v", err)
	}
	select {
	case <-transport.closed:
	default:
		t.Fatal("local close did not close the transport")
	}

	remoteOwner := registry.NodeOwner{
		NodeID: "server-b", Address: "10.0.0.2:9443", AgentID: "agent-close-service",
		InstanceID: "instance-remote", ConnectionID: "conn-remote", ServerNodeID: "server-b",
		ConnectionEpoch: 11, ServerNodeEpoch: 8,
	}
	remoteRegistry := &staticAgentConnectionRegistry{owners: []registry.NodeOwner{remoteOwner}}
	client := &fakeAgentConnectionRelayClient{}
	var dialedEndpoint string
	var dialedEpoch int64
	service = NewClusterAgentConnectionCloseService(remoteRegistry, controller, "server-a", func(_ context.Context, endpoint string, epoch int64) (AgentConnectionRelayClient, error) {
		dialedEndpoint, dialedEpoch = endpoint, epoch
		return client, nil
	})
	if err := service.Close(ctx, remoteOwner.AgentID, remoteOwner.ConnectionID, remoteOwner.ConnectionEpoch); err != nil {
		t.Fatalf("remote close error = %v", err)
	}
	if dialedEndpoint != "10.0.0.2:9443" || dialedEpoch != 8 || !client.closed {
		t.Fatalf("dial = %s/%d, closed = %v", dialedEndpoint, dialedEpoch, client.closed)
	}
	if len(client.requests) != 1 || client.requests[0].RequestedByNodeID != "server-a" ||
		client.requests[0].ConnectionID != "conn-remote" || client.requests[0].ConnectionEpoch != 11 {
		t.Fatalf("remote requests = %#v", client.requests)
	}
}

func TestClusterAgentConnectionCloseServiceMapsDialFailureAndStaleEpoch(t *testing.T) {
	ctx := context.Background()
	remoteOwner := registry.NodeOwner{
		NodeID: "server-b", Address: "10.0.0.2:9443", AgentID: "agent-close-service",
		ConnectionID: "conn-remote", ServerNodeID: "server-b", ConnectionEpoch: 11, ServerNodeEpoch: 8,
	}
	remoteRegistry := &staticAgentConnectionRegistry{owners: []registry.NodeOwner{remoteOwner}}
	service := NewClusterAgentConnectionCloseService(remoteRegistry, nil, "server-a", func(context.Context, string, int64) (AgentConnectionRelayClient, error) {
		return nil, errors.New("network unavailable")
	})
	if err := service.Close(ctx, remoteOwner.AgentID, remoteOwner.ConnectionID, remoteOwner.ConnectionEpoch); !errors.Is(err, ErrAgentConnectionNodeUnavailable) {
		t.Fatalf("dial failure error = %v, want %v", err, ErrAgentConnectionNodeUnavailable)
	}
	if err := service.Close(ctx, remoteOwner.AgentID, remoteOwner.ConnectionID, remoteOwner.ConnectionEpoch-1); !errors.Is(err, ErrEpoch) {
		t.Fatalf("stale error = %v, want %v", err, ErrEpoch)
	}
}
