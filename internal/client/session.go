package client

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/observability"
	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
	streamsession "github.com/tunnelmesh/tunnelmesh/internal/session"
)

var ErrSessionClosed = errors.New("client session closed")

type FrameTransport interface {
	Send(protocol.Frame) error
	Close() error
}
type ReceiveTransport interface {
	FrameTransport
	Receive() (protocol.Frame, error)
}
type StreamRequest struct {
	StreamID             uint32
	AgentID              string
	Protocol, TargetHost string
	TargetPort           int
	Metadata             []byte
}
type StreamOpenPayload = protocol.StreamOpenPayload
type SessionOpenMode uint8

const (
	SessionOpenLegacy SessionOpenMode = iota
	SessionOpenStrict
	SessionOpenFlowControl
)

const (
	defaultStreamWindow          = 262144
	defaultWindowUpdateThreshold = 131072
)

type Session struct {
	mu                    sync.RWMutex
	transport             FrameTransport
	closed                bool
	nextID                atomic.Uint32
	recvOnce              sync.Once
	done                  chan struct{}
	doneOnce              sync.Once
	runErr                error
	Metrics               *observability.Metrics
	writer                *streamsession.FairFrameWriter
	streams               map[uint32]*frameStream
	datagrams             map[uint32]*frameDatagramStream
	pendingPings          map[uint64]chan struct{}
	nextPingID            atomic.Uint64
	openMode              SessionOpenMode
	initialWindow         uint32
	windowUpdateThreshold uint32
}

func NewSession(tr FrameTransport) *Session {
	return NewSessionWithOpenMode(tr, SessionOpenLegacy)
}

func NewSessionWithOpenMode(tr FrameTransport, mode SessionOpenMode) *Session {
	s := &Session{
		transport: tr, done: make(chan struct{}),
		streams: make(map[uint32]*frameStream), datagrams: make(map[uint32]*frameDatagramStream),
		pendingPings: make(map[uint64]chan struct{}),
	}
	if tr != nil {
		s.writer = streamsession.NewFairFrameWriter(tr.Send, streamsession.FairWriterConfig{
			ControlQueueSize: 64, StreamQueueBytes: 262144, QuantumBytes: 32768,
		})
		go s.runWriter()
	}
	s.nextID.Store(1)
	if mode != SessionOpenLegacy {
		s.openMode = mode
	}
	s.initialWindow = defaultStreamWindow
	s.windowUpdateThreshold = defaultWindowUpdateThreshold
	return s
}

func (s *Session) runWriter() {
	if err := s.writer.Run(context.Background()); err != nil {
		s.finishReceive(err)
	}
}

func (s *Session) sendFrame(frame protocol.Frame) error {
	if s == nil {
		return ErrSessionClosed
	}
	if s.writer == nil {
		if s.transport == nil {
			return ErrSessionClosed
		}
		return s.transport.Send(frame)
	}
	if frame.Type == protocol.FrameData && frame.StreamID != 0 {
		return s.writer.EnqueueData(frame.StreamID, frame)
	}
	return s.writer.EnqueueControl(frame)
}

func (s *Session) OpenMode() SessionOpenMode {
	if s == nil {
		return SessionOpenLegacy
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.openMode
}
func (s *Session) OpenStream(ctx context.Context, req StreamRequest) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed || s.transport == nil {
		return ErrSessionClosed
	}
	if req.StreamID == 0 {
		return errors.New("stream id required")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	payload, err := protocol.EncodeStreamOpenPayload(protocol.StreamOpenPayload{AgentID: req.AgentID, Protocol: req.Protocol, TargetHost: req.TargetHost, TargetPort: req.TargetPort, Metadata: req.Metadata})
	if err != nil {
		return err
	}
	frame := protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: req.StreamID, Payload: payload}
	if s.OpenMode() == SessionOpenFlowControl {
		frame.Window = s.initialWindow
	}
	err = s.sendFrame(frame)
	if s.Metrics != nil {
		if err != nil {
			s.Metrics.ObserveStream(req.Protocol, "failed", observability.NormalizeErrorClass(err))
		} else {
			s.Metrics.ObserveStream(req.Protocol, "accepted", "")
		}
	}
	return err
}

