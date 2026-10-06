package tray

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// testServerURL converts an httptest HTTP URL into the ws:// form a client
// configuration carries, so the tray's derivation is exercised rather than bypassed.
func testServerURL(t *testing.T, server *httptest.Server) string {
	t.Helper()
	return "ws://" + strings.TrimPrefix(server.URL, "http://") + "/ws/client"
}

func TestServerClientCheckHealth(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewServerClient(0)
	if err := client.CheckHealth(context.Background(), testServerURL(t, server)); err != nil {
		t.Fatalf("CheckHealth: %v", err)
	}
	if gotPath != "/health/ready" {
		t.Fatalf("health path = %q, want /health/ready", gotPath)
	}
}

func TestServerClientCheckHealthReportsStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	err := NewServerClient(0).CheckHealth(context.Background(), testServerURL(t, server))
	if err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("CheckHealth error = %v, want the status", err)
	}
}

func TestServerClientCheckHealthRejectsBadURL(t *testing.T) {
	if err := NewServerClient(0).CheckHealth(context.Background(), "https://server.example/ws/client"); err == nil {
		t.Fatal("a non-WebSocket server URL must be rejected")
	}
}

func TestServerClientListAgents(t *testing.T) {
	var gotAuth, gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 200,
			"msg":  "OK",
			"data": map[string]any{
				"items": []map[string]any{
					{"id": "agent-a", "name": "office", "online": true},
					{"id": "agent-b", "name": "home", "online": false},
				},
				"nextCursor": "",
				"hasMore":    false,
			},
		})
	}))
	defer server.Close()

	agents, err := NewServerClient(0).ListAgents(context.Background(), testServerURL(t, server), "client-secret")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/v1/client/agents" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotAuth != "Bearer client-secret" {
		t.Fatalf("authorization = %q, want a bearer header", gotAuth)
	}
	if len(agents) != 2 || agents[0].ID != "agent-a" || agents[0].Name != "office" || !agents[0].Online {
		t.Fatalf("agents = %+v", agents)
	}
	if agents[1].Online {
		t.Fatalf("agent-b online = true, want the server's false")
	}
}

// TestServerClientListAgentsFollowsCursor covers the pagination contract: stopping at
// the first page would silently hide Agents from the routing picker.
func TestServerClientListAgentsFollowsCursor(t *testing.T) {
	pages := map[string][]map[string]any{
		"": {
			{"id": "agent-a", "name": "a", "online": true},
		},
		"cursor-1": {
			{"id": "agent-b", "name": "b", "online": false},
		},
	}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		cursor := r.URL.Query().Get("cursor")
		items := pages[cursor]
		next, more := "", false
		if cursor == "" {
			next, more = "cursor-1", true
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 200, "msg": "OK",
			"data": map[string]any{"items": items, "nextCursor": next, "hasMore": more},
		})
	}))
	defer server.Close()

	agents, err := NewServerClient(0).ListAgents(context.Background(), testServerURL(t, server), "t")
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 2 || agents[0].ID != "agent-a" || agents[1].ID != "agent-b" {
		t.Fatalf("agents = %+v, want both pages", agents)
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want one per page", requests)
	}
}

// TestServerClientListAgentsStopsOnCursorLoop guards against a server that keeps
// returning the same cursor: an unbounded loop would hang the settings window.
func TestServerClientListAgentsStopsOnCursorLoop(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 200, "msg": "OK",
			"data": map[string]any{
				"items":      []map[string]any{{"id": "agent-a", "name": "a", "online": true}},
				"nextCursor": "same", "hasMore": true,
			},
		})
	}))
	defer server.Close()

	agents, err := NewServerClient(0).ListAgents(context.Background(), testServerURL(t, server), "t")
	if err != nil {
		t.Fatalf("a repeated cursor must stop the walk rather than fail: %v", err)
	}
	if len(agents) != 1 {
		t.Fatalf("agents = %+v, want the single distinct Agent", agents)
	}
}

func TestServerClientListAgentsClassifiesUnauthorized(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 401, "msg": "Unauthorized"})
	}))
	defer server.Close()

	_, err := NewServerClient(0).ListAgents(context.Background(), testServerURL(t, server), "bad-token")
	if !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("error = %v, want ErrInvalidToken so the interface can say the token is wrong", err)
	}
}

func TestServerClientListAgentsReportsUnavailableService(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 503, "msg": "client_token_validator_unavailable"})
	}))
	defer server.Close()

	_, err := NewServerClient(0).ListAgents(context.Background(), testServerURL(t, server), "t")
	if !errors.Is(err, ErrServerTooOld) {
		t.Fatalf("error = %v, want ErrServerTooOld", err)
	}
}

func TestServerClientListAgentsReportsNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	_, err := NewServerClient(0).ListAgents(context.Background(), testServerURL(t, server), "t")
	if !errors.Is(err, ErrServerTooOld) {
		t.Fatalf("error = %v, want ErrServerTooOld for a Server without the endpoint", err)
	}
}

func TestServerClientListAgentsRequiresToken(t *testing.T) {
	if _, err := NewServerClient(0).ListAgents(context.Background(), "ws://server.example/ws/client", "  "); !errors.Is(err, ErrMissingToken) {
		t.Fatalf("error = %v, want ErrMissingToken", err)
	}
}
