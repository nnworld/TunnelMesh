package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/config"
	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
	"github.com/tunnelmesh/tunnelmesh/internal/relay"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

// Script runs are admin-only and bounded here as well as on the agent: the server
// refuses an oversized or badly-timed request before any stream opens.
const (
	agentExecDefaultTimeout = 60 * time.Second
	agentExecMaxTimeout     = 10 * time.Minute
	agentExecMaxScriptBytes = 64 << 10
	agentExecTicketTTL      = 30 * time.Second
	agentExecPath           = "/ws/agent-exec/"
)

var (
	errAgentExecTicketInvalid = errors.New("agent exec ticket is invalid, used or expired")
	ErrAgentExecUnsupported   = errors.New("agent does not support script execution")
	ErrAgentExecOffline       = errors.New("agent is offline")
)

// AgentExecAuditEvent records the facts of a run. Details never contain script text or
// output: a script often holds secrets, so only its digest and size are kept.
type AgentExecAuditEvent struct {
	Action  string
	UserID  string
	AgentID string
	Details string
}

// agentExecOpener opens the exec stream toward an agent. The relay transport
// satisfies it, and tests substitute a fake.
type agentExecOpener interface {
	OpenStream(ctx context.Context, req relay.StreamRequest) (io.ReadWriteCloser, error)
}

type agentExecEntry struct {
	hash    string
	agentID string
	userID  string
	expires time.Time
	used    bool
}

// agentExecTickets issues single-use, short-lived tickets. Only a SHA-256 digest of
// each ticket is kept, so the store never holds a usable credential.
type agentExecTickets struct {
	mu      sync.Mutex
	now     func() time.Time
	entries map[string]agentExecEntry
}

type agentExecTicket struct {
	SessionID string
	Ticket    string
	ExpiresAt time.Time
}

func newAgentExecTickets(now func() time.Time) *agentExecTickets {
	return &agentExecTickets{now: now, entries: make(map[string]agentExecEntry)}
}

func (t *agentExecTickets) issue(agentID, userID string, ttl time.Duration) (agentExecTicket, error) {
	secret, err := randomToken(32)
	if err != nil {
		return agentExecTicket{}, err
	}
	sessionID, err := randomToken(16)
	if err != nil {
		return agentExecTicket{}, err
	}
	digest := sha256.Sum256([]byte(secret))
	t.mu.Lock()
	defer t.mu.Unlock()
	expires := t.now().Add(ttl)
	t.entries[sessionID] = agentExecEntry{hash: hex.EncodeToString(digest[:]), agentID: agentID, userID: userID, expires: expires}
	return agentExecTicket{SessionID: sessionID, Ticket: secret, ExpiresAt: expires}, nil
}

// redeem consumes the ticket. A second attempt, an expired ticket and a wrong secret
// all return the same error, so the response does not reveal which one applied.
func (t *agentExecTickets) redeem(sessionID, ticket string) (agentExecEntry, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	entry, ok := t.entries[sessionID]
	if !ok || entry.used || t.now().After(entry.expires) {
		return agentExecEntry{}, errAgentExecTicketInvalid
	}
	digest := sha256.Sum256([]byte(ticket))
	if hex.EncodeToString(digest[:]) != entry.hash {
		return agentExecEntry{}, errAgentExecTicketInvalid
	}
	entry.used = true
	t.entries[sessionID] = entry
	return entry, nil
}

