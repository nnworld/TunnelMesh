package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/auth"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

func TestClientAPIListsOnlyOwnerScopedClients(t *testing.T) {
	test := newClientAPITest(t)
	test.createClient(t, "client-instance-1", "owner-1", "client-local-1")
	test.createClient(t, "client-instance-2", "owner-2", "client-local-2")
	response := test.requestAsUser(http.MethodGet, "/api/v1/clients", "owner-1")
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.Code)
	}
	if strings.Count(response.Body.String(), "client-instance-") != 1 {
		t.Fatalf("owner filter leaked data: %s", response.Body.String())
	}
}

func TestClientAPICloseRequiresEpoch(t *testing.T) {
	test := newClientAPITest(t)
	test.createClient(t, "client-instance-1", "owner-1", "client-local-1")
	response := test.requestAsUser(http.MethodDelete, "/api/v1/clients/client-instance-1/connections/client_connection_1", "owner-1")
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", response.Code)
	}
}

func TestClientAPICloseDelegatesEpochToClusterService(t *testing.T) {
	test := newClientAPITest(t)
	test.createClient(t, "client-instance-1", "owner-1", "client-local-1")
	test.createConnection("client-instance-1", "owner-1", "client_connection_1", 7)
	response := test.requestAsUser(http.MethodDelete, "/api/v1/clients/client-instance-1/connections/client_connection_1?connectionEpoch=7", "owner-1")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"closed":true`) {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if len(test.closeService.requests) != 1 {
		t.Fatalf("close requests = %#v", test.closeService.requests)
	}
	request := test.closeService.requests[0]
	if request.ClientInstanceID != "client-instance-1" || request.ConnectionID != "client_connection_1" || request.ConnectionEpoch != 7 {
		t.Fatalf("close request = %#v", request)
	}
}

func TestClientAPICloseValidatesLeaseBeforeDelegation(t *testing.T) {
	test := newClientAPITest(t)
	test.createClient(t, "client-instance-1", "owner-1", "client-local-1")
	test.createClient(t, "client-instance-2", "owner-1", "client-local-2")
	test.createConnection("client-instance-1", "owner-1", "client_connection_1", 7)

	wrongInstance := test.requestAsUser(http.MethodDelete, "/api/v1/clients/client-instance-2/connections/client_connection_1?connectionEpoch=7", "owner-1")
	if wrongInstance.Code != http.StatusNotFound {
		t.Fatalf("wrong instance status = %d, want 404", wrongInstance.Code)
	}
	stale := test.requestAsUser(http.MethodDelete, "/api/v1/clients/client-instance-1/connections/client_connection_1?connectionEpoch=6", "owner-1")
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale status = %d, want 409", stale.Code)
	}
	missing := test.requestAsUser(http.MethodDelete, "/api/v1/clients/client-instance-1/connections/client_connection_missing?connectionEpoch=7", "owner-1")
	if missing.Code != http.StatusOK || !strings.Contains(missing.Body.String(), `"closed":true`) {
		t.Fatalf("missing status = %d, body = %s", missing.Code, missing.Body.String())
	}
	if len(test.closeService.requests) != 0 {
		t.Fatalf("invalid close requests reached cluster service: %#v", test.closeService.requests)
	}
}

func TestClientAPIDetailAndConnectionsExposeObservability(t *testing.T) {
	test := newClientAPITest(t)
	test.createClient(t, "client-instance-1", "owner-1", "client-local-1")
	test.createConnection("client-instance-1", "owner-1", "client_connection_1", 7)

	detail := test.requestAsUser(http.MethodGet, "/api/v1/clients/client-instance-1", "owner-1")
	if detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), `"hostname":"worker-1"`) {
		t.Fatalf("detail status = %d, body = %s", detail.Code, detail.Body.String())
	}
	connections := test.requestAsUser(http.MethodGet, "/api/v1/clients/client-instance-1/connections", "owner-1")
	if connections.Code != http.StatusOK || !strings.Contains(connections.Body.String(), `"connectionId":"client_connection_1"`) {
		t.Fatalf("connections status = %d, body = %s", connections.Code, connections.Body.String())
	}
	if !strings.Contains(connections.Body.String(), `"activeStreams":3`) {
		t.Fatalf("connections did not expose active streams: %s", connections.Body.String())
	}
}

func TestClientAPIRejectsOtherOwner(t *testing.T) {
	test := newClientAPITest(t)
	test.createClient(t, "client-instance-1", "owner-1", "client-local-1")
	detail := test.requestAsUser(http.MethodGet, "/api/v1/clients/client-instance-1", "owner-2")
	if detail.Code != http.StatusForbidden {
		t.Fatalf("detail status = %d, want 403", detail.Code)
	}
	closeResponse := test.requestAsUser(http.MethodDelete, "/api/v1/clients/client-instance-1/connections/client_connection_1?connectionEpoch=7", "owner-2")
	if closeResponse.Code != http.StatusForbidden {
		t.Fatalf("close status = %d, want 403", closeResponse.Code)
	}
}

type clientAPITest struct {
	db           *storage.DB
	handler      http.Handler
	users        map[string]storage.User
	tokens       map[string]string
	closeService *recordingClientConnectionCloseService
}

func newClientAPITest(t *testing.T) *clientAPITest {
	t.Helper()
	ctx := context.Background()
	db, err := storage.OpenSQLite(ctx, "file:client-api-"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	authn := auth.NewAuthService(db)
	test := &clientAPITest{db: db, users: map[string]storage.User{}, tokens: map[string]string{}}
	for _, key := range []string{"owner-1", "owner-2", "admin"} {
		role := "user"
		if key == "admin" {
			role = "admin"
		}
		user, err := authn.CreateUser(ctx, key, key+"-pass", role)
		if err != nil {
			t.Fatal(err)
		}
		login, err := authn.Login(ctx, key, key+"-pass")
		if err != nil {
			t.Fatal(err)
		}
		test.users[key] = user
		test.tokens[key] = login.Token
	}
	api := NewAPI(db, authn)
	test.closeService = &recordingClientConnectionCloseService{}
	api.SetClusterClientConnections(test.closeService)
	test.handler = api.Handler()
	return test
}

func (test *clientAPITest) createConnection(clientInstanceID, ownerKey, connectionID string, epoch int64) {
	now := time.Now().UTC()
	lease := storage.ClientConnectionLease{
		ConnectionID: connectionID, ClientInstanceID: clientInstanceID, TokenID: "token-1",
		OwnerUserID: test.users[ownerKey].ID, ServerNodeID: "server-1", ConnectionEpoch: epoch,
		ActiveStreams: 3, HealthScore: 100, AcquiredAt: now, ExpiresAt: now.Add(time.Minute), UpdatedAt: now,
	}
	if _, err := test.db.ClientConnections().Register(context.Background(), lease); err != nil {
		panic(err)
	}
}

func (t *clientAPITest) requestAsUser(method, path, userKey string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, nil)
	request.Header.Set("Authorization", "Bearer "+t.tokens[userKey])
	response := httptest.NewRecorder()
	t.handler.ServeHTTP(response, request)
	return response
}

func (test *clientAPITest) createClient(t *testing.T, id, ownerKey, instanceID string) {
	t.Helper()
	now := time.Date(2026, 9, 11, 10, 0, 0, 0, time.UTC)
	expires := now.Add(time.Minute)
	instance := storage.ClientInstance{
		ID: id, OwnerUserID: test.users[ownerKey].ID, InstanceID: instanceID,
		Metadata:     `{"version":"v1.2.3","platform":"darwin/arm64","hostname":"worker-1","agent_ids":["agent-1"]}`,
		Capabilities: `["client_metadata.v1"]`,
		ReportedAt:   now, LastSeenAt: now, ExpiresAt: &expires, UpdatedAt: now,
	}
	if _, err := test.db.ClientInstances().Upsert(context.Background(), instance); err != nil {
		t.Fatal(err)
	}
}

type recordingClientConnectionCloseService struct {
	requests []ClientConnectionCloseRequest
}

func (s *recordingClientConnectionCloseService) Close(_ context.Context, request ClientConnectionCloseRequest) error {
	s.requests = append(s.requests, request)
	return nil
}
