package agent

import (
	"context"
	"errors"
	"io"
	"math/rand"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/observability"
	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
)

var ErrStreamNotFound = errors.New("agent: stream not found")
var ErrDuplicateStream = errors.New("agent: duplicate stream")

const agentStreamResetMessage = "stream rejected"

type StreamOpenPayload = protocol.StreamOpenPayload
type StreamDialFunc func(context.Context, string, string, int) (io.ReadWriteCloser, error)
type streamPayloadDialFunc func(context.Context, protocol.StreamOpenPayload) (io.ReadWriteCloser, error)
type FrameSender func(protocol.Frame) error
type streamEntry struct {
	conn       io.ReadWriteCloser
	generation uint64
	protocol   string
	localHalf  bool
	remoteHalf bool
}
type StreamDispatcher struct {
	ctx         context.Context
	cancel      context.CancelFunc
	dial        StreamDialFunc
	dialPayload streamPayloadDialFunc
	streams     map[uint32]*streamEntry
	generation  uint64
	mu          sync.Mutex
	send        FrameSender
	metrics     *observability.Metrics
	readers     sync.WaitGroup
	closed      bool
}

func NewStreamDispatcher(d Dialer, override StreamDialFunc) *StreamDispatcher {
	return NewStreamDispatcherWithSender(d, override, nil)
}
func NewStreamDispatcherWithCallback(d Dialer, override StreamDialFunc, cb func(protocol.Frame)) *StreamDispatcher {
	var sender FrameSender
	if cb != nil {
		sender = func(frame protocol.Frame) error {
			cb(frame)
			return nil
		}
	}
	return NewStreamDispatcherWithSender(d, override, sender)
}
func NewStreamDispatcherWithSender(d Dialer, override StreamDialFunc, sender FrameSender) *StreamDispatcher {
	var dial StreamDialFunc
	var dialPayload streamPayloadDialFunc
	if override != nil {
		dial = override
	} else {
		dialPayload = func(ctx context.Context, payload protocol.StreamOpenPayload) (io.ReadWriteCloser, error) {
			if payload.Protocol == "http" {
				return d.dialHTTPStreamPayload(ctx, payload)
			}
			switch payload.Protocol {
			case "tcp":
				return d.DialTCP(ctx, payload.TargetHost, payload.TargetPort)
			case "udp":
				return d.DialUDP(ctx, payload.TargetHost, payload.TargetPort)
			default:
				return nil, errors.New("agent: unsupported stream protocol")
			}
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &StreamDispatcher{ctx: ctx, cancel: cancel, dial: dial, dialPayload: dialPayload, streams: make(map[uint32]*streamEntry), send: sender}
}

// SetMetrics attaches the process-scoped observer without changing the
// transport contract used by existing callers.
func (d *StreamDispatcher) SetMetrics(metrics *observability.Metrics) {
	if d != nil {
		d.metrics = metrics
	}
}

func (d *StreamDispatcher) Handle(f protocol.Frame) error {
	if d == nil {
		return ErrStreamNotFound
	}
	switch f.Type {
	case protocol.FrameOpenStream:
		p, err := protocol.DecodeStreamOpenPayload(f.Payload)
		if err != nil {
			return err
		}
		if f.StreamID == 0 || p.TargetHost == "" || p.TargetPort < 1 || p.TargetPort > 65535 {
			return errors.New("agent: invalid stream target")
		}
		var c io.ReadWriteCloser
		var dialErr error
		if d.dialPayload != nil {
			c, dialErr = d.dialPayload(d.ctx, p)
		} else {
			c, dialErr = d.dial(d.ctx, p.Protocol, p.TargetHost, p.TargetPort)
		}
		if dialErr != nil {
			if d.metrics != nil {
				d.metrics.ObserveStream(p.Protocol, "failed", observability.NormalizeErrorClass(dialErr))
			}
			return dialErr
		}
		d.mu.Lock()
		if d.closed {
			d.mu.Unlock()
			_ = c.Close()
			return ErrStreamNotFound
		}
		if _, exists := d.streams[f.StreamID]; exists {
			d.mu.Unlock()
			_ = c.Close()
			return ErrDuplicateStream
		}
		d.generation++
		entry := &streamEntry{conn: c, generation: d.generation, protocol: p.Protocol}
		d.streams[f.StreamID] = entry
		d.readers.Add(1)
		d.mu.Unlock()
		go d.readBack(f.StreamID, entry)
		if d.metrics != nil {
			d.metrics.ObserveStream(p.Protocol, "accepted", "")
		}
		return nil
	case protocol.FrameData:
		d.mu.Lock()
		entry, ok := d.streams[f.StreamID]
		localHalf := ok && entry.localHalf
		d.mu.Unlock()
		if !ok {
			return ErrStreamNotFound
		}
		if localHalf {
			return d.rejectAndClose(f.StreamID, entry, protocol.ErrInvalidFrame)
		}
		written, err := entry.conn.Write(f.Payload)
		if d.metrics != nil && written > 0 {
			d.metrics.ObserveBytes("agent", "inbound", entry.protocol, int64(written))
		}
		if err == nil && written != len(f.Payload) {
			err = io.ErrShortWrite
		}
		if err != nil {
			return d.rejectAndClose(f.StreamID, entry, err)
		}
		return nil
	case protocol.FrameHalfClose:
		d.mu.Lock()
		entry, ok := d.streams[f.StreamID]
		if !ok {
			d.mu.Unlock()
			return ErrStreamNotFound
		}
		if entry.localHalf {
			d.mu.Unlock()
			return nil
		}
		entry.localHalf = true
		complete := entry.remoteHalf
		d.mu.Unlock()
		halfCloser, ok := entry.conn.(interface{ CloseWrite() error })
		if !ok {
			return d.rejectAndClose(f.StreamID, entry, errors.New("agent: target stream does not support half-close"))
		}
		if err := halfCloser.CloseWrite(); err != nil {
			return d.rejectAndClose(f.StreamID, entry, err)
		}
		if complete {
			d.mu.Lock()
			if current, exists := d.streams[f.StreamID]; exists && current == entry {
				delete(d.streams, f.StreamID)
			}
			d.mu.Unlock()
			return entry.conn.Close()
		}
		return nil
	case protocol.FrameReset:
		d.mu.Lock()
		entry, ok := d.streams[f.StreamID]
		if ok {
			delete(d.streams, f.StreamID)
		}
		d.mu.Unlock()
		if !ok {
			return ErrStreamNotFound
		}
		return entry.conn.Close()
	default:
		return nil
	}
}

// ActiveStreams returns the number of streams currently owned by this
// connection. It is used by the connection-pool controller for scaling.
func (d *StreamDispatcher) ActiveStreams() int {
	if d == nil {
		return 0
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.streams)
}

func (d *StreamDispatcher) readBack(id uint32, entry *streamEntry) {
	defer d.readers.Done()
	bufferSize := 32 << 10
	if strings.EqualFold(entry.protocol, "udp") {
		bufferSize = protocol.MaxPayload
	}
	buf := make([]byte, bufferSize)
	for {
		n, err := entry.conn.Read(buf)
		if d.ctx.Err() != nil {
			return
		}
		if n > 0 && d.send != nil {
			if sendErr := d.send(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: id, Payload: append([]byte(nil), buf[:n]...)}); sendErr != nil {
				d.removeAndClose(id, entry)
				return
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				d.removeAndClose(id, entry)
				_ = d.sendReset(id)
				return
			}
			d.mu.Lock()
			current, ok := d.streams[id]
			stale := !ok || current != entry
			if !stale {
				entry.remoteHalf = true
			}
			complete := !stale && (entry.localHalf || d.send == nil)
			if complete {
				delete(d.streams, id)
			}
			sender := d.send
			d.mu.Unlock()
			if !stale && sender != nil {
				if sendErr := sender(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameHalfClose, StreamID: id}); sendErr != nil {
					d.removeAndClose(id, entry)
					return
				}
			}
			if complete {
				_ = entry.conn.Close()
			}
			return
		}
	}
}

