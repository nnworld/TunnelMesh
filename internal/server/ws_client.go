package server

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/observability"
	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
	"github.com/tunnelmesh/tunnelmesh/internal/relay"
	streamsession "github.com/tunnelmesh/tunnelmesh/internal/session"
)

const (
	clientStreamResetMessage = "stream rejected"
	clientOpenLimitMessage   = "open limit reached"
	maxClientOpenAttempts    = 1 << 16
)

type ClientWSHandler struct{ Sessions *ClientSessionManager }

func (h *ClientWSHandler) Attach(id string, c WSConn) *WSFrameTransport {
	tr := NewWSFrameTransport(c)
	if h != nil && h.Sessions != nil {
		h.Sessions.Register(ClientSessionRecord{ConnectionID: id, ConnectionEpoch: 1}, tr)
	}
	return tr
}

type clientReceiveTransport interface {
	FrameTransport
	Receive() (protocol.Frame, error)
}

type clientRelayStream struct {
	conn             io.ReadWriteCloser
	protocol         string
	clientHalfClosed bool
	relayHalfClosed  bool
	sendState        *protocol.StreamState
	windowSignal     chan struct{}
	flowMu           sync.Mutex
	// inbound holds Client-to-Agent DATA that the pump has not handed over yet.
	// It is what keeps the shared frame loop off the critical path of a stalled
	// Agent, so it must stay byte-bounded and be closed with the stream.
	inbound *streamsession.BoundedFrameQueue
}

// ServeClientSession multiplexes one authenticated Client connection onto the
// existing relay transport. Authorization is re-evaluated for every OPEN.
func ServeClientSession(ctx context.Context, principal ClientSessionPrincipal, tr clientReceiveTransport, authorizer StreamAuthorizer, opener relay.NodeTransport) error {
	return serveClientSessionWithMetrics(ctx, principal, tr, authorizer, opener, maxClientOpenAttempts, nil)
}

func serveClientSessionWithOpenLimit(ctx context.Context, principal ClientSessionPrincipal, tr clientReceiveTransport, authorizer StreamAuthorizer, opener relay.NodeTransport, openLimit int) error {
	return serveClientSessionWithMetrics(ctx, principal, tr, authorizer, opener, openLimit, nil)
}

func serveClientSessionWithMetrics(ctx context.Context, principal ClientSessionPrincipal, tr clientReceiveTransport, authorizer StreamAuthorizer, opener relay.NodeTransport, openLimit int, metrics *observability.Metrics) error {
	return serveClientSessionWithService(ctx, principal, tr, authorizer, opener, openLimit, metrics, nil)
}

