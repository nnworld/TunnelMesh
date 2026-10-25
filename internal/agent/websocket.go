package agent

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"github.com/tunnelmesh/tunnelmesh/internal/reconnect"
	"math/rand"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/websocket"

	"github.com/tunnelmesh/tunnelmesh/internal/observability"
	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
	sharedtransport "github.com/tunnelmesh/tunnelmesh/internal/transport"
)

var (
	ErrAgentServerURLRequired = errors.New("agent: server URL is required")
	ErrAgentTokenRequired     = errors.New("agent: bearer token is required")
	ErrAgentIdentityRequired  = errors.New("agent: agent ID and node ID are required")
	ErrAgentSessionClosed     = errors.New("agent: session closed")
)

// WebSocketRunOptions controls reconnect pacing. Zero values use bounded
// production defaults with jitter.
type WebSocketRunOptions struct {
	BaseBackoff       time.Duration
	MaxBackoff        time.Duration
	HeartbeatInterval time.Duration
	Rand              *rand.Rand
	Metrics           *observability.Metrics
	Dialer            *sharedtransport.Dialer
}

const defaultHeartbeatInterval = 30 * time.Second

// SessionFrameHandler owns per-connection stream state. A new handler is
// created after every successful dial and closed before reconnecting.
type SessionFrameHandler interface {
	Handle(protocol.Frame) error
	Close() error
}

type SessionFrameHandlerFactory func(*Session) SessionFrameHandler
type ConnectionSessionFactory func(*Session, string) SessionFrameHandler

// RunWebSocket dials the configured Server, authenticates with a bearer token,
// reports metadata through the existing Session, and reconnects with bounded
// exponential backoff until the context is cancelled.
func RunWebSocket(ctx context.Context, serverURL, token, agentID, nodeID string, epoch int64, collector *MetadataCollector, onFrame func(protocol.Frame) error) error {
	return RunWebSocketWithOptions(ctx, serverURL, token, agentID, nodeID, epoch, collector, onFrame, WebSocketRunOptions{})
}

func RunWebSocketWithOptions(ctx context.Context, serverURL, token, agentID, nodeID string, epoch int64, collector *MetadataCollector, onFrame func(protocol.Frame) error, options WebSocketRunOptions) error {
	return runWebSocket(ctx, serverURL, token, agentID, nodeID, epoch, collector, onFrame, nil, options)
}

func RunWebSocketWithHandlerFactory(ctx context.Context, serverURL, token, agentID, nodeID string, epoch int64, collector *MetadataCollector, factory SessionFrameHandlerFactory, options WebSocketRunOptions) error {
	return runWebSocket(ctx, serverURL, token, agentID, nodeID, epoch, collector, nil, factory, options)
}

func runWebSocket(ctx context.Context, serverURL, token, agentID, nodeID string, epoch int64, collector *MetadataCollector, onFrame func(protocol.Frame) error, factory SessionFrameHandlerFactory, options WebSocketRunOptions) error {
	return runWebSocketConnection(ctx, serverURL, token, agentID, nodeID, "", "", epoch, collector, onFrame, factory, options)
}

