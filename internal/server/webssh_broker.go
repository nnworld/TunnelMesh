package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/config"
	"github.com/tunnelmesh/tunnelmesh/internal/observability"
	"github.com/tunnelmesh/tunnelmesh/internal/relay"
)

var (
	// ErrWebSSHMessageInvalid means the browser sent a non-binary frame or a
	// frame larger than the configured limit. Both terminate the session.
	ErrWebSSHMessageInvalid = errors.New("webssh binary websocket message required")
	// ErrWebSSHBrokerClosed identifies an admin-initiated process-local close.
	ErrWebSSHBrokerClosed = errors.New("webssh session was closed")
)

// WebSSHActiveSession exposes only the close surface needed by an
// administrator. SSH bytes and credentials are intentionally not available.
type WebSSHActiveSession struct {
	Cancel context.CancelCauseFunc
	WS     WSConn
}

// WebSSHAuditEvent carries metadata only. It deliberately excludes terminal
// output and credential material from browser SSH sessions.
type WebSSHAuditEvent struct {
	Action  string
	UserID  string
	AgentID string
	Error   string
}

// WebSSHBrokerDeps keeps transport and policy dependencies injected so the
// broker can be tested without a physical Agent relay or WebSocket server.
type WebSSHBrokerDeps struct {
	Sessions        *WebSSHSessionService
	Opener          relay.NodeTransport
	Upgrade         WSUpgrader
	Security        config.SecurityConfig
	MaxMessageBytes int
	Metrics         *observability.Metrics
	Audit           func(context.Context, WebSSHAuditEvent)
}

// WebSSHBroker maps a one-time ticket to exactly one Agent TCP stream.
type WebSSHBroker struct {
	deps     WebSSHBrokerDeps
	mu       sync.RWMutex
	active   map[string]WebSSHActiveSession
	closeOne sync.Map
}

func NewWebSSHBroker(deps WebSSHBrokerDeps) *WebSSHBroker {
	return &WebSSHBroker{deps: deps, active: make(map[string]WebSSHActiveSession)}
}

func (h *WebSSHBroker) maxMessageBytes() int {
	if h == nil || h.deps.MaxMessageBytes <= 0 {
		return 64 << 10
	}
	return h.deps.MaxMessageBytes
}

// ServeHTTP upgrades a browser connection and delegates the remainder to
// Handle. The associated ticket must be single-use and already persisted.
func (h *WebSSHBroker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if err := validateWebSSHOrigin(h.deps.Security, r); err != nil {
		http.Error(w, "webssh origin is not allowed", http.StatusForbidden)
		return
	}
	if h == nil || h.deps.Sessions == nil || h.deps.Opener == nil || h.deps.Upgrade == nil {
		http.Error(w, "webssh unavailable", http.StatusServiceUnavailable)
		return
	}
	prefix := "/ws/webssh/"
	if !strings.HasPrefix(r.URL.Path, prefix) {
		http.NotFound(w, r)
		return
	}
	sessionID := strings.Trim(strings.TrimPrefix(r.URL.Path, prefix), "/")
	ticket := strings.TrimSpace(r.URL.Query().Get("ticket"))
	if sessionID == "" || ticket == "" {
		http.Error(w, "webssh session and ticket are required", http.StatusBadRequest)
		return
	}
	ws, err := h.deps.Upgrade.Upgrade(w, r)
	if err != nil {
		return
	}
	_ = h.Handle(r.Context(), ws, sessionID, ticket)
}

