package agent

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
	"github.com/tunnelmesh/tunnelmesh/internal/reconnect"
	sharedtransport "github.com/tunnelmesh/tunnelmesh/internal/transport"
)

// agentPrefaceTimeout bounds the TLS handshake and the preface reply. A server that
// stalls here is treated as a failed dial, so the agent backs off and retries.
const agentPrefaceTimeout = 10 * time.Second

// prefaceReplyLen is the two-byte server answer: status, then a reserved zero byte.
const prefaceReplyLen = 2

// dialAgentTransport opens one connection for any supported agent server URL. ws and
// wss keep their original WebSocket path; tls, tcp and quic use the raw preface path.
func dialAgentTransport(ctx context.Context, serverURL, token string, options WebSocketRunOptions) (FrameTransport, error) {
	u, err := parseAgentServerURL(serverURL)
	if err != nil {
		return nil, err
	}
	switch u.Scheme {
	case "ws", "wss":
		return dialWebSocketWithDialer(ctx, serverURL, token, options.Dialer)
	case "tls", "tcp":
		return dialStreamTransport(ctx, u, token, options)
	case "quic":
		return dialQUICTransport(ctx, u, token, options)
	default:
		return nil, fmt.Errorf("agent: unsupported server URL scheme %q", u.Scheme)
	}
}

// parseAgentServerURL accepts every agent transport scheme and rejects anything else.
func parseAgentServerURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !u.IsAbs() || u.Host == "" {
		return nil, fmt.Errorf("agent: server URL %q must be absolute with a host", raw)
	}
	switch u.Scheme {
	case "ws", "wss", "tls", "tcp", "quic":
		return u, nil
	default:
		return nil, fmt.Errorf("agent: unsupported server URL scheme %q", u.Scheme)
	}
}

// agentTransportLabel is the bounded metrics label for a server URL.
func agentTransportLabel(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "unknown"
	}
	switch u.Scheme {
	case "ws", "wss":
		return "websocket"
	case "tls":
		return "tcp_tls"
	case "tcp":
		return "tcp"
	case "quic":
		return "quic"
	default:
		return "unknown"
	}
}

