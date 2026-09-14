package server

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"golang.org/x/net/websocket"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"

	"github.com/tunnelmesh/tunnelmesh/internal/auth"
	"github.com/tunnelmesh/tunnelmesh/internal/config"
	"github.com/tunnelmesh/tunnelmesh/internal/observability"
	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
	"github.com/tunnelmesh/tunnelmesh/internal/registry"
	"github.com/tunnelmesh/tunnelmesh/internal/relay"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

var ErrRuntimeDatabaseRequired = errors.New("server runtime: database is required")

const (
	agentHelloTimeout              = time.Second
	agentWebSocketMaxPayloadBytes  = protocol.MaxPayload + 16
	clientWebSocketMaxPayloadBytes = protocol.MaxPayload + 16
)

type RuntimeConfig struct {
	Security        config.SecurityConfig
	TLS             config.TLSConfig
	Relay           config.RelayConfig
	NodeID          string
	DynamicSuffix   string
	MetricsRegistry *prometheus.Registry
	// ConnectionSelector and RemoteRelay allow deployments to provide their
	// authenticated Server-node transport pool. Defaults are local-only.
	ConnectionSelector relay.ConnectionSelector
	ConnectionRegistry registry.NodeRegistry
	RemoteRelay        *relay.RelayService
	Stream             config.ServerStreamConfig
	AuthorizationCache config.AuthorizationCacheConfig
	Downloads          config.DownloadsConfig
	WebSSH             config.WebSSHConfig
	// ProxyEntry configures the internal listener that OpenResty relays the
	// tp-* managed HTTP proxy traffic to. Disabled means the listener is never
	// created, so upgrading cannot open a new port by accident.
	ProxyEntry config.ProxyEntryConfig
}

// ServerRuntime is the process-scoped server wiring shared by HTTP handlers
// and authenticated Agent WebSocket handlers. The caller owns DB lifecycle.
type ServerRuntime struct {
	DB                          *storage.DB
	AgentSessions               *AgentSessionManager
	AgentConnectionLeases       *AgentConnectionLeaseController
	ClientSessions              *ClientSessionManager
	ClientConnectionLeases      *ClientConnectionLeaseController
	ClientObservability         *ClientObservabilityService
	API                         *API
	Auth                        *auth.AuthService
	Credentials                 *auth.CredentialService
	ClientAuthorizer            StreamAuthorizer
	ClientTransport             relay.NodeTransport
	ClientStreamService         *ClientStreamService
	LocalAgentRelay             *AgentRelayTransport
	WebSSHBroker                *WebSSHBroker
	managedRoutes               *ManagedRouteHandler
	serverNodeLifecycle         *ServerNodeLifecycle
	relayServer                 *grpc.Server
	relayListener               net.Listener
	proxyEntry                  *ProxyEntryListener
	proxyEntryService           *ProxyEntry
	clientMetadataSweeper       *ClientMetadataSweeper
	clientMetadataSweeperCancel context.CancelFunc
	clientMetadataSweeperDone   chan error
	websshSweeper               *WebSSHSweeper
	websshSweeperCancel         context.CancelFunc
	websshSweeperDone           chan error
	config                      RuntimeConfig
	WebSSHEnabled               bool
	ProxyEntryEnabled           bool
	metricsRegistry             *prometheus.Registry
	metrics                     *observability.Metrics
	authorizationCacheCleanup   func() error
	closed                      atomic.Bool
}

