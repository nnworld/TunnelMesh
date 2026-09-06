package server

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"golang.org/x/net/websocket"

	"github.com/tunnelmesh/tunnelmesh/internal/auth"
	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

var ErrRuntimeDatabaseRequired = errors.New("server runtime: database is required")

// ServerRuntime is the process-scoped server wiring shared by HTTP handlers
// and authenticated Agent WebSocket handlers. The caller owns DB lifecycle.
type ServerRuntime struct {
	DB            *storage.DB
	AgentSessions *AgentSessionManager
	API           *API
	Auth          *auth.AuthService
	sessionConfig AgentSessionConfig
}

// NewServerRuntime creates the server runtime with durable Agent metadata
// persistence enabled. The CLI server run command and embedders should create
// one runtime at startup, then pass AgentSessions to ServeAgentSession.
func NewServerRuntime(db *storage.DB, cfg AgentSessionConfig) (*ServerRuntime, error) {
	if db == nil {
		return nil, ErrRuntimeDatabaseRequired
	}
	authService := auth.NewAuthService(db)
	return &ServerRuntime{DB: db, AgentSessions: NewAgentSessionManagerWithMetadata(db.Metadata(), cfg), API: NewAPI(db, authService), Auth: authService, sessionConfig: cfg}, nil
}

// Handler exposes management API, embedded web assets, and the Agent WebSocket
// endpoint under /ws/agent. Agent authentication is intentionally injected via
// AgentSessionConfig.Authenticate because agent credentials are deployment
// specific and are not inferred from user API tokens.
func (r *ServerRuntime) Handler() http.Handler {
	if r == nil {
		return http.NotFoundHandler()
	}
	return NewWebHandler(r.API.Handler(), websocket.Handler(r.serveAgentWS))
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
	registration := AgentRegistration{AgentID: payload.AgentID, NodeID: payload.NodeID, Epoch: payload.Epoch, Token: bearerToken(conn.Request())}
	if r.sessionConfig.Authenticate == nil {
		_ = transport.Close()
		return
	}
	_ = ServeAgentSessionWithInitialFrame(ctx, r.AgentSessions, registration, transport, initial, nil)
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
	srv := &http.Server{Handler: r.Handler(), ReadHeaderTimeout: 10 * time.Second}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()
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