func (d *StreamDispatcher) sendReset(id uint32) error {
	if d.send == nil {
		return nil
	}
	return d.send(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameReset, StreamID: id, Payload: []byte(agentStreamResetMessage)})
}

func (d *StreamDispatcher) rejectAndClose(id uint32, entry *streamEntry, reason error) error {
	d.removeAndClose(id, entry)
	if d.send == nil {
		return reason
	}
	return d.sendReset(id)
}

func (d *StreamDispatcher) removeAndClose(id uint32, entry *streamEntry) {
	d.mu.Lock()
	if d.streams[id] == entry {
		delete(d.streams, id)
	}
	d.mu.Unlock()
	_ = entry.conn.Close()
}
func (d *StreamDispatcher) Close() error {
	d.cancel()
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		d.readers.Wait()
		return nil
	}
	d.closed = true
	connections := make([]io.Closer, 0, len(d.streams))
	for id, entry := range d.streams {
		delete(d.streams, id)
		connections = append(connections, entry.conn)
	}
	d.mu.Unlock()
	var closeErr error
	for _, conn := range connections {
		closeErr = errors.Join(closeErr, conn.Close())
	}
	d.readers.Wait()
	return closeErr
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
	metadataInstanceID      string
	metadataConnectionID    string
	metadataEpoch           int64
	metadataRevision        uint64
	metadataReported        bool
	metadataSnapshot        MetadataSnapshot
	closed                  atomic.Bool
	BaseBackoff, MaxBackoff time.Duration
	HeartbeatInterval       time.Duration
	Rand                    *rand.Rand
	Metrics                 *observability.Metrics
	OnHeartbeatRTT          func(time.Duration)
	heartbeatSentNanos      atomic.Int64
	heartbeatRTTNanos       atomic.Int64
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

