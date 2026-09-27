package server

import (
	"context"
	"errors"
	"io"
	"math"
	"sync"
	"time"

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
// AgentRelayWindowConfig carries the data-plane credit policy the Server applies
// to Agent streams. It exists so `server.stream.*` is honoured instead of being
// documentation only; the zero value reproduces the compiled-in defaults.
type AgentRelayWindowConfig struct {
	// AdvertisedWindow is the credit the Server grants an Agent for one stream.
	// It is also the size of the buffer that enforces it.
	AdvertisedWindow uint32
	// UpdateThreshold is how many consumed bytes accumulate before credit is
	// returned. The invariant `window - threshold >= one frame` is enforced
	// rather than trusted, because a pump that waits for credit never times out.
	UpdateThreshold uint32
	// MaxActiveStreamsPerAgent is the node-local ceiling on simultaneously open
	// streams toward one Agent, across all of its connections.
	// server.stream.max_concurrent_opens only bounds how many opens are being
	// processed at once, so without a level cap one Client can pin an unbounded
	// number of live streams - and the Agent-side target connections behind them
	// - onto one Agent. Zero means unlimited.
	MaxActiveStreamsPerAgent int
}

func (c AgentRelayWindowConfig) advertisedWindow() uint32 {
	if c.AdvertisedWindow < protocol.MinRefillableWindow || c.AdvertisedWindow > protocol.DefaultServerReceiveWindow {
		return protocol.DefaultServerReceiveWindow
	}
	return c.AdvertisedWindow
}

// windowFor is the credit granted on one stream: never more than this Server is
// willing to buffer, and never more than what the requesting side asked for.
func (c AgentRelayWindowConfig) windowFor(requested uint32) uint32 {
	window := c.advertisedWindow()
	if peer := protocol.NegotiateReceiveWindow(requested); peer < window {
		window = peer
	}
	return window
}

func (c AgentRelayWindowConfig) updateThreshold(window uint32) uint32 {
	threshold := c.UpdateThreshold
	if threshold == 0 || threshold > window || window-threshold < protocol.MaxStreamFrame {
		return protocol.DefaultWindowUpdateThreshold
	}
	return threshold
}

type AgentRelayTransport struct {
	manager    *AgentSessionManager
	windows    AgentRelayWindowConfig
	mu         sync.Mutex
	streams    map[agentRelayStreamKey]*agentRelayStream
	allocators map[agentRelayGeneration]*agentRelayIDAllocator
	closed     bool
}

func NewAgentRelayTransport(manager *AgentSessionManager, windows AgentRelayWindowConfig) *AgentRelayTransport {
	return &AgentRelayTransport{manager: manager, windows: windows, streams: make(map[agentRelayStreamKey]*agentRelayStream), allocators: make(map[agentRelayGeneration]*agentRelayIDAllocator)}
}

type agentRelayGeneration struct {
	agentID          string
	connectionID     string
	connectionEpoch  int64
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
	agentID := request.AgentID
	var session *AgentSession
	var err error
	if request.CaseInsensitiveAgentID {
		session, err = t.manager.GetCaseInsensitive(agentID)
		if err != nil {
			return nil, err
		}
		agentID = session.AgentID
	} else {
		var ok bool
		session, ok = t.manager.Get(agentID)
		if !ok {
			return nil, relay.ErrNodeDisconnected
		}
	}
	if request.TargetConnectionID != "" {
		target, ok := t.manager.GetConnection(agentID, request.TargetConnectionID)
		if !ok {
			return nil, relay.ErrNodeDisconnected
		}
		if request.TargetConnectionEpoch != 0 && target.ConnectionEpoch != request.TargetConnectionEpoch {
			return nil, relay.ErrEpoch
		}
		session = target
	}
	if session == nil {
		return nil, relay.ErrNodeDisconnected
	}
	if request.StrictOpen && !session.Supports(protocol.CapabilityStreamOpenResult) {
		// Strict opens require an explicit Agent acknowledgement. Reject before
		// sending so legacy Agents never wait for a frame they cannot emit.
		return nil, ErrCapability
	}
	payload, err := protocol.EncodeStreamOpenPayload(protocol.StreamOpenPayload{
		AgentID: agentID, Protocol: request.Protocol, TargetHost: request.TargetHost,
		TargetPort: request.TargetPort, TargetScheme: request.TargetScheme,
		HostHeader: request.HostHeader, TLSServerName: request.TLSServerName,
		Metadata: append([]byte(nil), request.Metadata...),
	})
	if err != nil {
		return nil, err
	}
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil, errAgentRelayClosed
	}
	generation := agentRelayGeneration{agentID: agentID, connectionID: session.ConnectionID, connectionEpoch: session.ConnectionEpoch, serverGeneration: session.serverGeneration}
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
	if t.windows.MaxActiveStreamsPerAgent > 0 &&
		t.agentStreamCountLocked(agentID) >= t.windows.MaxActiveStreamsPerAgent {
		t.mu.Unlock()
		return nil, ErrAgentRelayStreamCapacity
	}
	stream := &agentRelayStream{
		transport: t, agentID: agentID, connectionID: session.ConnectionID,
		connectionEpoch: session.ConnectionEpoch, serverGeneration: session.serverGeneration,
		wireID: uint32(allocator.next), controlCh: make(chan protocol.Frame, 16), done: make(chan struct{}),
		controlSignal: make(chan struct{}, 1), dataSignal: make(chan struct{}, 1),
	}
	// A zero or undersized window means "unlimited" to a legacy Agent and lets a
	// fast peer overflow the inbound buffer, while an oversized one would size the
	// guard itself from an unverifiable number. NegotiateReceiveWindow keeps both
	// ends on a window that can always be refilled by a whole frame.
	window := t.windows.windowFor(request.InitialWindow)
	stream.receiveBudget = int(window)
	if sendState, stateErr := protocol.NewStreamState(stream.wireID, protocol.DefaultAgentReceiveWindow); stateErr == nil {
		_ = sendState.OpenLocal()
		stream.sendState = sendState
	}
	stream.windowSignal = make(chan struct{}, 1)
	if request.StrictOpen {
		stream.strictOpen = true
		stream.openResult = make(chan protocol.OpenResultPayload, 1)
	}
	t.streams[stream.key()] = stream
	t.mu.Unlock()
	openFlags := uint16(0)
	if request.StrictOpen {
		openFlags |= protocol.FlagStrictOpen
	}
	openFrame := protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, Flags: openFlags, StreamID: stream.wireID, Window: window, Payload: payload}
	if err := t.manager.sendServerGeneration(stream.agentID, stream.serverGeneration, openFrame); err != nil {
		t.detach(stream)
		stream.fail(err)
		return nil, err
	}
	return stream, nil
}

