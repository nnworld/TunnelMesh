package server

import (
	"context"
	"encoding/json"
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

func clientViewInstance(metadata string, stale bool) storage.ClientInstance {
	now := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	expires := now.Add(time.Minute)
	return storage.ClientInstance{
		ID: "client-instance-1", OwnerUserID: "owner-1", InstanceID: "client-local-1",
		Metadata: metadata, Capabilities: `["client_metadata.v1"]`, ReportedAt: now,
		LastSeenAt: now, ExpiresAt: &expires, Stale: stale, UpdatedAt: now,
	}
}

func clientViewLease(expiresAt time.Time) storage.ClientConnectionLease {
	return storage.ClientConnectionLease{
		ConnectionID: "client_connection_1", ClientInstanceID: "client-instance-1",
		TokenID: "token-1", OwnerUserID: "owner-1", ServerNodeID: "server-1",
		ConnectionEpoch: 1, ActiveStreams: 2, AcquiredAt: expiresAt.Add(-time.Minute),
		ExpiresAt: expiresAt, UpdatedAt: expiresAt,
	}
}

// Reported metadata and presence are separate facts. Before this split a live
// Client with an empty capabilities array was labelled "metadata 未上报", and a
// long-dead row was labelled "metadata 已过期" instead of offline.
func TestNewClientViewSplitsPresenceFromMetadataState(t *testing.T) {
	reported := `{"instance_id":"client-local-1","hostname":"worker-1","version":"v1.2.3"}`
	unreported := `{}`
	live := time.Now().UTC().Add(time.Minute)
	expiredLease := time.Now().UTC().Add(-time.Minute)
	for _, tc := range []struct {
		name              string
		metadata          string
		stale             bool
		lease             *time.Time
		wantStatus        string
		wantMetadataState string
	}{
		{"connected and fresh", reported, false, &live, "online", "fresh"},
		{"connected but metadata lapsed", reported, true, &live, "online", "expired"},
		{"connected without hello", unreported, false, &live, "online", "unavailable"},
		{"disconnected ghost", unreported, true, nil, "offline", "unavailable"},
		{"disconnected after reporting", reported, false, &expiredLease, "offline", "fresh"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var leases []storage.ClientConnectionLease
			if tc.lease != nil {
				leases = append(leases, clientViewLease(*tc.lease))
			}
			view := newClientView(clientViewInstance(tc.metadata, tc.stale), leases)
			if view.Status != tc.wantStatus {
				t.Fatalf("status = %q, want %q", view.Status, tc.wantStatus)
			}
			if view.MetadataState != tc.wantMetadataState {
				t.Fatalf("metadataState = %q, want %q", view.MetadataState, tc.wantMetadataState)
			}
			if view.Capabilities == nil {
				t.Fatalf("capabilities must serialise as [], got nil")
			}
		})
	}
}

func TestNewClientViewTreatsJSONNullCapabilitiesAsEmpty(t *testing.T) {
	instance := clientViewInstance(`{"instance_id":"client-local-1"}`, false)
	instance.Capabilities = "null"
	view := newClientView(instance, nil)
	if view.Capabilities == nil || len(view.Capabilities) != 0 {
		t.Fatalf("capabilities = %#v, want empty non-nil slice", view.Capabilities)
	}
	if view.MetadataState != "fresh" {
		t.Fatalf("metadataState = %q, want fresh", view.MetadataState)
	}
}

func (test *clientAPITest) createClientRow(t *testing.T, id, ownerKey, instanceID, metadata string, stale bool, withLease bool) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	expires := now.Add(time.Minute)
	if stale {
		expires = now.Add(-time.Minute)
	}
	if _, err := test.db.ClientInstances().Upsert(ctx, storage.ClientInstance{
		ID: id, OwnerUserID: test.users[ownerKey].ID, InstanceID: instanceID, Metadata: metadata,
		Capabilities: "[]", ReportedAt: now, LastSeenAt: now, ExpiresAt: &expires, Stale: stale, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if withLease {
		test.createConnection(id, ownerKey, "client_connection_"+id, 1)
	}
}

func decodeClientList(t *testing.T, body string) (clientListResponse, []byte) {
	t.Helper()
	var payload clientListResponse
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("decode list body: %v\n%s", err, body)
	}
	return payload, []byte(body)
}

