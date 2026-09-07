package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
)

var (
	ErrEpoch                     = errors.New("session: stale epoch")
	ErrBackpressure              = errors.New("session: writer backpressure")
	ErrSessionClosed             = errors.New("session: closed")
	ErrAuthentication            = errors.New("session: authentication failed")
	ErrCapability                = errors.New("session: unsupported capability")
	ErrServerGeneration          = errors.New("session: stale server connection generation")
	ErrServerGenerationExhausted = errors.New("session: server connection generation exhausted")
)

// MetadataCallback applies server-side allowlist/policy checks. Returning
// field errors keeps metadata failures isolated from normal data streams.
type MetadataCallback func(context.Context, AgentRegistration, protocol.AgentMetadataPayload) []protocol.AgentMetadataError

// FrameTransport is the small transport contract shared by WebSocket and
// relay adapters. Implementations must serialize Send calls.
type FrameTransport interface {
	Send(protocol.Frame) error
	Close() error
}

type AgentRegistration struct {
	AgentID, NodeID string
	// TokenID is the non-secret credential identifier used by audit/metrics.
	// Raw bearer tokens must not be retained in session state.
	TokenID      string
	Epoch        int64
	Capabilities []string
}
type AgentSessionConfig struct {
	SupportedCapabilities []string
	QueueSize             int
	MetadataTTL           time.Duration
	Authenticate          func(context.Context, AgentRegistration) error
	MetadataCallback      MetadataCallback
	MetadataService       *AgentMetadataService
}

type AgentSession struct {
	AgentID, NodeID  string
	Epoch            int64
	serverGeneration uint64
	Capabilities     []string
	transport        FrameTransport
	mu               sync.RWMutex
	closed           bool
	lastHeartbeat    time.Time
	registration     AgentRegistration
	metadataMu       sync.Mutex
	metadataRevision uint64
	metadataDigest   string
	metadataCallback MetadataCallback
	metadataService  *AgentMetadataService
	metadataTTL      time.Duration
	queue            chan protocol.Frame
	slots            chan struct{}
	stop             chan struct{}
	stopOnce         sync.Once
	drain            chan chan struct{}
	closing          bool
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
	mu                   sync.RWMutex
	sessions             map[string]*AgentSession
	nextServerGeneration uint64
	retiredEpochs        map[string]map[int64]struct{}
	cfg                  AgentSessionConfig
}

func NewAgentSessionManager(cfg AgentSessionConfig) *AgentSessionManager {
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = 64
	}
	if cfg.MetadataTTL <= 0 {
		cfg.MetadataTTL = 5 * time.Minute
	}
	return &AgentSessionManager{sessions: make(map[string]*AgentSession), retiredEpochs: make(map[string]map[int64]struct{}), cfg: cfg}
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
	s := &AgentSession{AgentID: req.AgentID, NodeID: req.NodeID, Epoch: req.Epoch, registration: req, metadataCallback: m.cfg.MetadataCallback, metadataService: m.cfg.MetadataService, metadataTTL: m.cfg.MetadataTTL, Capabilities: neg, transport: tr, lastHeartbeat: time.Now().UTC(), queue: make(chan protocol.Frame, m.cfg.QueueSize), slots: make(chan struct{}, m.cfg.QueueSize), stop: make(chan struct{}), drain: make(chan chan struct{})}
	m.mu.Lock()
	old := m.sessions[req.AgentID]
	if old != nil && old.Epoch >= req.Epoch {
		m.mu.Unlock()
		_ = s.Close()
		return nil, ErrEpoch
	}
	if m.nextServerGeneration == math.MaxUint64 {
		m.mu.Unlock()
		_ = s.Close()
		return nil, ErrServerGenerationExhausted
	}
	m.nextServerGeneration++
	s.serverGeneration = m.nextServerGeneration
	m.sessions[req.AgentID] = s
	m.mu.Unlock()
	go s.writer()
	if old != nil {
		_ = old.Close()
	}
	return s, nil
}

