package server

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/websocket"

	"github.com/tunnelmesh/tunnelmesh/internal/auth"
	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
	"github.com/tunnelmesh/tunnelmesh/internal/relay"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

func TestClientWSRouteIsExactAndRejectsWrongCredentialTypesBeforeUpgrade(t *testing.T) {
	ctx := context.Background()
	db, err := storage.OpenSQLite(ctx, "file:client-ws-auth?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	authService := auth.NewAuthService(db)
	owner, err := authService.CreateUser(ctx, "client-ws-owner", "client-ws-password", "user")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Agents().Create(ctx, storage.Agent{ID: "client-ws-agent", Name: "client-ws-agent", OwnerUserID: owner.ID, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	management, err := authService.Login(ctx, owner.Username, "client-ws-password")
	if err != nil {
		t.Fatal(err)
	}
	credentials := auth.NewCredentialService(db)
	clientToken, err := credentials.Create(ctx, auth.CreateTokenInput{Type: storage.TokenTypeClient, OwnerUserID: owner.ID})
	if err != nil {
		t.Fatal(err)
	}
	agentToken, err := credentials.Create(ctx, auth.CreateTokenInput{Type: storage.TokenTypeAgent, OwnerUserID: owner.ID, AgentID: "client-ws-agent"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Nodes().Create(ctx, storage.ServerNode{ID: "client-ws-node", Address: "127.0.0.1:1", Epoch: 1}); err != nil {
		t.Fatal(err)
	}
	nodeToken, err := credentials.Create(ctx, auth.CreateTokenInput{Type: storage.TokenTypeServerNode, OwnerUserID: owner.ID, NodeID: "client-ws-node"})
	if err != nil {
		t.Fatal(err)
	}
	revoked, err := credentials.Create(ctx, auth.CreateTokenInput{Type: storage.TokenTypeClient, OwnerUserID: owner.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := credentials.Revoke(ctx, revoked.TokenID); err != nil {
		t.Fatal(err)
	}
	future := time.Now().UTC().Add(time.Hour)
	expired, err := credentials.Create(ctx, auth.CreateTokenInput{Type: storage.TokenTypeClient, OwnerUserID: owner.ID, ExpiresAt: &future})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL().ExecContext(ctx, `UPDATE service_tokens SET expires_at=? WHERE id=?`, time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano), expired.TokenID); err != nil {
		t.Fatal(err)
	}
	_ = credentials.Close()

	runtime, address, stop := startClientWSTestRuntime(t, db)
	defer stop()
	_ = runtime
	for _, tc := range []struct {
		name  string
		token string
	}{
		{name: "missing"},
		{name: "management", token: management.Token},
		{name: "agent", token: agentToken.Secret},
		{name: "server node", token: nodeToken.Secret},
		{name: "revoked", token: revoked.Secret},
		{name: "expired", token: expired.Secret},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if status := rawClientWebSocketHandshakeStatus(t, address, "/ws/client", tc.token, "", ""); status != http.StatusForbidden {
				t.Fatalf("status = %d, want %d", status, http.StatusForbidden)
			}
		})
	}
	if status := rawClientWebSocketHandshakeStatus(t, address, "/ws/client", "", clientToken.Secret, ""); status != http.StatusForbidden {
		t.Fatalf("query token status = %d, want %d", status, http.StatusForbidden)
	}
	if status := rawClientWebSocketHandshakeStatus(t, address, "/ws/client", "", "", "token="+clientToken.Secret); status != http.StatusForbidden {
		t.Fatalf("cookie token status = %d, want %d", status, http.StatusForbidden)
	}
	if status := rawClientWebSocketHandshakeStatus(t, address, "/ws/client/other", clientToken.Secret, "", ""); status != http.StatusNotFound {
		t.Fatalf("non-exact route status = %d, want %d", status, http.StatusNotFound)
	}
	conn, status := rawClientWebSocketUpgrade(t, address, "/ws/client", clientToken.Secret, "", "")
	_ = conn.Close()
	if status != http.StatusSwitchingProtocols {
		t.Fatalf("valid client status = %d, want %d", status, http.StatusSwitchingProtocols)
	}
}

func TestClientWSAuthorizesEveryOpenAndKeepsConnectionUsableAfterDenial(t *testing.T) {
	ctx := context.Background()
	db, err := storage.OpenSQLite(ctx, "file:client-ws-open?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	authService := auth.NewAuthService(db)
	owner, err := authService.CreateUser(ctx, "client-open-owner", "client-open-password", "user")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Agents().Create(ctx, storage.Agent{ID: "agent-open", Name: "agent-open", OwnerUserID: owner.ID, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := db.Policies().Create(ctx, storage.AgentPolicy{ID: "client-open-policy", AgentID: "agent-open", TargetHost: "10.0.0.8", TargetPort: 22, Protocol: "tcp", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	credentials := auth.NewCredentialService(db)
	created, err := credentials.Create(ctx, auth.CreateTokenInput{Type: storage.TokenTypeClient, OwnerUserID: owner.ID, Scope: auth.TokenScope{AgentIDs: []string{"agent-open"}, Protocols: []string{"tcp"}, TargetPorts: []int{22, 23}}})
	if err != nil {
		t.Fatal(err)
	}
	_ = credentials.Close()

	runtime, address, stop := startClientWSTestRuntime(t, db)
	defer stop()
	opener := &recordingNodeTransport{opened: make(chan relay.StreamRequest, 2)}
	runtime.ClientTransport = opener

	config, err := websocket.NewConfig("ws://"+address+"/ws/client", "http://"+address)
	if err != nil {
		t.Fatal(err)
	}
	config.Header.Set("Authorization", "Bearer "+created.Secret)
	conn, err := websocket.DialConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	allowedPayload, _ := protocol.EncodeStreamOpenPayload(protocol.StreamOpenPayload{AgentID: "agent-open", Protocol: "tcp", TargetHost: "10.0.0.8", TargetPort: 22, Metadata: []byte("allowed")})
	deniedPayload, _ := protocol.EncodeStreamOpenPayload(protocol.StreamOpenPayload{AgentID: "agent-open", Protocol: "tcp", TargetHost: "10.0.0.8", TargetPort: 23, Metadata: []byte("denied")})
	if err := websocket.Message.Send(conn, clientServerFrameBytes(t, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: 1, Payload: allowedPayload})); err != nil {
		t.Fatal(err)
	}
	select {
	case request := <-opener.opened:
		if request.AgentID != "agent-open" || request.Protocol != "tcp" || request.TargetHost != "10.0.0.8" || request.TargetPort != 22 || string(request.Metadata) != "allowed" {
			t.Fatalf("relay request = %+v", request)
		}
	case <-time.After(time.Second):
		t.Fatal("allowed OPEN did not reach relay")
	}
	if err := websocket.Message.Send(conn, clientServerFrameBytes(t, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: 2, Payload: deniedPayload})); err != nil {
		t.Fatal(err)
	}
	denied := receiveClientServerFrame(t, conn)
	if denied.Type != protocol.FrameReset || denied.StreamID != 2 || len(denied.Payload) == 0 || len(denied.Payload) > 128 {
		t.Fatalf("denied frame = %+v, payload bytes=%d", denied, len(denied.Payload))
	}
	select {
	case request := <-opener.opened:
		t.Fatalf("denied OPEN reached relay: %+v", request)
	default:
	}
	if err := websocket.Message.Send(conn, clientServerFrameBytes(t, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FramePing, Payload: []byte("still-alive")})); err != nil {
		t.Fatal(err)
	}
	pong := receiveClientServerFrame(t, conn)
	if pong.Type != protocol.FramePong || string(pong.Payload) != "still-alive" {
		t.Fatalf("pong = %+v", pong)
	}
}

func TestServeClientSessionRejectsInvalidStreamTransitionsWithoutPanicking(t *testing.T) {
	transport := newScriptedClientTransport(
		protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: 99, Payload: []byte("unknown")},
		protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: 7, Payload: []byte(`{"agent_id":"a","protocol":"tcp","target_host":"h","target_port":1}`)},
		protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: 7, Payload: []byte(`{"agent_id":"a","protocol":"tcp","target_host":"h","target_port":1}`)},
	)
	opener := &recordingNodeTransport{opened: make(chan relay.StreamRequest, 1)}
	authorizer := streamAuthorizerFunc(func(context.Context, ClientSessionPrincipal, protocol.StreamOpenPayload) error { return nil })
	err := ServeClientSession(context.Background(), ClientSessionPrincipal{ConnectionID: "connection-transition", Identity: auth.TokenIdentity{TokenID: "token", Type: storage.TokenTypeClient}}, transport, authorizer, opener)
	if err != nil {
		t.Fatalf("ServeClientSession() error = %v", err)
	}
	if got := transport.resetCount(); got != 2 {
		t.Fatalf("RESET count = %d, want 2", got)
	}
}

func TestServeClientSessionRejectsZeroStreamIDAsConnectionProtocolError(t *testing.T) {
	transport := newScriptedClientTransport(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: 0})
	err := ServeClientSession(context.Background(), ClientSessionPrincipal{ConnectionID: "connection-zero", Identity: auth.TokenIdentity{TokenID: "token", Type: storage.TokenTypeClient}}, transport, streamAuthorizerFunc(func(context.Context, ClientSessionPrincipal, protocol.StreamOpenPayload) error { return nil }), &recordingNodeTransport{opened: make(chan relay.StreamRequest, 1)})
	if !errors.Is(err, protocol.ErrInvalidFrame) {
		t.Fatalf("ServeClientSession() error = %v, want ErrInvalidFrame", err)
	}
}

func TestServeClientSessionResetsStreamAfterShortRelayWrite(t *testing.T) {
	payload, err := protocol.EncodeStreamOpenPayload(protocol.StreamOpenPayload{AgentID: "a", Protocol: "tcp", TargetHost: "h", TargetPort: 1})
	if err != nil {
		t.Fatal(err)
	}
	transport := newScriptedClientTransport(
		protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: 9, Payload: payload},
		protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: 9, Payload: []byte("complete-frame")},
	)
	opener := &shortWriteNodeTransport{}
	err = ServeClientSession(context.Background(), ClientSessionPrincipal{ConnectionID: "connection-short-write", Identity: auth.TokenIdentity{TokenID: "token", Type: storage.TokenTypeClient}}, transport, streamAuthorizerFunc(func(context.Context, ClientSessionPrincipal, protocol.StreamOpenPayload) error { return nil }), opener)
	if err != nil {
		t.Fatal(err)
	}
	if got := transport.resetCount(); got != 1 {
		t.Fatalf("RESET count = %d, want 1 after partial relay write", got)
	}
}