// Handle authenticates the ticket before opening any relay stream. This order
// prevents an invalid ticket from consuming a logical Agent connection.
func (h *WebSSHBroker) Handle(ctx context.Context, ws WSConn, sessionID, ticket string) error {
	if h == nil || h.deps.Sessions == nil || h.deps.Opener == nil || ws == nil {
		return ErrWebSSHSessionUnavailable
	}
	defer ws.Close()
	session, server, err := h.deps.Sessions.AuthenticateTicket(ctx, sessionID, ticket, time.Now().UTC())
	if err != nil {
		return err
	}
	streamCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	if err := h.register(sessionID, WebSSHActiveSession{Cancel: cancel, WS: ws}); err != nil {
		return err
	}
	defer h.unregister(sessionID)
	openCtx := streamCtx
	if h.deps.Sessions.limits.OpenTimeout > 0 {
		var openCancel context.CancelFunc
		openCtx, openCancel = context.WithTimeout(streamCtx, h.deps.Sessions.limits.OpenTimeout)
		defer openCancel()
	}
	stream, err := h.deps.Opener.OpenStream(openCtx, relay.StreamRequest{
		AgentID: session.AgentID, Protocol: "tcp", TargetHost: server.Host, TargetPort: server.Port,
	})
	if err != nil {
		if h.deps.Metrics != nil {
			h.deps.Metrics.ObserveWebSSHStreamError(observability.NormalizeErrorClass(err))
		}
		h.durableClose(sessionID, "agent_unreachable")
		return fmt.Errorf("webssh agent is unreachable: %w", err)
	}
	defer stream.Close()
	started := time.Now().UTC()
	outcome := h.bridgeWithReason(sessionID, streamCtx, cancel, ws, stream)
	if h.deps.Metrics != nil {
		h.deps.Metrics.ObserveWebSSHStreamDuration(time.Since(started))
		if outcome.err != nil {
			h.deps.Metrics.ObserveWebSSHStreamError(observability.NormalizeErrorClass(outcome.err))
		}
	}
	// 桥接返回即代表本节点不再持有该会话：浏览器关页、远端 shell 退出或内部错误
	// 都必须回写 closed。服务端是唯一权威——标签页关闭不会调用管理 API，漏写会让
	// active 行占满每用户配额直到 session_ttl，把用户锁在“活跃会话数已达上限”。
	h.durableClose(sessionID, outcome.reason)
	return outcome.err
}

func (h *WebSSHBroker) durableClose(sessionID, reason string) {
	if h.deps.Sessions == nil {
		return
	}
	_ = h.deps.Sessions.CloseDurable(context.WithoutCancel(context.Background()), sessionID, reason)
}

func (h *WebSSHBroker) register(sessionID string, session WebSSHActiveSession) error {
	if sessionID == "" {
		return errors.New("webssh session id is required")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, exists := h.active[sessionID]; exists {
		return fmt.Errorf("webssh session %s is already active", sessionID)
	}
	h.active[sessionID] = session
	if h.deps.Metrics != nil {
		h.deps.Metrics.ObserveConnection("server", "webssh", "started", "")
		h.deps.Metrics.SetWebSSHActiveSessions(len(h.active))
	}
	return nil
}

func (h *WebSSHBroker) unregister(sessionID string) {
	h.mu.Lock()
	_, wasActive := h.active[sessionID]
	delete(h.active, sessionID)
	h.mu.Unlock()
	if wasActive && h.deps.Metrics != nil {
		h.deps.Metrics.ObserveConnection("server", "webssh", "closed", "")
		h.deps.Metrics.SetWebSSHActiveSessions(len(h.active))
	}
}

// CloseLocal terminates a session owned by this process. Missing local state
// is an error because callers must distinguish it from a successful close.
func (h *WebSSHBroker) CloseLocal(sessionID string) error {
	if h == nil {
		return ErrWebSSHBrokerClosed
	}
	h.mu.RLock()
	session, ok := h.active[sessionID]
	h.mu.RUnlock()
	if !ok {
		return ErrWebSSHBrokerClosed
	}
	if _, alreadyClosing := h.closeOne.LoadOrStore(sessionID, true); alreadyClosing {
		return nil
	}
	if session.Cancel != nil {
		session.Cancel(ErrWebSSHBrokerClosed)
	}
	if session.WS != nil {
		_ = session.WS.Close()
	}
	return nil
}

// CloseAll terminates every bridge owned by this process. Runtime shutdown
// calls it before releasing background workers so a live browser cannot keep
// an Agent relay stream open beyond the Server process lifetime.
func (h *WebSSHBroker) CloseAll() {
	if h == nil {
		return
	}
	h.mu.RLock()
	sessionIDs := make([]string, 0, len(h.active))
	for sessionID := range h.active {
		sessionIDs = append(sessionIDs, sessionID)
	}
	h.mu.RUnlock()
	for _, sessionID := range sessionIDs {
		_ = h.CloseLocal(sessionID)
	}
}

func (h *WebSSHBroker) ActiveCount() int {
	if h == nil {
		return 0
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.active)
}

// webSSHBridgeOutcome 携带桥接结束原因，供 Handle 回写持久化 close_reason。
type webSSHBridgeOutcome struct {
	err    error
	reason string
}

type webSSHCopyResult struct {
	from string
	err  error
}

func (h *WebSSHBroker) bridge(sessionID string, ctx context.Context, cancel context.CancelCauseFunc, ws WSConn, stream io.ReadWriteCloser) error {
	return h.bridgeWithReason(sessionID, ctx, cancel, ws, stream).err
}

func (h *WebSSHBroker) bridgeWithReason(sessionID string, ctx context.Context, cancel context.CancelCauseFunc, ws WSConn, stream io.ReadWriteCloser) webSSHBridgeOutcome {
	watchDone := make(chan struct{})
	defer close(watchDone)
	go func() {
		select {
		case <-ctx.Done():
			_ = stream.Close()
			_ = ws.Close()
		case <-watchDone:
		}
	}()

	errCh := make(chan webSSHCopyResult, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		errCh <- webSSHCopyResult{"browser", h.recoverCopy(func() error { return h.copyBrowserToAgent(ctx, ws, stream) })}
	}()
	go func() {
		defer wg.Done()
		errCh <- webSSHCopyResult{"agent", h.recoverCopy(func() error { return h.copyAgentToBrowser(ctx, ws, stream) })}
	}()

	first := <-errCh
	cancel(nil)
	_ = stream.Close()
	_ = ws.Close()
	second := <-errCh
	wg.Wait()
	h.closeOne.Delete(sessionID)
	result := first.err
	if errors.Is(result, context.Canceled) || errors.Is(result, io.EOF) {
		result = second.err
	}
	if cause := context.Cause(ctx); cause != nil && !errors.Is(cause, context.Canceled) {
		result = cause
	}
	if h.deps.Metrics != nil {
		h.deps.Metrics.ObserveStream("tcp", "closed", observability.NormalizeErrorClass(result))
	}
	reason := webSSHBridgeCloseReason(ctx, first, second)
	if errors.Is(result, io.EOF) || errors.Is(result, context.Canceled) {
		return webSSHBridgeOutcome{reason: reason}
	}
	return webSSHBridgeOutcome{err: result, reason: reason}
}

// webSSHBridgeCloseReason 把“哪一侧先结束”翻译成持久化 close_reason：远端通道 EOF
// 记 remote_closed，浏览器侧 EOF 记 client_disconnected，服务端主动取消记
// server_closed，其余记 bridge_closed。
func webSSHBridgeCloseReason(ctx context.Context, first, second webSSHCopyResult) string {
	if cause := context.Cause(ctx); cause != nil && !errors.Is(cause, context.Canceled) {
		return "server_closed"
	}
	ended := first
	if errors.Is(ended.err, context.Canceled) {
		ended = second
	}
	switch {
	case errors.Is(ended.err, io.EOF) && ended.from == "agent":
		return "remote_closed"
	case errors.Is(ended.err, io.EOF) && ended.from == "browser":
		return "client_disconnected"
	default:
		return "bridge_closed"
	}
}

func (h *WebSSHBroker) recoverCopy(copy func() error) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("webssh copy panic: %v", recovered)
		}
	}()
	return copy()
}

