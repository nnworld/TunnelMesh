package cli

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/build"
	"github.com/tunnelmesh/tunnelmesh/internal/client"
	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
)

func TestClientProxyUsesAuthenticatedSessionAndStdio(t *testing.T) {
	previous := runClientWebSocket
	defer func() { runClientWebSocket = previous }()
	transport := &cliFrameTransport{incoming: make(chan protocol.Frame, 4), sent: make(chan protocol.Frame, 4)}
	var gotURL, gotToken string
	runClientWebSocket = func(ctx context.Context, serverURL, token string, onReady func(*client.Session) error) error {
		gotURL, gotToken = serverURL, token
		session := client.NewSessionWithOpenMode(transport, client.SessionOpenStrict)
		return onReady(session)
	}

	root := NewClientRoot()
	root.SetIn(bytes.NewBufferString("request"))
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"proxy", "tcp", "--client.server_url", "ws://server.example/ws/client", "--client.token", "client-secret", "--agent", "agent-a", "--target-host", "10.0.0.8", "--target-port", "22"})
	done := make(chan error, 1)
	go func() { done <- root.ExecuteContext(context.Background()) }()

	open := <-transport.sent
	if open.Type != protocol.FrameOpenStream {
		t.Fatalf("first frame type = %d, want OPEN", open.Type)
	}
	payload, err := protocol.DecodeStreamOpenPayload(open.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if payload.AgentID != "agent-a" || payload.Protocol != "tcp" || payload.TargetHost != "10.0.0.8" || payload.TargetPort != 22 {
		t.Fatalf("OPEN payload = %+v", payload)
	}
	resultPayload, err := protocol.EncodeOpenResultPayload(protocol.OpenResultPayload{
		Accepted: true, Stage: protocol.OpenResultStageConnect, Code: protocol.OpenResultCodeOK,
	})
	if err != nil {
		t.Fatal(err)
	}
	transport.incoming <- protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenResult, StreamID: open.StreamID, Payload: resultPayload}
	data := <-transport.sent
	if data.Type != protocol.FrameData || string(data.Payload) != "request" {
		t.Fatalf("DATA frame = %+v", data)
	}
	transport.incoming <- protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: open.StreamID, Payload: []byte("response")}
	transport.incoming <- protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameHalfClose, StreamID: open.StreamID}
	if err := <-done; err != nil {
		t.Fatalf("proxy command error = %v", err)
	}
	if gotURL != "ws://server.example/ws/client" || gotToken != "client-secret" {
		t.Fatalf("runner URL=%q token=%q", gotURL, gotToken)
	}
	if out.String() != "response" {
		t.Fatalf("stdout = %q, want response", out.String())
	}
}

func TestClientForwardCommandsStartOnAuthenticatedSession(t *testing.T) {
	for _, proto := range []string{"tcp", "udp", "http"} {
		t.Run(proto, func(t *testing.T) {
			previous := runClientWebSocket
			defer func() { runClientWebSocket = previous }()
			called := false
			runClientWebSocket = func(_ context.Context, serverURL, token string, onReady func(*client.Session) error) error {
				called = true
				if serverURL != "ws://server.example/ws/client" || token != "client-secret" {
					t.Fatalf("runner URL=%q token=%q", serverURL, token)
				}
				return onReady(client.NewSession(&cliFrameTransport{incoming: make(chan protocol.Frame), sent: make(chan protocol.Frame, 4)}))
			}
			root := NewClientRoot()
			var out bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&out)
			root.SetArgs([]string{"forward", proto, "--client.server_url", "ws://server.example/ws/client", "--client.token", "client-secret", "--listen", "127.0.0.1:0", "--agent", "agent-a", "--target-host", "10.0.0.8", "--target-port", "22"})
			if err := root.ExecuteContext(context.Background()); err != nil {
				t.Fatalf("ExecuteContext() error = %v", err)
			}
			if !called {
				t.Fatal("authenticated Client WebSocket runner was not called")
			}
		})
	}
}