// HandleMetadata fences an authenticated Agent's metadata by identity, epoch,
// and monotonically increasing revision. Equal revisions are idempotent.
func (s *AgentSession) HandleMetadata(ctx context.Context, payload protocol.AgentMetadataPayload) (protocol.AgentMetadataAckPayload, error) {
	if s == nil {
		return protocol.AgentMetadataAckPayload{}, ErrSessionClosed
	}
	ack := protocol.AgentMetadataAckPayload{AgentID: s.AgentID, Epoch: s.Epoch, Revision: payload.Revision}
	digest := metadataDigest(payload)
	s.metadataMu.Lock()
	if s.isTerminal() {
		s.metadataMu.Unlock()
		return ack, ErrSessionClosed
	}
	if payload.Revision == 0 {
		s.metadataMu.Unlock()
		ack.Errors = append(ack.Errors, protocol.AgentMetadataError{Name: "revision", Code: "invalid_revision", Message: "metadata revision must be positive"})
		return ack, nil
	}
	if payload.AgentID != s.AgentID {
		s.metadataMu.Unlock()
		ack.Errors = append(ack.Errors, protocol.AgentMetadataError{Name: "agent_id", Code: "identity_mismatch", Message: "agent identity does not match authenticated session"})
		return ack, nil
	}
	if payload.NodeID != "" && payload.NodeID != s.NodeID {
		s.metadataMu.Unlock()
		ack.Errors = append(ack.Errors, protocol.AgentMetadataError{Name: "node_id", Code: "identity_mismatch", Message: "node identity does not match authenticated session"})
		return ack, nil
	}
	if payload.Epoch != s.Epoch {
		s.metadataMu.Unlock()
		ack.Errors = append(ack.Errors, protocol.AgentMetadataError{Name: "epoch", Code: "stale_epoch", Message: "metadata epoch does not match authenticated session"})
		return ack, nil
	}
	if payload.Revision < s.metadataRevision {
		s.metadataMu.Unlock()
		ack.Errors = append(ack.Errors, protocol.AgentMetadataError{Name: "revision", Code: "stale_revision", Message: "metadata revision is older than the accepted revision"})
		return ack, nil
	}
	if payload.Revision == s.metadataRevision {
		same := digest == s.metadataDigest
		s.metadataMu.Unlock()
		if !same {
			ack.Errors = append(ack.Errors, protocol.AgentMetadataError{Name: "revision", Code: "revision_conflict", Message: "metadata revision already contains a different snapshot"})
			return ack, nil
		}
		ack.Accepted = true
		ack.Idempotent = true
		return ack, nil
	}
	s.metadataMu.Unlock()
	var callbackErrors []protocol.AgentMetadataError
	if callback := s.managerMetadataCallback(); callback != nil {
		callbackErrors = callback(ctx, s.registration, payload)
	}
	if len(callbackErrors) == 0 {
		callbackErrors = s.persistMetadata(ctx, payload)
	}
	s.metadataMu.Lock()
	defer s.metadataMu.Unlock()
	if s.isTerminal() {
		return ack, ErrSessionClosed
	}
	if payload.Revision <= s.metadataRevision {
		if payload.Revision == s.metadataRevision && digest == s.metadataDigest {
			ack.Accepted = true
			ack.Idempotent = true
			return ack, nil
		}
		ack.Errors = append(ack.Errors, protocol.AgentMetadataError{Name: "revision", Code: "stale_revision", Message: "metadata revision was superseded"})
		return ack, nil
	}
	if len(callbackErrors) > 0 {
		ack.Errors = append(ack.Errors, callbackErrors...)
		return ack, nil
	}
	s.metadataRevision = payload.Revision
	s.metadataDigest = digest
	ack.Accepted = true
	return ack, nil
}

func (s *AgentSession) persistMetadata(ctx context.Context, payload protocol.AgentMetadataPayload) []protocol.AgentMetadataError {
	if s == nil || s.metadataService == nil {
		return nil
	}
	if payload.Revision > math.MaxInt64 {
		return []protocol.AgentMetadataError{{Name: "revision", Code: "invalid_revision", Message: "metadata revision exceeds storage range"}}
	}
	nodeID := payload.NodeID
	if nodeID == "" {
		nodeID = s.NodeID
	}
	items := make([]MetadataItem, len(payload.Items))
	for i, item := range payload.Items {
		items[i] = MetadataItem{Name: item.Name, Source: item.Source, Value: item.Value}
	}
	_, err := s.metadataService.Upsert(ctx, AgentMetadataInput{AgentID: s.AgentID, NodeID: nodeID, Epoch: s.Epoch, Revision: int64(payload.Revision), ReportedAt: payload.ReportedAt, ExpiresAt: timePtr(time.Now().UTC().Add(s.metadataTTL)), Items: items})
	if err == nil {
		return nil
	}
	return []protocol.AgentMetadataError{{Name: "metadata", Code: "invalid_metadata", Message: safeMetadataError(err)}}
}

func timePtr(v time.Time) *time.Time { return &v }

func safeMetadataError(err error) string {
	if err == nil {
		return ""
	}
	return fmt.Sprintf("metadata rejected: %v", err)
}