type streamAuthorizerFunc func(context.Context, ClientSessionPrincipal, protocol.StreamOpenPayload) error

func (f streamAuthorizerFunc) Authorize(ctx context.Context, principal ClientSessionPrincipal, request protocol.StreamOpenPayload) error {
	return f(ctx, principal, request)
}

type recordingNodeTransport struct {
	opened chan relay.StreamRequest
}

func (t *recordingNodeTransport) OpenStream(_ context.Context, request relay.StreamRequest) (io.ReadWriteCloser, error) {
	select {
	case t.opened <- request:
	default:
	}
	server, peer := net.Pipe()
	go func() {
		defer peer.Close()
		_, _ = io.Copy(io.Discard, peer)
	}()
	return server, nil
}

func (t *recordingNodeTransport) Close() error { return nil }

type shortWriteNodeTransport struct{}

func (t *shortWriteNodeTransport) OpenStream(context.Context, relay.StreamRequest) (io.ReadWriteCloser, error) {
	return &shortWriteConn{closed: make(chan struct{})}, nil
}

func (t *shortWriteNodeTransport) Close() error { return nil }

type shortWriteConn struct {
	closed chan struct{}
	once   sync.Once
}

func (c *shortWriteConn) Read([]byte) (int, error) {
	<-c.closed
	return 0, io.EOF
}