// Start begins the single receive dispatcher even before a logical stream is
// opened so connection-level PING frames are answered while the session is idle.
func (s *Session) Start() {
	if s == nil {
		return
	}
	tr, ok := s.transport.(ReceiveTransport)
	if !ok || tr == nil {
		s.finishReceive(errors.New("client session transport does not receive frames"))
		return
	}
	s.recvOnce.Do(func() { go s.receiveLoop(tr) })
}

func (s *Session) Wait(ctx context.Context) error {
	if s == nil {
		return ErrSessionClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		_ = s.Close()
		return ctx.Err()
	case <-s.done:
		s.mu.RLock()
		err := s.runErr
		s.mu.RUnlock()
		return err
	}
}

// Ping sends a correlation-ID PING and waits for the peer's matching PONG.
// It gives the connection pool a current RTT sample for tie-breaking.
func (s *Session) Ping(ctx context.Context) (time.Duration, error) {
	if s == nil {
		return 0, ErrSessionClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	id := s.nextPingID.Add(1)
	pong := make(chan struct{})
	s.mu.Lock()
	if s.closed || s.transport == nil {
		s.mu.Unlock()
		return 0, ErrSessionClosed
	}
	s.pendingPings[id] = pong
	s.mu.Unlock()

	payload := make([]byte, 8)
	binary.BigEndian.PutUint64(payload, id)
	started := time.Now()
	if err := s.sendFrame(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FramePing, Payload: payload}); err != nil {
		s.finishPing(id)
		return 0, err
	}
	select {
	case <-pong:
		return time.Since(started), nil
	case <-s.done:
		return 0, ErrSessionClosed
	case <-ctx.Done():
		s.finishPing(id)
		return 0, ctx.Err()
	}
}

func (s *Session) finishPing(id uint64) {
	s.mu.Lock()
	pong, exists := s.pendingPings[id]
	delete(s.pendingPings, id)
	s.mu.Unlock()
	if exists {
		close(pong)
	}
}

// OpenDatagram opens a message-oriented UDP association. Each WriteDatagram
// emits exactly one FrameData payload and each ReadDatagram returns exactly one
// received FrameData payload.
func (s *Session) OpenDatagram(ctx context.Context, req StreamRequest) (DatagramStream, error) {
	if s == nil {
		return nil, ErrSessionClosed
	}
	if req.StreamID == 0 {
		req.StreamID = s.nextID.Add(1) - 1
	}
	tr, ok := s.transport.(ReceiveTransport)
	if !ok || tr == nil {
		return nil, errors.New("client session transport does not receive frames")
	}
	stream := newFrameDatagramStream(s, req.StreamID)
	s.mu.Lock()
	if s.closed || s.transport == nil {
		s.mu.Unlock()
		return nil, ErrSessionClosed
	}
	if _, exists := s.datagrams[req.StreamID]; exists {
		s.mu.Unlock()
		return nil, errors.New("stream id already in use")
	}
	s.datagrams[req.StreamID] = stream
	s.mu.Unlock()
	s.Start()
	if err := s.OpenStream(ctx, req); err != nil {
		s.removeDatagram(req.StreamID)
		return nil, err
	}
	return stream, nil
}