func serveClientSessionWithService(ctx context.Context, principal ClientSessionPrincipal, tr clientReceiveTransport, authorizer StreamAuthorizer, opener relay.NodeTransport, openLimit int, metrics *observability.Metrics, service *ClientStreamService) error {
	if tr == nil || principal.ConnectionID == "" {
		return ErrSessionClosed
	}
	if openLimit <= 0 {
		openLimit = maxClientOpenAttempts
	}
	if ctx == nil {
		ctx = context.Background()
	}
	writer := streamsession.NewFairFrameWriter(tr.Send, streamsession.FairWriterConfig{})
	go func() { _ = writer.Run(ctx) }()
	defer func() {
		writer.Drain()
		_ = writer.Close()
	}()
	sendControl := func(frame protocol.Frame) error {
		if frame.Type == protocol.FrameData && frame.StreamID != 0 {
			return writer.EnqueueData(frame.StreamID, frame)
		}
		return writer.EnqueueControl(frame)
	}
	var mu sync.Mutex
	streams := make(map[uint32]*clientRelayStream)
	openings := make(map[uint32]OpenFuture)
	seen := make(map[uint32]struct{})
	var clientObservability *ClientObservabilityService
	if service != nil {
		clientObservability = service.observability
	}
	closeStream := func(id uint32) {
		mu.Lock()
		stream := streams[id]
		delete(streams, id)
		mu.Unlock()
		if stream != nil {
			stream.inbound.Close()
			_ = stream.conn.Close()
		}
		if clientObservability != nil {
			_ = clientObservability.StreamClosed(principal)
		}
	}
	reset := func(id uint32) error {
		if id == 0 {
			return nil
		}
		return sendControl(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameReset, StreamID: id, Payload: []byte(clientStreamResetMessage)})
	}
	newRelayStream := func(id uint32, conn io.ReadWriteCloser, proto string, initialWindow uint32) *clientRelayStream {
		stream := &clientRelayStream{conn: conn, protocol: proto}
		// Every positive window is honoured exactly as the Client advertised it.
		// Enlarging one would overrun the peer's inbound buffer, and ignoring flow
		// control for a small one would do the same with extra steps; a sub-frame
		// window is served by relayToClient, which caps each read to the credit that
		// is available right now, so the stream crawls instead of stalling on a wait
		// for a whole frame that can never be granted.
		if id != 0 && initialWindow > 0 {
			if sendState, err := protocol.NewStreamState(id, initialWindow); err == nil && sendState != nil {
				_ = sendState.OpenLocal()
				stream.sendState = sendState
				stream.windowSignal = make(chan struct{}, 1)
			}
		}
		// The queue is credit the pump needs to make progress while the Agent is
		// slow, so it is sized from the same window this stream runs on: overflow
		// then means the Client ignored its own credit, never ordinary
		// backpressure. Streams without a usable window get the advertised
		// default, which bounds memory per stream the same way.
		stream.inbound = streamsession.NewBoundedFrameQueue(int(protocol.NegotiateReceiveWindow(initialWindow)))
		go pumpClientUpload(id, stream, &mu, streams, metrics, closeStream, reset)
		return stream
	}
	defer func() {
		mu.Lock()
		remaining := make([]*clientRelayStream, 0, len(streams))
		for id, stream := range streams {
			delete(streams, id)
			remaining = append(remaining, stream)
		}
		mu.Unlock()
		for _, stream := range remaining {
			stream.inbound.Close()
			_ = stream.conn.Close()
		}
		for _, future := range openings {
			future.Cancel()
		}
	}()
	sendOpenResult := func(id uint32, result protocol.OpenResultPayload) error {
		payload, err := protocol.EncodeOpenResultPayload(result)
		if err != nil {
			return err
		}
		return sendControl(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameOpenResult, StreamID: id, Payload: payload})
	}
	sendMetadataAck := func(ack protocol.ClientMetadataAckPayload) error {
		payload, err := protocol.EncodeClientMetadataAckPayload(ack)
		if err != nil {
			return err
		}
		return sendControl(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameClientMetadataAck, Payload: payload})
	}
	if clientObservability != nil {
		epoch, err := newClientConnectionEpoch()
		if err != nil {
			return err
		}
		clientObservability.manager.Register(ClientSessionRecord{
			ConnectionID: principal.ConnectionID, TokenID: principal.Identity.TokenID,
			OwnerUserID: principal.Identity.OwnerUserID, ServerNodeID: clientObservability.serverNodeID,
			ConnectionEpoch: epoch, StartedAt: time.Now().UTC(), MetadataEnabled: principal.MetadataEnabled,
		}, tr)
		heartbeatCtx, stopHeartbeat := context.WithCancel(ctx)
		heartbeatDone := make(chan struct{})
		go func() {
			defer close(heartbeatDone)
			ticker := time.NewTicker(DefaultClientConnectionHeartbeatInterval)
			defer ticker.Stop()
			for {
				select {
				case <-heartbeatCtx.Done():
					return
				case <-ticker.C:
					_ = clientObservability.Heartbeat(heartbeatCtx, principal)
				}
			}
		}()
		defer func() {
			stopHeartbeat()
			<-heartbeatDone
			_ = clientObservability.Release(context.Background(), principal)
			clientObservability.manager.Remove(principal.ConnectionID)
		}()
	}
	metadataHelloSeen := !principal.MetadataEnabled
	for {
		frame, err := tr.Receive()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if err := frame.Validate(); err != nil {
			if frame.StreamID != 0 {
				_ = reset(frame.StreamID)
				continue
			}
			return err
		}
		if clientObservability != nil && principal.MetadataEnabled && !metadataHelloSeen {
			metadataHelloSeen = true
			if frame.Type == protocol.FrameClientHello {
				payload, decodeErr := protocol.DecodeClientMetadataPayload(frame.Payload)
				var ack protocol.ClientMetadataAckPayload
				if decodeErr != nil {
					ack = rejectedClientMetadataAck(payload, "invalid_payload")
					_, _ = clientObservability.RegisterLegacy(ctx, principal)
				} else {
					var helloErr error
					ack, helloErr = clientObservability.Hello(ctx, principal, payload)
					if helloErr != nil {
						_, _ = clientObservability.RegisterLegacy(ctx, principal)
					}
				}
				if err := sendMetadataAck(ack); err != nil {
					return err
				}
				continue
			}
			// A caller may negotiate the metadata subprotocol but wrap the
			// transport in a legacy Session. Preserve that connection by
			// downgrading observability and processing the first frame normally.
			_ = sendMetadataAck(protocol.ClientMetadataAckPayload{
				Accepted: false, Errors: []protocol.ClientMetadataError{{
					Name: "client_hello", Code: "invalid_frame", Message: "CLIENT_HELLO is required",
				}},
			})
			_, _ = clientObservability.RegisterLegacy(ctx, principal)
		}
		switch frame.Type {
		case protocol.FramePing:
			if clientObservability != nil {
				clientObservability.manager.ObserveHeartbeat(principal.ConnectionID)
			}
			if err := sendControl(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FramePong, Payload: append([]byte(nil), frame.Payload...)}); err != nil {
				return err
			}
		case protocol.FrameClientMetadataUpdate:
			if clientObservability == nil || !principal.MetadataEnabled {
				_ = reset(frame.StreamID)
				continue
			}
			payload, decodeErr := protocol.DecodeClientMetadataPayload(frame.Payload)
			if decodeErr != nil {
				_ = sendMetadataAck(protocol.ClientMetadataAckPayload{Accepted: false, Errors: []protocol.ClientMetadataError{{
					Name: "client_metadata", Code: "invalid_payload", Message: "metadata payload is invalid",
				}}})
				continue
			}
			ack, updateErr := clientObservability.Update(ctx, principal, payload)
			if updateErr != nil {
				ack.Accepted = false
			}
			if err := sendMetadataAck(ack); err != nil {
				return err
			}
		case protocol.FramePong:
			continue
		case protocol.FrameGoAway:
			return nil
		case protocol.FrameOpenStream:
			mu.Lock()
			_, duplicate := seen[frame.StreamID]
			_, opening := openings[frame.StreamID]
			if !duplicate && !opening && len(seen) >= openLimit {
				mu.Unlock()
				return sendControl(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameGoAway, Payload: []byte(clientOpenLimitMessage)})
			}
			if !duplicate && !opening {
				seen[frame.StreamID] = struct{}{}
			}
			mu.Unlock()
			if duplicate || opening {
				_ = reset(frame.StreamID)
				continue
			}
			request, decodeErr := protocol.DecodeStreamOpenPayload(frame.Payload)
			if decodeErr != nil {
				_ = reset(frame.StreamID)
				continue
			}
			if principal.StrictOpen && frame.Flags&protocol.FlagStrictOpen != 0 && service != nil {
				future := service.Open(ctx, principal, frame.StreamID, request)
				mu.Lock()
				openings[frame.StreamID] = future
				mu.Unlock()
				go func(id uint32, future OpenFuture, request protocol.StreamOpenPayload) {
					result, waitErr := future.Wait(ctx)
					if waitErr != nil {
						result = protocol.OpenResultPayload{Accepted: false, Stage: protocol.OpenResultStageQueue, Code: protocol.OpenResultCodeTimeout, Retryable: true}
					}
					mu.Lock()
					if openings[id] == future {
						delete(openings, id)
					}
					mu.Unlock()
					if !result.Accepted || future.Stream() == nil {
						_ = sendOpenResult(id, result)
						return
					}
					stream := newRelayStream(id, future.Stream(), request.Protocol, frame.Window)
					mu.Lock()
					if _, exists := streams[id]; exists {
						mu.Unlock()
						_ = stream.conn.Close()
						_ = sendOpenResult(id, protocol.OpenResultPayload{Accepted: false, Stage: protocol.OpenResultStageProtocol, Code: protocol.OpenResultCodeInternalError})
						return
					}
					streams[id] = stream
					mu.Unlock()
					if err := sendOpenResult(id, result); err != nil {
						closeStream(id)
						return
					}
					go relayToClient(ctx, id, stream, writer, &mu, streams)
					if clientObservability != nil {
						_ = clientObservability.StreamOpened(principal)
					}
					if metrics != nil {
						metrics.ObserveStream(request.Protocol, "accepted", "")
					}
				}(frame.StreamID, future, request)
				continue
			}
			if decodeErr != nil || authorizer == nil || authorizer.Authorize(ctx, principal, request) != nil || opener == nil {
				if metrics != nil {
					metrics.ObserveStream(request.Protocol, "rejected", observability.NormalizeErrorClass(errors.New("authorization")))
				}
				_ = reset(frame.StreamID)
				continue
			}
			conn, openErr := opener.OpenStream(ctx, relay.StreamRequest{StreamID: frame.StreamID, InitialWindow: frame.Window, AgentID: request.AgentID, Protocol: request.Protocol, TargetHost: request.TargetHost, TargetPort: request.TargetPort, Metadata: append([]byte(nil), request.Metadata...)})
			if openErr != nil || conn == nil {
				if metrics != nil {
					metrics.ObserveStream(request.Protocol, "failed", observability.NormalizeErrorClass(openErr))
				}
				_ = reset(frame.StreamID)
				continue
			}
			stream := newRelayStream(frame.StreamID, conn, request.Protocol, frame.Window)
			mu.Lock()
			streams[frame.StreamID] = stream
			mu.Unlock()
			go relayToClient(ctx, frame.StreamID, stream, writer, &mu, streams)
			if clientObservability != nil {
				_ = clientObservability.StreamOpened(principal)
			}
			if metrics != nil {
				metrics.ObserveStream(request.Protocol, "accepted", "")
			}
		case protocol.FrameData:
			mu.Lock()
			stream := streams[frame.StreamID]
			opening := openings[frame.StreamID]
			mu.Unlock()
			if opening != nil {
				opening.Cancel()
				mu.Lock()
				delete(openings, frame.StreamID)
				mu.Unlock()
				_ = reset(frame.StreamID)
				continue
			}
			if stream == nil || stream.clientHalfClosed {
				_ = reset(frame.StreamID)
				continue
			}
			// Handing the bytes to the stream's pump is the whole job here. The
			// Agent connection is written by exactly one goroutine per stream, so
			// a peer that stops reading parks its own pump instead of the frame
			// loop that also carries OPEN_STREAM, PING and every other stream.
			if !stream.inbound.TryPush(frame) {
				slog.WarnContext(ctx, "client_stream_queue_full_reset",
					"protocol", stream.protocol, "stream_id", frame.StreamID,
					"error_class", "flow_control")
				closeStream(frame.StreamID)
				_ = reset(frame.StreamID)
			}
		case protocol.FrameHalfClose:
			mu.Lock()
			stream := streams[frame.StreamID]
			opening := openings[frame.StreamID]
			if stream != nil {
				stream.clientHalfClosed = true
			}
			mu.Unlock()
			if opening != nil {
				opening.Cancel()
				mu.Lock()
				delete(openings, frame.StreamID)
				mu.Unlock()
				_ = reset(frame.StreamID)
				continue
			}
			if stream == nil {
				_ = reset(frame.StreamID)
				continue
			}
			if stream.inbound.TryPush(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameHalfClose, StreamID: frame.StreamID}) {
				continue
			}
			// The pump is already gone, so nobody will half-close the Agent side
			// for us. Retire the stream rather than leave it open forever.
			slog.WarnContext(ctx, "client_stream_queue_full_reset",
				"protocol", stream.protocol, "stream_id", frame.StreamID,
				"error_class", "stream_closed")
			closeStream(frame.StreamID)
			_ = reset(frame.StreamID)
		case protocol.FrameWindowUpdate:
			mu.Lock()
			stream := streams[frame.StreamID]
			mu.Unlock()
			if stream == nil || stream.sendState == nil {
				continue
			}
			stream.flowMu.Lock()
			err := stream.sendState.AddSendWindow(frame.Window)
			stream.flowMu.Unlock()
			if err != nil {
				closeStream(frame.StreamID)
				_ = reset(frame.StreamID)
				continue
			}
			select {
			case stream.windowSignal <- struct{}{}:
			default:
			}
		case protocol.FrameReset:
			mu.Lock()
			_, exists := streams[frame.StreamID]
			opening := openings[frame.StreamID]
			if opening != nil {
				delete(openings, frame.StreamID)
			}
			mu.Unlock()
			if opening != nil {
				opening.Cancel()
				continue
			}
			if !exists {
				_ = reset(frame.StreamID)
				continue
			}
			closeStream(frame.StreamID)
		default:
			_ = reset(frame.StreamID)
		}
	}
}

