package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math/rand"
	"net"
	"net/http"
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
	runtime, err := NewServerRuntime(db, AgentSessionConfig{})
	if err != nil {
		t.Fatal(err)
	}
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

	config.Header.Set("Authorization", "Bearer "+login.Token)
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
	login, err := authService.Login(context.Background(), user.Username, "agent-client-pass")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Agents().Create(context.Background(), storage.Agent{ID: "agent-client", Name: "agent-client", OwnerUserID: user.ID, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewServerRuntime(db, AgentSessionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serveErr := make(chan error, 1)
	go func() { serveErr <- runtime.ServeListener(ctx, listener) }()

	t.Setenv("TUNNELMESH_AGENT_REGION", "east")
	agentCtx, agentCancel := context.WithCancel(context.Background())
	agentErr := make(chan error, 1)
	go func() {
		agentErr <- agent.RunWebSocket(agentCtx, "ws://"+listener.Addr().String()+"/ws/agent", login.Token, "agent-client", "node-client", 1, agent.NewMetadataCollector([]config.MetadataSource{{Name: "region", Source: "env", Key: "TUNNELMESH_AGENT_REGION"}}), nil)
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
	login, err := authService.Login(context.Background(), user.Username, "reconnect-pass")
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := NewServerRuntime(db, AgentSessionConfig{})
	if err != nil {
		t.Fatal(err)
	}
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
		agentErr <- agent.RunWebSocketWithOptions(agentCtx, "ws://"+listener.Addr().String()+"/ws/agent", login.Token, "reconnect-agent", "node-reconnect", 1, agent.NewMetadataCollector([]config.MetadataSource{{Name: "region", Source: "env", Key: "TUNNELMESH_RECONNECT_REGION"}}), nil, agent.WebSocketRunOptions{BaseBackoff: 10 * time.Millisecond, MaxBackoff: 20 * time.Millisecond, Rand: rand.New(rand.NewSource(1))})
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