func TestClientRunUsesOneWebSocketPerAgent(t *testing.T) {
	previous := runClientSessionPool
	defer func() { runClientSessionPool = previous }()

	listen1 := reserveLoopbackListenAddress(t)
	listen2 := reserveLoopbackListenAddress(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "client.yaml")
	yaml := `mode: local
client:
  server_url: ws://server.example/ws/client
  token: client-secret
  tunnels:
    - name: socks-a
      protocol: socks5
      listen: ` + listen1 + `
      agent_id: agent-a
    - name: socks-b
      protocol: socks5
      listen: ` + listen2 + `
      agent_id: agent-b
`
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}

	transports := make(chan *cliFrameTransport, 2)
	ready := make(chan error, 2)
	runClientSessionPool = func(ctx context.Context, serverURL, token string, onReady func(*client.Session) error, _ client.WebSocketRunOptions) error {
		if serverURL != "ws://server.example/ws/client" || token != "client-secret" {
			ready <- fmt.Errorf("runner URL=%q token=%q", serverURL, token)
			return <-ready
		}
		transport := &cliFrameTransport{incoming: make(chan protocol.Frame), sent: make(chan protocol.Frame, 8)}
		session := client.NewSession(transport)
		session.Start()
		if err := onReady(session); err != nil {
			ready <- err
			_ = session.Close()
			return err
		}
		transports <- transport
		ready <- nil
		<-ctx.Done()
		_ = session.Close()
		return ctx.Err()
	}

	root := NewClientRoot()
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"--config", path, "run"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- root.ExecuteContext(ctx) }()

	for i := 0; i < 2; i++ {
		select {
		case err := <-ready:
			if err != nil {
				t.Fatalf("run readiness error %d = %v", i+1, err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("client run command did not become ready")
		}
	}
	firstTransport := <-transports
	secondTransport := <-transports
	sendSOCKS5Connect(t, listen1)
	sendSOCKS5Connect(t, listen2)

	agentIDs := map[string]bool{}
	opens := 0
	transportByAgent := map[string]*cliFrameTransport{}
	for opens < 2 {
		select {
		case open := <-firstTransport.sent:
			if open.Type != protocol.FrameOpenStream {
				continue
			}
			payload, err := protocol.DecodeStreamOpenPayload(open.Payload)
			if err != nil {
				t.Fatal(err)
			}
			if payload.Protocol != "tcp" || payload.TargetHost != "127.0.0.1" || payload.TargetPort != 80 {
				t.Fatalf("OPEN payload = %+v", payload)
			}
			agentIDs[payload.AgentID] = true
			transportByAgent[payload.AgentID] = firstTransport
			opens++
		case open := <-secondTransport.sent:
			if open.Type != protocol.FrameOpenStream {
				continue
			}
			payload, err := protocol.DecodeStreamOpenPayload(open.Payload)
			if err != nil {
				t.Fatal(err)
			}
			if payload.Protocol != "tcp" || payload.TargetHost != "127.0.0.1" || payload.TargetPort != 80 {
				t.Fatalf("OPEN payload = %+v", payload)
			}
			agentIDs[payload.AgentID] = true
			transportByAgent[payload.AgentID] = secondTransport
			opens++
		}
	}
	if !agentIDs["agent-a"] || !agentIDs["agent-b"] {
		t.Fatalf("opened agent IDs = %v, want agent-a and agent-b", agentIDs)
	}
	if transportByAgent["agent-a"] == transportByAgent["agent-b"] {
		t.Fatal("two agents should use different WebSocket transports")
	}

	cancel()
	err := <-done
	if err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("ExecuteContext() error = %v", err)
	}
}

