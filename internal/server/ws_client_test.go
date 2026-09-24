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
	"github.com/tunnelmesh/tunnelmesh/internal/config"
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
	nodeToken, err := credentials.Create(ctx, auth.CreateTokenInput{
		Type: storage.TokenTypeServerNode, OwnerUserID: owner.ID,
		Scope: auth.TokenScope{ServerNodeIDs: []string{"client-ws-node"}},
	})
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

func TestClientWebSocketHandshakeSelectsSubprotocol(t *testing.T) {
	tests := []struct {
		name            string
		offered         []string
		selected        string
		strictOpen      bool
		metadataEnabled bool
	}{
		{name: "modern", offered: protocol.ClientSubprotocols(), selected: protocol.SubprotocolClientMetadata, strictOpen: true, metadataEnabled: true},
		{name: "open result only", offered: []string{protocol.SubprotocolOpenResult}, selected: protocol.SubprotocolOpenResult, strictOpen: true},
		{name: "flow control", offered: []string{protocol.SubprotocolFlowControl}, selected: protocol.SubprotocolFlowControl, strictOpen: true},
		{name: "legacy", offered: []string{protocol.SubprotocolLegacy}, selected: protocol.SubprotocolLegacy},
		{name: "missing", selected: ""},
		{name: "unsupported", offered: []string{"private.v9"}, selected: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request, err := http.NewRequest(http.MethodGet, "ws://server.example/ws/client", nil)
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Origin", "https://tunnel.example.com")
			request.Header.Set("Authorization", "Bearer client-secret")
			for _, offered := range test.offered {
				request.Header.Add("Sec-WebSocket-Protocol", offered)
			}
			wsConfig := &websocket.Config{Protocol: test.offered}
			handshake := clientWebSocketHandshake(config.SecurityConfig{AllowedOrigins: []string{"https://tunnel.example.com"}}, func(context.Context, string) (ClientSessionPrincipal, error) {
				return ClientSessionPrincipal{
					ConnectionID: "connection", Identity: auth.TokenIdentity{TokenID: "token"},
				}, nil
			})
			if err := handshake(wsConfig, request); err != nil {
				t.Fatal(err)
			}
			if len(test.selected) == 0 {
				if len(wsConfig.Protocol) != 0 {
					t.Fatalf("selected protocols=%v, want none", wsConfig.Protocol)
				}
				return
			}
			if len(wsConfig.Protocol) != 1 || wsConfig.Protocol[0] != test.selected {
				t.Fatalf("selected protocols=%v, want %s", wsConfig.Protocol, test.selected)
			}
			principal, ok := clientPrincipalFromContext(request.Context())
			if !ok {
				t.Fatal("principal missing from request context")
			}
			if principal.StrictOpen != test.strictOpen || principal.MetadataEnabled != test.metadataEnabled {
				t.Fatalf("principal capabilities strict=%v metadata=%v, want strict=%v metadata=%v", principal.StrictOpen, principal.MetadataEnabled, test.strictOpen, test.metadataEnabled)
			}
		})
	}
}

func TestServeClientSessionStrictOpenDoesNotBlockPing(t *testing.T) {
	transport := newChannelClientTransport()
	inner := &blockingAuthorizeTransport{blockAgent: "agent", release: make(chan struct{})}
	service := NewClientStreamService(inner, inner, ClientStreamServiceConfig{MaxConcurrentOpens: 1, MaxPendingOpens: 1, OpenTimeout: time.Second}, nil)
	defer service.Close()
	payload, err := protocol.EncodeStreamOpenPayload(openRequest("agent"))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- serveClientSessionWithService(context.Background(), strictPrincipal(), transport, inner, inner, maxClientOpenAttempts, nil, service)
	}()
	transport.receive <- protocol.Frame{
		Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, Flags: protocol.FlagStrictOpen,
		StreamID: 7, Payload: payload,
	}
	transport.receive <- protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FramePing, Payload: []byte("ping")}
	pong := receiveChannelClientFrame(t, transport)
	if pong.Type != protocol.FramePong || string(pong.Payload) != "ping" {
		t.Fatalf("PING response=%+v, want PONG", pong)
	}
	close(inner.release)
	result := receiveChannelClientFrame(t, transport)
	if result.Type != protocol.FrameOpenResult || result.StreamID != 7 {
		t.Fatalf("open response=%+v, want OPEN_RESULT", result)
	}
	close(transport.receive)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestServeClientSessionHandlesMetadataHelloAndRelease(t *testing.T) {
	instances := &fakeClientInstanceRepo{nextID: "client-instance-1"}
	connections := &fakeClientConnectionRepo{}
	manager := NewClientSessionManager()
	leases := NewClientConnectionLeaseController(connections, manager, "server-1", 90*time.Second)
	observabilityService := NewClientObservabilityService(instances, leases, manager, "server-1", 5*time.Minute)
	transport := newChannelClientTransport()
	opener := &recordingNodeTransport{opened: make(chan relay.StreamRequest, 1)}
	authorizer := streamAuthorizerFunc(func(context.Context, ClientSessionPrincipal, protocol.StreamOpenPayload) error { return nil })
	service := NewClientStreamService(authorizer, opener, ClientStreamServiceConfig{
		MaxConcurrentOpens: 1, MaxPendingOpens: 1, OpenTimeout: time.Second,
		Observability: observabilityService,
	}, nil)
	defer service.Close()
	principal := ClientSessionPrincipal{
		ConnectionID: "connection-metadata", StrictOpen: true, MetadataEnabled: true,
		Identity: auth.TokenIdentity{TokenID: "token-1", OwnerUserID: "owner-1", Type: storage.TokenTypeClient},
	}
	done := make(chan error, 1)
	go func() {
		done <- serveClientSessionWithService(context.Background(), principal, transport, authorizer, opener, maxClientOpenAttempts, nil, service)
	}()

	helloPayload, err := protocol.EncodeClientMetadataPayload(protocol.ClientMetadataPayload{
		InstanceID: "client-0123456789abcdef", Revision: 1, ReportedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	transport.receive <- protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameClientHello, Payload: helloPayload}
	ackFrame := receiveChannelClientFrame(t, transport)
	if ackFrame.Type != protocol.FrameClientMetadataAck {
		t.Fatalf("metadata response=%+v, want ACK", ackFrame)
	}
	ack, err := protocol.DecodeClientMetadataAckPayload(ackFrame.Payload)
	if err != nil || !ack.Accepted || ack.ClientInstanceID != "client-instance-1" {
		t.Fatalf("metadata ack=%#v err=%v", ack, err)
	}

	transport.receive <- protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FramePing, Payload: []byte("ping")}
	if pong := receiveChannelClientFrame(t, transport); pong.Type != protocol.FramePong {
		t.Fatalf("PING response=%+v, want PONG", pong)
	}
	close(transport.receive)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(connections.registered) != 1 || len(connections.released) != 1 {
		t.Fatalf("leases registered=%#v released=%#v", connections.registered, connections.released)
	}
	if _, exists := manager.Get("connection-metadata"); exists {
		t.Fatal("connection was not removed from manager")
	}
}

