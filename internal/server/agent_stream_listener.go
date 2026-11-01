package server

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/config"
	"github.com/tunnelmesh/tunnelmesh/internal/observability"
	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
)

// The raw listeners are reachable without any HTTP layer in front of them. The HTTP
// stack normally absorbs slow headers and connection floods; here the listener has to
// do that itself, before a token is checked, so the budget and the preface deadline
// are part of the listener rather than optional hardening.
const (
	defaultAgentPrefaceTimeout = 3 * time.Second
	defaultAgentMaxUnauthTotal = 256
	defaultAgentMaxUnauthPerIP = 8
	// agentStreamMaxFailures failed authentications from one address within the
	// window block that address until the failures age out.
	agentStreamMaxFailures   = 10
	agentStreamFailureWindow = time.Minute
	agentStreamWriteTimeout  = 30 * time.Second
	agentStreamCloseTimeout  = 5 * time.Second
)

// agentStreamLimits are the pre-authentication limits. Zero fields take the defaults.
type agentStreamLimits struct {
	MaxUnauthTotal int
	MaxUnauthPerIP int
	PrefaceTimeout time.Duration
}

func (l agentStreamLimits) withDefaults() agentStreamLimits {
	if l.MaxUnauthTotal <= 0 {
		l.MaxUnauthTotal = defaultAgentMaxUnauthTotal
	}
	if l.MaxUnauthPerIP <= 0 {
		l.MaxUnauthPerIP = defaultAgentMaxUnauthPerIP
	}
	if l.PrefaceTimeout <= 0 {
		l.PrefaceTimeout = defaultAgentPrefaceTimeout
	}
	return l
}

// agentStreamBudget counts connections that have not yet authenticated. A slot is held
// from accept until the preface is accepted, and released exactly once.
type agentStreamBudget struct {
	mu       sync.Mutex
	maxTotal int
	maxPerIP int
	total    int
	perIP    map[string]int
	failures map[string][]time.Time
}

func newAgentStreamBudget(limits agentStreamLimits) *agentStreamBudget {
	limits = limits.withDefaults()
	return &agentStreamBudget{
		maxTotal: limits.MaxUnauthTotal,
		maxPerIP: limits.MaxUnauthPerIP,
		perIP:    make(map[string]int),
		failures: make(map[string][]time.Time),
	}
}

// acquire admits one unauthenticated connection from ip. The returned release is
// idempotent, so error paths and the success path can both call it.
func (b *agentStreamBudget) acquire(ip string) (func(), bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.blockedLocked(ip, time.Now()) || b.total >= b.maxTotal || b.perIP[ip] >= b.maxPerIP {
		return nil, false
	}
	b.total++
	b.perIP[ip]++
	var once sync.Once
	return func() {
		once.Do(func() {
			b.mu.Lock()
			defer b.mu.Unlock()
			b.total--
			b.perIP[ip]--
			if b.perIP[ip] <= 0 {
				delete(b.perIP, ip)
			}
		})
	}, true
}

func (b *agentStreamBudget) recordFailure(ip string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	b.failures[ip] = append(pruneFailures(b.failures[ip], now), now)
}

func (b *agentStreamBudget) blockedLocked(ip string, now time.Time) bool {
	recent := pruneFailures(b.failures[ip], now)
	if len(recent) == 0 {
		delete(b.failures, ip)
	} else {
		b.failures[ip] = recent
	}
	return len(recent) >= agentStreamMaxFailures
}

func pruneFailures(times []time.Time, now time.Time) []time.Time {
	kept := times[:0]
	for _, at := range times {
		if now.Sub(at) < agentStreamFailureWindow {
			kept = append(kept, at)
		}
	}
	return kept
}

// agentStreamAuthenticator is the token check shared with the WebSocket path.
type agentStreamAuthenticator func(ctx context.Context, token string) (agentConnectionAuthentication, error)

// agentStreamServe hands an admitted connection to the transport-neutral agent core.
type agentStreamServe func(ctx context.Context, transport agentFrameConn, auth agentConnectionAuthentication, afterHello func())

// agentStreamListener serves agents over one raw listener: TCP, TCP with TLS, or a
// QUIC stream. The transport decides how bytes arrive; everything after the preface is
// the same frame protocol the WebSocket path uses.
type agentStreamListener struct {
	name         string
	tlsConfig    *tls.Config
	limits       agentStreamLimits
	budget       *agentStreamBudget
	authenticate agentStreamAuthenticator
	serve        agentStreamServe
	metrics      *observability.Metrics

	mu      sync.Mutex
	closed  bool
	closers []io.Closer
	conns   map[io.Closer]struct{}
	wg      sync.WaitGroup
}

func newAgentStreamListener(name string, tlsConfig *tls.Config, limits agentStreamLimits, authenticate agentStreamAuthenticator, serve agentStreamServe) *agentStreamListener {
	limits = limits.withDefaults()
	return &agentStreamListener{
		name: name, tlsConfig: tlsConfig, limits: limits,
		budget: newAgentStreamBudget(limits), authenticate: authenticate, serve: serve,
		conns: make(map[io.Closer]struct{}),
	}
}