// SetMetadataConnectionIdentity attaches logical and physical connection
// identity to metadata reports while retaining the legacy helper above.
func (s *Session) SetMetadataConnectionIdentity(agentID, nodeID, instanceID, connectionID string, epoch int64) {
	if s == nil {
		return
	}
	s.metadataMu.Lock()
	defer s.metadataMu.Unlock()
	s.metadataAgentID, s.metadataNodeID = agentID, nodeID
	s.metadataInstanceID, s.metadataConnectionID = instanceID, connectionID
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
		AgentID: s.metadataAgentID, NodeID: s.metadataNodeID, InstanceID: s.metadataInstanceID,
		ConnectionID: s.metadataConnectionID, Epoch: s.metadataEpoch,
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
	return s.Send(frame)
}

// Send serializes dispatcher and session control frames on the authenticated
// transport and rejects writes once session shutdown begins.
func (s *Session) Send(frame protocol.Frame) error {
	if s == nil || s.transport == nil || s.closed.Load() {
		return ErrAgentSessionClosed
	}
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	if s.closed.Load() {
		return ErrAgentSessionClosed
	}
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
					s.heartbeatSentNanos.Store(time.Now().UnixNano())
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
			if sent := s.heartbeatSentNanos.Load(); sent > 0 {
				rtt := time.Since(time.Unix(0, sent))
				s.heartbeatRTTNanos.Store(rtt.Nanoseconds())
				if s.OnHeartbeatRTT != nil {
					s.OnHeartbeatRTT(rtt)
				}
			}
			if s.Metrics != nil {
				rtt := time.Duration(0)
				if sent := s.heartbeatSentNanos.Load(); sent > 0 {
					rtt = time.Since(time.Unix(0, sent))
				}
				s.Metrics.ObserveHeartbeat("agent", "pong", rtt)
			}
			continue
		}
		if onFrame != nil {
			if err := onFrame(f); err != nil {
				return err
			}
		}
	}
}

// LastHeartbeatRTT returns the most recent PING/PONG round-trip time.
func (s *Session) LastHeartbeatRTT() time.Duration {
	if s == nil {
		return 0
	}
	return time.Duration(s.heartbeatRTTNanos.Load())
}
func (s *Session) Close() error {
	if s.closed.Swap(true) {
		return nil
	}
	if s.Metrics != nil {
		s.Metrics.ObserveConnection("agent", "websocket", "closed", "")
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