// NewServerRuntime creates the server runtime with durable Agent metadata
// persistence enabled. The CLI server run command and embedders should create
// one runtime at startup, then pass AgentSessions to ServeAgentSession.
func NewServerRuntime(db *storage.DB, cfg AgentSessionConfig, options ...RuntimeConfig) (*ServerRuntime, error) {
	if db == nil {
		return nil, ErrRuntimeDatabaseRequired
	}
	authService := auth.NewAuthService(db)
	credentialService := auth.NewCredentialService(db)
	var runtimeConfig RuntimeConfig
	if len(options) > 0 {
		runtimeConfig = options[0]
		runtimeConfig.Security.AllowedHosts = append([]string(nil), runtimeConfig.Security.AllowedHosts...)
		runtimeConfig.Security.AllowedOrigins = append([]string(nil), runtimeConfig.Security.AllowedOrigins...)
	}
	cfg.ServerNodeID = runtimeConfig.NodeID
	agentSessions := NewAgentSessionManagerWithMetadata(db.Metadata(), cfg)
	localAgentRelay := NewAgentRelayTransport(agentSessions)
	serverNodeID := runtimeConfig.NodeID
	if strings.TrimSpace(serverNodeID) == "" {
		serverNodeID = "local"
	}
	connectionRegistry := runtimeConfig.ConnectionRegistry
	if connectionRegistry == nil {
		connectionRegistry = registry.NewDatabaseRegistry(db)
	}
	connectionSelector := runtimeConfig.ConnectionSelector
	if connectionSelector == nil {
		connectionSelector = NewAgentConnectionSelector(agentSessions, localAgentRelay, connectionRegistry, serverNodeID)
	}
	agentConnectionLeases := NewAgentConnectionLeaseController(connectionRegistry, agentSessions, localAgentRelay, serverNodeID, runtimeConfig.Relay.Endpoint, defaultAgentConnectionLeaseTTL)
	agentSessions.SetHeartbeatCallback(agentConnectionLeases.HeartbeatForSession)
	clientSessions := NewClientSessionManager()
	clientConnectionLeases := NewClientConnectionLeaseController(db.ClientConnections(), clientSessions, serverNodeID, DefaultClientConnectionLeaseTTL)
	clientObservability := NewClientObservabilityService(db.ClientInstances(), clientConnectionLeases, clientSessions, serverNodeID, DefaultClientMetadataTTL)
	clientTransport := relay.NodeTransport(localAgentRelay)
	if _, ok := connectionSelector.(*AgentConnectionSelector); !ok || runtimeConfig.RemoteRelay != nil {
		clientTransport = relay.NewSelectedTransport(connectionSelector, localAgentRelay, runtimeConfig.RemoteRelay)
	}
	routeTable := NewManagedRouteTable(db, runtimeConfig.DynamicSuffix, managedRouteCacheTTL)
	managedRoutes := NewManagedRouteHandler(routeTable, &HTTPProxyHandler{Opener: clientTransport})
	runtime := &ServerRuntime{DB: db, AgentSessions: agentSessions, AgentConnectionLeases: agentConnectionLeases, ClientSessions: clientSessions, ClientConnectionLeases: clientConnectionLeases, ClientObservability: clientObservability, API: NewAPI(db, authService), Auth: authService, Credentials: credentialService, ClientAuthorizer: NewCredentialStreamAuthorizer(credentialService), ClientTransport: clientTransport, LocalAgentRelay: localAgentRelay, managedRoutes: managedRoutes, config: runtimeConfig}
	runtime.API.SetAgentConnections(agentSessions, localAgentRelay)
	runtime.API.SetWebSSHLocalNodeID(serverNodeID)
	runtime.API.SetDownloads(runtimeConfig.Downloads)
	var serverNodeLifecycle *ServerNodeLifecycle
	var authorizationCacheCleanup func() error
	closeStartup := func() {
		if authorizationCacheCleanup != nil {
			_ = authorizationCacheCleanup()
		}
		if serverNodeLifecycle != nil {
			_ = serverNodeLifecycle.Close()
		}
		_ = credentialService.Close()
	}
	if strings.TrimSpace(runtimeConfig.NodeID) != "" {
		serverNodeLifecycle = NewServerNodeLifecycle(db, runtimeConfig.NodeID, runtimeConfig.Relay.Endpoint, defaultServerNodeHeartbeatInterval, defaultServerNodeHeartbeatTTL)
		if err := serverNodeLifecycle.Start(context.TODO()); err != nil {
			closeStartup()
			return nil, fmt.Errorf("server runtime: register server node: %w", err)
		}
		runtime.serverNodeLifecycle = serverNodeLifecycle
	}
	dialRelayNode := func(ctx context.Context, endpoint string, epoch int64) (AgentConnectionRelayClient, error) {
		client, err := runtime.DialRelayNode(ctx, endpoint, epoch)
		if err != nil {
			return nil, err
		}
		return client, nil
	}
	// WebSSH uses the same authenticated transport as Agent/Client cluster
	// close controls. The broker is initialized here so relay control handlers
	// can always find the process-local owner registry.
	runtime.WebSSHEnabled = runtimeConfig.WebSSH.Enabled
	runtime.API.websshService.SetLimits(WebSSHSessionLimits{
		TicketTTL: runtimeConfig.WebSSH.TicketTTL, SessionTTL: runtimeConfig.WebSSH.SessionTTL,
		MaxActivePerUser: runtimeConfig.WebSSH.MaxActiveSessionsUser, OpenTimeout: runtimeConfig.WebSSH.OpenTimeout,
		IdleTimeout: runtimeConfig.WebSSH.IdleTimeout,
	})
	runtime.WebSSHBroker = NewWebSSHBroker(WebSSHBrokerDeps{
		Sessions:        runtime.API.websshService,
		Opener:          clientTransport,
		Upgrade:         newXNetWebSSHUpgrader(runtimeConfig.Security, runtimeConfig.WebSSH.MaxMessageBytes),
		Security:        runtimeConfig.Security,
		MaxMessageBytes: runtimeConfig.WebSSH.MaxMessageBytes,
	})
	webSSHDialRelayNode := func(ctx context.Context, endpoint string, epoch int64) (WebSSHConnectionRelayClient, error) {
		return runtime.DialRelayNode(ctx, endpoint, epoch)
	}
	webSSHConnectionCloser := NewClusterWebSSHConnectionCloseService(db.WebSSHSessions(), db.Nodes(), runtime.WebSSHBroker, serverNodeID, webSSHDialRelayNode)
	runtime.API.SetWebSSHConnectionCloser(webSSHConnectionCloser)
	connectionCloser := NewClusterAgentConnectionCloseService(connectionRegistry, agentConnectionLeases, serverNodeID, dialRelayNode)
	runtime.API.SetClusterAgentConnections(connectionRegistry, connectionCloser, serverNodeID)
	clientDialRelayNode := func(ctx context.Context, endpoint string, epoch int64) (ClientConnectionRelayClient, error) {
		return runtime.DialRelayNode(ctx, endpoint, epoch)
	}
	clientConnectionCloser := NewClusterClientConnectionCloseService(db.ClientConnections(), db.Nodes(), clientConnectionLeases, serverNodeID, clientDialRelayNode)
	runtime.API.SetClusterClientConnections(clientConnectionCloser)
	registry := runtimeConfig.MetricsRegistry
	if registry == nil {
		registry = prometheus.NewRegistry()
	}
	runtime.metricsRegistry = registry
	runtime.metrics = observability.NewMetrics(registry)
	runtime.WebSSHBroker.deps.Metrics = runtime.metrics
	runtime.API.websshService.SetMetrics(runtime.metrics)
	if runtimeConfig.WebSSH.Enabled {
		// Sessions persisted by a previous process of this node can never be
		// served again; close them before new tickets are issued so they do
		// not consume the per-user active-session quota.
		closed, err := runtime.API.websshService.CloseStaleLocalSessions(context.Background(), time.Now().UTC(), "node_restarted")
		if err != nil {
			return nil, fmt.Errorf("server runtime: close stale webssh sessions: %w", err)
		}
		if closed > 0 {
			slog.Info("webssh_stale_sessions_closed", "count", closed, "node_id", serverNodeID)
		}
	}
	if runtimeConfig.AuthorizationCache.Enabled {
		positiveTTL := runtimeConfig.AuthorizationCache.LocalPositiveTTL
		if db.Driver() == storage.DriverMySQL {
			positiveTTL = runtimeConfig.AuthorizationCache.ClusterPositiveTTL
		}
		cachedAuthorizer, cleanup, err := NewCachingStreamAuthorizer(runtime.ClientAuthorizer, db.AuthorizationRevisions(), AuthorizationCacheConfig{
			Enabled: true, PositiveTTL: positiveTTL, NegativeTTL: runtimeConfig.AuthorizationCache.NegativeTTL,
			RevisionPollInterval: runtimeConfig.AuthorizationCache.RevisionPollInterval,
			MaxStaleOnPollError:  runtimeConfig.AuthorizationCache.MaxStaleOnPollError,
			MaxEntries:           runtimeConfig.AuthorizationCache.MaxEntries,
		}, runtime.metrics)
		if err != nil {
			closeStartup()
			return nil, fmt.Errorf("server runtime: authorization cache: %w", err)
		}
		runtime.ClientAuthorizer = cachedAuthorizer
		runtime.authorizationCacheCleanup = cleanup
		authorizationCacheCleanup = cleanup
		if notifier, ok := cachedAuthorizer.(interface{ NotifyAuthorizationChange() }); ok {
			credentialService.SetAuthorizationChangeNotifier(notifier.NotifyAuthorizationChange)
		}
	}
	if selector, ok := connectionSelector.(*AgentConnectionSelector); ok {
		selector.SetMetrics(runtime.metrics)
	}
	runtime.ClientStreamService = NewClientStreamService(runtime.ClientAuthorizer, runtime.ClientTransport, ClientStreamServiceConfig{
		MaxConcurrentOpens: runtimeConfig.Stream.MaxConcurrentOpens,
		MaxPendingOpens:    runtimeConfig.Stream.MaxPendingOpens,
		OpenTimeout:        clientOpenTimeoutFromConfig(runtimeConfig.Stream),
		Observability:      clientObservability,
	}, runtime.metrics)
	if runtimeConfig.Relay.Enabled {
		if strings.TrimSpace(runtimeConfig.NodeID) == "" {
			closeStartup()
			return nil, errors.New("server runtime: relay requires node identity")
		}
		tlsConfig, err := loadRelayServerTLS(runtimeConfig.Relay)
		if err != nil {
			closeStartup()
			return nil, err
		}
		listener, err := net.Listen("tcp", runtimeConfig.Relay.Listen)
		if err != nil {
			closeStartup()
			return nil, fmt.Errorf("server runtime: listen relay: %w", err)
		}
		runtime.relayListener = listener
		serverOptions := []grpc.ServerOption{
			grpc.ChainStreamInterceptor(relay.NewServerNodeStreamInterceptorWithMode(credentialService, db.Nodes(), runtime.metrics, tlsConfig != nil)),
			grpc.ChainUnaryInterceptor(relay.NewServerNodeUnaryInterceptorWithMode(credentialService, db.Nodes(), tlsConfig != nil)),
		}
		if tlsConfig != nil {
			serverOptions = append(serverOptions, grpc.Creds(credentials.NewTLS(tlsConfig)))
		}
		runtime.relayServer = grpc.NewServer(serverOptions...)
		relay.RegisterRelayServer(runtime.relayServer, relay.NewRelayServerWithControls(
			func(ctx context.Context, metadata relay.RelayOpenMetadata) (io.ReadWriteCloser, relay.RelayOpenResult, error) {
				return localAgentRelay.OpenStreamResult(ctx, metadata.Request)
			},
			localAgentRelay.OpenStream,
			runtime.closeAgentConnection,
			runtime.closeClientConnection,
			runtime.closeWebSSHConnection,
		))
	}
	runtime.Credentials = credentialService
	if runtimeConfig.ProxyEntry.Enabled {
		// Construction happens here rather than in ServeListener so an invalid
		// trusted_proxies value fails startup instead of silently leaving the
		// entry down while OpenResty keeps relaying into a dead port.
		// clientTransport is the agent-facing transport (local sessions plus the
		// cluster connection selector), so an egress agent attached to another
		// Server node is reached through the existing relay without extra wiring.
		entry := NewProxyEntry(runtimeConfig.ProxyEntry, routeTable, clientTransport,
			credentialSecretResolver{credentials: runtime.API.credentialService}, runtime.metrics, db.Audits())
		runtime.proxyEntryService = entry
		entryListener, err := NewProxyEntryListener(runtimeConfig.ProxyEntry, entry.Handler(), runtime.metrics)
		if err != nil {
			closeStartup()
			return nil, fmt.Errorf("server runtime: proxy entry: %w", err)
		}
		runtime.proxyEntry = entryListener
		runtime.ProxyEntryEnabled = true
		slog.Info("proxy_entry_enabled", "listen", runtimeConfig.ProxyEntry.Listen, "domain_suffix", runtimeConfig.ProxyEntry.DomainSuffix)
	}
	runtime.clientMetadataSweeper = NewClientMetadataSweeper(db.ClientInstances(), DefaultClientMetadataSweepInterval)
	sweeperCtx, stopSweeper := context.WithCancel(context.Background())
	runtime.clientMetadataSweeperCancel = stopSweeper
	runtime.clientMetadataSweeperDone = make(chan error, 1)
	go func() {
		runtime.clientMetadataSweeperDone <- runtime.clientMetadataSweeper.Run(sweeperCtx)
	}()
	runtime.websshSweeper = NewWebSSHSweeper(db.WebSSHSessions(), DefaultWebSSHSweepInterval)
	webSSHSweeperCtx, stopWebSSHSweeper := context.WithCancel(context.Background())
	runtime.websshSweeperCancel = stopWebSSHSweeper
	runtime.websshSweeperDone = make(chan error, 1)
	go func() {
		runtime.websshSweeperDone <- runtime.websshSweeper.Run(webSSHSweeperCtx)
	}()
	return runtime, nil
}

