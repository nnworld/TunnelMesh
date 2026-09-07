package server

import (
	"context"
	"errors"
	"io"
	"math"
	"sync"

	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
	"github.com/tunnelmesh/tunnelmesh/internal/relay"
)

var (
	errAgentRelayClosed           = errors.New("server: local Agent relay closed")
	errAgentRelayWireIDsExhausted = errors.New("server: local Agent relay wire IDs exhausted")
)

// AgentRelayTransport multiplexes local relay streams over the currently
// registered Agent WebSocket sessions. Agent wire IDs are monotonic and never
// reused within one process-local authenticated Agent connection incarnation.
type AgentRelayTransport struct {
	manager    *AgentSessionManager
	mu         sync.Mutex
	streams    map[agentRelayStreamKey]*agentRelayStream
	allocators map[agentRelayGeneration]*agentRelayIDAllocator
	closed     bool
}

func NewAgentRelayTransport(manager *AgentSessionManager) *AgentRelayTransport {
	return &AgentRelayTransport{manager: manager, streams: make(map[agentRelayStreamKey]*agentRelayStream), allocators: make(map[agentRelayGeneration]*agentRelayIDAllocator)}
}

type agentRelayGeneration struct {
	agentID          string
	serverGeneration uint64
}

type agentRelayStreamKey struct {
	agentRelayGeneration
	wireID uint32
}

type agentRelayIDAllocator struct{ next uint64 }

func (t *AgentRelayTransport) OpenStream(ctx context.Context, request relay.StreamRequest) (io.ReadWriteCloser, error) {
	if t == nil || t.manager == nil || request.AgentID == "" || request.TargetHost == "" || request.TargetPort < 1 || request.TargetPort > 65535 {
		return nil, relay.ErrNodeDisconnected
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	session, ok := t.manager.Get(request.AgentID)
	if !ok {
		return nil, relay.ErrNodeDisconnected
	}
	payload, err := protocol.EncodeStreamOpenPayload(protocol.StreamOpenPayload{AgentID: request.AgentID, Protocol: request.Protocol, TargetHost: request.TargetHost, TargetPort: request.TargetPort, Metadata: append([]byte(nil), request.Metadata...)})
	if err != nil {
		return nil, err
	}
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil, errAgentRelayClosed
	}
	generation := agentRelayGeneration{agentID: request.AgentID, serverGeneration: session.serverGeneration}
	allocator := t.allocators[generation]
	if allocator == nil {
		allocator = &agentRelayIDAllocator{}
		t.allocators[generation] = allocator
	}
	if allocator.next >= math.MaxUint32 {
		t.mu.Unlock()
		return nil, errAgentRelayWireIDsExhausted
	}
	allocator.next++
	stream := &agentRelayStream{transport: t, agentID: request.AgentID, serverGeneration: session.serverGeneration, wireID: uint32(allocator.next), readCh: make(chan []byte, 16), done: make(chan struct{})}
	t.streams[stream.key()] = stream
	t.mu.Unlock()
	if err := t.manager.sendServerGeneration(stream.agentID, stream.serverGeneration, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: stream.wireID, Payload: payload}); err != nil {
		t.detach(stream)
		stream.fail(err)
		return nil, err
	}
	return stream, nil
}

