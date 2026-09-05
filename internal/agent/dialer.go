package agent

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

type PolicyHook func(context.Context, string, string, int) error
type Dialer struct {
	Timeout    time.Duration
	Policy     PolicyHook
	HTTPClient *http.Client
	// HTTPStream allows the agent to inject a raw logical stream dialer for
	// managed HTTP and WebSocket routes. When nil, HTTP falls back to TCP so
	// request bytes remain transparent to the server-side proxy.
	HTTPStream func(context.Context, string, int) (io.ReadWriteCloser, error)
}

func (d Dialer) check(ctx context.Context, proto, host string, port int) error {
	if d.Policy != nil {
		return d.Policy(ctx, proto, host, port)
	}
	return nil
}
func (d Dialer) timeout() time.Duration {
	if d.Timeout <= 0 {
		return 10 * time.Second
	}
	return d.Timeout
}
func (d Dialer) DialTCP(ctx context.Context, host string, port int) (net.Conn, error) {
	if err := d.check(ctx, "tcp", host, port); err != nil {
		return nil, err
	}
	return (&net.Dialer{Timeout: d.timeout()}).DialContext(ctx, "tcp", net.JoinHostPort(host, fmt.Sprint(port)))
}

// DialHTTPStream opens the raw byte stream used by managed HTTP and WebSocket
// routes while evaluating the policy as protocol "http" (rather than silently
// reusing the TCP policy namespace).
func (d Dialer) DialHTTPStream(ctx context.Context, host string, port int) (net.Conn, error) {
	if err := d.check(ctx, "http", host, port); err != nil {
		return nil, err
	}
	return (&net.Dialer{Timeout: d.timeout()}).DialContext(ctx, "tcp", net.JoinHostPort(host, fmt.Sprint(port)))
}
func (d Dialer) DialUDP(ctx context.Context, host string, port int) (net.Conn, error) {
	if err := d.check(ctx, "udp", host, port); err != nil {
		return nil, err
	}
	nd := net.Dialer{Timeout: d.timeout()}
	c, err := nd.DialContext(ctx, "udp", net.JoinHostPort(host, fmt.Sprint(port)))
	return c, err
}
func (d Dialer) DoHTTP(ctx context.Context, method, url string, body io.Reader) (*http.Response, error) {
	if method == "" {
		method = http.MethodGet
	}
	u, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	host := u.URL.Hostname()
	port := 80
	if strings.EqualFold(u.URL.Scheme, "https") {
		port = 443
	}
	if p := u.URL.Port(); p != "" {
		fmt.Sscanf(p, "%d", &port)
	}
	if err := d.check(ctx, "http", host, port); err != nil {
		return nil, err
	}
	c := d.HTTPClient
	if c == nil {
		c = &http.Client{Timeout: d.timeout()}
	} else if c.Timeout <= 0 {
		clone := *c
		clone.Timeout = d.timeout()
		c = &clone
	}
	return c.Do(u)
}
