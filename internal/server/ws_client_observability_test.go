package server

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/auth"
	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
	"github.com/tunnelmesh/tunnelmesh/internal/relay"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

// clientGaugeFixture is a Client session wired to the real observability stack,
// so assertions run against the same in-memory counter the heartbeat persists
// into client_connection_leases.active_streams.
type clientGaugeFixture struct {
	manager     *ClientSessionManager
	connections *fakeClientConnectionRepo
	transport   *channelClientTransport
	principal   ClientSessionPrincipal
	done        chan error
}

func newClientGaugeSession(t *testing.T, connectionID string, opener relay.NodeTransport) *clientGaugeFixture {
	t.Helper()
	instances := &fakeClientInstanceRepo{nextID: "client-instance-1"}
	connections := &fakeClientConnectionRepo{}
	manager := NewClientSessionManager()
	leases := NewClientConnectionLeaseController(connections, manager, "server-1", 90*time.Second)
	observabilityService := NewClientObservabilityService(instances, leases, manager, "server-1", 5*time.Minute)
	transport := newChannelClientTransport()
	authorizer := streamAuthorizerFunc(func(context.Context, ClientSessionPrincipal, protocol.StreamOpenPayload) error { return nil })
	service := NewClientStreamService(authorizer, opener, ClientStreamServiceConfig{
		MaxConcurrentOpens: 2, MaxPendingOpens: 2, OpenTimeout: time.Second,
		Observability: observabilityService,
	}, nil)
	t.Cleanup(func() { _ = service.Close() })
	principal := ClientSessionPrincipal{
		ConnectionID: connectionID,
		Identity:     auth.TokenIdentity{TokenID: "token-1", OwnerUserID: "owner-1", Type: storage.TokenTypeClient},
	}
	done := make(chan error, 1)
	go func() {
		done <- serveClientSessionWithService(context.Background(), principal, transport, authorizer, opener, maxClientOpenAttempts, nil, service)
	}()
	return &clientGaugeFixture{manager: manager, connections: connections, transport: transport, principal: principal, done: done}
}

func (f *clientGaugeFixture) openStream(t *testing.T, id uint32, proto string) {
	t.Helper()
	payload, err := protocol.EncodeStreamOpenPayload(protocol.StreamOpenPayload{
		AgentID: "agent-1", Protocol: proto, TargetHost: "db.internal", TargetPort: 5432,
	})
	if err != nil {
		t.Fatal(err)
	}
	f.transport.receive <- protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: id, Payload: payload}
}