func (c *shortWriteConn) Write(payload []byte) (int, error) { return len(payload) - 1, nil }
func (c *shortWriteConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return nil
}

type scriptedClientTransport struct {
	receive []protocol.Frame
	sent    []protocol.Frame
}

func newScriptedClientTransport(frames ...protocol.Frame) *scriptedClientTransport {
	return &scriptedClientTransport{receive: frames}
}

func (t *scriptedClientTransport) Receive() (protocol.Frame, error) {
	if len(t.receive) == 0 {
		return protocol.Frame{}, io.EOF
	}
	frame := t.receive[0]
	t.receive = t.receive[1:]
	return frame, nil
}

func (t *scriptedClientTransport) Send(frame protocol.Frame) error {
	t.sent = append(t.sent, frame)
	return nil
}

func (t *scriptedClientTransport) Close() error { return nil }

func (t *scriptedClientTransport) resetCount() int {
	count := 0
	for _, frame := range t.sent {
		if frame.Type == protocol.FrameReset {
			count++
		}
	}
	return count
}

func startClientWSTestRuntime(t *testing.T, db *storage.DB) (*ServerRuntime, string, func()) {
	t.Helper()
	runtime, err := NewServerRuntime(db, AgentSessionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runtime.ServeListener(ctx, listener) }()
	return runtime, listener.Addr().String(), func() {
		cancel()
		<-done
		_ = runtime.Close()
	}
}

