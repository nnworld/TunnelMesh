package client

import (
	"context"
	"errors"
	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
	"sync"
)

var ErrSessionClosed = errors.New("client session closed")

type FrameTransport interface {
	Send(protocol.Frame) error
	Close() error
}
type StreamRequest struct {
	StreamID             uint32
	Protocol, TargetHost string
	TargetPort           int
	Metadata             []byte
}
type Session struct {
	mu        sync.RWMutex
	transport FrameTransport
	closed    bool
}

func NewSession(tr FrameTransport) *Session { return &Session{transport: tr} }
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
	return s.transport.Send(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: req.StreamID, Payload: req.Metadata})
}
func (s *Session) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	tr := s.transport
	s.mu.Unlock()
	if tr != nil {
		return tr.Close()
	}
	return nil
}
