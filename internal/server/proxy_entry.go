package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/config"
	"github.com/tunnelmesh/tunnelmesh/internal/observability"
	"github.com/tunnelmesh/tunnelmesh/internal/proxyentry"
	"github.com/tunnelmesh/tunnelmesh/internal/relay"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

const (
	// Fallbacks for the trusted-peer headers. The config loader already fills
	// these in, so the constants only matter for a zero-value ProxyEntryConfig.
	defaultProxyRouteHeader      = "X-TunnelMesh-Route"
	defaultProxyClientIPHeader   = "X-TunnelMesh-Client-IP"
	defaultProxyClientPortHeader = "X-TunnelMesh-Client-Port"

	// defaultConnectPort is used when a CONNECT authority carries no port.
	// Browsers always send one, so this only matters for hand-written clients.
	defaultConnectPort = 443
	// defaultProxyConnectTimeout bounds the agent-side dial when the config
	// leaves it at zero.
	defaultProxyConnectTimeout = 10 * time.Second
	// maxWatchdogInterval keeps the idle poll cheap on long-lived tunnels.
	maxWatchdogInterval = 15 * time.Second
	// minWatchdogInterval avoids a zero or negative ticker interval, which would
	// panic, when idle_timeout is configured very small.
	minWatchdogInterval = 10 * time.Millisecond

	proxyEntryModeConnect  = "connect"
	proxyEntryModeAbsolute = "absolute"

	proxyEntryResultSuccess = "success"
	proxyEntryResultDenied  = "denied"

	// proxyEntryComponent labels the shared byte counter so proxy traffic can be
	// separated from agent/client/webssh traffic on one dashboard.
	proxyEntryComponent = "proxy_entry"

	proxyEntryAuditResourceType = "proxy_route"
)

// errProxyEntryInternal is the fallback for faults with no stable public code.
// It is deliberately vague: an internal failure must not tell the caller which
// part of the pipeline broke.
var errProxyEntryInternal = proxyentry.NewError(http.StatusInternalServerError, "proxy_internal_error", "proxy entry could not serve the request")

// ProxyEntry is the policy engine behind the internal listener. OpenResty only
// moves bytes and injects trusted headers; every authorization decision and
// every byte of tunnel state lives here so it can be unit tested and so there
// is exactly one authoritative implementation.
type ProxyEntry struct {
	config           config.ProxyEntryConfig
	routeHeader      string
	clientIPHeader   string
	clientPortHeader string
	identity         proxyentry.RouteIdentity
	routes           proxyentry.RouteSource
	opener           relay.NodeTransport
	authenticator    *proxyentry.Authenticator
	metrics          *observability.Metrics
	audits           storage.AuditRepository

	mu            sync.Mutex
	activeTotal   int
	activeByRoute map[string]int
}

// NewProxyEntry wires the entry from the already-validated proxy_entry config.
// A nil secret resolver is replaced with one that always reports the store as
// unavailable, so a route configured for Basic auth degrades to a 503 instead
// of panicking on a nil interface.
func NewProxyEntry(cfg config.ProxyEntryConfig, routes proxyentry.RouteSource, opener relay.NodeTransport, secrets proxyentry.SecretResolver, metrics *observability.Metrics, audits storage.AuditRepository) *ProxyEntry {
	if secrets == nil {
		secrets = unavailableSecrets{}
	}
	return &ProxyEntry{
		config:           cfg,
		routeHeader:      orDefault(cfg.RouteHeader, defaultProxyRouteHeader),
		clientIPHeader:   orDefault(cfg.ClientIPHeader, defaultProxyClientIPHeader),
		clientPortHeader: orDefault(cfg.ClientPortHeader, defaultProxyClientPortHeader),
		identity:         proxyentry.NewHeaderRouteIdentity(orDefault(cfg.RouteHeader, defaultProxyRouteHeader), cfg.DomainSuffix),
		routes:           routes,
		opener:           opener,
		authenticator:    proxyentry.NewAuthenticator(secrets, cfg.AuthBackoffThreshold),
		metrics:          metrics,
		audits:           audits,
		activeByRoute:    map[string]int{},
	}
}

