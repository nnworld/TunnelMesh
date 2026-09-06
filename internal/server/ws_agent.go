package server

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"

	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
)

var ErrNonBinaryMessage = errors.New("websocket: binary message required")

// WSConn keeps the server independent from a particular WebSocket library.
type WSConn interface {
	ReadMessage() (int, []byte, error)
	WriteMessage(int, []byte) error
	Close() error
}
type WSFrameTransport struct {
	conn WSConn
	mu   sync.Mutex
}

func NewWSFrameTransport(c WSConn) *WSFrameTransport { return &WSFrameTransport{conn: c} }
func (t *WSFrameTransport) Send(f protocol.Frame) error {
	var b bytes.Buffer
	if err := protocol.NewEncoder(&b).WriteFrame(f); err != nil {
		return err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.conn.WriteMessage(2, b.Bytes())
}
func (t *WSFrameTransport) Receive() (protocol.Frame, error) {
	typ, b, err := t.conn.ReadMessage()
	if err != nil {
		return protocol.Frame{}, err
	}
	if typ != 2 {
		return protocol.Frame{}, ErrNonBinaryMessage
	}
	return protocol.NewDecoder(bytes.NewReader(b)).ReadFrame()
}
func (t *WSFrameTransport) Close() error { return t.conn.Close() }

func ServeAgentFrames(tr *WSFrameTransport, onFrame func(protocol.Frame) error) error {
	for {
		f, err := tr.Receive()
		if err != nil {
			if err == io.EOF {
				return nil
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

// ServeAgentSession registers an authenticated Agent and routes metadata
// control frames through the fenced session manager while preserving the
// existing callback path for stream frames.
func ServeAgentSession(ctx context.Context, manager *AgentSessionManager, registration AgentRegistration, tr *WSFrameTransport, onFrame func(protocol.Frame) error) error {
	if manager == nil || tr == nil {
		return ErrSessionClosed
	}
	session, err := manager.Register(ctx, registration, tr)
	if err != nil {
		return err
	}
	defer manager.RemoveSession(registration.AgentID, session)
	return ServeAgentFrames(tr, func(frame protocol.Frame) error {
		if frame.Type != protocol.FrameAgentHello && frame.Type != protocol.FrameAgentMetadataUpdate {
			if onFrame != nil {
				return onFrame(frame)
			}
			return nil
		}
		ack, err := session.HandleMetadataFrame(ctx, frame)
		if err != nil {
			// Decode/size failures cannot be safely attributed to a payload
			// identity, but still receive a field-scoped ACK so the data session
			// remains alive.
			ackPayload := protocol.AgentMetadataAckPayload{
				AgentID: registration.AgentID,
				Epoch:   registration.Epoch,
				Errors:  []protocol.AgentMetadataError{{Name: "payload", Code: "invalid_payload", Message: "metadata payload rejected"}},
			}
			encoded, encodeErr := protocol.EncodeAgentMetadataAckPayload(ackPayload)
			if encodeErr != nil {
				return encodeErr
			}
			return tr.Send(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FrameAgentMetadataAck, Payload: encoded})
		}
		return tr.Send(ack)
	})
}
