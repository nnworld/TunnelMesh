package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tunnelmesh/tunnelmesh/internal/relay"
	"github.com/tunnelmesh/tunnelmesh/internal/routing"
)

type proxyOpener struct{ conn io.ReadWriteCloser }

func (o proxyOpener) OpenStream(context.Context, relay.StreamRequest) (io.ReadWriteCloser, error) {
	return o.conn, nil
}
func (o proxyOpener) Close() error { return nil }

func TestHTTPProxyHandler(t *testing.T) {
	conn := &scriptedConn{read: strings.NewReader("HTTP/1.1 200 OK\r\nContent-Length: 5\r\n\r\nhello")}
	routes := routing.NewRouteResolver([]routing.Route{{Domain: "example.com", AgentID: "a", TargetHost: "10.0.0.1", TargetPort: 80}})
	h := NewHTTPProxyHandler(routes, proxyOpener{conn: conn})
	r := httptest.NewRequest(http.MethodGet, "http://example.com/hello", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK || w.Body.String() != "hello" {
		t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
	}
}

type scriptedConn struct {
	read io.Reader
	w    strings.Builder
}

func (c *scriptedConn) Read(p []byte) (int, error)  { return c.read.Read(p) }
func (c *scriptedConn) Write(p []byte) (int, error) { return c.w.Write(p) }
func (c *scriptedConn) Close() error                { return nil }
