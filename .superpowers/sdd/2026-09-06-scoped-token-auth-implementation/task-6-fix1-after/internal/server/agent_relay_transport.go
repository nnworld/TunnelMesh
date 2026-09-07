package server

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"

	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
	"github.com/tunnelmesh/tunnelmesh/internal/relay"
)

var errAgentRelayClosed = errors.New("server: local Agent relay closed")

// AgentRelayTransport multiplexes local relay streams over the currently
// registered Agent WebSocket sessions. Agent wire IDs are process-wide and
// independent from Client connection/stream IDs.
type AgentRelayTransport struct {
	manager *AgentSessionManager
	nextID  atomic.Uint32
	mu      sync.Mutex
	streams map[uint32]*agentRelayStream
	closed  bool
}

func NewAgentRelayTransport(manager *AgentSessionManager) *AgentRelayTransport {
	return &AgentRelayTransport{manager: manager, streams: make(map[uint32]*agentRelayStream)}
}

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
	stream := &agentRelayStream{transport: t, agentID: request.AgentID, epoch: session.Epoch, readCh: make(chan []byte, 16), done: make(chan struct{})}
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil, errAgentRelayClosed
	}
	for {
		stream.wireID = t.nextID.Add(1)
		if stream.wireID != 0 {
			if _, exists := t.streams[stream.wireID]; !exists {
				break
			}
		}
	}
	t.streams[stream.wireID] = stream
	t.mu.Unlock()
	if err := t.manager.SendGeneration(stream.agentID, stream.epoch, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: stream.wireID, Payload: payload}); err != nil {
		t.detach(stream)
		stream.fail(err)
		return nil, err
	}
	return stream, nil
}

// HandleAgentFrame is the single inbound dispatch point used by the Agent
// session callback. Epoch fencing prevents stale reconnect frames from being
// delivered to streams opened on the replacement generation.
func (t *AgentRelayTransport) HandleAgentFrame(agentID string, epoch int64, frame protocol.Frame) error {
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
	if session == nil || session.Epoch != epoch {
		t.manager.mu.RUnlock()
		return nil
	}
	session.mu.RLock()
	if session.closed || session.closing {
		session.mu.RUnlock()
		t.manager.mu.RUnlock()
		return nil
	}
	t.mu.Lock()
	stream := t.streams[frame.StreamID]
	if stream == nil || stream.agentID != agentID || stream.epoch != epoch {
		t.mu.Unlock()
		session.mu.RUnlock()
		t.manager.mu.RUnlock()
		return nil
	}
	var resetBackpressure bool
	switch frame.Type {
	case protocol.FrameData:
		if !stream.push(frame.Payload) {
			delete(t.streams, frame.StreamID)
			stream.fail(relay.ErrBackpressure)
			resetBackpressure = true
		}
	case protocol.FrameHalfClose:
		if stream.remoteHalfClose() {
			delete(t.streams, frame.StreamID)
		}
	case protocol.FrameReset:
		delete(t.streams, frame.StreamID)
		stream.fail(protocol.ErrStreamReset)
	}
	t.mu.Unlock()
	session.mu.RUnlock()
	t.manager.mu.RUnlock()
	if resetBackpressure {
		_ = t.manager.SendGeneration(agentID, epoch, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameReset, StreamID: frame.StreamID})
	}
	return nil
}

func (t *AgentRelayTransport) FailAgentGeneration(agentID string, epoch int64) {
	if t == nil {
		return
	}
	t.mu.Lock()
	var failed []*agentRelayStream
	for id, stream := range t.streams {
		if stream.agentID == agentID && stream.epoch == epoch {
			delete(t.streams, id)
			failed = append(failed, stream)
		}
	}
	t.mu.Unlock()
	for _, stream := range failed {
		stream.fail(relay.ErrNodeDisconnected)
	}
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
	for id, stream := range t.streams {
		delete(t.streams, id)
		streams = append(streams, stream)
	}
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
	if t.streams[stream.wireID] == stream {
		delete(t.streams, stream.wireID)
	}
	t.mu.Unlock()
}

type agentRelayStream struct {
	transport  *AgentRelayTransport
	agentID    string
	epoch      int64
	wireID     uint32
	readCh     chan []byte
	done       chan struct{}
	doneOnce   sync.Once
	closeOnce  sync.Once
	mu         sync.Mutex
	readBuf    []byte
	err        error
	localHalf  bool
	remoteHalf bool
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
	if err := s.transport.manager.SendGeneration(s.agentID, s.epoch, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: s.wireID, Payload: append([]byte(nil), payload...)}); err != nil {
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
	if err := s.transport.manager.SendGeneration(s.agentID, s.epoch, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameHalfClose, StreamID: s.wireID}); err != nil {
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
			_ = s.transport.manager.SendGeneration(s.agentID, s.epoch, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameReset, StreamID: s.wireID})
		}
		s.fail(io.ErrClosedPipe)
	})
	return nil
}

func (s *agentRelayStream) push(payload []byte) bool {
	select {
	case s.readCh <- append([]byte(nil), payload...):
		return true
	case <-s.done:
		return false
	default:
		return false
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