// Serve accepts connections on ln until ctx ends or Close is called.
func (l *agentStreamListener) Serve(ctx context.Context, ln net.Listener) error {
	if !l.addCloser(ln) {
		_ = ln.Close()
		return nil
	}
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil || l.isClosed() {
				return nil
			}
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			return err
		}
		ip := remoteIPOf(conn.RemoteAddr())
		release, ok := l.budget.acquire(ip)
		if !ok {
			_ = conn.Close()
			l.observe("rejected", "budget")
			continue
		}
		if !l.track(conn) {
			release()
			_ = conn.Close()
			continue
		}
		l.wg.Add(1)
		go func() {
			defer l.wg.Done()
			defer l.untrack(conn)
			defer release()
			l.handleConn(ctx, conn, ip, release)
		}()
	}
}

// handleConn runs the TLS handshake, if configured, then the preface exchange.
func (l *agentStreamListener) handleConn(ctx context.Context, raw net.Conn, ip string, release func()) {
	defer raw.Close()
	_ = raw.SetDeadline(time.Now().Add(l.limits.PrefaceTimeout))
	var stream protocol.DeadlineStream = raw
	if l.tlsConfig != nil {
		secure := tls.Server(raw, l.tlsConfig)
		if err := secure.HandshakeContext(ctx); err != nil {
			l.budget.recordFailure(ip)
			l.observe("failed", "handshake")
			return
		}
		stream = secure
	}
	l.handleStream(ctx, stream, ip, release)
}

// handleStream is shared by every transport. It reads the preface, authenticates the
// token, answers with a status, and only then hands the stream to the agent core.
func (l *agentStreamListener) handleStream(ctx context.Context, stream protocol.DeadlineStream, ip string, release func()) {
	_ = stream.SetReadDeadline(time.Now().Add(l.limits.PrefaceTimeout))
	token, err := protocol.ReadPreface(stream)
	if err != nil {
		l.budget.recordFailure(ip)
		l.reply(stream, protocol.StatusBadPreface)
		l.observe("failed", "bad_preface")
		return
	}
	auth, err := l.authenticate(ctx, string(token))
	for i := range token {
		token[i] = 0
	}
	if err != nil {
		l.budget.recordFailure(ip)
		l.reply(stream, protocol.StatusUnauthorized)
		l.observe("failed", "unauthorized")
		return
	}
	if err := l.reply(stream, protocol.StatusOK); err != nil {
		return
	}
	// Admitted: the connection no longer counts against the pre-authentication budget.
	release()
	if l.serve == nil {
		return
	}
	_ = stream.SetDeadline(time.Time{})
	transport := protocol.NewStreamConn(stream, agentStreamWriteTimeout)
	transport.SetIdleTimeout(agentHelloTimeout)
	l.serve(ctx, transport, auth, func() {
		transport.SetIdleTimeout(websocketIdleReadTimeout)
	})
}

func (l *agentStreamListener) reply(stream protocol.DeadlineStream, status protocol.PrefaceStatus) error {
	_ = stream.SetWriteDeadline(time.Now().Add(agentStreamWriteTimeout))
	_, err := stream.Write([]byte{byte(status), 0})
	return err
}

// Close stops accepting, closes live connections and waits, bounded, for handlers to
// return. It is idempotent.
func (l *agentStreamListener) Close() error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil
	}
	l.closed = true
	closers := append([]io.Closer(nil), l.closers...)
	for conn := range l.conns {
		closers = append(closers, conn)
	}
	l.mu.Unlock()
	for _, c := range closers {
		_ = c.Close()
	}
	done := make(chan struct{})
	go func() {
		l.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(agentStreamCloseTimeout):
	}
	return nil
}

func (l *agentStreamListener) addCloser(c io.Closer) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return false
	}
	l.closers = append(l.closers, c)
	return true
}

func (l *agentStreamListener) track(c io.Closer) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return false
	}
	l.conns[c] = struct{}{}
	return true
}

func (l *agentStreamListener) untrack(c io.Closer) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.conns, c)
}

func (l *agentStreamListener) isClosed() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.closed
}

func (l *agentStreamListener) observe(state, class string) {
	if l.metrics != nil {
		l.metrics.ObserveConnection("server", "agent_"+l.name, state, class)
	}
}

// remoteIPOf is the address budget key: the host without its ephemeral port.
func remoteIPOf(addr net.Addr) string {
	if addr == nil {
		return ""
	}
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return addr.String()
	}
	return strings.TrimSpace(host)
}

// loadAgentTLS returns the certificate for one raw listener. A listener's own files win;
// otherwise the server's native certificate is reused. nil means plaintext, which config
// validation only allows for a loopback TCP listener.
func loadAgentTLS(listener config.ServerAgentListenerConfig, native config.TLSConfig, quic bool) (*tls.Config, error) {
	certFile, keyFile := strings.TrimSpace(listener.TLSCertFile), strings.TrimSpace(listener.TLSKeyFile)
	if certFile == "" && native.Enabled {
		certFile, keyFile = strings.TrimSpace(native.CertFile), strings.TrimSpace(native.KeyFile)
	}
	if certFile == "" {
		return nil, nil
	}
	pair, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, err
	}
	cfg := &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12}
	if quic {
		cfg.MinVersion = tls.VersionTLS13
		cfg.NextProtos = []string{quicAgentALPN}
	}
	return cfg, nil
}

// quicAgentALPN names the agent protocol on a QUIC connection. A peer that does not
// offer it is refused by the TLS handshake, before any token is read.
const quicAgentALPN = "tunnelmesh-agent/1"
