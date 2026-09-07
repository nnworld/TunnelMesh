package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"golang.org/x/net/websocket"

	"github.com/tunnelmesh/tunnelmesh/internal/auth"
	"github.com/tunnelmesh/tunnelmesh/internal/config"
	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
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
	Security config.SecurityConfig
	TLS      config.TLSConfig
}

// ServerRuntime is the process-scoped server wiring shared by HTTP handlers
// and authenticated Agent WebSocket handlers. The caller owns DB lifecycle.
type ServerRuntime struct {
	DB               *storage.DB
	AgentSessions    *AgentSessionManager
	ClientSessions   *ClientSessionManager
	API              *API
	Auth             *auth.AuthService
	Credentials      *auth.CredentialService
	ClientAuthorizer StreamAuthorizer
	ClientTransport  relay.NodeTransport
	config           RuntimeConfig
}

// NewServerRuntime creates the server runtime with durable Agent metadata
// persistence enabled. The CLI server run command and embedders should create
// one runtime at startup, then pass AgentSessions to ServeAgentSession.
func NewServerRuntime(db *storage.DB, cfg AgentSessionConfig, options ...RuntimeConfig) (*ServerRuntime, error) {
	if db == nil {
		return nil, ErrRuntimeDatabaseRequired
	}
	authService := auth.NewAuthService(db)
	credentials := auth.NewCredentialService(db)
	var runtimeConfig RuntimeConfig
	if len(options) > 0 {
		runtimeConfig = options[0]
		runtimeConfig.Security.AllowedHosts = append([]string(nil), runtimeConfig.Security.AllowedHosts...)
		runtimeConfig.Security.AllowedOrigins = append([]string(nil), runtimeConfig.Security.AllowedOrigins...)
	}
	return &ServerRuntime{DB: db, AgentSessions: NewAgentSessionManagerWithMetadata(db.Metadata(), cfg), ClientSessions: NewClientSessionManager(), API: NewAPI(db, authService), Auth: authService, Credentials: credentials, ClientAuthorizer: NewCredentialStreamAuthorizer(credentials), config: runtimeConfig}, nil
}

// Close releases runtime-owned background workers. The caller continues to
// own the database lifecycle.
func (r *ServerRuntime) Close() error {
	if r == nil || r.Credentials == nil {
		return nil
	}
	return r.Credentials.Close()
}

// Handler exposes management API, embedded web assets, and the Agent WebSocket
// endpoint under /ws/agent. Scoped Agent credentials are validated before the
// optional AgentSessionConfig.Authenticate policy runs.
func (r *ServerRuntime) Handler() http.Handler {
	if r == nil {
		return http.NotFoundHandler()
	}
	return NewWebHandler(r.API.Handler(), r.agentWebSocketHandler(), r.clientWebSocketHandler())
}

func (r *ServerRuntime) agentWebSocketHandler() http.Handler {
	return websocket.Server{Handler: r.serveAgentWS, Handshake: agentWebSocketHandshake(r.config.Security, r.preauthenticateAgentConnection)}
}

func (r *ServerRuntime) clientWebSocketHandler() http.Handler {
	return websocket.Server{Handler: r.serveClientWS, Handshake: clientWebSocketHandshake(r.config.Security, r.preauthenticateClientConnection)}
}

func (r *ServerRuntime) preauthenticateClientConnection(ctx context.Context, raw string) (ClientSessionPrincipal, error) {
	identity, err := r.Credentials.ValidateAs(ctx, raw, storage.TokenTypeClient)
	if err != nil {
		return ClientSessionPrincipal{}, auth.ErrUnauthenticated
	}
	connectionID, err := newClientConnectionID()
	if err != nil {
		return ClientSessionPrincipal{}, auth.ErrUnauthenticated
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
	transport := NewWSFrameTransport(&xNetWSFrameConn{conn: conn})
	r.ClientSessions.Register(principal.ConnectionID, transport)
	defer r.ClientSessions.Remove(principal.ConnectionID)
	_ = ServeClientSession(ctx, principal, transport, r.ClientAuthorizer, r.ClientTransport)
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
	initial, err := transport.Receive()
	if err != nil || initial.Type != protocol.FrameAgentHello {
		_ = transport.Close()
		return
	}
	payload, err := protocol.DecodeAgentMetadataPayload(initial.Payload)
	if err != nil || strings.TrimSpace(payload.AgentID) == "" || strings.TrimSpace(payload.NodeID) == "" || payload.Epoch <= 0 {
		_ = transport.Close()
		return
	}
	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		_ = transport.Close()
		return
	}
	tokenID, err := r.authenticateAgentConnection(ctx, authentication, payload.AgentID)
	if err != nil {
		_ = transport.Close()
		return
	}
	registration := AgentRegistration{AgentID: payload.AgentID, NodeID: payload.NodeID, Epoch: payload.Epoch, TokenID: tokenID}
	_ = ServeAgentSessionWithInitialFrame(ctx, r.AgentSessions, registration, transport, initial, nil)
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

func (r *ServerRuntime) authenticateAgentConnection(ctx context.Context, authentication agentConnectionAuthentication, agentID string) (string, error) {
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
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(serveListener) }()
	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		return nil
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
