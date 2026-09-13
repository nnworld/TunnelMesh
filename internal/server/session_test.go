package server

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

type blockingStaleRepo struct {
	mu      sync.Mutex
	value   storage.AgentRuntimeMetadata
	started chan struct{}
	release chan struct{}
}

func (r *blockingStaleRepo) Upsert(_ context.Context, v storage.AgentRuntimeMetadata) error {
	r.mu.Lock()
	r.value = v
	r.mu.Unlock()
	return nil
}

func (r *blockingStaleRepo) Get(context.Context, string) (storage.AgentRuntimeMetadata, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.value, nil
}

func (r *blockingStaleRepo) List(context.Context, string, int) (storage.Page[storage.AgentRuntimeMetadata], error) {
	v, _ := r.Get(context.Background(), "")
	return storage.Page[storage.AgentRuntimeMetadata]{Items: []storage.AgentRuntimeMetadata{v}}, nil
}

func (r *blockingStaleRepo) MarkStale(_ context.Context, _ string, _ int64) error {
	select {
	case <-r.started:
	default:
		close(r.started)
	}
	<-r.release
	r.mu.Lock()
	r.value.Stale = true
	r.mu.Unlock()
	return nil
}
func (r *blockingStaleRepo) Touch(_ context.Context, _ string, _ int64, lastSeenAt, expiresAt time.Time) error {
	r.mu.Lock()
	r.value.LastSeenAt = lastSeenAt
	r.value.ExpiresAt = &expiresAt
	r.value.Stale = false
	r.mu.Unlock()
	return nil
}

func (r *blockingStaleRepo) stale() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.value.Stale
}

type fakeTransport struct {
	sent   chan protocol.Frame
	closed chan struct{}
}

func newFakeTransport() *fakeTransport {
	return &fakeTransport{sent: make(chan protocol.Frame, 8), closed: make(chan struct{})}
}
func (f *fakeTransport) Send(fr protocol.Frame) error {
	select {
	case f.sent <- fr:
		return nil
	default:
		return ErrBackpressure
	}
}
func (f *fakeTransport) Close() error {
	select {
	case <-f.closed:
	default:
		close(f.closed)
	}
	return nil
}

// tryReceive drains one queued frame without blocking, for assertions that no
// RESET was emitted.
func (f *fakeTransport) tryReceive() (protocol.Frame, bool) {
	select {
	case frame := <-f.sent:
		return frame, true
	default:
		return protocol.Frame{}, false
	}
}

func TestAgentSessionRegistrationNegotiatesAndHeartbeats(t *testing.T) {
	m := NewAgentSessionManager(AgentSessionConfig{SupportedCapabilities: []string{"tcp", "udp"}})
	tr := newFakeTransport()
	s, err := m.Register(context.Background(), AgentRegistration{AgentID: "a1", NodeID: "n1", Epoch: 2, Capabilities: []string{"tcp", "udp"}}, tr)
	if err != nil {
		t.Fatal(err)
	}
	if !s.Supports("udp") {
		t.Fatal("udp not negotiated")
	}
	if err := m.Heartbeat("a1", 2); err != nil {
		t.Fatal(err)
	}
	if err := m.Heartbeat("a1", 1); !errors.Is(err, ErrEpoch) {
		t.Fatalf("expected epoch fencing, got %v", err)
	}
}

