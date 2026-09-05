package server

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
)

var (
	ErrEpoch          = errors.New("session: stale epoch")
	ErrBackpressure   = errors.New("session: writer backpressure")
	ErrSessionClosed  = errors.New("session: closed")
	ErrAuthentication = errors.New("session: authentication failed")
	ErrCapability     = errors.New("session: unsupported capability")
)

// FrameTransport is the small transport contract shared by WebSocket and
// relay adapters. Implementations must serialize Send calls.
type FrameTransport interface {
	Send(protocol.Frame) error
	Close() error
}

type AgentRegistration struct {
	AgentID, NodeID, Token string
	Epoch                  int64
	Capabilities           []string
}
type AgentSessionConfig struct {
	SupportedCapabilities []string
	QueueSize             int
	Authenticate          func(context.Context, AgentRegistration) error
}

type AgentSession struct {
	AgentID, NodeID string
	Epoch           int64
	Capabilities    []string
	transport       FrameTransport
	mu              sync.RWMutex
	closed          bool
	lastHeartbeat   time.Time
	queue           chan protocol.Frame
	slots           chan struct{}
	stop            chan struct{}
}

func (s *AgentSession) Supports(capability string) bool {
	for _, c := range s.Capabilities {
		if c == capability {
			return true
		}
	}
	return false
}
func (s *AgentSession) LastHeartbeat() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastHeartbeat
}

type AgentSessionManager struct {
	mu       sync.RWMutex
	sessions map[string]*AgentSession
	cfg      AgentSessionConfig
}

func NewAgentSessionManager(cfg AgentSessionConfig) *AgentSessionManager {
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = 64
	}
	return &AgentSessionManager{sessions: make(map[string]*AgentSession), cfg: cfg}
}
func (m *AgentSessionManager) Register(ctx context.Context, req AgentRegistration, tr FrameTransport) (*AgentSession, error) {
	if m == nil || tr == nil || strings.TrimSpace(req.AgentID) == "" || strings.TrimSpace(req.NodeID) == "" || req.Epoch <= 0 {
		return nil, ErrAuthentication
	}
	if m.cfg.Authenticate != nil {
		if err := m.cfg.Authenticate(ctx, req); err != nil {
			return nil, err
		}
	}
	neg := req.Capabilities
	if len(m.cfg.SupportedCapabilities) > 0 {
		neg = nil
		for _, want := range req.Capabilities {
			for _, have := range m.cfg.SupportedCapabilities {
				if want == have {
					neg = append(neg, want)
					break
				}
			}
		}
	}
	s := &AgentSession{AgentID: req.AgentID, NodeID: req.NodeID, Epoch: req.Epoch, Capabilities: neg, transport: tr, lastHeartbeat: time.Now().UTC(), queue: make(chan protocol.Frame, m.cfg.QueueSize), slots: make(chan struct{}, m.cfg.QueueSize), stop: make(chan struct{})}
	go s.writer()
	m.mu.Lock()
	old := m.sessions[req.AgentID]
	m.sessions[req.AgentID] = s
	m.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
	return s, nil
}
func (m *AgentSessionManager) Get(id string) (*AgentSession, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.sessions[id]
	return s, ok
}
func (m *AgentSessionManager) Heartbeat(id string, epoch int64) error {
	s, ok := m.Get(id)
	if !ok {
		return ErrSessionClosed
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrSessionClosed
	}
	if s.Epoch != epoch {
		return ErrEpoch
	}
	s.lastHeartbeat = time.Now().UTC()
	return nil
}
func (m *AgentSessionManager) Send(id string, f protocol.Frame) error {
	s, ok := m.Get(id)
	if !ok {
		return ErrSessionClosed
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return ErrSessionClosed
	}
	select {
	case s.slots <- struct{}{}:
	default:
		return ErrBackpressure
	}
	select {
	case s.queue <- f:
		return nil
	default:
		<-s.slots
		return ErrBackpressure
	}
}
func (m *AgentSessionManager) GoAway(id string) error {
	s, ok := m.Get(id)
	if !ok {
		return ErrSessionClosed
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrSessionClosed
	}
	// GOAWAY is written synchronously so the peer receives the terminal frame
	// before the transport is closed and no new frames can be queued.
	err := s.transport.Send(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameGoAway})
	s.closed = true
	close(s.stop)
	s.mu.Unlock()
	_ = s.transport.Close()
	return err
}
func (s *AgentSession) writer() {
	for {
		select {
		case f := <-s.queue:
			_ = s.transport.Send(f)
			<-s.slots
		case <-s.stop:
			return
		}
	}
}
func (s *AgentSession) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	close(s.stop)
	s.mu.Unlock()
	return s.transport.Close()
}
func (m *AgentSessionManager) Remove(id string) {
	m.mu.Lock()
	s := m.sessions[id]
	delete(m.sessions, id)
	m.mu.Unlock()
	if s != nil {
		_ = s.Close()
	}
}

type StreamOpenRequest struct {
	StreamID             uint32
	Protocol, TargetHost string
	TargetPort           int
	Metadata             []byte
}
type ClientSessionManager struct {
	mu       sync.RWMutex
	sessions map[string]FrameTransport
}

func NewClientSessionManager() *ClientSessionManager {
	return &ClientSessionManager{sessions: make(map[string]FrameTransport)}
}
func (m *ClientSessionManager) Register(id string, tr FrameTransport) {
	if tr == nil {
		return
	}
	m.mu.Lock()
	old := m.sessions[id]
	m.sessions[id] = tr
	m.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
}
func (m *ClientSessionManager) Remove(id string) {
	m.mu.Lock()
	tr := m.sessions[id]
	delete(m.sessions, id)
	m.mu.Unlock()
	if tr != nil {
		_ = tr.Close()
	}
}
func (m *ClientSessionManager) OpenStream(ctx context.Context, id string, req StreamOpenRequest) error {
	m.mu.RLock()
	tr := m.sessions[id]
	m.mu.RUnlock()
	if tr == nil {
		return ErrSessionClosed
	}
	if req.StreamID == 0 || req.TargetPort < 1 || req.TargetPort > 65535 {
		return ErrCapability
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	return tr.Send(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: req.StreamID, Payload: req.Metadata})
}
