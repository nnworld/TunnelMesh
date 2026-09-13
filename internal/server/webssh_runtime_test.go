package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/websocket"

	"github.com/tunnelmesh/tunnelmesh/internal/config"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

func TestWebHandlerRoutesWebSSHBeforeSPAFallback(t *testing.T) {
	called := false
	webSSH := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusTeapot)
	})
	handler := NewWebHandlerWithManagedRoutes(http.NotFoundHandler(), nil, nil, nil, webSSH, nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/ws/webssh/session-id", nil))
	if response.Code != http.StatusTeapot || !called {
		t.Fatalf("webssh route status=%d called=%v", response.Code, called)
	}
}

func TestWebSSHServeHTTPRejectsOriginBeforeUpgrade(t *testing.T) {
	upgrade := &recordingWebSSHUpgrader{}
	broker := NewWebSSHBroker(WebSSHBrokerDeps{
		Sessions: nil, Opener: nil, Upgrade: upgrade,
		Security: config.SecurityConfig{AllowedOrigins: []string{"https://admin.example.com"}},
	})
	request := httptest.NewRequest(http.MethodGet, "/ws/webssh/session-id?ticket=secret", nil)
	request.Header.Set("Origin", "https://evil.example.com")
	response := httptest.NewRecorder()
	broker.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("origin rejection status=%d", response.Code)
	}
	if upgrade.called {
		t.Fatal("websocket was upgraded before origin validation")
	}
}

func TestXNetWebSSHUpgraderValidatesOriginAndStreamsBinary(t *testing.T) {
	security := config.SecurityConfig{AllowedOrigins: []string{"https://admin.example.com"}}
	upgrader := newXNetWebSSHUpgrader(security, 64<<10)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := upgrader.Upgrade(w, r)
		if err != nil {
			return
		}
		defer ws.Close()
		typ, data, err := ws.ReadMessage()
		if err != nil {
			return
		}
		if err := ws.WriteMessage(2, data); err != nil {
			return
		}
		if typ != 2 {
			t.Fatalf("message type=%d", typ)
		}
	}))
	defer server.Close()

	wsConfig, err := websocket.NewConfig(strings.Replace(server.URL, "http://", "ws://", 1), "https://admin.example.com")
	if err != nil {
		t.Fatal(err)
	}
	connection, err := websocket.DialConfig(wsConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if err := websocket.Message.Send(connection, []byte("binary")); err != nil {
		t.Fatal(err)
	}
	var received []byte
	if err := websocket.Message.Receive(connection, &received); err != nil {
		t.Fatal(err)
	}
	if string(received) != "binary" {
		t.Fatalf("received=%q", received)
	}

	rejectedConfig, err := websocket.NewConfig(strings.Replace(server.URL, "http://", "ws://", 1), "https://evil.example.com")
	if err != nil {
		t.Fatal(err)
	}
	rejected, err := websocket.DialConfig(rejectedConfig)
	if err == nil {
		_ = rejected.Close()
		t.Fatal("rejected origin completed websocket handshake")
	}
}

type recordingWebSSHUpgrader struct{ called bool }

func (u *recordingWebSSHUpgrader) Upgrade(http.ResponseWriter, *http.Request) (WSConn, error) {
	u.called = true
	return nil, errors.New("unexpected upgrade")
}

type fakeWebSSHConnectionCloser struct {
	err error
}

func (c fakeWebSSHConnectionCloser) Close(context.Context, string) error { return c.err }

func TestWebSSHAPIClusterClosePreservesDurableStateOnUnavailableOwner(t *testing.T) {
	api, token, _ := remoteServerAPITest(t)
	registerWebSSHTestLease(t, api)
	api.SetWebSSHConnectionCloser(fakeWebSSHConnectionCloser{err: ErrWebSSHConnectionNodeUnavailable})
	handler := api.Handler()
	created := createWebSSHSessionThroughAPI(t, handler, token)
	response := apiJSON(t, handler, http.MethodDelete, "/api/v1/ssh-sessions/"+created, token, "", nil)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("unavailable owner status=%d body=%s", response.Code, response.Body.String())
	}
	session, err := api.DB.WebSSHSessions().Get(context.Background(), created)
	if err != nil || session.Status != storage.WebSSHSessionPending {
		t.Fatalf("durable session=%+v err=%v", session, err)
	}
}