func TestClientSessionManagerTracksHeartbeatAndStreams(t *testing.T) {
	manager := NewClientSessionManager()
	transport := newFakeTransport()
	manager.Register(ClientSessionRecord{
		ConnectionID: "client_connection_1", TokenID: "token-1", OwnerUserID: "owner-1",
		ServerNodeID: "server-1", ConnectionEpoch: 1, StartedAt: time.Now().UTC(),
	}, transport)
	manager.ObserveHeartbeat("client_connection_1")
	manager.ObserveStreamOpened("client_connection_1")
	manager.ObserveStreamClosed("client_connection_1")
	record, ok := manager.Get("client_connection_1")
	if !ok {
		t.Fatal("connection not found")
	}
	if record.LastHeartbeatAt.IsZero() || record.ActiveStreams != 0 {
		t.Fatalf("record = %#v", record)
	}
	if err := manager.CloseConnection("client_connection_1", 2); !errors.Is(err, ErrEpoch) {
		t.Fatalf("stale close error=%v, want ErrEpoch", err)
	}
	if err := manager.CloseConnection("client_connection_1", 1); err != nil {
		t.Fatal(err)
	}
	select {
	case <-transport.closed:
	case <-time.After(time.Second):
		t.Fatal("current connection was not closed")
	}
}
func TestAgentSessionGoAwayAndBackpressure(t *testing.T) {
	m := NewAgentSessionManager(AgentSessionConfig{QueueSize: 1})
	tr := newGatedTransport()
	tr.gate = make(chan struct{})
	_, _ = m.Register(context.Background(), AgentRegistration{AgentID: "a", NodeID: "n", Epoch: 1}, tr)
	if err := m.Send("a", protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FramePing}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-tr.started:
	case <-time.After(time.Second):
		t.Fatal("writer did not start sending the first frame")
	}
	if err := m.Send("a", protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FramePing}); err != nil {
		t.Fatal(err)
	}
	if err := m.Send("a", protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FramePing}); !errors.Is(err, ErrBackpressure) {
		t.Fatalf("got %v", err)
	}
	close(tr.gate)
	if err := m.GoAway("a"); err != nil {
		t.Fatal(err)
	}
	if err := m.Send("a", protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FramePing}); !errors.Is(err, ErrSessionClosed) {
		t.Fatalf("got %v", err)
	}
}

func TestAgentSessionWriterPrioritizesControlAndFairStreams(t *testing.T) {
	tr := newGatedTransport()
	tr.gate = make(chan struct{})
	m := NewAgentSessionManager(AgentSessionConfig{QueueSize: 8})
	_, err := m.Register(context.Background(), AgentRegistration{AgentID: "fair", NodeID: "node", Epoch: 1}, tr)
	if err != nil {
		t.Fatal(err)
	}
	data := func(streamID uint32) protocol.Frame {
		return protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: streamID, Payload: []byte("data")}
	}
	if err := m.Send("fair", data(1)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-tr.started:
	case <-time.After(time.Second):
		t.Fatal("first stream frame did not reach the transport writer")
	}
	if err := m.Send("fair", data(2)); err != nil {
		t.Fatal(err)
	}
	if err := m.Send("fair", data(1)); err != nil {
		t.Fatal(err)
	}
	if err := m.Send("fair", protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FramePing}); err != nil {
		t.Fatal(err)
	}
	close(tr.gate)

	deadline := time.After(time.Second)
	want := []struct {
		streamID  uint32
		frameType protocol.FrameType
	}{
		{1, protocol.FrameData},
		{0, protocol.FramePing},
		{2, protocol.FrameData},
		{1, protocol.FrameData},
	}
	for _, expected := range want {
		select {
		case frame := <-tr.sent:
			if frame.StreamID != expected.streamID || frame.Type != expected.frameType {
				t.Fatalf("frame=%+v, want stream=%d type=%d", frame, expected.streamID, expected.frameType)
			}
		case <-deadline:
			t.Fatalf("timed out waiting for stream=%d type=%d", expected.streamID, expected.frameType)
		}
	}
	_ = m.GoAway("fair")
}

type gatedTransport struct {
	sent    chan protocol.Frame
	gate    chan struct{}
	closed  chan struct{}
	started chan struct{}
	once    sync.Once
}

func newGatedTransport() *gatedTransport {
	return &gatedTransport{sent: make(chan protocol.Frame, 16), started: make(chan struct{}), closed: make(chan struct{})}
}

func (t *gatedTransport) Send(frame protocol.Frame) error {
	t.once.Do(func() { close(t.started) })
	<-t.gate
	t.sent <- frame
	return nil
}