type clientListResponse struct {
	Data struct {
		Items []struct {
			ID            string `json:"id"`
			Status        string `json:"status"`
			MetadataState string `json:"metadataState"`
		} `json:"items"`
		Summary ClientListSummary `json:"summary"`
	} `json:"data"`
}

func TestClientAPIListReportsWholePopulationSummary(t *testing.T) {
	test := newClientAPITest(t)
	reported := `{"instance_id":"client-a","hostname":"a"}`
	test.createClientRow(t, "ci-a", "owner-1", "client-a", reported, false, true)
	test.createClientRow(t, "ci-b", "owner-1", "legacy-connection-b", `{}`, false, true)
	test.createClientRow(t, "ci-c", "owner-1", "legacy-connection-c", `{}`, true, false)
	test.createClientRow(t, "ci-d", "owner-2", "client-d", reported, true, false)

	response := test.requestAsUser(http.MethodGet, "/api/v1/clients", "owner-1")
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	payload, raw := decodeClientList(t, response.Body.String())
	want := ClientListSummary{Total: 3, Online: 2, ActiveConnections: 2, ActiveStreams: 6, MetadataUnavailable: 2}
	if payload.Data.Summary != want {
		t.Fatalf("summary = %+v, want %+v\n%s", payload.Data.Summary, want, raw)
	}
	// The owner-scoped summary must not count another owner's rows.
	if strings.Contains(string(raw), `"ci-d"`) {
		t.Fatalf("summary leaked another owner's instance: %s", raw)
	}
	for _, item := range payload.Data.Items {
		if item.Status != "online" && item.Status != "offline" {
			t.Fatalf("instance %s status = %q, want presence only", item.ID, item.Status)
		}
	}
}

func TestClientAPIFiltersMetadataStateAndAcceptsDeprecatedStatus(t *testing.T) {
	test := newClientAPITest(t)
	reported := `{"instance_id":"client-a","hostname":"a"}`
	test.createClientRow(t, "ci-a", "owner-1", "client-a", reported, false, true)
	test.createClientRow(t, "ci-b", "owner-1", "client-b", reported, true, true)
	test.createClientRow(t, "ci-c", "owner-1", "legacy-connection-c", `{}`, false, false)

	for _, tc := range []struct{ query, wantID string }{
		{"metadataState=expired", "ci-b"},
		{"metadataState=unavailable", "ci-c"},
		{"metadataState=fresh", "ci-a"},
		{"status=stale", "ci-b"},
		{"status=metadata_unavailable", "ci-c"},
		{"status=online&metadataState=expired", "ci-b"},
	} {
		response := test.requestAsUser(http.MethodGet, "/api/v1/clients?"+tc.query, "owner-1")
		if response.Code != http.StatusOK {
			t.Fatalf("query %s status = %d, body = %s", tc.query, response.Code, response.Body.String())
		}
		payload, raw := decodeClientList(t, response.Body.String())
		if len(payload.Data.Items) != 1 || payload.Data.Items[0].ID != tc.wantID {
			t.Fatalf("query %s matched %d rows (%s), want %s\n%s", tc.query, len(payload.Data.Items), idsOf(payload.Data.Items), tc.wantID, raw)
		}
	}
	invalid := test.requestAsUser(http.MethodGet, "/api/v1/clients?metadataState=nope", "owner-1")
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid metadataState status = %d, want 400", invalid.Code)
	}
}

func idsOf(items []struct {
	ID            string `json:"id"`
	Status        string `json:"status"`
	MetadataState string `json:"metadataState"`
}) string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item.ID)
	}
	return strings.Join(out, ",")
}