func TestServeClientSessionDowngradesWhenMetadataHelloIsMissing(t *testing.T) {
	instances := &fakeClientInstanceRepo{nextID: "client-instance-legacy"}
	connections := &fakeClientConnectionRepo{}
	manager := NewClientSessionManager()
	leases := NewClientConnectionLeaseController(connections, manager, "server-1", 90*time.Second)
	observabilityService := NewClientObservabilityService(instances, leases, manager, "server-1", 5*time.Minute)
	transport := newChannelClientTransport()
	opener := &recordingNodeTransport{opened: make(chan relay.StreamRequest, 1)}
	authorizer := streamAuthorizerFunc(func(context.Context, ClientSessionPrincipal, protocol.StreamOpenPayload) error { return nil })
	service := NewClientStreamService(authorizer, opener, ClientStreamServiceConfig{
		MaxConcurrentOpens: 1, MaxPendingOpens: 1, OpenTimeout: time.Second,
		Observability: observabilityService,
	}, nil)
	defer service.Close()
	principal := ClientSessionPrincipal{
		ConnectionID: "connection-metadata-legacy", StrictOpen: true, MetadataEnabled: true,
		Identity: auth.TokenIdentity{TokenID: "token-1", OwnerUserID: "owner-1", Type: storage.TokenTypeClient},
	}
	done := make(chan error, 1)
	go func() {
		done <- serveClientSessionWithService(context.Background(), principal, transport, authorizer, opener, maxClientOpenAttempts, nil, service)
	}()

	payload, err := protocol.EncodeStreamOpenPayload(openRequest("agent"))
	if err != nil {
		t.Fatal(err)
	}
	transport.receive <- protocol.Frame{
		Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: 19, Payload: payload,
	}
	select {
	case request := <-opener.opened:
		if request.StreamID != 19 {
			t.Fatalf("legacy request stream ID=%d, want 19", request.StreamID)
		}
	case <-time.After(time.Second):
		t.Fatal("first OPEN was discarded after missing metadata hello")
	}
	close(transport.receive)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestServeClientSessionLegacyOpenUsesLegacyPathOnStrictConnection(t *testing.T) {
	transport := newChannelClientTransport()
	opener := &recordingNodeTransport{opened: make(chan relay.StreamRequest, 1)}
	authorizer := streamAuthorizerFunc(func(context.Context, ClientSessionPrincipal, protocol.StreamOpenPayload) error { return nil })
	service := NewClientStreamService(authorizer, opener, ClientStreamServiceConfig{MaxConcurrentOpens: 1, MaxPendingOpens: 1, OpenTimeout: time.Second}, nil)
	defer service.Close()
	payload, err := protocol.EncodeStreamOpenPayload(openRequest("agent"))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- serveClientSessionWithService(context.Background(), strictPrincipal(), transport, authorizer, opener, maxClientOpenAttempts, nil, service)
	}()
	transport.receive <- protocol.Frame{
		Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: 17, Payload: payload,
	}
	select {
	case request := <-opener.opened:
		if request.StreamID != 17 || request.StrictOpen {
			t.Fatalf("legacy request=%+v, want non-strict stream 17", request)
		}
	case <-time.After(time.Second):
		t.Fatal("legacy OPEN did not use the legacy relay path")
	}
	close(transport.receive)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

type blockingClientFrameTransport struct {
	mu          sync.Mutex
	sent        chan protocol.Frame
	receive     chan protocol.Frame
	firstData   chan struct{}
	firstStream chan uint32
	release     chan struct{}
	closed      chan struct{}
	once        sync.Once
}

func newBlockingClientFrameTransport() *blockingClientFrameTransport {
	return &blockingClientFrameTransport{
		sent: make(chan protocol.Frame, 8), receive: make(chan protocol.Frame, 8),
		firstData: make(chan struct{}), firstStream: make(chan uint32, 1),
		release: make(chan struct{}), closed: make(chan struct{}),
	}
}

func (t *blockingClientFrameTransport) Send(frame protocol.Frame) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if frame.Type == protocol.FrameData && t.firstData != nil {
		t.once.Do(func() { close(t.firstData) })
		t.firstStream <- frame.StreamID
		select {
		case <-t.release:
		case <-t.closed:
			return errors.New("transport closed")
		}
	}
	select {
	case t.sent <- frame:
		return nil
	case <-t.closed:
		return errors.New("transport closed")
	}
}

func (t *blockingClientFrameTransport) Receive() (protocol.Frame, error) {
	select {
	case frame, ok := <-t.receive:
		if !ok {
			return protocol.Frame{}, io.EOF
		}
		return frame, nil
	case <-t.closed:
		return protocol.Frame{}, io.EOF
	}
}

func (t *blockingClientFrameTransport) Close() error {
	select {
	case <-t.closed:
	default:
		close(t.closed)
	}
	return nil
}

