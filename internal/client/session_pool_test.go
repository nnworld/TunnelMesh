package client

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
)

func TestSessionPoolCreatesOneConnectionPerAgent(t *testing.T) {
	calls := make(chan *poolTestTransport, 4)
	manager := NewSessionPoolManager(SessionPoolManagerOptions{
		ServerURL: "ws://server.example/ws/client",
		Token:     "client-secret",
		Config:    SessionPoolConfig{Min: 1, Max: 1, EvaluationInterval: time.Millisecond},
		Runner:    newPoolTestRunner(calls),
	})
	agentA := manager.Opener("agent-a")
	agentB := manager.Opener("agent-b")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- manager.Run(ctx) }()

	transports := make([]*poolTestTransport, 2)
	for i := range transports {
		select {
		case transports[i] = <-calls:
		case <-time.After(time.Second):
			t.Fatal("session pool did not create two WebSocket sessions")
		}
	}
	if manager.OpenCount("agent-a") != 1 || manager.OpenCount("agent-b") != 1 {
		t.Fatalf("open counts = agent-a:%d agent-b:%d, want 1 and 1", manager.OpenCount("agent-a"), manager.OpenCount("agent-b"))
	}

	openedAgents := make(map[string]bool)
	for i, opener := range []StreamOpener{agentA, agentB} {
		agentID := []string{"agent-a", "agent-b"}[i]
		stream, err := opener.OpenStream(ctx, StreamRequest{AgentID: agentID, Protocol: "tcp", TargetHost: "127.0.0.1", TargetPort: 3000})
		if err != nil {
			t.Fatal(err)
		}
		var frame protocol.Frame
		select {
		case frame = <-transports[0].sent:
		case frame = <-transports[1].sent:
		case <-time.After(time.Second):
			t.Fatal("session pool did not send OPEN frame")
		}
		if frame.Type != protocol.FrameOpenStream {
			t.Fatalf("frame type = %d, want OPEN", frame.Type)
		}
		payload, err := protocol.DecodeStreamOpenPayload(frame.Payload)
		if err != nil {
			t.Fatal(err)
		}
		if payload.AgentID != agentID || payload.Protocol != "tcp" || payload.TargetHost != "127.0.0.1" || payload.TargetPort != 3000 {
			t.Fatalf("OPEN payload = %+v", payload)
		}
		openedAgents[payload.AgentID] = true
		_ = stream.Close()
		// The fair writer may emit OPEN and the stream HalfClose close frame in
		// either order; drain the half-close so it cannot be mistaken for the
		// next iteration's OPEN frame.
		select {
		case frame := <-transports[0].sent:
			if frame.Type != protocol.FrameHalfClose && frame.Type != protocol.FrameReset {
				t.Fatalf("unexpected frame type = %d", frame.Type)
			}
		case frame := <-transports[1].sent:
			if frame.Type != protocol.FrameHalfClose && frame.Type != protocol.FrameReset {
				t.Fatalf("unexpected frame type = %d", frame.Type)
			}
		case <-time.After(100 * time.Millisecond):
			t.Fatal("did not receive stream close frame")
		}
	}
	if !openedAgents["agent-a"] || !openedAgents["agent-b"] {
		t.Fatalf("opened agents = %v, want agent-a and agent-b", openedAgents)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run() did not stop after context cancellation")
	}
}

func TestSessionPoolSelectsLeastActiveSession(t *testing.T) {
	calls := make(chan *poolTestTransport, 4)
	var callCount int
	var mu sync.Mutex
	gates := make([]chan struct{}, 2)
	gates[0] = make(chan struct{})
	gates[1] = make(chan struct{})
	runner := func(ctx context.Context, _, _ string, onReady func(*Session) error) error {
		mu.Lock()
		index := callCount
		callCount++
		mu.Unlock()
		<-gates[index]
		return newPoolTestRunner(calls)(ctx, "", "", onReady)
	}
	manager := NewSessionPoolManager(SessionPoolManagerOptions{
		ServerURL: "ws://server.example/ws/client",
		Token:     "client-secret",
		Config:    SessionPoolConfig{Min: 2, Max: 2},
		Runner:    runner,
	})
	opener := manager.Opener("agent-a")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- manager.Run(ctx) }()

	gates[0] <- struct{}{}
	first := waitPoolTransport(t, calls)
	firstStream, err := opener.OpenStream(ctx, StreamRequest{AgentID: "agent-a", Protocol: "tcp", TargetHost: "127.0.0.1", TargetPort: 3000})
	if err != nil {
		t.Fatal(err)
	}
	<-first.sent

	gates[1] <- struct{}{}
	second := waitPoolTransport(t, calls)
	secondStream, err := opener.OpenStream(ctx, StreamRequest{AgentID: "agent-a", Protocol: "tcp", TargetHost: "127.0.0.1", TargetPort: 3001})
	if err != nil {
		t.Fatal(err)
	}
	frame := <-second.sent
	payload, err := protocol.DecodeStreamOpenPayload(frame.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if payload.TargetPort != 3001 {
		t.Fatalf("target port = %d, want second session to serve stream", payload.TargetPort)
	}

	_ = firstStream.Close()
	_ = secondStream.Close()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		active := manager.ActiveStreams("agent-a")
		if len(active) == 2 && active[0] == 0 && active[1] == 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if active := manager.ActiveStreams("agent-a"); len(active) != 2 || active[0] != 0 || active[1] != 0 {
		t.Fatalf("active streams = %v, want two zero values", active)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run() did not stop after context cancellation")
	}
}