func (f *clientGaugeFixture) awaitCount(t *testing.T, want int) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		if got := f.manager.ActiveStreams(f.principal.ConnectionID); got == want {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("active stream count = %d, want %d", f.manager.ActiveStreams(f.principal.ConnectionID), want)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func (f *clientGaugeFixture) close(t *testing.T) {
	t.Helper()
	close(f.transport.receive)
	select {
	case err := <-f.done:
		if err != nil {
			t.Fatalf("session ended with %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("session did not stop")
	}
}

// abortedAgentConn fails the Agent-side read with a transport error, which is
// how a dead upstream reaches a live Client stream: the stream is retired by
// relayToClient instead of the orderly half-close path.
type abortedAgentConn struct {
	aborted   chan struct{}
	abortOnce sync.Once
	closed    chan struct{}
	closeOnce sync.Once
}

func newAbortedAgentConn() *abortedAgentConn {
	return &abortedAgentConn{aborted: make(chan struct{}), closed: make(chan struct{})}
}

func (c *abortedAgentConn) Read([]byte) (int, error) {
	select {
	case <-c.aborted:
		return 0, io.ErrUnexpectedEOF
	case <-c.closed:
		return 0, io.ErrClosedPipe
	}
}

func (c *abortedAgentConn) Write(payload []byte) (int, error) { return len(payload), nil }

func (c *abortedAgentConn) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return nil
}

func (c *abortedAgentConn) abort() { c.abortOnce.Do(func() { close(c.aborted) }) }

func clientSessionResetFrame(id uint32) protocol.Frame {
	return protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameReset, StreamID: id}
}

func TestServeClientSessionReleasesStreamCountWhenAgentReadFails(t *testing.T) {
	conn := newAbortedAgentConn()
	opener := &queuedNodeTransport{connections: []io.ReadWriteCloser{conn}, opened: make(chan relay.StreamRequest, 1)}
	fixture := newClientGaugeSession(t, "connection-agent-abort", opener)
	fixture.openStream(t, 21, "tcp")
	select {
	case <-opener.opened:
	case <-time.After(3 * time.Second):
		t.Fatal("Agent stream never opened")
	}
	fixture.awaitCount(t, 1)

	conn.abort()
	if frame := receiveChannelClientFrame(t, fixture.transport); frame.Type != protocol.FrameReset || frame.StreamID != 21 {
		t.Fatalf("abort response=%+v, want RESET for stream 21", frame)
	}
	// A retired stream must give its slot back: the gauge is what the heartbeat
	// writes to active_streams and the Client observability page renders.
	fixture.awaitCount(t, 0)
	fixture.close(t)
}

func TestServeClientSessionStreamCountIsExactForMixedClosePaths(t *testing.T) {
	aborted := newAbortedAgentConn()
	resetConn := newAbortedAgentConn()
	opener := &queuedNodeTransport{connections: []io.ReadWriteCloser{aborted, resetConn}, opened: make(chan relay.StreamRequest, 2)}
	fixture := newClientGaugeSession(t, "connection-mixed-close", opener)
	fixture.openStream(t, 31, "tcp")
	expectOpened(t, opener)
	fixture.openStream(t, 32, "tcp")
	expectOpened(t, opener)
	fixture.awaitCount(t, 2)

	aborted.abort()
	expectResetFor(t, fixture, 31)
	// The other stream is still open, so its slot must survive: an imprecise
	// release would report one live stream as zero (or fewer than live).
	fixture.awaitCount(t, 1)
	settle(t, fixture)
	if got := fixture.manager.ActiveStreams(fixture.principal.ConnectionID); got != 1 {
		t.Fatalf("active stream count settled at %d, want 1", got)
	}

	// A RESET for a live stream is honoured silently: the stream is retired, and
	// the peer that asked for it needs no echo.
	fixture.transport.receive <- clientSessionResetFrame(32)
	fixture.awaitCount(t, 0)
	settle(t, fixture)
	if got := fixture.manager.ActiveStreams(fixture.principal.ConnectionID); got != 0 {
		t.Fatalf("active stream count settled at %d, want 0", got)
	}
	fixture.close(t)
}

func TestServeClientSessionStreamCountSurvivesRepeatedReleases(t *testing.T) {
	conn := newAbortedAgentConn()
	opener := &queuedNodeTransport{connections: []io.ReadWriteCloser{conn}, opened: make(chan relay.StreamRequest, 1)}
	fixture := newClientGaugeSession(t, "connection-double-release", opener)
	fixture.openStream(t, 41, "tcp")
	expectOpened(t, opener)
	fixture.awaitCount(t, 1)

	conn.abort()
	expectResetFor(t, fixture, 41)
	// Every later release attempt for the same stream id must be a no-op; the
	// queue-full and pump-error paths can both race to close one stream.
	for i := 0; i < 3; i++ {
		fixture.transport.receive <- clientSessionResetFrame(41)
		expectResetFor(t, fixture, 41)
	}
	settle(t, fixture)
	if got := fixture.manager.ActiveStreams(fixture.principal.ConnectionID); got != 0 {
		t.Fatalf("active stream count = %d, want 0", got)
	}
	fixture.close(t)
}

func expectOpened(t *testing.T, opener *queuedNodeTransport) {
	t.Helper()
	select {
	case <-opener.opened:
	case <-time.After(3 * time.Second):
		t.Fatal("Agent stream never opened")
	}
}

func expectResetFor(t *testing.T, fixture *clientGaugeFixture, id uint32) {
	t.Helper()
	if frame := receiveChannelClientFrame(t, fixture.transport); frame.Type != protocol.FrameReset || frame.StreamID != id {
		t.Fatalf("response=%+v, want RESET for stream %d", frame, id)
	}
}

func settle(t *testing.T, fixture *clientGaugeFixture) {
	t.Helper()
	deadline := time.After(300 * time.Millisecond)
	for {
		select {
		case <-deadline:
			return
		case <-time.After(20 * time.Millisecond):
		}
	}
}