func TestServeClientSessionPrioritizesControlOverSecondRelayStream(t *testing.T) {
	transport := newBlockingClientFrameTransport()
	opener := &queuedNodeTransport{
		connections: []io.ReadWriteCloser{newSingleReadBlockingConn("slow-data"), newSingleReadBlockingConn("fast-data")},
		opened:      make(chan relay.StreamRequest, 2),
	}
	done := make(chan error, 1)
	go func() {
		done <- ServeClientSession(
			context.Background(), ClientSessionPrincipal{ConnectionID: "connection-fair-writer"},
			transport, streamAuthorizerFunc(func(context.Context, ClientSessionPrincipal, protocol.StreamOpenPayload) error { return nil }), opener,
		)
	}()
	defer func() {
		close(transport.receive)
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("session did not stop")
		}
	}()

	for _, streamID := range []uint32{1, 2} {
		payload, err := protocol.EncodeStreamOpenPayload(openRequest("agent"))
		if err != nil {
			t.Fatal(err)
		}
		transport.receive <- protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: streamID, Payload: payload}
	}
	select {
	case <-transport.firstData:
	case <-time.After(time.Second):
		t.Fatal("first relay DATA did not reach transport")
	}
	time.Sleep(50 * time.Millisecond)
	transport.receive <- protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FramePing, Payload: []byte("ping")}
	time.Sleep(20 * time.Millisecond)
	close(transport.release)

	firstStream := <-transport.firstStream
	secondStream := uint32(3 - firstStream)
	want := []struct {
		frameType protocol.FrameType
		streamID  uint32
	}{
		{protocol.FrameData, firstStream},
		{protocol.FramePong, 0},
		{protocol.FrameData, secondStream},
	}
	for i, expected := range want {
		select {
		case frame := <-transport.sent:
			if frame.Type != expected.frameType || frame.StreamID != expected.streamID {
				t.Fatalf("frame %d=%+v, want type=%d stream=%d", i, frame, expected.frameType, expected.streamID)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for frame %d", i)
		}
	}
}

func TestServeClientSessionPropagatesInitialWindowToRelay(t *testing.T) {
	payload, err := protocol.EncodeStreamOpenPayload(openRequest("agent"))
	if err != nil {
		t.Fatal(err)
	}
	transport := newScriptedClientTransport(protocol.Frame{
		Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: 15, Window: 4096, Payload: payload,
	})
	opener := &recordingNodeTransport{opened: make(chan relay.StreamRequest, 1)}
	if err := ServeClientSession(
		context.Background(), ClientSessionPrincipal{ConnectionID: "connection-window"},
		transport, streamAuthorizerFunc(func(context.Context, ClientSessionPrincipal, protocol.StreamOpenPayload) error { return nil }), opener,
	); err != nil {
		t.Fatal(err)
	}
	select {
	case request := <-opener.opened:
		if request.InitialWindow != 4096 {
			t.Fatalf("relay InitialWindow=%d, want 4096", request.InitialWindow)
		}
	default:
		t.Fatal("relay OPEN was not requested")
	}
}

type controlRelayConn struct {
	reads             chan readResult
	controls          chan protocol.Frame
	agentControls     chan protocol.Frame
	agentControlReady chan struct{}
	closed            chan struct{}
	closeOnce         sync.Once
}

func newControlRelayConn() *controlRelayConn {
	return &controlRelayConn{
		reads: make(chan readResult, 4), controls: make(chan protocol.Frame, 4),
		agentControls: make(chan protocol.Frame, 4), agentControlReady: make(chan struct{}, 1), closed: make(chan struct{}),
	}
}

func (c *controlRelayConn) Read(buffer []byte) (int, error) {
	select {
	case result := <-c.reads:
		return copy(buffer, result.payload), result.err
	case <-c.agentControlReady:
		return 0, nil
	case <-c.closed:
		return 0, io.EOF
	}
}

func (*controlRelayConn) Write([]byte) (int, error) { return 0, nil }
func (*controlRelayConn) CloseWrite() error         { return nil }
func (c *controlRelayConn) WriteControl(frame protocol.Frame) error {
	select {
	case c.controls <- frame:
		return nil
	case <-c.closed:
		return io.ErrClosedPipe
	}
}
func (c *controlRelayConn) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return nil
}

func (c *controlRelayConn) ReadControl() (protocol.Frame, bool) {
	select {
	case frame := <-c.agentControls:
		select {
		case <-c.agentControlReady:
		default:
		}
		return frame, true
	default:
		return protocol.Frame{}, false
	}
}

func (c *controlRelayConn) sendAgentControl(frame protocol.Frame) {
	c.agentControls <- frame
	c.agentControlReady <- struct{}{}
}

type controlRelayOpener struct {
	conn   *controlRelayConn
	opened chan relay.StreamRequest
}

func (o *controlRelayOpener) OpenStream(context.Context, relay.StreamRequest) (io.ReadWriteCloser, error) {
	o.opened <- relay.StreamRequest{}
	return o.conn, nil
}
func (*controlRelayOpener) Close() error { return nil }

func TestServeClientSessionRelayHonorsClientWindow(t *testing.T) {
	transport := newChannelClientTransport()
	conn := newControlRelayConn()
	conn.reads <- readResult{payload: []byte("0123456789abcdef")}
	conn.reads <- readResult{payload: []byte("01234567")}
	opener := &controlRelayOpener{conn: conn, opened: make(chan relay.StreamRequest, 1)}
	done := make(chan error, 1)
	go func() {
		done <- ServeClientSession(
			context.Background(), ClientSessionPrincipal{ConnectionID: "connection-relay-window"},
			transport, streamAuthorizerFunc(func(context.Context, ClientSessionPrincipal, protocol.StreamOpenPayload) error { return nil }), opener,
		)
	}()
	defer func() {
		close(transport.receive)
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("session did not stop")
		}
	}()

	payload, err := protocol.EncodeStreamOpenPayload(openRequest("agent"))
	if err != nil {
		t.Fatal(err)
	}
	transport.receive <- protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: 16, Window: 16, Payload: payload}
	<-opener.opened
	if frame := receiveChannelClientFrame(t, transport); frame.Type != protocol.FrameData || frame.StreamID != 16 || len(frame.Payload) != 16 {
		t.Fatalf("first DATA=%+v, want 16 bytes for stream 16", frame)
	}
	select {
	case frame := <-transport.sent:
		t.Fatalf("relay exceeded client window: %+v", frame)
	case <-time.After(100 * time.Millisecond):
	}

	transport.receive <- protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameWindowUpdate, StreamID: 16, Window: 8}
	select {
	case control := <-conn.controls:
		t.Fatalf("Client WINDOW_UPDATE leaked to Agent relay: %+v", control)
	case <-time.After(100 * time.Millisecond):
	}
	if frame := receiveChannelClientFrame(t, transport); frame.Type != protocol.FrameData || frame.StreamID != 16 || len(frame.Payload) != 8 {
		t.Fatalf("post-update DATA=%+v, want 8 bytes for stream 16", frame)
	}
}

