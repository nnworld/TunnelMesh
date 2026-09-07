package client

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"math/rand"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/websocket"

	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
)

func TestClientWSRejectsNonWebSocketURLAndMissingToken(t *testing.T) {
	if err := RunWebSocket(context.Background(), "https://server.example/ws/client", "token", nil); !errors.Is(err, ErrClientServerURLRequired) {
		t.Fatalf("RunWebSocket(https) error = %v, want ErrClientServerURLRequired", err)
	}
	if err := RunWebSocket(context.Background(), "wss://server.example/ws/client", "", nil); !errors.Is(err, ErrClientTokenRequired) {
		t.Fatalf("RunWebSocket(empty token) error = %v, want ErrClientTokenRequired", err)
	}
}

func TestClientWSUsesOnlyAuthorizationAndReconnectsAfterSessionEOF(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var connections atomic.Int32
	server := httptest.NewServer(websocket.Server{
		Handshake: func(_ *websocket.Config, r *http.Request) error {
			if got := r.Header.Get("Authorization"); got != "Bearer client-secret" {
				return errors.New("missing bearer authorization")
			}
			if r.URL.RawQuery != "" || len(r.Cookies()) != 0 {
				return errors.New("credential leaked outside authorization")
			}
			return nil
		},
		Handler: func(conn *websocket.Conn) {
			defer conn.Close()
			connections.Add(1)
			_ = websocket.Message.Send(conn, clientFrameBytes(t, protocol.Frame{Version: protocol.CurrentVersion, Type: protocol.FramePing, Payload: []byte("heartbeat")}))
			var raw []byte
			if err := websocket.Message.Receive(conn, &raw); err != nil {
				return
			}
			frame, err := protocol.NewDecoder(bytes.NewReader(raw)).ReadFrame()
			if err != nil || frame.Type != protocol.FramePong || string(frame.Payload) != "heartbeat" {
				return
			}
		},
	})
	defer server.Close()

	ready := make(chan struct{}, 2)
	errCh := make(chan error, 1)
	go func() {
		errCh <- RunWebSocketWithOptions(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/ws/client", "client-secret", func(session *Session) error {
			if session == nil {
				return errors.New("nil session")
			}
			ready <- struct{}{}
			if len(ready) == 2 {
				cancel()
			}
			return nil
		}, WebSocketRunOptions{BaseBackoff: 5 * time.Millisecond, MaxBackoff: 10 * time.Millisecond, Rand: rand.New(rand.NewSource(1))})
	}()

	for i := 0; i < 2; i++ {
		select {
		case <-ready:
		case <-time.After(2 * time.Second):
			t.Fatalf("ready callbacks = %d, connections = %d", i, connections.Load())
		}
	}
	cancel()
	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("RunWebSocket() error = %v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("RunWebSocket did not stop after context cancellation")
	}
}

func TestClientWSReconnectBackoffGrowsAcrossRepeatedDisconnects(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := httptest.NewServer(websocket.Handler(func(conn *websocket.Conn) {
		_ = conn.Close()
	}))
	defer server.Close()
	readyAt := make(chan time.Time, 3)
	errCh := make(chan error, 1)
	go func() {
		errCh <- RunWebSocketWithOptions(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/ws/client", "client-secret", func(*Session) error {
			readyAt <- time.Now()
			return nil
		}, WebSocketRunOptions{BaseBackoff: 20 * time.Millisecond, MaxBackoff: 200 * time.Millisecond, Rand: rand.New(rand.NewSource(1))})
	}()
	times := make([]time.Time, 3)
	for i := range times {
		select {
		case times[i] = <-readyAt:
		case <-time.After(2 * time.Second):
			t.Fatalf("ready callbacks = %d", i)
		}
	}
	cancel()
	<-errCh
	first := times[1].Sub(times[0])
	second := times[2].Sub(times[1])
	if second <= first+first/2 {
		t.Fatalf("reconnect gaps = %v then %v, want exponential growth", first, second)
	}
}

func TestClientWSDialerSendsNoCookieOrQueryToken(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	requestCh := make(chan *http.Request, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer conn.Close()
		req, readErr := http.ReadRequest(bufio.NewReader(conn))
		if readErr == nil {
			requestCh <- req
		}
		_, _ = conn.Write([]byte("HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\n\r\n"))
	}()
	_, _ = DialWebSocket(context.Background(), "ws://"+listener.Addr().String()+"/ws/client", "client-secret")
	select {
	case request := <-requestCh:
		if request.Header.Get("Authorization") != "Bearer client-secret" || request.URL.RawQuery != "" || request.Header.Get("Cookie") != "" {
			t.Fatalf("request authorization=%q query=%q cookie=%q", request.Header.Get("Authorization"), request.URL.RawQuery, request.Header.Get("Cookie"))
		}
	case <-time.After(time.Second):
		t.Fatal("dial request was not observed")
	}
}

func clientFrameBytes(t *testing.T, frame protocol.Frame) []byte {
	t.Helper()
	var payload bytes.Buffer
	if err := protocol.NewEncoder(&payload).WriteFrame(frame); err != nil {
		t.Fatal(err)
	}
	return payload.Bytes()
}