// retireClientStream drops a stream that is no longer current and releases both
// of its goroutines: the Agent-side reader and the Client-side upload pump.
// Closing the inbound queue is what lets a pump blocked in Pop return, so every
// path that removes a stream from the map must go through here.
func retireClientStream(id uint32, stream *clientRelayStream, mu *sync.Mutex, streams map[uint32]*clientRelayStream) {
	mu.Lock()
	if streams[id] == stream {
		delete(streams, id)
	}
	mu.Unlock()
	stream.inbound.Close()
	_ = stream.conn.Close()
}

// pumpClientUpload is the single writer for one Client-to-Agent stream. A target
// that stops taking bytes parks only this goroutine and this stream's bounded
// inbound queue, never the shared Client frame loop.
func pumpClientUpload(id uint32, stream *clientRelayStream, mu *sync.Mutex, streams map[uint32]*clientRelayStream, metrics *observability.Metrics, closeStream func(uint32), reset func(uint32) error) {
	for {
		frame, ok := stream.inbound.Pop()
		if !ok {
			return
		}
		if frame.Type == protocol.FrameHalfClose {
			halfCloser, ok := stream.conn.(interface{ CloseWrite() error })
			if !ok || halfCloser.CloseWrite() != nil {
				closeStream(id)
				_ = reset(id)
				return
			}
			mu.Lock()
			complete := stream.relayHalfClosed
			mu.Unlock()
			if complete {
				closeStream(id)
			}
			continue
		}
		written, err := stream.conn.Write(frame.Payload)
		if metrics != nil && written > 0 {
			metrics.ObserveBytes("server", "outbound", stream.protocol, int64(written))
		}
		if err == nil && written != len(frame.Payload) {
			err = io.ErrShortWrite
		}
		if err != nil {
			// A write error ends this stream, not the session: the other
			// streams and the frame loop must keep running.
			closeStream(id)
			_ = reset(id)
			return
		}
	}
}