func TestClientRunPassesRuntimeMetadataToSessionPool(t *testing.T) {
	previous := runClientSessionPool
	defer func() { runClientSessionPool = previous }()

	listen := reserveLoopbackListenAddress(t)
	dir := t.TempDir()
	identityPath := filepath.Join(dir, "client-instance-id")
	regionPath := filepath.Join(dir, "region")
	if err := os.WriteFile(regionPath, []byte("cn-north"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "client.yaml")
	yaml := `mode: local
client:
  server_url: ws://server.example/ws/client
  token: client-secret
  instance_id_path: ` + identityPath + `
  metadata:
    - name: region
      source: file
      path: ` + regionPath + `
  tunnels:
    - name: socks-a
      protocol: socks5
      listen: ` + listen + `
      agent_id: agent-a
`
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}

	optionsCh := make(chan client.WebSocketRunOptions, 1)
	runClientSessionPool = func(ctx context.Context, _, _ string, onReady func(*client.Session) error, options client.WebSocketRunOptions) error {
		optionsCh <- options
		transport := &cliFrameTransport{incoming: make(chan protocol.Frame), sent: make(chan protocol.Frame, 1)}
		session := client.NewSession(transport)
		session.Start()
		if err := onReady(session); err != nil {
			return err
		}
		<-ctx.Done()
		_ = session.Close()
		return ctx.Err()
	}

	root := NewClientRoot()
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"--config", path, "run"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- root.ExecuteContext(ctx) }()

	select {
	case options := <-optionsCh:
		if options.Metadata == nil {
			t.Fatal("client run did not pass metadata options to the session pool")
		}
		metadata := *options.Metadata
		if !strings.HasPrefix(metadata.InstanceID, "client-") || metadata.InstanceID != strings.ToLower(metadata.InstanceID) {
			t.Fatalf("generated instance ID = %q", metadata.InstanceID)
		}
		if len(metadata.AgentIDs) != 1 || metadata.AgentIDs[0] != "agent-a" {
			t.Fatalf("metadata agent IDs = %#v", metadata.AgentIDs)
		}
		if metadata.Version != build.Version || metadata.Commit != build.Commit {
			t.Fatalf("metadata build identity = %#v", metadata)
		}
		if len(metadata.Listeners) != 1 || metadata.Listeners[0].Protocol != "socks5" || metadata.Listeners[0].ListenAddress != listen || metadata.Listeners[0].AgentID != "agent-a" || !metadata.Listeners[0].Enabled {
			t.Fatalf("metadata listeners = %#v", metadata.Listeners)
		}
		if len(metadata.Metadata) != 1 || metadata.Metadata[0].Name != "region" || metadata.Metadata[0].Value != "cn-north" {
			t.Fatalf("metadata fields = %#v", metadata.Metadata)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("client run did not start the session pool")
	}

	persisted, err := os.ReadFile(identityPath)
	if err != nil {
		t.Fatalf("read generated client instance ID: %v", err)
	}
	if strings.TrimSpace(string(persisted)) == "" {
		t.Fatal("generated client instance ID file is empty")
	}

	cancel()
	if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("ExecuteContext() error = %v", err)
	}
}