func TestSessionPoolTieBreaksByLowestRTT(t *testing.T) {
	calls := make(chan *poolTestTransport, 4)
	manager := NewSessionPoolManager(SessionPoolManagerOptions{
		ServerURL: "ws://server.example/ws/client",
		Token:     "client-secret",
		Config:    SessionPoolConfig{Min: 2, Max: 2},
		Runner:    newPoolTestRunner(calls),
	})
	opener := manager.Opener("agent-a")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- manager.Run(ctx) }()

	first := waitPoolTransport(t, calls)
	second := waitPoolTransport(t, calls)
	pool := manager.pool("agent-a")
	pool.mu.Lock()
	firstEntry, secondEntry := pool.sessions[0], pool.sessions[1]
	pool.mu.Unlock()
	if firstEntry.session.transport != first {
		first, second = second, first
	}
	firstEntry.lastRTT.Store(int64(20 * time.Millisecond))
	secondEntry.lastRTT.Store(int64(time.Millisecond))

	stream, err := opener.OpenStream(ctx, StreamRequest{AgentID: "agent-a", Protocol: "tcp", TargetHost: "127.0.0.1", TargetPort: 3000})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()

	select {
	case frame := <-second.sent:
		if frame.Type != protocol.FrameOpenStream {
			t.Fatalf("frame type = %d, want OPEN", frame.Type)
		}
	case <-time.After(time.Second):
		t.Fatal("session pool did not select the lowest-RTT session")
	}
	select {
	case <-first.sent:
		t.Fatal("session pool selected the higher-RTT session")
	case <-time.After(20 * time.Millisecond):
	}

	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run() did not stop after context cancellation")
	}
}

func TestSessionPoolScalesUpAndDown(t *testing.T) {
	calls := make(chan *poolTestTransport, 4)
	manager := NewSessionPoolManager(SessionPoolManagerOptions{
		ServerURL: "ws://server.example/ws/client",
		Token:     "client-secret",
		Config: SessionPoolConfig{
			Min: 1, Max: 2, HighWatermark: 1, LowWatermark: 0,
			EvaluationInterval: 10 * time.Millisecond, Cooldown: 10 * time.Millisecond,
		},
		Runner: newPoolTestRunner(calls),
	})
	opener := manager.Opener("agent-a")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- manager.Run(ctx) }()

	first := waitPoolTransport(t, calls)
	stream, err := opener.OpenStream(ctx, StreamRequest{AgentID: "agent-a", Protocol: "tcp", TargetHost: "127.0.0.1", TargetPort: 3000})
	if err != nil {
		t.Fatal(err)
	}
	<-first.sent

	waitForCondition(t, time.Second, func() bool { return manager.OpenCount("agent-a") == 2 })
	second := waitPoolTransport(t, calls)
	if second == first {
		t.Fatal("scale-up reused the first transport")
	}
	waitForCondition(t, time.Second, func() bool { return len(manager.ActiveStreams("agent-a")) == 2 })

	_ = stream.Close()
	waitForCondition(t, 2*time.Second, func() bool {
		active := manager.ActiveStreams("agent-a")
		return len(active) == 2 && active[0] == 0 && active[1] == 0
	})
	// Closing the stream lets the next evaluator scale down. The newest slot
	// is canceled first, so the surviving session must be the first transport.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if manager.OpenCount("agent-a") == 1 && len(manager.ActiveStreams("agent-a")) == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run() did not stop after context cancellation")
	}
}

