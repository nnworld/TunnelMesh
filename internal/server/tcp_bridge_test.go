package server

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/tunnelmesh/tunnelmesh/internal/relay"
	"github.com/tunnelmesh/tunnelmesh/internal/routing"
)

type fakeWS struct {
	msgs [][]byte
	out  [][]byte
}

func (w *fakeWS) ReadMessage() (int, []byte, error) {
	if len(w.msgs) == 0 {
		return 0, nil, io.EOF
	}
	b := w.msgs[0]
	w.msgs = w.msgs[1:]
	return 2, b, nil
}
func (w *fakeWS) WriteMessage(_ int, b []byte) error {
	w.out = append(w.out, append([]byte(nil), b...))
	return nil
}
func (w *fakeWS) Close() error { return nil }

type echoConn struct{ strings.Builder }

func (c *echoConn) Read(p []byte) (int, error)  { return 0, io.EOF }
func (c *echoConn) Write(p []byte) (int, error) { return c.Builder.Write(p) }
func (c *echoConn) Close() error                { return nil }

type bridgeOpener struct{ conn io.ReadWriteCloser }

func (o bridgeOpener) OpenStream(context.Context, relay.StreamRequest) (io.ReadWriteCloser, error) {
	return o.conn, nil
}
func (o bridgeOpener) Close() error { return nil }

func TestTCPBridgeHandleBinaryAndLimit(t *testing.T) {
	conn := &echoConn{}
	h := &TCPBridgeHandler{Resolver: routing.NewRouteResolver([]routing.Route{{Domain: "example.com", AgentID: "a", TargetHost: "10.0.0.1", TargetPort: 22}}), Opener: bridgeOpener{conn: conn}, MaxBytes: 64 << 10}
	ws := &fakeWS{msgs: [][]byte{[]byte("ssh")}}
	if err := h.Handle(context.Background(), ws, "example.com"); err != nil {
		t.Fatal(err)
	}
	if conn.String() != "ssh" {
		t.Fatalf("got %q", conn.String())
	}
}