func TestClientRunStartsHTTPProxyTunnel(t *testing.T) {
	previous := runClientSessionPool
	defer func() { runClientSessionPool = previous }()

	listen := reserveLoopbackListenAddress(t)
	path := filepath.Join(t.TempDir(), "client.yaml")
	yaml := `mode: local
client:
  server_url: ws://server.example/ws/client
  token: client-secret
  tunnels:
    - name: http-proxy
      protocol: http-proxy
      listen: ` + listen + `
      agent_id: agent-a
      auth_mode: none
`
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}

	ready := make(chan error, 1)
	transport := &cliFrameTransport{incoming: make(chan protocol.Frame), sent: make(chan protocol.Frame, 4)}
	runClientSessionPool = func(ctx context.Context, serverURL, token string, onReady func(*client.Session) error, _ client.WebSocketRunOptions) error {
		if serverURL != "ws://server.example/ws/client" || token != "client-secret" {
			ready <- fmt.Errorf("runner URL=%q token=%q", serverURL, token)
			return <-ready
		}
		session := client.NewSession(transport)
		session.Start()
		if err := onReady(session); err != nil {
			ready <- err
			return err
		}
		ready <- nil
		<-ctx.Done()
		_ = session.Close()
		return ctx.Err()
	}

	root := NewClientRoot()
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"--config", path, "run"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- root.ExecuteContext(ctx) }()

	select {
	case err := <-ready:
		if err != nil {
			t.Fatalf("run readiness error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("client run command did not become ready")
	}

	conn, err := net.DialTimeout("tcp", listen, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte("CONNECT 127.0.0.1:3000 HTTP/1.1\r\nHost: 127.0.0.1:3000\r\n\r\n")); err != nil {
		t.Fatal(err)
	}
	response, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(response, "200 Connection established") {
		t.Fatalf("CONNECT response = %q", response)
	}

	select {
	case open := <-transport.sent:
		if open.Type != protocol.FrameOpenStream {
			t.Fatalf("frame type = %d, want OPEN", open.Type)
		}
		payload, err := protocol.DecodeStreamOpenPayload(open.Payload)
		if err != nil {
			t.Fatal(err)
		}
		if payload.Protocol != "tcp" || payload.AgentID != "agent-a" || payload.TargetHost != "127.0.0.1" || payload.TargetPort != 3000 {
			t.Fatalf("OPEN payload = %+v", payload)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("HTTP proxy did not open an agent stream")
	}

	cancel()
	err = <-done
	if err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("ExecuteContext() error = %v", err)
	}
}

func TestClientRunKeepsTunnelsAfterWebSocketReconnect(t *testing.T) {
	previous := runClientSessionPool
	defer func() { runClientSessionPool = previous }()

	listen := reserveLoopbackListenAddress(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "client.yaml")
	yaml := `mode: local
client:
  server_url: ws://server.example/ws/client
  token: client-secret
  tunnels:
    - name: socks-a
      protocol: socks5
      listen: ` + listen + `
      agent_id: agent-a
`
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}

	ready := make(chan *cliFrameTransport, 2)
	reconnect := make(chan struct{})
	runClientSessionPool = func(ctx context.Context, _, _ string, onReady func(*client.Session) error, _ client.WebSocketRunOptions) error {
		for round := 0; round < 2; round++ {
			transport := &cliFrameTransport{incoming: make(chan protocol.Frame), sent: make(chan protocol.Frame, 4)}
			session := client.NewSession(transport)
			session.Start()
			if err := onReady(session); err != nil {
				return err
			}
			ready <- transport
			if round == 0 {
				<-reconnect
			} else {
				<-ctx.Done()
			}
			_ = session.Close()
			if round == 1 {
				return ctx.Err()
			}
		}
		return nil
	}

	root := NewClientRoot()
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"--config", path, "run"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- root.ExecuteContext(ctx) }()

	first := <-ready
	sendSOCKS5Connect(t, listen)
	_ = first.Close()
	close(reconnect)

	second := <-ready
	sendSOCKS5Connect(t, listen)
	select {
	case open := <-second.sent:
		if open.Type != protocol.FrameOpenStream {
			t.Fatalf("frame type = %d, want OPEN", open.Type)
		}
		payload, err := protocol.DecodeStreamOpenPayload(open.Payload)
		if err != nil {
			t.Fatal(err)
		}
		if payload.AgentID != "agent-a" {
			t.Fatalf("OPEN agent = %q, want agent-a", payload.AgentID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("tunnel did not use the reconnected WebSocket")
	}

	cancel()
	if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("ExecuteContext() error = %v", err)
	}
}

func TestClientSOCKS5ForwardUsesAuthenticatedSessionAndPasswordEnv(t *testing.T) {
	previous := runClientWebSocket
	defer func() { runClientWebSocket = previous }()
	called := false
	runClientWebSocket = func(_ context.Context, serverURL, token string, onReady func(*client.Session) error) error {
		called = true
		if serverURL != "ws://server.example/ws/client" || token != "client-secret" {
			t.Fatalf("runner URL=%q token=%q", serverURL, token)
		}
		return onReady(client.NewSession(&cliFrameTransport{incoming: make(chan protocol.Frame), sent: make(chan protocol.Frame, 4)}))
	}
	t.Setenv("TUNNELMESH_SOCKS5_USERNAME", "alice")
	t.Setenv("TUNNELMESH_SOCKS5_PASSWORD", "secret")

	root := NewClientRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{
		"forward", "socks5",
		"--client.server_url", "ws://server.example/ws/client",
		"--client.token", "client-secret",
		"--listen", "127.0.0.1:0",
		"--agent", "agent-a",
		"--auth", "password",
	})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("ExecuteContext() error = %v", err)
	}
	if !called {
		t.Fatal("authenticated Client WebSocket runner was not called")
	}
	if !strings.Contains(out.String(), "socks5 forward") {
		t.Fatalf("output = %q, want socks5 forward readiness", out.String())
	}
}

