package client

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
	"io"
	"sync"
	"sync/atomic"
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
type StreamOpenPayload struct {
	AgentID    string `json:"agent_id,omitempty"`
	Protocol   string `json:"protocol"`
	TargetHost string `json:"target_host"`
	TargetPort int    `json:"target_port"`
	Metadata   []byte `json:"metadata,omitempty"`
}
type Session struct {
	mu        sync.RWMutex
	transport FrameTransport
	closed    bool
	nextID    atomic.Uint32
	recvOnce  sync.Once
	streams   map[uint32]*frameStream
	datagrams map[uint32]*frameDatagramStream
}

func NewSession(tr FrameTransport) *Session {
	s := &Session{transport: tr, streams: make(map[uint32]*frameStream), datagrams: make(map[uint32]*frameDatagramStream)}
	s.nextID.Store(1)
	return s
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
	payload, err := json.Marshal(StreamOpenPayload{AgentID: req.AgentID, Protocol: req.Protocol, TargetHost: req.TargetHost, TargetPort: req.TargetPort, Metadata: req.Metadata})
	if err != nil {
		return err
	}
	return s.transport.Send(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: req.StreamID, Payload: payload})
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
	s.recvOnce.Do(func() { go s.receiveLoop(tr) })
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
	s.recvOnce.Do(func() { go s.receiveLoop(tr) })
	if err := s.OpenStream(ctx, req); err != nil {
		s.removeStream(req.StreamID)
		return nil, err
	}
	return stream, nil
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
	return o.Session.OpenStreamConn(ctx, req)
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
			return
		}
		s.mu.RLock()
		stream := s.streams[f.StreamID]
		datagram := s.datagrams[f.StreamID]
		s.mu.RUnlock()
		if stream == nil && datagram == nil {
			continue
		}
		switch f.Type {
		case protocol.FrameData:
			if stream != nil {
				stream.push(f.Payload)
			} else {
				datagram.push(f.Payload)
			}
		case protocol.FrameHalfClose:
			if stream != nil {
				stream.finish(io.EOF)
				s.removeStream(f.StreamID)
			} else {
				datagram.finish(io.EOF)
				s.removeDatagram(f.StreamID)
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

type frameStream struct {
	session    *Session
	id         uint32
	readCh     chan []byte
	done       chan struct{}
	mu         sync.Mutex
	readBuf    []byte
	err        error
	finishOnce sync.Once
	closeOnce  sync.Once
	halfOnce   sync.Once
}

func newFrameStream(s *Session, id uint32) *frameStream {
	return &frameStream{session: s, id: id, readCh: make(chan []byte, 16), done: make(chan struct{})}
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
		select {
		case b := <-s.readCh:
			if len(b) == 0 {
				continue
			}
			s.mu.Lock()
			s.readBuf = append(s.readBuf, b...)
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
		case b := <-s.readCh:
			if len(b) == 0 {
				continue
			}
			s.mu.Lock()
			s.readBuf = append(s.readBuf, b...)
			s.mu.Unlock()
		case <-s.done:
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
	if err != nil {
		return 0, err
	}
	s.session.mu.RLock()
	closed := s.session.closed
	tr := s.session.transport
	s.session.mu.RUnlock()
	if closed || tr == nil {
		return 0, ErrSessionClosed
	}
	if err := tr.Send(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: s.id, Payload: append([]byte(nil), p...)}); err != nil {
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
			err = tr.Send(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameHalfClose, StreamID: s.id})
		}
	})
	return err
}
func (s *frameStream) push(p []byte) {
	select {
	case s.readCh <- append([]byte(nil), p...):
	case <-s.done:
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
	readCh     chan []byte
	done       chan struct{}
	mu         sync.Mutex
	err        error
	finishOnce sync.Once
	closeOnce  sync.Once
}

func newFrameDatagramStream(s *Session, id uint32) *frameDatagramStream {
	return &frameDatagramStream{session: s, id: id, readCh: make(chan []byte, 16), done: make(chan struct{})}
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
	select {
	case p := <-s.readCh:
		return p, nil
	default:
	}
	select {
	case p := <-s.readCh:
		return p, nil
	case <-s.done:
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
	return tr.Send(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: s.id, Payload: append([]byte(nil), p...)})
}
func (s *frameDatagramStream) push(p []byte) {
	select {
	case s.readCh <- append([]byte(nil), p...):
	case <-s.done:
	}
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
			_ = tr.Send(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameHalfClose, StreamID: s.id})
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
