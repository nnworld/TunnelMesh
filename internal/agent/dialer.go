package agent

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
	"github.com/tunnelmesh/tunnelmesh/internal/routing"
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
	// TLSRootCAs is optional for tests and deployments with a private trust
	// bundle. Nil uses the process system roots.
	TLSRootCAs *x509.CertPool
	// ICMPEcho serves "icmp-echo" streams. Nil means this agent process has no
	// ping socket, which is reported as an explicit refusal rather than a silent
	// fallback: the server negotiated the capability, so "unsupported protocol"
	// would be a lie it cannot act on.
	ICMPEcho *Echoer
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

// dialHTTPStreamPayload applies upstream HTTP/TLS options from OPEN_STREAM. It
// remains unexported because the wire payload is the only supported source of
// these security-sensitive options.
func (d Dialer) dialHTTPStreamPayload(ctx context.Context, payload protocol.StreamOpenPayload) (io.ReadWriteCloser, error) {
	if payload.TargetScheme != "https" {
		if d.HTTPStream != nil {
			return d.HTTPStream(ctx, payload.TargetHost, payload.TargetPort)
		}
		return d.DialHTTPStream(ctx, payload.TargetHost, payload.TargetPort)
	}

	// A custom logical HTTP stream cannot be wrapped as TLS because tls.Client
	// requires a net.Conn; HTTPS therefore always uses the real TCP dialer.
	conn, err := d.DialHTTPStream(ctx, payload.TargetHost, payload.TargetPort)
	if err != nil {
		return nil, err
	}
	serverName := payload.TLSServerName
	if serverName == "" {
		serverName = payload.TargetHost
	}
	tlsConn := tls.Client(conn, &tls.Config{
		ServerName: serverName,
		RootCAs:    d.TLSRootCAs,
		MinVersion: tls.VersionTLS12,
	})
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return tlsConn, nil
}
func (d Dialer) DialUDP(ctx context.Context, host string, port int) (net.Conn, error) {
	if err := d.check(ctx, "udp", host, port); err != nil {
		return nil, err
	}
	nd := net.Dialer{Timeout: d.timeout()}
	c, err := nd.DialContext(ctx, "udp", net.JoinHostPort(host, fmt.Sprint(port)))
	return c, err
}

// dialStreamPayload keeps protocol dispatch at the agent boundary so the
// bounded executor depends on one dial interface rather than wire details.
func (d Dialer) dialStreamPayload(ctx context.Context, payload protocol.StreamOpenPayload) (io.ReadWriteCloser, error) {
	if payload.Protocol == "http" {
		return d.dialHTTPStreamPayload(ctx, payload)
	}
	switch payload.Protocol {
	case "tcp":
		return d.DialTCP(ctx, payload.TargetHost, payload.TargetPort)
	case "udp":
		return d.DialUDP(ctx, payload.TargetHost, payload.TargetPort)
	case protocol.StreamProtocolICMPEcho:
		return d.dialICMPEchoStream(ctx, payload)
	default:
		return nil, errors.New("agent: unsupported stream protocol")
	}
}

// errICMPEchoUnavailable reports an agent that was asked for an echo it cannot
// send. It is separate from a target refusal so an operator can tell "this
// agent has no ping socket" apart from "this target is not allowed".
var errICMPEchoUnavailable = errors.New("agent: icmp echo is not available on this agent")

// dialICMPEchoStream validates the target before any socket is touched, then
// hands back a one-request/one-reply adapter. The address is checked twice on
// purpose: routing.IsDangerousAddress is the single source of the SSRF rule the
// rest of the agent already uses, and validateEchoTarget adds the address-family
// and range rules that only ICMP has.
func (d Dialer) dialICMPEchoStream(ctx context.Context, payload protocol.StreamOpenPayload) (io.ReadWriteCloser, error) {
	if d.ICMPEcho == nil {
		return nil, errICMPEchoUnavailable
	}
	// ICMP has no port, so the policy hook sees 0. Passing the wire value
	// instead would let a port allowlist decide an address-only question.
	if err := d.check(ctx, "icmp", payload.TargetHost, 0); err != nil {
		return nil, err
	}
	target, err := netip.ParseAddr(strings.TrimSpace(payload.TargetHost))
	if err != nil {
		// The server resolves names before opening a stream, so a hostname here
		// means the control plane sent something the agent must not resolve.
		return nil, fmt.Errorf("%w: target must be a literal ip address", ErrEchoTargetRejected)
	}
	if routing.IsDangerousAddress(target.AsSlice()) {
		return nil, fmt.Errorf("%w: address class is denied", ErrEchoTargetRejected)
	}
	if _, err := validateEchoTarget(target); err != nil {
		return nil, err
	}
	return newICMPEchoStream(d.ICMPEcho, target), nil
}