// OpenStreamConn opens a logical byte stream over a receiving frame transport.
// The regular OpenStream method is retained for callers that only need to
// enqueue an OPEN frame; this variant wires DATA/HALF_CLOSE frames to an
// io.ReadWriteCloser for local forwarding.
func (s *Session) OpenStreamConn(ctx context.Context, req StreamRequest) (io.ReadWriteCloser, error) {
	if s == nil {
		return nil, ErrSessionClosed
	}
	if req.StreamID == 0 {
		req.StreamID = s.nextID.Add(1) - 1
		if req.StreamID == 0 {
			req.StreamID = s.nextID.Add(1) - 1
		}
	}
	tr, ok := s.transport.(ReceiveTransport)
	if !ok || tr == nil {
		return nil, errors.New("client session transport does not receive frames")
	}
	stream := newFrameStream(s, req.StreamID)
	s.mu.Lock()
	if s.closed || s.transport == nil {
		s.mu.Unlock()
		return nil, ErrSessionClosed
	}
	if _, exists := s.streams[req.StreamID]; exists {
		s.mu.Unlock()
		return nil, errors.New("stream id already in use")
	}
	s.streams[req.StreamID] = stream
	s.mu.Unlock()
	s.Start()
	if err := s.OpenStream(ctx, req); err != nil {
		s.removeStream(req.StreamID)
		return nil, err
	}
	return stream, nil
}

// OpenStreamResult returns a stream only after a strict OPEN_RESULT. DATA sent
// by the peer before the result is buffered so slow dials do not lose bytes.
func (s *Session) OpenStreamResult(ctx context.Context, req StreamRequest) (io.ReadWriteCloser, protocol.OpenResultPayload, error) {
	if s == nil {
		return nil, failureOpenResult(protocol.OpenResultCodeInternalError), ErrSessionClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if req.StreamID == 0 {
		req.StreamID = s.nextID.Add(1) - 1
		if req.StreamID == 0 {
			req.StreamID = s.nextID.Add(1) - 1
		}
	}
	tr, ok := s.transport.(ReceiveTransport)
	if !ok || tr == nil {
		return nil, failureOpenResult(protocol.OpenResultCodeInternalError), errors.New("client session transport does not receive frames")
	}
	stream := newFrameStream(s, req.StreamID)
	stream.openResult = make(chan protocol.OpenResultPayload, 1)
	s.mu.Lock()
	if s.closed || s.transport == nil {
		s.mu.Unlock()
		return nil, failureOpenResult(protocol.OpenResultCodeInternalError), ErrSessionClosed
	}
	if _, exists := s.streams[req.StreamID]; exists {
		s.mu.Unlock()
		return nil, failureOpenResult(protocol.OpenResultCodeInternalError), errors.New("stream id already in use")
	}
	s.streams[req.StreamID] = stream
	s.mu.Unlock()
	s.Start()
	payload, err := protocol.EncodeStreamOpenPayload(protocol.StreamOpenPayload{
		AgentID: req.AgentID, Protocol: req.Protocol, TargetHost: req.TargetHost,
		TargetPort: req.TargetPort, Metadata: req.Metadata,
	})
	if err != nil {
		s.removeStream(req.StreamID)
		return nil, failureOpenResult(protocol.OpenResultCodeInternalError), err
	}
	openFrame := protocol.Frame{
		Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, Flags: protocol.FlagStrictOpen,
		StreamID: req.StreamID, Payload: payload,
	}
	if s.OpenMode() == SessionOpenFlowControl {
		openFrame.Window = s.initialWindow
	}
	if err := s.sendFrame(openFrame); err != nil {
		s.removeStream(req.StreamID)
		stream.fail(err)
		return nil, failureOpenResult(protocol.OpenResultCodeInternalError), err
	}
	var result protocol.OpenResultPayload
	select {
	case result = <-stream.openResult:
	default:
		select {
		case result = <-stream.openResult:
		case <-stream.done:
			s.removeStream(req.StreamID)
			return nil, failureOpenResult(protocol.OpenResultCodeInternalError), nil
		case <-ctx.Done():
			s.removeStream(req.StreamID)
			stream.fail(ctx.Err())
			_ = s.sendFrame(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameReset, StreamID: req.StreamID})
			return nil, failureOpenResult(protocol.OpenResultCodeTimeout), ctx.Err()
		}
	}
	if !result.Accepted {
		s.removeStream(req.StreamID)
		stream.fail(protocol.ErrStreamReset)
		_ = s.sendFrame(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameReset, StreamID: req.StreamID})
		return nil, result, nil
	}
	return stream, result, nil
}