func orDefault(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

// credentialSecretResolver adapts CredentialService to the proxyentry contract.
//
// It calls the ownership-free ResolveProxyBasicSecret on purpose: the entry
// authenticates the holder of a Proxy-Authorization header, who is not a logged
// in TunnelMesh user and therefore has no principal to compare against. The
// administrator already authorized the pairing when the credential was attached
// to the route.
//
// A store failure is translated into the proxyentry sentinel so the caller
// answers 503 without counting the attempt against the client's brute-force
// backoff. Every other failure stays opaque and is treated as bad credentials,
// which keeps credential IDs unprobeable.
type credentialSecretResolver struct{ credentials *CredentialService }

func (r credentialSecretResolver) ProxyBasicSecret(ctx context.Context, credentialID string) (proxyentry.CredentialSecret, error) {
	if r.credentials == nil {
		return proxyentry.CredentialSecret{}, proxyentry.ErrSecretStoreUnavailable
	}
	username, password, err := r.credentials.ResolveProxyBasicSecret(ctx, credentialID)
	if err != nil {
		if errors.Is(err, ErrCredentialSecretUnavailable) {
			return proxyentry.CredentialSecret{}, proxyentry.ErrSecretStoreUnavailable
		}
		return proxyentry.CredentialSecret{}, err
	}
	return proxyentry.CredentialSecret{Username: username, Password: password}, nil
}

var _ proxyentry.SecretResolver = credentialSecretResolver{}

// unavailableSecrets fails every lookup. It exists so the Authenticator always
// has a resolver to call.
type unavailableSecrets struct{}

func (unavailableSecrets) ProxyBasicSecret(context.Context, string) (proxyentry.CredentialSecret, error) {
	return proxyentry.CredentialSecret{}, proxyentry.ErrSecretStoreUnavailable
}

// Handler exposes the entry as an http.Handler for the internal listener.
func (p *ProxyEntry) Handler() http.Handler { return p }

// ServeHTTP applies the decision chain. The order is load-bearing: identity
// before route lookup, route before ACL, ACL before auth, auth before capacity.
// Every rejection records its metric before the response is written, so a
// client that never reads the body still shows up in the dashboard.
func (p *ProxyEntry) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	mode := proxyEntryModeAbsolute
	if r.Method == http.MethodConnect {
		mode = proxyEntryModeConnect
	}
	clientIP, ipErr := proxyentry.ParseClientIP(r.Header.Get(p.clientIPHeader))
	key, routeErr := p.identity.Resolve(r)
	if ipErr != nil || routeErr != nil {
		// The reason is logged but the response is the same generic 403 as every
		// other identity failure, so the entry cannot be used to discover which
		// header the front end forgot to inject.
		slog.WarnContext(r.Context(), "proxy_entry_identity_invalid",
			"mode", mode, "peer", r.RemoteAddr, "client_ip_error", ipErr, "route_error", routeErr)
		p.deny(w, r, "", mode, started, proxyentry.ErrRouteIdentityInvalid, "identity")
		return
	}
	routeName := string(key)
	if p.routes == nil {
		p.deny(w, r, routeName, mode, started, proxyentry.ErrRouteUnavailable, "route_unavailable")
		return
	}
	route, ok := p.routes.ProxyRoute(r.Context(), routeName)
	if !ok || !route.Active() {
		// Unknown and disabled routes answer identically: a differing response
		// would turn the entry into a tp-* name oracle.
		p.deny(w, r, routeName, mode, started, proxyentry.ErrRouteUnavailable, "route_unavailable")
		return
	}
	acl, err := proxyentry.NewSourceACL(route.SourceCIDRs)
	if err != nil {
		// A stored CIDR that no longer parses is a configuration fault. Deny and
		// log it; silently falling back to an open ACL would turn a typo into an
		// exposure.
		slog.ErrorContext(r.Context(), "proxy_entry_source_acl_invalid", "route", routeName, "error", err)
		acl = proxyentry.DenyAllSourceACL()
	}
	if !acl.Allow(clientIP) {
		p.observeACLDenied(routeName)
		p.audit(r.Context(), "proxy_route_denied", route, proxyEntryAuditDetail{
			RouteDomain: routeName, AgentID: route.AgentID, ClientIP: clientIP.String(), Reason: "source_acl",
		})
		p.deny(w, r, routeName, mode, started, proxyentry.ErrSourceDenied, "source_acl")
		return
	}
	if err := p.authenticator.Authorize(r.Context(), route, clientIP, r.Header.Get("Proxy-Authorization")); err != nil {
		code := proxyEntryErrorCode(err)
		p.observeAuthFailure(routeName, code)
		p.audit(r.Context(), "proxy_auth_failed", route, proxyEntryAuditDetail{
			RouteDomain: routeName, AgentID: route.AgentID, ClientIP: clientIP.String(), Reason: code,
		})
		p.deny(w, r, routeName, mode, started, err, "auth")
		return
	}
	release, err := p.reserve(route)
	if err != nil {
		p.deny(w, r, routeName, mode, started, err, "capacity")
		return
	}
	// The reservation is held for the whole request, including the tunnel, so
	// ServeHTTP does not return until the splice is finished.
	defer release()
	if r.Method == http.MethodConnect {
		p.handleConnect(w, r, route, clientIP, mode, started)
		return
	}
	p.handleAbsoluteForm(w, r, route, clientIP, mode, started)
}