func relayToClient(ctx context.Context, id uint32, stream *clientRelayStream, writer *streamsession.FairFrameWriter, mu *sync.Mutex, streams map[uint32]*clientRelayStream) {
	bufferSize := 32 << 10
	if strings.EqualFold(stream.protocol, "udp") {
		bufferSize = protocol.MaxPayload
	}
	buffer := make([]byte, bufferSize)
	for {
		// Agent-originated WINDOW_UPDATE frames must not wait behind the next
		// DATA read; otherwise a window-exhausted peer cannot make progress.
		if controlReader, ok := stream.conn.(interface{ ReadControl() (protocol.Frame, bool) }); ok {
			if control, available := controlReader.ReadControl(); available {
				control.Version = protocol.CurrentVersion
				control.StreamID = id
				if err := writer.EnqueueControl(control); err != nil {
					retireClientStream(id, stream, mu, streams)
					return
				}
				continue
			}
		}
		limit, ok := clientSendLimit(id, stream, mu, streams, bufferSize)
		if !ok {
			return
		}
		n, err := stream.conn.Read(buffer[:limit])
		if n > 0 {
			mu.Lock()
			current := streams[id]
			mu.Unlock()
			if current != stream {
				return
			}
			if err := waitForClientWindow(id, stream, mu, streams, n); err != nil {
				retireClientStream(id, stream, mu, streams)
				return
			}
			if sendErr := writer.EnqueueData(id, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: id, Payload: append([]byte(nil), buffer[:n]...)}); sendErr != nil {
				slog.WarnContext(ctx, "client_stream_queue_full_reset",
					"protocol", stream.protocol, "stream_id", id,
					"error_class", observability.NormalizeErrorClass(sendErr))
				retireClientStream(id, stream, mu, streams)
				// The peer must learn the byte stream ended early. Dropping the
				// stream silently leaves the Client holding a short body, which
				// surfaces as a truncated download with nothing in the log to
				// explain it. Unreachable for a peer that honours its window,
				// because the queue is sized from the credit it granted.
				_ = writer.EnqueueControl(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameReset, StreamID: id, Payload: []byte(clientStreamResetMessage)})
				return
			}
		}
		if err != nil {
			mu.Lock()
			current := streams[id]
			if current == stream {
				stream.relayHalfClosed = true
			}
			complete := stream.clientHalfClosed
			mu.Unlock()
			if current != stream {
				return
			}
			if !errors.Is(err, io.EOF) {
				retireClientStream(id, stream, mu, streams)
				_ = writer.EnqueueControl(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameReset, StreamID: id, Payload: []byte(clientStreamResetMessage)})
				return
			}
			_ = writer.EnqueueControl(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameHalfClose, StreamID: id})
			if complete {
				retireClientStream(id, stream, mu, streams)
			}
			return
		}
	}
}