// icmpEchoStream gives one in-flight echo the datagram semantics the dispatcher
// already relies on for UDP: a single Write is the request, a single Read is the
// reply, and the stream is over after that. Keeping the same shape is what lets
// the existing stream machinery serve ICMP without a second data path.
type icmpEchoStream struct {
	echoer *Echoer
	target netip.Addr

	// The dial executor cancels the context it passes in as soon as the dial
	// returns, so the echo cannot borrow it: the stream owns this context and
	// only Close ends it.
	ctx    context.Context
	cancel context.CancelFunc

	mu       sync.Mutex
	started  bool
	closed   bool
	readDone bool
	reply    protocol.ICMPEchoReply
	replyErr error

	done     chan struct{}
	doneOnce sync.Once
}

func newICMPEchoStream(echoer *Echoer, target netip.Addr) *icmpEchoStream {
	ctx, cancel := context.WithCancel(context.Background())
	return &icmpEchoStream{
		echoer: echoer, target: target, ctx: ctx, cancel: cancel,
		done: make(chan struct{}),
	}
}

func (s *icmpEchoStream) Write(payload []byte) (int, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return 0, io.ErrClosedPipe
	}
	if s.started {
		s.mu.Unlock()
		return 0, errors.New("agent: an icmp echo stream carries exactly one request")
	}
	request, err := protocol.DecodeICMPEchoRequest(payload)
	if err != nil {
		s.mu.Unlock()
		return 0, fmt.Errorf("agent: invalid icmp echo request: %w", err)
	}
	s.started = true
	s.mu.Unlock()
	// The echo outlives this Write, so it runs on its own goroutine; Read is
	// what collects the outcome.
	go s.run(request)
	return len(payload), nil
}

func (s *icmpEchoStream) Read(buffer []byte) (int, error) {
	select {
	case <-s.done:
	case <-s.ctx.Done():
		return 0, io.EOF
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.readDone {
		return 0, io.EOF
	}
	s.readDone = true
	if errors.Is(s.replyErr, context.Canceled) {
		// The server gave up on this stream. There is nobody left to answer.
		return 0, io.EOF
	}
	encoded, err := protocol.EncodeICMPEchoReply(s.reply)
	if err != nil {
		return 0, err
	}
	if len(encoded) > len(buffer) {
		return 0, fmt.Errorf("agent: icmp echo reply needs %d bytes, read buffer holds %d", len(encoded), len(buffer))
	}
	return copy(buffer, encoded), nil
}

func (s *icmpEchoStream) Close() error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	// Cancelling is what unblocks both the in-flight Send and a Read that is
	// still waiting for a request that will never come.
	s.cancel()
	return nil
}

// run performs the echo and records the outcome. A failure is still an outcome:
// the reply datagram is the only channel this stream has, so an engine that
// stopped mid-echo answers "unsupported" instead of leaving the server to time
// out and guess.
func (s *icmpEchoStream) run(request protocol.ICMPEchoRequest) {
	defer s.doneOnce.Do(func() { close(s.done) })
	echo, err := s.echoer.Send(s.ctx, EchoRequest{
		CorrelationID: request.CorrelationID, Target: s.target,
		Identifier: request.Identifier, Sequence: request.Sequence, Data: request.Data,
	})
	s.mu.Lock()
	defer s.mu.Unlock()
	s.replyErr = err
	status := echo.Status
	if status == "" {
		status = StatusEchoUnsupported
	}
	identifier, sequence := echo.Identifier, echo.Sequence
	if err != nil {
		// Send returns a zero reply when it errored, so the peer's own
		// identifiers have to be restored for the server to match the answer.
		identifier, sequence = request.Identifier, request.Sequence
	}
	s.reply = protocol.ICMPEchoReply{
		CorrelationID: request.CorrelationID, Identifier: identifier, Sequence: sequence,
		Data: echo.Data, Status: status, RTTMillis: echo.RTT.Milliseconds(),
	}
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