// ActiveTunnels reports the number of reserved slots across all routes. It
// counts reservations rather than established tunnels so the value is stable
// while a request is between the capacity check and the agent dial.
func (p *ProxyEntry) ActiveTunnels() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.activeTotal
}

// reserve takes one concurrency slot for a route. The returned release is
// idempotent, so a deferred call is safe on every error path.
func (p *ProxyEntry) reserve(route proxyentry.Route) (func(), error) {
	p.mu.Lock()
	if p.config.MaxConcurrentTunnels > 0 && p.activeTotal >= p.config.MaxConcurrentTunnels {
		p.mu.Unlock()
		return nil, proxyentry.ErrCapacityExhausted
	}
	if route.MaxConcurrentTunnels > 0 && p.activeByRoute[route.ID] >= route.MaxConcurrentTunnels {
		p.mu.Unlock()
		return nil, proxyentry.ErrCapacityExhausted
	}
	p.activeTotal++
	p.activeByRoute[route.ID]++
	p.mu.Unlock()
	// The gauge is driven by reserve/release rather than by tunnel open/close so
	// the increment and the decrement cannot drift apart on an error path.
	p.observeTunnel(route.Domain, true)

	routeID, domain := route.ID, route.Domain
	var once sync.Once
	return func() {
		once.Do(func() {
			p.mu.Lock()
			p.activeTotal--
			if p.activeByRoute[routeID]--; p.activeByRoute[routeID] <= 0 {
				delete(p.activeByRoute, routeID)
			}
			p.mu.Unlock()
			p.observeTunnel(domain, false)
		})
	}, nil
}

// handleConnect establishes one TCP tunnel through the route's agent.
func (p *ProxyEntry) handleConnect(w http.ResponseWriter, r *http.Request, route proxyentry.Route, clientIP net.IP, mode string, started time.Time) {
	routeName := route.Domain
	host, port, err := splitConnectTarget(r)
	if err != nil {
		p.deny(w, r, routeName, mode, started, proxyentry.ErrTargetInvalid, "target")
		return
	}
	target := net.JoinHostPort(host, strconv.Itoa(port))
	policy, err := proxyentry.NewTargetPolicy(route.TargetCIDRs, route.TargetPorts, route.AllowPrivateTargets)
	if err != nil {
		slog.ErrorContext(r.Context(), "proxy_entry_target_policy_invalid", "route", routeName, "error", err)
		p.deny(w, r, routeName, mode, started, proxyentry.ErrTargetDenied, "target")
		return
	}
	if err := policy.Validate(host, port); err != nil {
		p.deny(w, r, routeName, mode, started, targetError(err), "target")
		return
	}

	request := relay.StreamRequest{AgentID: route.AgentID, Protocol: "tcp", TargetHost: host, TargetPort: port}
	request.Metadata = traceparentFromRequest(r)
	stream, cancelOpen, err := p.openEgress(r.Context(), request)
	if err != nil {
		perr := egressError(err)
		p.audit(r.Context(), "proxy_egress_failed", route, proxyEntryAuditDetail{
			RouteDomain: routeName, AgentID: route.AgentID, ClientIP: clientIP.String(), Target: target,
			Reason: proxyEntryErrorCode(perr),
		})
		p.deny(w, r, routeName, mode, started, perr, "egress")
		return
	}
	// cancelOpen must outlive the dial: a gRPC-backed transport binds the stream
	// to the context it was created with, so cancelling early would tear down a
	// healthy tunnel.
	defer cancelOpen()
	defer stream.Close()

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		p.deny(w, r, routeName, mode, started, errProxyEntryInternal, "internal")
		return
	}
	conn, buf, err := hijacker.Hijack()
	if err != nil {
		slog.ErrorContext(r.Context(), "proxy_entry_hijack_failed", "route", routeName, "error", err)
		p.deny(w, r, routeName, mode, started, errProxyEntryInternal, "internal")
		return
	}
	defer conn.Close()
	if err := writeConnectEstablished(buf, stream); err != nil {
		// The response writer is already hijacked, so there is nothing left to
		// answer with; the only useful signal is the log line.
		slog.WarnContext(r.Context(), "proxy_entry_handshake_failed", "route", routeName, "target", target, "error", err)
		return
	}

	p.observeRequest(routeName, mode, proxyEntryResultSuccess, "")
	p.audit(r.Context(), "proxy_tunnel_opened", route, proxyEntryAuditDetail{
		RouteDomain: routeName, AgentID: route.AgentID, ClientIP: clientIP.String(), Target: target,
	})
	tunnelStarted := time.Now()
	fromClient, fromUpstream, reason := spliceWithIdleTimeout(conn, stream, p.config.IdleTimeout)
	elapsed := time.Since(tunnelStarted)
	p.observeTunnelDuration(routeName, reason, elapsed)
	p.observeTunnelBytes(fromClient, fromUpstream)
	p.audit(r.Context(), "proxy_tunnel_closed", route, proxyEntryAuditDetail{
		RouteDomain: routeName, AgentID: route.AgentID, ClientIP: clientIP.String(), Target: target,
		BytesUp: fromClient, BytesDown: fromUpstream, DurationMs: elapsed.Milliseconds(), Reason: reason,
	})
	slog.InfoContext(r.Context(), "proxy_tunnel_closed",
		"route", routeName, "agent_id", route.AgentID, "target", target, "client_ip", clientIP.String(),
		"reason", reason, "bytes_up", fromClient, "bytes_down", fromUpstream,
		"setup_ms", tunnelStarted.Sub(started).Milliseconds(), "duration_ms", elapsed.Milliseconds())
}