func TestServeClientSessionForwardsAgentWindowUpdate(t *testing.T) {
	transport := newChannelClientTransport()
	conn := newControlRelayConn()
	opener := &controlRelayOpener{conn: conn, opened: make(chan relay.StreamRequest, 1)}
	done := make(chan error, 1)
	go func() {
		done <- ServeClientSession(
			context.Background(), ClientSessionPrincipal{ConnectionID: "connection-agent-window"},
			transport, streamAuthorizerFunc(func(context.Context, ClientSessionPrincipal, protocol.StreamOpenPayload) error { return nil }), opener,
		)
	}()
	defer func() {
		close(transport.receive)
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("session did not stop")
		}
	}()

	payload, err := protocol.EncodeStreamOpenPayload(openRequest("agent"))
	if err != nil {
		t.Fatal(err)
	}
	transport.receive <- protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: 17, Window: 262144, Payload: payload}
	<-opener.opened
	conn.sendAgentControl(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameWindowUpdate, Window: 16})
	frame := receiveChannelClientFrame(t, transport)
	if frame.Type != protocol.FrameWindowUpdate || frame.StreamID != 17 || frame.Window != 16 {
		t.Fatalf("Agent WINDOW_UPDATE=%+v, want stream 17 window 16", frame)
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

func TestClientWSRuntimeDefaultTransportBridgesRegisteredAgentBidirectionally(t *testing.T) {
	ctx := context.Background()
	db, err := storage.OpenSQLite(ctx, "file:client-ws-runtime-agent?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	authService := auth.NewAuthService(db)
	owner, err := authService.CreateUser(ctx, "runtime-client-owner", "runtime-client-password", "user")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Agents().Create(ctx, storage.Agent{ID: "runtime-agent", Name: "runtime-agent", OwnerUserID: owner.ID, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := db.Policies().Create(ctx, storage.AgentPolicy{ID: "runtime-client-policy", AgentID: "runtime-agent", TargetHost: "10.0.0.8", TargetPort: 22, Protocol: "tcp", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	credentials := auth.NewCredentialService(db)
	agentToken, err := credentials.Create(ctx, auth.CreateTokenInput{Type: storage.TokenTypeAgent, OwnerUserID: owner.ID, AgentID: "runtime-agent"})
	if err != nil {
		t.Fatal(err)
	}
	clientToken, err := credentials.Create(ctx, auth.CreateTokenInput{Type: storage.TokenTypeClient, OwnerUserID: owner.ID, Scope: auth.TokenScope{AgentIDs: []string{"runtime-agent"}, Protocols: []string{"tcp"}, TargetPorts: []int{22}}})
	if err != nil {
		t.Fatal(err)
	}
	_ = credentials.Close()

	_, address, stop := startClientWSTestRuntime(t, db)
	defer stop()
	agentConfig, _ := websocket.NewConfig("ws://"+address+"/ws/agent", "http://"+address)
	agentConfig.Header.Set("Authorization", "Bearer "+agentToken.Secret)
	agentConn, err := websocket.DialConfig(agentConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer agentConn.Close()
	hello, _ := protocol.EncodeAgentMetadataPayload(protocol.AgentMetadataPayload{AgentID: "runtime-agent", NodeID: "runtime-node", Epoch: 1, Revision: 1, ReportedAt: time.Now().UTC(), Items: []protocol.AgentMetadataItem{}})
	if err := websocket.Message.Send(agentConn, clientServerFrameBytes(t, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameAgentHello, Payload: hello})); err != nil {
		t.Fatal(err)
	}
	if ack := receiveClientServerFrame(t, agentConn); ack.Type != protocol.FrameAgentMetadataAck {
		t.Fatalf("agent hello response = %+v", ack)
	}

	clientConfig, _ := websocket.NewConfig("ws://"+address+"/ws/client", "http://"+address)
	clientConfig.Header.Set("Authorization", "Bearer "+clientToken.Secret)
	clientConn, err := websocket.DialConfig(clientConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer clientConn.Close()
	openPayload, _ := protocol.EncodeStreamOpenPayload(protocol.StreamOpenPayload{AgentID: "runtime-agent", Protocol: "tcp", TargetHost: "10.0.0.8", TargetPort: 22})
	if err := websocket.Message.Send(clientConn, clientServerFrameBytes(t, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: 11, Payload: openPayload})); err != nil {
		t.Fatal(err)
	}
	agentOpen := receiveClientServerFrame(t, agentConn)
	if agentOpen.Type != protocol.FrameOpenStream || agentOpen.StreamID == 0 {
		t.Fatalf("Agent OPEN = %+v", agentOpen)
	}
	if err := websocket.Message.Send(clientConn, clientServerFrameBytes(t, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: 11, Payload: []byte("client-to-agent")})); err != nil {
		t.Fatal(err)
	}
	if frame := receiveClientServerFrame(t, agentConn); frame.Type != protocol.FrameData || frame.StreamID != agentOpen.StreamID || string(frame.Payload) != "client-to-agent" {
		t.Fatalf("Agent DATA = %+v", frame)
	}
	if err := websocket.Message.Send(agentConn, clientServerFrameBytes(t, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: agentOpen.StreamID, Payload: []byte("agent-to-client")})); err != nil {
		t.Fatal(err)
	}
	if frame := receiveClientServerFrame(t, clientConn); frame.Type != protocol.FrameData || frame.StreamID != 11 || string(frame.Payload) != "agent-to-client" {
		t.Fatalf("Client DATA = %+v", frame)
	}
	if err := websocket.Message.Send(clientConn, clientServerFrameBytes(t, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameHalfClose, StreamID: 11})); err != nil {
		t.Fatal(err)
	}
	if frame := receiveClientServerFrame(t, agentConn); frame.Type != protocol.FrameHalfClose || frame.StreamID != agentOpen.StreamID {
		t.Fatalf("Agent HALF_CLOSE = %+v", frame)
	}
	if err := websocket.Message.Send(agentConn, clientServerFrameBytes(t, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameHalfClose, StreamID: agentOpen.StreamID})); err != nil {
		t.Fatal(err)
	}
	if frame := receiveClientServerFrame(t, clientConn); frame.Type != protocol.FrameHalfClose || frame.StreamID != 11 {
		t.Fatalf("Client HALF_CLOSE = %+v", frame)
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

func TestServeClientSessionWithoutObservabilityAcceptsAuthenticatedClient(t *testing.T) {
	transport := newScriptedClientTransport(protocol.Frame{
		Version: protocol.CurrentVersion, Type: protocol.FramePing, Payload: []byte("ping"),
	})
	principal := ClientSessionPrincipal{
		ConnectionID: "connection-without-observability",
		Identity: auth.TokenIdentity{
			TokenID: "token-1", OwnerUserID: "owner-1", Type: storage.TokenTypeClient,
		},
	}
	err := ServeClientSession(
		context.Background(), principal, transport,
		streamAuthorizerFunc(func(context.Context, ClientSessionPrincipal, protocol.StreamOpenPayload) error { return nil }),
		&recordingNodeTransport{opened: make(chan relay.StreamRequest, 1)},
	)
	if err != nil {
		t.Fatalf("ServeClientSession() error = %v", err)
	}
	sent := transport.sentFrames()
	if len(sent) != 1 || sent[0].Type != protocol.FramePong {
		t.Fatalf("responses = %#v, want one PONG", sent)
	}
}

func TestServeClientSessionResetsStreamAfterShortRelayWrite(t *testing.T) {
	payload, err := protocol.EncodeStreamOpenPayload(protocol.StreamOpenPayload{AgentID: "a", Protocol: "tcp", TargetHost: "h", TargetPort: 1})
	if err != nil {
		t.Fatal(err)
	}
	// The upload is written by the stream's own pump, so the session must stay
	// open while the partial write turns into a RESET: a session that already
	// returned would retire the queue and drop the frame instead.
	transport := newChannelClientTransport()
	opener := &shortWriteNodeTransport{}
	done := make(chan error, 1)
	go func() {
		done <- ServeClientSession(context.Background(), ClientSessionPrincipal{ConnectionID: "connection-short-write", Identity: auth.TokenIdentity{TokenID: "token", Type: storage.TokenTypeClient}}, transport, streamAuthorizerFunc(func(context.Context, ClientSessionPrincipal, protocol.StreamOpenPayload) error { return nil }), opener)
	}()
	transport.receive <- protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: 9, Payload: payload}
	transport.receive <- protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: 9, Payload: []byte("complete-frame")}
	reset := false
	deadline := time.After(2 * time.Second)
	for !reset {
		select {
		case frame := <-transport.sent:
			reset = frame.Type == protocol.FrameReset && frame.StreamID == 9
		case <-deadline:
			t.Fatal("no RESET after the Agent connection accepted a partial upload")
		}
	}
	close(transport.receive)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("session did not stop")
	}
}

func TestServeClientSessionNeverReusesStreamIDOrForwardsLateOldData(t *testing.T) {
	transport := newChannelClientTransport()
	old := newLateReadConn()
	opener := &queuedNodeTransport{connections: []io.ReadWriteCloser{old, newLateReadConn()}, opened: make(chan relay.StreamRequest, 2)}
	payload, err := protocol.EncodeStreamOpenPayload(protocol.StreamOpenPayload{AgentID: "a", Protocol: "tcp", TargetHost: "h", TargetPort: 1})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- ServeClientSession(context.Background(), ClientSessionPrincipal{ConnectionID: "connection-reuse", Identity: auth.TokenIdentity{TokenID: "token", Type: storage.TokenTypeClient}}, transport, streamAuthorizerFunc(func(context.Context, ClientSessionPrincipal, protocol.StreamOpenPayload) error { return nil }), opener)
	}()
	transport.receive <- protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: 5, Payload: payload}
	<-opener.opened
	transport.receive <- protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameReset, StreamID: 5}
	transport.receive <- protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: 5, Payload: payload}
	reset := receiveChannelClientFrame(t, transport)
	if reset.Type != protocol.FrameReset || reset.StreamID != 5 {
		t.Fatalf("reused ID response = %+v", reset)
	}
	select {
	case request := <-opener.opened:
		t.Fatalf("reused ID opened a second relay stream: %+v", request)
	case <-time.After(50 * time.Millisecond):
	}
	old.reads <- readResult{payload: []byte("late-secret")}
	select {
	case frame := <-transport.sent:
		if frame.Type == protocol.FrameData {
			t.Fatalf("late old DATA reached Client: %+v", frame)
		}
	case <-time.After(50 * time.Millisecond):
	}
	close(transport.receive)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestServeClientSessionNeverReusesStreamIDAfterCompleteHalfClose(t *testing.T) {
	transport := newChannelClientTransport()
	conn := newDirectionalReadConn()
	opener := &queuedNodeTransport{connections: []io.ReadWriteCloser{conn, newDirectionalReadConn()}, opened: make(chan relay.StreamRequest, 2)}
	payload, _ := protocol.EncodeStreamOpenPayload(protocol.StreamOpenPayload{AgentID: "a", Protocol: "tcp", TargetHost: "h", TargetPort: 1})
	done := make(chan error, 1)
	go func() {
		done <- ServeClientSession(context.Background(), ClientSessionPrincipal{ConnectionID: "connection-complete-reuse", Identity: auth.TokenIdentity{TokenID: "token", Type: storage.TokenTypeClient}}, transport, streamAuthorizerFunc(func(context.Context, ClientSessionPrincipal, protocol.StreamOpenPayload) error { return nil }), opener)
	}()
	transport.receive <- protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: 12, Payload: payload}
	<-opener.opened
	transport.receive <- protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameHalfClose, StreamID: 12}
	select {
	case <-conn.writeClosed:
	case <-time.After(time.Second):
		t.Fatal("relay CloseWrite was not called")
	}
	conn.reads <- readResult{err: io.EOF}
	if frame := receiveChannelClientFrame(t, transport); frame.Type != protocol.FrameHalfClose || frame.StreamID != 12 {
		t.Fatalf("complete response = %+v", frame)
	}
	transport.receive <- protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: 12, Payload: payload}
	if frame := receiveChannelClientFrame(t, transport); frame.Type != protocol.FrameReset || frame.StreamID != 12 {
		t.Fatalf("reused completed ID response = %+v", frame)
	}
	select {
	case request := <-opener.opened:
		t.Fatalf("completed stream ID opened again: %+v", request)
	case <-time.After(50 * time.Millisecond):
	}
	close(transport.receive)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestServeClientSessionResetsHalfCloseWithoutDirectionalRelaySupport(t *testing.T) {
	transport := newChannelClientTransport()
	conn := newLateReadConn()
	opener := &queuedNodeTransport{connections: []io.ReadWriteCloser{conn}, opened: make(chan relay.StreamRequest, 1)}
	payload, _ := protocol.EncodeStreamOpenPayload(protocol.StreamOpenPayload{AgentID: "a", Protocol: "tcp", TargetHost: "h", TargetPort: 1})
	done := make(chan error, 1)
	go func() {
		done <- ServeClientSession(context.Background(), ClientSessionPrincipal{ConnectionID: "connection-half-close", Identity: auth.TokenIdentity{TokenID: "token", Type: storage.TokenTypeClient}}, transport, streamAuthorizerFunc(func(context.Context, ClientSessionPrincipal, protocol.StreamOpenPayload) error { return nil }), opener)
	}()
	transport.receive <- protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: 6, Payload: payload}
	<-opener.opened
	transport.receive <- protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameHalfClose, StreamID: 6}
	frame := receiveChannelClientFrame(t, transport)
	if frame.Type != protocol.FrameReset || frame.StreamID != 6 {
		t.Fatalf("half-close response = %+v, want RESET", frame)
	}
	close(transport.receive)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestServeClientSessionMapsNonEOFRelayReadErrorToReset(t *testing.T) {
	transport := newChannelClientTransport()
	conn := newLateReadConn()
	conn.reads <- readResult{err: errors.New("private relay failure")}
	opener := &queuedNodeTransport{connections: []io.ReadWriteCloser{conn}, opened: make(chan relay.StreamRequest, 1)}
	payload, _ := protocol.EncodeStreamOpenPayload(protocol.StreamOpenPayload{AgentID: "a", Protocol: "tcp", TargetHost: "h", TargetPort: 1})
	done := make(chan error, 1)
	go func() {
		done <- ServeClientSession(context.Background(), ClientSessionPrincipal{ConnectionID: "connection-read-error", Identity: auth.TokenIdentity{TokenID: "token", Type: storage.TokenTypeClient}}, transport, streamAuthorizerFunc(func(context.Context, ClientSessionPrincipal, protocol.StreamOpenPayload) error { return nil }), opener)
	}()
	transport.receive <- protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: 8, Payload: payload}
	frame := receiveChannelClientFrame(t, transport)
	if frame.Type != protocol.FrameReset || frame.StreamID != 8 || string(frame.Payload) != clientStreamResetMessage {
		t.Fatalf("relay error response = %+v, want bounded RESET", frame)
	}
	close(transport.receive)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestServeClientSessionPreservesLargeUDPFrameBoundary(t *testing.T) {
	transport := newChannelClientTransport()
	payload := bytes.Repeat([]byte("u"), 64<<10)
	conn := &singlePayloadConn{payload: payload, release: make(chan struct{})}
	opener := &queuedNodeTransport{connections: []io.ReadWriteCloser{conn}, opened: make(chan relay.StreamRequest, 1)}
	openPayload, _ := protocol.EncodeStreamOpenPayload(protocol.StreamOpenPayload{AgentID: "a", Protocol: "udp", TargetHost: "h", TargetPort: 53})
	done := make(chan error, 1)
	go func() {
		done <- ServeClientSession(context.Background(), ClientSessionPrincipal{ConnectionID: "connection-udp-boundary", Identity: auth.TokenIdentity{TokenID: "token", Type: storage.TokenTypeClient}}, transport, streamAuthorizerFunc(func(context.Context, ClientSessionPrincipal, protocol.StreamOpenPayload) error { return nil }), opener)
	}()
	transport.receive <- protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: 14, Payload: openPayload}
	frame := receiveChannelClientFrame(t, transport)
	if frame.Type != protocol.FrameData || frame.StreamID != 14 || len(frame.Payload) != len(payload) {
		t.Fatalf("UDP DATA type=%d id=%d bytes=%d, want one %d-byte frame", frame.Type, frame.StreamID, len(frame.Payload), len(payload))
	}
	_ = conn.Close()
	close(transport.receive)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestServeClientSessionCapsUniqueOpenAttemptsBeforePayloadDecode(t *testing.T) {
	transport := newScriptedClientTransport(
		protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: 1, Payload: []byte("{")},
		protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: 1, Payload: []byte("{")},
		protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: 2, Payload: []byte("{")},
		protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: 3, Payload: []byte("{")},
	)
	err := serveClientSessionWithOpenLimit(context.Background(), ClientSessionPrincipal{ConnectionID: "connection-open-limit", Identity: auth.TokenIdentity{TokenID: "token", Type: storage.TokenTypeClient}}, transport, streamAuthorizerFunc(func(context.Context, ClientSessionPrincipal, protocol.StreamOpenPayload) error {
		t.Fatal("malformed OPEN reached authorizer")
		return nil
	}), &recordingNodeTransport{opened: make(chan relay.StreamRequest, 1)}, 2)
	if err != nil {
		t.Fatal(err)
	}
	sent := transport.sentFrames()
	if len(sent) != 4 {
		t.Fatalf("sent frame count = %d, want three RESETs and one GOAWAY", len(sent))
	}
	for i, frame := range sent[:3] {
		if frame.Type != protocol.FrameReset {
			t.Fatalf("frame %d = %+v, want RESET", i, frame)
		}
	}
	terminal := sent[3]
	if terminal.Type != protocol.FrameGoAway || terminal.StreamID != 0 || len(terminal.Payload) == 0 || len(terminal.Payload) > 128 {
		t.Fatalf("terminal frame = %+v, want bounded GOAWAY", terminal)
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
	mu      sync.Mutex
	receive []protocol.Frame
	sent    []protocol.Frame
}