// OpenLogicalStream is a descriptive alias used by forwarders embedding a
// client Session directly.
func (s *Session) OpenLogicalStream(ctx context.Context, req StreamRequest) (io.ReadWriteCloser, error) {
	return s.OpenStreamConn(ctx, req)
}

// SessionOpener adapts a frame Session to the StreamOpener interface consumed
// by local TCP/UDP/HTTP listeners.
type SessionOpener struct{ Session *Session }

func NewSessionOpener(s *Session) SessionOpener { return SessionOpener{Session: s} }
func (o SessionOpener) OpenStream(ctx context.Context, req StreamRequest) (io.ReadWriteCloser, error) {
	if o.Session == nil {
		return nil, ErrSessionClosed
	}
	if o.Session.OpenMode() != SessionOpenLegacy {
		stream, result, err := o.Session.OpenStreamResult(ctx, req)
		if err != nil {
			return nil, err
		}
		if !result.Accepted {
			return nil, protocol.ErrStreamReset
		}
		return stream, nil
	}
	return o.Session.OpenStreamConn(ctx, req)
}

func (o SessionOpener) OpenStreamResult(ctx context.Context, req StreamRequest) (io.ReadWriteCloser, protocol.OpenResultPayload, error) {
	if o.Session == nil {
		return nil, failureOpenResult(protocol.OpenResultCodeInternalError), ErrSessionClosed
	}
	if o.Session.OpenMode() != SessionOpenLegacy {
		return o.Session.OpenStreamResult(ctx, req)
	}
	stream, err := o.Session.OpenStreamConn(ctx, req)
	if err != nil || stream == nil {
		return nil, failureOpenResult(protocol.OpenResultCodeInternalError), err
	}
	return stream, protocol.OpenResultPayload{Accepted: true, Stage: protocol.OpenResultStageConnect, Code: protocol.OpenResultCodeOK}, nil
}
func (o SessionOpener) OpenDatagram(ctx context.Context, req StreamRequest) (DatagramStream, error) {
	if o.Session == nil {
		return nil, ErrSessionClosed
	}
	return o.Session.OpenDatagram(ctx, req)
}

func (s *Session) receiveLoop(tr ReceiveTransport) {
	for {
		f, err := tr.Receive()
		if err != nil {
			s.mu.Lock()
			for id, stream := range s.streams {
				stream.fail(err)
				delete(s.streams, id)
			}
			for id, stream := range s.datagrams {
				stream.finish(err)
				delete(s.datagrams, id)
			}
			s.mu.Unlock()
			s.finishReceive(err)
			return
		}
		if f.Type == protocol.FramePing {
			if err := s.sendFrame(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FramePong, Payload: append([]byte(nil), f.Payload...)}); err != nil {
				s.finishReceive(err)
				return
			}
			continue
		}
		if f.Type == protocol.FramePong {
			if len(f.Payload) >= 8 {
				s.finishPing(binary.BigEndian.Uint64(f.Payload[:8]))
			}
			if s.Metrics != nil {
				s.Metrics.ObserveHeartbeat("client", "pong", 0)
			}
			continue
		}
		s.mu.RLock()
		stream := s.streams[f.StreamID]
		datagram := s.datagrams[f.StreamID]
		s.mu.RUnlock()
		if stream == nil && datagram == nil {
			continue
		}
		switch f.Type {
		case protocol.FrameOpenResult:
			payload, decodeErr := protocol.DecodeOpenResultPayload(f.Payload)
			if decodeErr != nil || stream == nil || !stream.deliverOpenResult(payload) {
				if stream != nil {
					stream.finish(protocol.ErrInvalidFrame)
					s.removeStream(f.StreamID)
				}
				_ = s.sendFrame(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameReset, StreamID: f.StreamID})
				continue
			}
			if !payload.Accepted {
				stream.finish(protocol.ErrStreamReset)
			}
		case protocol.FrameData:
			if s.Metrics != nil && len(f.Payload) > 0 {
				s.Metrics.ObserveBytes("client", "inbound", "frame", int64(len(f.Payload)))
			}
			if stream != nil {
				stream.push(f.Payload)
			} else {
				datagram.push(f.Payload)
			}
		case protocol.FrameHalfClose:
			if stream != nil {
				stream.finish(io.EOF)
			} else {
				datagram.finish(io.EOF)
				s.removeDatagram(f.StreamID)
			}
		case protocol.FrameWindowUpdate:
			if stream != nil && stream.flow != nil {
				if err := stream.flow.AddSendWindow(f.Window); err != nil {
					stream.finish(err)
					s.removeStream(f.StreamID)
					_ = s.sendFrame(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameReset, StreamID: f.StreamID})
				}
			}
		case protocol.FrameReset:
			if stream != nil {
				stream.finish(protocol.ErrStreamReset)
				s.removeStream(f.StreamID)
			} else {
				datagram.finish(protocol.ErrStreamReset)
				s.removeDatagram(f.StreamID)
			}
		}
	}
}