// handleAbsoluteForm forwards a non-CONNECT proxy request over one logical
// stream. Field for field it mirrors HTTPProxyHandler.ServeRoute: write the
// normalized request into the stream, read exactly one response back, stream it
// out. No http.Client is involved, so there is no connection pool and no
// redirect following -- a 3xx is passed through verbatim and the browser decides
// whether to come back through the proxy.
//
// Protocol upgrades are deliberately unsupported here. A WebSocket target
// belongs on a managed reverse-proxy route, and leaving Upgrade in place would
// make http.ReadResponse expect a 101 this branch cannot splice.
func (p *ProxyEntry) handleAbsoluteForm(w http.ResponseWriter, r *http.Request, route proxyentry.Route, clientIP net.IP, mode string, started time.Time) {
	routeName := route.Domain
	host, port, scheme, err := splitProxyTarget(r)
	if err != nil {
		p.deny(w, r, routeName, mode, started, targetError(err), "target")
		return
	}
	policy, err := proxyentry.NewTargetPolicy(route.TargetCIDRs, route.TargetPorts, route.AllowPrivateTargets)
	if err != nil {
		slog.ErrorContext(r.Context(), "proxy_entry_target_policy_invalid", "route", routeName, "error", err)
		p.deny(w, r, routeName, mode, started, proxyentry.ErrTargetDenied, "target")
		return
	}
	if err := policy.Validate(host, port); err != nil {
		p.deny(w, r, routeName, mode, started, targetError(err), "target")
		return
	}
	target := net.JoinHostPort(host, strconv.Itoa(port))
	request := relay.StreamRequest{
		AgentID: route.AgentID, Protocol: "http",
		TargetHost: host, TargetPort: port, TargetScheme: scheme, HostHeader: host,
	}
	request.Metadata = traceparentFromRequest(r)
	stream, cancelOpen, err := p.openEgress(r.Context(), request)
	if err != nil {
		perr := egressError(err)
		p.audit(r.Context(), "proxy_egress_failed", route, proxyEntryAuditDetail{
			RouteDomain: routeName, AgentID: route.AgentID, ClientIP: clientIP.String(), Target: target,
			Reason: proxyEntryErrorCode(perr),
		})
		p.deny(w, r, routeName, mode, started, perr, "egress")
		return
	}
	defer cancelOpen()
	defer stream.Close()

	upstreamReq := p.normalizeProxyEntryRequest(r, host, port)
	if err := upstreamReq.Write(stream); err != nil {
		slog.WarnContext(r.Context(), "proxy_entry_upstream_write_failed", "route", routeName, "target", target, "error", err)
		p.deny(w, r, routeName, mode, started, proxyentry.ErrEgressUnavailable, "egress_write")
		return
	}
	resp, err := http.ReadResponse(bufio.NewReader(stream), upstreamReq)
	if err != nil {
		slog.WarnContext(r.Context(), "proxy_entry_upstream_read_failed", "route", routeName, "target", target, "error", err)
		p.deny(w, r, routeName, mode, started, proxyentry.ErrEgressUnavailable, "egress_read")
		return
	}
	defer resp.Body.Close()

	p.observeRequest(routeName, mode, proxyEntryResultSuccess, "")
	elapsed := time.Since(started)
	p.audit(r.Context(), "proxy_request_forwarded", route, proxyEntryAuditDetail{
		RouteDomain: routeName, AgentID: route.AgentID, ClientIP: clientIP.String(), Target: target,
		DurationMs: elapsed.Milliseconds(), Status: resp.StatusCode,
	})
	_ = copyResponse(r.Context(), w, resp)
	slog.InfoContext(r.Context(), "proxy_request_forwarded",
		"route", routeName, "agent_id", route.AgentID, "target", target, "client_ip", clientIP.String(),
		"method", r.Method, "status", resp.StatusCode, "duration_ms", elapsed.Milliseconds())
}