type channelClientTransport struct {
	receive chan protocol.Frame
	sent    chan protocol.Frame
}

func newChannelClientTransport() *channelClientTransport {
	return &channelClientTransport{receive: make(chan protocol.Frame, 8), sent: make(chan protocol.Frame, 8)}
}

func receiveChannelClientFrame(t *testing.T, transport *channelClientTransport) protocol.Frame {
	t.Helper()
	select {
	case frame := <-transport.sent:
		return frame
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for Client frame")
		return protocol.Frame{}
	}
}

func (t *channelClientTransport) Receive() (protocol.Frame, error) {
	frame, ok := <-t.receive
	if !ok {
		return protocol.Frame{}, io.EOF
	}
	return frame, nil
}
func (t *channelClientTransport) Send(frame protocol.Frame) error { t.sent <- frame; return nil }
func (t *channelClientTransport) Close() error                    { return nil }

type readResult struct {
	payload []byte
	err     error
}

type lateReadConn struct {
	reads chan readResult
}

func newLateReadConn() *lateReadConn { return &lateReadConn{reads: make(chan readResult, 4)} }
func (c *lateReadConn) Read(buffer []byte) (int, error) {
	result := <-c.reads
	return copy(buffer, result.payload), result.err
}
func (c *lateReadConn) Write(payload []byte) (int, error) { return len(payload), nil }
func (c *lateReadConn) Close() error                      { return nil }

