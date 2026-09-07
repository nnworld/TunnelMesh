package e2e

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"golang.org/x/net/websocket"

	"github.com/tunnelmesh/tunnelmesh/internal/auth"
	"github.com/tunnelmesh/tunnelmesh/internal/config"
	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
	"github.com/tunnelmesh/tunnelmesh/internal/server"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

func TestScopedTokenConnectionsAndRotation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fixture := fmt.Sprintf("%d", time.Now().UnixNano())
	db, err := storage.OpenSQLite(ctx, "file:e2e-scoped-token-"+fixture+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	authService := auth.NewAuthService(db)
	owner, err := authService.CreateUser(ctx, "e2e-owner-"+fixture, "e2e-password", "user")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Agents().Create(ctx, storage.Agent{ID: "e2e-agent", Name: "e2e-agent", OwnerUserID: owner.ID, Enabled: true}); err != nil {
		t.Fatal(err)
	}

	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	go func() {
		for {
			c, e := echo.Accept()
			if e != nil {
				return
			}
			go func(conn net.Conn) { defer conn.Close(); _, _ = io.Copy(conn, conn) }(c)
		}
	}()
	echoHost, echoPort, _ := net.SplitHostPort(echo.Addr().String())
	var port int
	_, _ = fmt.Sscanf(echoPort, "%d", &port)
	now := time.Now().UTC()
	if err := db.Policies().Create(ctx, storage.AgentPolicy{ID: "e2e-policy", AgentID: "e2e-agent", TargetHost: echoHost, TargetPort: port, Protocol: "tcp", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	credentials := auth.NewCredentialService(db)
	defer credentials.Close()
	agentToken, err := credentials.Create(ctx, auth.CreateTokenInput{Type: storage.TokenTypeAgent, OwnerUserID: owner.ID, AgentID: "e2e-agent"})
	if err != nil {
		t.Fatal(err)
	}
	clientToken, err := credentials.Create(ctx, auth.CreateTokenInput{Type: storage.TokenTypeClient, OwnerUserID: owner.ID, Scope: auth.TokenScope{AgentIDs: []string{"e2e-agent"}, Protocols: []string{"tcp"}, TargetCIDRs: []string{"127.0.0.0/8"}, TargetPorts: []int{port}}})
	if err != nil {
		t.Fatal(err)
	}

	runtime, err := server.NewServerRuntime(db, server.AgentSessionConfig{MetadataTTL: time.Minute}, server.RuntimeConfig{Security: config.SecurityConfig{}})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- runtime.ServeListener(ctx, listener) }()
	defer func() {
		cancel()
		_ = listener.Close()
		select {
		case err := <-serveErr:
			if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, net.ErrClosed) {
				t.Errorf("runtime cleanup error = %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Error("runtime cleanup timed out")
		}
	}()
	address := listener.Addr().String()

	agentConfig, _ := websocket.NewConfig("ws://"+address+"/ws/agent", "http://"+address)
	agentConfig.Header.Set("Authorization", "Bearer "+agentToken.Secret)
	agentConn, err := websocket.DialConfig(agentConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer agentConn.Close()
	hello, _ := protocol.EncodeAgentMetadataPayload(protocol.AgentMetadataPayload{AgentID: "e2e-agent", NodeID: "e2e-node", Epoch: 1, Revision: 1, ReportedAt: now})
	if err := sendFrame(agentConn, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameAgentHello, Payload: hello}); err != nil {
		t.Fatal(err)
	}
	if frame := receiveFrame(t, agentConn); frame.Type != protocol.FrameAgentMetadataAck {
		t.Fatalf("agent hello ack = %+v", frame)
	}

	clientConfig, _ := websocket.NewConfig("ws://"+address+"/ws/client", "http://"+address)
	clientConfig.Header.Set("Authorization", "Bearer "+clientToken.Secret)
	clientConn, err := websocket.DialConfig(clientConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer clientConn.Close()
	openUDP, _ := protocol.EncodeStreamOpenPayload(protocol.StreamOpenPayload{AgentID: "e2e-agent", Protocol: "udp", TargetHost: echoHost, TargetPort: port})
	if err := sendFrame(clientConn, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: 7, Payload: openUDP}); err != nil {
		t.Fatal(err)
	}
	if frame := receiveFrame(t, clientConn); frame.Type != protocol.FrameReset || frame.StreamID != 7 {
		t.Fatalf("denied UDP frame = %+v", frame)
	}
	if err := sendFrame(clientConn, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FramePing, Payload: []byte("alive")}); err != nil {
		t.Fatal(err)
	}
	if frame := receiveFrame(t, clientConn); frame.Type != protocol.FramePong {
		t.Fatalf("client pong = %+v", frame)
	}

	openTCP, _ := protocol.EncodeStreamOpenPayload(protocol.StreamOpenPayload{AgentID: "e2e-agent", Protocol: "tcp", TargetHost: echoHost, TargetPort: port})
	if err := sendFrame(clientConn, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: 9, Payload: openTCP}); err != nil {
		t.Fatal(err)
	}
	agentOpen := receiveFrame(t, agentConn)
	if agentOpen.Type != protocol.FrameOpenStream {
		t.Fatalf("agent open = %+v", agentOpen)
	}
	target, err := net.DialTimeout("tcp", echo.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	if err := sendFrame(clientConn, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: 9, Payload: []byte("hello")}); err != nil {
		t.Fatal(err)
	}
	if frame := receiveFrame(t, agentConn); frame.Type != protocol.FrameData {
		t.Fatalf("agent data = %+v", frame)
	} else if _, err := target.Write(frame.Payload); err != nil {
		t.Fatal(err)
	}
	_ = target.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 5)
	if _, err := io.ReadFull(target, buf); err != nil {
		t.Fatal(err)
	}
	if err := sendFrame(agentConn, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: agentOpen.StreamID, Payload: buf}); err != nil {
		t.Fatal(err)
	}
	if frame := receiveFrame(t, clientConn); frame.Type != protocol.FrameData || string(frame.Payload) != "hello" {
		t.Fatalf("client echo = %+v", frame)
	}

	if _, err := credentials.Rotate(ctx, clientToken.TokenID); err != nil {
		t.Fatal(err)
	}
	if err := sendFrame(clientConn, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: 11, Payload: openTCP}); err != nil {
		t.Fatal(err)
	}
	if frame := receiveFrame(t, clientConn); frame.Type != protocol.FrameReset || frame.StreamID != 11 {
		t.Fatalf("rotated token frame = %+v", frame)
	}

	page, err := db.Audits().List(ctx, "", 200)
	if err != nil {
		t.Fatal(err)
	}
	for _, audit := range page.Items {
		if bytes.Contains([]byte(audit.Details), []byte(agentToken.Secret)) || bytes.Contains([]byte(audit.Details), []byte(clientToken.Secret)) {
			t.Fatalf("audit leaked token secret: %+v", audit)
		}
	}
}

func sendFrame(conn *websocket.Conn, frame protocol.Frame) error {
	var b bytes.Buffer
	if err := protocol.Encode(&b, frame); err != nil {
		return err
	}
	_ = conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	err := websocket.Message.Send(conn, b.Bytes())
	_ = conn.SetWriteDeadline(time.Time{})
	return err
}
func receiveFrame(t *testing.T, conn *websocket.Conn) protocol.Frame {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	var raw []byte
	if err := websocket.Message.Receive(conn, &raw); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Time{})
	frame, err := protocol.NewDecoder(bytes.NewReader(raw)).ReadFrame()
	if err != nil {
		t.Fatal(err)
	}
	return frame
}