func metadataDigest(payload protocol.AgentMetadataPayload) string {
	payload.ReportedAt = time.Time{}
	encoded, _ := json.Marshal(payload)
	return string(encoded)
}

func (s *AgentSession) isTerminal() bool {
	s.mu.RLock()
	terminal := s.closed || s.closing
	s.mu.RUnlock()
	return terminal
}

func (s *AgentSession) managerMetadataCallback() MetadataCallback {
	// The callback is copied onto the session at registration time so handling
	// remains independent from manager map lifetime.
	return s.metadataCallback
}

// HandleMetadataFrame decodes a bounded metadata control payload and returns
// an ACK frame. Decode/policy errors become structured ACK errors where the
// authenticated session is available; no stream is closed.
func (s *AgentSession) HandleMetadataFrame(ctx context.Context, frame protocol.Frame) (protocol.Frame, error) {
	if frame.Type != protocol.FrameAgentHello && frame.Type != protocol.FrameAgentMetadataUpdate {
		return protocol.Frame{}, protocol.ErrInvalidFrame
	}
	payload, err := protocol.DecodeAgentMetadataPayload(frame.Payload)
	if err != nil {
		return protocol.Frame{}, err
	}
	ack, err := s.HandleMetadata(ctx, payload)
	if err != nil {
		return protocol.Frame{}, err
	}
	encoded, err := protocol.EncodeAgentMetadataAckPayload(ack)
	if err != nil {
		return protocol.Frame{}, err
	}
	return protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameAgentMetadataAck, Payload: encoded}, nil
}

func (m *AgentSessionManager) HandleMetadata(ctx context.Context, id string, frame protocol.Frame) (protocol.Frame, error) {
	s, ok := m.Get(id)
	if !ok {
		return protocol.Frame{}, ErrSessionClosed
	}
	return s.HandleMetadataFrame(ctx, frame)
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
	if s.closed || s.closing {
		return ErrSessionClosed
	}
	if s.Epoch != epoch {
		return ErrEpoch
	}
	s.lastHeartbeat = time.Now().UTC()
	return nil
}