// Close releases runtime-owned background workers. The caller continues to
// own the database lifecycle.
func (r *ServerRuntime) Close() error {
	if r == nil {
		return nil
	}
	r.closed.Store(true)
	if r.WebSSHBroker != nil {
		r.WebSSHBroker.CloseAll()
	}
	var relayErr, credentialErr, serverNodeErr, authorizationCacheErr, clientMetadataSweeperErr, webSSHSweeperErr error
	if r.clientMetadataSweeperCancel != nil {
		r.clientMetadataSweeperCancel()
		if err := <-r.clientMetadataSweeperDone; err != nil && !errors.Is(err, context.Canceled) {
			clientMetadataSweeperErr = err
		}
		r.clientMetadataSweeperCancel = nil
	}
	if r.websshSweeperCancel != nil {
		r.websshSweeperCancel()
		if err := <-r.websshSweeperDone; err != nil && !errors.Is(err, context.Canceled) {
			webSSHSweeperErr = err
		}
		r.websshSweeperCancel = nil
		r.websshSweeperDone = nil
	}
	if r.authorizationCacheCleanup != nil {
		authorizationCacheErr = r.authorizationCacheCleanup()
	}
	if r.serverNodeLifecycle != nil {
		serverNodeErr = r.serverNodeLifecycle.Close()
	}
	if r.LocalAgentRelay != nil {
		relayErr = r.LocalAgentRelay.Close()
	}
	if r.relayServer != nil {
		r.relayServer.Stop()
	}
	if r.relayListener != nil {
		_ = r.relayListener.Close()
	}
	if r.Credentials != nil {
		credentialErr = r.Credentials.Close()
	}
	if r.ClientStreamService != nil {
		_ = r.ClientStreamService.Close()
	}
	return errors.Join(relayErr, credentialErr, serverNodeErr, authorizationCacheErr, clientMetadataSweeperErr, webSSHSweeperErr)
}

