package server

import (
	"bufio"
	"bytes"
	"context"
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

	"github.com/tunnelmesh/tunnelmesh/internal/agent"
	"github.com/tunnelmesh/tunnelmesh/internal/auth"
	"github.com/tunnelmesh/tunnelmesh/internal/config"
	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

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
	invalidWS, err := websocket.DialConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := websocket.Message.Send(invalidWS, frameBytes(t, protocol.FrameAgentHello, payload)); err != nil {
		t.Fatal(err)
	}
	_ = invalidWS.SetReadDeadline(time.Now().Add(time.Second))
	var rejected []byte
	if err := websocket.Message.Receive(invalidWS, &rejected); err == nil {
		t.Fatal("invalid API token unexpectedly received an acknowledgement")
	}
	_ = invalidWS.Close()

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
	audits, err := db.Audits().List(ctx, "", 100)
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

func rawWebSocketHandshakeStatus(t *testing.T, address, host, origin, token string) int {
	t.Helper()
	conn, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	request := fmt.Sprintf("GET /ws/agent HTTP/1.1\r\nHost: %s\r\nOrigin: %s\r\nAuthorization: Bearer %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\n", host, origin, token)
	if _, err := conn.Write([]byte(request)); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	response, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodGet})
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	return response.StatusCode
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

func frameBytes(t *testing.T, typ protocol.FrameType, payload []byte) []byte {
	t.Helper()
	var frame bytes.Buffer
	if err := protocol.NewEncoder(&frame).WriteFrame(protocol.Frame{Version: protocol.CurrentVersion, Type: typ, Payload: payload}); err != nil {
		t.Fatal(err)
	}
	return frame.Bytes()
}
