package agent

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
	"github.com/tunnelmesh/tunnelmesh/internal/reconnect"
)

// fakePrefaceServer answers one preface with the given status and then closes.
func fakePrefaceServer(t *testing.T, status protocol.PrefaceStatus) (addr string, got chan []byte) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	got = make(chan []byte, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		token, err := protocol.ReadPreface(conn)
		if err != nil {
			got <- nil
			return
		}
		got <- token
		_, _ = conn.Write([]byte{byte(status), 0})
		time.Sleep(200 * time.Millisecond)
	}()
	return ln.Addr().String(), got
}

func TestAgentTransportLabelsAreStable(t *testing.T) {
	cases := map[string]string{
		"wss://edge/ws/agent": "websocket",
		"ws://edge/ws/agent":  "websocket",
		"tls://edge:7443":     "tcp_tls",
		"tcp://edge:7000":     "tcp",
		"quic://edge:7443":    "quic",
	}
	for raw, want := range cases {
		if got := agentTransportLabel(raw); got != want {
			t.Errorf("agentTransportLabel(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestDialStreamSendsPrefaceAndAcceptsOK(t *testing.T) {
	addr, got := fakePrefaceServer(t, protocol.StatusOK)
	transport, err := dialAgentTransport(context.Background(), "tcp://"+addr, "tm_token", WebSocketRunOptions{})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer transport.Close()
	if token := <-got; string(token) != "tm_token" {
		t.Fatalf("server received token %q", token)
	}
}

func TestDialStreamMapsRefusalToRejectedError(t *testing.T) {
	for _, status := range []protocol.PrefaceStatus{protocol.StatusUnauthorized, protocol.StatusUnsupportedVersion} {
		addr, _ := fakePrefaceServer(t, status)
		_, err := dialAgentTransport(context.Background(), "tcp://"+addr, "tm_token", WebSocketRunOptions{})
		if !reconnect.IsRejected(err) {
			t.Fatalf("status %s: err %v must count as rejection", status, err)
		}
	}
}

func TestDialStreamBusyIsRetryableNotRejected(t *testing.T) {
	addr, _ := fakePrefaceServer(t, protocol.StatusBusy)
	_, err := dialAgentTransport(context.Background(), "tcp://"+addr, "tm_token", WebSocketRunOptions{})
	if err == nil || reconnect.IsRejected(err) {
		t.Fatalf("busy must fail without counting as rejection: %v", err)
	}
}

func TestDialStreamRefusesEmptyToken(t *testing.T) {
	if _, err := dialAgentTransport(context.Background(), "tcp://127.0.0.1:1", "", WebSocketRunOptions{}); !errors.Is(err, ErrAgentTokenRequired) {
		t.Fatalf("err = %v, want ErrAgentTokenRequired", err)
	}
}

func TestTLSDialUsesTLS12AndServerName(t *testing.T) {
	cfg := agentTLSConfig("edge.example.com", nil)
	if cfg.MinVersion != tls.VersionTLS12 || cfg.ServerName != "edge.example.com" || cfg.InsecureSkipVerify {
		t.Fatalf("tls config = %+v", cfg)
	}
}

func TestPrefaceStatusReplyIsTwoBytes(t *testing.T) {
	var reply bytes.Buffer
	reply.Write([]byte{byte(protocol.StatusOK), 0})
	if reply.Len() != prefaceReplyLen {
		t.Fatalf("reply length %d, want %d", reply.Len(), prefaceReplyLen)
	}
}
