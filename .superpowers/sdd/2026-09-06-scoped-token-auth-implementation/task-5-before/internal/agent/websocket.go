package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/websocket"

	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
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
}

// RunWebSocket dials the configured Server, authenticates with a bearer token,
// reports metadata through the existing Session, and reconnects with bounded
// exponential backoff until the context is cancelled.
func RunWebSocket(ctx context.Context, serverURL, token, agentID, nodeID string, epoch int64, collector *MetadataCollector, onFrame func(protocol.Frame) error) error {
	return RunWebSocketWithOptions(ctx, serverURL, token, agentID, nodeID, epoch, collector, onFrame, WebSocketRunOptions{})
}

func RunWebSocketWithOptions(ctx context.Context, serverURL, token, agentID, nodeID string, epoch int64, collector *MetadataCollector, onFrame func(protocol.Frame) error, options WebSocketRunOptions) error {
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
		heartbeat = time.Minute
	}
	rng := options.Rand
	if rng == nil {
		rng = rand.New(rand.NewSource(time.Now().UnixNano()))
	}
	attempt, currentEpoch := 0, epoch
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		transport, err := DialWebSocket(ctx, serverURL, token)
		if err == nil {
			session := NewSessionWithMetadata(transport, collector)
			session.BaseBackoff, session.MaxBackoff, session.Rand = base, max, rng
			session.HeartbeatInterval = heartbeat
			session.SetMetadataIdentity(agentID, nodeID, currentEpoch)
			err = session.Run(ctx, onFrame)
			if ctx.Err() != nil {
				return ctx.Err()
			}
			currentEpoch++
		}
		attempt++
		delay := base
		for i := 0; i < attempt-1 && delay < max; i++ {
			delay *= 2
		}
		if delay > max {
			delay = max
		}
		delay = time.Duration(float64(delay) * (0.8 + rng.Float64()*0.4))
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
	if strings.TrimSpace(token) == "" {
		return nil, ErrAgentTokenRequired
	}
	u, err := parseWebSocketURL(serverURL)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	config, err := websocket.NewConfig(serverURL, originFor(u))
	if err != nil {
		return nil, err
	}
	config.Header.Set("Authorization", "Bearer "+token)
	conn, err := websocket.DialConfig(config)
	if err != nil {
		return nil, err
	}
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
	return websocket.Message.Send(t.conn, payload.Bytes())
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