// closeAgentConnection is the authenticated inter-Server control handler. The
// unary interceptor supplies the caller identity; the request's caller field
// is only a correlation ID and must match that authenticated identity.
func (r *ServerRuntime) closeAgentConnection(ctx context.Context, req relay.CloseAgentConnectionRequest) error {
	if r == nil || r.AgentConnectionLeases == nil {
		return status.Error(codes.Unimplemented, "agent connection close control is not configured")
	}
	principal, ok := relay.ServerNodePrincipalFromContext(ctx)
	if !ok {
		return status.Error(codes.PermissionDenied, "relay server-node identity is required")
	}
	if principal.NodeID != req.RequestedByNodeID {
		return status.Error(codes.PermissionDenied, "relay close caller identity is denied")
	}
	err := r.AgentConnectionLeases.CloseConnection(ctx, req.AgentID, req.ConnectionID, req.ConnectionEpoch)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrEpoch):
		return status.Error(codes.FailedPrecondition, "agent connection epoch is stale")
	case errors.Is(err, ErrSessionClosed):
		return status.Error(codes.NotFound, "agent connection is not found")
	default:
		return status.Error(codes.Internal, "agent connection close failed")
	}
}

func (r *ServerRuntime) closeClientConnection(ctx context.Context, req relay.CloseClientConnectionRequest) error {
	if r == nil || r.ClientConnectionLeases == nil {
		return status.Error(codes.Unimplemented, "client connection close control is not configured")
	}
	principal, ok := relay.ServerNodePrincipalFromContext(ctx)
	if !ok || principal.NodeID != req.RequestedByNodeID {
		return status.Error(codes.PermissionDenied, "relay close caller identity is denied")
	}
	if err := r.ClientConnectionLeases.CloseConnection(req.ConnectionID, req.ConnectionEpoch); err != nil {
		switch {
		case errors.Is(err, ErrEpoch):
			return status.Error(codes.FailedPrecondition, "client connection epoch is stale")
		case errors.Is(err, ErrSessionClosed):
			return status.Error(codes.NotFound, "client connection is not found")
		default:
			return status.Error(codes.Internal, "client connection close failed")
		}
	}
	return nil
}