// HandleAgentFrame is the single inbound dispatch point used by the Agent
// session callback. Exact process-local connection fencing prevents stale
// reconnect frames from being delivered to a replacement with the same Epoch.
func (t *AgentRelayTransport) handleAgentFrameGeneration(agentID string, serverGeneration uint64, frame protocol.Frame) error {
	if t == nil || t.manager == nil {
		return errAgentRelayClosed
	}
	if err := frame.Validate(); err != nil {
		return err
	}
	// Hold the current Agent generation stable through frame admission. Without
	// this fence, an old WebSocket callback could deliver a frame after its
	// replacement was registered but before the old session cleanup ran.
	t.manager.mu.RLock()
	session := t.manager.sessions[agentID]
	if session == nil || session.serverGeneration != serverGeneration {
		t.manager.mu.RUnlock()
		return nil
	}
	session.mu.RLock()
	if session.closed || session.closing {
		session.mu.RUnlock()
		t.manager.mu.RUnlock()
		return nil
	}
	key := agentRelayStreamKey{agentRelayGeneration: agentRelayGeneration{agentID: agentID, serverGeneration: serverGeneration}, wireID: frame.StreamID}
	t.mu.Lock()
	stream := t.streams[key]
	if stream == nil || stream.agentID != agentID || stream.serverGeneration != serverGeneration {
		t.mu.Unlock()
		session.mu.RUnlock()
		t.manager.mu.RUnlock()
		return nil
	}
	var resetStream bool
	switch frame.Type {
	case protocol.FrameData:
		if enqueueErr := stream.enqueue(frame.Payload); enqueueErr != nil {
			delete(t.streams, key)
			stream.fail(enqueueErr)
			resetStream = true
		}
	case protocol.FrameHalfClose:
		if stream.remoteHalfClose() {
			delete(t.streams, key)
		}
	case protocol.FrameReset:
		delete(t.streams, key)
		stream.fail(protocol.ErrStreamReset)
	}
	t.mu.Unlock()
	session.mu.RUnlock()
	t.manager.mu.RUnlock()
	if resetStream {
		_ = t.manager.sendServerGeneration(agentID, serverGeneration, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameReset, StreamID: frame.StreamID})
	}
	return nil
}

// HandleAgentFrame preserves the legacy Epoch-based callback API. Ambiguous
// same-epoch reconnects fail closed rather than guessing a connection.
func (t *AgentRelayTransport) HandleAgentFrame(agentID string, epoch int64, frame protocol.Frame) error {
	serverGeneration, err := t.manager.resolveServerGeneration(agentID, epoch)
	if err != nil {
		return err
	}
	return t.handleAgentFrameGeneration(agentID, serverGeneration, frame)
}

func (t *AgentRelayTransport) failAgentGeneration(agentID string, serverGeneration uint64) {
	if t == nil {
		return
	}
	t.mu.Lock()
	var failed []*agentRelayStream
	generation := agentRelayGeneration{agentID: agentID, serverGeneration: serverGeneration}
	for key, stream := range t.streams {
		if stream.agentID == agentID && stream.serverGeneration == serverGeneration {
			delete(t.streams, key)
			failed = append(failed, stream)
		}
	}
	delete(t.allocators, generation)
	t.mu.Unlock()
	for _, stream := range failed {
		stream.fail(relay.ErrNodeDisconnected)
	}
}

// FailAgentGeneration preserves the legacy Epoch-based teardown API. It fails
// closed for an ambiguous same-epoch reconnect instead of touching the live
// replacement generation.
func (t *AgentRelayTransport) FailAgentGeneration(agentID string, epoch int64) {
	serverGeneration, err := t.manager.resolveServerGeneration(agentID, epoch)
	if err != nil {
		return
	}
	t.failAgentGeneration(agentID, serverGeneration)
}

func (t *AgentRelayTransport) Close() error {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	streams := make([]*agentRelayStream, 0, len(t.streams))
	for key, stream := range t.streams {
		delete(t.streams, key)
		streams = append(streams, stream)
	}
	clear(t.allocators)
	t.mu.Unlock()
	for _, stream := range streams {
		stream.fail(errAgentRelayClosed)
	}
	return nil
}

func (t *AgentRelayTransport) detach(stream *agentRelayStream) {
	if t == nil || stream == nil {
		return
	}
	t.mu.Lock()
	if t.streams[stream.key()] == stream {
		delete(t.streams, stream.key())
	}
	t.mu.Unlock()
}

type agentRelayStream struct {
	transport        *AgentRelayTransport
	agentID          string
	serverGeneration uint64
	wireID           uint32
	readCh           chan []byte
	done             chan struct{}
	doneOnce         sync.Once
	closeOnce        sync.Once
	mu               sync.Mutex
	readBuf          []byte
	err              error
	localHalf        bool
	remoteHalf       bool
}

func (s *agentRelayStream) key() agentRelayStreamKey {
	return agentRelayStreamKey{agentRelayGeneration: agentRelayGeneration{agentID: s.agentID, serverGeneration: s.serverGeneration}, wireID: s.wireID}
}