// splitProxyTarget resolves the real target of a non-CONNECT proxy request. It
// accepts both shapes the entry can see: an absolute-form URL (a direct hit on
// the internal listener, which is what tests and curl produce) and the
// origin-form rewrite OpenResty's "location /" emits, where the authority
// survives only in the Host header.
func splitProxyTarget(r *http.Request) (string, int, string, error) {
	if r == nil || r.URL == nil {
		return "", 0, "", proxyentry.ErrTargetInvalid
	}
	target, scheme := r.URL, r.URL.Scheme
	if target.Host == "" {
		// Origin-form. Defaulting the scheme to http is not a guess: an https
		// target always reaches this entry as CONNECT and is served by
		// handleConnect, so anything arriving in origin-form is plaintext.
		authority := strings.TrimSpace(r.Host)
		if authority == "" {
			return "", 0, "", proxyentry.ErrTargetInvalid
		}
		parsed, err := url.Parse("http://" + authority)
		if err != nil {
			return "", 0, "", proxyentry.ErrTargetInvalid
		}
		target, scheme = parsed, "http"
	}
	if scheme != "http" && scheme != "https" {
		return "", 0, "", proxyentry.ErrTargetInvalid
	}
	host := target.Hostname()
	portText := target.Port()
	if portText == "" {
		if scheme == "https" {
			portText = "443"
		} else {
			portText = "80"
		}
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 || host == "" {
		return "", 0, "", proxyentry.ErrTargetInvalid
	}
	return host, port, scheme, nil
}

// normalizeProxyEntryRequest rewrites an absolute-form proxy request into the
// origin-form the target expects and strips hop-by-hop plus trusted-peer
// headers. It mirrors internal/client.normalizeProxyRequest and additionally
// removes the X-TunnelMesh-* headers so route metadata never reaches a target.
func (p *ProxyEntry) normalizeProxyEntryRequest(r *http.Request, host string, port int) *http.Request {
	upstream := r.Clone(r.Context())
	upstream.RequestURI = ""
	upstream.URL.Scheme = ""
	upstream.URL.Host = ""
	upstream.Host = net.JoinHostPort(host, strconv.Itoa(port))
	// Hop-by-hop headers are meaningful only between the client and this entry.
	for _, name := range []string{
		"Proxy-Authorization", "Proxy-Connection", "Connection", "Keep-Alive",
		"Te", "Trailer", "Transfer-Encoding", "Upgrade",
	} {
		upstream.Header.Del(name)
	}
	// Map keys are already canonical, so one prefix sweep covers every
	// X-TunnelMesh-* header without enumerating them.
	for key := range upstream.Header {
		if strings.HasPrefix(key, "X-Tunnelmesh-") {
			upstream.Header.Del(key)
		}
	}
	// The prefix sweep misses operator-renamed headers, whose canonical form may
	// not start with X-Tunnelmesh-. Deleting the configured names explicitly
	// closes that gap; Del is a no-op when the name is absent.
	upstream.Header.Del(p.routeHeader)
	upstream.Header.Del(p.clientIPHeader)
	upstream.Header.Del(p.clientPortHeader)
	return upstream
}

// traceparentFromRequest extracts the caller's W3C trace context. Metadata is
// the only field the stream-open payload carries for cross-process correlation,
// so the value rides along and the agent-side connect joins the same trace. It
// returns nil when the front end did not forward one.
func traceparentFromRequest(r *http.Request) []byte {
	value := strings.TrimSpace(r.Header.Get("Traceparent"))
	if value == "" {
		return nil
	}
	return []byte(value)
}

// targetError normalizes a target-validation failure. proxyentry already returns
// the two stable codes; anything unexpected is treated as a policy denial
// rather than an internal fault, so a target can never be reached by accident
// because a mapping was missing.
func targetError(err error) error {
	if perr := asProxyEntryError(err); perr != nil {
		return perr
	}
	return proxyentry.ErrTargetDenied
}

// egressError normalizes a stream failure onto the two stable egress codes, so
// the CONNECT and absolute-form branches cannot drift apart.
func egressError(err error) error {
	if perr := asProxyEntryError(err); perr != nil {
		return perr
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return proxyentry.ErrEgressTimeout
	}
	return proxyentry.ErrEgressUnavailable
}

// splitConnectTarget parses the CONNECT authority. The Host header is preferred
// because that is what OpenResty forwards verbatim; the request-target is the
// fallback for a relay that rewrites Host.
func splitConnectTarget(r *http.Request) (string, int, error) {
	authority := strings.TrimSpace(r.Host)
	if authority == "" && r.URL != nil {
		authority = strings.TrimSpace(r.URL.Host)
	}
	if authority == "" {
		return "", 0, errors.New("connect authority is empty")
	}
	host, portText, err := net.SplitHostPort(authority)
	if err != nil {
		// An authority without a port is legal in practice; default to the TLS
		// port rather than rejecting a working client.
		host, portText = authority, strconv.Itoa(defaultConnectPort)
	}
	if strings.TrimSpace(host) == "" {
		return "", 0, errors.New("connect authority has no host")
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return "", 0, err
	}
	return host, port, nil
}

// openEgress dials the agent-side stream described by request and enforces the
// connect timeout with a timer instead of a deadline context. Both the CONNECT
// and absolute-form branches go through it so the timeout, the abandonment
// behaviour and the error mapping cannot drift apart; the caller supplies the
// fully populated StreamRequest, including Protocol and any trace metadata.
//
// A deadline context cannot be used here: relay.GRPCNodeTransport creates the
// client stream from the context it is given, so a deadline that fires after a
// successful open would kill a healthy tunnel. The caller owns the returned
// cancel func and must defer it for the tunnel's whole lifetime.
func (p *ProxyEntry) openEgress(ctx context.Context, request relay.StreamRequest) (io.ReadWriteCloser, context.CancelFunc, error) {
	if p.opener == nil {
		return nil, func() {}, proxyentry.ErrEgressUnavailable
	}
	target := net.JoinHostPort(request.TargetHost, strconv.Itoa(request.TargetPort))
	openCtx, cancelOpen := context.WithCancel(ctx)
	type openResult struct {
		stream io.ReadWriteCloser
		err    error
	}
	results := make(chan openResult, 1)
	go func() {
		stream, err := p.opener.OpenStream(openCtx, request)
		results <- openResult{stream: stream, err: err}
	}()
	// abandon cancels the dial and reaps a stream that arrives late, so neither
	// the goroutine nor an opened agent-side connection leaks.
	abandon := func() {
		cancelOpen()
		go func() {
			if res := <-results; res.stream != nil {
				_ = res.stream.Close()
			}
		}()
	}

	timeout := p.config.ConnectTimeout
	if timeout <= 0 {
		timeout = defaultProxyConnectTimeout
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case res := <-results:
		if res.err != nil {
			cancelOpen()
			slog.WarnContext(ctx, "proxy_entry_egress_open_failed",
				"agent_id", request.AgentID, "protocol", request.Protocol, "target", target, "error", res.err)
			return nil, func() {}, proxyentry.ErrEgressUnavailable
		}
		return res.stream, cancelOpen, nil
	case <-timer.C:
		abandon()
		slog.WarnContext(ctx, "proxy_entry_egress_open_timeout",
			"agent_id", request.AgentID, "protocol", request.Protocol, "target", target, "timeout", timeout.String())
		return nil, func() {}, proxyentry.ErrEgressTimeout
	case <-ctx.Done():
		// The client hung up while the dial was still in flight.
		abandon()
		return nil, func() {}, errProxyEntryInternal
	}
}

// writeConnectEstablished sends the 200 that turns the connection into a tunnel
// and forwards any bytes the client pipelined behind the CONNECT.
func writeConnectEstablished(buf *bufio.ReadWriter, stream io.Writer) error {
	if _, err := buf.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return err
	}
	if err := buf.Flush(); err != nil {
		return err
	}
	// net/http may have read tunnel bytes into the buffered reader returned by
	// Hijack. They belong to the tunnel, so they are forwarded before the splice
	// takes over, mirroring the WebSocket upgrade path in http_proxy.go.
	if buffered := buf.Reader.Buffered(); buffered > 0 {
		if _, err := io.CopyN(stream, buf.Reader, int64(buffered)); err != nil {
			return err
		}
	}
	return nil
}