func newClientConnectionEpoch() (int64, error) {
	var entropy [8]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return 0, err
	}
	epoch := int64(binary.BigEndian.Uint64(entropy[:]))
	if epoch <= 0 {
		epoch = -epoch
	}
	if epoch <= 0 {
		epoch = 1
	}
	return epoch, nil
}

// clientSendLimit reports how many bytes may be read from the Agent for one DATA
// frame right now: at most one frame, and never more than the credit the Client
// still holds. Only this goroutine spends send credit, and WINDOW_UPDATE only
// adds to it, so a read capped by the current value can never over-commit.
// Returning false means the stream is gone and the caller must stop.
func clientSendLimit(id uint32, stream *clientRelayStream, mu *sync.Mutex, streams map[uint32]*clientRelayStream, want int) (int, bool) {
	if stream.sendState == nil {
		return want, true
	}
	for {
		if available := int(stream.sendState.SendWindow()); available > 0 {
			if available > want {
				available = want
			}
			return available, true
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-stream.windowSignal:
			timer.Stop()
		case <-timer.C:
		}
		mu.Lock()
		current, ok := streams[id]
		mu.Unlock()
		if !ok || current != stream {
			return 0, false
		}
	}
}

func waitForClientWindow(id uint32, stream *clientRelayStream, mu *sync.Mutex, streams map[uint32]*clientRelayStream, size int) error {
	if stream.sendState == nil {
		return nil
	}
	for {
		stream.flowMu.Lock()
		err := stream.sendState.ConsumeSend(uint32(size))
		stream.flowMu.Unlock()
		if err == nil {
			return nil
		}
		if !errors.Is(err, protocol.ErrWindowExhausted) {
			return err
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-stream.windowSignal:
			timer.Stop()
		case <-timer.C:
			mu.Lock()
			current, ok := streams[id]
			mu.Unlock()
			if !ok || current != stream {
				return nil
			}
		}
	}
}
