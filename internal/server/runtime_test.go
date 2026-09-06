package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
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
	runtime, err := NewServerRuntime(db, AgentSessionConfig{})
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
	runtime, err := NewServerRuntime(db, AgentSessionConfig{Authenticate: func(ctx context.Context, registration AgentRegistration) error {
		if registration.Token == "" {
			return ErrAuthentication
		}
		_, err := authService.ValidateToken(ctx, registration.Token)
		return err
	}})
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
	runtime, err := NewServerRuntime(db, AgentSessionConfig{Authenticate: func(ctx context.Context, registration AgentRegistration) error {
		_, err := authService.ValidateToken(ctx, registration.Token)
		return err
	}})
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

func frameBytes(t *testing.T, typ protocol.FrameType, payload []byte) []byte {
	t.Helper()
	var frame bytes.Buffer
	if err := protocol.NewEncoder(&frame).WriteFrame(protocol.Frame{Version: protocol.CurrentVersion, Type: typ, Payload: payload}); err != nil {
		t.Fatal(err)
	}
	return frame.Bytes()
}