// deny records the metric, writes one log line and renders the stable error
// envelope. It never logs header values: Proxy-Authorization travels on exactly
// these requests.
func (p *ProxyEntry) deny(w http.ResponseWriter, r *http.Request, routeName, mode string, started time.Time, err error, errorClass string) {
	perr := asProxyEntryError(err)
	if perr == nil {
		perr, errorClass = errProxyEntryInternal, "internal"
	}
	p.observeRequest(routeName, mode, proxyEntryResultDenied, errorClass)
	slog.WarnContext(r.Context(), "proxy_entry_denied",
		"route", routeName, "mode", mode, "status", perr.Status, "code", perr.Code,
		"error_class", errorClass, "peer", r.RemoteAddr, "target", strings.TrimSpace(r.Host),
		"duration_ms", time.Since(started).Milliseconds())
	writeProxyEntryError(w, perr)
}

// writeProxyEntryError renders the project's {code,msg,data} envelope with the
// stable proxyentry error code in data.error, so one client-side parser covers
// both the management API and the proxy entry.
func writeProxyEntryError(w http.ResponseWriter, perr *proxyentry.Error) {
	for key, value := range perr.HTTPHeaders() {
		w.Header().Set(key, value)
	}
	// A denied request never becomes a tunnel, so there is nothing to keep the
	// connection for. Closing also stops a credential-probing loop from parking
	// sockets on the entry; 407 retries happen on a fresh connection anyway.
	w.Header().Set("Connection", "close")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	body, err := json.Marshal(map[string]any{
		"code": perr.Status,
		"msg":  perr.Error(),
		"data": map[string]any{"error": perr.Code},
	})
	if err != nil {
		body = []byte(`{"code":500,"msg":"proxy entry error","data":null}`)
	}
	w.WriteHeader(perr.Status)
	_, _ = w.Write(body)
	_, _ = w.Write([]byte("\n"))
}