func (h *WebSSHBroker) copyBrowserToAgent(ctx context.Context, ws WSConn, stream io.Writer) error {
	limit := h.maxMessageBytes()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if h.deps.Sessions != nil && h.deps.Sessions.limits.IdleTimeout > 0 {
			if deadline, ok := ws.(deadlineWSConn); ok {
				if err := deadline.SetReadDeadline(time.Now().Add(h.deps.Sessions.limits.IdleTimeout)); err != nil {
					return fmt.Errorf("webssh idle deadline: %w", err)
				}
			}
		}
		typ, data, err := ws.ReadMessage()
		if err != nil {
			return err
		}
		if typ != 2 || len(data) > limit {
			return ErrWebSSHMessageInvalid
		}
		if err := writeAll(stream, data); err != nil {
			return err
		}
		if h.deps.Metrics != nil {
			h.deps.Metrics.ObserveBytes("webssh", "upload", "tcp", int64(len(data)))
			h.deps.Metrics.ObserveWebSSHBytes("upload", int64(len(data)))
		}
	}
}

func (h *WebSSHBroker) copyAgentToBrowser(ctx context.Context, ws WSConn, stream io.Reader) error {
	buf := make([]byte, 32<<10)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		n, err := stream.Read(buf)
		if n > 0 {
			if writeErr := ws.WriteMessage(2, buf[:n]); writeErr != nil {
				return writeErr
			}
			if h.deps.Metrics != nil {
				h.deps.Metrics.ObserveBytes("webssh", "download", "tcp", int64(n))
				h.deps.Metrics.ObserveWebSSHBytes("download", int64(n))
			}
		}
		if err != nil {
			return err
		}
	}
}