// closeWebSSHConnection is the authenticated inter-Server control handler for
// browser WebSSH process state. Durable session state is closed by the
// originating node before this RPC is issued.
func (r *ServerRuntime) closeWebSSHConnection(ctx context.Context, req relay.CloseWebSSHConnectionRequest) error {
	if r == nil || r.WebSSHBroker == nil {
		return status.Error(codes.Unimplemented, "webssh close control is not configured")
	}
	if !r.WebSSHEnabled {
		return status.Error(codes.Unimplemented, "webssh is disabled")
	}
	principal, ok := relay.ServerNodePrincipalFromContext(ctx)
	if !ok || principal.NodeID != req.RequestedByNodeID {
		return status.Error(codes.PermissionDenied, "relay webssh close caller identity is denied")
	}
	if err := r.WebSSHBroker.CloseLocal(req.SessionID); err != nil {
		switch {
		case errors.Is(err, ErrWebSSHBrokerClosed):
			return status.Error(codes.NotFound, "webssh session is not active locally")
		default:
			return status.Error(codes.Internal, "webssh connection close failed")
		}
	}
	return nil
}

// DialRelayNode is the authenticated production client path for a cluster
// relay. It intentionally has no unauthenticated fallback.
func (r *ServerRuntime) DialRelayNode(ctx context.Context, endpoint string, epoch int64) (*relay.GRPCNodeTransport, error) {
	if r == nil || !r.config.Relay.Enabled {
		return nil, errors.New("server runtime: authenticated relay is disabled")
	}
	if strings.TrimSpace(endpoint) == "" {
		endpoint = r.config.Relay.Endpoint
	}
	if strings.TrimSpace(endpoint) == "" {
		return nil, errors.New("server runtime: relay endpoint is required")
	}
	if epoch <= 0 {
		return nil, errors.New("server runtime: relay epoch is required")
	}
	tlsConfig, err := loadRelayClientTLS(r.config.Relay)
	if err != nil {
		return nil, err
	}
	return relay.DialAuthenticatedGRPCNode(ctx, endpoint, r.config.NodeID, epoch, r.config.Relay.NodeToken, tlsConfig)
}

// Handler exposes management API, embedded web assets, and the Agent WebSocket
// endpoint under /ws/agent. Scoped Agent credentials are validated before the
// optional AgentSessionConfig.Authenticate policy runs.
func (r *ServerRuntime) Handler() http.Handler {
	if r == nil {
		return http.NotFoundHandler()
	}
	health := NewHealthHandler(r, promhttp.HandlerFor(r.metricsRegistry, promhttp.HandlerOpts{}))
	var webSSH http.Handler
	if r.WebSSHEnabled {
		webSSH = r.WebSSHBroker
	}
	return NewWebHandlerWithManagedRoutes(r.API.Handler(), r.agentWebSocketHandler(), r.clientWebSocketHandler(), health, webSSH, r.managedRoutes)
}

// Live reports process responsiveness and intentionally performs no I/O.
func (r *ServerRuntime) Live(context.Context) error {
	if r == nil || r.closed.Load() {
		return errors.New("runtime is closed")
	}
	return nil
}

// Ready checks only bounded dependency state and returns component names and
// booleans to callers; detailed errors stay out of the HTTP response.
func (r *ServerRuntime) Ready(ctx context.Context) []ComponentStatus {
	if r == nil || r.closed.Load() {
		return []ComponentStatus{{Name: "runtime", Healthy: false, Error: "runtime unavailable"}}
	}
	checkCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	components := []ComponentStatus{{Name: "database", Healthy: r.DB != nil}}
	if r.DB != nil {
		if err := r.DB.Ping(checkCtx); err != nil {
			components[0].Healthy = false
			components[0].Error = err.Error()
		}
	}
	if r.config.Relay.Enabled {
		components = append(components, ComponentStatus{Name: "relay", Healthy: r.relayListener != nil})
	}
	if r.config.AuthorizationCache.Enabled {
		healthy := true
		if health, ok := r.ClientAuthorizer.(interface{ Unhealthy() bool }); ok {
			healthy = !health.Unhealthy()
		}
		cacheComponent := ComponentStatus{Name: "authorization_cache", Healthy: healthy}
		if !healthy {
			cacheComponent.Error = "authorization revision source is unavailable"
		}
		components = append(components, cacheComponent)
	}
	ready := true
	for _, component := range components {
		if !component.Healthy {
			ready = false
			break
		}
	}
	if r.metrics != nil {
		r.metrics.SetReady("server", ready)
	}
	return components
}

func (r *ServerRuntime) agentWebSocketHandler() http.Handler {
	return websocket.Server{Handler: r.serveAgentWS, Handshake: agentWebSocketHandshake(r.config.Security, r.preauthenticateAgentConnection)}
}

