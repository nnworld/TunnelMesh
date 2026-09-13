package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"strings"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/auth"
	"github.com/tunnelmesh/tunnelmesh/internal/observability"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

var ErrWebSSHSessionUnavailable = errors.New("webssh session is unavailable")

// ErrWebSSHActiveSessionLimit is a sentinel so the API layer can answer the
// 409 documented in docs/api/openapi.yaml instead of a generic 500.
var ErrWebSSHActiveSessionLimit = errors.New("webssh active session limit reached")

type WebSSHSessionLimits struct {
	TicketTTL        time.Duration
	SessionTTL       time.Duration
	MaxActivePerUser int
	OpenTimeout      time.Duration
	IdleTimeout      time.Duration
}

type CreateWebSSHSessionInput struct {
	Username     string
	CredentialID string
}

type WebSSHSessionTicket struct {
	SessionID     string
	Ticket        string
	WebSocketPath string
	// Auth is one-time material the browser uses to authenticate libssh2
	// without prompting. It is nil whenever auto-authentication is impossible.
	Auth *WebSSHAuthPayload
}

// WebSSH authentication kinds. The browser maps them onto libssh2 userauth
// methods: password -> "password", private_key -> "publickey".
const (
	WebSSHAuthKindPassword   = "password"
	WebSSHAuthKindPrivateKey = "private_key"
)

// WebSSHAuthPayload is returned only by the session-create call, only to the
// credential owner, and is never written to the database, the idempotency
// store, the audit log or any response other than that one.
type WebSSHAuthPayload struct {
	Kind         string `json:"kind"`
	CredentialID string `json:"credentialId"`
	Password     string `json:"password,omitempty"`
	PrivateKey   string `json:"privateKey,omitempty"`
	Passphrase   string `json:"passphrase,omitempty"`
}

// CredentialSecretResolver decrypts a credential secret for its owner. It is
// implemented by *CredentialService; depending on the interface keeps the
// WebSSH service free of encryption details and makes the fallback path
// (no resolver configured) explicit and testable.
type CredentialSecretResolver interface {
	GetWithSecret(ctx context.Context, actor auth.Principal, id string) (storage.Credential, *CredentialSecret, error)
}

type WebSSHSessionService struct {
	sessions    storage.WebSSHSessionRepository
	servers     storage.RemoteServerRepository
	credentials storage.CredentialRepository
	agents      storage.AgentRepository
	leases      storage.LeaseRepository
	audits      storage.AuditRepository
	localNodeID string
	limits      WebSSHSessionLimits
	now         func() time.Time
	metrics     *observability.Metrics
	secrets     CredentialSecretResolver
}

func NewWebSSHSessionService(sessions storage.WebSSHSessionRepository, servers storage.RemoteServerRepository, credentials storage.CredentialRepository, agents storage.AgentRepository, leases storage.LeaseRepository, audits storage.AuditRepository, localNodeID string) *WebSSHSessionService {
	return &WebSSHSessionService{
		sessions: sessions, servers: servers, credentials: credentials, agents: agents,
		leases: leases, audits: audits, localNodeID: strings.TrimSpace(localNodeID),
		limits: WebSSHSessionLimits{
			TicketTTL: 30 * time.Second, SessionTTL: 8 * time.Hour, MaxActivePerUser: 5,
			OpenTimeout: 10 * time.Second, IdleTimeout: 5 * time.Minute,
		},
		now: func() time.Time { return time.Now().UTC() },
	}
}

// SetLimits applies validated configuration without exposing mutable service
// internals to callers.
func (s *WebSSHSessionService) SetLimits(limits WebSSHSessionLimits) {
	if s == nil {
		return
	}
	if limits.TicketTTL > 0 {
		s.limits.TicketTTL = limits.TicketTTL
	}
	if limits.SessionTTL > 0 {
		s.limits.SessionTTL = limits.SessionTTL
	}
	if limits.MaxActivePerUser > 0 {
		s.limits.MaxActivePerUser = limits.MaxActivePerUser
	}
	if limits.OpenTimeout > 0 {
		s.limits.OpenTimeout = limits.OpenTimeout
	}
	if limits.IdleTimeout > 0 {
		s.limits.IdleTimeout = limits.IdleTimeout
	}
}

// SetLocalNodeID lets the runtime inject the registered Server identity after
// the API service graph has been constructed.
func (s *WebSSHSessionService) SetLocalNodeID(localNodeID string) {
	if s == nil {
		return
	}
	s.localNodeID = strings.TrimSpace(localNodeID)
}

// SetMetrics installs optional WebSSH-specific collectors.
func (s *WebSSHSessionService) SetMetrics(metrics *observability.Metrics) {
	if s == nil {
		return
	}
	s.metrics = metrics
}

// SetCredentialSecrets installs the owner-scoped secret resolver that enables
// auto-authentication. Without it every session falls back to the manual
// password prompt, which keeps deployments without an encryption key working.
func (s *WebSSHSessionService) SetCredentialSecrets(resolver CredentialSecretResolver) {
	if s == nil {
		return
	}
	s.secrets = resolver
}

