package e2e

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/client"
	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
	"github.com/tunnelmesh/tunnelmesh/internal/relay"
	"github.com/tunnelmesh/tunnelmesh/internal/routing"
	"github.com/tunnelmesh/tunnelmesh/internal/server"
)

type sshDuplex struct {
	mu         sync.Mutex
	written    bytes.Buffer
	read       *bytes.Reader
	closed     bool
	closeWrite bool
}

func newSSHDuplex(response []byte) *sshDuplex {
	return &sshDuplex{read: bytes.NewReader(response)}
}

func (s *sshDuplex) Read(p []byte) (int, error) { return s.read.Read(p) }

func (s *sshDuplex) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0, io.ErrClosedPipe
	}
	return s.written.Write(p)
}

func (s *sshDuplex) CloseWrite() error {
	s.mu.Lock()
	s.closeWrite = true
	s.mu.Unlock()
	return nil
}

func (s *sshDuplex) Close() error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	return nil
}

func (s *sshDuplex) Written() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]byte(nil), s.written.Bytes()...)
}

func (s *sshDuplex) CloseWriteCalled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closeWrite
}

func TestAgentMetadataSSHByteIntegrityAndExitStatus(t *testing.T) {
	sshHandshake := []byte("SSH-2.0-TunnelMeshTest\r\n")
	remoteCommand := []byte{0, 0, 0, 20, 0, 0, 0, 4, 'e', 'x', 'e', 'c', 0, 0, 0, 0}
	exitStatus := []byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	stream := newSSHDuplex(append([]byte("SSH-2.0-target\r\n"), exitStatus...))
	var output bytes.Buffer
	input := bytes.NewReader(append(append([]byte{}, sshHandshake...), remoteCommand...))
	if err := client.ProxyStdio(context.Background(), input, &output, stream); err != nil {
		t.Fatal(err)
	}
	if got := stream.Written(); !bytes.Equal(got, append(sshHandshake, remoteCommand...)) {
		t.Fatalf("SSH bytes changed in proxy: got %x", got)
	}
	if !bytes.Equal(output.Bytes(), append([]byte("SSH-2.0-target\r\n"), exitStatus...)) {
		t.Fatalf("remote bytes changed in proxy: got %x", output.Bytes())
	}
	if !stream.CloseWriteCalled() {
		t.Fatal("proxy did not propagate stdin EOF as a half-close")
	}
}

func TestAgentMetadataSSHPolicyDenial(t *testing.T) {
	policy, err := routing.NewPolicy([]string{"10.0.0.0/8"}, []int{22})
	if err != nil {
		t.Fatal(err)
	}
	resolver := routing.NewRouteResolver([]routing.Route{{Domain: "ssh.example.com", AgentID: "agent-1", TargetHost: "10.0.0.8", TargetPort: 2200, AllowedCIDRs: []string{"10.0.0.0/8"}, AllowedPorts: []int{22}}}, routing.WithPolicy(policy))
	if _, err := resolver.ResolveHTTP("ssh.example.com", "/"); !errors.Is(err, routing.ErrPortNotAllowed) {
		t.Fatalf("expected policy denial, got %v", err)
	}
}

type e2eTransport struct {
	sent   chan protocol.Frame
	closed chan struct{}
}

func newE2ETransport() *e2eTransport {
	return &e2eTransport{sent: make(chan protocol.Frame, 8), closed: make(chan struct{})}
}

func (t *e2eTransport) Send(frame protocol.Frame) error {
	select {
	case <-t.closed:
		return io.ErrClosedPipe
	default:
	}
	t.sent <- frame
	return nil
}

func (t *e2eTransport) Close() error {
	select {
	case <-t.closed:
	default:
		close(t.closed)
	}
	return nil
}

func TestAgentMetadataSSHReconnectCleanup(t *testing.T) {
	manager := server.NewAgentSessionManager(server.AgentSessionConfig{})
	oldTransport := newE2ETransport()
	old, err := manager.Register(context.Background(), server.AgentRegistration{AgentID: "agent-1", NodeID: "node-a", Epoch: 1}, oldTransport)
	if err != nil {
		t.Fatal(err)
	}
	newTransport := newE2ETransport()
	current, err := manager.Register(context.Background(), server.AgentRegistration{AgentID: "agent-1", NodeID: "node-b", Epoch: 2}, newTransport)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := manager.Get("agent-1"); !ok || got != current {
		t.Fatal("reconnect did not replace current session")
	}
	select {
	case <-oldTransport.closed:
	case <-time.After(time.Second):
		t.Fatal("old session was not closed after reconnect")
	}
	manager.RemoveSession("agent-1", old)
	if got, ok := manager.Get("agent-1"); !ok || got != current {
		t.Fatal("stale cleanup removed the replacement session")
	}
	manager.RemoveSession("agent-1", current)
	if _, ok := manager.Get("agent-1"); ok {
		t.Fatal("current session remained after cleanup")
	}
}

type e2eBridgeStream struct {
	bytes.Buffer
	closed chan struct{}
}

func (s *e2eBridgeStream) Read([]byte) (int, error) { <-s.closed; return 0, io.EOF }
func (s *e2eBridgeStream) CloseWrite() error {
	select {
	case <-s.closed:
	default:
		close(s.closed)
	}
	return nil
}
func (s *e2eBridgeStream) Close() error {
	select {
	case <-s.closed:
	default:
		close(s.closed)
	}
	return nil
}

type e2eBridgeWS struct {
	msgs   [][]byte
	closed chan struct{}
}

func (w *e2eBridgeWS) ReadMessage() (int, []byte, error) {
	if len(w.msgs) == 0 {
		return 0, nil, io.EOF
	}
	msg := w.msgs[0]
	w.msgs = w.msgs[1:]
	return 2, msg, nil
}
func (w *e2eBridgeWS) WriteMessage(_ int, _ []byte) error { return nil }
func (w *e2eBridgeWS) Close() error {
	select {
	case <-w.closed:
	default:
		close(w.closed)
	}
	return nil
}

type e2eBridgeOpener struct{ stream io.ReadWriteCloser }

func (o e2eBridgeOpener) OpenStream(context.Context, relay.StreamRequest) (io.ReadWriteCloser, error) {
	return o.stream, nil
}
func (o e2eBridgeOpener) Close() error { return nil }

func TestAgentMetadataSSHBridgeDisconnectCleanup(t *testing.T) {
	stream := &e2eBridgeStream{closed: make(chan struct{})}
	ws := &e2eBridgeWS{msgs: [][]byte{[]byte("ssh-handshake")}, closed: make(chan struct{})}
	handler := &server.TCPBridgeHandler{
		Resolver: routing.NewRouteResolver([]routing.Route{{Domain: "ssh.example.com", AgentID: "agent-1", TargetHost: "10.0.0.8", TargetPort: 22}}),
		Opener:   e2eBridgeOpener{stream: stream},
	}
	if err := handler.Handle(context.Background(), ws, "ssh.example.com"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-stream.closed:
	case <-time.After(time.Second):
		t.Fatal("target stream was not closed after websocket disconnect")
	}
}

func TestAgentMetadataSSHPolicyRejectsLoopbackTarget(t *testing.T) {
	if err := (&routing.Policy{}).Validate(net.ParseIP("127.0.0.1"), 22); !errors.Is(err, routing.ErrDangerousAddress) {
		t.Fatalf("expected loopback policy denial, got %v", err)
	}
}