func (r *ServerRuntime) clientWebSocketHandler() http.Handler {
	return websocket.Server{Handler: r.serveClientWS, Handshake: clientWebSocketHandshake(r.config.Security, r.preauthenticateClientConnection)}
}

func (r *ServerRuntime) preauthenticateClientConnection(ctx context.Context, raw string) (ClientSessionPrincipal, error) {
	started := time.Now()
	identity, err := r.Credentials.ValidateAs(ctx, raw, storage.TokenTypeClient)
	if err != nil {
		if r.metrics != nil {
			r.metrics.ObserveStage("server", "token_auth", "failure", observability.NormalizeErrorClass(err), time.Since(started))
		}
		return ClientSessionPrincipal{}, auth.ErrUnauthenticated
	}
	connectionID, err := newClientConnectionID()
	if err != nil {
		if r.metrics != nil {
			r.metrics.ObserveStage("server", "token_auth", "failure", observability.NormalizeErrorClass(err), time.Since(started))
		}
		return ClientSessionPrincipal{}, auth.ErrUnauthenticated
	}
	if r.metrics != nil {
		r.metrics.ObserveStage("server", "token_auth", "success", "", time.Since(started))
	}
	return ClientSessionPrincipal{ConnectionID: connectionID, Identity: identity}, nil
}

func newClientConnectionID() (string, error) {
	var entropy [16]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return "", err
	}
	return "client_" + hex.EncodeToString(entropy[:]), nil
}

func clientOpenTimeoutFromConfig(stream config.ServerStreamConfig) time.Duration {
	// The wire open timeout is not configurable separately in this release;
	// a bounded default keeps strict opens from pinning futures forever.
	return 8 * time.Second
}

func (r *ServerRuntime) serveClientWS(conn *websocket.Conn) {
	ctx := context.Background()
	if conn.Request() != nil {
		ctx = conn.Request().Context()
	}
	principal, ok := clientPrincipalFromContext(ctx)
	if !ok {
		_ = conn.Close()
		return
	}
	conn.MaxPayloadBytes = clientWebSocketMaxPayloadBytes
	if r.metrics != nil {
		r.metrics.ObserveConnection("server", "client", "started", "")
	}
	transport := NewWSFrameTransport(&xNetWSFrameConn{conn: conn})
	defer func() {
		if r.metrics != nil {
			r.metrics.ObserveConnection("server", "client", "closed", "")
		}
	}()
	_ = serveClientSessionWithService(ctx, principal, transport, r.ClientAuthorizer, r.ClientTransport, maxClientOpenAttempts, r.metrics, r.ClientStreamService)
}