func (s *WebSSHSessionService) Create(ctx context.Context, actor auth.Principal, serverID string, input CreateWebSSHSessionInput) (WebSSHSessionTicket, error) {
	if s == nil || s.sessions == nil || s.servers == nil || s.agents == nil || s.leases == nil {
		return WebSSHSessionTicket{}, ErrWebSSHSessionUnavailable
	}
	if s.localNodeID == "" {
		return WebSSHSessionTicket{}, errors.New("webssh local node identity is required")
	}
	username := strings.TrimSpace(input.Username)
	if username == "" || len(username) > 255 {
		return WebSSHSessionTicket{}, errors.New("webssh username is required")
	}
	server, err := s.servers.Get(ctx, serverID)
	if err != nil {
		return WebSSHSessionTicket{}, err
	}
	if server.DeletedAt != nil || !server.Enabled {
		return WebSSHSessionTicket{}, errors.New("remote server is unavailable")
	}
	if !isAdmin(actor) && server.OwnerUserID != actor.UserID {
		return WebSSHSessionTicket{}, ErrResourceForbidden
	}
	agent, err := s.agents.Get(ctx, server.AgentID)
	if err != nil {
		return WebSSHSessionTicket{}, err
	}
	if !agent.Enabled || (!isAdmin(actor) && agent.OwnerUserID != actor.UserID) {
		return WebSSHSessionTicket{}, errors.New("remote server agent is unavailable")
	}
	if input.CredentialID != "" {
		credential, err := s.credentials.Get(ctx, input.CredentialID)
		if err != nil {
			return WebSSHSessionTicket{}, err
		}
		if credential.DeletedAt != nil || !credential.Enabled || credential.OwnerUserID != actor.UserID {
			return WebSSHSessionTicket{}, errors.New("remote server credential is unavailable")
		}
	}
	leases, err := s.leases.ListActiveByAgent(ctx, server.AgentID)
	if err != nil {
		return WebSSHSessionTicket{}, err
	}
	if len(leases) == 0 {
		return WebSSHSessionTicket{}, errors.New("remote server agent is offline")
	}
	now := s.now()
	active, err := s.sessions.CountActiveByOwner(ctx, actor.UserID, now)
	if err != nil {
		return WebSSHSessionTicket{}, err
	}
	if active >= s.limits.MaxActivePerUser {
		return WebSSHSessionTicket{}, ErrWebSSHActiveSessionLimit
	}
	ticketBytes := make([]byte, 32)
	if _, err := rand.Read(ticketBytes); err != nil {
		return WebSSHSessionTicket{}, err
	}
	ticket := base64.RawURLEncoding.EncodeToString(ticketBytes)
	sum := sha256.Sum256([]byte(ticket))
	session := storage.WebSSHSession{
		OwnerUserID: actor.UserID, RemoteServerID: server.ID, AgentID: server.AgentID,
		OwnerNodeID: s.localNodeID, TicketHash: base64.RawURLEncoding.EncodeToString(sum[:]),
		TicketExpiresAt: now.Add(s.limits.TicketTTL), Status: storage.WebSSHSessionPending,
		CreatedAt: now, ExpiresAt: now.Add(s.limits.SessionTTL),
	}
	created, err := s.sessions.Create(ctx, session)
	if err != nil {
		return WebSSHSessionTicket{}, err
	}
	s.writeAudit(ctx, actor, "webssh.session.created", created.ID, "created")
	authPayload := s.resolveAuth(ctx, actor, input.CredentialID, created.ID)
	if s.metrics != nil {
		s.metrics.ObserveWebSSHTicketCreated()
	}
	return WebSSHSessionTicket{SessionID: created.ID, Ticket: ticket, WebSocketPath: "/ws/webssh/" + created.ID, Auth: authPayload}, nil
}

// resolveAuth decrypts the bound credential secret for the session owner.
// Every failure mode degrades to manual authentication instead of failing the
// session: a missing resolver, a public-key-only credential, a missing
// encryption key or a corrupt blob are all recoverable by typing a password.
func (s *WebSSHSessionService) resolveAuth(ctx context.Context, actor auth.Principal, credentialID, sessionID string) *WebSSHAuthPayload {
	if credentialID == "" || s.secrets == nil {
		return nil
	}
	credential, secret, err := s.secrets.GetWithSecret(ctx, actor, credentialID)
	if err != nil || secret == nil {
		return nil
	}
	payload := &WebSSHAuthPayload{CredentialID: credential.ID}
	switch {
	case secret.Password != "":
		payload.Kind, payload.Password = WebSSHAuthKindPassword, secret.Password
	case secret.PrivateKey != "":
		payload.Kind, payload.PrivateKey, payload.Passphrase = WebSSHAuthKindPrivateKey, secret.PrivateKey, secret.Passphrase
	default:
		return nil
	}
	s.writeCredentialRevealAudit(ctx, actor, credentialID, sessionID)
	return payload
}