func (s *Session) finishReceive(err error) {
	if s == nil {
		return
	}
	s.doneOnce.Do(func() {
		s.mu.Lock()
		s.runErr = err
		s.mu.Unlock()
		close(s.done)
	})
}

func (s *Session) removeDatagram(id uint32) {
	s.mu.Lock()
	delete(s.datagrams, id)
	s.mu.Unlock()
}

func (s *Session) removeStream(id uint32) {
	s.mu.Lock()
	delete(s.streams, id)
	s.mu.Unlock()
}

func failureOpenResult(code protocol.OpenResultCode) protocol.OpenResultPayload {
	return protocol.OpenResultPayload{
		Accepted: false, Stage: protocol.OpenResultStageConnect, Code: code,
		Retryable: code == protocol.OpenResultCodeQueueFull || code == protocol.OpenResultCodeTimeout,
	}
}

type frameStream struct {
	session        *Session
	id             uint32
	readQueue      *streamsession.BoundedFrameQueue
	readSignal     chan struct{}
	openResult     chan protocol.OpenResultPayload
	done           chan struct{}
	mu             sync.Mutex
	readBuf        []byte
	err            error
	finishOnce     sync.Once
	resultOnce     sync.Once
	closeOnce      sync.Once
	halfOnce       sync.Once
	flow           *protocol.StreamState
	receiveUnacked uint32
}

func (s *frameStream) deliverOpenResult(payload protocol.OpenResultPayload) bool {
	if s.openResult == nil {
		return false
	}
	delivered := false
	s.resultOnce.Do(func() {
		s.openResult <- payload
		delivered = true
	})
	return delivered
}