// RefreshMetadataLease extends metadata freshness for the current session.
// Missing metadata is harmless because an Agent may heartbeat before its first
// accepted metadata snapshot.
func (m *AgentSessionManager) RefreshMetadataLease(ctx context.Context, id string, epoch int64) error {
	s, ok := m.Get(id)
	if !ok {
		return ErrSessionClosed
	}
	if err := m.Heartbeat(id, epoch); err != nil {
		return err
	}
	if s.metadataService == nil {
		return nil
	}
	if err := s.metadataService.Touch(ctx, id, epoch, s.metadataTTL); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	return nil
}
func (m *AgentSessionManager) Send(id string, f protocol.Frame) error {
	s, ok := m.Get(id)
	if !ok {
		return ErrSessionClosed
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed || s.closing {
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

// SendGeneration queues a frame only when the exact process-local Agent
// connection incarnation is still current. Protocol Epoch remains a separate
// persisted business generation and is not sufficient to fence reconnects.
func (m *AgentSessionManager) sendServerGeneration(id string, serverGeneration uint64, f protocol.Frame) error {
	if m == nil {
		return ErrSessionClosed
	}
	m.mu.RLock()
	s := m.sessions[id]
	if s == nil {
		m.mu.RUnlock()
		return ErrSessionClosed
	}
	if s.serverGeneration != serverGeneration {
		m.mu.RUnlock()
		return ErrServerGeneration
	}
	s.mu.RLock()
	if s.closed || s.closing {
		s.mu.RUnlock()
		m.mu.RUnlock()
		return ErrSessionClosed
	}
	select {
	case s.slots <- struct{}{}:
	default:
		s.mu.RUnlock()
		m.mu.RUnlock()
		return ErrBackpressure
	}
	select {
	case s.queue <- f:
		s.mu.RUnlock()
		m.mu.RUnlock()
		return nil
	default:
		<-s.slots
		s.mu.RUnlock()
		m.mu.RUnlock()
		return ErrBackpressure
	}
}

// SendGeneration preserves the legacy Epoch-based API. New internal callers
// must use sendServerGeneration so reconnect incarnations remain exact-fenced.
func (m *AgentSessionManager) SendGeneration(id string, epoch int64, f protocol.Frame) error {
	serverGeneration, err := m.resolveServerGeneration(id, epoch)
	if err != nil {
		return err
	}
	return m.sendServerGeneration(id, serverGeneration, f)
}

func (m *AgentSessionManager) resolveServerGeneration(id string, epoch int64) (uint64, error) {
	if m == nil {
		return 0, ErrSessionClosed
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	s := m.sessions[id]
	if s == nil {
		return 0, ErrSessionClosed
	}
	if s.Epoch != epoch {
		return 0, ErrEpoch
	}
	if epochs := m.retiredEpochs[id]; epochs != nil {
		if _, ambiguous := epochs[epoch]; ambiguous {
			return 0, ErrServerGeneration
		}
	}
	return s.serverGeneration, nil
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
	s.closing = true
	s.mu.Unlock()
	ack := make(chan struct{})
	select {
	case s.drain <- ack:
		<-ack
	case <-s.stop:
		return ErrSessionClosed
	}
	s.mu.Lock()
	s.closed = true
	s.stopOnce.Do(func() { close(s.stop) })
	s.mu.Unlock()
	err := s.transport.Send(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameGoAway})
	_ = s.transport.Close()
	return err
}
func (s *AgentSession) writer() {
	for {
		select {
		case f := <-s.queue:
			if err := s.transport.Send(f); err != nil {
				s.fail(err)
				<-s.slots
				return
			}
			<-s.slots
		case ack := <-s.drain:
			for {
				select {
				case f := <-s.queue:
					if err := s.transport.Send(f); err != nil {
						s.fail(err)
						<-s.slots
						close(ack)
						return
					}
					<-s.slots
				default:
					close(ack)
					return
				}
			}
		case <-s.stop:
			return
		}
	}
}
func (s *AgentSession) fail(_ error) {
	s.metadataMu.Lock()
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		s.closing = true
	}
	s.mu.Unlock()
	s.metadataMu.Unlock()
	s.stopOnce.Do(func() { close(s.stop) })
	_ = s.transport.Close()
}
func (s *AgentSession) Close() error {
	s.metadataMu.Lock()
	s.mu.Lock()
	if s.closed || s.closing {
		s.mu.Unlock()
		s.metadataMu.Unlock()
		return nil
	}
	s.closed = true
	s.stopOnce.Do(func() { close(s.stop) })
	s.mu.Unlock()
	s.metadataMu.Unlock()
	return s.transport.Close()
}
func (m *AgentSessionManager) Remove(id string) {
	m.mu.Lock()
	s := m.sessions[id]
	if s != nil {
		// Keep the manager lock while fencing metadata. Otherwise a same-epoch
		// reconnect can register between map removal and MarkStale, allowing
		// the old session's cleanup to stale the replacement snapshot.
		_ = s.Close()
		m.markMetadataStale(context.Background(), s)
		delete(m.sessions, id)
		if m.retiredEpochs[id] == nil {
			m.retiredEpochs[id] = make(map[int64]struct{})
		}
		m.retiredEpochs[id][s.Epoch] = struct{}{}
	}
	m.mu.Unlock()
}

// RemoveSession removes only the expected current session. This prevents an
// old WebSocket's EOF cleanup from deleting a newer reconnect for the same ID.
func (m *AgentSessionManager) RemoveSession(id string, expected *AgentSession) {
	m.mu.Lock()
	current := m.sessions[id]
	if current == expected {
		// Keep the manager lock through stale marking so a replacement cannot
		// become visible until cleanup of the expected session is fenced.
		if expected != nil {
			_ = expected.Close()
			m.markMetadataStale(context.Background(), expected)
		}
		delete(m.sessions, id)
		if expected != nil {
			if m.retiredEpochs[id] == nil {
				m.retiredEpochs[id] = make(map[int64]struct{})
			}
			m.retiredEpochs[id][expected.Epoch] = struct{}{}
		}
	}
	m.mu.Unlock()
}

func (m *AgentSessionManager) markMetadataStale(ctx context.Context, s *AgentSession) {
	if m == nil || s == nil || m.cfg.MetadataService == nil {
		return
	}
	_ = m.cfg.MetadataService.MarkStale(ctx, s.AgentID, s.Epoch)
}

type StreamOpenRequest struct {
	StreamID             uint32
	Protocol, TargetHost string
	TargetPort           int
	Metadata             []byte
}
type StreamOpenPayload = protocol.StreamOpenPayload
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
	payload, err := protocol.EncodeStreamOpenPayload(protocol.StreamOpenPayload{Protocol: req.Protocol, TargetHost: req.TargetHost, TargetPort: req.TargetPort, Metadata: req.Metadata})
	if err != nil {
		return err
	}
	return tr.Send(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenStream, StreamID: req.StreamID, Payload: payload})
}