func (t *gatedTransport) Close() error {
	select {
	case <-t.closed:
	default:
		close(t.closed)
	}
	return nil
}
func TestClientSessionOpenLocalRouting(t *testing.T) {
	c := NewClientSessionManager()
	tr := newFakeTransport()
	c.Register(ClientSessionRecord{ConnectionID: "u", ConnectionEpoch: 1}, tr)
	if err := c.OpenStream(context.Background(), "u", StreamOpenRequest{StreamID: 7, Protocol: "tcp", TargetHost: "127.0.0.1", TargetPort: 80}); err != nil {
		t.Fatal(err)
	}
	select {
	case f := <-tr.sent:
		if f.Type != protocol.FrameOpenStream || f.StreamID != 7 {
			t.Fatalf("%+v", f)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}
}

func TestClientSessionOpenStreamEncodesRouteFields(t *testing.T) {
	c := NewClientSessionManager()
	tr := newFakeTransport()
	c.Register(ClientSessionRecord{ConnectionID: "u", ConnectionEpoch: 1}, tr)
	if err := c.OpenStream(context.Background(), "u", StreamOpenRequest{StreamID: 9, Protocol: "udp", TargetHost: "10.0.0.2", TargetPort: 5353, Metadata: []byte("m")}); err != nil {
		t.Fatal(err)
	}
	f := <-tr.sent
	var got map[string]any
	if err := json.Unmarshal(f.Payload, &got); err != nil {
		t.Fatalf("payload is not JSON: %v", err)
	}
	if got["protocol"] != "udp" || got["target_host"] != "10.0.0.2" || got["target_port"] != float64(5353) {
		t.Fatalf("route payload=%v", got)
	}
}

func TestGoAwayDrainsQueuedFramesBeforeTerminal(t *testing.T) {
	tr := newFakeTransport()
	m := NewAgentSessionManager(AgentSessionConfig{QueueSize: 8})
	_, _ = m.Register(context.Background(), AgentRegistration{AgentID: "a", NodeID: "n", Epoch: 1}, tr)
	for i := 0; i < 3; i++ {
		if err := m.Send("a", protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FramePing, StreamID: uint32(i + 1)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.GoAway("a"); err != nil {
		t.Fatal(err)
	}
	seen := make([]protocol.Frame, 0, 4)
	for len(seen) < 4 {
		select {
		case f := <-tr.sent:
			seen = append(seen, f)
		case <-time.After(time.Second):
			t.Fatalf("frames=%d", len(seen))
		}
	}
	for i, f := range seen {
		if i < 3 && f.Type == protocol.FrameGoAway {
			t.Fatalf("GOAWAY before queued frame at %d", i)
		}
	}
	if seen[3].Type != protocol.FrameGoAway {
		t.Fatalf("terminal=%+v", seen[3])
	}
}

type fakeWSConn struct {
	typ     int
	payload []byte
}

func (c *fakeWSConn) ReadMessage() (int, []byte, error) { return c.typ, c.payload, nil }
func (c *fakeWSConn) WriteMessage(int, []byte) error    { return nil }
func (c *fakeWSConn) Close() error                      { return nil }
func TestWSFrameTransportRejectsTextMessages(t *testing.T) {
	c := &fakeWSConn{typ: 1}
	tr := NewWSFrameTransport(c)
	if _, err := tr.Receive(); !errors.Is(err, ErrNonBinaryMessage) {
		t.Fatalf("err=%v", err)
	}
}

func TestRegisterRejectsStaleEpoch(t *testing.T) {
	m := NewAgentSessionManager(AgentSessionConfig{})
	_, _ = m.Register(context.Background(), AgentRegistration{AgentID: "a", NodeID: "n", Epoch: 4}, newFakeTransport())
	if _, err := m.Register(context.Background(), AgentRegistration{AgentID: "a", NodeID: "n2", Epoch: 3}, newFakeTransport()); !errors.Is(err, ErrEpoch) {
		t.Fatalf("err=%v", err)
	}
}

func TestAgentSessionManagerKeepsConnectionsForSameAgent(t *testing.T) {
	m := NewAgentSessionManager(AgentSessionConfig{})
	first, err := m.Register(context.Background(), AgentRegistration{
		AgentID: "pool-agent", NodeID: "node-a", InstanceID: "instance-a", ConnectionID: "conn-a", ConnectionEpoch: 1, Epoch: 1,
	}, newFakeTransport())
	if err != nil {
		t.Fatal(err)
	}
	second, err := m.Register(context.Background(), AgentRegistration{
		AgentID: "pool-agent", NodeID: "node-b", InstanceID: "instance-b", ConnectionID: "conn-b", ConnectionEpoch: 1, Epoch: 1,
	}, newFakeTransport())
	if err != nil {
		t.Fatal(err)
	}
	sessions := m.List("pool-agent")
	if len(sessions) != 2 {
		t.Fatalf("connections=%d, want 2", len(sessions))
	}
	if sessions[0] != first || sessions[1] != second {
		t.Fatalf("connections=%v, want registration order [first second]", sessions)
	}
	if got, ok := m.GetConnection("pool-agent", "conn-a"); !ok || got != first {
		t.Fatalf("GetConnection(conn-a) = (%v, %v), want first", got, ok)
	}
	if got, ok := m.GetConnection("pool-agent", "conn-b"); !ok || got != second {
		t.Fatalf("GetConnection(conn-b) = (%v, %v), want second", got, ok)
	}
}

func TestAgentSessionManagerReplacesOnlyMatchingConnection(t *testing.T) {
	m := NewAgentSessionManager(AgentSessionConfig{})
	first, err := m.Register(context.Background(), AgentRegistration{AgentID: "replace-agent", NodeID: "node-a", ConnectionID: "conn-a", ConnectionEpoch: 1, Epoch: 1}, newFakeTransport())
	if err != nil {
		t.Fatal(err)
	}
	second, err := m.Register(context.Background(), AgentRegistration{AgentID: "replace-agent", NodeID: "node-b", ConnectionID: "conn-b", ConnectionEpoch: 1, Epoch: 1}, newFakeTransport())
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := m.Register(context.Background(), AgentRegistration{AgentID: "replace-agent", NodeID: "node-c", ConnectionID: "conn-a", ConnectionEpoch: 2, Epoch: 2}, newFakeTransport())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Register(context.Background(), AgentRegistration{AgentID: "replace-agent", NodeID: "node-d", ConnectionID: "conn-a", ConnectionEpoch: 2, Epoch: 2}, newFakeTransport()); !errors.Is(err, ErrEpoch) {
		t.Fatalf("equal epoch err=%v, want ErrEpoch", err)
	}
	if got, ok := m.GetConnection("replace-agent", "conn-a"); !ok || got != replacement {
		t.Fatalf("replacement=%v,%v, want registered replacement", got, ok)
	}
	if got, ok := m.GetConnection("replace-agent", "conn-b"); !ok || got != second {
		t.Fatalf("other connection=%v,%v, want unchanged", got, ok)
	}
	m.RemoveSession("replace-agent", replacement)
	if _, ok := m.GetConnection("replace-agent", "conn-a"); ok {
		t.Fatal("replacement remained registered")
	}
	if _, ok := m.GetConnection("replace-agent", "conn-b"); !ok {
		t.Fatal("other connection was removed")
	}
	if first == replacement {
		t.Fatal("replacement unexpectedly reused session object")
	}
}

func TestAgentSessionManagerCloseConnectionSendsGoAway(t *testing.T) {
	manager := NewAgentSessionManager(AgentSessionConfig{})
	transport := newFakeTransport()
	session, err := manager.Register(context.Background(), AgentRegistration{
		AgentID: "close-agent", NodeID: "node-a", ConnectionID: "conn-a", ConnectionEpoch: 7, Epoch: 7,
	}, transport)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.CloseConnection(session.AgentID, session.ConnectionID, session.ConnectionEpoch); err != nil {
		t.Fatalf("CloseConnection() error = %v", err)
	}
	select {
	case frame := <-transport.sent:
		if frame.Type != protocol.FrameGoAway {
			t.Fatalf("frame type = %v, want GOAWAY", frame.Type)
		}
	default:
		t.Fatal("GOAWAY was not sent")
	}
	select {
	case <-transport.closed:
	default:
		t.Fatal("transport was not closed")
	}
}

func TestAgentSessionManagerCloseConnectionRejectsStaleEpoch(t *testing.T) {
	manager := NewAgentSessionManager(AgentSessionConfig{})
	transport := newFakeTransport()
	session, err := manager.Register(context.Background(), AgentRegistration{
		AgentID: "stale-close-agent", NodeID: "node-a", ConnectionID: "conn-a", ConnectionEpoch: 7, Epoch: 7,
	}, transport)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.CloseConnection(session.AgentID, session.ConnectionID, session.ConnectionEpoch-1); !errors.Is(err, ErrEpoch) {
		t.Fatalf("CloseConnection() error = %v, want %v", err, ErrEpoch)
	}
	select {
	case <-transport.closed:
		t.Fatal("stale close closed the live transport")
	default:
	}
}

func TestAgentSessionManagerCloseConnectionDoesNotAffectSiblingConnection(t *testing.T) {
	manager := NewAgentSessionManager(AgentSessionConfig{})
	firstTransport := newFakeTransport()
	secondTransport := newFakeTransport()
	first, err := manager.Register(context.Background(), AgentRegistration{
		AgentID: "sibling-agent", NodeID: "node-a", ConnectionID: "conn-a", ConnectionEpoch: 7, Epoch: 7,
	}, firstTransport)
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.Register(context.Background(), AgentRegistration{
		AgentID: "sibling-agent", NodeID: "node-b", ConnectionID: "conn-b", ConnectionEpoch: 8, Epoch: 8,
	}, secondTransport)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.CloseConnection(first.AgentID, first.ConnectionID, first.ConnectionEpoch); err != nil {
		t.Fatalf("CloseConnection() error = %v", err)
	}
	if _, ok := manager.GetConnection(second.AgentID, second.ConnectionID); !ok {
		t.Fatal("sibling connection was removed")
	}
	select {
	case <-secondTransport.closed:
		t.Fatal("sibling transport was closed")
	default:
	}
}

func TestAgentSessionManagerAllocatesMonotonicServerGenerationsAndFailsClosedAtOverflow(t *testing.T) {
	manager := NewAgentSessionManager(AgentSessionConfig{})
	first, err := manager.Register(context.Background(), AgentRegistration{AgentID: "generation-agent", NodeID: "node-a", Epoch: 1}, newFakeTransport())
	if err != nil {
		t.Fatal(err)
	}
	manager.RemoveSession("generation-agent", first)
	second, err := manager.Register(context.Background(), AgentRegistration{AgentID: "generation-agent", NodeID: "node-b", Epoch: 1}, newFakeTransport())
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.sendServerGeneration("generation-agent", first.serverGeneration, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FramePing}); !errors.Is(err, ErrServerGeneration) {
		t.Fatalf("SendGeneration(old same-epoch incarnation) error = %v, want ErrServerGeneration", err)
	}
	third, err := manager.Register(context.Background(), AgentRegistration{AgentID: "generation-agent", NodeID: "node-c", Epoch: 2}, newFakeTransport())
	if err != nil {
		t.Fatal(err)
	}
	if first.serverGeneration == 0 || second.serverGeneration <= first.serverGeneration || third.serverGeneration <= second.serverGeneration {
		t.Fatalf("server generations = %d, %d, %d; want non-zero monotonic incarnations", first.serverGeneration, second.serverGeneration, third.serverGeneration)
	}

	exhausted := NewAgentSessionManager(AgentSessionConfig{})
	exhausted.nextServerGeneration = math.MaxUint64
	if session, err := exhausted.Register(context.Background(), AgentRegistration{AgentID: "overflow-agent", NodeID: "node", Epoch: 1}, newFakeTransport()); session != nil || !errors.Is(err, ErrServerGenerationExhausted) {
		t.Fatalf("Register() = (%v, %v), want nil ErrServerGenerationExhausted", session, err)
	}
	if _, ok := exhausted.Get("overflow-agent"); ok {
		t.Fatal("overflowed server generation registered a live session")
	}
}

func TestRemoveSessionDoesNotStaleSameEpochReplacement(t *testing.T) {
	repo := &blockingStaleRepo{started: make(chan struct{}), release: make(chan struct{})}
	service := NewAgentMetadataService(repo)
	if _, err := service.Upsert(context.Background(), AgentMetadataInput{
		AgentID: "a", NodeID: "n", Epoch: 1, Revision: 1,
		Items: []MetadataItem{{Name: "region", Source: "env", Value: "east"}},
	}); err != nil {
		t.Fatal(err)
	}
	m := NewAgentSessionManager(AgentSessionConfig{MetadataService: service})
	old, err := m.Register(context.Background(), AgentRegistration{AgentID: "a", NodeID: "n", Epoch: 1}, newFakeTransport())
	if err != nil {
		t.Fatal(err)
	}
	removeDone := make(chan struct{})
	go func() {
		m.RemoveSession("a", old)
		close(removeDone)
	}()
	select {
	case <-repo.started:
	case <-time.After(time.Second):
		t.Fatal("stale marking did not start")
	}
	type registerResult struct {
		session *AgentSession
		err     error
	}
	replacementCh := make(chan registerResult, 1)
	go func() {
		s, err := m.Register(context.Background(), AgentRegistration{AgentID: "a", NodeID: "n", Epoch: 1}, newFakeTransport())
		replacementCh <- registerResult{session: s, err: err}
	}()
	select {
	case <-replacementCh:
		t.Fatal("replacement registered before stale fencing completed")
	case <-time.After(20 * time.Millisecond):
	}
	close(repo.release)
	select {
	case <-removeDone:
	case <-time.After(time.Second):
		t.Fatal("session removal did not finish")
	}
	result := <-replacementCh
	if result.err != nil {
		t.Fatalf("replacement registration failed: %v", result.err)
	}
	replacement := result.session
	ack, err := replacement.HandleMetadata(context.Background(), protocol.AgentMetadataPayload{
		AgentID: "a", NodeID: "n", Epoch: 1, Revision: 2,
		Items: []protocol.AgentMetadataItem{{Name: "region", Source: "env", Value: "west"}},
	})
	if err != nil || !ack.Accepted {
		t.Fatalf("replacement metadata ack=%+v err=%v", ack, err)
	}
	if repo.stale() {
		t.Fatal("old session cleanup marked same-epoch replacement stale")
	}
}

func TestNewAgentSessionManagerWithMetadataWiresPersistence(t *testing.T) {
	repo := &fakeMetadataRepo{}
	m := NewAgentSessionManagerWithMetadata(repo, AgentSessionConfig{})
	s, err := m.Register(context.Background(), AgentRegistration{AgentID: "a", NodeID: "n", Epoch: 1}, newFakeTransport())
	if err != nil {
		t.Fatal(err)
	}
	ack, err := s.HandleMetadata(context.Background(), protocol.AgentMetadataPayload{
		AgentID: "a", NodeID: "n", Epoch: 1, Revision: 1,
		Items: []protocol.AgentMetadataItem{{Name: "region", Source: "env", Value: "east"}},
	})
	if err != nil || !ack.Accepted {
		t.Fatalf("metadata ack=%+v err=%v", ack, err)
	}
	if repo.value.AgentID != "a" || repo.value.NodeID != "n" || repo.value.Epoch != 1 {
		t.Fatalf("metadata was not persisted: %+v", repo.value)
	}
}

type failingTransport struct{ fakeTransport }

func (f *failingTransport) Send(protocol.Frame) error { return errors.New("write failed") }
func TestWriterErrorClosesSession(t *testing.T) {
	m := NewAgentSessionManager(AgentSessionConfig{QueueSize: 1})
	_, _ = m.Register(context.Background(), AgentRegistration{AgentID: "a", NodeID: "n", Epoch: 1}, &failingTransport{fakeTransport: *newFakeTransport()})
	if err := m.Send("a", protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FramePing}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	if err := m.Send("a", protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FramePing}); !errors.Is(err, ErrSessionClosed) {
		t.Fatalf("err=%v", err)
	}
}