func newFrameStream(s *Session, id uint32) *frameStream {
	stream := &frameStream{
		session: s, id: id, readQueue: streamsession.NewBoundedFrameQueue(262144),
		readSignal: make(chan struct{}, 1), done: make(chan struct{}),
	}
	if s.OpenMode() == SessionOpenFlowControl {
		if state, err := protocol.NewStreamState(id, s.initialWindow); err == nil {
			_ = state.OpenLocal()
			stream.flow = state
		}
	}
	return stream
}
func (s *frameStream) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for {
		s.mu.Lock()
		if len(s.readBuf) > 0 {
			n := copy(p, s.readBuf)
			s.readBuf = s.readBuf[n:]
			s.mu.Unlock()
			return n, nil
		}
		s.mu.Unlock()
		// Drain DATA already queued before honoring a terminal frame. The
		// receiver can enqueue DATA and HALF_CLOSE back-to-back.
		if payload, ok := s.readQueue.TryPop(); ok {
			if len(payload.Payload) > 0 {
				s.mu.Lock()
				s.readBuf = append(s.readBuf, payload.Payload...)
				s.mu.Unlock()
			}
			s.releaseReceiveWindow(len(payload.Payload))
			continue
		}
		s.mu.Lock()
		err := s.err
		s.mu.Unlock()
		if err != nil {
			return 0, err
		}
		select {
		case <-s.readSignal:
			continue
		case <-s.done:
			if payload, ok := s.readQueue.TryPop(); ok {
				if len(payload.Payload) > 0 {
					s.mu.Lock()
					s.readBuf = append(s.readBuf, payload.Payload...)
					s.mu.Unlock()
				}
				s.releaseReceiveWindow(len(payload.Payload))
				continue
			}
			s.mu.Lock()
			err := s.err
			s.mu.Unlock()
			if err == nil {
				return 0, io.EOF
			}
			return 0, err
		}
	}
}
func (s *frameStream) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	s.mu.Lock()
	err := s.err
	s.mu.Unlock()
	if err != nil && !errors.Is(err, io.EOF) {
		return 0, err
	}
	s.session.mu.RLock()
	closed := s.session.closed
	tr := s.session.transport
	s.session.mu.RUnlock()
	if closed || tr == nil {
		return 0, ErrSessionClosed
	}
	if s.flow != nil {
		if err := s.flow.ConsumeSend(uint32(len(p))); err != nil {
			return 0, err
		}
	}
	if err := s.session.sendFrame(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: s.id, Payload: append([]byte(nil), p...)}); err != nil {
		return 0, err
	}
	return len(p), nil
}
func (s *frameStream) Close() error {
	s.closeOnce.Do(func() {
		_ = s.CloseWrite()
		s.finish(io.EOF)
		s.session.removeStream(s.id)
	})
	return nil
}
func (s *frameStream) CloseWrite() error {
	var err error
	s.halfOnce.Do(func() {
		s.session.mu.RLock()
		tr := s.session.transport
		closed := s.session.closed
		s.session.mu.RUnlock()
		if tr != nil && !closed {
			err = s.session.sendFrame(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameHalfClose, StreamID: s.id})
		}
	})
	return err
}
func (s *frameStream) push(p []byte) {
	select {
	case <-s.done:
		return
	default:
	}
	if s.flow != nil {
		if err := s.flow.Handle(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: s.id, Payload: p}); err != nil {
			s.finish(err)
			_ = s.session.sendFrame(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameReset, StreamID: s.id})
			s.session.removeStream(s.id)
			return
		}
	}
	if s.readQueue.TryPush(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: s.id, Payload: append([]byte(nil), p...)}) {
		select {
		case s.readSignal <- struct{}{}:
		default:
		}
		return
	}
	s.finish(protocol.ErrWindowExhausted)
	_ = s.session.sendFrame(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameReset, StreamID: s.id})
	s.session.removeStream(s.id)
}
func (s *frameStream) releaseReceiveWindow(n int) {
	if s.flow == nil || n <= 0 {
		return
	}
	s.mu.Lock()
	s.receiveUnacked += uint32(n)
	update := uint32(0)
	if s.receiveUnacked >= s.session.windowUpdateThreshold {
		update = s.receiveUnacked
		s.receiveUnacked = 0
	}
	s.mu.Unlock()
	if update == 0 {
		return
	}
	if err := s.flow.AddReceiveWindow(update); err == nil {
		_ = s.session.sendFrame(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameWindowUpdate, StreamID: s.id, Window: update})
	}
}
func (s *frameStream) finish(err error) {
	s.finishOnce.Do(func() {
		s.mu.Lock()
		s.err = err
		s.mu.Unlock()
		close(s.done)
	})
}
func (s *frameStream) fail(err error) { s.finish(err) }

type frameDatagramStream struct {
	session    *Session
	id         uint32
	readQueue  *streamsession.BoundedFrameQueue
	readSignal chan struct{}
	done       chan struct{}
	mu         sync.Mutex
	err        error
	finishOnce sync.Once
	closeOnce  sync.Once
}

