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
	ErrAmbiguousAgentID          = errors.New("session: ambiguous agent id")
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
	InstanceID   string
	ConnectionID string
	// ConnectionEpoch fences one logical connection; legacy registrations
	// without it reuse the protocol epoch.
	ConnectionEpoch int64
	Capabilities    []string
}
type AgentSessionConfig struct {
	SupportedCapabilities []string
	ServerNodeID          string
	QueueSize             int
	MetadataTTL           time.Duration
	HeartbeatCallback     func(context.Context, *AgentSession) error
	Authenticate          func(context.Context, AgentRegistration) error
	MetadataCallback      MetadataCallback
	MetadataService       *AgentMetadataService
}

type AgentSession struct {
	AgentID, NodeID    string
	Epoch              int64
	InstanceID         string
	InstanceIDExplicit bool
	ConnectionID       string
	ConnectionEpoch    int64
	serverGeneration   uint64
	Capabilities       []string
	transport          FrameTransport
	mu                 sync.RWMutex
	closed             bool
	lastHeartbeat      time.Time
	registration       AgentRegistration
	metadataMu         sync.Mutex
	metadataRevision   uint64
	metadataDigest     string
	metadataCallback   MetadataCallback
	metadataService    *AgentMetadataService
	metadataTTL        time.Duration
	queue              chan protocol.Frame
	slots              chan struct{}
	stop               chan struct{}
	stopOnce           sync.Once
	drain              chan chan struct{}
	closing            bool
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

// Healthy reports whether the local WebSocket can still accept frames. The
// manager may retain a closed session briefly until its read loop performs
// cleanup, so connection selection must consult this state.
func (s *AgentSession) Healthy() bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return !s.closed && !s.closing
}

type AgentSessionManager struct {
	mu                   sync.RWMutex
	sessions             map[string]map[string]*AgentSession
	connectionOrder      map[string][]string
	nextServerGeneration uint64
	retiredEpochs        map[string]map[string]map[int64]struct{}
	retiredGenerations   map[uint64]struct{}
	cfg                  AgentSessionConfig
}