func rawClientWebSocketHandshakeStatus(t *testing.T, address, path, token, queryToken, cookie string) int {
	t.Helper()
	conn, status := rawClientWebSocketUpgrade(t, address, path, token, queryToken, cookie)
	_ = conn.Close()
	return status
}

func rawClientWebSocketUpgrade(t *testing.T, address, path, token, queryToken, cookie string) (net.Conn, int) {
	t.Helper()
	conn, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	if queryToken != "" {
		path += "?token=" + queryToken
	}
	request := "GET " + path + " HTTP/1.1\r\nHost: " + address + "\r\nOrigin: http://" + address + "\r\n"
	if token != "" {
		request += "Authorization: Bearer " + token + "\r\n"
	}
	if cookie != "" {
		request += "Cookie: " + cookie + "\r\n"
	}
	request += "Upgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\n"
	if _, err := conn.Write([]byte(request)); err != nil {
		_ = conn.Close()
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	response, err := http.ReadResponse(bufioNewReader(conn), &http.Request{Method: http.MethodGet})
	if err != nil {
		_ = conn.Close()
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Time{})
	return conn, response.StatusCode
}

func clientServerFrameBytes(t *testing.T, frame protocol.Frame) []byte {
	t.Helper()
	var payload bytes.Buffer
	if err := protocol.NewEncoder(&payload).WriteFrame(frame); err != nil {
		t.Fatal(err)
	}
	return payload.Bytes()
}

func receiveClientServerFrame(t *testing.T, conn *websocket.Conn) protocol.Frame {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	var raw []byte
	if err := websocket.Message.Receive(conn, &raw); err != nil {
		t.Fatal(err)
	}
	frame, err := protocol.NewDecoder(bytes.NewReader(raw)).ReadFrame()
	if err != nil {
		t.Fatal(err)
	}
	return frame
}

func bufioNewReader(reader io.Reader) *bufio.Reader { return bufio.NewReader(reader) }
