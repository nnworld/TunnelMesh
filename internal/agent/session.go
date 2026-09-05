package agent

import (
	"context"
	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
	"math/rand"
	"sync/atomic"
	"time"
)

type FrameTransport interface {
	Send(protocol.Frame) error
	Receive() (protocol.Frame, error)
	Close() error
}
type Session struct {
	transport               FrameTransport
	closed                  atomic.Bool
	BaseBackoff, MaxBackoff time.Duration
	Rand                    *rand.Rand
}

func NewSession(tr FrameTransport) *Session {
	return &Session{transport: tr, BaseBackoff: time.Second, MaxBackoff: 30 * time.Second, Rand: rand.New(rand.NewSource(time.Now().UnixNano()))}
}
func (s *Session) Run(ctx context.Context, onFrame func(protocol.Frame) error) error {
	if s == nil || s.transport == nil {
		return context.Canceled
	}
	defer s.Close()
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = s.transport.Close()
		case <-done:
		}
	}()
	for {
		select {
		case <-ctx.Done():
			_ = s.transport.Close()
			return ctx.Err()
		default:
		}
		f, err := s.transport.Receive()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		if f.Type == protocol.FrameGoAway {
			return nil
		}
		if onFrame != nil {
			if err := onFrame(f); err != nil {
				return err
			}
		}
	}
}
func (s *Session) Close() error {
	if s.closed.Swap(true) {
		return nil
	}
	return s.transport.Close()
}
func (s *Session) ReconnectDelay(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	d := s.BaseBackoff
	for i := 0; i < attempt && d < s.MaxBackoff; i++ {
		d *= 2
	}
	if d > s.MaxBackoff {
		d = s.MaxBackoff
	}
	if s.Rand == nil {
		return d
	}
	jitter := 0.8 + s.Rand.Float64()*0.4
	return time.Duration(float64(d) * jitter)
}
