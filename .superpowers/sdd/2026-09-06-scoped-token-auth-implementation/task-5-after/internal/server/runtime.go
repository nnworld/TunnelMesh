package server

import (
	"context"
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
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

var ErrRuntimeDatabaseRequired = errors.New("server runtime: database is required")

type RuntimeConfig struct {
	Security config.SecurityConfig
	TLS      config.TLSConfig
}

// ServerRuntime is the process-scoped server wiring shared by HTTP handlers
// and authenticated Agent WebSocket handlers. The caller owns DB lifecycle.
type ServerRuntime struct {
	DB            *storage.DB
	AgentSessions *AgentSessionManager
	API           *API
	Auth          *auth.AuthService
	Credentials   *auth.CredentialService
	config        RuntimeConfig
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
	return &ServerRuntime{DB: db, AgentSessions: NewAgentSessionManagerWithMetadata(db.Metadata(), cfg), API: NewAPI(db, authService), Auth: authService, Credentials: credentials, config: runtimeConfig}, nil
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
	return NewWebHandler(r.API.Handler(), r.agentWebSocketHandler())
}

func (r *ServerRuntime) agentWebSocketHandler() http.Handler {
	return websocket.Server{Handler: r.serveAgentWS, Handshake: agentWebSocketHandshake(r.config.Security)}
}

func (r *ServerRuntime) serveAgentWS(conn *websocket.Conn) {
	ctx := context.Background()
	if conn.Request() != nil {
		ctx = conn.Request().Context()
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
	tokenID, err := r.authenticateAgentConnection(ctx, bearerToken(conn.Request()), payload.AgentID)
	if err != nil {
		_ = transport.Close()
		return
	}
	registration := AgentRegistration{AgentID: payload.AgentID, NodeID: payload.NodeID, Epoch: payload.Epoch, TokenID: tokenID}
	_ = ServeAgentSessionWithInitialFrame(ctx, r.AgentSessions, registration, transport, initial, nil)
}

func (r *ServerRuntime) authenticateAgentConnection(ctx context.Context, raw, agentID string) (string, error) {
	identity, err := r.Credentials.ValidateAs(ctx, raw, storage.TokenTypeAgent)
	if err == nil && identity.AgentID == agentID {
		return identity.TokenID, nil
	}
	// Deprecated: management connection tokens are removed in v0.3.0. This
	// migration path is disabled unless explicitly enabled in configuration.
	if !r.config.Security.AllowLegacyConnectionTokens {
		return "", auth.ErrUnauthenticated
	}
	principal, err := r.Auth.ValidateToken(ctx, raw)
	if err != nil {
		return "", auth.ErrUnauthenticated
	}
	agent, err := r.DB.Agents().Get(ctx, agentID)
	if err != nil || !agent.Enabled || (principal.Role != "admin" && agent.OwnerUserID != principal.UserID) {
		return "", auth.ErrForbidden
	}
	if err := r.DB.Audits().Create(ctx, storage.AuditLog{
		ActorUserID:  principal.UserID,
		Action:       "deprecated_connection_token",
		ResourceType: "agent",
		ResourceID:   agentID,
		Details:      `{"removalVersion":"v0.3.0"}`,
	}); err != nil {
		return "", err
	}
	slog.WarnContext(ctx, "deprecated_connection_token", "agent_id", agentID, "user_id", principal.UserID, "removal_version", "v0.3.0")
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