// dialStreamTransport connects over TCP (optionally TLS), sends the preface and wraps
// the stream in the shared frame codec.
func dialStreamTransport(ctx context.Context, u *url.URL, token string, options WebSocketRunOptions) (FrameTransport, error) {
	if strings.TrimSpace(token) == "" {
		return nil, ErrAgentTokenRequired
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	address := u.Host
	if u.Port() == "" {
		return nil, fmt.Errorf("agent: %s server URL needs an explicit port", u.Scheme)
	}
	dialer := options.Dialer
	if dialer == nil {
		var err error
		dialer, err = sharedtransport.NewDialer(sharedtransport.DialOptions{Mode: sharedtransport.ProxyModeDirect})
		if err != nil {
			return nil, err
		}
	}
	// Proxy selection keys on the WebSocket-style scheme so tls behaves like wss and
	// tcp like ws under HTTP CONNECT and system proxies.
	proxyScheme := "ws"
	if u.Scheme == "tls" {
		proxyScheme = "wss"
	}
	conn, err := dialer.DialContextForScheme(ctx, "tcp", address, proxyScheme)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(agentPrefaceTimeout)
	_ = conn.SetDeadline(deadline)
	var stream net.Conn = conn
	if u.Scheme == "tls" {
		secure := tls.Client(conn, agentTLSConfig(u.Hostname(), options.TLSRootCAs))
		if err := secure.HandshakeContext(ctx); err != nil {
			_ = conn.Close()
			return nil, err
		}
		stream = secure
	}
	if err := exchangeAgentPreface(stream, token); err != nil {
		_ = stream.Close()
		return nil, err
	}
	// Clear the dial deadline: the session now owns liveness through heartbeats.
	_ = stream.SetDeadline(time.Time{})
	return newStreamFrameTransport(stream), nil
}

// agentTLSConfig is the client TLS policy: TLS 1.2 minimum with verification on, the
// same bar as the wss dialer.
func agentTLSConfig(serverName string, roots *x509.CertPool) *tls.Config {
	return &tls.Config{ServerName: serverName, MinVersion: tls.VersionTLS12, RootCAs: roots}
}

// dialQUICTransport opens one QUIC connection and runs the agent protocol on its single
// bidirectional stream. TLS 1.3 and the agent ALPN are mandatory; the server refuses any
// other application protocol during the handshake.
func dialQUICTransport(ctx context.Context, u *url.URL, token string, options WebSocketRunOptions) (FrameTransport, error) {
	if strings.TrimSpace(token) == "" {
		return nil, ErrAgentTokenRequired
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if u.Port() == "" {
		return nil, errors.New("agent: quic server URL needs an explicit port")
	}
	dialCtx, cancel := context.WithTimeout(ctx, agentPrefaceTimeout)
	defer cancel()
	tlsConfig := &tls.Config{
		ServerName: u.Hostname(),
		MinVersion: tls.VersionTLS13,
		NextProtos: []string{quicAgentALPN},
		RootCAs:    options.TLSRootCAs,
	}
	conn, err := quic.DialAddr(dialCtx, u.Host, tlsConfig, &quic.Config{
		MaxIdleTimeout:  quicAgentIdleTimeout,
		KeepAlivePeriod: quicAgentKeepAlive,
	})
	if err != nil {
		return nil, err
	}
	stream, err := conn.OpenStreamSync(dialCtx)
	if err != nil {
		_ = conn.CloseWithError(0, "open stream failed")
		return nil, err
	}
	_ = stream.SetDeadline(time.Now().Add(agentPrefaceTimeout))
	if err := exchangeAgentPreface(stream, token); err != nil {
		_ = conn.CloseWithError(0, "preface refused")
		return nil, err
	}
	_ = stream.SetDeadline(time.Time{})
	return protocol.NewStreamConn(quicStream{Stream: stream, conn: conn}, sharedtransport.WriteTimeout), nil
}

// quicStream closes the whole connection with its stream, because the agent protocol
// uses one stream and nothing else would release the connection.
type quicStream struct {
	*quic.Stream
	conn *quic.Conn
}

func (s quicStream) Close() error {
	_ = s.Stream.Close()
	return s.conn.CloseWithError(0, "agent session closed")
}

// Quic settings the agent shares with the server. The server side keeps the same values
// so neither end declares the other idle while the heartbeat is still running.
const (
	quicAgentIdleTimeout = 60 * time.Second
	quicAgentKeepAlive   = 20 * time.Second
)

// quicAgentALPN names the agent protocol; it must match the server's constant.
const quicAgentALPN = "tunnelmesh-agent/1"

// exchangeAgentPreface writes the bearer preface and reads the server's status. Any
// refusal becomes an error whose rejection class matches the server's reason.
func exchangeAgentPreface(conn io.ReadWriter, token string) error {
	if err := protocol.WritePreface(conn, []byte(token)); err != nil {
		return err
	}
	var reply [prefaceReplyLen]byte
	if _, err := io.ReadFull(conn, reply[:]); err != nil {
		return fmt.Errorf("agent: no preface reply from server: %w", err)
	}
	status := protocol.PrefaceStatus(reply[0])
	switch status {
	case protocol.StatusOK:
		return nil
	case protocol.StatusUnauthorized, protocol.StatusUnsupportedVersion:
		return &reconnect.RejectedError{Reason: status.String()}
	case protocol.StatusBusy:
		return errors.New("agent: server is busy; retrying later")
	default:
		return fmt.Errorf("agent: server refused the preface: %s", status)
	}
}

// newStreamFrameTransport adapts a dialed stream to the agent FrameTransport.
func newStreamFrameTransport(stream net.Conn) FrameTransport {
	return protocol.NewStreamConn(stream, sharedtransport.WriteTimeout)
}