type directionalReadConn struct {
	*lateReadConn
	writeClosed chan struct{}
	once        sync.Once
}

type singlePayloadConn struct {
	payload []byte
	release chan struct{}
	once    sync.Once
}

func (c *singlePayloadConn) Read(buffer []byte) (int, error) {
	if len(c.payload) > 0 {
		n := copy(buffer, c.payload)
		c.payload = c.payload[n:]
		return n, nil
	}
	<-c.release
	return 0, io.EOF
}
func (c *singlePayloadConn) Write(payload []byte) (int, error) { return len(payload), nil }
func (c *singlePayloadConn) CloseWrite() error                 { return nil }
func (c *singlePayloadConn) Close() error {
	c.once.Do(func() { close(c.release) })
	return nil
}

type singleReadBlockingConn struct {
	payload []byte
	release chan struct{}
	once    sync.Once
}

func newSingleReadBlockingConn(payload string) *singleReadBlockingConn {
	return &singleReadBlockingConn{payload: []byte(payload), release: make(chan struct{})}
}

func (c *singleReadBlockingConn) Read(buffer []byte) (int, error) {
	if len(c.payload) > 0 {
		n := copy(buffer, c.payload)
		c.payload = c.payload[n:]
		return n, nil
	}
	<-c.release
	return 0, io.EOF
}
func (*singleReadBlockingConn) Write([]byte) (int, error) { return 0, nil }
func (*singleReadBlockingConn) CloseWrite() error         { return nil }
func (c *singleReadBlockingConn) Close() error {
	c.once.Do(func() { close(c.release) })
	return nil
}

