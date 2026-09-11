package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/agent"
	"github.com/tunnelmesh/tunnelmesh/internal/auth"
	"github.com/tunnelmesh/tunnelmesh/internal/client"
	"github.com/tunnelmesh/tunnelmesh/internal/config"
	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
	"github.com/tunnelmesh/tunnelmesh/internal/server"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

func TestAgentRunCommandForwardsTCPUDPAndHTTPThroughRealDispatcher(t *testing.T) {
	ctx := context.Background()
	tcpListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tcpListener.Close()
	tcpPort := tcpListener.Addr().(*net.TCPAddr).Port
	udpTarget, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer udpTarget.Close()
	udpPort := udpTarget.LocalAddr().(*net.UDPAddr).Port
	httpTarget := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = io.WriteString(writer, "agent-http-response")
	}))
	defer httpTarget.Close()
	httpHost, httpPortText, err := net.SplitHostPort(strings.TrimPrefix(httpTarget.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	httpPort, err := strconv.Atoi(httpPortText)
	if err != nil {
		t.Fatal(err)
	}

	db, err := storage.OpenSQLite(ctx, "file:agent-cli-runtime?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	authService := auth.NewAuthService(db)
	owner, err := authService.CreateUser(ctx, "agent-cli-owner", "agent-cli-password", "user")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Agents().Create(ctx, storage.Agent{ID: "agent-cli", Name: "agent-cli", OwnerUserID: owner.ID, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for i, policy := range []storage.AgentPolicy{
		{ID: "agent-cli-tcp", AgentID: "agent-cli", TargetHost: "127.0.0.1", TargetPort: tcpPort, Protocol: "tcp", CreatedAt: now, UpdatedAt: now},
		{ID: "agent-cli-udp", AgentID: "agent-cli", TargetHost: "127.0.0.1", TargetPort: udpPort, Protocol: "udp", CreatedAt: now, UpdatedAt: now},
		{ID: "agent-cli-http", AgentID: "agent-cli", TargetHost: httpHost, TargetPort: httpPort, Protocol: "http", CreatedAt: now, UpdatedAt: now},
	} {
		if err := db.Policies().Create(ctx, policy); err != nil {
			t.Fatalf("create policy %d: %v", i, err)
		}
	}
	credentials := auth.NewCredentialService(db)
	agentToken, err := credentials.Create(ctx, auth.CreateTokenInput{Type: storage.TokenTypeAgent, OwnerUserID: owner.ID, AgentID: "agent-cli"})
	if err != nil {
		t.Fatal(err)
	}
	clientToken, err := credentials.Create(ctx, auth.CreateTokenInput{Type: storage.TokenTypeClient, OwnerUserID: owner.ID, Scope: auth.TokenScope{AgentIDs: []string{"agent-cli"}, Protocols: []string{"tcp", "udp", "http"}, TargetPorts: []int{tcpPort, udpPort, httpPort}}})
	if err != nil {
		t.Fatal(err)
	}
	_ = credentials.Close()

	runtime, err := server.NewServerRuntime(db, server.AgentSessionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	runtimeListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	runtimeCtx, stopRuntime := context.WithCancel(ctx)
	defer stopRuntime()
	runtimeDone := make(chan error, 1)
	go func() { runtimeDone <- runtime.ServeListener(runtimeCtx, runtimeListener) }()

	configPath := filepath.Join(t.TempDir(), "agent.yaml")
	configBody := fmt.Sprintf("mode: local\nnode:\n  id: agent-cli-node\nagent:\n  instance_id: agent-cli-instance\n  server_url: ws://%s/ws/agent\n  id: agent-cli\n  token: %s\n", runtimeListener.Addr().String(), agentToken.Secret)
	if err := os.WriteFile(configPath, []byte(configBody), 0o600); err != nil {
		t.Fatal(err)
	}
	agentCtx, stopAgent := context.WithCancel(ctx)
	agentDone := make(chan error, 1)
	root := NewAgentRoot()
	root.SetArgs([]string{"run", "--config", configPath})
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	go func() { agentDone <- root.ExecuteContext(agentCtx) }()
	waitForCondition(t, 3*time.Second, func() bool {
		_, registered := runtime.AgentSessions.Get("agent-cli")
		return registered
	}, "Agent CLI did not register a live session")
	strictAgent := false
	for _, session := range runtime.AgentSessions.List("agent-cli") {
		if session.Supports(protocol.CapabilityStreamOpenResult) {
			strictAgent = true
		}
	}
	if !strictAgent {
		t.Fatal("Agent CLI did not register stream open-result capability")
	}

	clientTransport, err := client.DialWebSocket(ctx, "ws://"+runtimeListener.Addr().String()+"/ws/client", clientToken.Secret)
	if err != nil {
		t.Fatal(err)
	}
	clientSession := client.NewSession(clientTransport)
	defer clientSession.Close()

	tcpResult := make(chan error, 1)
	go serveAgentCLITCPTarget(tcpListener, tcpResult)
	tcpStream, err := clientSession.OpenStreamConn(ctx, client.StreamRequest{AgentID: "agent-cli", Protocol: "tcp", TargetHost: "127.0.0.1", TargetPort: tcpPort})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tcpStream.Write([]byte("tcp-request")); err != nil {
		t.Fatal(err)
	}
	if err := tcpStream.(interface{ CloseWrite() error }).CloseWrite(); err != nil {
		t.Fatal(err)
	}
	if got := readExactWithTimeout(t, tcpStream, len("tcp-response")); string(got) != "tcp-response" {
		t.Fatalf("TCP response = %q", got)
	}
	if err := waitError(t, tcpResult, "TCP target"); err != nil {
		t.Fatal(err)
	}
	_ = tcpStream.Close()

	// Keep the real socket test below the smallest common host UDP limit.
	// The dispatcher unit test separately covers a 60 KiB logical datagram.
	udpPayload := bytes.Repeat([]byte("u"), 8<<10)
	udpResult := make(chan error, 1)
	go serveAgentCLIUDPTarget(udpTarget, udpPayload, udpResult)
	udpStream, err := clientSession.OpenDatagram(ctx, client.StreamRequest{AgentID: "agent-cli", Protocol: "udp", TargetHost: "127.0.0.1", TargetPort: udpPort})
	if err != nil {
		t.Fatal(err)
	}
	if err := udpStream.WriteDatagram(udpPayload); err != nil {
		t.Fatal(err)
	}
	udpResponse := readDatagramWithTimeout(t, udpStream)
	if !bytes.Equal(udpResponse, udpPayload) {
		t.Fatalf("UDP response bytes = %d, want %d-byte datagram", len(udpResponse), len(udpPayload))
	}
	if err := waitError(t, udpResult, "UDP target"); err != nil {
		t.Fatal(err)
	}
	_ = udpStream.Close()

	httpStream, err := clientSession.OpenStreamConn(ctx, client.StreamRequest{AgentID: "agent-cli", Protocol: "http", TargetHost: httpHost, TargetPort: httpPort})
	if err != nil {
		t.Fatal(err)
	}
	request := "GET /through-agent HTTP/1.1\r\nHost: " + net.JoinHostPort(httpHost, httpPortText) + "\r\nConnection: close\r\n\r\n"
	if _, err := httpStream.Write([]byte(request)); err != nil {
		t.Fatal(err)
	}
	httpResponse := readAllWithTimeout(t, httpStream)
	if !bytes.Contains(httpResponse, []byte("agent-http-response")) {
		t.Fatalf("HTTP response = %q", httpResponse)
	}
	_ = httpStream.Close()

	stopAgent()
	if err := waitError(t, agentDone, "Agent command"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Agent command error = %v, want context canceled", err)
	}
	stopRuntime()
	if err := waitError(t, runtimeDone, "Server runtime"); err != nil {
		t.Fatal(err)
	}
}

func TestAgentDispatcherFactoryAdvertisesStreamCapabilities(t *testing.T) {
	transport := &cliFrameTransport{incoming: make(chan protocol.Frame), sent: make(chan protocol.Frame, 1)}
	session := agent.NewSessionWithMetadata(transport, agent.NewMetadataCollector(nil))
	factory := NewAgentDispatcherFactory(config.AgentStreamConfig{MaxConcurrentDials: 3, MaxPendingDials: 7})
	handler := factory(session, "connection-1")
	if handler == nil {
		t.Fatal("factory returned no handler")
	}
	if err := session.ReportMetadata(context.Background()); err != nil {
		t.Fatal(err)
	}
	frame := <-transport.sent
	payload, err := protocol.DecodeAgentMetadataPayload(frame.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(payload.Capabilities) != 1 || payload.Capabilities[0] != protocol.CapabilityStreamOpenResult {
		t.Fatalf("agent capabilities=%v", payload.Capabilities)
	}
}

func serveAgentCLITCPTarget(listener net.Listener, result chan<- error) {
	conn, err := listener.Accept()
	if err != nil {
		result <- err
		return
	}
	defer conn.Close()
	request := make([]byte, len("tcp-request"))
	if _, err := io.ReadFull(conn, request); err != nil {
		result <- err
		return
	}
	if string(request) != "tcp-request" {
		result <- fmt.Errorf("TCP request = %q", request)
		return
	}
	one := make([]byte, 1)
	if n, err := conn.Read(one); n != 0 || !errors.Is(err, io.EOF) {
		result <- fmt.Errorf("TCP half-close read = (%d, %v), want EOF", n, err)
		return
	}
	_, err = conn.Write([]byte("tcp-response"))
	result <- err
}

func serveAgentCLIUDPTarget(conn *net.UDPConn, want []byte, result chan<- error) {
	buffer := make([]byte, protocol.MaxPayload)
	n, source, err := conn.ReadFromUDP(buffer)
	if err != nil {
		result <- err
		return
	}
	if !bytes.Equal(buffer[:n], want) {
		result <- fmt.Errorf("UDP target bytes = %d, want %d", n, len(want))
		return
	}
	_, err = conn.WriteToUDP(buffer[:n], source)
	result <- err
}

func readExactWithTimeout(t *testing.T, reader io.Reader, size int) []byte {
	t.Helper()
	result := make(chan struct {
		payload []byte
		err     error
	}, 1)
	go func() {
		payload := make([]byte, size)
		_, err := io.ReadFull(reader, payload)
		result <- struct {
			payload []byte
			err     error
		}{payload: payload, err: err}
	}()
	select {
	case got := <-result:
		if got.err != nil {
			t.Fatal(got.err)
		}
		return got.payload
	case <-time.After(3 * time.Second):
		t.Fatal("timed out reading stream response")
		return nil
	}
}

func readDatagramWithTimeout(t *testing.T, stream client.DatagramStream) []byte {
	t.Helper()
	result := make(chan struct {
		payload []byte
		err     error
	}, 1)
	go func() {
		payload, err := stream.ReadDatagram()
		result <- struct {
			payload []byte
			err     error
		}{payload: payload, err: err}
	}()
	select {
	case got := <-result:
		if got.err != nil {
			t.Fatal(got.err)
		}
		return got.payload
	case <-time.After(3 * time.Second):
		t.Fatal("timed out reading UDP response")
		return nil
	}
}

func readAllWithTimeout(t *testing.T, reader io.Reader) []byte {
	t.Helper()
	result := make(chan struct {
		payload []byte
		err     error
	}, 1)
	go func() {
		payload, err := io.ReadAll(reader)
		result <- struct {
			payload []byte
			err     error
		}{payload: payload, err: err}
	}()
	select {
	case got := <-result:
		if got.err != nil {
			t.Fatal(got.err)
		}
		return got.payload
	case <-time.After(3 * time.Second):
		t.Fatal("timed out reading HTTP response")
		return nil
	}
}

func waitForCondition(t *testing.T, timeout time.Duration, condition func() bool, message string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal(message)
}

func waitError(t *testing.T, result <-chan error, name string) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for %s", name)
		return nil
	}
}