// writeCredentialRevealAudit records that plaintext left the server. Only the
// identifiers are stored; the values never are.
func (s *WebSSHSessionService) writeCredentialRevealAudit(ctx context.Context, actor auth.Principal, credentialID, sessionID string) {
	if s == nil || s.audits == nil {
		return
	}
	_ = s.audits.Create(ctx, storage.AuditLog{
		ActorUserID: actor.UserID, Action: "credential.revealed", ResourceType: "credential",
		ResourceID: credentialID, Details: `{"sessionId":"` + sessionID + `"}`,
	})
}

func (s *WebSSHSessionService) Get(ctx context.Context, actor auth.Principal, sessionID string) (storage.WebSSHSession, error) {
	if s == nil || s.sessions == nil {
		return storage.WebSSHSession{}, ErrWebSSHSessionUnavailable
	}
	session, err := s.sessions.Get(ctx, sessionID)
	if err != nil {
		return storage.WebSSHSession{}, err
	}
	if !isAdmin(actor) && session.OwnerUserID != actor.UserID {
		return storage.WebSSHSession{}, ErrResourceForbidden
	}
	return session, nil
}

// List returns the caller's own active sessions so the admin console can offer a
// self-service disconnect when the per-user active-session quota is exhausted.
// The owner scope is forced here rather than read from the request: cross-user
// session management would need its own authorization and audit model.
func (s *WebSSHSessionService) List(ctx context.Context, actor auth.Principal, cursor string, limit int) (storage.Page[storage.WebSSHSession], error) {
	if s == nil || s.sessions == nil {
		return storage.Page[storage.WebSSHSession]{}, ErrWebSSHSessionUnavailable
	}
	return s.sessions.ListActivePage(ctx, actor.UserID, s.now(), cursor, limit)
}

func (s *WebSSHSessionService) Close(ctx context.Context, actor auth.Principal, sessionID, reason string) error {
	if _, err := s.Get(ctx, actor, sessionID); err != nil {
		return err
	}
	if reason == "" {
		reason = "closed"
	}
	if err := s.sessions.Close(ctx, sessionID, reason, s.now()); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	s.writeAudit(ctx, actor, "webssh.session.closed", sessionID, reason)
	return nil
}

// CloseDurable records terminal session state without an HTTP principal. It
// is used by transport failure paths and cannot bypass repository validation.
func (s *WebSSHSessionService) CloseDurable(ctx context.Context, sessionID, reason string) error {
	if s == nil || s.sessions == nil {
		return ErrWebSSHSessionUnavailable
	}
	if reason == "" {
		reason = "closed"
	}
	if err := s.sessions.Close(ctx, sessionID, reason, s.now()); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return nil
}

func (s *WebSSHSessionService) AuthenticateTicket(ctx context.Context, sessionID, ticket string, now time.Time) (storage.WebSSHSession, storage.RemoteServer, error) {
	if s == nil || s.sessions == nil || s.servers == nil {
		return storage.WebSSHSession{}, storage.RemoteServer{}, ErrWebSSHSessionUnavailable
	}
	sum := sha256.Sum256([]byte(ticket))
	session, err := s.sessions.ConsumeTicket(ctx, sessionID, base64.RawURLEncoding.EncodeToString(sum[:]), now)
	if err != nil {
		if errors.Is(err, storage.ErrWebSSHTicketInvalid) && s.metrics != nil {
			s.metrics.ObserveWebSSHTicketReuse(s.webSSHTicketRejectReason(ctx, sessionID, now))
		}
		return storage.WebSSHSession{}, storage.RemoteServer{}, err
	}
	server, err := s.servers.Get(ctx, session.RemoteServerID)
	if err != nil {
		_ = s.sessions.Close(ctx, sessionID, "remote_server_unavailable", now)
		return storage.WebSSHSession{}, storage.RemoteServer{}, err
	}
	return session, server, nil
}

func (s *WebSSHSessionService) webSSHTicketRejectReason(ctx context.Context, sessionID string, now time.Time) string {
	session, err := s.sessions.Get(ctx, sessionID)
	if err != nil {
		return "invalid"
	}
	switch session.Status {
	case storage.WebSSHSessionActive, storage.WebSSHSessionClosed, storage.WebSSHSessionExpired:
		return "reused"
	case storage.WebSSHSessionPending:
		if !session.TicketExpiresAt.After(now) {
			return "expired"
		}
	}
	return "invalid"
}

func (s *WebSSHSessionService) writeAudit(ctx context.Context, actor auth.Principal, action, id, result string) {
	if s == nil || s.audits == nil {
		return
	}
	_ = s.audits.Create(ctx, storage.AuditLog{
		ActorUserID: actor.UserID, Action: action, ResourceType: "webssh_session",
		ResourceID: id, Details: `{"result":"` + result + `"}`,
	})
}

// CloseStaleLocalSessions closes active sessions owned by this node. It is
// called once during startup, before tickets can be issued, so sessions left
// by a previous process never block the per-user active-session quota.
func (s *WebSSHSessionService) CloseStaleLocalSessions(ctx context.Context, now time.Time, reason string) (int64, error) {
	if s == nil || s.sessions == nil {
		return 0, ErrWebSSHSessionUnavailable
	}
	if s.localNodeID == "" {
		return 0, errors.New("webssh local node identity is required")
	}
	return s.sessions.CloseActiveByNode(ctx, s.localNodeID, now, reason)
}
