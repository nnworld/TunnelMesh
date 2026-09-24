package server

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

func TestHTTPProxyHandlerMarksDynamicAgentIDCaseInsensitive(t *testing.T) {
	routes := routing.NewRouteResolver(nil, routing.WithDynamicSuffix("apps.example.com"))
	opener := &recordingProxyOpener{conn: &scriptedConn{read: strings.NewReader("")}}
	h := NewHTTPProxyHandler(routes, opener)
	request := httptest.NewRequest(http.MethodGet, "http://agent-tfjxvxtpivp8knxb-127-0-0-1-3000.apps.example.com/", nil)

	h.ServeHTTP(httptest.NewRecorder(), request)

	if opener.request.AgentID != "agent-tfjxvxtpivp8knxb" || !opener.request.CaseInsensitiveAgentID {
		t.Fatalf("request = %#v", opener.request)
	}
}

func TestHTTPProxyHandlerPropagatesUpstreamDomainAndTLS(t *testing.T) {
	conn := &scriptedConn{read: strings.NewReader("HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok")}
	routes := routing.NewRouteResolver([]routing.Route{{
		Domain: "app.example.com", AgentID: "agent-a", TargetHost: "10.0.0.1", TargetPort: 443,
		HostHeader: "service.internal.example.com", TargetScheme: "https", TLSServerName: "service.internal.example.com",
	}})
	opener := &recordingProxyOpener{conn: conn}
	h := NewHTTPProxyHandler(routes, opener)

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "http://app.example.com/hello", nil))

	if opener.request.TargetScheme != "https" || opener.request.HostHeader != "service.internal.example.com" ||
		opener.request.TLSServerName != "service.internal.example.com" {
		t.Fatalf("relay request = %#v", opener.request)
	}
	if !strings.Contains(conn.w.String(), "Host: service.internal.example.com") {
		t.Fatalf("upstream request did not use configured Host: %q", conn.w.String())
	}
}

func TestHTTPProxyHandlerDefaultsUpstreamHostToTargetHost(t *testing.T) {
	conn := &scriptedConn{read: strings.NewReader("HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok")}
	routes := routing.NewRouteResolver([]routing.Route{{Domain: "app.example.com", AgentID: "agent-a", TargetHost: "10.0.0.1", TargetPort: 8080}})
	h := NewHTTPProxyHandler(routes, &recordingProxyOpener{conn: conn})

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "http://app.example.com/hello", nil))

	if !strings.Contains(conn.w.String(), "Host: 10.0.0.1") {
		t.Fatalf("upstream request did not default Host to target: %q", conn.w.String())
	}
}

func TestHTTPProxyHandlerWebSocketUsesConfiguredHost(t *testing.T) {
	conn := &scriptedConn{read: strings.NewReader("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")}
	routes := routing.NewRouteResolver([]routing.Route{{
		Domain: "app.example.com", AgentID: "agent-a", TargetHost: "10.0.0.1", TargetPort: 443,
		HostHeader: "service.internal.example.com", TargetScheme: "https", TLSServerName: "service.internal.example.com",
	}})
	opener := &recordingProxyOpener{conn: conn}
	h := NewHTTPProxyHandler(routes, opener)
	request := httptest.NewRequest(http.MethodGet, "http://app.example.com/ws", nil)
	request.Header.Set("Upgrade", "websocket")
	request.Header.Set("Connection", "Upgrade")
	request.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	request.Header.Set("Sec-WebSocket-Version", "13")

	h.ServeHTTP(&hijackResponseWriter{}, request)

	if opener.request.TargetScheme != "https" || opener.request.HostHeader != "service.internal.example.com" ||
		opener.request.TLSServerName != "service.internal.example.com" {
		t.Fatalf("relay request = %#v", opener.request)
	}
	if !strings.Contains(conn.w.String(), "Host: service.internal.example.com") {
		t.Fatalf("upgrade request did not use configured Host: %q", conn.w.String())
	}
}

type recordingProxyOpener struct {
	conn    io.ReadWriteCloser
	request relay.StreamRequest
}

func (o *recordingProxyOpener) OpenStream(_ context.Context, request relay.StreamRequest) (io.ReadWriteCloser, error) {
	o.request = request
	return o.conn, nil
}

func (o *recordingProxyOpener) Close() error { return nil }

type scriptedConn struct {
	read io.Reader
	w    strings.Builder
}

func (c *scriptedConn) Read(p []byte) (int, error)  { return c.read.Read(p) }
func (c *scriptedConn) Write(p []byte) (int, error) { return c.w.Write(p) }
func (c *scriptedConn) Close() error                { return nil }

type hijackResponseWriter struct {
	header http.Header
}

func (w *hijackResponseWriter) Header() http.Header {
	if w.header == nil {
		w.header = http.Header{}
	}
	return w.header
}
func (w *hijackResponseWriter) Write([]byte) (int, error) { return 0, nil }
func (w *hijackResponseWriter) WriteHeader(int)           {}
func (w *hijackResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	client := nopNetConn{}
	return client, bufio.NewReadWriter(bufio.NewReader(client), bufio.NewWriter(client)), nil
}

type nopNetConn struct{}

func (nopNetConn) Read([]byte) (int, error)         { return 0, io.EOF }
func (nopNetConn) Write(p []byte) (int, error)      { return len(p), nil }
func (nopNetConn) Close() error                     { return nil }
func (nopNetConn) LocalAddr() net.Addr              { return nil }
func (nopNetConn) RemoteAddr() net.Addr             { return nil }
func (nopNetConn) SetDeadline(time.Time) error      { return nil }
func (nopNetConn) SetReadDeadline(time.Time) error  { return nil }
func (nopNetConn) SetWriteDeadline(time.Time) error { return nil }

// failingBody reads a little, then breaks like an upstream connection that died
// mid-response.
type failingBody struct {
	remaining int
	err       error
}

func (b *failingBody) Read(p []byte) (int, error) {
	if b.remaining > 0 {
		n := min(b.remaining, len(p))
		copy(p[:n], "partial"[b.remaining-n:])
		b.remaining -= n
		return n, nil
	}
	return 0, b.err
}

func TestCopyResponseReportsShortBody(t *testing.T) {
	breakErr := errors.New("upstream body broke mid-flight")
	resp := &http.Response{
		StatusCode:    http.StatusOK,
		Header:        http.Header{"Content-Length": []string{"11"}},
		Body:          io.NopCloser(&failingBody{remaining: 7, err: breakErr}),
		ContentLength: 11,
	}
	recorder := httptest.NewRecorder()
	if err := copyResponse(context.Background(), recorder, resp); !errors.Is(err, breakErr) {
		t.Fatalf("copyResponse error = %v, want %v", err, breakErr)
	}
}