func TestClientSOCKS5ForwardAcceptsAuthURL(t *testing.T) {
	previous := runClientWebSocket
	defer func() { runClientWebSocket = previous }()
	runClientWebSocket = func(_ context.Context, _ string, _ string, onReady func(*client.Session) error) error {
		return onReady(client.NewSession(&cliFrameTransport{incoming: make(chan protocol.Frame), sent: make(chan protocol.Frame, 4)}))
	}

	root := NewClientRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{
		"forward", "socks5",
		"--client.server_url", "ws://server.example/ws/client",
		"--client.token", "client-secret",
		"--listen", "127.0.0.1:0",
		"--agent", "agent-a",
		"--auth-url", "http://auth.internal/validate",
	})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("ExecuteContext() error = %v", err)
	}
	if !strings.Contains(out.String(), "socks5 forward") {
		t.Fatalf("output = %q, want socks5 forward readiness", out.String())
	}
}

func TestClientHTTPProxyForwardUsesAuthenticatedSessionAndBasicEnv(t *testing.T) {
	previous := runClientWebSocket
	defer func() { runClientWebSocket = previous }()
	called := false
	runClientWebSocket = func(_ context.Context, serverURL, token string, onReady func(*client.Session) error) error {
		called = true
		if serverURL != "ws://server.example/ws/client" || token != "client-secret" {
			t.Fatalf("runner URL=%q token=%q", serverURL, token)
		}
		return onReady(client.NewSession(&cliFrameTransport{incoming: make(chan protocol.Frame), sent: make(chan protocol.Frame, 4)}))
	}
	t.Setenv("TUNNELMESH_HTTP_PROXY_USERNAME", "alice")
	t.Setenv("TUNNELMESH_HTTP_PROXY_PASSWORD", "secret")

	root := NewClientRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{
		"forward", "http-proxy",
		"--client.server_url", "ws://server.example/ws/client",
		"--client.token", "client-secret",
		"--listen", "127.0.0.1:0",
		"--agent", "agent-a",
		"--auth", "basic",
	})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("ExecuteContext() error = %v", err)
	}
	if !called {
		t.Fatal("authenticated Client WebSocket runner was not called")
	}
	if !strings.Contains(out.String(), "http-proxy forward") {
		t.Fatalf("output = %q, want http-proxy forward readiness", out.String())
	}
}

func TestClientHTTPProxyForwardAcceptsAuthURL(t *testing.T) {
	previous := runClientWebSocket
	defer func() { runClientWebSocket = previous }()
	runClientWebSocket = func(_ context.Context, _ string, _ string, onReady func(*client.Session) error) error {
		return onReady(client.NewSession(&cliFrameTransport{incoming: make(chan protocol.Frame), sent: make(chan protocol.Frame, 4)}))
	}

	root := NewClientRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{
		"forward", "http-proxy",
		"--client.server_url", "ws://server.example/ws/client",
		"--client.token", "client-secret",
		"--listen", "127.0.0.1:0",
		"--agent", "agent-a",
		"--auth-url", "http://auth.internal/validate",
	})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("ExecuteContext() error = %v", err)
	}
	if !strings.Contains(out.String(), "http-proxy forward") {
		t.Fatalf("output = %q, want http-proxy forward readiness", out.String())
	}
}

func TestClientHTTPProxyForwardRejectsUnsafeNonLoopbackListener(t *testing.T) {
	previous := runClientWebSocket
	defer func() { runClientWebSocket = previous }()
	runClientWebSocket = func(context.Context, string, string, func(*client.Session) error) error {
		return errors.New("unexpected authenticated session")
	}
	tests := []struct {
		name string
		args []string
	}{
		{"without allow remote", []string{"--listen", "0.0.0.0:8080"}},
		{"with allow remote but no basic auth", []string{"--listen", "0.0.0.0:8080", "--allow-remote"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := NewClientRoot()
			root.SetOut(io.Discard)
			root.SetErr(io.Discard)
			root.SetArgs(append([]string{
				"forward", "http-proxy",
				"--client.server_url", "ws://server.example/ws/client",
				"--client.token", "client-secret",
				"--agent", "agent-a",
			}, test.args...))
			if err := root.ExecuteContext(context.Background()); err == nil {
				t.Fatal("unsafe non-loopback HTTP proxy listener was accepted")
			}
		})
	}
}