// proxyEntryErrorCode extracts the stable code used as a metric label and audit
// reason. Only the fixed proxyentry code set can appear, which is what keeps
// those labels low-cardinality.
func proxyEntryErrorCode(err error) string {
	if perr := asProxyEntryError(err); perr != nil {
		return perr.Code
	}
	return errProxyEntryInternal.Code
}

func asProxyEntryError(err error) *proxyentry.Error {
	var perr *proxyentry.Error
	if errors.As(err, &perr) {
		return perr
	}
	return nil
}

// proxyEntryAuditDetail is the JSON stored in audit_logs.details for proxy
// events. It carries routing facts only: no credentials, no header values and
// no tunnel payload.
type proxyEntryAuditDetail struct {
	RouteDomain string `json:"routeDomain"`
	AgentID     string `json:"agentId,omitempty"`
	ClientIP    string `json:"clientIp,omitempty"`
	Target      string `json:"target,omitempty"`
	BytesUp     int64  `json:"bytesUp,omitempty"`
	BytesDown   int64  `json:"bytesDown,omitempty"`
	DurationMs  int64  `json:"durationMs,omitempty"`
	Status      int    `json:"status,omitempty"`
	Reason      string `json:"reason,omitempty"`
}

func (p *ProxyEntry) audit(ctx context.Context, action string, route proxyentry.Route, detail proxyEntryAuditDetail) {
	if p == nil || p.audits == nil {
		return
	}
	encoded, err := json.Marshal(detail)
	if err != nil {
		encoded = []byte("{}")
	}
	if err := p.audits.Create(ctx, storage.AuditLog{
		Action: action, ResourceType: proxyEntryAuditResourceType, ResourceID: route.ID, Details: string(encoded),
	}); err != nil {
		// An audit failure must never break a tunnel or a denial response; the
		// gap is visible here instead.
		slog.WarnContext(ctx, "proxy_entry_audit_failed", "action", action, "route", route.ID, "error", err)
	}
}

func (p *ProxyEntry) observeRequest(route, mode, result, errorClass string) {
	if p.metrics != nil {
		p.metrics.ObserveProxyEntryRequest(route, mode, result, errorClass)
	}
}

func (p *ProxyEntry) observeTunnel(route string, active bool) {
	if p.metrics != nil {
		p.metrics.ObserveProxyEntryTunnel(route, active)
	}
}

func (p *ProxyEntry) observeTunnelDuration(route, result string, duration time.Duration) {
	if p.metrics != nil {
		p.metrics.ObserveProxyEntryTunnelDuration(route, result, duration)
	}
}

func (p *ProxyEntry) observeAuthFailure(route, reason string) {
	if p.metrics != nil {
		p.metrics.ObserveProxyEntryAuthFailure(route, reason)
	}
}