func NewAgentSessionManager(cfg AgentSessionConfig) *AgentSessionManager {
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = 64
	}
	if cfg.MetadataTTL <= 0 {
		cfg.MetadataTTL = 5 * time.Minute
	}
	return &AgentSessionManager{
		sessions:           make(map[string]map[string]*AgentSession),
		connectionOrder:    make(map[string][]string),
		retiredEpochs:      make(map[string]map[string]map[int64]struct{}),
		retiredGenerations: make(map[uint64]struct{}),
		cfg:                cfg,
	}
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
	connectionID := strings.TrimSpace(req.ConnectionID)
	if connectionID == "" {
		connectionID = "legacy"
	}
	instanceID := strings.TrimSpace(req.InstanceID)
	instanceIDExplicit := instanceID != ""
	if instanceID == "" {
		instanceID = req.NodeID
	}
	req.InstanceID = instanceID
	connectionEpoch := req.ConnectionEpoch
	if connectionEpoch == 0 {
		connectionEpoch = req.Epoch
	}
	s := &AgentSession{AgentID: req.AgentID, NodeID: req.NodeID, Epoch: req.Epoch, InstanceID: instanceID, InstanceIDExplicit: instanceIDExplicit, ConnectionID: connectionID, ConnectionEpoch: connectionEpoch, registration: req, metadataCallback: m.cfg.MetadataCallback, metadataService: m.cfg.MetadataService, metadataTTL: m.cfg.MetadataTTL, Capabilities: neg, transport: tr, lastHeartbeat: time.Now().UTC(), queue: make(chan protocol.Frame, m.cfg.QueueSize), slots: make(chan struct{}, m.cfg.QueueSize), stop: make(chan struct{}), drain: make(chan chan struct{})}
	m.mu.Lock()
	agentSessions := m.sessions[req.AgentID]
	if agentSessions == nil {
		agentSessions = make(map[string]*AgentSession)
		m.sessions[req.AgentID] = agentSessions
	}
	old := agentSessions[connectionID]
	if old != nil && old.ConnectionEpoch >= connectionEpoch {
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
	agentSessions[connectionID] = s
	order := m.connectionOrder[req.AgentID]
	found := false
	for _, id := range order {
		if id == connectionID {
			found = true
			break
		}
	}
	if !found {
		m.connectionOrder[req.AgentID] = append(order, connectionID)
	}
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
	ack := protocol.AgentMetadataAckPayload{
		AgentID: s.AgentID, Epoch: s.Epoch, Revision: payload.Revision,
		ConnectionPoolSupported: true, MaxConnectionsPerAgent: 64,
	}
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
	if payload.InstanceID != "" && payload.InstanceID != s.InstanceID {
		s.metadataMu.Unlock()
		ack.Errors = append(ack.Errors, protocol.AgentMetadataError{Name: "instance_id", Code: "identity_mismatch", Message: "agent instance identity does not match authenticated session"})
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
	_, err := s.metadataService.Upsert(ctx, AgentMetadataInput{AgentID: s.AgentID, InstanceID: s.InstanceID, NodeID: nodeID, Epoch: s.Epoch, Revision: int64(payload.Revision), ReportedAt: payload.ReportedAt, ExpiresAt: timePtr(time.Now().UTC().Add(s.metadataTTL)), Items: items})
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
	sessions := m.List(id)
	if len(sessions) == 0 {
		return nil, false
	}
	return sessions[0], true
}

// List returns every live connection for a logical Agent in registration
// order. The order makes behavior deterministic while still allowing callers
// to apply their own health/locality-aware selection policy.
func (m *AgentSessionManager) List(id string) []*AgentSession {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	connections := m.sessions[id]
	order := m.connectionOrder[id]
	sessions := make([]*AgentSession, 0, len(order))
	for _, connectionID := range order {
		if s := connections[connectionID]; s != nil {
			sessions = append(sessions, s)
		}
	}
	return sessions
}

func (m *AgentSessionManager) HasConnection(agentID, connectionID string) bool {
	if m == nil || agentID == "" || connectionID == "" {
		return false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, exists := m.sessions[agentID][connectionID]
	return exists
}

func (m *AgentSessionManager) ServerNodeID() string {
	if m == nil {
		return ""
	}
	return m.cfg.ServerNodeID
}

// SetHeartbeatCallback lets the runtime attach a cluster lease refresher
// after both the manager and lease controller have been constructed.
func (m *AgentSessionManager) SetHeartbeatCallback(callback func(context.Context, *AgentSession) error) {
	if m == nil {
		return
	}
	m.cfg.HeartbeatCallback = callback
}

func (m *AgentSessionManager) GetConnection(agentID, connectionID string) (*AgentSession, bool) {
	if m == nil || connectionID == "" {
		return nil, false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.sessions[agentID][connectionID]
	return s, ok
}

// GetCaseInsensitive resolves a dynamic-domain Agent ID without allowing an
// ambiguous case-insensitive match to select an arbitrary Agent.
func (m *AgentSessionManager) GetCaseInsensitive(id string) (*AgentSession, error) {
	if m == nil || strings.TrimSpace(id) == "" {
		return nil, ErrSessionClosed
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	var match *AgentSession
	count := 0
	for key, connections := range m.sessions {
		if strings.EqualFold(key, id) {
			count++
			if count > 1 {
				return nil, ErrAmbiguousAgentID
			}
			for _, connectionID := range m.connectionOrder[key] {
				if s := connections[connectionID]; s != nil {
					match = s
					break
				}
			}
		}
	}
	if count == 1 {
		return match, nil
	}
	return nil, ErrSessionClosed
}
func (m *AgentSessionManager) Heartbeat(id string, epoch int64) error {
	s, ok := m.getEpoch(id, epoch)
	if !ok {
		if len(m.List(id)) > 0 {
			return ErrEpoch
		}
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
	s, ok := m.getEpoch(id, epoch)
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

// RefreshSessionMetadataLease extends only the reporting instance's lease. A
// logical Agent can own multiple instances, so the legacy agent-only refresh
// cannot tell which metadata row a heartbeat belongs to.
func (m *AgentSessionManager) RefreshSessionMetadataLease(ctx context.Context, s *AgentSession) error {
	if m == nil || s == nil {
		return ErrSessionClosed
	}
	if err := m.Heartbeat(s.AgentID, s.Epoch); err != nil {
		return err
	}
	if s.metadataService == nil {
		return nil
	}
	touchErr := s.metadataService.TouchInstance(ctx, s.AgentID, s.InstanceID, s.Epoch, s.metadataTTL)
	if !s.InstanceIDExplicit {
		touchErr = s.metadataService.Touch(ctx, s.AgentID, s.Epoch, s.metadataTTL)
	}
	if err := touchErr; err != nil {
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

func (m *AgentSessionManager) getEpoch(id string, epoch int64) (*AgentSession, bool) {
	sessions := m.List(id)
	var match *AgentSession
	matches := 0
	for _, s := range sessions {
		if s.Epoch == epoch {
			match = s
			matches++
		}
	}
	return match, matches == 1
}

func (m *AgentSessionManager) getServerGeneration(serverGeneration uint64) *AgentSession {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, connections := range m.sessions {
		for _, s := range connections {
			if s.serverGeneration == serverGeneration {
				return s
			}
		}
	}
	return nil
}

// SendGeneration queues a frame only when the exact process-local Agent
// connection incarnation is still current. Protocol Epoch remains a separate
// persisted business generation and is not sufficient to fence reconnects.
func (m *AgentSessionManager) sendServerGeneration(id string, serverGeneration uint64, f protocol.Frame) error {
	if m == nil {
		return ErrSessionClosed
	}
	m.mu.RLock()
	var s *AgentSession
	for _, connection := range m.sessions[id] {
		if connection.serverGeneration == serverGeneration {
			s = connection
			break
		}
	}
	if s == nil {
		if _, retired := m.retiredGenerations[serverGeneration]; retired {
			m.mu.RUnlock()
			return ErrServerGeneration
		}
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
	sessions := m.List(id)
	var s *AgentSession
	for _, candidate := range sessions {
		if candidate.Epoch == epoch {
			s = candidate
			break
		}
	}
	if s == nil {
		return 0, ErrSessionClosed
	}
	m.mu.RLock()
	if connections := m.retiredEpochs[id]; connections != nil {
		if epochs := connections[s.ConnectionID]; epochs != nil {
			if _, ambiguous := epochs[s.ConnectionEpoch]; ambiguous {
				m.mu.RUnlock()
				return 0, ErrServerGeneration
			}
		}
	}
	generation := s.serverGeneration
	m.mu.RUnlock()
	return generation, nil
}
func (m *AgentSessionManager) GoAway(id string) error {
	s, ok := m.Get(id)
	if !ok {
		return ErrSessionClosed
	}
	return m.goAwaySession(s)
}

// CloseConnection closes one exact process-local Agent connection. The
// registry lease may have a different fencing epoch, so callers must pass the
// session's protocol connection epoch rather than a lease epoch.
func (m *AgentSessionManager) CloseConnection(agentID, connectionID string, connectionEpoch int64) error {
	if m == nil || connectionID == "" {
		return ErrSessionClosed
	}
	session, ok := m.GetConnection(agentID, connectionID)
	if !ok {
		return ErrSessionClosed
	}
	if session.ConnectionEpoch != connectionEpoch {
		return ErrEpoch
	}
	return m.goAwaySession(session)
}

func (m *AgentSessionManager) goAwaySession(s *AgentSession) error {
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
	for _, s := range m.sessions[id] {
		// Keep the manager lock while fencing metadata. Otherwise a same-epoch
		// reconnect can register between map removal and MarkStale, allowing
		// the old session's cleanup to stale the replacement snapshot.
		_ = s.Close()
		m.markMetadataStale(context.Background(), s)
		if m.retiredEpochs[id] == nil {
			m.retiredEpochs[id] = make(map[string]map[int64]struct{})
		}
		epochs := m.retiredEpochs[id][s.ConnectionID]
		if epochs == nil {
			epochs = make(map[int64]struct{})
			m.retiredEpochs[id][s.ConnectionID] = epochs
		}
		epochs[s.ConnectionEpoch] = struct{}{}
		m.retiredGenerations[s.serverGeneration] = struct{}{}
	}
	delete(m.sessions, id)
	delete(m.connectionOrder, id)
	m.mu.Unlock()
}

// RemoveSession removes only the expected current session. This prevents an
// old WebSocket's EOF cleanup from deleting a newer reconnect for the same ID.
func (m *AgentSessionManager) RemoveSession(id string, expected *AgentSession) {
	m.mu.Lock()
	current := m.sessions[id][expected.ConnectionID]
	if current == expected {
		instanceHasOtherConnection := false
		for _, s := range m.sessions[id] {
			if s != expected && s.InstanceID == expected.InstanceID {
				instanceHasOtherConnection = true
				break
			}
		}
		// Keep the manager lock through stale marking so a replacement cannot
		// become visible until cleanup of the expected session is fenced.
		if expected != nil {
			_ = expected.Close()
			if !instanceHasOtherConnection {
				m.markMetadataStale(context.Background(), expected)
			}
		}
		delete(m.sessions[id], expected.ConnectionID)
		if len(m.sessions[id]) == 0 {
			delete(m.sessions, id)
			delete(m.connectionOrder, id)
		} else {
			order := m.connectionOrder[id]
			kept := order[:0]
			for _, connectionID := range order {
				if connectionID != expected.ConnectionID {
					kept = append(kept, connectionID)
				}
			}
			m.connectionOrder[id] = kept
		}
		if expected != nil {
			if m.retiredEpochs[id] == nil {
				m.retiredEpochs[id] = make(map[string]map[int64]struct{})
			}
			epochs := m.retiredEpochs[id][expected.ConnectionID]
			if epochs == nil {
				epochs = make(map[int64]struct{})
				m.retiredEpochs[id][expected.ConnectionID] = epochs
			}
			epochs[expected.ConnectionEpoch] = struct{}{}
			m.retiredGenerations[expected.serverGeneration] = struct{}{}
		}
	}
	m.mu.Unlock()
}

func (m *AgentSessionManager) markMetadataStale(ctx context.Context, s *AgentSession) {
	if m == nil || s == nil || m.cfg.MetadataService == nil {
		return
	}
	if s.InstanceIDExplicit {
		_ = m.cfg.MetadataService.MarkInstanceStale(ctx, s.AgentID, s.InstanceID, s.Epoch)
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