func (s *agentRelayStream) Read(buffer []byte) (int, error) {
	if len(buffer) == 0 {
		return 0, nil
	}
	for {
		s.mu.Lock()
		if len(s.readBuf) > 0 {
			n := copy(buffer, s.readBuf)
			s.readBuf = s.readBuf[n:]
			s.mu.Unlock()
			return n, nil
		}
		s.mu.Unlock()
		select {
		case payload := <-s.readCh:
			s.mu.Lock()
			s.readBuf = append(s.readBuf, payload...)
			s.mu.Unlock()
			continue
		default:
		}
		s.mu.Lock()
		err := s.err
		s.mu.Unlock()
		if err != nil {
			return 0, err
		}
		select {
		case payload := <-s.readCh:
			s.mu.Lock()
			s.readBuf = append(s.readBuf, payload...)
			s.mu.Unlock()
		case <-s.done:
		}
	}
}

func (s *agentRelayStream) Write(payload []byte) (int, error) {
	if len(payload) == 0 {
		return 0, nil
	}
	if len(payload) > protocol.MaxPayload {
		return 0, protocol.ErrPayloadTooLarge
	}
	s.mu.Lock()
	if (s.err != nil && !errors.Is(s.err, io.EOF)) || s.localHalf {
		err := s.err
		if err == nil {
			err = io.ErrClosedPipe
		}
		s.mu.Unlock()
		return 0, err
	}
	s.mu.Unlock()
	if err := s.transport.manager.sendServerGeneration(s.agentID, s.serverGeneration, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: s.wireID, Payload: append([]byte(nil), payload...)}); err != nil {
		s.transport.detach(s)
		s.fail(err)
		return 0, err
	}
	return len(payload), nil
}

func (s *agentRelayStream) CloseWrite() error {
	s.mu.Lock()
	if s.err != nil && !errors.Is(s.err, io.EOF) {
		err := s.err
		s.mu.Unlock()
		return err
	}
	if s.localHalf {
		s.mu.Unlock()
		return nil
	}
	s.localHalf = true
	complete := s.remoteHalf
	s.mu.Unlock()
	if err := s.transport.manager.sendServerGeneration(s.agentID, s.serverGeneration, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameHalfClose, StreamID: s.wireID}); err != nil {
		s.transport.detach(s)
		s.fail(err)
		return err
	}
	if complete {
		s.transport.detach(s)
	}
	return nil
}

func (s *agentRelayStream) Close() error {
	s.closeOnce.Do(func() {
		s.transport.detach(s)
		s.mu.Lock()
		complete := s.localHalf && s.remoteHalf
		terminal := s.err != nil && !errors.Is(s.err, io.EOF)
		s.mu.Unlock()
		if !complete && !terminal {
			_ = s.transport.manager.sendServerGeneration(s.agentID, s.serverGeneration, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameReset, StreamID: s.wireID})
		}
		s.fail(io.ErrClosedPipe)
	})
	return nil
}

func (s *agentRelayStream) enqueue(payload []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.remoteHalf {
		return protocol.ErrInvalidFrame
	}
	if s.err != nil {
		return s.err
	}
	select {
	case s.readCh <- append([]byte(nil), payload...):
		return nil
	default:
		return relay.ErrBackpressure
	}
}

func (s *agentRelayStream) remoteHalfClose() bool {
	s.mu.Lock()
	if s.remoteHalf {
		s.mu.Unlock()
		return false
	}
	s.remoteHalf = true
	complete := s.localHalf
	s.mu.Unlock()
	s.finish(io.EOF)
	return complete
}

func (s *agentRelayStream) fail(err error) { s.finish(err) }

func (s *agentRelayStream) finish(err error) {
	s.mu.Lock()
	// A remote HALF_CLOSE publishes EOF for the read direction, but a later
	// RESET or generation failure must still terminate the write direction.
	if s.err == nil || (errors.Is(s.err, io.EOF) && !errors.Is(err, io.EOF)) {
		s.err = err
	}
	s.mu.Unlock()
	s.doneOnce.Do(func() { close(s.done) })
}