func TestWebSSHAPIClusterCloseClosesAfterProcessClose(t *testing.T) {
	api, token, _ := remoteServerAPITest(t)
	registerWebSSHTestLease(t, api)
	api.SetWebSSHConnectionCloser(fakeWebSSHConnectionCloser{})
	handler := api.Handler()
	created := createWebSSHSessionThroughAPI(t, handler, token)
	response := apiJSON(t, handler, http.MethodDelete, "/api/v1/ssh-sessions/"+created, token, "", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("close status=%d body=%s", response.Code, response.Body.String())
	}
	session, err := api.DB.WebSSHSessions().Get(context.Background(), created)
	if err != nil || session.Status != storage.WebSSHSessionClosed {
		t.Fatalf("durable session=%+v err=%v", session, err)
	}
}

func TestServerRuntimeWiresWebSSHWithRealNodeIdentity(t *testing.T) {
	ctx := context.Background()
	db, err := storage.OpenSQLite(ctx, "file:runtime-webssh-wiring?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	security := config.SecurityConfig{AllowedOrigins: []string{"https://admin.example.com"}}
	runtime, err := NewServerRuntime(db, AgentSessionConfig{}, RuntimeConfig{
		NodeID: "server-webssh", Security: security,
		WebSSH: config.WebSSHConfig{Enabled: true, TicketTTL: time.Second, SessionTTL: time.Hour, MaxActiveSessionsUser: 2, OpenTimeout: time.Second, IdleTimeout: time.Minute, MaxMessageBytes: 4096},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	if runtime.API.localNodeID != "server-webssh" {
		t.Fatalf("local node id=%q", runtime.API.localNodeID)
	}
	if runtime.WebSSHBroker.deps.Upgrade == nil || runtime.WebSSHBroker.deps.Security.AllowedOrigins[0] != "https://admin.example.com" {
		t.Fatalf("broker deps=%+v", runtime.WebSSHBroker.deps)
	}
	request := httptest.NewRequest(http.MethodGet, "/ws/webssh/session", nil)
	request.Header.Set("Origin", "https://evil.example.com")
	response := httptest.NewRecorder()
	runtime.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("origin status=%d", response.Code)
	}

	disabled, err := NewServerRuntime(db, AgentSessionConfig{}, RuntimeConfig{NodeID: "server-disabled", WebSSH: config.WebSSHConfig{Enabled: false}})
	if err != nil {
		t.Fatal(err)
	}
	defer disabled.Close()
	allowed := httptest.NewRequest(http.MethodGet, "/ws/webssh/session", nil)
	allowed.Header.Set("Origin", "https://admin.example.com")
	disabledResponse := httptest.NewRecorder()
	disabled.Handler().ServeHTTP(disabledResponse, allowed)
	if disabledResponse.Code != http.StatusNotFound {
		t.Fatalf("disabled status=%d", disabledResponse.Code)
	}
}

func createWebSSHSessionThroughAPI(t *testing.T, handler http.Handler, token string) string {
	t.Helper()
	response := apiJSON(t, handler, http.MethodPost, "/api/v1/remote-servers/server-a/ssh-sessions", token, "webssh-close-key", map[string]any{"username": "deploy"})
	if response.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", response.Code, response.Body.String())
	}
	var payload struct {
		Data struct {
			SessionID string `json:"sessionId"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.SessionID == "" {
		t.Fatalf("empty session id: %s", response.Body.String())
	}
	return payload.Data.SessionID
}

func registerWebSSHTestLease(t *testing.T, api *API) {
	t.Helper()
	lease := storage.AgentLease{
		AgentID: "agent-a", ConnectionID: "conn-webssh-close", NodeID: "agent-node-a",
		ServerNodeID: "node-a", InstanceID: "instance", ConnectionEpoch: 1, TTL: time.Minute,
	}
	if _, err := api.DB.Leases().RegisterConnection(context.Background(), lease); err != nil {
		t.Fatal(err)
	}
}
