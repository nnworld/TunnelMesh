# Task 6 review package

Base HEAD: `163fe12121d2f839ea4bf4907f55a8e7dd835057`

## Changed files

```text
Files .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-before/internal/agent/session.go and .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/agent/session.go differ
Only in .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/cli: client_runtime_test.go
Files .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-before/internal/cli/root.go and .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/cli/root.go differ
Files .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-before/internal/cli/root_test.go and .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/cli/root_test.go differ
Files .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-before/internal/client/forward.go and .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/client/forward.go differ
Files .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-before/internal/client/forward_test.go and .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/client/forward_test.go differ
Files .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-before/internal/client/session.go and .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/client/session.go differ
Only in .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/client: websocket.go
Only in .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/client: websocket_test.go
Files .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-before/internal/config/config.go and .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/config/config.go differ
Files .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-before/internal/config/config_test.go and .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/config/config_test.go differ
Only in .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/protocol: stream_open.go
Only in .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/protocol: stream_open_test.go
Files .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-before/internal/server/middleware.go and .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/server/middleware.go differ
Files .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-before/internal/server/runtime.go and .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/server/runtime.go differ
Files .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-before/internal/server/session_manager.go and .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/server/session_manager.go differ
Only in .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/server: stream_authorizer.go
Only in .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/server: stream_authorizer_test.go
Files .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-before/internal/server/web.go and .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/server/web.go differ
Files .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-before/internal/server/ws_client.go and .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/server/ws_client.go differ
Only in .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/server: ws_client_test.go
```

## Full task-only diff