// OpenStreamResult waits for an Agent OPEN_RESULT in strict mode. Legacy opens
// retain the immediate-success contract used by existing route handlers.
func (t *AgentRelayTransport) OpenStreamResult(ctx context.Context, request relay.StreamRequest) (io.ReadWriteCloser, relay.RelayOpenResult, error) {
	stream, err := t.OpenStream(ctx, request)
	if err != nil || stream == nil {
		code := protocol.OpenResultCodeInternalError
		if errors.Is(err, ErrCapability) {
			code = protocol.OpenResultCodeUnsupportedCapability
			err = nil
		}
		return nil, relay.RelayOpenResult{Payload: failureResult(protocol.OpenResultStageRelay, code)}, err
	}
	agentStream, ok := stream.(*agentRelayStream)
	if !ok {
		_ = stream.Close()
		return nil, relay.RelayOpenResult{Payload: failureResult(protocol.OpenResultStageRelay, protocol.OpenResultCodeInternalError)}, nil
	}
	if !request.StrictOpen {
		return stream, relay.RelayOpenResult{Payload: protocol.OpenResultPayload{Accepted: true, Stage: protocol.OpenResultStageConnect, Code: protocol.OpenResultCodeOK}}, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var payload protocol.OpenResultPayload
	select {
	case payload = <-agentStream.openResult:
	default:
		select {
		case payload = <-agentStream.openResult:
		case <-agentStream.done:
			return nil, relay.RelayOpenResult{Payload: failureResult(protocol.OpenResultStageRelay, protocol.OpenResultCodeInternalError)}, nil
		case <-ctx.Done():
			_ = stream.Close()
			return nil, relay.RelayOpenResult{Payload: failureResult(protocol.OpenResultStageRelay, protocol.OpenResultCodeTimeout)}, ctx.Err()
		}
	}
	if !payload.Accepted {
		t.detach(agentStream)
		agentStream.finish(protocol.ErrStreamReset)
		return nil, relay.RelayOpenResult{Payload: payload}, nil
	}
	return stream, relay.RelayOpenResult{Payload: payload}, nil
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
	session := t.manager.getServerGeneration(serverGeneration)
	if session == nil || session.serverGeneration != serverGeneration {
		return nil
	}
	session.mu.RLock()
	if session.closed || session.closing {
		session.mu.RUnlock()
		return nil
	}
	key := agentRelayStreamKey{agentRelayGeneration: agentRelayGeneration{agentID: agentID, connectionID: session.ConnectionID, connectionEpoch: session.ConnectionEpoch, serverGeneration: serverGeneration}, wireID: frame.StreamID}
	t.mu.Lock()
	stream := t.streams[key]
	if stream == nil || stream.agentID != agentID || stream.connectionID != session.ConnectionID || stream.connectionEpoch != session.ConnectionEpoch || stream.serverGeneration != serverGeneration {
		t.mu.Unlock()
		session.mu.RUnlock()
		return nil
	}
	var resetStream bool
	switch frame.Type {
	case protocol.FrameOpenResult:
		payload, decodeErr := protocol.DecodeOpenResultPayload(frame.Payload)
		if decodeErr != nil || !stream.deliverOpenResult(payload) {
			delete(t.streams, key)
			stream.fail(protocol.ErrInvalidFrame)
			resetStream = true
		}
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
	case protocol.FrameWindowUpdate:
		// Credit the Server's send window first so a blocked Write resumes even
		// when no consumer ever drains controlCh.
		if stream.sendState != nil {
			if windowErr := stream.sendState.AddSendWindow(frame.Window); windowErr == nil {
				select {
				case stream.windowSignal <- struct{}{}:
				default:
				}
			}
		}
		select {
		case stream.controlCh <- frame:
			select {
			case stream.controlSignal <- struct{}{}:
			default:
			}
		default:
			// Best-effort mirror only. The authoritative credit was applied to
			// sendState above, and the WebSSH/SFTP consumer never polls
			// ReadControl, so a full mirror queue must not kill an otherwise
			// healthy bulk upload: it used to fail the stream with
			// ErrWindowExhausted once more than cap(controlCh) updates arrived.
		}
	case protocol.FrameReset:
		delete(t.streams, key)
		stream.fail(protocol.ErrStreamReset)
	}
	t.mu.Unlock()
	session.mu.RUnlock()
	if resetStream {
		_ = t.manager.sendServerGeneration(agentID, serverGeneration, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameReset, StreamID: frame.StreamID})
	}
	return nil
}

