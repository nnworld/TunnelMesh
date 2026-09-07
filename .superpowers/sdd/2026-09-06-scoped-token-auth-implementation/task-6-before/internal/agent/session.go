package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math/rand"
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
)

var ErrStreamNotFound = errors.New("agent: stream not found")
var ErrDuplicateStream = errors.New("agent: duplicate stream")

type StreamOpenPayload struct {
	AgentID    string `json:"agent_id,omitempty"`
	Protocol   string `json:"protocol"`
	TargetHost string `json:"target_host"`
	TargetPort int    `json:"target_port"`
	Metadata   []byte `json:"metadata,omitempty"`
}
type StreamDialFunc func(context.Context, string, string, int) (io.ReadWriteCloser, error)
type streamEntry struct {
	conn       io.ReadWriteCloser
	generation uint64
}
type StreamDispatcher struct {
	ctx        context.Context
	cancel     context.CancelFunc
	dial       StreamDialFunc
	streams    map[uint32]*streamEntry
	generation uint64
	mu         sync.Mutex
	onFrame    func(protocol.Frame)
}

func NewStreamDispatcher(d Dialer, override StreamDialFunc) *StreamDispatcher {
	if override == nil {
		override = func(ctx context.Context, proto, host string, port int) (io.ReadWriteCloser, error) {
			switch proto {
			case "tcp":
				return d.DialTCP(ctx, host, port)
			case "udp":
				return d.DialUDP(ctx, host, port)
			case "http":
				if d.HTTPStream != nil {
					return d.HTTPStream(ctx, host, port)
				}
				return d.DialHTTPStream(ctx, host, port)
			default:
				return nil, errors.New("agent: unsupported stream protocol")
			}
		}
	}
	return NewStreamDispatcherWithCallback(d, override, nil)
}
func NewStreamDispatcherWithCallback(d Dialer, override StreamDialFunc, cb func(protocol.Frame)) *StreamDispatcher {
	if override == nil {
		override = func(ctx context.Context, proto, host string, port int) (io.ReadWriteCloser, error) {
			switch proto {
			case "tcp":
				return d.DialTCP(ctx, host, port)
			case "udp":
				return d.DialUDP(ctx, host, port)
			case "http":
				if d.HTTPStream != nil {
					return d.HTTPStream(ctx, host, port)
				}
				return d.DialHTTPStream(ctx, host, port)
			default:
				return nil, errors.New("agent: unsupported stream protocol")
			}
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &StreamDispatcher{ctx: ctx, cancel: cancel, dial: override, streams: make(map[uint32]*streamEntry), onFrame: cb}
}
func (d *StreamDispatcher) Handle(f protocol.Frame) error {
	if d == nil {
		return ErrStreamNotFound
	}
	switch f.Type {
	case protocol.FrameOpenStream:
		var p StreamOpenPayload
		if err := json.Unmarshal(f.Payload, &p); err != nil {
			return err
		}
		if f.StreamID == 0 || p.TargetHost == "" || p.TargetPort < 1 || p.TargetPort > 65535 {
			return errors.New("agent: invalid stream target")
		}
		c, err := d.dial(d.ctx, p.Protocol, p.TargetHost, p.TargetPort)
		if err != nil {
			return err
		}
		d.mu.Lock()
		if _, exists := d.streams[f.StreamID]; exists {
			d.mu.Unlock()
			_ = c.Close()
			return ErrDuplicateStream
		}
		d.generation++
		entry := &streamEntry{conn: c, generation: d.generation}
		d.streams[f.StreamID] = entry
		d.mu.Unlock()
		go d.readBack(f.StreamID, entry)
		return nil
	case protocol.FrameData:
		d.mu.Lock()
		entry, ok := d.streams[f.StreamID]
		d.mu.Unlock()
		if !ok {
			return ErrStreamNotFound
		}
		_, err := entry.conn.Write(f.Payload)
		return err
	case protocol.FrameHalfClose, protocol.FrameReset:
		d.mu.Lock()
		entry, ok := d.streams[f.StreamID]
		d.mu.Unlock()
		if !ok {
			return ErrStreamNotFound
		}
		d.mu.Lock()
		if current, exists := d.streams[f.StreamID]; exists && current == entry {
			delete(d.streams, f.StreamID)
		}
		d.mu.Unlock()
		return entry.conn.Close()
	default:
		return nil
	}
}

func (d *StreamDispatcher) readBack(id uint32, entry *streamEntry) {
	buf := make([]byte, 32<<10)
	for {
		n, err := entry.conn.Read(buf)
		if n > 0 && d.onFrame != nil {
			d.onFrame(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: id, Payload: append([]byte(nil), buf[:n]...)})
		}
		if err != nil {
			d.mu.Lock()
			current, ok := d.streams[id]
			stale := !ok || current != entry
			if !stale {
				delete(d.streams, id)
			}
			cb := d.onFrame
			d.mu.Unlock()
			if !stale && cb != nil {
				cb(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameHalfClose, StreamID: id})
			}
			return
		}
	}
}
func (d *StreamDispatcher) Close() error {
	d.cancel()
	d.mu.Lock()
	defer d.mu.Unlock()
	for id, entry := range d.streams {
		_ = entry.conn.Close()
		delete(d.streams, id)
	}
	return nil
}