```diff
diff -ruN --exclude=*.__ABSENT__ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-before/internal/agent/session.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/agent/session.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-before/internal/agent/session.go	2026-09-06 17:56:43
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/agent/session.go	2026-09-06 17:56:59
@@ -2,7 +2,6 @@
 
 import (
 	"context"
-	"encoding/json"
 	"errors"
 	"io"
 	"math/rand"
@@ -17,13 +16,7 @@
 var ErrStreamNotFound = errors.New("agent: stream not found")
 var ErrDuplicateStream = errors.New("agent: duplicate stream")
 
-type StreamOpenPayload struct {
-	AgentID    string `json:"agent_id,omitempty"`
-	Protocol   string `json:"protocol"`
-	TargetHost string `json:"target_host"`
-	TargetPort int    `json:"target_port"`
-	Metadata   []byte `json:"metadata,omitempty"`
-}
+type StreamOpenPayload = protocol.StreamOpenPayload
 type StreamDialFunc func(context.Context, string, string, int) (io.ReadWriteCloser, error)
 type streamEntry struct {
 	conn       io.ReadWriteCloser
@@ -86,8 +79,8 @@
 	}
 	switch f.Type {
 	case protocol.FrameOpenStream:
-		var p StreamOpenPayload
-		if err := json.Unmarshal(f.Payload, &p); err != nil {
+		p, err := protocol.DecodeStreamOpenPayload(f.Payload)
+		if err != nil {
 			return err
 		}
 		if f.StreamID == 0 || p.TargetHost == "" || p.TargetPort < 1 || p.TargetPort > 65535 {
diff -ruN --exclude=*.__ABSENT__ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-before/internal/cli/client_runtime_test.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/cli/client_runtime_test.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-before/internal/cli/client_runtime_test.go	1970-01-01 08:00:00
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/cli/client_runtime_test.go	2026-09-06 17:56:59
@@ -0,0 +1,118 @@
+package cli
+
+import (
+	"bytes"
+	"context"
+	"io"
+	"sync"
+	"testing"
+
+	"github.com/tunnelmesh/tunnelmesh/internal/client"
+	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
+)
+
+func TestClientProxyUsesAuthenticatedSessionAndStdio(t *testing.T) {
+	previous := runClientWebSocket
+	defer func() { runClientWebSocket = previous }()
+	transport := &cliFrameTransport{incoming: make(chan protocol.Frame, 4), sent: make(chan protocol.Frame, 4)}
+	var gotURL, gotToken string
+	runClientWebSocket = func(ctx context.Context, serverURL, token string, onReady func(*client.Session) error) error {
+		gotURL, gotToken = serverURL, token
+		session := client.NewSession(transport)
+		return onReady(session)
+	}
+
+	root := NewClientRoot()
+	root.SetIn(bytes.NewBufferString("request"))
+	var out bytes.Buffer
+	root.SetOut(&out)
+	root.SetErr(&out)
+	root.SetArgs([]string{"proxy", "tcp", "--client.server_url", "ws://server.example/ws/client", "--client.token", "client-secret", "--agent", "agent-a", "--target-host", "10.0.0.8", "--target-port", "22"})
+	done := make(chan error, 1)
+	go func() { done <- root.ExecuteContext(context.Background()) }()
+
+	open := <-transport.sent
+	if open.Type != protocol.FrameOpenStream {
+		t.Fatalf("first frame type = %d, want OPEN", open.Type)
+	}
+	payload, err := protocol.DecodeStreamOpenPayload(open.Payload)
+	if err != nil {
+		t.Fatal(err)
+	}
+	if payload.AgentID != "agent-a" || payload.Protocol != "tcp" || payload.TargetHost != "10.0.0.8" || payload.TargetPort != 22 {
+		t.Fatalf("OPEN payload = %+v", payload)
+	}
+	data := <-transport.sent
+	if data.Type != protocol.FrameData || string(data.Payload) != "request" {
+		t.Fatalf("DATA frame = %+v", data)
+	}
+	transport.incoming <- protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: open.StreamID, Payload: []byte("response")}
+	transport.incoming <- protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameHalfClose, StreamID: open.StreamID}
+	if err := <-done; err != nil {
+		t.Fatalf("proxy command error = %v", err)
+	}
+	if gotURL != "ws://server.example/ws/client" || gotToken != "client-secret" {
+		t.Fatalf("runner URL=%q token=%q", gotURL, gotToken)
+	}
+	if out.String() != "response" {
+		t.Fatalf("stdout = %q, want response", out.String())
+	}
+}
+
+func TestClientForwardCommandsStartOnAuthenticatedSession(t *testing.T) {
+	for _, proto := range []string{"tcp", "udp", "http"} {
+		t.Run(proto, func(t *testing.T) {
+			previous := runClientWebSocket
+			defer func() { runClientWebSocket = previous }()
+			called := false
+			runClientWebSocket = func(_ context.Context, serverURL, token string, onReady func(*client.Session) error) error {
+				called = true
+				if serverURL != "ws://server.example/ws/client" || token != "client-secret" {
+					t.Fatalf("runner URL=%q token=%q", serverURL, token)
+				}
+				return onReady(client.NewSession(&cliFrameTransport{incoming: make(chan protocol.Frame), sent: make(chan protocol.Frame, 4)}))
+			}
+			root := NewClientRoot()
+			var out bytes.Buffer
+			root.SetOut(&out)
+			root.SetErr(&out)
+			root.SetArgs([]string{"forward", proto, "--client.server_url", "ws://server.example/ws/client", "--client.token", "client-secret", "--listen", "127.0.0.1:0", "--agent", "agent-a", "--target-host", "10.0.0.8", "--target-port", "22"})
+			if err := root.ExecuteContext(context.Background()); err != nil {
+				t.Fatalf("ExecuteContext() error = %v", err)
+			}
+			if !called {
+				t.Fatal("authenticated Client WebSocket runner was not called")
+			}
+		})
+	}
+}
+
+type cliFrameTransport struct {
+	incoming chan protocol.Frame
+	sent     chan protocol.Frame
+	mu       sync.Mutex
+	closed   bool
+}
+
+func (t *cliFrameTransport) Send(frame protocol.Frame) error {
+	t.sent <- frame
+	return nil
+}
+
+func (t *cliFrameTransport) Receive() (protocol.Frame, error) {
+	frame, ok := <-t.incoming
+	if !ok {
+		return protocol.Frame{}, io.EOF
+	}
+	return frame, nil
+}
+
+func (t *cliFrameTransport) Close() error {
+	t.mu.Lock()
+	if !t.closed {
+		t.closed = true
+		close(t.incoming)
+	}
+	t.mu.Unlock()
+	return nil
+}
diff -ruN --exclude=*.__ABSENT__ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-before/internal/cli/root.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/cli/root.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-before/internal/cli/root.go	2026-09-06 17:20:25
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/cli/root.go	2026-09-06 17:56:59
@@ -5,12 +5,15 @@
 
 import (
 	"context"
+	"errors"
 	"fmt"
+	"io"
 	"strings"
 
 	"github.com/spf13/cobra"
 	"github.com/tunnelmesh/tunnelmesh/internal/agent"
 	"github.com/tunnelmesh/tunnelmesh/internal/auth"
+	"github.com/tunnelmesh/tunnelmesh/internal/client"
 	"github.com/tunnelmesh/tunnelmesh/internal/config"
 	"github.com/tunnelmesh/tunnelmesh/internal/server"
 	"github.com/tunnelmesh/tunnelmesh/internal/storage"
@@ -50,6 +53,8 @@
 	tlsCertFile                 string
 	tlsKeyFile                  string
 	tlsMinVersion               string
+	clientServerURL             string
+	clientToken                 string
 }
 
 func newRoot(use string, factory func(*rootOptions) []*cobra.Command) *cobra.Command {
@@ -81,6 +86,8 @@
 	flags.StringVar(&opts.tlsCertFile, "tls.cert_file", "", "native TLS certificate file")
 	flags.StringVar(&opts.tlsKeyFile, "tls.key_file", "", "native TLS private key file")
 	flags.StringVar(&opts.tlsMinVersion, "tls.min_version", "", "native TLS minimum version (1.2 or 1.3)")
+	flags.StringVar(&opts.clientServerURL, "client.server_url", "", "Client WebSocket URL")
+	flags.StringVar(&opts.clientToken, "client.token", "", "Client bearer token")
 	root.AddCommand(factory(opts)...)
 	return root
 }
@@ -256,11 +263,58 @@
 		if targetHost == "" || targetPort < 1 || targetPort > 65535 {
 			return fmt.Errorf("%s forward requires --target-host and --target-port", proto)
 		}
-		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s forward %s -> %s:%d via %s\n", proto, listen, targetHost, targetPort, agentID)
+		if strings.TrimSpace(cfg.Client.ServerURL) == "" || strings.TrimSpace(cfg.Client.Token) == "" {
+			return fmt.Errorf("%s forward requires client.server_url and client.token", proto)
+		}
 		if len(cfg.Client.Tunnels) > 0 {
 			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "loaded %d configured tunnel(s)\n", len(cfg.Client.Tunnels))
 		}
-		return nil
+		var active io.Closer
+		defer func() {
+			if active != nil {
+				_ = active.Close()
+			}
+		}()
+		return runClientWebSocket(cmd.Context(), cfg.Client.ServerURL, cfg.Client.Token, func(session *client.Session) error {
+			if active != nil {
+				_ = active.Close()
+				active = nil
+			}
+			opener := client.NewSessionOpener(session)
+			switch proto {
+			case "tcp":
+				forward, err := client.NewTCPForward(opener, client.TCPForwardConfig{ListenAddr: listen, AgentID: agentID, TargetHost: targetHost, TargetPort: targetPort})
+				if err != nil {
+					return err
+				}
+				if err := forward.Start(cmd.Context()); err != nil {
+					return err
+				}
+				active = forward
+			case "udp":
+				forward, err := client.NewUDPForward(opener, client.UDPForwardConfig{ListenAddr: listen, AgentID: agentID, TargetHost: targetHost, TargetPort: targetPort})
+				if err != nil {
+					return err
+				}
+				if err := forward.Start(cmd.Context()); err != nil {
+					return err
+				}
+				active = forward
+			case "http":
+				forward, err := client.NewHTTPForward(opener, client.HTTPForwardConfig{ListenAddr: listen, AgentID: agentID, TargetHost: targetHost, TargetPort: targetPort})
+				if err != nil {
+					return err
+				}
+				if err := forward.Start(cmd.Context()); err != nil {
+					return err
+				}
+				active = forward
+			default:
+				return fmt.Errorf("unsupported forward protocol %q", proto)
+			}
+			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s forward %s -> %s:%d via %s\n", proto, listen, targetHost, targetPort, agentID)
+			return nil
+		})
 	}
 	return cmd
 }
@@ -280,11 +334,23 @@
 		if targetHost == "" || targetPort < 1 || targetPort > 65535 {
 			return fmt.Errorf("proxy %s requires --target-host and --target-port", proto)
 		}
-		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "proxy %s %s:%d via %s\n", proto, targetHost, targetPort, agentID)
-		if cfg.Client.ServerURL == "" {
-			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "note: configure client.server_url for a live WebSocket session")
+		if strings.TrimSpace(cfg.Client.ServerURL) == "" || strings.TrimSpace(cfg.Client.Token) == "" {
+			return fmt.Errorf("proxy %s requires client.server_url and client.token", proto)
 		}
-		return nil
+		err = runClientWebSocket(cmd.Context(), cfg.Client.ServerURL, cfg.Client.Token, func(session *client.Session) error {
+			stream, openErr := client.NewSessionOpener(session).OpenStream(cmd.Context(), client.StreamRequest{AgentID: agentID, Protocol: proto, TargetHost: targetHost, TargetPort: targetPort})
+			if openErr != nil {
+				return openErr
+			}
+			if proxyErr := client.ProxyStdio(cmd.Context(), cmd.InOrStdin(), cmd.OutOrStdout(), stream); proxyErr != nil {
+				return proxyErr
+			}
+			return errClientProxyComplete
+		})
+		if errors.Is(err, errClientProxyComplete) {
+			return nil
+		}
+		return err
 	}
 	return cmd
 }
@@ -354,8 +420,19 @@
 	if flags.Changed("tls.min_version") {
 		values["tls.min_version"] = opts.tlsMinVersion
 	}
+	if flags.Changed("client.server_url") {
+		values["client.server_url"] = opts.clientServerURL
+	}
+	if flags.Changed("client.token") {
+		values["client.token"] = opts.clientToken
+	}
 	return values
 }
+
+var (
+	runClientWebSocket     = client.RunWebSocket
+	errClientProxyComplete = errors.New("client proxy complete")
+)
 
 func effectiveAgentID(cfg config.Config) string {
 	if cfg.Agent.ID != "" {
diff -ruN --exclude=*.__ABSENT__ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-before/internal/cli/root_test.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/cli/root_test.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-before/internal/cli/root_test.go	2026-09-06 17:20:25
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/cli/root_test.go	2026-09-06 17:56:59
@@ -45,6 +45,9 @@
 		}
 	}
 	client := cli.NewClientRoot()
+	if client.PersistentFlags().Lookup("client.token") == nil {
+		t.Fatal("client root missing --client.token")
+	}
 	for _, name := range []string{"login", "agent", "tunnel", "forward", "publish", "proxy", "stop", "status"} {
 		if findCommand(client, name) == nil {
 			t.Fatalf("client root missing %q command", name)
diff -ruN --exclude=*.__ABSENT__ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-before/internal/client/forward.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/client/forward.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-before/internal/client/forward.go	2026-09-06 17:56:30
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/client/forward.go	2026-09-06 17:56:59
@@ -280,9 +280,10 @@
 		return err
 	}
 	f.ln = ln
-	f.server = &http.Server{Handler: http.HandlerFunc(f.handleHTTP)}
+	server := &http.Server{Handler: http.HandlerFunc(f.handleHTTP)}
+	f.server = server
 	go func() {
-		_ = f.server.Serve(ln)
+		_ = server.Serve(ln)
 	}()
 	if ctx != nil {
 		go func() {
diff -ruN --exclude=*.__ABSENT__ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-before/internal/client/forward_test.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/client/forward_test.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-before/internal/client/forward_test.go	2026-09-06 17:56:30
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/client/forward_test.go	2026-09-06 17:56:59
@@ -310,6 +310,23 @@
 	}
 }
 
+func TestHTTPForwardCanCloseImmediatelyAfterStart(t *testing.T) {
+	for i := 0; i < 100; i++ {
+		fwd, err := NewHTTPForward(openerFunc(func(context.Context, StreamRequest) (io.ReadWriteCloser, error) {
+			return nil, errors.New("unused")
+		}), HTTPForwardConfig{ListenAddr: "127.0.0.1:0", AgentID: "a", TargetHost: "service", TargetPort: 8080})
+		if err != nil {
+			t.Fatal(err)
+		}
+		if err := fwd.Start(context.Background()); err != nil {
+			t.Fatal(err)
+		}
+		if err := fwd.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
+			t.Fatal(err)
+		}
+	}
+}
+
 func TestHTTPForwardUpgradeBridgesRawBytes(t *testing.T) {
 	remote := newTestStream([]byte("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\nremote-data"))
 	fwd, err := NewHTTPForward(openerFunc(func(_ context.Context, req StreamRequest) (io.ReadWriteCloser, error) {
@@ -454,6 +471,27 @@
 	}
 	if string(got) != "delayed-reply" {
 		t.Fatalf("got=%q", got)
+	}
+}
+
+func TestSessionRemoteHalfCloseKeepsLocalWriteSideOpen(t *testing.T) {
+	tr := &receiveTransport{sent: make(chan protocol.Frame, 3), recv: make(chan protocol.Frame, 2), done: make(chan struct{})}
+	s := NewSession(tr)
+	stream, err := s.OpenStreamConn(context.Background(), StreamRequest{Protocol: "tcp", TargetHost: "h", TargetPort: 1})
+	if err != nil {
+		t.Fatal(err)
+	}
+	open := <-tr.sent
+	tr.recv <- protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameHalfClose, StreamID: open.StreamID}
+	if _, err := stream.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
+		t.Fatalf("Read() error = %v, want EOF", err)
+	}
+	if _, err := stream.Write([]byte("final-request")); err != nil {
+		t.Fatalf("Write() after remote half-close error = %v", err)
+	}
+	data := <-tr.sent
+	if data.Type != protocol.FrameData || string(data.Payload) != "final-request" {
+		t.Fatalf("DATA frame = %+v", data)
 	}
 }
 
diff -ruN --exclude=*.__ABSENT__ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-before/internal/client/session.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/client/session.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-before/internal/client/session.go	2026-09-06 17:20:25
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/client/session.go	2026-09-06 17:56:59
@@ -2,12 +2,12 @@
 
 import (
 	"context"
-	"encoding/json"
 	"errors"
-	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
 	"io"
 	"sync"
 	"sync/atomic"
+
+	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
 )
 
 var ErrSessionClosed = errors.New("client session closed")
@@ -27,25 +27,22 @@
 	TargetPort           int
 	Metadata             []byte
 }
-type StreamOpenPayload struct {
-	AgentID    string `json:"agent_id,omitempty"`
-	Protocol   string `json:"protocol"`
-	TargetHost string `json:"target_host"`
-	TargetPort int    `json:"target_port"`
-	Metadata   []byte `json:"metadata,omitempty"`
-}
+type StreamOpenPayload = protocol.StreamOpenPayload
 type Session struct {
 	mu        sync.RWMutex
 	transport FrameTransport
 	closed    bool
 	nextID    atomic.Uint32
 	recvOnce  sync.Once
+	done      chan struct{}
+	doneOnce  sync.Once
+	runErr    error
 	streams   map[uint32]*frameStream
 	datagrams map[uint32]*frameDatagramStream
 }
 
 func NewSession(tr FrameTransport) *Session {
-	s := &Session{transport: tr, streams: make(map[uint32]*frameStream), datagrams: make(map[uint32]*frameDatagramStream)}
+	s := &Session{transport: tr, done: make(chan struct{}), streams: make(map[uint32]*frameStream), datagrams: make(map[uint32]*frameDatagramStream)}
 	s.nextID.Store(1)
 	return s
 }
@@ -63,13 +60,46 @@
 		return ctx.Err()
 	default:
 	}
-	payload, err := json.Marshal(StreamOpenPayload{AgentID: req.AgentID, Protocol: req.Protocol, TargetHost: req.TargetHost, TargetPort: req.TargetPort, Metadata: req.Metadata})
+	payload, err := protocol.EncodeStreamOpenPayload(protocol.StreamOpenPayload{AgentID: req.AgentID, Protocol: req.Protocol, TargetHost: req.TargetHost, TargetPort: req.TargetPort, Metadata: req.Metadata})
 	if err != nil {
 		return err
 	}
 	return s.transport.Send(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: req.StreamID, Payload: payload})
 }
 
+// Start begins the single receive dispatcher even before a logical stream is
+// opened so connection-level PING frames are answered while the session is idle.
+func (s *Session) Start() {
+	if s == nil {
+		return
+	}
+	tr, ok := s.transport.(ReceiveTransport)
+	if !ok || tr == nil {
+		s.finishReceive(errors.New("client session transport does not receive frames"))
+		return
+	}
+	s.recvOnce.Do(func() { go s.receiveLoop(tr) })
+}
+
+func (s *Session) Wait(ctx context.Context) error {
+	if s == nil {
+		return ErrSessionClosed
+	}
+	if ctx == nil {
+		ctx = context.Background()
+	}
+	select {
+	case <-ctx.Done():
+		_ = s.Close()
+		return ctx.Err()
+	case <-s.done:
+		s.mu.RLock()
+		err := s.runErr
+		s.mu.RUnlock()
+		return err
+	}
+}
+
 // OpenDatagram opens a message-oriented UDP association. Each WriteDatagram
 // emits exactly one FrameData payload and each ReadDatagram returns exactly one
 // received FrameData payload.
@@ -96,7 +126,7 @@
 	}
 	s.datagrams[req.StreamID] = stream
 	s.mu.Unlock()
-	s.recvOnce.Do(func() { go s.receiveLoop(tr) })
+	s.Start()
 	if err := s.OpenStream(ctx, req); err != nil {
 		s.removeDatagram(req.StreamID)
 		return nil, err
@@ -134,7 +164,7 @@
 	}
 	s.streams[req.StreamID] = stream
 	s.mu.Unlock()
-	s.recvOnce.Do(func() { go s.receiveLoop(tr) })
+	s.Start()
 	if err := s.OpenStream(ctx, req); err != nil {
 		s.removeStream(req.StreamID)
 		return nil, err
@@ -180,8 +210,19 @@
 				delete(s.datagrams, id)
 			}
 			s.mu.Unlock()
+			s.finishReceive(err)
 			return
 		}
+		if f.Type == protocol.FramePing {
+			if err := tr.Send(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FramePong, Payload: append([]byte(nil), f.Payload...)}); err != nil {
+				s.finishReceive(err)
+				return
+			}
+			continue
+		}
+		if f.Type == protocol.FramePong {
+			continue
+		}
 		s.mu.RLock()
 		stream := s.streams[f.StreamID]
 		datagram := s.datagrams[f.StreamID]
@@ -199,7 +240,6 @@
 		case protocol.FrameHalfClose:
 			if stream != nil {
 				stream.finish(io.EOF)
-				s.removeStream(f.StreamID)
 			} else {
 				datagram.finish(io.EOF)
 				s.removeDatagram(f.StreamID)
@@ -216,6 +256,18 @@
 	}
 }
 
+func (s *Session) finishReceive(err error) {
+	if s == nil {
+		return
+	}
+	s.doneOnce.Do(func() {
+		s.mu.Lock()
+		s.runErr = err
+		s.mu.Unlock()
+		close(s.done)
+	})
+}
+
 func (s *Session) removeDatagram(id uint32) {
 	s.mu.Lock()
 	delete(s.datagrams, id)
@@ -302,7 +354,7 @@
 	s.mu.Lock()
 	err := s.err
 	s.mu.Unlock()
-	if err != nil {
+	if err != nil && !errors.Is(err, io.EOF) {
 		return 0, err
 	}
 	s.session.mu.RLock()
diff -ruN --exclude=*.__ABSENT__ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-before/internal/client/websocket.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/client/websocket.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-before/internal/client/websocket.go	1970-01-01 08:00:00
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/client/websocket.go	2026-09-06 17:56:59
@@ -0,0 +1,174 @@
+package client
+
+import (
+	"bytes"
+	"context"
+	"errors"
+	"fmt"
+	"math/rand"
+	"net/url"
+	"strings"
+	"sync"
+	"time"
+
+	"golang.org/x/net/websocket"
+
+	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
+)
+
+var (
+	ErrClientServerURLRequired = errors.New("client: server URL must be an absolute ws:// or wss:// URL")
+	ErrClientTokenRequired     = errors.New("client: bearer token is required")
+)
+
+type WebSocketRunOptions struct {
+	BaseBackoff time.Duration
+	MaxBackoff  time.Duration
+	Rand        *rand.Rand
+}
+
+func RunWebSocket(ctx context.Context, serverURL, token string, onReady func(*Session) error) error {
+	return RunWebSocketWithOptions(ctx, serverURL, token, onReady, WebSocketRunOptions{})
+}
+
+func RunWebSocketWithOptions(ctx context.Context, serverURL, token string, onReady func(*Session) error, options WebSocketRunOptions) error {
+	if _, err := parseClientWebSocketURL(serverURL); err != nil {
+		return err
+	}
+	if strings.TrimSpace(token) == "" {
+		return ErrClientTokenRequired
+	}
+	if ctx == nil {
+		ctx = context.Background()
+	}
+	base := options.BaseBackoff
+	if base <= 0 {
+		base = time.Second
+	}
+	max := options.MaxBackoff
+	if max <= 0 {
+		max = 30 * time.Second
+	}
+	rng := options.Rand
+	if rng == nil {
+		rng = rand.New(rand.NewSource(time.Now().UnixNano()))
+	}
+	attempt := 0
+	for {
+		if err := ctx.Err(); err != nil {
+			return err
+		}
+		transport, err := DialWebSocket(ctx, serverURL, token)
+		if err == nil {
+			session := NewSession(transport)
+			session.Start()
+			if onReady != nil {
+				if readyErr := onReady(session); readyErr != nil {
+					_ = session.Close()
+					return readyErr
+				}
+			}
+			err = session.Wait(ctx)
+			_ = session.Close()
+			if ctx.Err() != nil {
+				return ctx.Err()
+			}
+		}
+		attempt++
+		delay := clientReconnectDelay(base, max, attempt, rng)
+		timer := time.NewTimer(delay)
+		select {
+		case <-ctx.Done():
+			timer.Stop()
+			return ctx.Err()
+		case <-timer.C:
+		}
+	}
+}
+
+func DialWebSocket(ctx context.Context, serverURL, token string) (ReceiveTransport, error) {
+	if strings.TrimSpace(token) == "" {
+		return nil, ErrClientTokenRequired
+	}
+	u, err := parseClientWebSocketURL(serverURL)
+	if err != nil {
+		return nil, err
+	}
+	if ctx == nil {
+		ctx = context.Background()
+	}
+	config, err := websocket.NewConfig(u.String(), clientOriginFor(u))
+	if err != nil {
+		return nil, err
+	}
+	config.Header.Set("Authorization", "Bearer "+token)
+	conn, err := config.DialContext(ctx)
+	if err != nil {
+		return nil, err
+	}
+	conn.MaxPayloadBytes = protocol.MaxPayload + 16
+	return &clientWebSocketTransport{conn: conn}, nil
+}
+
+func parseClientWebSocketURL(raw string) (*url.URL, error) {
+	u, err := url.Parse(strings.TrimSpace(raw))
+	if err != nil || (u.Scheme != "ws" && u.Scheme != "wss") || u.Host == "" || u.User != nil || u.Fragment != "" || !u.IsAbs() {
+		return nil, fmt.Errorf("%w: %q", ErrClientServerURLRequired, raw)
+	}
+	return u, nil
+}
+
+func clientOriginFor(u *url.URL) string {
+	scheme := "http"
+	if u.Scheme == "wss" {
+		scheme = "https"
+	}
+	return (&url.URL{Scheme: scheme, Host: u.Host}).String()
+}
+
+func clientReconnectDelay(base, max time.Duration, attempt int, rng *rand.Rand) time.Duration {
+	delay := base
+	for i := 1; i < attempt && delay < max; i++ {
+		delay *= 2
+	}
+	if delay > max {
+		delay = max
+	}
+	return time.Duration(float64(delay) * (0.8 + rng.Float64()*0.4))
+}
+
+type clientWebSocketTransport struct {
+	conn *websocket.Conn
+	mu   sync.Mutex
+}
+
+func (t *clientWebSocketTransport) Send(frame protocol.Frame) error {
+	if t == nil || t.conn == nil {
+		return ErrSessionClosed
+	}
+	var payload bytes.Buffer
+	if err := protocol.NewEncoder(&payload).WriteFrame(frame); err != nil {
+		return err
+	}
+	t.mu.Lock()
+	defer t.mu.Unlock()
+	return websocket.Message.Send(t.conn, payload.Bytes())
+}
+
+func (t *clientWebSocketTransport) Receive() (protocol.Frame, error) {
+	if t == nil || t.conn == nil {
+		return protocol.Frame{}, ErrSessionClosed
+	}
+	var payload []byte
+	if err := websocket.Message.Receive(t.conn, &payload); err != nil {
+		return protocol.Frame{}, err
+	}
+	return protocol.NewDecoder(bytes.NewReader(payload)).ReadFrame()
+}
+
+func (t *clientWebSocketTransport) Close() error {
+	if t == nil || t.conn == nil {
+		return nil
+	}
+	return t.conn.Close()
+}
diff -ruN --exclude=*.__ABSENT__ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-before/internal/client/websocket_test.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/client/websocket_test.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-before/internal/client/websocket_test.go	1970-01-01 08:00:00
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/client/websocket_test.go	2026-09-06 17:56:59
@@ -0,0 +1,163 @@
+package client
+
+import (
+	"bufio"
+	"bytes"
+	"context"
+	"errors"
+	"math/rand"
+	"net"
+	"net/http"
+	"net/http/httptest"
+	"strings"
+	"sync/atomic"
+	"testing"
+	"time"
+
+	"golang.org/x/net/websocket"
+
+	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
+)
+
+func TestClientWSRejectsNonWebSocketURLAndMissingToken(t *testing.T) {
+	if err := RunWebSocket(context.Background(), "https://server.example/ws/client", "token", nil); !errors.Is(err, ErrClientServerURLRequired) {
+		t.Fatalf("RunWebSocket(https) error = %v, want ErrClientServerURLRequired", err)
+	}
+	if err := RunWebSocket(context.Background(), "wss://server.example/ws/client", "", nil); !errors.Is(err, ErrClientTokenRequired) {
+		t.Fatalf("RunWebSocket(empty token) error = %v, want ErrClientTokenRequired", err)
+	}
+}
+
+func TestClientWSUsesOnlyAuthorizationAndReconnectsAfterSessionEOF(t *testing.T) {
+	ctx, cancel := context.WithCancel(context.Background())
+	defer cancel()
+	var connections atomic.Int32
+	server := httptest.NewServer(websocket.Server{
+		Handshake: func(_ *websocket.Config, r *http.Request) error {
+			if got := r.Header.Get("Authorization"); got != "Bearer client-secret" {
+				return errors.New("missing bearer authorization")
+			}
+			if r.URL.RawQuery != "" || len(r.Cookies()) != 0 {
+				return errors.New("credential leaked outside authorization")
+			}
+			return nil
+		},
+		Handler: func(conn *websocket.Conn) {
+			defer conn.Close()
+			connections.Add(1)
+			_ = websocket.Message.Send(conn, clientFrameBytes(t, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FramePing, Payload: []byte("heartbeat")}))
+			var raw []byte
+			if err := websocket.Message.Receive(conn, &raw); err != nil {
+				return
+			}
+			frame, err := protocol.NewDecoder(bytes.NewReader(raw)).ReadFrame()
+			if err != nil || frame.Type != protocol.FramePong || string(frame.Payload) != "heartbeat" {
+				return
+			}
+		},
+	})
+	defer server.Close()
+
+	ready := make(chan struct{}, 2)
+	errCh := make(chan error, 1)
+	go func() {
+		errCh <- RunWebSocketWithOptions(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/ws/client", "client-secret", func(session *Session) error {
+			if session == nil {
+				return errors.New("nil session")
+			}
+			ready <- struct{}{}
+			if len(ready) == 2 {
+				cancel()
+			}
+			return nil
+		}, WebSocketRunOptions{BaseBackoff: 5 * time.Millisecond, MaxBackoff: 10 * time.Millisecond, Rand: rand.New(rand.NewSource(1))})
+	}()
+
+	for i := 0; i < 2; i++ {
+		select {
+		case <-ready:
+		case <-time.After(2 * time.Second):
+			t.Fatalf("ready callbacks = %d, connections = %d", i, connections.Load())
+		}
+	}
+	cancel()
+	select {
+	case err := <-errCh:
+		if !errors.Is(err, context.Canceled) {
+			t.Fatalf("RunWebSocket() error = %v, want context canceled", err)
+		}
+	case <-time.After(time.Second):
+		t.Fatal("RunWebSocket did not stop after context cancellation")
+	}
+}
+
+func TestClientWSReconnectBackoffGrowsAcrossRepeatedDisconnects(t *testing.T) {
+	ctx, cancel := context.WithCancel(context.Background())
+	defer cancel()
+	server := httptest.NewServer(websocket.Handler(func(conn *websocket.Conn) {
+		_ = conn.Close()
+	}))
+	defer server.Close()
+	readyAt := make(chan time.Time, 3)
+	errCh := make(chan error, 1)
+	go func() {
+		errCh <- RunWebSocketWithOptions(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/ws/client", "client-secret", func(*Session) error {
+			readyAt <- time.Now()
+			return nil
+		}, WebSocketRunOptions{BaseBackoff: 20 * time.Millisecond, MaxBackoff: 200 * time.Millisecond, Rand: rand.New(rand.NewSource(1))})
+	}()
+	times := make([]time.Time, 3)
+	for i := range times {
+		select {
+		case times[i] = <-readyAt:
+		case <-time.After(2 * time.Second):
+			t.Fatalf("ready callbacks = %d", i)
+		}
+	}
+	cancel()
+	<-errCh
+	first := times[1].Sub(times[0])
+	second := times[2].Sub(times[1])
+	if second <= first+first/2 {
+		t.Fatalf("reconnect gaps = %v then %v, want exponential growth", first, second)
+	}
+}
+
+func TestClientWSDialerSendsNoCookieOrQueryToken(t *testing.T) {
+	listener, err := net.Listen("tcp", "127.0.0.1:0")
+	if err != nil {
+		t.Fatal(err)
+	}
+	defer listener.Close()
+	requestCh := make(chan *http.Request, 1)
+	go func() {
+		conn, acceptErr := listener.Accept()
+		if acceptErr != nil {
+			return
+		}
+		defer conn.Close()
+		req, readErr := http.ReadRequest(bufio.NewReader(conn))
+		if readErr == nil {
+			requestCh <- req
+		}
+		_, _ = conn.Write([]byte("HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\n\r\n"))
+	}()
+	_, _ = DialWebSocket(context.Background(), "ws://"+listener.Addr().String()+"/ws/client", "client-secret")
+	select {
+	case request := <-requestCh:
+		if request.Header.Get("Authorization") != "Bearer client-secret" || request.URL.RawQuery != "" || request.Header.Get("Cookie") != "" {
+			t.Fatalf("request authorization=%q query=%q cookie=%q", request.Header.Get("Authorization"), request.URL.RawQuery, request.Header.Get("Cookie"))
+		}
+	case <-time.After(time.Second):
+		t.Fatal("dial request was not observed")
+	}
+}
+
+func clientFrameBytes(t *testing.T, frame protocol.Frame) []byte {
+	t.Helper()
+	var payload bytes.Buffer
+	if err := protocol.NewEncoder(&payload).WriteFrame(frame); err != nil {
+		t.Fatal(err)
+	}
+	return payload.Bytes()
+}
diff -ruN --exclude=*.__ABSENT__ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-before/internal/config/config.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/config/config.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-before/internal/config/config.go	2026-09-06 17:20:25
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/config/config.go	2026-09-06 17:56:59
@@ -146,6 +146,7 @@
 
 type ClientConfig struct {
 	ServerURL string         `mapstructure:"server_url" json:"server_url" yaml:"server_url"`
+	Token     string         `mapstructure:"token" json:"-" yaml:"-"`
 	Tunnels   []TunnelConfig `mapstructure:"tunnels" json:"tunnels" yaml:"tunnels"`
 }
 
@@ -316,7 +317,7 @@
 		"server.agent_ws_addr", "server.client_ws_addr", "server.tcp_bridge.enabled", "server.tcp_bridge_enabled",
 		"security.allowed_hosts", "security.allowed_origins", "security.allow_legacy_connection_tokens",
 		"tls.enabled", "tls.cert_file", "tls.key_file", "tls.min_version",
-		"agent.server_url", "agent.id", "agent.token", "client.server_url",
+		"agent.server_url", "agent.id", "agent.token", "client.server_url", "client.token",
 	}
 	for _, key := range keys {
 		_ = v.BindEnv(key)
diff -ruN --exclude=*.__ABSENT__ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-before/internal/config/config_test.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/config/config_test.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-before/internal/config/config_test.go	2026-09-06 17:56:30
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/config/config_test.go	2026-09-06 17:56:59
@@ -159,6 +159,31 @@
 	}
 }
 
+func TestLoadClientTokenPrecedenceAndRedaction(t *testing.T) {
+	dir := t.TempDir()
+	path := filepath.Join(dir, "client-token.yaml")
+	if err := os.WriteFile(path, []byte("client:\n  server_url: wss://file.example/ws/client\n  token: file-secret\n"), 0o600); err != nil {
+		t.Fatal(err)
+	}
+	t.Setenv("TUNNELMESH_CLIENT_TOKEN", "env-secret")
+	cfg, err := config.Load(context.Background(), config.ConfigOptions{ConfigFile: path, CLI: map[string]any{"client.token": "cli-secret"}})
+	if err != nil {
+		t.Fatal(err)
+	}
+	if cfg.Client.Token != "cli-secret" {
+		t.Fatalf("Client.Token = %q, want CLI value", cfg.Client.Token)
+	}
+	data, err := cfg.RedactedJSON()
+	if err != nil {
+		t.Fatal(err)
+	}
+	for _, secret := range []string{"file-secret", "env-secret", "cli-secret"} {
+		if strings.Contains(string(data), secret) {
+			t.Fatalf("redacted config leaked %q: %s", secret, data)
+		}
+	}
+}
+
 func TestLoadSecurityAndNativeTLSDefaults(t *testing.T) {
 	t.Setenv("TUNNELMESH_SECURITY_ALLOW_LEGACY_CONNECTION_TOKENS", "")
 	t.Setenv("TUNNELMESH_TLS_ENABLED", "")
diff -ruN --exclude=*.__ABSENT__ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-before/internal/protocol/stream_open.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/protocol/stream_open.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-before/internal/protocol/stream_open.go	1970-01-01 08:00:00
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/protocol/stream_open.go	2026-09-06 17:56:59
@@ -0,0 +1,36 @@
+package protocol
+
+import "encoding/json"
+
+// StreamOpenPayload is the single shared JSON model for OPEN_STREAM frames.
+// Authentication identity is deliberately absent: the server derives it from
+// the pre-upgrade bearer credential instead of trusting frame data.
+type StreamOpenPayload struct {
+	AgentID    string `json:"agent_id,omitempty"`
+	Protocol   string `json:"protocol"`
+	TargetHost string `json:"target_host"`
+	TargetPort int    `json:"target_port"`
+	Metadata   []byte `json:"metadata,omitempty"`
+}
+
+func EncodeStreamOpenPayload(payload StreamOpenPayload) ([]byte, error) {
+	encoded, err := json.Marshal(payload)
+	if err != nil {
+		return nil, err
+	}
+	if len(encoded) > MaxPayload {
+		return nil, ErrPayloadTooLarge
+	}
+	return encoded, nil
+}
+
+func DecodeStreamOpenPayload(encoded []byte) (StreamOpenPayload, error) {
+	if len(encoded) > MaxPayload {
+		return StreamOpenPayload{}, ErrPayloadTooLarge
+	}
+	var payload StreamOpenPayload
+	if err := json.Unmarshal(encoded, &payload); err != nil {
+		return StreamOpenPayload{}, err
+	}
+	return payload, nil
+}
diff -ruN --exclude=*.__ABSENT__ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-before/internal/protocol/stream_open_test.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/protocol/stream_open_test.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-before/internal/protocol/stream_open_test.go	1970-01-01 08:00:00
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/protocol/stream_open_test.go	2026-09-06 17:56:59
@@ -0,0 +1,25 @@
+package protocol_test
+
+import (
+	"encoding/json"
+	"testing"
+
+	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
+)
+
+func TestStreamOpenPayloadIsTheSharedWireModel(t *testing.T) {
+	payload, err := json.Marshal(protocol.StreamOpenPayload{
+		AgentID:    "agent-a",
+		Protocol:   "udp",
+		TargetHost: "10.0.0.8",
+		TargetPort: 5353,
+		Metadata:   []byte("dns"),
+	})
+	if err != nil {
+		t.Fatal(err)
+	}
+	const want = `{"agent_id":"agent-a","protocol":"udp","target_host":"10.0.0.8","target_port":5353,"metadata":"ZG5z"}`
+	if string(payload) != want {
+		t.Fatalf("payload = %s, want %s", payload, want)
+	}
+}
diff -ruN --exclude=*.__ABSENT__ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-before/internal/server/middleware.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/server/middleware.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-before/internal/server/middleware.go	2026-09-06 17:56:30
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/server/middleware.go	2026-09-06 17:56:59
@@ -15,6 +15,7 @@
 
 type principalContextKey struct{}
 type agentAuthenticationContextKey struct{}
+type clientPrincipalContextKey struct{}
 
 type agentConnectionAuthentication struct {
 	Identity  auth.TokenIdentity
@@ -44,6 +45,41 @@
 func agentAuthenticationFromContext(ctx context.Context) (agentConnectionAuthentication, bool) {
 	authentication, ok := ctx.Value(agentAuthenticationContextKey{}).(agentConnectionAuthentication)
 	return authentication, ok
+}
+
+func clientPrincipalFromContext(ctx context.Context) (ClientSessionPrincipal, bool) {
+	principal, ok := ctx.Value(clientPrincipalContextKey{}).(ClientSessionPrincipal)
+	return principal, ok && principal.ConnectionID != "" && principal.Identity.TokenID != ""
+}
+
+func clientWebSocketHandshake(security config.SecurityConfig, authenticate func(context.Context, string) (ClientSessionPrincipal, error)) func(*websocket.Config, *http.Request) error {
+	return func(wsConfig *websocket.Config, r *http.Request) error {
+		if len(security.AllowedHosts) > 0 && !exactHostAllowed(r.Host, security.AllowedHosts) {
+			return fmt.Errorf("websocket host is not allowed")
+		}
+		values := r.Header.Values("Origin")
+		if len(values) != 1 {
+			return fmt.Errorf("websocket origin is not allowed")
+		}
+		origin, normalized, ok := normalizeOrigin(values[0])
+		if !ok || (len(security.AllowedOrigins) > 0 && !containsNormalizedOrigin(normalized, security.AllowedOrigins)) {
+			return fmt.Errorf("websocket origin is not allowed")
+		}
+		raw := bearerToken(r)
+		if raw == "" || authenticate == nil {
+			return auth.ErrUnauthenticated
+		}
+		principal, err := authenticate(r.Context(), raw)
+		if err != nil {
+			return auth.ErrUnauthenticated
+		}
+		authenticatedRequest := r.WithContext(context.WithValue(r.Context(), clientPrincipalContextKey{}, principal))
+		authenticatedRequest.Header = r.Header.Clone()
+		authenticatedRequest.Header.Del("Authorization")
+		*r = *authenticatedRequest
+		wsConfig.Origin = origin
+		return nil
+	}
 }
 
 func agentWebSocketHandshake(security config.SecurityConfig, authenticate func(context.Context, string) (agentConnectionAuthentication, error)) func(*websocket.Config, *http.Request) error {
diff -ruN --exclude=*.__ABSENT__ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-before/internal/server/runtime.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/server/runtime.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-before/internal/server/runtime.go	2026-09-06 17:20:25
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/server/runtime.go	2026-09-06 17:56:59
@@ -2,6 +2,8 @@
 
 import (
 	"context"
+	"crypto/rand"
+	"encoding/hex"
 	"errors"
 	"log/slog"
 	"net"
@@ -14,14 +16,16 @@
 	"github.com/tunnelmesh/tunnelmesh/internal/auth"
 	"github.com/tunnelmesh/tunnelmesh/internal/config"
 	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
+	"github.com/tunnelmesh/tunnelmesh/internal/relay"
 	"github.com/tunnelmesh/tunnelmesh/internal/storage"
 )
 
 var ErrRuntimeDatabaseRequired = errors.New("server runtime: database is required")
 
 const (
-	agentHelloTimeout             = time.Second
-	agentWebSocketMaxPayloadBytes = protocol.MaxPayload + 16
+	agentHelloTimeout              = time.Second
+	agentWebSocketMaxPayloadBytes  = protocol.MaxPayload + 16
+	clientWebSocketMaxPayloadBytes = protocol.MaxPayload + 16
 )
 
 type RuntimeConfig struct {
@@ -32,12 +36,15 @@
 // ServerRuntime is the process-scoped server wiring shared by HTTP handlers
 // and authenticated Agent WebSocket handlers. The caller owns DB lifecycle.
 type ServerRuntime struct {
-	DB            *storage.DB
-	AgentSessions *AgentSessionManager
-	API           *API
-	Auth          *auth.AuthService
-	Credentials   *auth.CredentialService
-	config        RuntimeConfig
+	DB               *storage.DB
+	AgentSessions    *AgentSessionManager
+	ClientSessions   *ClientSessionManager
+	API              *API
+	Auth             *auth.AuthService
+	Credentials      *auth.CredentialService
+	ClientAuthorizer StreamAuthorizer
+	ClientTransport  relay.NodeTransport
+	config           RuntimeConfig
 }
 
 // NewServerRuntime creates the server runtime with durable Agent metadata
@@ -55,7 +62,7 @@
 		runtimeConfig.Security.AllowedHosts = append([]string(nil), runtimeConfig.Security.AllowedHosts...)
 		runtimeConfig.Security.AllowedOrigins = append([]string(nil), runtimeConfig.Security.AllowedOrigins...)
 	}
-	return &ServerRuntime{DB: db, AgentSessions: NewAgentSessionManagerWithMetadata(db.Metadata(), cfg), API: NewAPI(db, authService), Auth: authService, Credentials: credentials, config: runtimeConfig}, nil
+	return &ServerRuntime{DB: db, AgentSessions: NewAgentSessionManagerWithMetadata(db.Metadata(), cfg), ClientSessions: NewClientSessionManager(), API: NewAPI(db, authService), Auth: authService, Credentials: credentials, ClientAuthorizer: NewCredentialStreamAuthorizer(credentials), config: runtimeConfig}, nil
 }
 
 // Close releases runtime-owned background workers. The caller continues to
@@ -74,11 +81,52 @@
 	if r == nil {
 		return http.NotFoundHandler()
 	}
-	return NewWebHandler(r.API.Handler(), r.agentWebSocketHandler())
+	return NewWebHandler(r.API.Handler(), r.agentWebSocketHandler(), r.clientWebSocketHandler())
 }
 
 func (r *ServerRuntime) agentWebSocketHandler() http.Handler {
 	return websocket.Server{Handler: r.serveAgentWS, Handshake: agentWebSocketHandshake(r.config.Security, r.preauthenticateAgentConnection)}
+}
+
+func (r *ServerRuntime) clientWebSocketHandler() http.Handler {
+	return websocket.Server{Handler: r.serveClientWS, Handshake: clientWebSocketHandshake(r.config.Security, r.preauthenticateClientConnection)}
+}
+
+func (r *ServerRuntime) preauthenticateClientConnection(ctx context.Context, raw string) (ClientSessionPrincipal, error) {
+	identity, err := r.Credentials.ValidateAs(ctx, raw, storage.TokenTypeClient)
+	if err != nil {
+		return ClientSessionPrincipal{}, auth.ErrUnauthenticated
+	}
+	connectionID, err := newClientConnectionID()
+	if err != nil {
+		return ClientSessionPrincipal{}, auth.ErrUnauthenticated
+	}
+	return ClientSessionPrincipal{ConnectionID: connectionID, Identity: identity}, nil
+}
+
+func newClientConnectionID() (string, error) {
+	var entropy [16]byte
+	if _, err := rand.Read(entropy[:]); err != nil {
+		return "", err
+	}
+	return "client_" + hex.EncodeToString(entropy[:]), nil
+}
+
+func (r *ServerRuntime) serveClientWS(conn *websocket.Conn) {
+	ctx := context.Background()
+	if conn.Request() != nil {
+		ctx = conn.Request().Context()
+	}
+	principal, ok := clientPrincipalFromContext(ctx)
+	if !ok {
+		_ = conn.Close()
+		return
+	}
+	conn.MaxPayloadBytes = clientWebSocketMaxPayloadBytes
+	transport := NewWSFrameTransport(&xNetWSFrameConn{conn: conn})
+	r.ClientSessions.Register(principal.ConnectionID, transport)
+	defer r.ClientSessions.Remove(principal.ConnectionID)
+	_ = ServeClientSession(ctx, principal, transport, r.ClientAuthorizer, r.ClientTransport)
 }
 
 func (r *ServerRuntime) serveAgentWS(conn *websocket.Conn) {
diff -ruN --exclude=*.__ABSENT__ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-before/internal/server/session_manager.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/server/session_manager.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-before/internal/server/session_manager.go	2026-09-06 17:20:25
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/server/session_manager.go	2026-09-06 17:56:59
@@ -498,13 +498,7 @@
 	TargetPort           int
 	Metadata             []byte
 }
-type StreamOpenPayload struct {
-	AgentID    string `json:"agent_id,omitempty"`
-	Protocol   string `json:"protocol"`
-	TargetHost string `json:"target_host"`
-	TargetPort int    `json:"target_port"`
-	Metadata   []byte `json:"metadata,omitempty"`
-}
+type StreamOpenPayload = protocol.StreamOpenPayload
 type ClientSessionManager struct {
 	mu       sync.RWMutex
 	sessions map[string]FrameTransport
@@ -549,7 +543,7 @@
 		return ctx.Err()
 	default:
 	}
-	payload, err := json.Marshal(StreamOpenPayload{Protocol: req.Protocol, TargetHost: req.TargetHost, TargetPort: req.TargetPort, Metadata: req.Metadata})
+	payload, err := protocol.EncodeStreamOpenPayload(protocol.StreamOpenPayload{Protocol: req.Protocol, TargetHost: req.TargetHost, TargetPort: req.TargetPort, Metadata: req.Metadata})
 	if err != nil {
 		return err
 	}
diff -ruN --exclude=*.__ABSENT__ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-before/internal/server/stream_authorizer.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/server/stream_authorizer.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-before/internal/server/stream_authorizer.go	1970-01-01 08:00:00
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/server/stream_authorizer.go	2026-09-06 17:56:59
@@ -0,0 +1,43 @@
+package server
+
+import (
+	"context"
+
+	"github.com/tunnelmesh/tunnelmesh/internal/auth"
+	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
+)
+
+type ClientRegistration struct {
+	Token string
+}
+
+// ClientSessionPrincipal retains only non-secret identity derived during the
+// HTTP upgrade. Raw bearer tokens never enter session state.
+type ClientSessionPrincipal struct {
+	ConnectionID string
+	Identity     auth.TokenIdentity
+}
+
+type StreamAuthorizer interface {
+	Authorize(context.Context, ClientSessionPrincipal, protocol.StreamOpenPayload) error
+}
+
+type CredentialStreamAuthorizer struct {
+	credentials *auth.CredentialService
+}
+
+func NewCredentialStreamAuthorizer(credentials *auth.CredentialService) *CredentialStreamAuthorizer {
+	return &CredentialStreamAuthorizer{credentials: credentials}
+}
+
+func (a *CredentialStreamAuthorizer) Authorize(ctx context.Context, principal ClientSessionPrincipal, request protocol.StreamOpenPayload) error {
+	if a == nil || a.credentials == nil || principal.ConnectionID == "" {
+		return auth.ErrForbidden
+	}
+	return a.credentials.AuthorizeStream(ctx, principal.Identity, auth.StreamAuthorizationRequest{
+		AgentID:    request.AgentID,
+		Protocol:   request.Protocol,
+		TargetHost: request.TargetHost,
+		TargetPort: request.TargetPort,
+	})
+}
diff -ruN --exclude=*.__ABSENT__ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-before/internal/server/stream_authorizer_test.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/server/stream_authorizer_test.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-before/internal/server/stream_authorizer_test.go	1970-01-01 08:00:00
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/server/stream_authorizer_test.go	2026-09-06 17:56:59
@@ -0,0 +1,61 @@
+package server
+
+import (
+	"context"
+	"errors"
+	"testing"
+	"time"
+
+	"github.com/tunnelmesh/tunnelmesh/internal/auth"
+	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
+	"github.com/tunnelmesh/tunnelmesh/internal/storage"
+)
+
+func TestStreamAuthorizerRechecksTokenScopeAndAgentPolicyForEveryOpen(t *testing.T) {
+	ctx := context.Background()
+	db, err := storage.OpenSQLite(ctx, "file:stream-authorizer?mode=memory&cache=shared")
+	if err != nil {
+		t.Fatal(err)
+	}
+	defer db.Close()
+	authService := auth.NewAuthService(db)
+	owner, err := authService.CreateUser(ctx, "stream-owner", "stream-password", "user")
+	if err != nil {
+		t.Fatal(err)
+	}
+	if err := db.Agents().Create(ctx, storage.Agent{ID: "agent-a", Name: "agent-a", OwnerUserID: owner.ID, Enabled: true}); err != nil {
+		t.Fatal(err)
+	}
+	now := time.Now().UTC()
+	if err := db.Policies().Create(ctx, storage.AgentPolicy{ID: "allow-ssh", AgentID: "agent-a", TargetHost: "10.0.0.8", TargetPort: 22, Protocol: "tcp", AllowedCIDRs: "10.0.0.0/24", AllowedPorts: "22", CreatedAt: now, UpdatedAt: now}); err != nil {
+		t.Fatal(err)
+	}
+	credentials := auth.NewCredentialService(db)
+	defer credentials.Close()
+	created, err := credentials.Create(ctx, auth.CreateTokenInput{Type: storage.TokenTypeClient, OwnerUserID: owner.ID, Scope: auth.TokenScope{AgentIDs: []string{"agent-a"}, Protocols: []string{"tcp"}, TargetCIDRs: []string{"10.0.0.0/24"}, TargetPorts: []int{22}}})
+	if err != nil {
+		t.Fatal(err)
+	}
+	identity, err := credentials.ValidateAs(ctx, created.Secret, storage.TokenTypeClient)
+	if err != nil {
+		t.Fatal(err)
+	}
+	authorizer := NewCredentialStreamAuthorizer(credentials)
+	principal := ClientSessionPrincipal{ConnectionID: "connection-a", Identity: identity}
+	allowed := protocol.StreamOpenPayload{AgentID: "agent-a", Protocol: "tcp", TargetHost: "10.0.0.8", TargetPort: 22}
+	if err := authorizer.Authorize(ctx, principal, allowed); err != nil {
+		t.Fatalf("Authorize(allowed) error = %v", err)
+	}
+
+	denied := allowed
+	denied.TargetPort = 23
+	if err := authorizer.Authorize(ctx, principal, denied); !errors.Is(err, auth.ErrForbidden) {
+		t.Fatalf("Authorize(policy denied) error = %v, want ErrForbidden", err)
+	}
+	if err := credentials.Revoke(ctx, created.TokenID); err != nil {
+		t.Fatal(err)
+	}
+	if err := authorizer.Authorize(ctx, principal, allowed); !errors.Is(err, auth.ErrForbidden) {
+		t.Fatalf("Authorize(after revoke) error = %v, want ErrForbidden", err)
+	}
+}
diff -ruN --exclude=*.__ABSENT__ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-before/internal/server/web.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/server/web.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-before/internal/server/web.go	2026-09-06 17:56:30
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/server/web.go	2026-09-06 17:56:59
@@ -14,7 +14,7 @@
 //go:embed web_dist
 var webDist embed.FS
 
-func NewWebHandler(api http.Handler, ws http.Handler) http.Handler {
+func NewWebHandler(api http.Handler, ws http.Handler, clientWS ...http.Handler) http.Handler {
 	root, _ := fs.Sub(webDist, "web_dist")
 	static := http.FileServer(http.FS(root))
 	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
@@ -31,6 +31,14 @@
 				http.NotFound(w, r)
 			} else {
 				ws.ServeHTTP(w, r)
+			}
+			return
+		}
+		if r.URL.Path == "/ws/client" {
+			if len(clientWS) == 0 || clientWS[0] == nil {
+				http.NotFound(w, r)
+			} else {
+				clientWS[0].ServeHTTP(w, r)
 			}
 			return
 		}
diff -ruN --exclude=*.__ABSENT__ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-before/internal/server/ws_client.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/server/ws_client.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-before/internal/server/ws_client.go	2026-09-06 17:20:25
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/server/ws_client.go	2026-09-06 17:56:59
@@ -1,8 +1,17 @@
 package server
 
-// Client WebSocket handling shares the versioned frame transport with agents.
-// Keeping this adapter separate makes authentication and routing handlers easy
-// to evolve without coupling them to a WebSocket implementation.
+import (
+	"context"
+	"errors"
+	"io"
+	"sync"
+
+	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
+	"github.com/tunnelmesh/tunnelmesh/internal/relay"
+)
+
+const clientStreamResetMessage = "stream rejected"
+
 type ClientWSHandler struct{ Sessions *ClientSessionManager }
 
 func (h *ClientWSHandler) Attach(id string, c WSConn) *WSFrameTransport {
@@ -11,4 +20,186 @@
 		h.Sessions.Register(id, tr)
 	}
 	return tr
+}
+
+type clientReceiveTransport interface {
+	FrameTransport
+	Receive() (protocol.Frame, error)
+}
+
+type clientRelayStream struct {
+	conn             io.ReadWriteCloser
+	clientHalfClosed bool
+	relayHalfClosed  bool
+}
+
+// ServeClientSession multiplexes one authenticated Client connection onto the
+// existing relay transport. Authorization is re-evaluated for every OPEN.
+func ServeClientSession(ctx context.Context, principal ClientSessionPrincipal, tr clientReceiveTransport, authorizer StreamAuthorizer, opener relay.NodeTransport) error {
+	if tr == nil || principal.ConnectionID == "" {
+		return ErrSessionClosed
+	}
+	if ctx == nil {
+		ctx = context.Background()
+	}
+	var mu sync.Mutex
+	streams := make(map[uint32]*clientRelayStream)
+	closeStream := func(id uint32) {
+		mu.Lock()
+		stream := streams[id]
+		delete(streams, id)
+		mu.Unlock()
+		if stream != nil {
+			_ = stream.conn.Close()
+		}
+	}
+	defer func() {
+		mu.Lock()
+		remaining := make([]io.Closer, 0, len(streams))
+		for id, stream := range streams {
+			delete(streams, id)
+			remaining = append(remaining, stream.conn)
+		}
+		mu.Unlock()
+		for _, stream := range remaining {
+			_ = stream.Close()
+		}
+	}()
+	reset := func(id uint32) error {
+		if id == 0 {
+			return nil
+		}
+		return tr.Send(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameReset, StreamID: id, Payload: []byte(clientStreamResetMessage)})
+	}
+	for {
+		frame, err := tr.Receive()
+		if err != nil {
+			if errors.Is(err, io.EOF) {
+				return nil
+			}
+			return err
+		}
+		if err := frame.Validate(); err != nil {
+			if frame.StreamID != 0 {
+				_ = reset(frame.StreamID)
+				continue
+			}
+			return err
+		}
+		switch frame.Type {
+		case protocol.FramePing:
+			if err := tr.Send(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FramePong, Payload: append([]byte(nil), frame.Payload...)}); err != nil {
+				return err
+			}
+		case protocol.FramePong:
+			continue
+		case protocol.FrameGoAway:
+			return nil
+		case protocol.FrameOpenStream:
+			mu.Lock()
+			_, duplicate := streams[frame.StreamID]
+			mu.Unlock()
+			if duplicate {
+				_ = reset(frame.StreamID)
+				continue
+			}
+			request, decodeErr := protocol.DecodeStreamOpenPayload(frame.Payload)
+			if decodeErr != nil || authorizer == nil || authorizer.Authorize(ctx, principal, request) != nil || opener == nil {
+				_ = reset(frame.StreamID)
+				continue
+			}
+			conn, openErr := opener.OpenStream(ctx, relay.StreamRequest{StreamID: frame.StreamID, AgentID: request.AgentID, Protocol: request.Protocol, TargetHost: request.TargetHost, TargetPort: request.TargetPort, Metadata: append([]byte(nil), request.Metadata...)})
+			if openErr != nil || conn == nil {
+				_ = reset(frame.StreamID)
+				continue
+			}
+			stream := &clientRelayStream{conn: conn}
+			mu.Lock()
+			streams[frame.StreamID] = stream
+			mu.Unlock()
+			go relayToClient(frame.StreamID, stream, tr, &mu, streams)
+		case protocol.FrameData:
+			mu.Lock()
+			stream := streams[frame.StreamID]
+			mu.Unlock()
+			if stream == nil || stream.clientHalfClosed {
+				_ = reset(frame.StreamID)
+				continue
+			}
+			written, err := stream.conn.Write(frame.Payload)
+			if err == nil && written != len(frame.Payload) {
+				err = io.ErrShortWrite
+			}
+			if err != nil {
+				closeStream(frame.StreamID)
+				_ = reset(frame.StreamID)
+			}
+		case protocol.FrameHalfClose:
+			mu.Lock()
+			stream := streams[frame.StreamID]
+			if stream != nil {
+				stream.clientHalfClosed = true
+			}
+			mu.Unlock()
+			if stream == nil {
+				_ = reset(frame.StreamID)
+				continue
+			}
+			if halfCloser, ok := stream.conn.(interface{ CloseWrite() error }); ok {
+				_ = halfCloser.CloseWrite()
+			}
+			mu.Lock()
+			complete := stream.relayHalfClosed
+			mu.Unlock()
+			if complete {
+				closeStream(frame.StreamID)
+			}
+		case protocol.FrameReset:
+			mu.Lock()
+			_, exists := streams[frame.StreamID]
+			mu.Unlock()
+			if !exists {
+				_ = reset(frame.StreamID)
+				continue
+			}
+			closeStream(frame.StreamID)
+		default:
+			_ = reset(frame.StreamID)
+		}
+	}
+}
+
+func relayToClient(id uint32, stream *clientRelayStream, tr FrameTransport, mu *sync.Mutex, streams map[uint32]*clientRelayStream) {
+	buffer := make([]byte, 32<<10)
+	for {
+		n, err := stream.conn.Read(buffer)
+		if n > 0 {
+			if sendErr := tr.Send(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: id, Payload: append([]byte(nil), buffer[:n]...)}); sendErr != nil {
+				_ = stream.conn.Close()
+				return
+			}
+		}
+		if err != nil {
+			mu.Lock()
+			current := streams[id]
+			if current == stream {
+				stream.relayHalfClosed = true
+			}
+			complete := stream.clientHalfClosed
+			mu.Unlock()
+			if current != stream {
+				return
+			}
+			_ = tr.Send(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameHalfClose, StreamID: id})
+			if complete || !errors.Is(err, io.EOF) {
+				mu.Lock()
+				if streams[id] == stream {
+					delete(streams, id)
+				}
+				mu.Unlock()
+				_ = stream.conn.Close()
+			}
+			return
+		}
+	}
 }
diff -ruN --exclude=*.__ABSENT__ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-before/internal/server/ws_client_test.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/server/ws_client_test.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-before/internal/server/ws_client_test.go	1970-01-01 08:00:00
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-after/internal/server/ws_client_test.go	2026-09-06 17:56:59
@@ -0,0 +1,395 @@
+package server
+
+import (
+	"bufio"
+	"bytes"
+	"context"
+	"errors"
+	"io"
+	"net"
+	"net/http"
+	"sync"
+	"testing"
+	"time"
+
+	"golang.org/x/net/websocket"
+
+	"github.com/tunnelmesh/tunnelmesh/internal/auth"
+	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
+	"github.com/tunnelmesh/tunnelmesh/internal/relay"
+	"github.com/tunnelmesh/tunnelmesh/internal/storage"
+)
+
+func TestClientWSRouteIsExactAndRejectsWrongCredentialTypesBeforeUpgrade(t *testing.T) {
+	ctx := context.Background()
+	db, err := storage.OpenSQLite(ctx, "file:client-ws-auth?mode=memory&cache=shared")
+	if err != nil {
+		t.Fatal(err)
+	}
+	defer db.Close()
+	authService := auth.NewAuthService(db)
+	owner, err := authService.CreateUser(ctx, "client-ws-owner", "client-ws-password", "user")
+	if err != nil {
+		t.Fatal(err)
+	}
+	if err := db.Agents().Create(ctx, storage.Agent{ID: "client-ws-agent", Name: "client-ws-agent", OwnerUserID: owner.ID, Enabled: true}); err != nil {
+		t.Fatal(err)
+	}
+	management, err := authService.Login(ctx, owner.Username, "client-ws-password")
+	if err != nil {
+		t.Fatal(err)
+	}
+	credentials := auth.NewCredentialService(db)
+	clientToken, err := credentials.Create(ctx, auth.CreateTokenInput{Type: storage.TokenTypeClient, OwnerUserID: owner.ID})
+	if err != nil {
+		t.Fatal(err)
+	}
+	agentToken, err := credentials.Create(ctx, auth.CreateTokenInput{Type: storage.TokenTypeAgent, OwnerUserID: owner.ID, AgentID: "client-ws-agent"})
+	if err != nil {
+		t.Fatal(err)
+	}
+	if err := db.Nodes().Create(ctx, storage.ServerNode{ID: "client-ws-node", Address: "127.0.0.1:1", Epoch: 1}); err != nil {
+		t.Fatal(err)
+	}
+	nodeToken, err := credentials.Create(ctx, auth.CreateTokenInput{Type: storage.TokenTypeServerNode, OwnerUserID: owner.ID, NodeID: "client-ws-node"})
+	if err != nil {
+		t.Fatal(err)
+	}
+	revoked, err := credentials.Create(ctx, auth.CreateTokenInput{Type: storage.TokenTypeClient, OwnerUserID: owner.ID})
+	if err != nil {
+		t.Fatal(err)
+	}
+	if err := credentials.Revoke(ctx, revoked.TokenID); err != nil {
+		t.Fatal(err)
+	}
+	future := time.Now().UTC().Add(time.Hour)
+	expired, err := credentials.Create(ctx, auth.CreateTokenInput{Type: storage.TokenTypeClient, OwnerUserID: owner.ID, ExpiresAt: &future})
+	if err != nil {
+		t.Fatal(err)
+	}
+	if _, err := db.SQL().ExecContext(ctx, `UPDATE service_tokens SET expires_at=? WHERE id=?`, time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano), expired.TokenID); err != nil {
+		t.Fatal(err)
+	}
+	_ = credentials.Close()
+
+	runtime, address, stop := startClientWSTestRuntime(t, db)
+	defer stop()
+	_ = runtime
+	for _, tc := range []struct {
+		name  string
+		token string
+	}{
+		{name: "missing"},
+		{name: "management", token: management.Token},
+		{name: "agent", token: agentToken.Secret},
+		{name: "server node", token: nodeToken.Secret},
+		{name: "revoked", token: revoked.Secret},
+		{name: "expired", token: expired.Secret},
+	} {
+		t.Run(tc.name, func(t *testing.T) {
+			if status := rawClientWebSocketHandshakeStatus(t, address, "/ws/client", tc.token, "", ""); status != http.StatusForbidden {
+				t.Fatalf("status = %d, want %d", status, http.StatusForbidden)
+			}
+		})
+	}
+	if status := rawClientWebSocketHandshakeStatus(t, address, "/ws/client", "", clientToken.Secret, ""); status != http.StatusForbidden {
+		t.Fatalf("query token status = %d, want %d", status, http.StatusForbidden)
+	}
+	if status := rawClientWebSocketHandshakeStatus(t, address, "/ws/client", "", "", "token="+clientToken.Secret); status != http.StatusForbidden {
+		t.Fatalf("cookie token status = %d, want %d", status, http.StatusForbidden)
+	}
+	if status := rawClientWebSocketHandshakeStatus(t, address, "/ws/client/other", clientToken.Secret, "", ""); status != http.StatusNotFound {
+		t.Fatalf("non-exact route status = %d, want %d", status, http.StatusNotFound)
+	}
+	conn, status := rawClientWebSocketUpgrade(t, address, "/ws/client", clientToken.Secret, "", "")
+	_ = conn.Close()
+	if status != http.StatusSwitchingProtocols {
+		t.Fatalf("valid client status = %d, want %d", status, http.StatusSwitchingProtocols)
+	}
+}
+
+func TestClientWSAuthorizesEveryOpenAndKeepsConnectionUsableAfterDenial(t *testing.T) {
+	ctx := context.Background()
+	db, err := storage.OpenSQLite(ctx, "file:client-ws-open?mode=memory&cache=shared")
+	if err != nil {
+		t.Fatal(err)
+	}
+	defer db.Close()
+	authService := auth.NewAuthService(db)
+	owner, err := authService.CreateUser(ctx, "client-open-owner", "client-open-password", "user")
+	if err != nil {
+		t.Fatal(err)
+	}
+	if err := db.Agents().Create(ctx, storage.Agent{ID: "agent-open", Name: "agent-open", OwnerUserID: owner.ID, Enabled: true}); err != nil {
+		t.Fatal(err)
+	}
+	now := time.Now().UTC()
+	if err := db.Policies().Create(ctx, storage.AgentPolicy{ID: "client-open-policy", AgentID: "agent-open", TargetHost: "10.0.0.8", TargetPort: 22, Protocol: "tcp", CreatedAt: now, UpdatedAt: now}); err != nil {
+		t.Fatal(err)
+	}
+	credentials := auth.NewCredentialService(db)
+	created, err := credentials.Create(ctx, auth.CreateTokenInput{Type: storage.TokenTypeClient, OwnerUserID: owner.ID, Scope: auth.TokenScope{AgentIDs: []string{"agent-open"}, Protocols: []string{"tcp"}, TargetPorts: []int{22, 23}}})
+	if err != nil {
+		t.Fatal(err)
+	}
+	_ = credentials.Close()
+
+	runtime, address, stop := startClientWSTestRuntime(t, db)
+	defer stop()
+	opener := &recordingNodeTransport{opened: make(chan relay.StreamRequest, 2)}
+	runtime.ClientTransport = opener
+
+	config, err := websocket.NewConfig("ws://"+address+"/ws/client", "http://"+address)
+	if err != nil {
+		t.Fatal(err)
+	}
+	config.Header.Set("Authorization", "Bearer "+created.Secret)
+	conn, err := websocket.DialConfig(config)
+	if err != nil {
+		t.Fatal(err)
+	}
+	defer conn.Close()
+	allowedPayload, _ := protocol.EncodeStreamOpenPayload(protocol.StreamOpenPayload{AgentID: "agent-open", Protocol: "tcp", TargetHost: "10.0.0.8", TargetPort: 22, Metadata: []byte("allowed")})
+	deniedPayload, _ := protocol.EncodeStreamOpenPayload(protocol.StreamOpenPayload{AgentID: "agent-open", Protocol: "tcp", TargetHost: "10.0.0.8", TargetPort: 23, Metadata: []byte("denied")})
+	if err := websocket.Message.Send(conn, clientServerFrameBytes(t, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: 1, Payload: allowedPayload})); err != nil {
+		t.Fatal(err)
+	}
+	select {
+	case request := <-opener.opened:
+		if request.AgentID != "agent-open" || request.Protocol != "tcp" || request.TargetHost != "10.0.0.8" || request.TargetPort != 22 || string(request.Metadata) != "allowed" {
+			t.Fatalf("relay request = %+v", request)
+		}
+	case <-time.After(time.Second):
+		t.Fatal("allowed OPEN did not reach relay")
+	}
+	if err := websocket.Message.Send(conn, clientServerFrameBytes(t, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: 2, Payload: deniedPayload})); err != nil {
+		t.Fatal(err)
+	}
+	denied := receiveClientServerFrame(t, conn)
+	if denied.Type != protocol.FrameReset || denied.StreamID != 2 || len(denied.Payload) == 0 || len(denied.Payload) > 128 {
+		t.Fatalf("denied frame = %+v, payload bytes=%d", denied, len(denied.Payload))
+	}
+	select {
+	case request := <-opener.opened:
+		t.Fatalf("denied OPEN reached relay: %+v", request)
+	default:
+	}
+	if err := websocket.Message.Send(conn, clientServerFrameBytes(t, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FramePing, Payload: []byte("still-alive")})); err != nil {
+		t.Fatal(err)
+	}
+	pong := receiveClientServerFrame(t, conn)
+	if pong.Type != protocol.FramePong || string(pong.Payload) != "still-alive" {
+		t.Fatalf("pong = %+v", pong)
+	}
+}
+
+func TestServeClientSessionRejectsInvalidStreamTransitionsWithoutPanicking(t *testing.T) {
+	transport := newScriptedClientTransport(
+		protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: 99, Payload: []byte("unknown")},
+		protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: 7, Payload: []byte(`{"agent_id":"a","protocol":"tcp","target_host":"h","target_port":1}`)},
+		protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: 7, Payload: []byte(`{"agent_id":"a","protocol":"tcp","target_host":"h","target_port":1}`)},
+	)
+	opener := &recordingNodeTransport{opened: make(chan relay.StreamRequest, 1)}
+	authorizer := streamAuthorizerFunc(func(context.Context, ClientSessionPrincipal, protocol.StreamOpenPayload) error { return nil })
+	err := ServeClientSession(context.Background(), ClientSessionPrincipal{ConnectionID: "connection-transition", Identity: auth.TokenIdentity{TokenID: "token", Type: storage.TokenTypeClient}}, transport, authorizer, opener)
+	if err != nil {
+		t.Fatalf("ServeClientSession() error = %v", err)
+	}
+	if got := transport.resetCount(); got != 2 {
+		t.Fatalf("RESET count = %d, want 2", got)
+	}
+}
+
+func TestServeClientSessionRejectsZeroStreamIDAsConnectionProtocolError(t *testing.T) {
+	transport := newScriptedClientTransport(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: 0})
+	err := ServeClientSession(context.Background(), ClientSessionPrincipal{ConnectionID: "connection-zero", Identity: auth.TokenIdentity{TokenID: "token", Type: storage.TokenTypeClient}}, transport, streamAuthorizerFunc(func(context.Context, ClientSessionPrincipal, protocol.StreamOpenPayload) error { return nil }), &recordingNodeTransport{opened: make(chan relay.StreamRequest, 1)})
+	if !errors.Is(err, protocol.ErrInvalidFrame) {
+		t.Fatalf("ServeClientSession() error = %v, want ErrInvalidFrame", err)
+	}
+}
+
+func TestServeClientSessionResetsStreamAfterShortRelayWrite(t *testing.T) {
+	payload, err := protocol.EncodeStreamOpenPayload(protocol.StreamOpenPayload{AgentID: "a", Protocol: "tcp", TargetHost: "h", TargetPort: 1})
+	if err != nil {
+		t.Fatal(err)
+	}
+	transport := newScriptedClientTransport(
+		protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: 9, Payload: payload},
+		protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: 9, Payload: []byte("complete-frame")},
+	)
+	opener := &shortWriteNodeTransport{}
+	err = ServeClientSession(context.Background(), ClientSessionPrincipal{ConnectionID: "connection-short-write", Identity: auth.TokenIdentity{TokenID: "token", Type: storage.TokenTypeClient}}, transport, streamAuthorizerFunc(func(context.Context, ClientSessionPrincipal, protocol.StreamOpenPayload) error { return nil }), opener)
+	if err != nil {
+		t.Fatal(err)
+	}
+	if got := transport.resetCount(); got != 1 {
+		t.Fatalf("RESET count = %d, want 1 after partial relay write", got)
+	}
+}
+
+type streamAuthorizerFunc func(context.Context, ClientSessionPrincipal, protocol.StreamOpenPayload) error
+
+func (f streamAuthorizerFunc) Authorize(ctx context.Context, principal ClientSessionPrincipal, request protocol.StreamOpenPayload) error {
+	return f(ctx, principal, request)
+}
+
+type recordingNodeTransport struct {
+	opened chan relay.StreamRequest
+}
+
+func (t *recordingNodeTransport) OpenStream(_ context.Context, request relay.StreamRequest) (io.ReadWriteCloser, error) {
+	select {
+	case t.opened <- request:
+	default:
+	}
+	server, peer := net.Pipe()
+	go func() {
+		defer peer.Close()
+		_, _ = io.Copy(io.Discard, peer)
+	}()
+	return server, nil
+}
+
+func (t *recordingNodeTransport) Close() error { return nil }
+
+type shortWriteNodeTransport struct{}
+
+func (t *shortWriteNodeTransport) OpenStream(context.Context, relay.StreamRequest) (io.ReadWriteCloser, error) {
+	return &shortWriteConn{closed: make(chan struct{})}, nil
+}
+
+func (t *shortWriteNodeTransport) Close() error { return nil }
+
+type shortWriteConn struct {
+	closed chan struct{}
+	once   sync.Once
+}
+
+func (c *shortWriteConn) Read([]byte) (int, error) {
+	<-c.closed
+	return 0, io.EOF
+}
+
+func (c *shortWriteConn) Write(payload []byte) (int, error) { return len(payload) - 1, nil }
+func (c *shortWriteConn) Close() error {
+	c.once.Do(func() { close(c.closed) })
+	return nil
+}
+
+type scriptedClientTransport struct {
+	receive []protocol.Frame
+	sent    []protocol.Frame
+}
+
+func newScriptedClientTransport(frames ...protocol.Frame) *scriptedClientTransport {
+	return &scriptedClientTransport{receive: frames}
+}
+
+func (t *scriptedClientTransport) Receive() (protocol.Frame, error) {
+	if len(t.receive) == 0 {
+		return protocol.Frame{}, io.EOF
+	}
+	frame := t.receive[0]
+	t.receive = t.receive[1:]
+	return frame, nil
+}
+
+func (t *scriptedClientTransport) Send(frame protocol.Frame) error {
+	t.sent = append(t.sent, frame)
+	return nil
+}
+
+func (t *scriptedClientTransport) Close() error { return nil }
+
+func (t *scriptedClientTransport) resetCount() int {
+	count := 0
+	for _, frame := range t.sent {
+		if frame.Type == protocol.FrameReset {
+			count++
+		}
+	}
+	return count
+}
+
+func startClientWSTestRuntime(t *testing.T, db *storage.DB) (*ServerRuntime, string, func()) {
+	t.Helper()
+	runtime, err := NewServerRuntime(db, AgentSessionConfig{})
+	if err != nil {
+		t.Fatal(err)
+	}
+	listener, err := net.Listen("tcp", "127.0.0.1:0")
+	if err != nil {
+		t.Fatal(err)
+	}
+	ctx, cancel := context.WithCancel(context.Background())
+	done := make(chan error, 1)
+	go func() { done <- runtime.ServeListener(ctx, listener) }()
+	return runtime, listener.Addr().String(), func() {
+		cancel()
+		<-done
+		_ = runtime.Close()
+	}
+}
+
+func rawClientWebSocketHandshakeStatus(t *testing.T, address, path, token, queryToken, cookie string) int {
+	t.Helper()
+	conn, status := rawClientWebSocketUpgrade(t, address, path, token, queryToken, cookie)
+	_ = conn.Close()
+	return status
+}
+
+func rawClientWebSocketUpgrade(t *testing.T, address, path, token, queryToken, cookie string) (net.Conn, int) {
+	t.Helper()
+	conn, err := net.Dial("tcp", address)
+	if err != nil {
+		t.Fatal(err)
+	}
+	if queryToken != "" {
+		path += "?token=" + queryToken
+	}
+	request := "GET " + path + " HTTP/1.1\r\nHost: " + address + "\r\nOrigin: http://" + address + "\r\n"
+	if token != "" {
+		request += "Authorization: Bearer " + token + "\r\n"
+	}
+	if cookie != "" {
+		request += "Cookie: " + cookie + "\r\n"
+	}
+	request += "Upgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\n"
+	if _, err := conn.Write([]byte(request)); err != nil {
+		_ = conn.Close()
+		t.Fatal(err)
+	}
+	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
+	response, err := http.ReadResponse(bufioNewReader(conn), &http.Request{Method: http.MethodGet})
+	if err != nil {
+		_ = conn.Close()
+		t.Fatal(err)
+	}
+	_ = conn.SetReadDeadline(time.Time{})
+	return conn, response.StatusCode
+}
+
+func clientServerFrameBytes(t *testing.T, frame protocol.Frame) []byte {
+	t.Helper()
+	var payload bytes.Buffer
+	if err := protocol.NewEncoder(&payload).WriteFrame(frame); err != nil {
+		t.Fatal(err)
+	}
+	return payload.Bytes()
+}
+
+func receiveClientServerFrame(t *testing.T, conn *websocket.Conn) protocol.Frame {
+	t.Helper()
+	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
+	var raw []byte
+	if err := websocket.Message.Receive(conn, &raw); err != nil {
+		t.Fatal(err)
+	}
+	frame, err := protocol.NewDecoder(bytes.NewReader(raw)).ReadFrame()
+	if err != nil {
+		t.Fatal(err)
+	}
+	return frame
+}
+
+func bufioNewReader(reader io.Reader) *bufio.Reader { return bufio.NewReader(reader) }
```