func TestClientHTTPProxyForwardRequiresBasicEnvironment(t *testing.T) {
	previous := runClientWebSocket
	defer func() { runClientWebSocket = previous }()
	runClientWebSocket = func(context.Context, string, string, func(*client.Session) error) error {
		return errors.New("unexpected authenticated session")
	}
	t.Setenv("TUNNELMESH_HTTP_PROXY_USERNAME", "alice")
	t.Setenv("TUNNELMESH_HTTP_PROXY_PASSWORD", "")
	root := NewClientRoot()
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{
		"forward", "http-proxy",
		"--client.server_url", "ws://server.example/ws/client",
		"--client.token", "client-secret",
		"--listen", "127.0.0.1:0",
		"--agent", "agent-a",
		"--auth", "basic",
	})
	if err := root.ExecuteContext(context.Background()); err == nil {
		t.Fatal("basic auth was accepted without a password")
	}
}

func TestClientSOCKS5ForwardRejectsUnsafeNonLoopbackListener(t *testing.T) {
	previous := runClientWebSocket
	defer func() { runClientWebSocket = previous }()
	runClientWebSocket = func(context.Context, string, string, func(*client.Session) error) error {
		return errors.New("unexpected authenticated session")
	}
	tests := []struct {
		name string
		args []string
	}{
		{"without allow remote", []string{"--listen", "0.0.0.0:1080"}},
		{"with allow remote but no password", []string{"--listen", "0.0.0.0:1080", "--allow-remote"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := NewClientRoot()
			root.SetOut(io.Discard)
			root.SetErr(io.Discard)
			root.SetArgs(append([]string{
				"forward", "socks5",
				"--client.server_url", "ws://server.example/ws/client",
				"--client.token", "client-secret",
				"--agent", "agent-a",
			}, test.args...))
			if err := root.ExecuteContext(context.Background()); err == nil {
				t.Fatal("unsafe non-loopback SOCKS5 listener was accepted")
			}
		})
	}
}

func TestClientSOCKS5ForwardRequiresPasswordEnvironment(t *testing.T) {
	previous := runClientWebSocket
	defer func() { runClientWebSocket = previous }()
	runClientWebSocket = func(context.Context, string, string, func(*client.Session) error) error {
		return errors.New("unexpected authenticated session")
	}
	t.Setenv("TUNNELMESH_SOCKS5_USERNAME", "alice")
	t.Setenv("TUNNELMESH_SOCKS5_PASSWORD", "")
	root := NewClientRoot()
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{
		"forward", "socks5",
		"--client.server_url", "ws://server.example/ws/client",
		"--client.token", "client-secret",
		"--listen", "127.0.0.1:0",
		"--agent", "agent-a",
		"--auth", "password",
	})
	if err := root.ExecuteContext(context.Background()); err == nil {
		t.Fatal("password mode was accepted without a password")
	}
}

type cliFrameTransport struct {
	incoming chan protocol.Frame
	sent     chan protocol.Frame
	mu       sync.Mutex
	closed   bool
}

func reserveLoopbackListenAddress(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	return addr
}

func sendSOCKS5Connect(t *testing.T, address string) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", address, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte{5, 1, 0}); err != nil {
		t.Fatal(err)
	}
	method := make([]byte, 2)
	if _, err := io.ReadFull(conn, method); err != nil {
		t.Fatal(err)
	}
	if method[0] != 5 || method[1] != 0 {
		t.Fatalf("SOCKS5 method reply = %v, want no-auth", method)
	}
	request := []byte{5, 1, 0, 1, 127, 0, 0, 1, 0, 80}
	if _, err := conn.Write(request); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, 10)
	if _, err := io.ReadFull(conn, reply); err != nil {
		t.Fatal(err)
	}
	if reply[1] != 0 {
		t.Fatalf("SOCKS5 reply code = %d, want succeeded", reply[1])
	}
}

func (t *cliFrameTransport) Send(frame protocol.Frame) error {
	t.sent <- frame
	return nil
}

func (t *cliFrameTransport) Receive() (protocol.Frame, error) {
	frame, ok := <-t.incoming
	if !ok {
		return protocol.Frame{}, io.EOF
	}
	return frame, nil
}

func (t *cliFrameTransport) Close() error {
	t.mu.Lock()
	if !t.closed {
		t.closed = true
		close(t.incoming)
	}
	t.mu.Unlock()
	return nil
}
