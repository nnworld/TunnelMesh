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
	streamsession "github.com/tunnelmesh/tunnelmesh/internal/session"
)

var ErrStreamNotFound = errors.New("agent: stream not found")
var ErrDuplicateStream = errors.New("agent: duplicate stream")

const agentStreamResetMessage = "stream rejected"

const (
	// The credit contract lives in internal/protocol so the Server relay and
	// the Agent dispatcher can never drift apart.
	defaultAgentReceiveWindow         = protocol.DefaultAgentReceiveWindow
	defaultAgentWindowUpdateThreshold = protocol.DefaultWindowUpdateThreshold
)

type StreamOpenPayload = protocol.StreamOpenPayload
type StreamDialFunc func(context.Context, string, string, int) (io.ReadWriteCloser, error)
type streamPayloadDialFunc func(context.Context, protocol.StreamOpenPayload) (io.ReadWriteCloser, error)
type FrameSender func(protocol.Frame) error
type streamEntry struct {
	conn           io.ReadWriteCloser
	generation     uint64
	protocol       string
	localHalf      bool
	remoteHalf     bool
	sendState      *protocol.StreamState
	receiveState   *protocol.StreamState
	windowSignal   chan struct{}
	flowMu         sync.Mutex
	receiveUnacked uint32
	openedAt       time.Time
	ttfbRecorded   bool
}
type pendingStream struct {
	cancel        context.CancelFunc
	data          [][]byte
	halfClosed    bool
	strict        bool
	initialWindow uint32
}
type StreamDispatcher struct {
	ctx         context.Context
	cancel      context.CancelFunc
	dialPayload streamPayloadDialFunc
	streams     map[uint32]*streamEntry
	generation  uint64
	mu          sync.Mutex
	send        FrameSender
	metrics     *observability.Metrics
	readers     sync.WaitGroup
	closed      bool
	pending     map[uint32]*pendingStream
	executor    *DialExecutor
	openResult  bool
	ttfbSamples latencySamples
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
	return NewStreamDispatcherWithConfig(d, override, sender, DialExecutorConfig{}, nil)
}