func randomToken(n int) (string, error) {
	raw := make([]byte, n)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

type agentExecBrokerDeps struct {
	Opener         agentExecOpener
	Upgrade        WSUpgrader
	Security       config.SecurityConfig
	MaxScriptBytes int
	Audit          func(context.Context, AgentExecAuditEvent)
}

// agentExecBroker maps one redeemed ticket to one script run over one exec stream.
type agentExecBroker struct {
	deps    agentExecBrokerDeps
	tickets *agentExecTickets
}

func newAgentExecBroker(deps agentExecBrokerDeps) *agentExecBroker {
	if deps.MaxScriptBytes <= 0 {
		deps.MaxScriptBytes = agentExecMaxScriptBytes
	}
	return &agentExecBroker{deps: deps, tickets: newAgentExecTickets(time.Now)}
}

// agentExecRequest is the one message the browser sends after connecting.
type agentExecRequest struct {
	Type       string `json:"type"`
	Script     string `json:"script"`
	TimeoutSec int    `json:"timeoutSec"`
}

// ServeHTTP upgrades a browser connection and hands it to Handle. The origin rule is
// the one WebSSH uses, so a page on another site cannot drive a run.
func (b *agentExecBroker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if err := validateWebSSHOrigin(b.deps.Security, r); err != nil {
		http.Error(w, "agent exec origin is not allowed", http.StatusForbidden)
		return
	}
	if b.deps.Upgrade == nil || b.deps.Opener == nil {
		http.Error(w, "agent exec unavailable", http.StatusServiceUnavailable)
		return
	}
	sessionID := strings.Trim(strings.TrimPrefix(r.URL.Path, agentExecPath), "/")
	ticket := strings.TrimSpace(r.URL.Query().Get("ticket"))
	if sessionID == "" || ticket == "" {
		http.Error(w, "agent exec session and ticket are required", http.StatusBadRequest)
		return
	}
	ws, err := b.deps.Upgrade.Upgrade(w, r)
	if err != nil {
		return
	}
	_ = b.Handle(r.Context(), ws, sessionID, ticket)
}

// Handle validates the request before opening anything toward the agent. The ticket is
// consumed first, so a failed or repeated connection cannot start a second run.
func (b *agentExecBroker) Handle(ctx context.Context, ws WSConn, sessionID, ticket string) error {
	defer ws.Close()
	entry, err := b.tickets.redeem(sessionID, ticket)
	if err != nil {
		return err
	}
	_, raw, err := ws.ReadMessage()
	if err != nil {
		return err
	}
	var request agentExecRequest
	if err := json.Unmarshal(raw, &request); err != nil || request.Type != "run" {
		return sendAgentExecError(ws, "the first message must be a run request")
	}
	if len(request.Script) == 0 || len(request.Script) > b.deps.MaxScriptBytes {
		return sendAgentExecError(ws, "script is empty or exceeds the server limit")
	}
	timeout := agentExecDefaultTimeout
	if request.TimeoutSec > 0 {
		timeout = time.Duration(request.TimeoutSec) * time.Second
	}
	if timeout > agentExecMaxTimeout {
		timeout = agentExecMaxTimeout
	}
	script := []byte(request.Script)
	digest := sha256.Sum256(script)
	started := time.Now().UTC()
	b.audit(ctx, "agent.exec.started", entry, map[string]any{
		"script_sha256": hex.EncodeToString(digest[:]), "script_bytes": len(script), "timeout_s": int(timeout.Seconds()),
	})

	stream, err := b.deps.Opener.OpenStream(ctx, relay.StreamRequest{AgentID: entry.agentID, Protocol: protocol.StreamProtocolExec})
	if err != nil {
		b.audit(ctx, "agent.exec.finished", entry, map[string]any{"result": "agent_unreachable"})
		return sendAgentExecError(ws, "agent is unreachable")
	}
	defer stream.Close()
	header, err := protocol.EncodeExecHeader(protocol.ExecHeader{TimeoutSeconds: int(timeout.Seconds()), ScriptBytes: len(script)})
	if err != nil {
		return sendAgentExecError(ws, "invalid run request")
	}
	if _, err := stream.Write(append(header, script...)); err != nil {
		return sendAgentExecError(ws, "agent rejected the run request")
	}

	finished := map[string]any{"result": "no_exit"}
	for {
		kind, payload, err := protocol.ReadExecRecord(stream)
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			finished["result"] = "stream_error"
			break
		}
		switch kind {
		case protocol.ExecRecordStdout, protocol.ExecRecordStderr:
			name := "stdout"
			if kind == protocol.ExecRecordStderr {
				name = "stderr"
			}
			if err := sendAgentExecFrame(ws, map[string]any{"type": name, "data": base64.StdEncoding.EncodeToString(payload)}); err != nil {
				return err
			}
		case protocol.ExecRecordExit:
			var exit protocol.ExecExit
			if err := json.Unmarshal(payload, &exit); err != nil {
				return err
			}
			finished = map[string]any{
				"result": "exited", "exit_code": exit.ExitCode, "signaled": exit.Signaled,
				"timed_out": exit.TimedOut, "truncated": exit.Truncated, "duration_ms": exit.DurationMS,
			}
			if err := sendAgentExecFrame(ws, map[string]any{
				"type": "exit", "exitCode": exit.ExitCode, "signaled": exit.Signaled,
				"timedOut": exit.TimedOut, "truncated": exit.Truncated, "durationMs": exit.DurationMS,
			}); err != nil {
				return err
			}
		case protocol.ExecRecordError:
			finished["result"] = "agent_error"
			_ = sendAgentExecFrame(ws, map[string]any{"type": "error", "message": string(payload)})
		}
	}
	finished["duration_ms_total"] = time.Since(started).Milliseconds()
	b.audit(ctx, "agent.exec.finished", entry, finished)
	return nil
}

func (b *agentExecBroker) audit(ctx context.Context, action string, entry agentExecEntry, details map[string]any) {
	if b.deps.Audit == nil {
		return
	}
	encoded, err := json.Marshal(details)
	if err != nil {
		return
	}
	b.deps.Audit(ctx, AgentExecAuditEvent{Action: action, UserID: entry.userID, AgentID: entry.agentID, Details: string(encoded)})
}

func sendAgentExecFrame(ws WSConn, frame map[string]any) error {
	encoded, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	return ws.WriteMessage(1, encoded)
}

func sendAgentExecError(ws WSConn, message string) error {
	_ = sendAgentExecFrame(ws, map[string]any{"type": "error", "message": message})
	return fmt.Errorf("agent exec: %s", message)
}

// writeAgentExecAudit persists a run event. The details are already digests and
// counters, never script text or output, so they are safe to store as they are.
func (r *ServerRuntime) writeAgentExecAudit(ctx context.Context, event AgentExecAuditEvent) {
	if r == nil || r.DB == nil {
		return
	}
	_ = r.DB.Audits().Create(ctx, storage.AuditLog{
		ActorUserID: event.UserID, Action: event.Action, ResourceType: "agent",
		ResourceID: event.AgentID, Details: event.Details,
	})
}

// withAgentExecRoute sends the script-run WebSocket path to the broker and leaves every
// other request to the normal web handler.
func withAgentExecRoute(broker *agentExecBroker, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if broker != nil && strings.HasPrefix(r.URL.Path, agentExecPath) {
			broker.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}
