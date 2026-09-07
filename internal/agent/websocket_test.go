package agent

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/websocket"

	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
)

type lifecycleFrameHandler struct {
	closed chan struct{}
	once   sync.Once
}

func TestDefaultWebSocketHeartbeatMatchesLivenessContract(t *testing.T) {
	if defaultHeartbeatInterval != 30*time.Second {
		t.Fatalf("default heartbeat = %s, want 30s", defaultHeartbeatInterval)
	}
}

func (*lifecycleFrameHandler) Handle(protocol.Frame) error { return nil }
func (h *lifecycleFrameHandler) Close() error {
	h.once.Do(func() { close(h.closed) })
	return nil
}

func TestRunWebSocketCreatesAndClosesHandlerForEveryConnection(t *testing.T) {
	server := httptest.NewServer(websocket.Handler(func(conn *websocket.Conn) {
		var hello []byte
		_ = websocket.Message.Receive(conn, &hello)
		_ = conn.Close()
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := &lifecycleFrameHandler{closed: make(chan struct{})}
	second := &lifecycleFrameHandler{closed: make(chan struct{})}
	factoryCalls := 0
	factoryErr := make(chan error, 1)
	errCh := make(chan error, 1)
	go func() {
		errCh <- RunWebSocketWithHandlerFactory(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), "agent-token", "agent-handler", "node-handler", 1, NewMetadataCollector(nil), func(*Session) SessionFrameHandler {
			factoryCalls++
			switch factoryCalls {
			case 1:
				return first
			case 2:
				select {
				case <-first.closed:
				default:
					factoryErr <- errors.New("first handler remained open when replacement connection started")
				}
				cancel()
				return second
			default:
				return second
			}
		}, WebSocketRunOptions{BaseBackoff: time.Millisecond, MaxBackoff: 2 * time.Millisecond, HeartbeatInterval: time.Hour})
	}()
	select {
	case err := <-factoryErr:
		t.Fatal(err)
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("RunWebSocketWithHandlerFactory() error = %v, want context canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for handler reconnect lifecycle")
	}
	select {
	case <-second.closed:
	case <-time.After(time.Second):
		t.Fatal("second handler was not closed when its session ended")
	}
}
