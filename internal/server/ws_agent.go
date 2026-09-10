package server

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"

	"github.com/tunnelmesh/tunnelmesh/internal/observability"
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
	return serveAgentSession(ctx, manager, registration, tr, nil, func(_ *AgentSession, frame protocol.Frame) error {
		if onFrame == nil {
			return nil
		}
		return onFrame(frame)
	}, nil)
}

func ServeAgentSessionWithMetrics(ctx context.Context, manager *AgentSessionManager, registration AgentRegistration, tr *WSFrameTransport, onFrame func(protocol.Frame) error, metrics *observability.Metrics) error {
	return serveAgentSessionWithMetrics(ctx, manager, registration, tr, nil, func(_ *AgentSession, frame protocol.Frame) error {
		if onFrame == nil {
			return nil
		}
		return onFrame(frame)
	}, nil, metrics)
}

// ServeAgentSessionWithInitialFrame is used by WebSocket adapters that must
// inspect the first hello frame to authenticate and derive the registration.
// The frame is processed through the same metadata fencing path as all later
// frames, then the session remains attached to the transport until EOF.
func ServeAgentSessionWithInitialFrame(ctx context.Context, manager *AgentSessionManager, registration AgentRegistration, tr *WSFrameTransport, initial protocol.Frame, onFrame func(protocol.Frame) error) error {
	return serveAgentSession(ctx, manager, registration, tr, &initial, func(_ *AgentSession, frame protocol.Frame) error {
		if onFrame == nil {
			return nil
		}
		return onFrame(frame)
	}, nil)
}

func serveAgentSession(ctx context.Context, manager *AgentSessionManager, registration AgentRegistration, tr *WSFrameTransport, initial *protocol.Frame, onFrame func(*AgentSession, protocol.Frame) error, onClose func(*AgentSession)) error {
	return serveAgentSessionWithMetrics(ctx, manager, registration, tr, initial, onFrame, onClose, nil)
}

func serveAgentSessionWithMetrics(ctx context.Context, manager *AgentSessionManager, registration AgentRegistration, tr *WSFrameTransport, initial *protocol.Frame, onFrame func(*AgentSession, protocol.Frame) error, onClose func(*AgentSession), metrics *observability.Metrics) error {
	if manager == nil || tr == nil {
		return ErrSessionClosed
	}
	instanceID := registration.InstanceID
	if instanceID == "" {
		instanceID = registration.NodeID
	}
	connectionID := registration.ConnectionID
	if connectionID == "" {
		connectionID = "legacy"
	}
	session, err := manager.Register(ctx, registration, tr)
	if err != nil {
		if metrics != nil {
			metrics.ObserveConnection("server", "agent", "failed", observability.NormalizeErrorClass(err))
			metrics.ObserveAgentConnection(registration.AgentID, instanceID, connectionID, false)
			metrics.ObserveAgentConnectionError(registration.AgentID, instanceID, connectionID, observability.NormalizeErrorClass(err))
		}
		return err
	}
	if metrics != nil {
		metrics.ObserveConnection("server", "agent", "started", "")
		metrics.ObserveAgentConnection(registration.AgentID, session.InstanceID, session.ConnectionID, true)
		metrics.SetAgentConnectionCapacity(registration.AgentID, session.InstanceID, 64)
	}
	return serveRegisteredAgentSessionWithMetrics(ctx, manager, registration, session, tr, initial, onFrame, onClose, metrics)
}

func serveRegisteredAgentSessionWithMetrics(ctx context.Context, manager *AgentSessionManager, registration AgentRegistration, session *AgentSession, tr *WSFrameTransport, initial *protocol.Frame, onFrame func(*AgentSession, protocol.Frame) error, onClose func(*AgentSession), metrics *observability.Metrics) error {
	if manager == nil || tr == nil || session == nil {
		return ErrSessionClosed
	}
	defer func() {
		if metrics != nil {
			metrics.ObserveConnection("server", "agent", "closed", "")
			metrics.ObserveAgentConnection(registration.AgentID, session.InstanceID, session.ConnectionID, false)
		}
		manager.RemoveSession(registration.AgentID, session)
		if onClose != nil {
			onClose(session)
		}
	}()
	handle := func(frame protocol.Frame) error {
		if frame.Type == protocol.FramePing || frame.Type == protocol.FramePong {
			// Heartbeats keep both the live session timestamp and unchanged
			// metadata snapshots fresh. A transient metadata-store failure must
			// not tear down an otherwise healthy forwarding channel.
			_ = manager.RefreshSessionMetadataLease(ctx, session)
			if manager.cfg.HeartbeatCallback != nil {
				_ = manager.cfg.HeartbeatCallback(ctx, session)
			}
			if frame.Type == protocol.FramePing {
				return tr.Send(protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FramePong, Payload: frame.Payload})
			}
			if metrics != nil {
				metrics.ObserveHeartbeat("server", "pong", 0)
			}
			return nil
		}
		if frame.Type != protocol.FrameAgentHello && frame.Type != protocol.FrameAgentMetadataUpdate {
			if onFrame != nil {
				return onFrame(session, frame)
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
	}
	if initial != nil {
		if err := handle(*initial); err != nil {
			return err
		}
	}
	return ServeAgentFrames(tr, handle)
}