func (r *ServerRuntime) serveAgentWS(conn *websocket.Conn) {
	ctx := context.Background()
	if conn.Request() != nil {
		ctx = conn.Request().Context()
	}
	authentication, ok := agentAuthenticationFromContext(ctx)
	if !ok {
		_ = conn.Close()
		return
	}
	// The WebSocket message includes the 16-byte TunnelMesh frame header in
	// addition to the protocol payload bounded by protocol.MaxPayload.
	conn.MaxPayloadBytes = agentWebSocketMaxPayloadBytes
	if err := conn.SetReadDeadline(time.Now().Add(agentHelloTimeout)); err != nil {
		_ = conn.Close()
		return
	}
	tr := &xNetWSFrameConn{conn: conn}
	transport := NewWSFrameTransport(tr)
	helloStarted := time.Now()
	initial, err := transport.Receive()
	if err != nil || initial.Type != protocol.FrameAgentHello {
		if r.metrics != nil {
			r.metrics.ObserveStage("server", "agent_hello", "failure", observability.NormalizeErrorClass(err), time.Since(helloStarted))
		}
		_ = transport.Close()
		return
	}
	payload, err := protocol.DecodeAgentMetadataPayload(initial.Payload)
	if err != nil || strings.TrimSpace(payload.AgentID) == "" || strings.TrimSpace(payload.NodeID) == "" || payload.Epoch <= 0 {
		if r.metrics != nil {
			r.metrics.ObserveStage("server", "agent_hello", "failure", observability.NormalizeErrorClass(err), time.Since(helloStarted))
		}
		_ = transport.Close()
		return
	}
	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		_ = transport.Close()
		return
	}
	if r.metrics != nil {
		r.metrics.ObserveStage("server", "agent_hello", "success", "", time.Since(helloStarted))
	}
	tokenID, err := r.authenticateAgentConnection(ctx, authentication, payload.AgentID)
	if err != nil {
		_ = transport.Close()
		return
	}
	registration := AgentRegistration{
		AgentID: payload.AgentID, NodeID: payload.NodeID, Epoch: payload.Epoch,
		InstanceID: payload.InstanceID, ConnectionID: payload.ConnectionID,
		ConnectionEpoch: payload.Epoch, TokenID: tokenID,
		Capabilities: payload.Capabilities,
	}
	if r.LocalAgentRelay == nil {
		_ = transport.Close()
		return
	}
	if registration.InstanceID == "" {
		registration.InstanceID = registration.NodeID
	}
	if registration.ConnectionID == "" {
		registration.ConnectionID = "legacy"
	}
	action := agentConnectionAuditAction(r.AgentSessions, registration)
	session, err := r.AgentSessions.Register(ctx, registration, transport)
	actorUserID := authentication.Identity.OwnerUserID
	if authentication.Legacy {
		actorUserID = authentication.Principal.UserID
	}
	if err != nil {
		if r.metrics != nil {
			r.metrics.ObserveConnection("server", "agent", "failed", observability.NormalizeErrorClass(err))
			r.metrics.ObserveAgentConnection(registration.AgentID, registration.InstanceID, registration.ConnectionID, false)
			r.metrics.ObserveAgentConnectionError(registration.AgentID, registration.InstanceID, registration.ConnectionID, observability.NormalizeErrorClass(err))
		}
		_ = writeAgentConnectionAudit(ctx, r.DB.Audits(), actorUserID, agentConnectionRejectedAudit, registration, err)
		_ = transport.Close()
		return
	}
	leaseOwner, err := r.AgentConnectionLeases.Register(ctx, session)
	if err != nil {
		if r.metrics != nil {
			r.metrics.ObserveConnection("server", "agent", "failed", observability.NormalizeErrorClass(err))
			r.metrics.ObserveAgentConnection(registration.AgentID, registration.InstanceID, registration.ConnectionID, false)
			r.metrics.ObserveAgentConnectionError(registration.AgentID, registration.InstanceID, registration.ConnectionID, observability.NormalizeErrorClass(err))
		}
		_ = writeAgentConnectionAudit(ctx, r.DB.Audits(), actorUserID, agentConnectionRejectedAudit, registration, err)
		_ = session.Close()
		r.AgentSessions.RemoveSession(registration.AgentID, session)
		_ = transport.Close()
		return
	}
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.Background(), agentConnectionLeaseTimeout)
		defer cancel()
		if err := r.AgentConnectionLeases.Release(releaseCtx, leaseOwner); err != nil {
			if r.metrics != nil {
				r.metrics.ObserveAgentConnectionError(registration.AgentID, registration.InstanceID, registration.ConnectionID, observability.NormalizeErrorClass(err))
			}
		}
	}()
	if r.metrics != nil {
		r.metrics.ObserveConnection("server", "agent", "started", "")
		r.metrics.ObserveAgentConnection(registration.AgentID, session.InstanceID, session.ConnectionID, true)
		r.metrics.SetAgentConnectionCapacity(registration.AgentID, session.InstanceID, 64)
	}
	_ = writeAgentConnectionAudit(ctx, r.DB.Audits(), actorUserID, action, registration, nil)
	defer func() {
		_ = writeAgentConnectionAudit(context.Background(), r.DB.Audits(), actorUserID, agentConnectionClosedAudit, registration, nil)
	}()
	_ = serveRegisteredAgentSessionWithMetrics(ctx, r.AgentSessions, registration, session, transport, &initial, func(session *AgentSession, frame protocol.Frame) error {
		return r.LocalAgentRelay.handleAgentFrameGeneration(registration.AgentID, session.serverGeneration, frame)
	}, func(session *AgentSession) {
		r.LocalAgentRelay.failAgentGeneration(registration.AgentID, session.serverGeneration)
	}, r.metrics)
}

func (r *ServerRuntime) preauthenticateAgentConnection(ctx context.Context, raw string) (agentConnectionAuthentication, error) {
	identity, err := r.Credentials.ValidateAs(ctx, raw, storage.TokenTypeAgent)
	if err == nil {
		return agentConnectionAuthentication{Identity: identity}, nil
	}
	// Deprecated: management connection tokens are removed in v0.3.0. This
	// migration path is disabled unless explicitly enabled in configuration.
	if !r.config.Security.AllowLegacyConnectionTokens {
		return agentConnectionAuthentication{}, auth.ErrUnauthenticated
	}
	principal, err := r.Auth.ValidateToken(ctx, raw)
	if err != nil {
		return agentConnectionAuthentication{}, auth.ErrUnauthenticated
	}
	return agentConnectionAuthentication{Principal: principal, Legacy: true}, nil
}

func (r *ServerRuntime) authenticateAgentConnection(ctx context.Context, authentication agentConnectionAuthentication, agentID string) (tokenID string, retErr error) {
	started := time.Now()
	defer func() {
		if r.metrics != nil {
			result, class := "success", ""
			if retErr != nil {
				result, class = "failure", observability.NormalizeErrorClass(retErr)
			}
			r.metrics.ObserveStage("server", "token_auth", result, class, time.Since(started))
		}
	}()
	if !authentication.Legacy {
		if authentication.Identity.TokenID == "" || authentication.Identity.AgentID != agentID {
			return "", auth.ErrUnauthenticated
		}
		return authentication.Identity.TokenID, nil
	}
	agent, err := r.DB.Agents().Get(ctx, agentID)
	if err != nil || !agent.Enabled || (authentication.Principal.Role != "admin" && agent.OwnerUserID != authentication.Principal.UserID) {
		return "", auth.ErrForbidden
	}
	if err := r.DB.Audits().Create(ctx, storage.AuditLog{
		ActorUserID:  authentication.Principal.UserID,
		Action:       "deprecated_connection_token",
		ResourceType: "agent",
		ResourceID:   agentID,
		Details:      `{"removalVersion":"v0.3.0"}`,
	}); err != nil {
		return "", err
	}
	slog.WarnContext(ctx, "deprecated_connection_token", "agent_id", agentID, "user_id", authentication.Principal.UserID, "removal_version", "v0.3.0")
	return "", nil
}