// HandleAgentFrame preserves the legacy Epoch-based callback API. Ambiguous
// same-epoch reconnects fail closed rather than guessing a connection.
func (t *AgentRelayTransport) HandleAgentFrame(agentID string, epoch int64, frame protocol.Frame) error {
	if t == nil {
		return errAgentRelayClosed
	}
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
	var generation agentRelayGeneration
	for key, stream := range t.streams {
		if stream.agentID == agentID && stream.serverGeneration == serverGeneration {
			delete(t.streams, key)
			failed = append(failed, stream)
			generation = key.agentRelayGeneration
		}
	}
	if len(failed) > 0 {
		delete(t.allocators, generation)
	}
	t.mu.Unlock()
	for _, stream := range failed {
		stream.fail(relay.ErrNodeDisconnected)
	}
}

// FailAgentGeneration preserves the legacy Epoch-based teardown API. It fails
// closed for an ambiguous same-epoch reconnect instead of touching the live
// replacement generation.
func (t *AgentRelayTransport) FailAgentGeneration(agentID string, epoch int64) {
	if t == nil {
		return
	}
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

// ErrAgentRelayStreamCapacity reports that an Agent already holds as many live
// streams on this node as `server.stream.max_active_per_agent` allows. It maps to
// the existing retryable queue_full open result, so no wire value is invented and
// a legacy Client that does not read OPEN_RESULT still sees a closed stream.
var ErrAgentRelayStreamCapacity = errors.New("agent relay: active stream capacity reached")

// ActiveStreamsForAgent counts every live stream toward one Agent on this node,
// across all of its connections, which is what the per-agent ceiling bounds.
func (t *AgentRelayTransport) ActiveStreamsForAgent(agentID string) int {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.agentStreamCountLocked(agentID)
}

func (t *AgentRelayTransport) agentStreamCountLocked(agentID string) int {
	count := 0
	for _, stream := range t.streams {
		if stream.agentID == agentID {
			count++
		}
	}
	return count
}

// ActiveStreams returns the number of currently open local streams for one
// Agent connection. It is a process-local input to least-connections routing.
func (t *AgentRelayTransport) ActiveStreams(agentID, connectionID string) int {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	count := 0
	for _, stream := range t.streams {
		if stream.agentID == agentID && stream.connectionID == connectionID {
			count++
		}
	}
	return count
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
	connectionID     string
	connectionEpoch  int64
	serverGeneration uint64
	wireID           uint32
	controlCh        chan protocol.Frame
	controlSignal    chan struct{}
	// dataSignal wakes a blocked Read after inbound bytes are appended. It is a
	// wake token rather than a byte channel because the receive window is
	// accounted in bytes: slotting the buffer by frame count let a peer that
	// stayed inside its window with many small frames overflow it and take a
	// fatal RESET mid-transfer.
	dataSignal     chan struct{}
	openResult     chan protocol.OpenResultPayload
	done           chan struct{}
	doneOnce       sync.Once
	closeOnce      sync.Once
	openResultOnce sync.Once
	strictOpen     bool
	mu             sync.Mutex
	readBuf        []byte
	// receiveBudget is the inbound byte budget and equals the window advertised
	// in OPEN_STREAM. Credit is only returned after Read consumes bytes, so a
	// window-respecting peer can never exceed it.
	receiveBudget int
	// readPos is the consumed prefix of readBuf. Tracking it keeps Read
	// O(bytes returned): compacting on every Read made a consumer that reads in
	// small chunks re-memmove the whole tail per call, which is quadratic and
	// showed up as a 150 s package under `-race`.
	readPos    int
	err        error
	localHalf  bool
	remoteHalf bool
	// sendState mirrors the credit the Agent grants for Server-to-Agent DATA.
	// The Agent enforces it unconditionally, so the Server must consume it or
	// a bulk upload resets the stream mid-transfer.
	sendState *protocol.StreamState
	// receiveUnacked accumulates bytes handed to Read until a WINDOW_UPDATE is
	// owed to the Agent; without it the Agent's send window never refills and
	// a bulk download stalls the relay into a fatal backpressure error.
	receiveUnacked uint32
	windowSignal   chan struct{}
}

func (s *agentRelayStream) deliverOpenResult(payload protocol.OpenResultPayload) bool {
	if !s.strictOpen {
		return false
	}
	delivered := false
	s.openResultOnce.Do(func() {
		s.openResult <- payload
		delivered = true
	})
	return delivered
}

func (s *agentRelayStream) key() agentRelayStreamKey {
	return agentRelayStreamKey{agentRelayGeneration: agentRelayGeneration{agentID: s.agentID, connectionID: s.connectionID, connectionEpoch: s.connectionEpoch, serverGeneration: s.serverGeneration}, wireID: s.wireID}
}

func (s *agentRelayStream) Read(buffer []byte) (int, error) {
	if len(buffer) == 0 {
		return 0, nil
	}
	for {
		s.mu.Lock()
		if pending := len(s.readBuf) - s.readPos; pending > 0 {
			n := copy(buffer, s.readBuf[s.readPos:])
			s.readPos += n
			if s.readPos == len(s.readBuf) {
				// Hand the whole buffer back; the next enqueue starts fresh.
				s.readBuf = nil
				s.readPos = 0
			}
			s.mu.Unlock()
			s.releaseReceiveWindow(n)
			return n, nil
		}
		// Clear a stale wake token while still holding the lock: enqueue appends
		// under the same lock, so no byte can land between this drain and the
		// blocking wait below.
		select {
		case <-s.dataSignal:
		default:
		}
		s.mu.Unlock()
		select {
		case <-s.controlSignal:
			return 0, nil
		default:
		}
		s.mu.Lock()
		err := s.err
		s.mu.Unlock()
		if err != nil {
			return 0, err
		}
		select {
		case <-s.dataSignal:
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
	// The Agent enforces its receive window with a stream RESET, so sending
	// beyond the credited window is not an option: wait for WINDOW_UPDATE
	// credit instead and let the pressure propagate to the caller.
	sent := 0
	for sent < len(payload) {
		chunk, err := s.consumeSendWindow(len(payload) - sent)
		if err != nil {
			if sent > 0 {
				return sent, err
			}
			return 0, err
		}
		frame := protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: s.wireID, Payload: append([]byte(nil), payload[sent:sent+chunk]...)}
		if err := s.transport.manager.sendServerGeneration(s.agentID, s.serverGeneration, frame); err != nil {
			s.transport.detach(s)
			s.fail(err)
			return sent, err
		}
		sent += chunk
	}
	return sent, nil
}

// consumeSendWindow reserves up to `remaining` bytes of Agent-granted credit,
// blocking while the window is empty. It never holds the stream lock while
// waiting, so inbound WINDOW_UPDATE frames keep flowing.
func (s *agentRelayStream) consumeSendWindow(remaining int) (int, error) {
	for {
		s.mu.Lock()
		if (s.err != nil && !errors.Is(s.err, io.EOF)) || s.localHalf {
			err := s.err
			if err == nil {
				err = io.ErrClosedPipe
			}
			s.mu.Unlock()
			return 0, err
		}
		state := s.sendState
		s.mu.Unlock()
		if state == nil {
			return remaining, nil
		}
		want := uint32(remaining)
		if want > protocol.MaxStreamFrame {
			want = protocol.MaxStreamFrame
		}
		if err := state.ConsumeSend(want); err == nil {
			return int(want), nil
		} else if !errors.Is(err, protocol.ErrWindowExhausted) {
			return 0, err
		}
		// Partial credit is still usable: a 40 KiB write may proceed as a
		// 32 KiB frame now and the remainder after the next WINDOW_UPDATE.
		if available := state.SendWindow(); available > 0 {
			if available < want {
				want = available
			}
			if err := state.ConsumeSend(want); err == nil {
				return int(want), nil
			} else if !errors.Is(err, protocol.ErrWindowExhausted) {
				return 0, err
			}
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-s.windowSignal:
			timer.Stop()
		case <-timer.C:
		case <-s.done:
			timer.Stop()
			s.mu.Lock()
			err := s.err
			s.mu.Unlock()
			if err == nil {
				err = io.ErrClosedPipe
			}
			return 0, err
		}
	}
}

// releaseReceiveWindow returns credit to the Agent once the consumer has
// actually read bytes, which is what unblocks a throttled `sz` on the peer.
func (s *agentRelayStream) releaseReceiveWindow(consumed int) {
	if consumed <= 0 {
		return
	}
	s.mu.Lock()
	if s.err != nil {
		s.mu.Unlock()
		return
	}
	s.receiveUnacked += uint32(consumed)
	buffered := len(s.readBuf) - s.readPos
	remaining := uint32(0)
	if free := s.receiveBudget - buffered; free > 0 {
		remaining = uint32(free)
	}
	update := uint32(0)
	threshold := s.transport.windows.updateThreshold(uint32(max(s.receiveBudget, 0)))
	if protocol.ShouldFlushWindowUpdate(remaining, s.receiveUnacked, threshold) {
		update = s.receiveUnacked
		s.receiveUnacked = 0
	}
	s.mu.Unlock()
	if update == 0 {
		return
	}
	// Best-effort: a failed control write fails the stream elsewhere.
	_ = s.WriteControl(protocol.Frame{Type: protocol.FrameWindowUpdate, Window: update})
}

func (s *agentRelayStream) WriteControl(frame protocol.Frame) error {
	if frame.Type != protocol.FrameWindowUpdate {
		return protocol.ErrInvalidFrame
	}
	frame.Version = protocol.CurrentVersion
	frame.StreamID = s.wireID
	if err := s.transport.manager.sendServerGeneration(s.agentID, s.serverGeneration, frame); err != nil {
		s.transport.detach(s)
		s.fail(err)
		return err
	}
	return nil
}

func (s *agentRelayStream) ReadControl() (protocol.Frame, bool) {
	select {
	case frame := <-s.controlCh:
		select {
		case <-s.controlSignal:
		default:
		}
		return frame, true
	default:
		return protocol.Frame{}, false
	}
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
	budget := s.receiveBudget
	if budget <= 0 {
		budget = protocol.DefaultServerReceiveWindow
	}
	if s.readPos > 0 {
		// Drop the consumed prefix before appending: otherwise a stream that
		// alternates small reads and inbound frames grows readBuf without bound
		// while the accounted bytes stay inside the window.
		remaining := copy(s.readBuf, s.readBuf[s.readPos:])
		s.readBuf = s.readBuf[:remaining]
		s.readPos = 0
	}
	if len(s.readBuf)+len(payload) > budget {
		// Unreachable for a peer that honours the OPEN_STREAM window, because
		// credit is released only after Read consumed the bytes. Retained as a
		// guard so a window-ignoring peer fails one stream, not the process.
		return relay.ErrBackpressure
	}
	if len(payload) == 0 {
		return nil
	}
	s.readBuf = append(s.readBuf, payload...)
	select {
	case s.dataSignal <- struct{}{}:
	default:
	}
	return nil
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