func (p *ProxyEntry) observeACLDenied(route string) {
	if p.metrics != nil {
		p.metrics.ObserveProxyEntryACLDenied(route)
	}
}

// observeTunnelBytes reuses the shared byte counter with the same
// upload/download directions the WebSSH broker uses, so one panel can compare
// the two interactive traffic classes.
func (p *ProxyEntry) observeTunnelBytes(fromClient, fromUpstream int64) {
	if p.metrics == nil {
		return
	}
	p.metrics.ObserveBytes(proxyEntryComponent, "upload", "tcp", fromClient)
	p.metrics.ObserveBytes(proxyEntryComponent, "download", "tcp", fromUpstream)
}

// activityReader refreshes one shared last-activity timestamp on every
// successful read. Both directions write to the same atomic, so traffic in
// either direction counts as "not idle".
type activityReader struct {
	reader io.Reader
	last   *atomic.Int64
}

func (a *activityReader) Read(buffer []byte) (int, error) {
	n, err := a.reader.Read(buffer)
	if n > 0 {
		a.last.Store(time.Now().UnixNano())
	}
	return n, err
}

// spliceWithIdleTimeout copies both directions and closes the pair once neither
// side has produced bytes for the whole timeout. The mesh stream ignores
// SetDeadline, so the watchdog owns idle detection.
//
// reason is one of "success", "timeout" and "error". It labels
// tunnelmesh_proxy_entry_tunnel_duration_seconds and the Grafana panel groups
// by it, so those three values are the complete enum: an agent that keeps
// dropping mid-tunnel stays visible instead of being averaged into one success
// bucket.
func spliceWithIdleTimeout(down net.Conn, up io.ReadWriteCloser, idle time.Duration) (fromClient, fromUpstream int64, reason string) {
	var last atomic.Int64
	last.Store(time.Now().UnixNano())
	var idleFired atomic.Bool
	done := make(chan struct{})
	var once sync.Once
	closeAll := func() {
		once.Do(func() {
			// Expiring the deadline unblocks the client-side copy; closing the
			// stream unblocks the upstream-side one.
			_ = down.SetDeadline(time.Now())
			_ = up.Close()
			close(done)
		})
	}

	// Byte counts travel back over a buffered channel rather than through the
	// named return values: closeAll only guarantees that one side finished, so
	// the other may still be inside io.Copy. Writing the named results from
	// those goroutines while the caller reads them is a data race.
	type copyResult struct {
		toUpstream bool
		n          int64
		err        error
	}
	results := make(chan copyResult, 2)

	go func() {
		n, err := io.Copy(up, &activityReader{reader: down, last: &last})
		results <- copyResult{toUpstream: true, n: n, err: err}
		closeAll()
	}()
	go func() {
		n, err := io.Copy(down, &activityReader{reader: up, last: &last})
		results <- copyResult{toUpstream: false, n: n, err: err}
		closeAll()
	}()
	if idle > 0 {
		go func() {
			interval := idle / 4
			if interval > maxWatchdogInterval {
				interval = maxWatchdogInterval
			}
			if interval < minWatchdogInterval {
				interval = minWatchdogInterval
			}
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for {
				select {
				case <-done:
					return
				case <-ticker.C:
					if time.Since(time.Unix(0, last.Load())) >= idle {
						idleFired.Store(true)
						closeAll()
						return
					}
				}
			}
		}()
	}

	// Both directions are collected before returning. closeAll has already
	// expired the client deadline and closed the stream, so neither copy can
	// block the caller for long.
	errs := make([]error, 0, 2)
	for i := 0; i < 2; i++ {
		res := <-results
		if res.toUpstream {
			fromClient = res.n
		} else {
			fromUpstream = res.n
		}
		errs = append(errs, res.err)
	}
	return fromClient, fromUpstream, classifyTunnelResult(idleFired.Load(), errs...)
}

// classifyTunnelResult maps the idle watchdog flag and the two copy errors onto
// the low-cardinality result label. Anything that is the expected consequence
// of closeAll counts as a clean shutdown; only a real transport fault is an
// error. The watchdog wins because it already explains both copy failures.
func classifyTunnelResult(idleFired bool, errs ...error) string {
	if idleFired {
		return "timeout"
	}
	for _, err := range errs {
		switch {
		case err == nil,
			errors.Is(err, io.EOF),
			errors.Is(err, io.ErrClosedPipe),
			errors.Is(err, net.ErrClosed),
			errors.Is(err, os.ErrDeadlineExceeded):
		default:
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				continue
			}
			return "error"
		}
	}
	return "success"
}
