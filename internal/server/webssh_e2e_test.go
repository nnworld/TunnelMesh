package server

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/websocket"

	"github.com/tunnelmesh/tunnelmesh/internal/auth"
	"github.com/tunnelmesh/tunnelmesh/internal/config"
	"github.com/tunnelmesh/tunnelmesh/internal/relay"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

type browserBrokerE2EOpener struct {
	target  string
	request relay.StreamRequest
}

func (o *browserBrokerE2EOpener) OpenStream(_ context.Context, request relay.StreamRequest) (io.ReadWriteCloser, error) {
	o.request = request
	return net.Dial("tcp", o.target)
}

func (o *browserBrokerE2EOpener) Close() error { return nil }

func TestWebSSHBrowserBrokerE2E(t *testing.T) {
	api, token, _ := remoteServerAPITest(t)
	ctx := context.Background()

	targetListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer targetListener.Close()
	targetAddress := targetListener.Addr().(*net.TCPAddr)

	server, err := api.DB.RemoteServers().Get(ctx, "server-a")
	if err != nil {
		t.Fatal(err)
	}
	server.Host = "127.0.0.1"
	server.Port = targetAddress.Port
	if err := api.DB.RemoteServers().Update(ctx, server); err != nil {
		t.Fatal(err)
	}
	if _, err := api.DB.Leases().RegisterConnection(ctx, storage.AgentLease{
		AgentID: "agent-a", ConnectionID: "conn-e2e", ServerNodeID: "node-e2e",
		NodeID: "agent-node-e2e", InstanceID: "instance-e2e", ConnectionEpoch: 1, TTL: time.Minute,
	}); err != nil {
		t.Fatal(err)
	}
	api.websshService = NewWebSSHSessionService(
		api.DB.WebSSHSessions(), api.DB.RemoteServers(), api.DB.Credentials(),
		api.DB.Agents(), api.DB.Leases(), api.DB.Audits(), "node-e2e",
	)

	// A real HTTP server is required because x/net/websocket validates the
	// handshake and hijacks the connection through the production upgrader.
	var brokerHandler http.Handler = http.NotFoundHandler()
	httpServer := httptest.NewServer(brokerHandler)
	defer httpServer.Close()

	opener := &browserBrokerE2EOpener{target: targetListener.Addr().String()}
	broker := NewWebSSHBroker(WebSSHBrokerDeps{
		Sessions: api.websshService,
		Opener:   opener,
		Upgrade:  newXNetWebSSHUpgrader(config.SecurityConfig{AllowedOrigins: []string{httpServer.URL}}, 64<<10),
		Security: config.SecurityConfig{AllowedOrigins: []string{httpServer.URL}},
	})
	httpServer.Config.Handler = NewWebHandlerWithManagedRoutes(api.Handler(), nil, nil, nil, broker, nil)

	agentConn := make(chan net.Conn, 1)
	agentClosed := make(chan struct{})
	go func() {
		defer close(agentClosed)
		conn, err := targetListener.Accept()
		if err != nil {
			return
		}
		agentConn <- conn
		request := make([]byte, len("SSH-2.0-test"))
		if _, err := io.ReadFull(conn, request); err != nil {
			return
		}
		if _, err := conn.Write([]byte("SSH-2.0-test\n")); err != nil {
			return
		}
		_, _ = io.Copy(io.Discard, conn)
	}()

	handler := httpServer.Config.Handler
	created := apiJSON(t, handler, http.MethodPost, "/api/v1/remote-servers/server-a/ssh-sessions", token, "webssh-e2e-key", map[string]any{"username": "deploy"})
	if created.Code != http.StatusCreated {
		t.Fatalf("session API status=%d body=%s", created.Code, created.Body.String())
	}
	var ticketResponse struct {
		Data struct {
			SessionID     string `json:"sessionId"`
			Ticket        string `json:"ticket"`
			WebSocketPath string `json:"websocketPath"`
		} `json:"data"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &ticketResponse); err != nil {
		t.Fatal(err)
	}
	if ticketResponse.Data.SessionID == "" || ticketResponse.Data.Ticket == "" || ticketResponse.Data.WebSocketPath == "" {
		t.Fatalf("ticket response=%s", created.Body.String())
	}

	wsURL := strings.Replace(httpServer.URL, "http://", "ws://", 1) +
		ticketResponse.Data.WebSocketPath + "?ticket=" + url.QueryEscape(ticketResponse.Data.Ticket)
	wsConfig, err := websocket.NewConfig(wsURL, httpServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	connection, err := websocket.DialConfig(wsConfig)
	if err != nil {
		t.Fatalf("websocket dial: %v", err)
	}

	if err := websocket.Message.Send(connection, []byte("SSH-2.0-test")); err != nil {
		t.Fatalf("websocket send: %v", err)
	}
	var response []byte
	if err := websocket.Message.Receive(connection, &response); err != nil {
		t.Fatalf("websocket receive: %v", err)
	}
	if string(response) != "SSH-2.0-test\n" {
		t.Fatalf("agent response=%q", response)
	}
	if opener.request.AgentID != "agent-a" || opener.request.TargetHost != "127.0.0.1" || opener.request.TargetPort != targetAddress.Port {
		t.Fatalf("relay request=%+v", opener.request)
	}

	reused, err := websocket.DialConfig(wsConfig)
	if err != nil {
		t.Fatalf("ticket reuse handshake: %v", err)
	}
	var rejected []byte
	if err := websocket.Message.Receive(reused, &rejected); err == nil {
		t.Fatal("one-time ticket was accepted twice")
	}
	_ = reused.Close()

	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-agentClosed:
	case <-time.After(time.Second):
		t.Fatal("agent TCP connection was not closed")
	}
	deadline := time.After(time.Second)
	for broker.ActiveCount() != 0 {
		select {
		case <-deadline:
			t.Fatalf("broker active sessions=%d", broker.ActiveCount())
		case <-time.After(10 * time.Millisecond):
		}
	}
	if conn := <-agentConn; conn != nil {
		_ = conn.Close()
	}
}

func TestServerRuntimeCloseTerminatesWebSSHBridges(t *testing.T) {
	ctx := context.Background()
	db, err := storage.OpenSQLite(ctx, "file:runtime-webssh-close-e2e?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	runtime, err := NewServerRuntime(db, AgentSessionConfig{}, RuntimeConfig{
		NodeID:   "runtime-webssh-close",
		WebSSH:   config.WebSSHConfig{Enabled: true, TicketTTL: time.Minute, SessionTTL: time.Hour, OpenTimeout: time.Second, IdleTimeout: time.Minute},
		Security: config.SecurityConfig{AllowedOrigins: []string{"https://admin.example.com"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	actor := auth.Principal{UserID: "user-close", Username: "alice", Role: "user"}
	if err := db.Agents().Create(ctx, storage.Agent{ID: "agent-close", Name: "agent-close", OwnerUserID: actor.UserID, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RemoteServers().Create(ctx, storage.RemoteServer{
		ID: "server-close", OwnerUserID: actor.UserID, Name: "server-close", Host: "127.0.0.1", Port: 22,
		DefaultUsername: "deploy", AgentID: "agent-close", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Leases().RegisterConnection(ctx, storage.AgentLease{
		AgentID: "agent-close", ConnectionID: "conn-close", ServerNodeID: "runtime-webssh-close",
		NodeID: "agent-node-close", InstanceID: "instance-close", ConnectionEpoch: 1, TTL: time.Minute,
	}); err != nil {
		t.Fatal(err)
	}

	opener := &fakeWebSSHOpener{stream: newFakeRelayStream()}
	runtime.WebSSHBroker.deps.Opener = opener
	ticket, err := runtime.API.websshService.Create(ctx, actor, "server-close", CreateWebSSHSessionInput{Username: "deploy"})
	if err != nil {
		t.Fatal(err)
	}
	ws := newFakeWebSSHWS()
	result := make(chan error, 1)
	go func() { result <- runtime.WebSSHBroker.Handle(ctx, ws, ticket.SessionID, ticket.Ticket) }()
	deadline := time.After(time.Second)
	for runtime.WebSSHBroker.ActiveCount() != 1 {
		select {
		case <-deadline:
			t.Fatal("WebSSH bridge was not registered")
		case <-time.After(10 * time.Millisecond):
		}
	}

	closed := make(chan error, 1)
	go func() { closed <- runtime.Close() }()
	select {
	case <-opener.stream.closed:
	case <-time.After(time.Second):
		t.Fatal("runtime close did not terminate the Agent stream")
	}
	select {
	case <-ws.closeCh:
	case <-time.After(time.Second):
		t.Fatal("runtime close did not terminate the browser WebSocket")
	}
	select {
	case <-result:
	case <-time.After(time.Second):
		t.Fatal("runtime close did not terminate the bridge")
	}
	if err := <-closed; err != nil {
		t.Fatalf("runtime close error: %v", err)
	}
	if runtime.WebSSHBroker.ActiveCount() != 0 {
		t.Fatalf("active WebSSH sessions=%d", runtime.WebSSHBroker.ActiveCount())
	}
}
