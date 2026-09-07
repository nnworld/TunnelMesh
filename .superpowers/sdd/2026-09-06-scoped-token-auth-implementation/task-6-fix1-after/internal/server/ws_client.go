package server

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"

	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
	"github.com/tunnelmesh/tunnelmesh/internal/relay"
)

const clientStreamResetMessage = "stream rejected"

type ClientWSHandler struct{ Sessions *ClientSessionManager }

func (h *ClientWSHandler) Attach(id string, c WSConn) *WSFrameTransport {
	tr := NewWSFrameTransport(c)
	if h != nil && h.Sessions != nil {
		h.Sessions.Register(id, tr)
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
}

// ServeClientSession multiplexes one authenticated Client connection onto the
// existing relay transport. Authorization is re-evaluated for every OPEN.
func ServeClientSession(ctx context.Context, principal ClientSessionPrincipal, tr clientReceiveTransport, authorizer StreamAuthorizer, opener relay.NodeTransport) error {
	if tr == nil || principal.ConnectionID == "" {
		return ErrSessionClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var mu sync.Mutex
	streams := make(map[uint32]*clientRelayStream)
	seen := make(map[uint32]struct{})
	closeStream := func(id uint32) {
		mu.Lock()
		stream := streams[id]
		delete(streams, id)
		mu.Unlock()
		if stream != nil {
			_ = stream.conn.Close()
		}
	}
	defer func() {
		mu.Lock()
		remaining := make([]io.Closer, 0, len(streams))
		for id, stream := range streams {
			delete(streams, id)
			remaining = append(remaining, stream.conn)
		}
		mu.Unlock()
		for _, stream := range remaining {
			_ = stream.Close()
		}
	}()
	reset := func(id uint32) error {
		if id == 0 {
			return nil
		}
		return tr.Send(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameReset, StreamID: id, Payload: []byte(clientStreamResetMessage)})
	}
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
		switch frame.Type {
		case protocol.FramePing:
			if err := tr.Send(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FramePong, Payload: append([]byte(nil), frame.Payload...)}); err != nil {
				return err
			}
		case protocol.FramePong:
			continue
		case protocol.FrameGoAway:
			return nil
		case protocol.FrameOpenStream:
			mu.Lock()
			_, duplicate := seen[frame.StreamID]
			seen[frame.StreamID] = struct{}{}
			mu.Unlock()
			if duplicate {
				_ = reset(frame.StreamID)
				continue
			}
			request, decodeErr := protocol.DecodeStreamOpenPayload(frame.Payload)
			if decodeErr != nil || authorizer == nil || authorizer.Authorize(ctx, principal, request) != nil || opener == nil {
				_ = reset(frame.StreamID)
				continue
			}
			conn, openErr := opener.OpenStream(ctx, relay.StreamRequest{StreamID: frame.StreamID, AgentID: request.AgentID, Protocol: request.Protocol, TargetHost: request.TargetHost, TargetPort: request.TargetPort, Metadata: append([]byte(nil), request.Metadata...)})
			if openErr != nil || conn == nil {
				_ = reset(frame.StreamID)
				continue
			}
			stream := &clientRelayStream{conn: conn, protocol: request.Protocol}
			mu.Lock()
			streams[frame.StreamID] = stream
			mu.Unlock()
			go relayToClient(frame.StreamID, stream, tr, &mu, streams)
		case protocol.FrameData:
			mu.Lock()
			stream := streams[frame.StreamID]
			mu.Unlock()
			if stream == nil || stream.clientHalfClosed {
				_ = reset(frame.StreamID)
				continue
			}
			written, err := stream.conn.Write(frame.Payload)
			if err == nil && written != len(frame.Payload) {
				err = io.ErrShortWrite
			}
			if err != nil {
				closeStream(frame.StreamID)
				_ = reset(frame.StreamID)
			}
		case protocol.FrameHalfClose:
			mu.Lock()
			stream := streams[frame.StreamID]
			if stream != nil {
				stream.clientHalfClosed = true
			}
			mu.Unlock()
			if stream == nil {
				_ = reset(frame.StreamID)
				continue
			}
			halfCloser, ok := stream.conn.(interface{ CloseWrite() error })
			if !ok || halfCloser.CloseWrite() != nil {
				closeStream(frame.StreamID)
				_ = reset(frame.StreamID)
				continue
			}
			mu.Lock()
			complete := stream.relayHalfClosed
			mu.Unlock()
			if complete {
				closeStream(frame.StreamID)
			}
		case protocol.FrameReset:
			mu.Lock()
			_, exists := streams[frame.StreamID]
			mu.Unlock()
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

func relayToClient(id uint32, stream *clientRelayStream, tr FrameTransport, mu *sync.Mutex, streams map[uint32]*clientRelayStream) {
	bufferSize := 32 << 10
	if strings.EqualFold(stream.protocol, "udp") {
		bufferSize = protocol.MaxPayload
	}
	buffer := make([]byte, bufferSize)
	for {
		n, err := stream.conn.Read(buffer)
		if n > 0 {
			mu.Lock()
			current := streams[id]
			mu.Unlock()
			if current != stream {
				return
			}
			if sendErr := tr.Send(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameData, StreamID: id, Payload: append([]byte(nil), buffer[:n]...)}); sendErr != nil {
				mu.Lock()
				if streams[id] == stream {
					delete(streams, id)
				}
				mu.Unlock()
				_ = stream.conn.Close()
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
				mu.Lock()
				if streams[id] == stream {
					delete(streams, id)
				}
				mu.Unlock()
				_ = stream.conn.Close()
				_ = tr.Send(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameReset, StreamID: id, Payload: []byte(clientStreamResetMessage)})
				return
			}
			_ = tr.Send(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameHalfClose, StreamID: id})
			if complete {
				mu.Lock()
				if streams[id] == stream {
					delete(streams, id)
				}
				mu.Unlock()
				_ = stream.conn.Close()
			}
			return
		}
	}
}