func newDirectionalReadConn() *directionalReadConn {
	return &directionalReadConn{lateReadConn: newLateReadConn(), writeClosed: make(chan struct{})}
}
func (c *directionalReadConn) CloseWrite() error {
	c.once.Do(func() { close(c.writeClosed) })
	return nil
}

type queuedNodeTransport struct {
	mu          sync.Mutex
	connections []io.ReadWriteCloser
	opened      chan relay.StreamRequest
}

func (t *queuedNodeTransport) OpenStream(_ context.Context, request relay.StreamRequest) (io.ReadWriteCloser, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.opened <- request
	if len(t.connections) == 0 {
		return nil, errors.New("no queued connection")
	}
	conn := t.connections[0]
	t.connections = t.connections[1:]
	return conn, nil
}
func (t *queuedNodeTransport) Close() error { return nil }

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
	t.mu.Lock()
	defer t.mu.Unlock()
	t.sent = append(t.sent, frame)
	return nil
}

func (t *scriptedClientTransport) Close() error { return nil }

// sentFrames 返回已发送帧的快照；writer goroutine 与测试 goroutine 并发访问
// sent，直接读切片是数据竞争。
func (t *scriptedClientTransport) sentFrames() []protocol.Frame {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]protocol.Frame(nil), t.sent...)
}

func (t *scriptedClientTransport) resetCount() int {
	count := 0
	for _, frame := range t.sentFrames() {
		if frame.Type == protocol.FrameReset {
			count++
		}
	}
	return count
}

// waitForResetCount 有界轮询 RESET 数量。relay pump goroutine 可能在
// ServeClientSession 因 EOF 返回并完成 writer Drain 之后才把 RESET 入队，
// 因此“返回后立即断言”依赖了实现并不保证的顺序，会在满负载下偶发失败。
func waitForResetCount(t *testing.T, transport *scriptedClientTransport, want int) int {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		if got := transport.resetCount(); got >= want {
			return got
		}
		select {
		case <-deadline:
			return transport.resetCount()
		case <-time.After(2 * time.Millisecond):
		}
	}
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

type stalledAgentConn struct {
	writes  chan []byte
	unblock chan struct{}
	closed  chan struct{}
	once    sync.Once
}

func newStalledAgentConn() *stalledAgentConn {
	return &stalledAgentConn{writes: make(chan []byte, 8), unblock: make(chan struct{}), closed: make(chan struct{})}
}

func (c *stalledAgentConn) Read([]byte) (int, error) {
	<-c.closed
	return 0, io.EOF
}

func (c *stalledAgentConn) Write(payload []byte) (int, error) {
	select {
	case <-c.unblock:
	case <-c.closed:
		return 0, io.ErrClosedPipe
	}
	select {
	case c.writes <- append([]byte(nil), payload...):
		return len(payload), nil
	case <-c.closed:
		return 0, io.ErrClosedPipe
	}
}

func (c *stalledAgentConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return nil
}

type recordingAgentConn struct {
	writes chan []byte
	closed chan struct{}
	once   sync.Once
}

func newRecordingAgentConn() *recordingAgentConn {
	return &recordingAgentConn{writes: make(chan []byte, 8), closed: make(chan struct{})}
}

func (c *recordingAgentConn) Read([]byte) (int, error) {
	<-c.closed
	return 0, io.EOF
}

func (c *recordingAgentConn) Write(payload []byte) (int, error) {
	select {
	case c.writes <- append([]byte(nil), payload...):
		return len(payload), nil
	case <-c.closed:
		return 0, io.ErrClosedPipe
	}
}

func (c *recordingAgentConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return nil
}

// pumpClientTransportFrames keeps the stub transport's Send non-blocking for the
// whole session. A session flushes frames that are still queued while it exits,
// so a test that stops reading `sent` would park that flush forever.
func pumpClientTransportFrames(t *testing.T, transport *channelClientTransport) (<-chan protocol.Frame, func()) {
	t.Helper()
	frames := make(chan protocol.Frame, 256)
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case frame := <-transport.sent:
				select {
				case frames <- frame:
				default:
				}
			case <-stop:
				return
			}
		}
	}()
	return frames, func() { close(stop) }
}

