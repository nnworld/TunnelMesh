package client

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/websocket"

	"github.com/tunnelmesh/tunnelmesh/internal/observability"
	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
)

var (
	ErrClientServerURLRequired = errors.New("client: server URL must be an absolute ws:// or wss:// URL")
	ErrClientTokenRequired     = errors.New("client: bearer token is required")
)

type WebSocketRunOptions struct {
	BaseBackoff time.Duration
	MaxBackoff  time.Duration
	Rand        *rand.Rand
	Metrics     *observability.Metrics
}

func RunWebSocket(ctx context.Context, serverURL, token string, onReady func(*Session) error) error {
	return RunWebSocketWithOptions(ctx, serverURL, token, onReady, WebSocketRunOptions{})
}

func RunWebSocketWithOptions(ctx context.Context, serverURL, token string, onReady func(*Session) error, options WebSocketRunOptions) error {
	if _, err := parseClientWebSocketURL(serverURL); err != nil {
		return err
	}
	if strings.TrimSpace(token) == "" {
		return ErrClientTokenRequired
	}
	if ctx == nil {
		ctx = context.Background()
	}
	base := options.BaseBackoff
	if base <= 0 {
		base = time.Second
	}
	max := options.MaxBackoff
	if max <= 0 {
		max = 30 * time.Second
	}
	rng := options.Rand
	if rng == nil {
		rng = rand.New(rand.NewSource(time.Now().UnixNano()))
	}
	attempt := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		transport, err := DialWebSocket(ctx, serverURL, token)
		if err == nil {
			if options.Metrics != nil {
				options.Metrics.ObserveConnection("client", "websocket", "started", "")
			}
			session := NewSession(transport)
			session.Metrics = options.Metrics
			session.Start()
			if onReady != nil {
				if readyErr := onReady(session); readyErr != nil {
					_ = session.Close()
					return readyErr
				}
			}
			err = session.Wait(ctx)
			_ = session.Close()
			if ctx.Err() != nil {
				return ctx.Err()
			}
		} else if options.Metrics != nil {
			options.Metrics.ObserveConnection("client", "websocket", "failed", observability.NormalizeErrorClass(err))
			options.Metrics.ObserveStage("client", "websocket_upgrade", "failure", observability.NormalizeErrorClass(err), 0)
		}
		attempt++
		delay := clientReconnectDelay(base, max, attempt, rng)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func DialWebSocket(ctx context.Context, serverURL, token string) (ReceiveTransport, error) {
	if strings.TrimSpace(token) == "" {
		return nil, ErrClientTokenRequired
	}
	u, err := parseClientWebSocketURL(serverURL)
	if err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	config, err := websocket.NewConfig(u.String(), clientOriginFor(u))
	if err != nil {
		return nil, err
	}
	config.Header.Set("Authorization", "Bearer "+token)
	conn, err := config.DialContext(ctx)
	if err != nil {
		return nil, err
	}
	conn.MaxPayloadBytes = protocol.MaxPayload + 16
	return &clientWebSocketTransport{conn: conn}, nil
}

func parseClientWebSocketURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "ws" && u.Scheme != "wss") || u.Host == "" || u.User != nil || u.Fragment != "" || !u.IsAbs() {
		return nil, fmt.Errorf("%w: %q", ErrClientServerURLRequired, raw)
	}
	return u, nil
}

func clientOriginFor(u *url.URL) string {
	scheme := "http"
	if u.Scheme == "wss" {
		scheme = "https"
	}
	return (&url.URL{Scheme: scheme, Host: u.Host}).String()
}

func clientReconnectDelay(base, max time.Duration, attempt int, rng *rand.Rand) time.Duration {
	delay := base
	for i := 1; i < attempt && delay < max; i++ {
		delay *= 2
	}
	if delay > max {
		delay = max
	}
	delay = time.Duration(float64(delay) * (0.8 + rng.Float64()*0.4))
	if delay > max {
		return max
	}
	return delay
}

type clientWebSocketTransport struct {
	conn *websocket.Conn
	mu   sync.Mutex
}

func (t *clientWebSocketTransport) Send(frame protocol.Frame) error {
	if t == nil || t.conn == nil {
		return ErrSessionClosed
	}
	var payload bytes.Buffer
	if err := protocol.NewEncoder(&payload).WriteFrame(frame); err != nil {
		return err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return websocket.Message.Send(t.conn, payload.Bytes())
}

func (t *clientWebSocketTransport) Receive() (protocol.Frame, error) {
	if t == nil || t.conn == nil {
		return protocol.Frame{}, ErrSessionClosed
	}
	var payload []byte
	if err := websocket.Message.Receive(t.conn, &payload); err != nil {
		return protocol.Frame{}, err
	}
	return protocol.NewDecoder(bytes.NewReader(payload)).ReadFrame()
}

func (t *clientWebSocketTransport) Close() error {
	if t == nil || t.conn == nil {
		return nil
	}
	return t.conn.Close()
}
