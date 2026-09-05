package protocol

import (
	"errors"
	"math"
	"sync"
)

type StreamStatus uint8

const (
	StreamIdle StreamStatus = iota
	StreamOpen
	StreamHalfClosedLocal
	StreamHalfClosedRemote
	StreamClosed
	StreamReset
)

type StreamState struct {
	mu            sync.Mutex
	id            uint32
	status        StreamStatus
	sendWindow    uint32
	receiveWindow uint32
}

func NewStreamState(id uint32, initialWindow uint32) (*StreamState, error) {
	if id == 0 {
		return nil, ErrInvalidFrame
	}
	if initialWindow == 0 {
		initialWindow = 65535
	}
	return &StreamState{id: id, status: StreamIdle, sendWindow: initialWindow, receiveWindow: initialWindow}, nil
}

func NewStream(id uint32, initialWindow uint32) (*StreamState, error) {
	return NewStreamState(id, initialWindow)
}

func (s *StreamState) ID() uint32 {
	if s == nil {
		return 0
	}
	return s.id
}
func (s *StreamState) Status() StreamStatus {
	if s == nil {
		return StreamClosed
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

func (s *StreamState) State() StreamStatus { return s.Status() }
func (s *StreamState) SendWindow() uint32  { s.mu.Lock(); defer s.mu.Unlock(); return s.sendWindow }
func (s *StreamState) ReceiveWindow() uint32 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.receiveWindow
}

func (s *StreamState) OpenLocal() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status != StreamIdle {
		return ErrInvalidTransition
	}
	s.status = StreamOpen
	return nil
}

func (s *StreamState) HalfCloseLocal() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch s.status {
	case StreamOpen:
		s.status = StreamHalfClosedLocal
	case StreamHalfClosedRemote:
		s.status = StreamClosed
	case StreamHalfClosedLocal:
		return nil
	case StreamClosed:
		return ErrStreamClosed
	case StreamReset:
		return ErrStreamReset
	default:
		return ErrInvalidTransition
	}
	return nil
}

func (s *StreamState) HalfCloseRemote() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch s.status {
	case StreamOpen:
		s.status = StreamHalfClosedRemote
	case StreamHalfClosedLocal:
		s.status = StreamClosed
	case StreamHalfClosedRemote:
		return nil
	case StreamClosed:
		return ErrStreamClosed
	case StreamReset:
		return ErrStreamReset
	default:
		return ErrInvalidTransition
	}
	return nil
}

func (s *StreamState) Reset() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status == StreamReset {
		return ErrStreamReset
	}
	if s.status == StreamClosed {
		return ErrStreamClosed
	}
	s.status = StreamReset
	return nil
}

func (s *StreamState) AddSendWindow(n uint32) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status == StreamReset {
		return ErrStreamReset
	}
	if s.status == StreamClosed {
		return ErrStreamClosed
	}
	if uint64(s.sendWindow)+uint64(n) > math.MaxUint32 {
		return errors.New("protocol: send window overflow")
	}
	s.sendWindow += n
	return nil
}

func (s *StreamState) WindowUpdate(n uint32) error { return s.AddSendWindow(n) }

func (s *StreamState) ConsumeSend(n uint32) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status == StreamReset {
		return ErrStreamReset
	}
	if s.status == StreamClosed || s.status == StreamHalfClosedLocal {
		return ErrStreamClosed
	}
	if n > s.sendWindow {
		return ErrWindowExhausted
	}
	s.sendWindow -= n
	return nil
}

func (s *StreamState) AddReceiveWindow(n uint32) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status == StreamReset {
		return ErrStreamReset
	}
	if s.status == StreamClosed {
		return ErrStreamClosed
	}
	if uint64(s.receiveWindow)+uint64(n) > math.MaxUint32 {
		return errors.New("protocol: receive window overflow")
	}
	s.receiveWindow += n
	return nil
}

func (s *StreamState) consumeReceive(n uint32) error {
	if n > s.receiveWindow {
		return ErrWindowExhausted
	}
	s.receiveWindow -= n
	return nil
}

func (s *StreamState) Handle(f Frame) error {
	if err := f.Validate(); err != nil {
		return err
	}
	if f.StreamID != s.id {
		return ErrInvalidFrame
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch f.Type {
	case FrameOpenStream:
		if s.status != StreamIdle {
			return ErrInvalidTransition
		}
		s.status = StreamOpen
		if f.Window != 0 {
			s.sendWindow = f.Window
		}
	case FrameData:
		if s.status == StreamReset {
			return ErrStreamReset
		}
		if s.status == StreamClosed || s.status == StreamHalfClosedRemote || s.status == StreamIdle {
			return ErrStreamClosed
		}
		if err := s.consumeReceive(uint32(len(f.Payload))); err != nil {
			return err
		}
	case FrameHalfClose:
		switch s.status {
		case StreamOpen:
			s.status = StreamHalfClosedRemote
		case StreamHalfClosedLocal:
			s.status = StreamClosed
		case StreamHalfClosedRemote:
			return nil
		case StreamClosed:
			return ErrStreamClosed
		case StreamReset:
			return ErrStreamReset
		default:
			return ErrInvalidTransition
		}
	case FrameReset:
		if s.status == StreamReset {
			return ErrStreamReset
		}
		if s.status == StreamClosed {
			return ErrStreamClosed
		}
		s.status = StreamReset
	case FrameWindowUpdate:
		if f.Window == 0 || uint64(s.sendWindow)+uint64(f.Window) > math.MaxUint32 {
			return ErrInvalidFrame
		}
		s.sendWindow += f.Window
	default:
		return ErrInvalidFrame
	}
	return nil
}
