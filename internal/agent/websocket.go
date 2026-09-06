package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"golang.org/x/net/websocket"

	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
)

var (
	ErrAgentServerURLRequired = errors.New("agent: server URL is required")
	ErrAgentTokenRequired     = errors.New("agent: bearer token is required")
	ErrAgentIdentityRequired  = errors.New("agent: agent ID and node ID are required")
	ErrAgentSessionClosed     = errors.New("agent: session closed")
)

// RunWebSocket dials the configured Server, authenticates with a bearer token,
// reports metadata through the existing Session, and processes server frames
// until the context is cancelled or the connection closes.
func RunWebSocket(ctx context.Context, serverURL, token, agentID, nodeID string, epoch int64, collector *MetadataCollector, onFrame func(protocol.Frame) error) error {
	if strings.TrimSpace(serverURL) == "" {
		return ErrAgentServerURLRequired
	}
	if strings.TrimSpace(token) == "" {
		return ErrAgentTokenRequired
	}
	if strings.TrimSpace(agentID) == "" || strings.TrimSpace(nodeID) == "" || epoch <= 0 {
		return ErrAgentIdentityRequired
	}
	transport, err := DialWebSocket(ctx, serverURL, token)
	if err != nil {
		return err
	}
	session := NewSessionWithMetadata(transport, collector)
	session.SetMetadataIdentity(agentID, nodeID, epoch)
	return session.Run(ctx, onFrame)
}

// DialWebSocket creates the binary protocol transport used by Agent sessions.
// Only ws:// and wss:// endpoints are accepted; callers must provide a token.
func DialWebSocket(ctx context.Context, serverURL, token string) (FrameTransport, error) {
	if strings.TrimSpace(token) == "" {
		return nil, ErrAgentTokenRequired
	}
	u, err := url.Parse(serverURL)
	if err != nil || (u.Scheme != "ws" && u.Scheme != "wss") || u.Host == "" {
		return nil, fmt.Errorf("agent: invalid websocket URL %q", serverURL)
	}
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