type FrameTransport interface {
	Send(protocol.Frame) error
	Receive() (protocol.Frame, error)
	Close() error
}
type Session struct {
	transport               FrameTransport
	sendMu                  sync.Mutex
	metadataMu              sync.Mutex
	metadataCollector       *MetadataCollector
	metadataAgentID         string
	metadataNodeID          string
	metadataEpoch           int64
	metadataRevision        uint64
	metadataReported        bool
	metadataSnapshot        MetadataSnapshot
	closed                  atomic.Bool
	BaseBackoff, MaxBackoff time.Duration
	HeartbeatInterval       time.Duration
	Rand                    *rand.Rand
}

func NewSession(tr FrameTransport) *Session {
	return &Session{transport: tr, BaseBackoff: time.Second, MaxBackoff: 30 * time.Second, Rand: rand.New(rand.NewSource(time.Now().UnixNano()))}
}

// NewSessionWithMetadata attaches an independent metadata collector. Metadata
// failures are returned to the caller and never close the frame transport.
func NewSessionWithMetadata(tr FrameTransport, collector *MetadataCollector) *Session {
	session := NewSession(tr)
	session.metadataCollector = collector
	return session
}

// SetMetadataIdentity sets the authenticated identity attached to future
// hello/update reports. It is safe to call before Run or ReportMetadata.
func (s *Session) SetMetadataIdentity(agentID, nodeID string, epoch int64) {
	if s == nil {
		return
	}
	s.metadataMu.Lock()
	defer s.metadataMu.Unlock()
	s.metadataAgentID = agentID
	s.metadataNodeID = nodeID
	s.metadataEpoch = epoch
}

// ResetMetadataReport forces the next report to be a complete hello, as is
// required after a reconnect.
func (s *Session) ResetMetadataReport() {
	if s == nil {
		return
	}
	s.metadataMu.Lock()
	defer s.metadataMu.Unlock()
	s.metadataReported = false
}

// CollectMetadata reads the configured snapshot without changing session
// state, allowing metadata errors to remain separate from data forwarding.
func (s *Session) CollectMetadata(ctx context.Context) (MetadataSnapshot, error) {
	if s == nil || s.metadataCollector == nil {
		return MetadataSnapshot{Values: map[string]string{}}, nil
	}
	return s.metadataCollector.Collect(ctx)
}

// ReportMetadata collects and sends a full hello or changed snapshot. Field
// errors are included in the control payload; they never close the session.
func (s *Session) ReportMetadata(ctx context.Context) error {
	if s == nil || s.metadataCollector == nil {
		return nil
	}
	s.metadataMu.Lock()
	defer s.metadataMu.Unlock()
	snapshot, err := s.metadataCollector.Collect(ctx)
	if err != nil {
		return err
	}
	if s.metadataReported && reflect.DeepEqual(s.metadataSnapshot.Fields, snapshot.Fields) && reflect.DeepEqual(s.metadataSnapshot.Errors, snapshot.Errors) {
		return nil
	}
	s.metadataRevision++
	payload := protocol.AgentMetadataPayload{
		AgentID: s.metadataAgentID, NodeID: s.metadataNodeID, Epoch: s.metadataEpoch,
		Revision: s.metadataRevision, ReportedAt: time.Now().UTC(),
		Items:  make([]protocol.AgentMetadataItem, 0, len(snapshot.Fields)),
		Errors: make([]protocol.AgentMetadataError, 0, len(snapshot.Errors)),
	}
	for _, field := range snapshot.Fields {
		payload.Items = append(payload.Items, protocol.AgentMetadataItem{Name: field.Name, Source: field.Source, Value: field.Value})
	}
	for _, fieldErr := range snapshot.Errors {
		payload.Errors = append(payload.Errors, protocol.AgentMetadataError{Name: fieldErr.Name, Code: "collection_error", Message: fieldErr.Error})
	}
	encoded, err := protocol.EncodeAgentMetadataPayload(payload)
	if err != nil {
		return err
	}
	frameType := protocol.FrameAgentMetadataUpdate
	if !s.metadataReported {
		frameType = protocol.FrameAgentHello
	}
	if err := s.send(protocol.Frame{Version: protocol.CurrentVersion, Type: frameType, Payload: encoded}); err != nil {
		return err
	}
	s.metadataSnapshot = snapshot
	s.metadataReported = true
	return nil
}

// SendMetadata is retained as an explicit transport-oriented alias for
// callers that use the session as a reporting loop.
func (s *Session) SendMetadata(ctx context.Context) error { return s.ReportMetadata(ctx) }

func (s *Session) send(frame protocol.Frame) error {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	return s.transport.Send(frame)
}

func (s *Session) Run(ctx context.Context, onFrame func(protocol.Frame) error) error {
	if s == nil || s.transport == nil {
		return context.Canceled
	}
	// Metadata is best-effort and isolated from the data stream. A collection
	// or policy error must not prevent the authenticated session from serving
	// TCP/UDP/HTTP frames.
	_ = s.ReportMetadata(ctx)
	defer s.Close()
	done := make(chan struct{})
	defer close(done)
	if s.HeartbeatInterval > 0 {
		ticker := time.NewTicker(s.HeartbeatInterval)
		defer ticker.Stop()
		go func() {
			for {
				select {
				case <-ticker.C:
					_ = s.send(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FramePing})
				case <-done:
					return
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	go func() {
		select {
		case <-ctx.Done():
			_ = s.Close()
		case <-done:
		}
	}()
	for {
		select {
		case <-ctx.Done():
			_ = s.Close()
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
		if f.Type == protocol.FramePing {
			if err := s.send(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FramePong, Payload: f.Payload}); err != nil {
				return err
			}
			continue
		}
		if f.Type == protocol.FramePong {
			continue
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
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
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
