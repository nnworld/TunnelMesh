package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

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
		session := client.NewSession(transport)
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