func TestSessionPoolReconnectsWithoutReplacingOpener(t *testing.T) {
	ready := make(chan *poolTestTransport, 2)
	runner := func(ctx context.Context, _, _ string, onReady func(*Session) error) error {
		first := newPoolTestTransport()
		firstSession := NewSession(first)
		firstSession.Start()
		if err := onReady(firstSession); err != nil {
			return err
		}
		ready <- first
		<-first.done

		second := newPoolTestTransport()
		secondSession := NewSession(second)
		secondSession.Start()
		if err := onReady(secondSession); err != nil {
			return err
		}
		ready <- second
		<-ctx.Done()
		_ = second.Close()
		return ctx.Err()
	}
	manager := NewSessionPoolManager(SessionPoolManagerOptions{
		ServerURL: "ws://server.example/ws/client",
		Token:     "client-secret",
		Config:    SessionPoolConfig{Min: 1, Max: 1},
		Runner:    runner,
	})
	opener := manager.Opener("agent-a")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- manager.Run(ctx) }()

	first := waitPoolTransport(t, ready)
	_ = first.Close()
	second := waitPoolTransport(t, ready)
	var stream io.ReadWriteCloser
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		opened, err := opener.OpenStream(ctx, StreamRequest{AgentID: "agent-a", Protocol: "tcp", TargetHost: "127.0.0.1", TargetPort: 3000})
		if err == nil {
			stream = opened
			break
		} else if !errors.Is(err, ErrAgentSessionUnavailable) {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if stream == nil {
		t.Fatal("session pool did not recover after reconnect")
	}
	frame := <-second.sent
	if frame.Type != protocol.FrameOpenStream {
		t.Fatalf("frame type = %d, want OPEN", frame.Type)
	}
	_ = stream.Close()

	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run() did not stop after context cancellation")
	}
}

func TestSessionPoolRunnerErrorFailsRun(t *testing.T) {
	manager := NewSessionPoolManager(SessionPoolManagerOptions{
		ServerURL: "ws://server.example/ws/client",
		Token:     "client-secret",
		Config:    SessionPoolConfig{Min: 1, Max: 1},
		Runner: func(context.Context, string, string, func(*Session) error) error {
			return errors.New("runner failed")
		},
	})
	_ = manager.Opener("agent-a")
	err := manager.Run(context.Background())
	if err == nil || err.Error() != "runner failed" {
		t.Fatalf("Run() error = %v, want runner failed", err)
	}
}

func TestSessionPoolRequiresAtLeastOneAgent(t *testing.T) {
	manager := NewSessionPoolManager(SessionPoolManagerOptions{
		ServerURL: "ws://server.example/ws/client",
		Token:     "client-secret",
		Config:    SessionPoolConfig{Min: 1, Max: 1},
	})
	if err := manager.Run(context.Background()); err == nil || err.Error() != "client: session pool has no agents" {
		t.Fatalf("Run() error = %v, want no agents", err)
	}
}

func newPoolTestRunner(calls chan *poolTestTransport) SessionPoolRunner {
	return func(ctx context.Context, _, _ string, onReady func(*Session) error) error {
		transport := newPoolTestTransport()
		session := NewSession(transport)
		session.Start()
		if err := onReady(session); err != nil {
			_ = transport.Close()
			return err
		}
		calls <- transport
		<-ctx.Done()
		_ = transport.Close()
		return ctx.Err()
	}
}

func waitPoolTransport(t *testing.T, calls chan *poolTestTransport) *poolTestTransport {
	t.Helper()
	select {
	case transport := <-calls:
		return transport
	case <-time.After(time.Second):
		t.Fatal("session pool runner was not ready")
		return nil
	}
}

func waitForCondition(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition was not met before timeout")
}

type poolTestTransport struct {
	sent      chan protocol.Frame
	recv      chan protocol.Frame
	done      chan struct{}
	closeOnce sync.Once
}

func newPoolTestTransport() *poolTestTransport {
	return &poolTestTransport{sent: make(chan protocol.Frame, 8), recv: make(chan protocol.Frame), done: make(chan struct{})}
}

func (t *poolTestTransport) Send(frame protocol.Frame) error {
	t.sent <- frame
	return nil
}

func (t *poolTestTransport) Receive() (protocol.Frame, error) {
	select {
	case frame := <-t.recv:
		return frame, nil
	case <-t.done:
		return protocol.Frame{}, io.EOF
	}
}

func (t *poolTestTransport) Close() error {
	t.closeOnce.Do(func() { close(t.done) })
	return nil
}