func runWebSocketConnection(ctx context.Context, serverURL, token, agentID, nodeID, instanceID, connectionID string, epoch int64, collector *MetadataCollector, onFrame func(protocol.Frame) error, factory SessionFrameHandlerFactory, options WebSocketRunOptions) error {
	if strings.TrimSpace(serverURL) == "" {
		return ErrAgentServerURLRequired
	}
	if strings.TrimSpace(token) == "" {
		return ErrAgentTokenRequired
	}
	if strings.TrimSpace(agentID) == "" || strings.TrimSpace(nodeID) == "" || epoch <= 0 {
		return ErrAgentIdentityRequired
	}
	if _, err := parseWebSocketURL(serverURL); err != nil {
		return err
	}
	base := options.BaseBackoff
	if base <= 0 {
		base = time.Second
	}
	max := options.MaxBackoff
	if max <= 0 {
		max = 30 * time.Second
	}
	heartbeat := options.HeartbeatInterval
	if heartbeat <= 0 {
		heartbeat = defaultHeartbeatInterval
	}
	rng := options.Rand
	if rng == nil {
		rng = rand.New(rand.NewSource(time.Now().UnixNano()))
	}
	backoff := reconnect.New(base, max, rng)
	currentEpoch := epoch
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		stageStarted := time.Now()
		var delay time.Duration
		transport, dialErr := dialWebSocketWithDialer(ctx, serverURL, token, options.Dialer)
		if dialErr == nil {
			if options.Metrics != nil {
				options.Metrics.ObserveConnection("agent", "websocket", "started", "")
				options.Metrics.ObserveStage("agent", "websocket_upgrade", "success", "", time.Since(stageStarted))
			}
			sessionStarted := time.Now()
			session := NewSessionWithMetadata(transport, collector)
			session.Metrics = options.Metrics
			session.BaseBackoff, session.MaxBackoff, session.Rand = base, max, rng
			session.HeartbeatInterval = heartbeat
			if instanceID == "" && connectionID == "" {
				session.SetMetadataIdentity(agentID, nodeID, currentEpoch)
			} else {
				session.SetMetadataConnectionIdentity(agentID, nodeID, instanceID, connectionID, currentEpoch)
			}
			if factory == nil {
				_ = session.Run(ctx, onFrame)
			} else if handler := factory(session); handler == nil {
				_ = session.Close()
			} else {
				_ = errors.Join(session.Run(ctx, handler.Handle), handler.Close())
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			currentEpoch++
			delay = backoff.AfterSession(time.Since(sessionStarted))
		} else {
			if options.Metrics != nil {
				options.Metrics.ObserveConnection("agent", "websocket", "failed", observability.NormalizeErrorClass(dialErr))
				options.Metrics.ObserveStage("agent", "websocket_upgrade", "failure", observability.NormalizeErrorClass(dialErr), time.Since(stageStarted))
			}
			delay = backoff.AfterDialFailure(dialErr)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
	}
}

// DialWebSocket creates the binary protocol transport used by Agent sessions.
// Only ws:// and wss:// endpoints are accepted; callers must provide a token.
func DialWebSocket(ctx context.Context, serverURL, token string) (FrameTransport, error) {
	return dialWebSocketWithDialer(ctx, serverURL, token, nil)
}

func dialWebSocketWithDialer(ctx context.Context, serverURL, token string, dialer *sharedtransport.Dialer) (FrameTransport, error) {
	if strings.TrimSpace(token) == "" {
		return nil, ErrAgentTokenRequired
	}
	u, err := parseWebSocketURL(serverURL)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	config, err := websocket.NewConfig(serverURL, originFor(u))
	if err != nil {
		return nil, err
	}
	config.Header.Set("Authorization", "Bearer "+token)
	if dialer == nil {
		dialer, err = sharedtransport.NewDialer(sharedtransport.DialOptions{Mode: sharedtransport.ProxyModeDirect})
		if err != nil {
			return nil, err
		}
	}
	address := u.Host
	if u.Port() == "" {
		address += ":80"
		if u.Scheme == "wss" {
			address = u.Host + ":443"
		}
	}
	netConn, err := dialer.DialContextForScheme(ctx, "tcp", address, u.Scheme)
	if err != nil {
		return nil, &websocket.DialError{Config: config, Err: err}
	}
	if u.Scheme == "wss" {
		secure := tls.Client(netConn, &tls.Config{ServerName: u.Hostname(), MinVersion: tls.VersionTLS12})
		if err := secure.HandshakeContext(ctx); err != nil {
			_ = netConn.Close()
			return nil, &websocket.DialError{Config: config, Err: err}
		}
		netConn = secure
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = netConn.SetDeadline(deadline)
	}
	conn, err := websocket.NewClient(config, netConn)
	if err != nil {
		_ = netConn.Close()
		return nil, &websocket.DialError{Config: config, Err: err}
	}
	_ = netConn.SetDeadline(time.Time{})
	conn.MaxPayloadBytes = protocol.MaxPayload
	return &websocketFrameTransport{conn: conn}, nil
}

func parseWebSocketURL(serverURL string) (*url.URL, error) {
	u, err := url.Parse(serverURL)
	if err != nil || (u.Scheme != "ws" && u.Scheme != "wss") || u.Host == "" {
		return nil, fmt.Errorf("agent: invalid websocket URL %q", serverURL)
	}
	return u, nil
}

func originFor(u *url.URL) string {
	scheme := "http"
	if u.Scheme == "wss" {
		scheme = "https"
	}
	return (&url.URL{Scheme: scheme, Host: u.Host}).String()
}

type websocketFrameTransport struct{ conn *websocket.Conn }

func (t *websocketFrameTransport) Send(frame protocol.Frame) error {
	if t == nil || t.conn == nil {
		return ErrAgentSessionClosed
	}
	var payload bytes.Buffer
	if err := protocol.NewEncoder(&payload).WriteFrame(frame); err != nil {
		return err
	}
	_ = t.conn.SetWriteDeadline(time.Now().Add(sharedtransport.WriteTimeout))
	if err := websocket.Message.Send(t.conn, payload.Bytes()); err != nil {
		// A timed-out or partial write leaves the frame stream corrupt, so the
		// connection cannot be reused; closing it also wakes the read loop so the
		// session ends and reconnects instead of limping.
		_ = t.conn.Close()
		return err
	}
	return nil
}

func (t *websocketFrameTransport) Receive() (protocol.Frame, error) {
	if t == nil || t.conn == nil {
		return protocol.Frame{}, ErrAgentSessionClosed
	}
	var payload []byte
	if err := websocket.Message.Receive(t.conn, &payload); err != nil {
		return protocol.Frame{}, err
	}
	return protocol.NewDecoder(bytes.NewReader(payload)).ReadFrame()
}

func (t *websocketFrameTransport) Close() error {
	if t == nil || t.conn == nil {
		return nil
	}
	return t.conn.Close()
}