func readClientTransportFrame(t *testing.T, frames <-chan protocol.Frame, timeout time.Duration, message string) protocol.Frame {
	t.Helper()
	select {
	case frame := <-frames:
		return frame
	case <-time.After(timeout):
		t.Fatal(message)
		return protocol.Frame{}
	}
}

func clientSessionTestPrincipal(name string) ClientSessionPrincipal {
	return ClientSessionPrincipal{ConnectionID: name, Identity: auth.TokenIdentity{TokenID: "token", Type: storage.TokenTypeClient}}
}

func clientSessionOpenFrame(t *testing.T, id uint32) protocol.Frame {
	t.Helper()
	payload, err := protocol.EncodeStreamOpenPayload(protocol.StreamOpenPayload{AgentID: "agent-a", Protocol: "tcp", TargetHost: "target.internal", TargetPort: 80})
	if err != nil {
		t.Fatal(err)
	}
	return protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: id, Payload: payload}
}

func TestServeClientSessionUploadToStalledAgentDoesNotBlockOtherStreams(t *testing.T) {
	transport := newChannelClientTransport()
	stalled, healthy := newStalledAgentConn(), newRecordingAgentConn()
	opener := &queuedNodeTransport{connections: []io.ReadWriteCloser{stalled, healthy}, opened: make(chan relay.StreamRequest, 2)}
	_, stopPump := pumpClientTransportFrames(t, transport)
	defer stopPump()
	done := make(chan error, 1)
	go func() {
		done <- ServeClientSession(context.Background(), clientSessionTestPrincipal("connection-upload-isolation"), transport, streamAuthorizerFunc(func(context.Context, ClientSessionPrincipal, protocol.StreamOpenPayload) error { return nil }), opener)
	}()
	transport.receive <- clientSessionOpenFrame(t, 1)
	<-opener.opened
	transport.receive <- protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: 1, Payload: []byte("blocked")}
	transport.receive <- clientSessionOpenFrame(t, 2)
	select {
	case <-opener.opened:
	case <-time.After(3 * time.Second):
		close(stalled.unblock)
		<-done
		t.Fatal("a stalled agent upload blocked the next stream from opening")
	}
	transport.receive <- protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: 2, Payload: []byte("hello")}
	select {
	case got := <-healthy.writes:
		if string(got) != "hello" {
			t.Fatalf("second stream wrote %q", got)
		}
	case <-time.After(3 * time.Second):
		close(stalled.unblock)
		t.Fatal("one stalled upload froze the whole Client session")
	}
	close(stalled.unblock)
	close(transport.receive)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestServeClientSessionAnswersPingDuringStalledUpload(t *testing.T) {
	transport := newChannelClientTransport()
	stalled := newStalledAgentConn()
	opener := &queuedNodeTransport{connections: []io.ReadWriteCloser{stalled}, opened: make(chan relay.StreamRequest, 1)}
	frames, stopPump := pumpClientTransportFrames(t, transport)
	defer stopPump()
	done := make(chan error, 1)
	go func() {
		done <- ServeClientSession(context.Background(), clientSessionTestPrincipal("connection-ping-during-stall"), transport, streamAuthorizerFunc(func(context.Context, ClientSessionPrincipal, protocol.StreamOpenPayload) error { return nil }), opener)
	}()
	transport.receive <- clientSessionOpenFrame(t, 1)
	<-opener.opened
	transport.receive <- protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: 1, Payload: []byte("blocked")}
	transport.receive <- protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FramePing, Payload: []byte("still-alive")}
	frame := readClientTransportFrame(t, frames, 3*time.Second, "the Client frame loop is parked on one stalled upload; PING went unanswered")
	if frame.Type != protocol.FramePong || string(frame.Payload) != "still-alive" {
		t.Fatalf("frame while upload stalled = %+v, want PONG", frame)
	}
	close(stalled.unblock)
	close(transport.receive)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestServeClientSessionUploadQueueOverflowResetsSingleStream(t *testing.T) {
	transport := newChannelClientTransport()
	stalled, healthy := newStalledAgentConn(), newRecordingAgentConn()
	opener := &queuedNodeTransport{connections: []io.ReadWriteCloser{stalled, healthy}, opened: make(chan relay.StreamRequest, 2)}
	frames, stopPump := pumpClientTransportFrames(t, transport)
	defer stopPump()
	done := make(chan error, 1)
	go func() {
		done <- ServeClientSession(context.Background(), clientSessionTestPrincipal("connection-upload-overflow"), transport, streamAuthorizerFunc(func(context.Context, ClientSessionPrincipal, protocol.StreamOpenPayload) error { return nil }), opener)
	}()
	transport.receive <- clientSessionOpenFrame(t, 1)
	<-opener.opened
	chunk := make([]byte, protocol.MaxStreamFrame)
	for i := range chunk {
		chunk[i] = 'x'
	}
	// The pump holds one chunk and the queue the advertised window, so the
	// refusal arrives a frame past that. Pushing until then is fast; waiting for
	// a reply on every push would only make the test slow.
	var reset protocol.Frame
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && reset.Type != protocol.FrameReset {
		transport.receive <- protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: 1, Payload: chunk}
		pending := true
		for pending {
			select {
			case frame := <-frames:
				if frame.Type == protocol.FrameReset && frame.StreamID == 1 {
					reset = frame
				}
			default:
				pending = false
			}
		}
	}
	if reset.Type != protocol.FrameReset {
		close(stalled.unblock)
		t.Fatal("an oversized upload queue did not reset its own stream")
	}
	transport.receive <- clientSessionOpenFrame(t, 2)
	select {
	case <-opener.opened:
	case <-time.After(3 * time.Second):
		close(stalled.unblock)
		t.Fatal("session unusable after one stream was reset")
	}
	transport.receive <- protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: 2, Payload: []byte("survived")}
	select {
	case got := <-healthy.writes:
		if string(got) != "survived" {
			t.Fatalf("surviving stream wrote %q", got)
		}
	case <-time.After(3 * time.Second):
		close(stalled.unblock)
		t.Fatal("the surviving stream never got its bytes")
	}
	close(stalled.unblock)
	close(transport.receive)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
