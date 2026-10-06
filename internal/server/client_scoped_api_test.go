package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/auth"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

// clientScopedFixture wires the same auth.CredentialService the client WebSocket
// handshake uses, because the client-scoped endpoints must trust exactly the
// credential the tunnel data plane already trusts. Anything else would let a
// token list Agents it is not allowed to open streams through.
type clientScopedFixture struct {
	api          *API
	owner        storage.User
	ownerToken   string
	scopedToken  string
	otherToken   string
	agentToken   string
	consoleToken string
}

func newClientScopedFixture(t *testing.T) *clientScopedFixture {
	t.Helper()
	ctx := context.Background()
	api, _, owner := apiTestServer(t)
	credentials := auth.NewCredentialService(api.DB)
	t.Cleanup(func() { _ = credentials.Close() })
	api.SetClientTokenValidator(credentials)

	service := auth.NewAuthService(api.DB)
	other, err := service.CreateUser(ctx, "client-scoped.other", "other-password-12", "user")
	if err != nil {
		t.Fatal(err)
	}
	for _, agent := range []storage.Agent{
		{ID: "csa-agent-online", Name: "online", OwnerUserID: owner.ID, Enabled: true},
		{ID: "csa-agent-offline", Name: "offline", OwnerUserID: owner.ID, Enabled: true},
		{ID: "csa-agent-disabled", Name: "disabled", OwnerUserID: owner.ID, Enabled: false},
		{ID: "csa-agent-foreign", Name: "foreign", OwnerUserID: other.ID, Enabled: true},
	} {
		if err := api.DB.Agents().Create(ctx, agent); err != nil {
			t.Fatal(err)
		}
	}
	// Only the "online" Agent holds a lease, which is the same fact the dashboard
	// and GET /api/v1/agents use for connectivity.
	if _, err := api.DB.Leases().Acquire(ctx, storage.AgentLease{AgentID: "csa-agent-online", NodeID: "csa-node", TTL: time.Hour}); err != nil {
		t.Fatal(err)
	}

	create := func(in auth.CreateTokenInput) string {
		t.Helper()
		created, err := credentials.Create(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		return created.Secret
	}
	client := func(ownerID string, scope auth.TokenScope) string {
		return create(auth.CreateTokenInput{Type: storage.TokenTypeClient, OwnerUserID: ownerID, Scope: scope})
	}
	return &clientScopedFixture{
		api:         api,
		owner:       owner,
		ownerToken:  client(owner.ID, auth.TokenScope{}),
		scopedToken: client(owner.ID, auth.TokenScope{AgentIDs: []string{"csa-agent-online"}}),
		otherToken:  client(other.ID, auth.TokenScope{}),
		// An Agent token authenticates the Agent data plane, so it carries an Agent
		// binding. It must still be refused here: a tunnel credential is not a client
		// credential even when both live in service_tokens.
		agentToken: create(auth.CreateTokenInput{
			Type: storage.TokenTypeAgent, OwnerUserID: owner.ID, AgentID: "csa-agent-online",
		}),
		consoleToken: apiToken(t, api, owner.Username, "alice-pass"),
	}
}

// clientAgentIDs decodes the envelope and returns the Agent IDs in list order.
func clientAgentIDs(t *testing.T, body string) []string {
	t.Helper()
	var envelope struct {
		Code int `json:"code"`
		Data struct {
			Items []struct {
				ID     string `json:"id"`
				Name   string `json:"name"`
				Online bool   `json:"online"`
			} `json:"items"`
			NextCursor string `json:"nextCursor"`
			HasMore    bool   `json:"hasMore"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &envelope); err != nil {
		t.Fatalf("decode response %q: %v", body, err)
	}
	if envelope.Code != http.StatusOK {
		t.Fatalf("envelope code = %d, body %s", envelope.Code, body)
	}
	ids := make([]string, 0, len(envelope.Data.Items))
	for _, item := range envelope.Data.Items {
		ids = append(ids, item.ID)
	}
	return ids
}

func TestClientScopedAgentsListsOnlyOwnerEnabledAgents(t *testing.T) {
	fixture := newClientScopedFixture(t)
	response := apiJSON(t, fixture.api.Handler(), http.MethodGet, "/api/v1/client/agents", fixture.ownerToken, "", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", response.Code, response.Body.String())
	}
	got := clientAgentIDs(t, response.Body.String())
	// The disabled Agent cannot serve a stream and the foreign Agent belongs to
	// another owner, so neither may appear: the tray offers this list as the set
	// of Agents a tunnel can actually be pointed at.
	want := map[string]bool{"csa-agent-online": true, "csa-agent-offline": true}
	if len(got) != len(want) {
		t.Fatalf("agents = %v, want %v", got, want)
	}
	for _, id := range got {
		if !want[id] {
			t.Fatalf("unexpected agent %q in %v", id, got)
		}
	}
	if !containsSubstring(response.Body.String(), `"online":true`) || !containsSubstring(response.Body.String(), `"online":false`) {
		t.Fatalf("online flag not differentiated: %s", response.Body.String())
	}
}

func TestClientScopedAgentsHonoursTokenScope(t *testing.T) {
	fixture := newClientScopedFixture(t)
	response := apiJSON(t, fixture.api.Handler(), http.MethodGet, "/api/v1/client/agents", fixture.scopedToken, "", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", response.Code, response.Body.String())
	}
	got := clientAgentIDs(t, response.Body.String())
	if len(got) != 1 || got[0] != "csa-agent-online" {
		t.Fatalf("scoped agents = %v, want [csa-agent-online]", got)
	}
}

func TestClientScopedAgentsIsolatesOwners(t *testing.T) {
	fixture := newClientScopedFixture(t)
	response := apiJSON(t, fixture.api.Handler(), http.MethodGet, "/api/v1/client/agents", fixture.otherToken, "", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	got := clientAgentIDs(t, response.Body.String())
	if len(got) != 1 || got[0] != "csa-agent-foreign" {
		t.Fatalf("other owner agents = %v, want [csa-agent-foreign]", got)
	}
}

// TestClientScopedAgentsRejectsNonClientCredentials pins the authorization
// boundary: an Agent token and a console login token both authenticate elsewhere
// in the product, but neither may enumerate Agents through the client surface.
func TestClientScopedAgentsRejectsNonClientCredentials(t *testing.T) {
	fixture := newClientScopedFixture(t)
	handler := fixture.api.Handler()
	for name, token := range map[string]string{
		"missing":      "",
		"agentToken":   fixture.agentToken,
		"consoleToken": fixture.consoleToken,
		"garbage":      "not-a-real-token",
	} {
		response := apiJSON(t, handler, http.MethodGet, "/api/v1/client/agents", token, "", nil)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("%s: status = %d, want 401, body %s", name, response.Code, response.Body.String())
		}
	}
}

func TestClientScopedAgentsPaginates(t *testing.T) {
	fixture := newClientScopedFixture(t)
	handler := fixture.api.Handler()
	first := apiJSON(t, handler, http.MethodGet, "/api/v1/client/agents?limit=1", fixture.ownerToken, "", nil)
	if first.Code != http.StatusOK {
		t.Fatalf("first page status = %d", first.Code)
	}
	var page struct {
		Data struct {
			Items      []map[string]any `json:"items"`
			NextCursor string           `json:"nextCursor"`
			HasMore    bool             `json:"hasMore"`
		} `json:"data"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Data.Items) != 1 || !page.Data.HasMore || page.Data.NextCursor == "" {
		t.Fatalf("first page = %+v", page.Data)
	}
	second := apiJSON(t, handler, http.MethodGet, fmt.Sprintf("/api/v1/client/agents?limit=1&cursor=%s", page.Data.NextCursor), fixture.ownerToken, "", nil)
	if second.Code != http.StatusOK {
		t.Fatalf("second page status = %d", second.Code)
	}
	got := clientAgentIDs(t, second.Body.String())
	if len(got) != 1 || got[0] == page.Data.Items[0]["id"] {
		t.Fatalf("second page = %v, want the other agent", got)
	}
}

func TestClientScopedAgentsMethodAndPath(t *testing.T) {
	fixture := newClientScopedFixture(t)
	handler := fixture.api.Handler()
	if response := apiJSON(t, handler, http.MethodPost, "/api/v1/client/agents", fixture.ownerToken, "", nil); response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d, want 405", response.Code)
	}
	if response := apiJSON(t, handler, http.MethodGet, "/api/v1/client/unknown", fixture.ownerToken, "", nil); response.Code != http.StatusNotFound {
		t.Fatalf("unknown subpath status = %d, want 404", response.Code)
	}
	// The admin-facing plural /clients surface must keep its own authentication:
	// a client service token is not a console session.
	if response := apiJSON(t, handler, http.MethodGet, "/api/v1/clients", fixture.ownerToken, "", nil); response.Code != http.StatusUnauthorized {
		t.Fatalf("admin /clients with client token status = %d, want 401", response.Code)
	}
}

func TestClientScopedAgentsUnavailableWithoutValidator(t *testing.T) {
	api, _, _ := apiTestServer(t)
	response := apiJSON(t, api.Handler(), http.MethodGet, "/api/v1/client/agents", "any-token", "", nil)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", response.Code)
	}
}

func containsSubstring(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}