func newFrameDatagramStream(s *Session, id uint32) *frameDatagramStream {
	return &frameDatagramStream{
		session: s, id: id, readQueue: streamsession.NewBoundedFrameQueue(262144),
		readSignal: make(chan struct{}, 1), done: make(chan struct{}),
	}
}
func (s *frameDatagramStream) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	b, err := s.ReadDatagram()
	if len(b) > len(p) {
		return copy(p, b), errors.New("client: datagram buffer too small")
	}
	return copy(p, b), err
}
func (s *frameDatagramStream) Write(p []byte) (int, error) {
	if err := s.WriteDatagram(p); err != nil {
		return 0, err
	}
	return len(p), nil
}
func (s *frameDatagramStream) ReadDatagram() ([]byte, error) {
	// Drain datagrams already accepted before reporting a terminal state.
	// Each queued frame is one datagram, preserving message boundaries.
	if frame, ok := s.readQueue.TryPop(); ok {
		return frame.Payload, nil
	}
	s.mu.Lock()
	err := s.err
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	select {
	case <-s.readSignal:
		if frame, ok := s.readQueue.TryPop(); ok {
			return frame.Payload, nil
		}
		return nil, io.EOF
	case <-s.done:
		if frame, ok := s.readQueue.TryPop(); ok {
			return frame.Payload, nil
		}
		s.mu.Lock()
		err := s.err
		s.mu.Unlock()
		if err == nil {
			err = io.EOF
		}
		return nil, err
	}
}
func (s *frameDatagramStream) WriteDatagram(p []byte) error {
	s.mu.Lock()
	err := s.err
	s.mu.Unlock()
	if err != nil {
		return err
	}
	s.session.mu.RLock()
	tr := s.session.transport
	closed := s.session.closed
	s.session.mu.RUnlock()
	if closed || tr == nil {
		return ErrSessionClosed
	}
	return s.session.sendFrame(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: s.id, Payload: append([]byte(nil), p...)})
}
func (s *frameDatagramStream) push(p []byte) {
	select {
	case <-s.done:
		return
	default:
	}
	// The receive loop must never block on an unread UDP stream. Reaching the
	// per-stream byte limit resets only this stream, not the WebSocket session.
	if s.readQueue.TryPush(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: s.id, Payload: append([]byte(nil), p...)}) {
		select {
		case s.readSignal <- struct{}{}:
		default:
		}
		return
	}
	s.finish(protocol.ErrWindowExhausted)
	_ = s.session.sendFrame(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameReset, StreamID: s.id})
	s.session.removeDatagram(s.id)
}
func (s *frameDatagramStream) finish(err error) {
	s.finishOnce.Do(func() { s.mu.Lock(); s.err = err; s.mu.Unlock(); close(s.done) })
}
func (s *frameDatagramStream) Close() error {
	s.closeOnce.Do(func() {
		s.finish(io.EOF)
		s.session.removeDatagram(s.id)
		s.session.mu.RLock()
		tr := s.session.transport
		closed := s.session.closed
		s.session.mu.RUnlock()
		if tr != nil && !closed {
			_ = s.session.sendFrame(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameHalfClose, StreamID: s.id})
		}
	})
	return nil
}
func (s *Session) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	tr := s.transport
	streams := make([]*frameStream, 0, len(s.streams))
	for id, stream := range s.streams {
		streams = append(streams, stream)
		delete(s.streams, id)
	}
	datagrams := make([]*frameDatagramStream, 0, len(s.datagrams))
	for id, stream := range s.datagrams {
		datagrams = append(datagrams, stream)
		delete(s.datagrams, id)
	}
	s.mu.Unlock()
	if s.writer != nil {
		_ = s.writer.Close()
	}
	if s.Metrics != nil {
		s.Metrics.ObserveConnection("client", "websocket", "closed", "")
	}
	for _, stream := range streams {
		stream.fail(ErrSessionClosed)
	}
	for _, stream := range datagrams {
		stream.finish(ErrSessionClosed)
	}
	if tr != nil {
		return tr.Close()
	}
	return nil
}