func NewStreamDispatcherWithConfig(d Dialer, override StreamDialFunc, sender FrameSender, config DialExecutorConfig, dial streamPayloadDialFunc) *StreamDispatcher {
	var dialPayload streamPayloadDialFunc
	if dial != nil {
		dialPayload = dial
	} else if override != nil {
		legacyDial := override
		dialPayload = func(ctx context.Context, payload protocol.StreamOpenPayload) (io.ReadWriteCloser, error) {
			return legacyDial(ctx, payload.Protocol, payload.TargetHost, payload.TargetPort)
		}
	} else {
		dialPayload = d.dialStreamPayload
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &StreamDispatcher{
		ctx: ctx, cancel: cancel, dialPayload: dialPayload,
		streams: make(map[uint32]*streamEntry), pending: make(map[uint32]*pendingStream),
		send: sender, executor: NewDialExecutorWithDial(config, dialPayload),
	}
}

// SetMetrics attaches the process-scoped observer without changing the
// transport contract used by existing callers.
func (d *StreamDispatcher) SetMetrics(metrics *observability.Metrics) {
	if d != nil {
		d.metrics = metrics
	}
}

func (d *StreamDispatcher) SetOpenResultEnabled(enabled bool) {
	if d == nil {
		return
	}
	d.mu.Lock()
	d.openResult = enabled
	d.mu.Unlock()
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
		strictOpen := d.openResult && f.Flags&protocol.FlagStrictOpen != 0
		d.mu.Lock()
		if d.closed {
			d.mu.Unlock()
			return ErrStreamNotFound
		}
		if _, exists := d.streams[f.StreamID]; exists {
			d.mu.Unlock()
			d.rejectStreamMode(f.StreamID, DialResult{Code: protocol.OpenResultCodeInternalError, Stage: protocol.OpenResultStageProtocol}, strictOpen)
			return nil
		}
		if _, exists := d.pending[f.StreamID]; exists {
			d.mu.Unlock()
			d.rejectStreamMode(f.StreamID, DialResult{Code: protocol.OpenResultCodeInternalError, Stage: protocol.OpenResultStageProtocol}, strictOpen)
			return nil
		}
		streamCtx, cancel := context.WithCancel(d.ctx)
		d.pending[f.StreamID] = &pendingStream{cancel: cancel, strict: strictOpen, initialWindow: f.Window}
		d.generation++
		d.mu.Unlock()
		request := DialRequest{Frame: f, Payload: p, Result: func(result DialResult) {
			d.completeDial(f.StreamID, p.Protocol, result)
		}}
		if err := d.executor.Submit(streamCtx, request); err != nil {
			d.removePending(f.StreamID)
			code := protocol.OpenResultCodeInternalError
			if errors.Is(err, ErrDialQueueFull) {
				code = protocol.OpenResultCodeQueueFull
			}
			d.rejectStreamMode(f.StreamID, DialResult{Code: code, Stage: protocol.OpenResultStageQueue, Retryable: code == protocol.OpenResultCodeQueueFull}, strictOpen)
			if d.metrics != nil {
				d.metrics.ObserveStream(p.Protocol, "failed", observability.NormalizeErrorClass(err))
			}
		}
		return nil
	case protocol.FrameData:
		d.mu.Lock()
		entry, ok := d.streams[f.StreamID]
		pending := d.pending[f.StreamID]
		pendingOK := pending != nil
		localHalf := ok && entry.localHalf
		strict := pendingOK && pending.strict
		d.mu.Unlock()
		if !ok {
			if pendingOK {
				if strict {
					d.cancelPending(f.StreamID)
					_ = d.sendReset(f.StreamID)
					return nil
				}
				d.mu.Lock()
				if current, exists := d.pending[f.StreamID]; exists {
					current.data = append(current.data, append([]byte(nil), f.Payload...))
				}
				d.mu.Unlock()
				return nil
			}
			_ = d.sendReset(f.StreamID)
			return nil
		}
		if localHalf {
			return d.rejectAndClose(f.StreamID, entry, protocol.ErrInvalidFrame)
		}
		if entry.receiveState != nil {
			if err := entry.receiveState.Handle(f); err != nil {
				return d.rejectAndClose(f.StreamID, entry, err)
			}
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
		d.releaseReceiveWindow(f.StreamID, entry, written)
		return nil
	case protocol.FrameHalfClose:
		d.mu.Lock()
		entry, ok := d.streams[f.StreamID]
		pending, pendingOK := d.pending[f.StreamID]
		if !ok {
			if pendingOK {
				// The dial worker reads this flag in completeDial, so the
				// pending entry must only be mutated while d.mu is held.
				pending.halfClosed = true
			}
			d.mu.Unlock()
			return nil
		}
		if entry.localHalf {
			d.mu.Unlock()
			return nil
		}
		d.mu.Unlock()
		if err := d.handleHalfClose(f.StreamID, entry); err != nil {
			return d.rejectAndClose(f.StreamID, entry, err)
		}
		return nil
	case protocol.FrameReset:
		d.mu.Lock()
		entry, ok := d.streams[f.StreamID]
		pending, pendingOK := d.pending[f.StreamID]
		if ok {
			delete(d.streams, f.StreamID)
		}
		if pendingOK {
			delete(d.pending, f.StreamID)
		}
		d.mu.Unlock()
		if !ok && !pendingOK {
			// A RESET can follow HALF_CLOSE completion after the peer has
			// already retired the stream. It is stale stream state, not a
			// reason to disconnect the Agent session.
			return nil
		}
		if pending != nil && pending.cancel != nil {
			pending.cancel()
		}
		d.executor.Cancel(f.StreamID)
		if entry != nil {
			// RESET is stream-local. A target that is already closed by its
			// reader/writer goroutine must not escalate into a session error.
			_ = entry.conn.Close()
		}
		return nil
	case protocol.FrameWindowUpdate:
		d.mu.Lock()
		entry, ok := d.streams[f.StreamID]
		d.mu.Unlock()
		if !ok {
			// Window updates can arrive after the local stream was reset by
			// backpressure. Treat the late control frame as stream-local and
			// keep the Agent session available for other streams.
			return nil
		}
		if entry.sendState == nil {
			return nil
		}
		entry.flowMu.Lock()
		err := entry.sendState.AddSendWindow(f.Window)
		entry.flowMu.Unlock()
		if err != nil {
			return d.rejectAndClose(f.StreamID, entry, err)
		}
		select {
		case entry.windowSignal <- struct{}{}:
		default:
		}
		return nil
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

func (d *StreamDispatcher) PendingDials() int {
	return d.executor.PendingDials()
}

func (d *StreamDispatcher) OpenP95() time.Duration {
	return d.executor.OpenP95()
}

func (d *StreamDispatcher) TTFBP95() time.Duration {
	return d.ttfbSamples.P95()
}

func (d *StreamDispatcher) completeDial(id uint32, proto string, result DialResult) {
	d.mu.Lock()
	pending, _ := d.pending[id]
	delete(d.pending, id)
	earlyData := [][]byte(nil)
	halfClosed := false
	if pending != nil {
		earlyData = pending.data
		halfClosed = pending.halfClosed
	}
	if result.Code != protocol.OpenResultCodeOK || result.Conn == nil {
		strict := pending != nil && pending.strict
		d.mu.Unlock()
		if pending != nil && pending.cancel != nil {
			pending.cancel()
		}
		if result.Code == protocol.OpenResultCodeOK {
			result.Code = protocol.OpenResultCodeInternalError
			result.Stage = protocol.OpenResultStageConnect
		}
		d.rejectStreamMode(id, result, strict)
		if d.metrics != nil {
			d.metrics.ObserveStream(proto, "failed", observability.NormalizeErrorClass(result.Err))
		}
		_ = strict
		return
	}
	if d.closed {
		d.mu.Unlock()
		_ = result.Conn.Close()
		return
	}
	if _, exists := d.streams[id]; exists {
		d.mu.Unlock()
		_ = result.Conn.Close()
		d.rejectStream(id, DialResult{Code: protocol.OpenResultCodeInternalError, Stage: protocol.OpenResultStageProtocol})
		return
	}
	d.generation++
	entry := &streamEntry{conn: result.Conn, generation: d.generation, protocol: proto}
	entry.openedAt = time.Now()
	if pending != nil && pending.initialWindow > 0 {
		if sendState, err := protocol.NewStreamState(id, pending.initialWindow); err == nil {
			_ = sendState.OpenLocal()
			entry.sendState = sendState
		}
		if receiveState, err := protocol.NewStreamState(id, defaultAgentReceiveWindow); err == nil {
			_ = receiveState.OpenLocal()
			entry.receiveState = receiveState
		}
		entry.windowSignal = make(chan struct{}, 1)
	}
	d.streams[id] = entry
	d.readers.Add(1)
	strict := pending != nil && pending.strict
	d.mu.Unlock()
	var openResultErr error
	if strict {
		openResultErr = d.sendOpenResult(id, protocol.OpenResultPayload{Accepted: true, Stage: protocol.OpenResultStageConnect, Code: protocol.OpenResultCodeOK})
	}
	if d.metrics != nil {
		d.metrics.ObserveStream(proto, "accepted", "")
	}
	for _, payload := range earlyData {
		if _, err := entry.conn.Write(payload); err != nil {
			_ = d.rejectAndClose(id, entry, err)
			return
		}
	}
	if halfClosed {
		if err := d.handleHalfClose(id, entry); err != nil {
			_ = d.rejectAndClose(id, entry, err)
			return
		}
	}
	if openResultErr != nil {
		_ = d.rejectAndClose(id, entry, openResultErr)
		return
	}
	go d.readBack(id, entry)
}

func (d *StreamDispatcher) handleHalfClose(id uint32, entry *streamEntry) error {
	d.mu.Lock()
	if current, exists := d.streams[id]; !exists || current != entry || entry.localHalf {
		d.mu.Unlock()
		return nil
	}
	entry.localHalf = true
	complete := entry.remoteHalf
	d.mu.Unlock()
	halfCloser, ok := entry.conn.(interface{ CloseWrite() error })
	if !ok {
		return errors.New("agent: target stream does not support half-close")
	}
	if err := halfCloser.CloseWrite(); err != nil {
		return err
	}
	if complete {
		d.mu.Lock()
		if current, exists := d.streams[id]; exists && current == entry {
			delete(d.streams, id)
		}
		d.mu.Unlock()
		return entry.conn.Close()
	}
	return nil
}

func (d *StreamDispatcher) rejectStream(id uint32, result DialResult) {
	d.mu.Lock()
	strict := d.openResult
	d.mu.Unlock()
	d.rejectStreamMode(id, result, strict)
}

func (d *StreamDispatcher) rejectStreamMode(id uint32, result DialResult, strict bool) {
	if result.Stage == "" {
		result.Stage = protocol.OpenResultStageConnect
	}
	if result.Code == "" {
		result.Code = protocol.OpenResultCodeInternalError
	}
	if result.RetryAfter < 0 {
		result.RetryAfter = 0
	}
	if strict {
		payload := protocol.OpenResultPayload{
			Accepted: false, Stage: result.Stage, Code: result.Code,
			Retryable: result.Retryable, RetryAfterMS: int(result.RetryAfter.Milliseconds()),
		}
		_ = d.sendOpenResult(id, payload)
		return
	}
	_ = d.sendReset(id)
}

func (d *StreamDispatcher) sendOpenResult(id uint32, result protocol.OpenResultPayload) error {
	if d.send == nil {
		return nil
	}
	payload, err := protocol.EncodeOpenResultPayload(result)
	if err != nil {
		return err
	}
	return d.send(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenResult, StreamID: id, Payload: payload})
}

func (d *StreamDispatcher) removePending(id uint32) {
	d.mu.Lock()
	pending, exists := d.pending[id]
	if exists {
		delete(d.pending, id)
	}
	d.mu.Unlock()
	if pending != nil && pending.cancel != nil {
		pending.cancel()
	}
}

func (d *StreamDispatcher) cancelPending(id uint32) {
	d.mu.Lock()
	pending, exists := d.pending[id]
	if exists {
		delete(d.pending, id)
	}
	d.mu.Unlock()
	if pending != nil && pending.cancel != nil {
		pending.cancel()
	}
	d.executor.Cancel(id)
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
			if !entry.ttfbRecorded {
				entry.ttfbRecorded = true
				d.ttfbSamples.Record(time.Since(entry.openedAt))
			}
			if err := d.waitForSendWindow(id, entry, n); err != nil {
				d.removeAndClose(id, entry)
				return
			}
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

func (d *StreamDispatcher) waitForSendWindow(id uint32, entry *streamEntry, size int) error {
	if entry.sendState == nil {
		return nil
	}
	for {
		entry.flowMu.Lock()
		err := entry.sendState.ConsumeSend(uint32(size))
		entry.flowMu.Unlock()
		if err == nil {
			return nil
		}
		if !errors.Is(err, protocol.ErrWindowExhausted) {
			return err
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-entry.windowSignal:
			timer.Stop()
		case <-timer.C:
			d.mu.Lock()
			current, ok := d.streams[id]
			d.mu.Unlock()
			if !ok || current != entry {
				return nil
			}
		case <-d.ctx.Done():
			timer.Stop()
			return d.ctx.Err()
		}
	}
}

func (d *StreamDispatcher) releaseReceiveWindow(id uint32, entry *streamEntry, size int) {
	if entry.receiveState == nil || size <= 0 {
		return
	}
	entry.flowMu.Lock()
	entry.receiveUnacked += uint32(size)
	update := uint32(0)
	if entry.receiveUnacked >= defaultAgentWindowUpdateThreshold {
		update = entry.receiveUnacked
		entry.receiveUnacked = 0
	}
	entry.flowMu.Unlock()
	if update == 0 {
		return
	}
	if err := entry.receiveState.AddReceiveWindow(update); err == nil && d.send != nil {
		_ = d.send(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameWindowUpdate, StreamID: id, Window: update})
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
	executorErr := d.executor.Close()
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
	return errors.Join(closeErr, executorErr)
}

type FrameTransport interface {
	Send(protocol.Frame) error
	Receive() (protocol.Frame, error)
	Close() error
}
type Session struct {
	transport               FrameTransport
	writer                  *streamsession.FairFrameWriter
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
	metadataCapabilities    []string
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
	writer := streamsession.NewFairFrameWriter(tr.Send, streamsession.FairWriterConfig{})
	go func() { _ = writer.Run(context.Background()) }()
	return &Session{
		transport: tr, writer: writer, BaseBackoff: time.Second, MaxBackoff: 30 * time.Second,
		Rand: rand.New(rand.NewSource(time.Now().UnixNano())),
	}
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

// SetCapabilities advertises protocol features on the next hello/update. The
// server still returns the negotiated intersection in its metadata ACK.
func (s *Session) SetCapabilities(capabilities []string) {
	if s == nil {
		return
	}
	s.metadataMu.Lock()
	defer s.metadataMu.Unlock()
	s.metadataCapabilities = append([]string(nil), capabilities...)
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
	capabilities := append([]string(nil), s.metadataCapabilities...)
	payload := protocol.AgentMetadataPayload{
		AgentID: s.metadataAgentID, NodeID: s.metadataNodeID, InstanceID: s.metadataInstanceID,
		ConnectionID: s.metadataConnectionID, Epoch: s.metadataEpoch,
		Revision: s.metadataRevision, ReportedAt: time.Now().UTC(),
		Items:        make([]protocol.AgentMetadataItem, 0, len(snapshot.Fields)),
		Errors:       make([]protocol.AgentMetadataError, 0, len(snapshot.Errors)),
		Capabilities: capabilities,
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
	if err := s.sendControlSync(protocol.Frame{Version: protocol.CurrentVersion, Type: frameType, Payload: encoded}); err != nil {
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

// sendControlSync preserves the historical synchronous metadata-reporting
// contract while still giving control frames priority over queued DATA.
func (s *Session) sendControlSync(frame protocol.Frame) error {
	if s == nil || s.transport == nil || s.closed.Load() {
		return ErrAgentSessionClosed
	}
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	if s.closed.Load() {
		return ErrAgentSessionClosed
	}
	return s.writer.EnqueueControlSync(frame)
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
	if frame.Type == protocol.FrameData && frame.StreamID != 0 {
		return s.writer.EnqueueData(frame.StreamID, frame)
	}
	return s.writer.EnqueueControl(frame)
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

// WriterQueueWaitP95 exposes the bounded writer-side queue latency so the
// connection pool can react to transport backpressure without inspecting the
// private writer implementation.
func (s *Session) WriterQueueWaitP95() time.Duration {
	if s == nil || s.writer == nil {
		return 0
	}
	return s.writer.WriterQueueWaitP95()
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
	s.writer.Drain()
	_ = s.writer.Close()
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