// Serve binds a TCP listener on addr and blocks until ctx cancellation or a
// serving error. Shutdown is graceful and closes active HTTP/WebSocket conns.
func (r *ServerRuntime) Serve(ctx context.Context, addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	return r.ServeListener(ctx, ln)
}

func (r *ServerRuntime) ServeListener(ctx context.Context, ln net.Listener) error {
	if r == nil || ln == nil {
		return ErrRuntimeDatabaseRequired
	}
	serveListener, err := nativeTLSListener(ln, r.config.TLS)
	if err != nil {
		_ = ln.Close()
		return err
	}
	srv := &http.Server{Handler: r.Handler(), ReadHeaderTimeout: 10 * time.Second}
	// The proxy entry is parented to the caller's context so a SIGTERM stops it
	// even if shutdown() is never reached, and cancelEntry lets the error paths
	// stop it immediately.
	entryCtx, cancelEntry := context.WithCancel(ctx)
	defer cancelEntry()
	proxyErrCh := make(chan error, 1)
	// entryDone is closed once the entry goroutine has returned, which is what
	// makes shutdown synchronous: Addr() doubles as the "is it listening"
	// signal, so a supervisor (or a test) that sees ServeListener return must
	// not still observe a bound public traffic port.
	entryDone := make(chan struct{})
	if r.proxyEntry != nil {
		go func() {
			defer close(entryDone)
			if err := r.proxyEntry.Serve(entryCtx); err != nil {
				proxyErrCh <- fmt.Errorf("server runtime: proxy entry serve failed: %w", err)
			}
		}()
	} else {
		close(entryDone)
	}
	relayErrCh := make(chan error, 1)
	if r.relayServer != nil && r.relayListener != nil {
		go func() {
			err := r.relayServer.Serve(r.relayListener)
			if errors.Is(err, grpc.ErrServerStopped) {
				relayErrCh <- nil
				return
			}
			if err == nil {
				relayErrCh <- errors.New("server runtime: relay server stopped unexpectedly")
				return
			}
			relayErrCh <- fmt.Errorf("server runtime: relay serve failed: %w", err)
		}()
	}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(serveListener) }()
	shutdown := func() {
		cancelEntry()
		// Bounded by the entry's own shutdown timeout: Serve always returns
		// after its context is cancelled, so this cannot hang the runtime.
		<-entryDone
		if r.relayServer != nil {
			r.relayServer.Stop()
		}
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}
	select {
	case err := <-errCh:
		shutdown()
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case relayErr := <-relayErrCh:
		shutdown()
		return relayErr
	case proxyErr := <-proxyErrCh:
		// The entry is a public traffic path: if it cannot serve, the process
		// must exit so the supervisor restarts it rather than blackholing
		// every tp-* request.
		shutdown()
		return proxyErr
	case <-ctx.Done():
		shutdown()
		return nil
	}
}

func loadRelayServerTLS(cfg config.RelayConfig) (*tls.Config, error) {
	complete, err := relayTLSMaterialComplete(cfg)
	if err != nil || !complete {
		return nil, err
	}
	cert, err := tls.LoadX509KeyPair(cfg.Cert, cfg.Key)
	if err != nil {
		return nil, fmt.Errorf("server runtime: load relay certificate: %w", err)
	}
	caPEM, err := os.ReadFile(cfg.CA)
	if err != nil {
		return nil, fmt.Errorf("server runtime: read relay CA: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("server runtime: relay CA contains no certificates")
	}
	return relay.NewRelayServerTLSConfig(cert, roots)
}

func loadRelayClientTLS(cfg config.RelayConfig) (*tls.Config, error) {
	complete, err := relayTLSMaterialComplete(cfg)
	if err != nil || !complete {
		return nil, err
	}
	cert, err := tls.LoadX509KeyPair(cfg.Cert, cfg.Key)
	if err != nil {
		return nil, fmt.Errorf("server runtime: load relay client certificate: %w", err)
	}
	caPEM, err := os.ReadFile(cfg.CA)
	if err != nil {
		return nil, fmt.Errorf("server runtime: read relay client CA: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("server runtime: relay client CA contains no certificates")
	}
	return relay.NewRelayClientTLSConfig(cert, roots, cfg.ServerName)
}

func relayTLSMaterialComplete(cfg config.RelayConfig) (bool, error) {
	count := 0
	for _, value := range []string{cfg.CA, cfg.Cert, cfg.Key} {
		if strings.TrimSpace(value) != "" {
			count++
		}
	}
	switch count {
	case 0:
		return false, nil
	case 3:
		return true, nil
	default:
		return false, errors.New("server runtime: relay requires all CA, certificate, and key fields for mTLS or none for plaintext")
	}
}

type xNetWSFrameConn struct{ conn *websocket.Conn }

func (c *xNetWSFrameConn) ReadMessage() (int, []byte, error) {
	var payload []byte
	if err := websocket.Message.Receive(c.conn, &payload); err != nil {
		return 0, nil, err
	}
	return websocket.BinaryFrame, payload, nil
}

func (c *xNetWSFrameConn) WriteMessage(_ int, payload []byte) error {
	return websocket.Message.Send(c.conn, payload)
}

func (c *xNetWSFrameConn) Close() error { return c.conn.Close() }
