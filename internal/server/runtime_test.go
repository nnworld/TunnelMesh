package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/websocket"
	"google.golang.org/grpc"

	"github.com/tunnelmesh/tunnelmesh/internal/agent"
	"github.com/tunnelmesh/tunnelmesh/internal/auth"
	"github.com/tunnelmesh/tunnelmesh/internal/config"
	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
	"github.com/tunnelmesh/tunnelmesh/internal/registry"
	"github.com/tunnelmesh/tunnelmesh/internal/relay"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

func TestServeListenerReportsRelayServeFailure(t *testing.T) {
	db, err := storage.OpenSQLite(context.Background(), "file:runtime-relay-serve-failure?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	runtime, err := NewServerRuntime(db, AgentSessionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	relayListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if err := relayListener.Close(); err != nil {
		t.Fatal(err)
	}
	runtime.relayServer = grpc.NewServer()
	runtime.relayListener = relayListener
	httpListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer httpListener.Close()
	serveErr := make(chan error, 1)
	go func() { serveErr <- runtime.ServeListener(context.Background(), httpListener) }()
	select {
	case err := <-serveErr:
		if err == nil {
			t.Fatal("ServeListener returned nil after relay Serve failure")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ServeListener did not report relay Serve failure")
	}
}

func TestNewServerRuntimeStartsPlaintextRelayWhenCertificatesOmitted(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	db, err := storage.OpenSQLite(ctx, "file:runtime-plaintext-relay?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	runtime, err := NewServerRuntime(db, AgentSessionConfig{}, RuntimeConfig{
		NodeID: "server-plaintext",
		Relay: config.RelayConfig{
			Enabled: true, Listen: "127.0.0.1:0", Endpoint: "127.0.0.1:9443", NodeToken: "secret",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	if runtime.relayListener == nil || runtime.relayServer == nil {
		t.Fatalf("plaintext relay was not initialized: listener=%v server=%v", runtime.relayListener, runtime.relayServer)
	}

	httpListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer httpListener.Close()
	serveErr := make(chan error, 1)
	go func() { serveErr <- runtime.ServeListener(ctx, httpListener) }()
	node, err := db.Nodes().Get(ctx, "server-plaintext")
	if err != nil {
		t.Fatal(err)
	}
	client, err := runtime.DialRelayNode(ctx, runtime.relayListener.Addr().String(), node.Epoch)
	if err != nil {
		t.Fatalf("DialRelayNode(plaintext) error = %v", err)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-serveErr:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runtime did not stop after context cancellation")
	}
}

func TestServerRuntimeRelayPropagatesStrictOpenResult(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	db, err := storage.OpenSQLite(ctx, "file:runtime-strict-relay?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	runtime, err := NewServerRuntime(db, AgentSessionConfig{}, RuntimeConfig{
		NodeID: "server-strict-relay",
		Relay: config.RelayConfig{
			Enabled: true, Listen: "127.0.0.1:0", Endpoint: "127.0.0.1:9443", NodeToken: "secret",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()

	httpListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer httpListener.Close()
	serveErr := make(chan error, 1)
	go func() { serveErr <- runtime.ServeListener(ctx, httpListener) }()

	agentTransport := newFakeTransport()
	agentSession, err := runtime.AgentSessions.Register(ctx, AgentRegistration{
		AgentID: "agent-runtime", NodeID: "server-strict-relay", Epoch: 1,
		Capabilities: []string{protocol.CapabilityStreamOpenResult},
	}, agentTransport)
	if err != nil {
		t.Fatal(err)
	}
	node, err := db.Nodes().Get(ctx, "server-strict-relay")
	if err != nil {
		t.Fatal(err)
	}
	authService := auth.NewAuthService(db)
	owner, err := authService.CreateUser(ctx, "runtime-strict-owner", "password", "user")
	if err != nil {
		t.Fatal(err)
	}
	credentials := auth.NewCredentialService(db)
	defer credentials.Close()
	nodeToken, err := credentials.Create(ctx, auth.CreateTokenInput{
		Type: storage.TokenTypeServerNode, OwnerUserID: owner.ID,
		Scope: auth.TokenScope{ServerNodeIDs: []string{"server-strict-relay"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	client, err := relay.DialAuthenticatedGRPCNode(ctx, runtime.relayListener.Addr().String(), "server-strict-relay", node.Epoch, nodeToken.Secret, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	type openResult struct {
		stream io.ReadWriteCloser
		result relay.RelayOpenResult
		err    error
	}
	resultCh := make(chan openResult, 1)
	go func() {
		stream, result, openErr := client.OpenStreamResult(ctx, relay.StreamRequest{
			NodeID: "server-strict-relay", Epoch: node.Epoch, AgentID: "agent-runtime",
			StrictOpen: true, Protocol: "tcp", TargetHost: "10.0.0.8", TargetPort: 22,
		})
		resultCh <- openResult{stream: stream, result: result, err: openErr}
	}()

	var open protocol.Frame
	select {
	case open = <-agentTransport.sent:
	case got := <-resultCh:
		t.Fatalf("relay returned before Agent OPEN frame: stream=%v result=%+v err=%v", got.stream, got.result.Payload, got.err)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for Agent OPEN frame")
	}
	if open.Type != protocol.FrameOpenStream || open.Flags&protocol.FlagStrictOpen == 0 {
		t.Fatalf("Agent OPEN frame = %+v, want strict OPEN_STREAM", open)
	}
	payload, err := protocol.EncodeOpenResultPayload(protocol.OpenResultPayload{
		Accepted: true, Stage: protocol.OpenResultStageConnect, Code: protocol.OpenResultCodeOK,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.LocalAgentRelay.handleAgentFrameGeneration("agent-runtime", agentSession.serverGeneration, protocol.Frame{
		Version: protocol.CurrentVersion, Type: protocol.FrameOpenResult, StreamID: open.StreamID, Payload: payload,
	}); err != nil {
		t.Fatal(err)
	}

	select {
	case got := <-resultCh:
		if got.err != nil || got.stream == nil || !got.result.Payload.Accepted || got.result.Payload.Code != protocol.OpenResultCodeOK {
			t.Fatalf("relay result stream=%v result=%+v err=%v", got.stream, got.result.Payload, got.err)
		}
		if err := runtime.LocalAgentRelay.handleAgentFrameGeneration("agent-runtime", agentSession.serverGeneration, protocol.Frame{
			Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: open.StreamID, Payload: []byte("relay-data"),
		}); err != nil {
			t.Fatal(err)
		}
		buffer := make([]byte, len("relay-data"))
		if _, err := io.ReadFull(got.stream, buffer); err != nil || string(buffer) != "relay-data" {
			t.Fatalf("relay data=%q err=%v", buffer, err)
		}
		_ = got.stream.Close()
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for strict relay result")
	}
	cancel()
	select {
	case err := <-serveErr:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runtime did not stop after context cancellation")
	}
}

func TestNewServerRuntimeRejectsPartialRelayTLSMaterial(t *testing.T) {
	db, err := storage.OpenSQLite(context.Background(), "file:runtime-partial-relay-tls?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = NewServerRuntime(db, AgentSessionConfig{}, RuntimeConfig{
		NodeID: "server-partial-tls",
		Relay: config.RelayConfig{
			Enabled: true, Listen: "127.0.0.1:0", Endpoint: "127.0.0.1:9443",
			CA: "/etc/tunnelmesh/certs/relay-ca.pem", NodeToken: "secret",
		},
	})
	if err == nil || !strings.Contains(err.Error(), "all CA, certificate, and key") {
		t.Fatalf("NewServerRuntime(partial TLS) error = %v, want complete TLS material error", err)
	}
}

func TestNewServerRuntimeLoadsCompleteRelayTLSMaterial(t *testing.T) {
	certFile, keyFile, _ := writeNativeTLSCertificate(t)
	db, err := storage.OpenSQLite(context.Background(), "file:runtime-complete-relay-tls?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	runtime, err := NewServerRuntime(db, AgentSessionConfig{}, RuntimeConfig{
		NodeID: "server-complete-tls",
		Relay: config.RelayConfig{
			Enabled: true, Listen: "127.0.0.1:0", Endpoint: "127.0.0.1:9443",
			CA: certFile, Cert: certFile, Key: keyFile, ServerName: "localhost", NodeToken: "secret",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	if runtime.relayListener == nil || runtime.relayServer == nil {
		t.Fatalf("mTLS relay was not initialized: listener=%v server=%v", runtime.relayListener, runtime.relayServer)
	}
}

func TestNewServerRuntimeWiresAgentMetadataPersistence(t *testing.T) {
	db, err := storage.OpenSQLite(context.Background(), "file:server-runtime?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	runtime, err := NewServerRuntime(db, AgentSessionConfig{Authenticate: func(context.Context, AgentRegistration) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	session, err := runtime.AgentSessions.Register(context.Background(), AgentRegistration{AgentID: "agent-runtime", NodeID: "node-runtime", Epoch: 1}, newFakeTransport())
	if err != nil {
		t.Fatal(err)
	}
	ack, err := session.HandleMetadata(context.Background(), protocol.AgentMetadataPayload{
		AgentID: "agent-runtime", NodeID: "node-runtime", Epoch: 1, Revision: 1,
		Items: []protocol.AgentMetadataItem{{Name: "region", Source: "env", Value: "east"}},
	})
	if err != nil || !ack.Accepted {
		t.Fatalf("metadata ack=%+v err=%v", ack, err)
	}
	metadata, err := db.Metadata().Get(context.Background(), "agent-runtime")
	if err != nil || metadata.AgentID != "agent-runtime" || metadata.NodeID != "node-runtime" {
		t.Fatalf("metadata=%+v err=%v", metadata, err)
	}
}

func TestNewServerRuntimeWiresClientObservability(t *testing.T) {
	db, err := storage.OpenSQLite(context.Background(), "file:server-runtime-client-observability?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	runtime, err := NewServerRuntime(db, AgentSessionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if runtime.ClientSessions == nil {
		t.Fatal("ClientSessions was not wired")
	}
	if runtime.ClientConnectionLeases == nil {
		t.Fatal("ClientConnectionLeases was not wired")
	}
	if runtime.ClientObservability == nil {
		t.Fatal("ClientObservability was not wired")
	}
	if runtime.ClientStreamService == nil || runtime.ClientStreamService.observability != runtime.ClientObservability {
		t.Fatal("ClientStreamService was not wired to ClientObservability")
	}
	if runtime.clientMetadataSweeper == nil || runtime.clientMetadataSweeperCancel == nil || runtime.clientMetadataSweeperDone == nil {
		t.Fatal("ClientMetadataSweeper was not started")
	}
	if err := runtime.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func TestNewServerRuntimeWiresAuthorizationCacheAndReadiness(t *testing.T) {
	ctx := context.Background()
	db, err := storage.OpenSQLite(ctx, "file:runtime-authorization-cache?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	cacheConfig := config.AuthorizationCacheConfig{
		Enabled: true, LocalPositiveTTL: 5 * time.Second, ClusterPositiveTTL: 5 * time.Minute,
		NegativeTTL: 3 * time.Second, RevisionPollInterval: time.Millisecond,
		MaxStaleOnPollError: 5 * time.Second, MaxEntries: 100,
	}
	runtime, err := NewServerRuntime(db, AgentSessionConfig{}, RuntimeConfig{AuthorizationCache: cacheConfig})
	if err != nil {
		t.Fatal(err)
	}
	wrapper, ok := runtime.ClientAuthorizer.(*cachingStreamAuthorizer)
	if !ok {
		t.Fatalf("client authorizer type=%T, want caching stream authorizer", runtime.ClientAuthorizer)
	}
	if wrapper.config.PositiveTTL != cacheConfig.LocalPositiveTTL {
		t.Fatalf("SQLite positive TTL=%v, want %v", wrapper.config.PositiveTTL, cacheConfig.LocalPositiveTTL)
	}
	if status := findComponent(runtime.Ready(ctx), "authorization_cache"); status == nil || !status.Healthy {
		t.Fatalf("authorization cache readiness=%+v, want healthy", status)
	}

	called := false
	originalCleanup := runtime.authorizationCacheCleanup
	runtime.authorizationCacheCleanup = func() error {
		called = true
		return originalCleanup()
	}
	if err := runtime.Close(); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("runtime Close did not stop the authorization cache poller")
	}
}

func TestServerRuntimeAuthorizationCacheReadinessFailsClosed(t *testing.T) {
	ctx := context.Background()
	db, err := storage.OpenSQLite(ctx, "file:runtime-authorization-cache-unhealthy?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := NewServerRuntime(db, AgentSessionConfig{}, RuntimeConfig{AuthorizationCache: config.AuthorizationCacheConfig{
		Enabled: true, LocalPositiveTTL: time.Minute, ClusterPositiveTTL: 5 * time.Minute,
		NegativeTTL: time.Second, RevisionPollInterval: time.Millisecond,
		MaxStaleOnPollError: time.Second, MaxEntries: 16,
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	wrapper, ok := runtime.ClientAuthorizer.(*cachingStreamAuthorizer)
	if !ok {
		t.Fatalf("client authorizer type=%T, want caching stream authorizer", runtime.ClientAuthorizer)
	}
	deadline := time.Now().Add(time.Second)
	for !wrapper.Unhealthy() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !wrapper.Unhealthy() {
		t.Fatal("authorization cache did not report an unhealthy revision source")
	}
	status := findComponent(runtime.Ready(ctx), "authorization_cache")
	if status == nil || status.Healthy {
		t.Fatalf("authorization cache readiness=%+v, want unhealthy", status)
	}
}

func TestServerRuntimeAuthorizationCacheRevocationBlocksNewStreams(t *testing.T) {
	ctx := context.Background()
	db, err := storage.OpenSQLite(ctx, "file:runtime-authorization-revocation?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	runtime, err := NewServerRuntime(db, AgentSessionConfig{}, RuntimeConfig{AuthorizationCache: config.AuthorizationCacheConfig{
		Enabled: true, LocalPositiveTTL: time.Minute, ClusterPositiveTTL: 5 * time.Minute,
		NegativeTTL: time.Second, RevisionPollInterval: time.Millisecond,
		MaxStaleOnPollError: time.Second, MaxEntries: 16,
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()

	owner, err := runtime.Auth.CreateUser(ctx, "authorization-cache-owner", "password", "user")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Agents().Create(ctx, storage.Agent{ID: "authorization-cache-agent", Name: "agent", OwnerUserID: owner.ID, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := db.Policies().Create(ctx, storage.AgentPolicy{ID: "authorization-cache-policy", AgentID: "authorization-cache-agent", TargetHost: "10.0.0.8", TargetPort: 22, Protocol: "tcp", AllowedCIDRs: "10.0.0.0/24", AllowedPorts: "22"}); err != nil {
		t.Fatal(err)
	}
	created, err := runtime.Credentials.Create(ctx, auth.CreateTokenInput{Type: storage.TokenTypeClient, OwnerUserID: owner.ID})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := runtime.Credentials.ValidateAs(ctx, created.Secret, storage.TokenTypeClient)
	if err != nil {
		t.Fatal(err)
	}
	principal := ClientSessionPrincipal{ConnectionID: "authorization-cache-connection", Identity: identity}
	request := protocol.StreamOpenPayload{AgentID: "authorization-cache-agent", Protocol: "tcp", TargetHost: "10.0.0.8", TargetPort: 22}
	if err := runtime.ClientAuthorizer.Authorize(ctx, principal, request); err != nil {
		t.Fatalf("Authorize before revoke error=%v", err)
	}
	if err := runtime.Credentials.Revoke(ctx, created.TokenID); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(time.Second)
	var revokedErr error
	for time.Now().Before(deadline) {
		revokedErr = runtime.ClientAuthorizer.Authorize(ctx, principal, request)
		if errors.Is(revokedErr, auth.ErrForbidden) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("Authorize after revoke error=%v, want forbidden", revokedErr)
}

func findComponent(components []ComponentStatus, name string) *ComponentStatus {
	for i := range components {
		if components[i].Name == name {
			return &components[i]
		}
	}
	return nil
}

func TestAgentWebSocketRouteIsExact(t *testing.T) {
	ws := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	handler := NewWebHandler(http.NotFoundHandler(), ws)

	for _, tc := range []struct {
		path string
		want int
	}{
		{path: "/ws/agent", want: http.StatusNoContent},
		{path: "/ws/client", want: http.StatusNotFound},
		{path: "/ws/agent/other", want: http.StatusNotFound},
		{path: "/ws/unknown", want: http.StatusNotFound},
	} {
		t.Run(tc.path, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, tc.path, nil))
			if recorder.Code != tc.want {
				t.Fatalf("GET %s status = %d, want %d", tc.path, recorder.Code, tc.want)
			}
		})
	}
}

func TestServerRuntimeRoutesManagedHostBeforeSPAFallback(t *testing.T) {
	ctx := context.Background()
	db, err := storage.OpenSQLite(ctx, "file:runtime-managed-routes?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	now := time.Now().UTC()
	if err := db.Tunnels().Create(ctx, storage.Tunnel{
		ID: "tunnel-explicit", AgentID: "agent-explicit", Protocol: "http",
		Domain: "app.example.test", TargetHost: "10.0.0.8", TargetPort: 3000,
		Status: "active", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	runtime, err := NewServerRuntime(db, AgentSessionConfig{}, RuntimeConfig{
		DynamicSuffix: "apps.example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()

	tests := []struct {
		name string
		host string
	}{
		{name: "explicit database route", host: "app.example.test"},
		{name: "dynamic route", host: "agent-explicit-10-0-0-8-3000.apps.example.com"},
		{name: "dynamic agent-local route", host: "agent-explicit-127-0-0-1-3000.apps.example.com"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "http://"+tt.host+"/", nil)
			recorder := httptest.NewRecorder()
			runtime.Handler().ServeHTTP(recorder, request)

			// Agent is intentionally offline in this test. Reaching the proxy
			// proves that the request did not fall through to the admin SPA.
			if recorder.Code != http.StatusBadGateway {
				t.Fatalf("status = %d, want %d; body = %q", recorder.Code, http.StatusBadGateway, recorder.Body.String())
			}
			if strings.Contains(strings.ToLower(recorder.Body.String()), "<!doctype html") {
				t.Fatal("managed host was swallowed by the admin SPA fallback")
			}
		})
	}
}

func TestServerRuntimeServesAPIAndAgentWebSocket(t *testing.T) {
	db, err := storage.OpenSQLite(context.Background(), "file:runtime-http?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	authService := auth.NewAuthService(db)
	user, err := authService.CreateUser(context.Background(), "runtime-owner", "runtime-pass", "user")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Agents().Create(context.Background(), storage.Agent{ID: "agent-ws", Name: "agent-ws", OwnerUserID: user.ID, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	login, err := authService.Login(context.Background(), user.Username, "runtime-pass")
	if err != nil {
		t.Fatal(err)
	}
	credentials := auth.NewCredentialService(db)
	agentToken, err := credentials.Create(context.Background(), auth.CreateTokenInput{Type: storage.TokenTypeAgent, OwnerUserID: user.ID, AgentID: "agent-ws"})
	if err != nil {
		t.Fatal(err)
	}
	_ = credentials.Close()
	runtime, err := NewServerRuntime(db, AgentSessionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serveErr := make(chan error, 1)
	go func() { serveErr <- runtime.ServeListener(ctx, listener) }()
	baseURL := "http://" + listener.Addr().String()
	resp, err := http.Get(baseURL + "/")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("web status=%d", resp.StatusCode)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	wsURL := "ws://" + listener.Addr().String() + "/ws/agent"
	config, err := websocket.NewConfig(wsURL, baseURL)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := protocol.EncodeAgentMetadataPayload(protocol.AgentMetadataPayload{AgentID: "agent-ws", NodeID: "node-ws", Epoch: 1, Revision: 1, Items: []protocol.AgentMetadataItem{{Name: "region", Source: "env", Value: "east"}}})
	if err != nil {
		t.Fatal(err)
	}
	config.Header.Set("Authorization", "Bearer invalid-token")
	if status := rawWebSocketHandshakeStatus(t, listener.Addr().String(), listener.Addr().String(), baseURL, "invalid-token"); status != http.StatusForbidden {
		t.Fatalf("invalid Agent token handshake status = %d, want %d", status, http.StatusForbidden)
	}

	config.Header.Set("Authorization", "Bearer "+agentToken.Secret)
	ws, err := websocket.DialConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	if err := websocket.Message.Send(ws, frameBytes(t, protocol.FrameAgentHello, payload)); err != nil {
		t.Fatal(err)
	}
	var ackBytes []byte
	if err := websocket.Message.Receive(ws, &ackBytes); err != nil {
		t.Fatal(err)
	}
	ackFrame, err := protocol.NewDecoder(bytes.NewReader(ackBytes)).ReadFrame()
	if err != nil {
		t.Fatal(err)
	}
	ack, err := protocol.DecodeAgentMetadataAckPayload(ackFrame.Payload)
	if err != nil || !ack.Accepted {
		t.Fatalf("ack=%+v err=%v", ack, err)
	}

	request, _ := http.NewRequest(http.MethodGet, baseURL+"/api/v1/agents/agent-ws/metadata?includeStale=true", nil)
	request.Header.Set("Authorization", "Bearer "+login.Token)
	apiResponse, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(apiResponse.Body)
	_ = apiResponse.Body.Close()
	if apiResponse.StatusCode != http.StatusOK || !bytes.Contains(body, []byte(`"region"`)) {
		t.Fatalf("metadata api status=%d body=%s", apiResponse.StatusCode, body)
	}

	cancel()
	select {
	case err := <-serveErr:
		if err != nil && err != context.Canceled {
			t.Fatalf("serve error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("runtime did not stop")
	}
	var envelope map[string]any
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("invalid API JSON: %v", err)
	}
	if envelope["data"] == nil {
		t.Fatalf("metadata API response missing data: %v", envelope)
	}
}

func TestServerRuntimeAcceptsRealAgentWebSocketClient(t *testing.T) {
	db, err := storage.OpenSQLite(context.Background(), "file:runtime-agent-client?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	authService := auth.NewAuthService(db)
	user, err := authService.CreateUser(context.Background(), "agent-client-owner", "agent-client-pass", "user")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Agents().Create(context.Background(), storage.Agent{ID: "agent-client", Name: "agent-client", OwnerUserID: user.ID, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	credentials := auth.NewCredentialService(db)
	agentToken, err := credentials.Create(context.Background(), auth.CreateTokenInput{Type: storage.TokenTypeAgent, OwnerUserID: user.ID, AgentID: "agent-client"})
	if err != nil {
		t.Fatal(err)
	}
	_ = credentials.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	runtime, err := NewServerRuntime(db, AgentSessionConfig{}, RuntimeConfig{Security: config.SecurityConfig{AllowedHosts: []string{address}, AllowedOrigins: []string{"http://" + address}}})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serveErr := make(chan error, 1)
	go func() { serveErr <- runtime.ServeListener(ctx, listener) }()

	t.Setenv("TUNNELMESH_AGENT_REGION", "east")
	agentCtx, agentCancel := context.WithCancel(context.Background())
	agentErr := make(chan error, 1)
	go func() {
		agentErr <- agent.RunWebSocket(agentCtx, "ws://"+address+"/ws/agent", agentToken.Secret, "agent-client", "node-client", 1, agent.NewMetadataCollector([]config.MetadataSource{{Name: "region", Source: "env", Key: "TUNNELMESH_AGENT_REGION"}}), nil)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		metadata, getErr := db.Metadata().Get(context.Background(), "agent-client")
		if getErr == nil && metadata.AgentID == "agent-client" {
			if !bytes.Contains([]byte(metadata.Metadata), []byte(`"region"`)) {
				t.Fatalf("metadata=%s", metadata.Metadata)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("agent metadata not persisted: metadata=%+v err=%v", metadata, getErr)
		}
		time.Sleep(10 * time.Millisecond)
	}
	agentCancel()
	select {
	case err := <-agentErr:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("agent error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("agent did not stop")
	}
	cancel()
	select {
	case err := <-serveErr:
		if err != nil {
			t.Fatalf("server error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("server did not stop")
	}
}

func TestServerRuntimeBindsAgentTokenToEnabledOwner(t *testing.T) {
	db, err := storage.OpenSQLite(context.Background(), "file:runtime-agent-ownership?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	authService := auth.NewAuthService(db)
	owner, err := authService.CreateUser(context.Background(), "ownership-owner", "owner-pass", "user")
	if err != nil {
		t.Fatal(err)
	}
	other, err := authService.CreateUser(context.Background(), "ownership-other", "other-pass", "user")
	if err != nil {
		t.Fatal(err)
	}
	ownerLogin, err := authService.Login(context.Background(), owner.Username, "owner-pass")
	if err != nil {
		t.Fatal(err)
	}
	otherLogin, err := authService.Login(context.Background(), other.Username, "other-pass")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Agents().Create(context.Background(), storage.Agent{ID: "owned-agent", Name: "owned-agent", OwnerUserID: owner.ID, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := db.Agents().Create(context.Background(), storage.Agent{ID: "disabled-agent", Name: "disabled-agent", OwnerUserID: owner.ID, Enabled: false}); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewServerRuntime(db, AgentSessionConfig{}, RuntimeConfig{Security: config.SecurityConfig{AllowLegacyConnectionTokens: true}})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serveErr := make(chan error, 1)
	go func() { serveErr <- runtime.ServeListener(ctx, listener) }()
	defer func() {
		cancel()
		<-serveErr
	}()

	for _, tc := range []struct {
		name, token, agentID string
	}{
		{name: "unrelated owner", token: otherLogin.Token, agentID: "owned-agent"},
		{name: "disabled agent", token: ownerLogin.Token, agentID: "disabled-agent"},
		{name: "missing agent", token: ownerLogin.Token, agentID: "missing-agent"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wsURL := "ws://" + listener.Addr().String() + "/ws/agent"
			config, err := websocket.NewConfig(wsURL, "http://"+listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			config.Header.Set("Authorization", "Bearer "+tc.token)
			ws, err := websocket.DialConfig(config)
			if err != nil {
				t.Fatal(err)
			}
			defer ws.Close()
			payload, err := protocol.EncodeAgentMetadataPayload(protocol.AgentMetadataPayload{AgentID: tc.agentID, NodeID: "node", Epoch: 1, Revision: 1})
			if err != nil {
				t.Fatal(err)
			}
			if err := websocket.Message.Send(ws, frameBytes(t, protocol.FrameAgentHello, payload)); err != nil {
				t.Fatal(err)
			}
			_ = ws.SetReadDeadline(time.Now().Add(time.Second))
			var rejected []byte
			if err := websocket.Message.Receive(ws, &rejected); err == nil {
				t.Fatal("unauthorized Agent unexpectedly received an acknowledgement")
			}
		})
	}
}

func TestAgentTokenAuthenticationBoundaries(t *testing.T) {
	ctx := context.Background()
	db, err := storage.OpenSQLite(ctx, "file:runtime-agent-token-boundaries?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	authService := auth.NewAuthService(db)
	owner, err := authService.CreateUser(ctx, "token-boundary-owner", "owner-pass", "user")
	if err != nil {
		t.Fatal(err)
	}
	management, err := authService.Login(ctx, owner.Username, "owner-pass")
	if err != nil {
		t.Fatal(err)
	}
	for _, agentID := range []string{"agent-token-valid", "agent-token-other", "agent-token-disabled", "agent-token-expired", "agent-token-revoked"} {
		if err := db.Agents().Create(ctx, storage.Agent{ID: agentID, Name: agentID, OwnerUserID: owner.ID, Enabled: true}); err != nil {
			t.Fatal(err)
		}
	}
	credentials := auth.NewCredentialService(db)
	defer credentials.Close()
	createAgentToken := func(agentID string, expiresAt *time.Time) auth.CreatedToken {
		t.Helper()
		created, createErr := credentials.Create(ctx, auth.CreateTokenInput{Type: storage.TokenTypeAgent, OwnerUserID: owner.ID, AgentID: agentID, ExpiresAt: expiresAt})
		if createErr != nil {
			t.Fatal(createErr)
		}
		return created
	}
	valid := createAgentToken("agent-token-valid", nil)
	other := createAgentToken("agent-token-other", nil)
	disabled := createAgentToken("agent-token-disabled", nil)
	future := time.Now().UTC().Add(time.Hour)
	expired := createAgentToken("agent-token-expired", &future)
	revoked := createAgentToken("agent-token-revoked", nil)
	clientToken, err := credentials.Create(ctx, auth.CreateTokenInput{Type: storage.TokenTypeClient, OwnerUserID: owner.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := credentials.Revoke(ctx, revoked.TokenID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL().ExecContext(ctx, `UPDATE service_tokens SET expires_at=? WHERE id=?`, time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano), expired.TokenID); err != nil {
		t.Fatal(err)
	}
	disabledAgent, err := db.Agents().Get(ctx, "agent-token-disabled")
	if err != nil {
		t.Fatal(err)
	}
	disabledAgent.Enabled = false
	if err := db.Agents().Update(ctx, disabledAgent); err != nil {
		t.Fatal(err)
	}

	registrations := make(chan AgentRegistration, 1)
	runtime, err := NewServerRuntime(db, AgentSessionConfig{Authenticate: func(_ context.Context, registration AgentRegistration) error {
		select {
		case registrations <- registration:
		default:
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serverCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	serveErr := make(chan error, 1)
	go func() { serveErr <- runtime.ServeListener(serverCtx, listener) }()
	defer func() {
		cancel()
		<-serveErr
	}()

	tests := []struct {
		name       string
		token      string
		agentID    string
		queryToken string
		cookie     string
	}{
		{name: "missing token", agentID: "agent-token-valid"},
		{name: "management token", token: management.Token, agentID: "agent-token-valid"},
		{name: "client token", token: clientToken.Secret, agentID: "agent-token-valid"},
		{name: "wrong agent binding", token: other.Secret, agentID: "agent-token-valid"},
		{name: "disabled agent", token: disabled.Secret, agentID: "agent-token-disabled"},
		{name: "revoked token", token: revoked.Secret, agentID: "agent-token-revoked"},
		{name: "expired token", token: expired.Secret, agentID: "agent-token-expired"},
		{name: "query token", agentID: "agent-token-valid", queryToken: valid.Secret},
		{name: "cookie token", agentID: "agent-token-valid", cookie: "token=" + valid.Secret},
	}
	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if agentWebSocketAccepted(t, listener.Addr().String(), tc.token, tc.agentID, int64(i+1), tc.queryToken, tc.cookie) {
				t.Fatal("unauthorized Agent received a metadata acknowledgement")
			}
		})
	}
	if !agentWebSocketAccepted(t, listener.Addr().String(), valid.Secret, "agent-token-valid", 100, "", "") {
		t.Fatal("valid Agent token was rejected")
	}
	select {
	case registration := <-registrations:
		if registration.TokenID != valid.TokenID {
			t.Fatalf("registration TokenID = %q, want %q", registration.TokenID, valid.TokenID)
		}
	case <-time.After(time.Second):
		t.Fatal("valid Agent registration was not observed")
	}
}

func TestAgentInvalidCredentialRejectedBeforeUpgrade(t *testing.T) {
	ctx := context.Background()
	db, err := storage.OpenSQLite(ctx, "file:runtime-agent-preupgrade-auth?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	authService := auth.NewAuthService(db)
	owner, err := authService.CreateUser(ctx, "preupgrade-owner", "owner-pass", "user")
	if err != nil {
		t.Fatal(err)
	}
	management, err := authService.Login(ctx, owner.Username, "owner-pass")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Agents().Create(ctx, storage.Agent{ID: "preupgrade-agent", Name: "preupgrade-agent", OwnerUserID: owner.ID, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	credentials := auth.NewCredentialService(db)
	agentToken, err := credentials.Create(ctx, auth.CreateTokenInput{Type: storage.TokenTypeAgent, OwnerUserID: owner.ID, AgentID: "preupgrade-agent"})
	if err != nil {
		t.Fatal(err)
	}
	clientToken, err := credentials.Create(ctx, auth.CreateTokenInput{Type: storage.TokenTypeClient, OwnerUserID: owner.ID})
	if err != nil {
		t.Fatal(err)
	}
	revokedToken, err := credentials.Create(ctx, auth.CreateTokenInput{Type: storage.TokenTypeAgent, OwnerUserID: owner.ID, AgentID: "preupgrade-agent"})
	if err != nil {
		t.Fatal(err)
	}
	if err := credentials.Revoke(ctx, revokedToken.TokenID); err != nil {
		t.Fatal(err)
	}
	_ = credentials.Close()

	runtime, err := NewServerRuntime(db, AgentSessionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serverCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	serveErr := make(chan error, 1)
	go func() { serveErr <- runtime.ServeListener(serverCtx, listener) }()
	defer func() {
		cancel()
		<-serveErr
	}()
	address := listener.Addr().String()

	for _, tc := range []struct {
		name  string
		token string
	}{
		{name: "arbitrary token", token: "not-a-valid-token"},
		{name: "client token", token: clientToken.Secret},
		{name: "revoked Agent token", token: revokedToken.Secret},
		{name: "management token with legacy disabled", token: management.Token},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if status := rawWebSocketHandshakeStatus(t, address, address, "http://"+address, tc.token); status != http.StatusForbidden {
				t.Fatalf("invalid credential handshake status = %d, want %d", status, http.StatusForbidden)
			}
		})
	}
	if status := rawWebSocketHandshakeStatus(t, address, address, "http://"+address, agentToken.Secret); status != http.StatusSwitchingProtocols {
		t.Fatalf("valid Agent credential handshake status = %d, want %d", status, http.StatusSwitchingProtocols)
	}
}

func TestAgentWebSocketStrictOriginWithoutAllowlist(t *testing.T) {
	ctx := context.Background()
	db, err := storage.OpenSQLite(ctx, "file:runtime-agent-strict-origin?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	authService := auth.NewAuthService(db)
	owner, err := authService.CreateUser(ctx, "strict-origin-owner", "owner-pass", "user")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Agents().Create(ctx, storage.Agent{ID: "strict-origin-agent", Name: "strict-origin-agent", OwnerUserID: owner.ID, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	credentials := auth.NewCredentialService(db)
	created, err := credentials.Create(ctx, auth.CreateTokenInput{Type: storage.TokenTypeAgent, OwnerUserID: owner.ID, AgentID: "strict-origin-agent"})
	if err != nil {
		t.Fatal(err)
	}
	_ = credentials.Close()

	runtime, err := NewServerRuntime(db, AgentSessionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serverCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	serveErr := make(chan error, 1)
	go func() { serveErr <- runtime.ServeListener(serverCtx, listener) }()
	defer func() {
		cancel()
		<-serveErr
	}()
	address := listener.Addr().String()

	for _, origin := range []string{
		"ftp://" + address,
		"relative-origin",
		"http://" + address + "/path",
		"http://" + address + "?query=value",
		"http://" + address + "#fragment",
	} {
		t.Run(origin, func(t *testing.T) {
			if status := rawWebSocketHandshakeStatus(t, address, address, origin, created.Secret); status != http.StatusForbidden {
				t.Fatalf("invalid Origin %q handshake status = %d, want %d", origin, status, http.StatusForbidden)
			}
		})
	}
	for _, origin := range []string{"http://" + address, "https://" + address} {
		t.Run(origin, func(t *testing.T) {
			if status := rawWebSocketHandshakeStatus(t, address, address, origin, created.Secret); status != http.StatusSwitchingProtocols {
				t.Fatalf("valid Origin %q handshake status = %d, want %d", origin, status, http.StatusSwitchingProtocols)
			}
		})
	}
}

func TestAgentWebSocketClosesConnectionWhenHelloTimesOut(t *testing.T) {
	address, token, stop := startAuthenticatedAgentWebSocketServer(t, "hello-timeout")
	defer stop()
	wsURL := "ws://" + address + "/ws/agent"
	wsConfig, err := websocket.NewConfig(wsURL, "http://"+address)
	if err != nil {
		t.Fatal(err)
	}
	wsConfig.Header.Set("Authorization", "Bearer "+token)
	ws, err := websocket.DialConfig(wsConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()

	started := time.Now()
	_ = ws.SetReadDeadline(started.Add(3 * time.Second))
	var payload []byte
	if err := websocket.Message.Receive(ws, &payload); err == nil {
		t.Fatal("connection without Agent Hello remained open")
	}
	if elapsed := time.Since(started); elapsed >= 2*time.Second {
		t.Fatalf("connection without Agent Hello closed after %v, want before 2s", elapsed)
	}
}

func TestAgentWebSocketRejectsOversizedFirstFrameBeforeReadingPayload(t *testing.T) {
	address, token, stop := startAuthenticatedAgentWebSocketServer(t, "oversized-first-frame")
	defer stop()
	conn, status := rawWebSocketUpgrade(t, address, address, "http://"+address, token)
	defer conn.Close()
	if status != http.StatusSwitchingProtocols {
		t.Fatalf("handshake status = %d, want %d", status, http.StatusSwitchingProtocols)
	}

	var frameHeader [14]byte
	frameHeader[0] = 0x82
	frameHeader[1] = 0xff
	binary.BigEndian.PutUint64(frameHeader[2:10], uint64(protocol.MaxPayload+16+1))
	copy(frameHeader[10:], []byte{1, 2, 3, 4})
	started := time.Now()
	if _, err := conn.Write(frameHeader[:]); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(started.Add(750 * time.Millisecond))
	_, readErr := io.ReadAll(conn)
	if netErr, ok := readErr.(net.Error); ok && netErr.Timeout() {
		t.Fatalf("server waited for oversized first-frame payload instead of rejecting its declared length: %v", readErr)
	}
	if elapsed := time.Since(started); elapsed >= 600*time.Millisecond {
		t.Fatalf("oversized first frame closed after %v, want immediate rejection", elapsed)
	}
}

func TestAgentWebSocketHostAndOriginAllowlist(t *testing.T) {
	ctx := context.Background()
	db, err := storage.OpenSQLite(ctx, "file:runtime-agent-allowlist?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	authService := auth.NewAuthService(db)
	owner, err := authService.CreateUser(ctx, "allowlist-owner", "owner-pass", "user")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Agents().Create(ctx, storage.Agent{ID: "allowlist-agent", Name: "allowlist-agent", OwnerUserID: owner.ID, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	credentials := auth.NewCredentialService(db)
	created, err := credentials.Create(ctx, auth.CreateTokenInput{Type: storage.TokenTypeAgent, OwnerUserID: owner.ID, AgentID: "allowlist-agent"})
	if err != nil {
		t.Fatal(err)
	}
	_ = credentials.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	runtime, err := NewServerRuntime(db, AgentSessionConfig{}, RuntimeConfig{Security: config.SecurityConfig{
		AllowedHosts:   []string{address, "EXAMPLE.COM"},
		AllowedOrigins: []string{"http://" + address},
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	serverCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	serveErr := make(chan error, 1)
	go func() { serveErr <- runtime.ServeListener(serverCtx, listener) }()
	defer func() {
		cancel()
		<-serveErr
	}()

	if !agentWebSocketAcceptedFromOrigin(t, address, created.Secret, "allowlist-agent", 1, "http://"+address) {
		t.Fatal("Agent dialer Origin matching the Server URL was rejected")
	}
	if agentWebSocketAcceptedFromOrigin(t, address, created.Secret, "allowlist-agent", 2, "https://evil.example") {
		t.Fatal("unlisted Origin completed an Agent WebSocket session")
	}
	if status := rawWebSocketHandshakeStatus(t, address, "evil.example", "http://"+address, created.Secret); status != http.StatusForbidden {
		t.Fatalf("unlisted Host handshake status = %d, want %d", status, http.StatusForbidden)
	}
	if status := rawWebSocketHandshakeStatus(t, address, "example.com@evil.example", "http://"+address, created.Secret); status == http.StatusSwitchingProtocols {
		t.Fatal("malicious Host completed a WebSocket upgrade")
	}
	if status := rawWebSocketHandshakeStatus(t, address, "example.com", "http://"+address, created.Secret); status != http.StatusSwitchingProtocols {
		t.Fatalf("case-normalized allowed Host status = %d, want %d", status, http.StatusSwitchingProtocols)
	}
}

func TestAgentLegacyManagementTokenRequiresMigrationFlagAndAuditsWarning(t *testing.T) {
	ctx := context.Background()
	db, err := storage.OpenSQLite(ctx, "file:runtime-agent-legacy-token?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	authService := auth.NewAuthService(db)
	owner, err := authService.CreateUser(ctx, "legacy-owner", "owner-pass", "user")
	if err != nil {
		t.Fatal(err)
	}
	login, err := authService.Login(ctx, owner.Username, "owner-pass")
	if err != nil {
		t.Fatal(err)
	}
	other, err := authService.CreateUser(ctx, "legacy-other", "other-pass", "user")
	if err != nil {
		t.Fatal(err)
	}
	otherLogin, err := authService.Login(ctx, other.Username, "other-pass")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Agents().Create(ctx, storage.Agent{ID: "legacy-agent", Name: "legacy-agent", OwnerUserID: owner.ID, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := db.Agents().Create(ctx, storage.Agent{ID: "legacy-disabled-agent", Name: "legacy-disabled-agent", OwnerUserID: owner.ID, Enabled: false}); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := NewServerRuntime(db, AgentSessionConfig{}, RuntimeConfig{Security: config.SecurityConfig{AllowLegacyConnectionTokens: true}})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	var logs bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })
	serverCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	serveErr := make(chan error, 1)
	go func() { serveErr <- runtime.ServeListener(serverCtx, listener) }()
	defer func() {
		cancel()
		<-serveErr
	}()
	address := listener.Addr().String()
	if status := rawWebSocketHandshakeStatus(t, address, address, "http://"+address, "invalid-legacy-token"); status != http.StatusForbidden {
		t.Fatalf("invalid legacy credential handshake status = %d, want %d", status, http.StatusForbidden)
	}
	if status := rawWebSocketHandshakeStatus(t, address, address, "http://"+address, login.Token); status != http.StatusSwitchingProtocols {
		t.Fatalf("valid legacy credential handshake status = %d, want %d", status, http.StatusSwitchingProtocols)
	}

	if !agentWebSocketAccepted(t, listener.Addr().String(), login.Token, "legacy-agent", 1, "", "") {
		t.Fatal("legacy management token was rejected while migration flag was enabled")
	}
	if agentWebSocketAccepted(t, listener.Addr().String(), otherLogin.Token, "legacy-agent", 2, "", "") {
		t.Fatal("legacy management token from another owner was accepted")
	}
	if agentWebSocketAccepted(t, listener.Addr().String(), login.Token, "legacy-disabled-agent", 1, "", "") {
		t.Fatal("legacy management token connected a disabled Agent")
	}
	if !strings.Contains(logs.String(), "deprecated_connection_token") {
		t.Fatalf("warning log = %q, want deprecated_connection_token", logs.String())
	}
	if strings.Contains(logs.String(), login.Token) {
		t.Fatal("warning log leaked raw management token")
	}
	audits, err := db.Audits().List(ctx, storage.AuditFilter{}, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, audit := range audits.Items {
		if audit.Action != "deprecated_connection_token" {
			continue
		}
		found = true
		if audit.ActorUserID != owner.ID || audit.ResourceID != "legacy-agent" {
			t.Fatalf("legacy audit = %+v", audit)
		}
		if strings.Contains(audit.Details, login.Token) {
			t.Fatal("legacy audit leaked raw management token")
		}
	}
	if !found {
		t.Fatalf("deprecated connection audit missing: %+v", audits.Items)
	}
}

func agentWebSocketAccepted(t *testing.T, address, token, agentID string, epoch int64, queryToken, cookie string) bool {
	t.Helper()
	return agentWebSocketAcceptedWithOptions(t, address, token, agentID, epoch, queryToken, cookie, "http://"+address)
}

func agentWebSocketAcceptedFromOrigin(t *testing.T, address, token, agentID string, epoch int64, origin string) bool {
	t.Helper()
	return agentWebSocketAcceptedWithOptions(t, address, token, agentID, epoch, "", "", origin)
}

func agentWebSocketAcceptedWithOptions(t *testing.T, address, token, agentID string, epoch int64, queryToken, cookie, origin string) bool {
	t.Helper()
	location := &url.URL{Scheme: "ws", Host: address, Path: "/ws/agent"}
	if queryToken != "" {
		location.RawQuery = "token=" + url.QueryEscape(queryToken)
	}
	config, err := websocket.NewConfig(location.String(), origin)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		config.Header.Set("Authorization", "Bearer "+token)
	}
	if cookie != "" {
		config.Header.Set("Cookie", cookie)
	}
	ws, err := websocket.DialConfig(config)
	if err != nil {
		return false
	}
	defer ws.Close()
	payload, err := protocol.EncodeAgentMetadataPayload(protocol.AgentMetadataPayload{AgentID: agentID, NodeID: "node-token-boundary", Epoch: epoch, Revision: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := websocket.Message.Send(ws, frameBytes(t, protocol.FrameAgentHello, payload)); err != nil {
		return false
	}
	_ = ws.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	var ackBytes []byte
	if err := websocket.Message.Receive(ws, &ackBytes); err != nil {
		return false
	}
	frame, err := protocol.NewDecoder(bytes.NewReader(ackBytes)).ReadFrame()
	if err != nil || frame.Type != protocol.FrameAgentMetadataAck {
		return false
	}
	ack, err := protocol.DecodeAgentMetadataAckPayload(frame.Payload)
	return err == nil && ack.Accepted
}

func startAuthenticatedAgentWebSocketServer(t *testing.T, name string) (string, string, func()) {
	t.Helper()
	ctx := context.Background()
	db, err := storage.OpenSQLite(ctx, "file:runtime-agent-"+name+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	authService := auth.NewAuthService(db)
	owner, err := authService.CreateUser(ctx, name+"-owner", "owner-pass", "user")
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	agentID := name + "-agent"
	if err := db.Agents().Create(ctx, storage.Agent{ID: agentID, Name: agentID, OwnerUserID: owner.ID, Enabled: true}); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	credentials := auth.NewCredentialService(db)
	created, err := credentials.Create(ctx, auth.CreateTokenInput{Type: storage.TokenTypeAgent, OwnerUserID: owner.ID, AgentID: agentID})
	if err != nil {
		_ = credentials.Close()
		_ = db.Close()
		t.Fatal(err)
	}
	_ = credentials.Close()
	runtime, err := NewServerRuntime(db, AgentSessionConfig{})
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		_ = runtime.Close()
		_ = db.Close()
		t.Fatal(err)
	}
	serverCtx, cancel := context.WithCancel(ctx)
	serveErr := make(chan error, 1)
	go func() { serveErr <- runtime.ServeListener(serverCtx, listener) }()
	return listener.Addr().String(), created.Secret, func() {
		cancel()
		<-serveErr
		_ = runtime.Close()
		_ = db.Close()
	}
}

func rawWebSocketHandshakeStatus(t *testing.T, address, host, origin, token string) int {
	t.Helper()
	conn, status := rawWebSocketUpgrade(t, address, host, origin, token)
	_ = conn.Close()
	return status
}

func rawWebSocketUpgrade(t *testing.T, address, host, origin, token string) (net.Conn, int) {
	t.Helper()
	conn, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	request := fmt.Sprintf("GET /ws/agent HTTP/1.1\r\nHost: %s\r\nOrigin: %s\r\nAuthorization: Bearer %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\n", host, origin, token)
	if _, err := conn.Write([]byte(request)); err != nil {
		_ = conn.Close()
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	response, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodGet})
	if err != nil {
		_ = conn.Close()
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Time{})
	return conn, response.StatusCode
}

func TestRealAgentReconnectsWithNewEpochAfterServerClosesSession(t *testing.T) {
	db, err := storage.OpenSQLite(context.Background(), "file:runtime-agent-reconnect?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	authService := auth.NewAuthService(db)
	user, err := authService.CreateUser(context.Background(), "reconnect-owner", "reconnect-pass", "user")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Agents().Create(context.Background(), storage.Agent{ID: "reconnect-agent", Name: "reconnect-agent", OwnerUserID: user.ID, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	credentials := auth.NewCredentialService(db)
	agentToken, err := credentials.Create(context.Background(), auth.CreateTokenInput{Type: storage.TokenTypeAgent, OwnerUserID: user.ID, AgentID: "reconnect-agent"})
	if err != nil {
		t.Fatal(err)
	}
	_ = credentials.Close()
	runtime, err := NewServerRuntime(db, AgentSessionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serverCtx, serverCancel := context.WithCancel(context.Background())
	defer serverCancel()
	serveErr := make(chan error, 1)
	go func() { serveErr <- runtime.ServeListener(serverCtx, listener) }()

	t.Setenv("TUNNELMESH_RECONNECT_REGION", "east")
	agentCtx, agentCancel := context.WithCancel(context.Background())
	agentErr := make(chan error, 1)
	go func() {
		agentErr <- agent.RunWebSocketWithOptions(agentCtx, "ws://"+listener.Addr().String()+"/ws/agent", agentToken.Secret, "reconnect-agent", "node-reconnect", 1, agent.NewMetadataCollector([]config.MetadataSource{{Name: "region", Source: "env", Key: "TUNNELMESH_RECONNECT_REGION"}}), nil, agent.WebSocketRunOptions{BaseBackoff: 10 * time.Millisecond, MaxBackoff: 20 * time.Millisecond, Rand: rand.New(rand.NewSource(1))})
	}()
	waitForEpoch := func(want int64) storage.AgentRuntimeMetadata {
		deadline := time.Now().Add(2 * time.Second)
		for {
			metadata, getErr := db.Metadata().Get(context.Background(), "reconnect-agent")
			if getErr == nil && metadata.Epoch >= want {
				return metadata
			}
			if time.Now().After(deadline) {
				t.Fatalf("metadata epoch=%d err=%v, want >=%d", metadata.Epoch, getErr, want)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	first := waitForEpoch(1)
	if first.Epoch != 1 {
		t.Fatalf("first epoch=%d", first.Epoch)
	}
	runtime.AgentSessions.Remove("reconnect-agent")
	second := waitForEpoch(2)
	if second.Epoch != 2 || second.Stale {
		t.Fatalf("reconnect metadata=%+v", second)
	}
	agentCancel()
	select {
	case err := <-agentErr:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("agent error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("agent did not stop")
	}
	serverCancel()
	select {
	case err := <-serveErr:
		if err != nil {
			t.Fatalf("server error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("server did not stop")
	}
}

func TestRuntimeRejectsAgentConnectionWhenLeaseRegistrationFails(t *testing.T) {
	ctx := context.Background()
	db, err := storage.OpenSQLite(ctx, "file:runtime-agent-lease-reject?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	authService := auth.NewAuthService(db)
	owner, err := authService.CreateUser(ctx, "lease-reject-owner", "owner-pass", "user")
	if err != nil {
		t.Fatal(err)
	}
	agentID := "lease-reject-agent"
	if err := db.Agents().Create(ctx, storage.Agent{ID: agentID, Name: agentID, OwnerUserID: owner.ID, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	credentials := auth.NewCredentialService(db)
	token, err := credentials.Create(ctx, auth.CreateTokenInput{Type: storage.TokenTypeAgent, OwnerUserID: owner.ID, AgentID: agentID})
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := NewServerRuntime(db, AgentSessionConfig{}, RuntimeConfig{
		ConnectionRegistry: &recordingConnectionRegistry{registerErr: registry.ErrLeaseHeld},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serverCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	serveErr := make(chan error, 1)
	go func() { serveErr <- runtime.ServeListener(serverCtx, listener) }()
	defer func() {
		cancel()
		<-serveErr
	}()
	if agentWebSocketAccepted(t, listener.Addr().String(), token.Secret, agentID, 1, "", "") {
		t.Fatal("Agent connection was accepted when lease registration failed")
	}
	if sessions := runtime.AgentSessions.List(agentID); len(sessions) != 0 {
		t.Fatalf("sessions = %#v, want none", sessions)
	}
}

func frameBytes(t *testing.T, typ protocol.FrameType, payload []byte) []byte {
	t.Helper()
	var frame bytes.Buffer
	if err := protocol.NewEncoder(&frame).WriteFrame(protocol.Frame{Version: protocol.CurrentVersion, Type: typ, Payload: payload}); err != nil {
		t.Fatal(err)
	}
	return frame.Bytes()
}
