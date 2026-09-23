package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/auth"
	"github.com/tunnelmesh/tunnelmesh/internal/config"
	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
	"github.com/tunnelmesh/tunnelmesh/internal/proxyentry"
	"github.com/tunnelmesh/tunnelmesh/internal/routing"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

// API is the HTTP application service. Repositories are kept behind the
// storage interfaces so the handler remains usable with SQLite, MySQL, and
// small in-memory fakes in tests.
type API struct {
	DB                  *storage.DB
	Auth                *auth.AuthService
	users               storage.UserRepository
	agents              storage.AgentRepository
	policies            storage.PolicyRepository
	tunnels             storage.TunnelRepository
	audits              storage.AuditRepository
	idem                storage.IdempotencyRepository
	dashboard           storage.DashboardRepository
	routeMu             sync.Mutex
	service             *apiService
	tokenService        *TokenService
	accounts            *auth.AccountService
	serverNodes         *ServerNodeService
	traceroute          *TracerouteService
	probeService        *ProbeService
	agentSessions       *AgentSessionManager
	localAgentRelay     *AgentRelayTransport
	clusterConnections  AgentConnectionLister
	connectionCloser    AgentConnectionCloseService
	clientInstances     storage.ClientInstanceRepository
	clientConnections   storage.ClientConnectionRepository
	clientCloser        ClientConnectionCloseService
	credentialService   *CredentialService
	remoteServerService *RemoteServerService
	websshService       *WebSSHSessionService
	websshCloser        WebSSHConnectionCloseService
	localNodeID         string
	downloads           config.DownloadsConfig
	identity            *IdentityServices
	// vpnPeerService is nil until the runtime installs it with SetVPN, because
	// assembling it needs the loaded server configuration and this node's
	// identity, neither of which NewAPI is given. The handlers answer 503 while
	// it is nil, which distinguishes "not wired" from "no such endpoint".
	vpnPeerService *VPNPeerService
	// vpnDataPlane is the running gateway, and it is nil in every binary that is
	// not built with -tags vpn or on a node with server.vpn disabled. Holding the
	// interface rather than the concrete type is what keeps the untagged build
	// from reaching a field only a tagged build has. The endpoints that need a
	// live data plane answer 501 while it is nil instead of an empty list that
	// would read as "nothing is happening".
	vpnDataPlane VPNDataPlane
	// vpnConfig and vpnNodeID are the node view the gateway status endpoint reports
	// when no data plane is installed, which is every untagged binary and every node
	// with server.vpn disabled. SetVPN records them, because that is the only place
	// the loaded server configuration reaches the API.
	vpnConfig config.VPNConfig
	vpnNodeID string
	// trustedProxyList decides whether X-Forwarded-For is believed. It is empty by
	// default, which means the direct peer address is always used.
	trustedProxyList []string
}

// apiService is the application layer between HTTP handlers and storage. It
// owns validation and repository orchestration; handlers are responsible only
// for transport decoding, authorization, and response rendering.
type apiService struct {
	agents   storage.AgentRepository
	metadata *AgentMetadataService
	policies storage.PolicyRepository
	tunnels  storage.TunnelRepository
	audits   storage.AuditRepository
}

// APIServer is kept as a descriptive alias for integrations that call the
// management application a server rather than an API.
type APIServer = API

func NewAPI(db *storage.DB, authService *auth.AuthService) *API {
	if authService == nil && db != nil {
		authService = auth.NewAuthService(db)
	}
	a := &API{DB: db, Auth: authService}
	if db != nil {
		a.users, a.agents, a.policies, a.tunnels, a.audits, a.idem, a.dashboard = db.Users(), db.Agents(), db.Policies(), db.Tunnels(), db.Audits(), db.Idempotency(), db.Dashboard()
		a.service = &apiService{agents: a.agents, metadata: NewAgentMetadataService(db.Metadata()), policies: a.policies, tunnels: a.tunnels, audits: a.audits}
		a.tokenService = NewTokenService(db)
		a.accounts = auth.NewAccountService(db)
		a.serverNodes = NewServerNodeService(db)
		a.traceroute = NewTracerouteService(db)
		a.probeService = NewProbeService(db)
		a.clientInstances = db.ClientInstances()
		a.clientConnections = db.ClientConnections()
		a.credentialService = NewCredentialService(db.Credentials(), db.Agents(), db.Audits())
		a.remoteServerService = NewRemoteServerService(db.RemoteServers(), db.Credentials(), db.Agents(), db.Policies(), db.Audits(), db.Leases())
		a.websshService = NewWebSSHSessionService(db.WebSSHSessions(), db.RemoteServers(), db.Credentials(), db.Agents(), db.Leases(), db.Audits(), "local")
		// Auto-authentication needs owner-scoped secret decryption; when no
		// encryption key is configured the resolver stays available but returns
		// no secret, so sessions degrade to the manual password prompt.
		a.websshService.SetCredentialSecrets(a.credentialService)
	}
	return a
}

// SetAgentConnections lets the runtime expose process-local connection state
// alongside durable metadata. API tests and embedders may omit it; the
// metadata endpoint then reports only persisted Agent instances.
func (a *API) SetAgentConnections(sessions *AgentSessionManager, relay *AgentRelayTransport) {
	if a == nil {
		return
	}
	a.agentSessions = sessions
	a.localAgentRelay = relay
}

// SetWebSSHConnectionCloser installs the process/cluster connection closer.
// It is optional so API-level tests and embedders can operate without a live
// broker while still exercising durable session semantics.
func (a *API) SetWebSSHConnectionCloser(closer WebSSHConnectionCloseService) {
	if a == nil {
		return
	}
	a.websshCloser = closer
}

// SetWebSSHLocalNodeID propagates the runtime node identity into ticket
// creation so cluster ownership is recorded before a browser connects.
// SetTrustedProxies installs the reverse-proxy addresses whose X-Forwarded-For
// header may be believed when resolving a management-API client IP. The list is
// empty by default, which means the direct peer address is always used and a
// spoofed header cannot influence login throttling or audit records.
func (a *API) SetTrustedProxies(proxies []string) {
	if a == nil {
		return
	}
	a.trustedProxyList = append([]string(nil), proxies...)
}

func (a *API) SetWebSSHLocalNodeID(localNodeID string) {
	if a == nil {
		return
	}
	a.localNodeID = strings.TrimSpace(localNodeID)
	if a.websshService != nil {
		a.websshService.SetLocalNodeID(localNodeID)
	}
}

func (s *apiService) CreateAgent(ctx context.Context, owner string, req agentRequest) (storage.Agent, error) {
	if strings.TrimSpace(req.Name) == "" {
		return storage.Agent{}, errors.New("name is required")
	}
	v := storage.Agent{Name: strings.TrimSpace(req.Name), OwnerUserID: owner, Capabilities: encodeStrings(req.Capabilities), Enabled: true}
	if req.Enabled != nil {
		v.Enabled = *req.Enabled
	}
	if err := s.agents.Create(ctx, v); err != nil {
		return storage.Agent{}, err
	}
	page, err := s.agents.List(ctx, "", 500)
	if err != nil {
		return storage.Agent{}, err
	}
	var latest storage.Agent
	for _, found := range page.Items {
		if found.OwnerUserID == owner && found.Name == v.Name && (latest.ID == "" || found.CreatedAt.After(latest.CreatedAt)) {
			latest = found
		}
	}
	if latest.ID != "" {
		return latest, nil
	}
	return storage.Agent{}, sql.ErrNoRows
}

func (s *apiService) CreateTunnel(ctx context.Context, v storage.Tunnel) (storage.Tunnel, error) {
	if v.Protocol == storage.ProtocolHTTPProxy {
		// The real target of a tp-* route comes from each proxied request, so
		// the wildcard sentinel is the only valid stored value.
		if v.AgentID == "" || v.TargetHost != storage.ProxyTargetWildcard || v.TargetPort != 0 {
			return storage.Tunnel{}, errors.New("http-proxy routes require an agentId and the wildcard sentinel target")
		}
	} else if v.AgentID == "" || v.TargetHost == "" || v.TargetPort < 1 || v.TargetPort > 65535 {
		return storage.Tunnel{}, errors.New("agentId, targetHost and valid targetPort are required")
	}
	if err := routing.ValidateDomainPattern(v.Domain); err != nil {
		return storage.Tunnel{}, err
	}
	if err := s.tunnels.Create(ctx, v); err != nil {
		return storage.Tunnel{}, err
	}
	page, err := s.tunnels.List(ctx, "", 500)
	if err != nil {
		return storage.Tunnel{}, err
	}
	var latest storage.Tunnel
	for _, found := range page.Items {
		if found.AgentID == v.AgentID && found.Domain == v.Domain && found.PathPrefix == v.PathPrefix && (latest.ID == "" || found.CreatedAt.After(latest.CreatedAt)) {
			latest = found
		}
	}
	if latest.ID != "" {
		return latest, nil
	}
	return storage.Tunnel{}, sql.ErrNoRows
}
func (s *apiService) GetAgent(ctx context.Context, id string) (storage.Agent, error) {
	return s.agents.Get(ctx, id)
}
func (s *apiService) GetAgentMetadataView(ctx context.Context, id string) (AgentMetadataView, error) {
	if s == nil || s.metadata == nil {
		return AgentMetadataView{}, errors.New("metadata service unavailable")
	}
	return s.metadata.GetView(ctx, id)
}
func (s *apiService) ListAgents(ctx context.Context, cursor string, limit int) (storage.Page[storage.Agent], error) {
	return s.agents.List(ctx, cursor, limit)
}
func (s *apiService) UpdateAgent(ctx context.Context, v storage.Agent) error {
	return s.agents.Update(ctx, v)
}
func (s *apiService) DeleteAgent(ctx context.Context, id string) error {
	return s.agents.Delete(ctx, id)
}
func (s *apiService) GetPolicy(ctx context.Context, id string) (storage.AgentPolicy, error) {
	return s.policies.Get(ctx, id)
}
func (s *apiService) ListPolicies(ctx context.Context, agentID, cursor string, limit int) (storage.Page[storage.AgentPolicy], error) {
	return s.policies.ListByAgent(ctx, agentID, cursor, limit)
}
func (s *apiService) ListPoliciesStatus(ctx context.Context, agentID, cursor string, limit int, status storage.PolicyStatus) (storage.Page[storage.AgentPolicy], error) {
	return s.policies.ListByAgentStatus(ctx, agentID, cursor, limit, status)
}
func (s *apiService) CreatePolicy(ctx context.Context, v storage.AgentPolicy) error {
	return s.policies.Create(ctx, v)
}
func (s *apiService) UpdatePolicy(ctx context.Context, v storage.AgentPolicy) error {
	return s.policies.Update(ctx, v)
}
func (s *apiService) DeletePolicy(ctx context.Context, id string) error {
	return s.policies.Delete(ctx, id)
}
func (s *apiService) RestorePolicy(ctx context.Context, id string) error {
	return s.policies.Restore(ctx, id)
}
func (s *apiService) GetTunnel(ctx context.Context, id string) (storage.Tunnel, error) {
	return s.tunnels.Get(ctx, id)
}
func (s *apiService) ListTunnels(ctx context.Context, cursor string, limit int) (storage.Page[storage.Tunnel], error) {
	return s.tunnels.List(ctx, cursor, limit)
}
func (s *apiService) UpdateTunnel(ctx context.Context, v storage.Tunnel) error {
	if err := routing.ValidateDomainPattern(v.Domain); err != nil {
		return err
	}
	return s.tunnels.Update(ctx, v)
}
func (s *apiService) DeleteTunnel(ctx context.Context, id string) error {
	return s.tunnels.Delete(ctx, id)
}
func (s *apiService) ListAudits(ctx context.Context, filter storage.AuditFilter, cursor string, limit int) (storage.Page[storage.AuditLog], error) {
	return s.audits.List(ctx, filter, cursor, limit)
}
func (s *apiService) CreateAudit(ctx context.Context, v storage.AuditLog) error {
	return s.audits.Create(ctx, v)
}

// NewAPIHandler is a convenience for net/http and embedders.
func NewAPIHandler(db *storage.DB, authService *auth.AuthService) http.Handler {
	return NewAPI(db, authService)
}

func NewServerAPI(db *storage.DB, authService *auth.AuthService) *API {
	return NewAPI(db, authService)
}

func (a *API) Handler() http.Handler { return a }

type apiEnvelope struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data any    `json:"data"`
}

func writeJSON(w http.ResponseWriter, status int, data any) []byte {
	b, _ := json.Marshal(apiEnvelope{Code: status, Msg: http.StatusText(status), Data: data})
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(b)
	return b
}

func writeAPIError(w http.ResponseWriter, status int, msg string) {
	if msg == "" {
		msg = http.StatusText(status)
	}
	_ = writeJSON(w, status, map[string]any{"error": msg})
}

func (a *API) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if a == nil || a.Auth == nil || a.agents == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "api unavailable")
		return
	}
	path := strings.TrimSuffix(r.URL.Path, "/")
	if path == "" {
		path = "/"
	}
	// The unauthenticated identity surface is matched before the bearer check so
	// login, the MFA step, and the OIDC redirect endpoints stay reachable without
	// a session. Everything else under /auth/ falls through to the authenticated
	// dispatch below.
	if a.handlePublicAuth(w, r, path) {
		return
	}
	if !strings.HasPrefix(path, "/api/v1/") {
		writeAPIError(w, http.StatusNotFound, "not found")
		return
	}
	p, err := a.authenticate(r)
	if err != nil {
		writeAPIError(w, http.StatusUnauthorized, err.Error())
		return
	}
	r = r.WithContext(withPrincipal(r.Context(), p))
	parts := splitPath(path)
	if len(parts) == 0 {
		writeJSON(w, http.StatusOK, map[string]string{"service": "tunnelmesh"})
		return
	}
	switch parts[0] {
	case "auth":
		a.handleAuth(w, r, p, parts[1:])
	case "sso":
		a.handleSSO(w, r, p, parts[1:])
	case "credentials":
		a.handleCredentials(w, r, p, parts[1:])
	case "remote-servers":
		a.handleRemoteServers(w, r, p, parts[1:])
	case "agents":
		a.handleAgents(w, r, p, parts[1:])
	case "routes", "tunnels":
		a.handleTunnels(w, r, p, parts[1:])
	case "audit-logs", "audits":
		a.handleAudits(w, r, p)
	case "tokens":
		a.handleTokens(w, r, p, parts[1:])
	case "server-nodes":
		a.handleServerNodes(w, r, p, parts[1:])
	case "clients":
		a.handleClients(w, r, p, parts[1:])
	case "downloads":
		if len(parts) == 1 {
			a.handleDownloads(w, r, p)
			return
		}
		writeAPIError(w, http.StatusNotFound, "not found")
	case "users":
		a.handleUsers(w, r, p, parts[1:])
	case "dashboard":
		a.handleDashboard(w, r, p, parts[1:])
	case "traces":
		a.handleTraces(w, r, p, parts[1:])
	case "vpn-peers":
		a.handleVPNPeers(w, r, p, parts[1:])
	case "vpn-nodes":
		a.handleVPNNodes(w, r, p, parts[1:])
	case "ssh-sessions":
		if len(parts) == 1 {
			if r.Method == http.MethodGet {
				a.listWebSSHSessions(w, r, p)
				return
			}
			writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if len(parts) == 2 && parts[0] == "ssh-sessions" && parts[1] != "" {
			sessionID := parts[1]
			switch r.Method {
			case http.MethodGet:
				a.getWebSSHSession(w, r, p, sessionID)
			case http.MethodDelete:
				a.closeWebSSHSession(w, r, p, sessionID)
			default:
				writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
			}
			return
		}
		writeAPIError(w, http.StatusNotFound, "not found")
	default:
		writeAPIError(w, http.StatusNotFound, "not found")
	}
}

func splitPath(path string) []string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) >= 2 && parts[0] == "api" && parts[1] == "v1" {
		parts = parts[2:]
	}
	if len(parts) == 1 && parts[0] == "" {
		return nil
	}
	return parts
}

func (a *API) authenticate(r *http.Request) (auth.Principal, error) {
	if a.Auth == nil {
		return auth.Principal{}, auth.ErrUnauthenticated
	}
	return a.Auth.ValidateToken(r.Context(), bearerToken(r))
}

func (a *API) handleAgents(w http.ResponseWriter, r *http.Request, p auth.Principal, parts []string) {
	if len(parts) == 0 {
		switch r.Method {
		case http.MethodGet:
			a.listAgents(w, r, p)
		case http.MethodPost:
			if !isAdmin(p) {
				writeAPIError(w, http.StatusForbidden, "admin role required")
				return
			}
			a.createAgent(w, r, p)
		default:
			writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
		return
	}
	id := parts[0]
	if len(parts) >= 2 && parts[1] == "trace" {
		a.handleAgentTrace(w, r, p, id)
		return
	}
	if len(parts) >= 2 && (parts[1] == "diagnose" || parts[1] == "probes") {
		a.handleAgentProbes(w, r, p, id, parts[1])
		return
	}
	if len(parts) >= 2 && parts[1] == "metadata" {
		a.handleAgentMetadata(w, r, p, id)
		return
	}
	if len(parts) >= 2 && parts[1] == "connections" {
		a.handleAgentConnections(w, r, p, id, parts[2:])
		return
	}
	if len(parts) >= 2 && parts[1] == "policies" {
		a.handlePolicies(w, r, p, id, parts[2:])
		return
	}
	agent, err := a.service.GetAgent(r.Context(), id)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	if !isAdmin(p) && agent.OwnerUserID != p.UserID {
		writeAPIError(w, http.StatusForbidden, "forbidden")
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, publicAgent(agent))
	case http.MethodPut, http.MethodPatch:
		if !isAdmin(p) {
			writeAPIError(w, http.StatusForbidden, "admin role required")
			return
		}
		var req agentRequest
		if err := decodeJSON(r, &req); err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		if req.Name != "" {
			agent.Name = strings.TrimSpace(req.Name)
		}
		if req.Capabilities != nil {
			agent.Capabilities = encodeStrings(req.Capabilities)
		}
		if req.Enabled != nil {
			agent.Enabled = *req.Enabled
		}
		agent.UpdatedAt = time.Now().UTC()
		if err := a.service.UpdateAgent(r.Context(), agent); err != nil {
			writeStorageError(w, err)
			return
		}
		a.audit(r.Context(), p, "agent.updated", "agent", agent.ID)
		writeJSON(w, http.StatusOK, publicAgent(agent))
	case http.MethodDelete:
		if !isAdmin(p) {
			writeAPIError(w, http.StatusForbidden, "admin role required")
			return
		}
		if err := a.service.DeleteAgent(r.Context(), id); err != nil {
			writeStorageError(w, err)
			return
		}
		a.audit(r.Context(), p, "agent.deleted", "agent", id)
		writeJSON(w, http.StatusOK, map[string]string{"id": id})
	default:
		writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *API) handleAgentProbes(w http.ResponseWriter, r *http.Request, p auth.Principal, agentID, action string) {
	if a.probeService == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "probe service unavailable")
		return
	}
	if action == "probes" {
		if r.Method != http.MethodGet {
			writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		agent, err := a.service.GetAgent(r.Context(), agentID)
		if err != nil {
			writeStorageError(w, err)
			return
		}
		if !isAdmin(p) && agent.OwnerUserID != p.UserID {
			writeAPIError(w, http.StatusForbidden, "forbidden")
			return
		}
		page, err := a.DB.ProbeResults().ListByAgent(r.Context(), agentID, r.URL.Query().Get("cursor"), queryLimit(r))
		if err != nil {
			writeStorageError(w, err)
			return
		}
		items := make([]any, len(page.Items))
		for i := range page.Items {
			items[i] = page.Items[i]
		}
		writeJSON(w, http.StatusOK, pageData(items, page.NextCursor, page.HasMore))
		return
	}
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var input struct {
		Kind      string `json:"kind"`
		Host      string `json:"host"`
		Port      int    `json:"port"`
		TimeoutMs int    `json:"timeoutMs"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	timeout := time.Duration(input.TimeoutMs) * time.Millisecond
	result, err := a.probeService.Diagnose(r.Context(), p, agentID, DiagnoseRequest{Kind: input.Kind, Host: input.Host, Port: input.Port, Timeout: timeout})
	if err != nil {
		if errors.Is(err, auth.ErrForbidden) {
			writeAPIError(w, http.StatusForbidden, "forbidden")
		} else {
			writeStorageError(w, err)
		}
		return
	}
	a.audit(r.Context(), p, "agent.diagnose", "agent", agentID)
	writeJSON(w, http.StatusOK, result)
}

func (a *API) handleAgentTrace(w http.ResponseWriter, r *http.Request, p auth.Principal, agentID string) {
	if r.Method != http.MethodPost || a.traceroute == nil {
		writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	agent, err := a.service.GetAgent(r.Context(), agentID)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	if !isAdmin(p) && agent.OwnerUserID != p.UserID {
		writeAPIError(w, http.StatusForbidden, "forbidden")
		return
	}
	var request protocol.TraceRequest
	if err := decodeJSON(r, &request); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	request.AgentID = agentID
	includeSensitive := request.IncludeSensitive && isAdmin(p)
	if request.IncludeSensitive && !isAdmin(p) {
		writeAPIError(w, http.StatusForbidden, "admin role required for sensitive trace fields")
		return
	}
	if request.IncludeSecrets {
		writeAPIError(w, http.StatusForbidden, "trace secrets require the token reveal endpoint")
		return
	}
	result, err := a.traceroute.StartForUser(r.Context(), p.UserID, request, includeSensitive, false)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	a.audit(r.Context(), p, "agent.trace", "agent", agentID)
	if includeSensitive {
		w.Header().Set("Cache-Control", "no-store")
	}
	writeJSON(w, http.StatusOK, result)
}

func (a *API) handleTraces(w http.ResponseWriter, r *http.Request, p auth.Principal, parts []string) {
	if r.Method != http.MethodGet || len(parts) != 1 || a.traceroute == nil {
		writeAPIError(w, http.StatusNotFound, "not found")
		return
	}
	result, err := a.traceroute.GetForUser(r.Context(), parts[0], p.UserID, isAdmin(p))
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "trace not found")
		return
	}
	if result.TraceID == "" {
		writeAPIError(w, http.StatusNotFound, "trace not found")
		return
	}
	if isAdmin(p) {
		w.Header().Set("Cache-Control", "no-store")
	}
	writeJSON(w, http.StatusOK, result)
}

type agentMetadataResponse struct {
	AgentID     string                          `json:"agentId"`
	InstanceID  string                          `json:"instanceId"`
	NodeID      string                          `json:"nodeId"`
	Epoch       int64                           `json:"epoch"`
	Revision    int64                           `json:"revision"`
	Stale       bool                            `json:"stale"`
	ReportedAt  time.Time                       `json:"reportedAt"`
	UpdatedAt   time.Time                       `json:"updatedAt"`
	Items       []MetadataItem                  `json:"items"`
	Instances   []agentMetadataInstanceResponse `json:"instances"`
	Connections []agentConnectionResponse       `json:"connections"`
}

type agentMetadataInstanceResponse struct {
	InstanceID      string         `json:"instanceId"`
	NodeID          string         `json:"nodeId"`
	Epoch           int64          `json:"epoch"`
	Revision        int64          `json:"revision"`
	Stale           bool           `json:"stale"`
	ReportedAt      time.Time      `json:"reportedAt"`
	UpdatedAt       time.Time      `json:"updatedAt"`
	Items           []MetadataItem `json:"items"`
	ConnectionCount int            `json:"connectionCount"`
}

type agentConnectionResponse struct {
	AgentID         string    `json:"agentId"`
	InstanceID      string    `json:"instanceId"`
	NodeID          string    `json:"nodeId"`
	ConnectionID    string    `json:"connectionId"`
	Epoch           int64     `json:"epoch"`
	ConnectionEpoch int64     `json:"connectionEpoch"`
	ServerNodeID    string    `json:"serverNodeId"`
	Healthy         bool      `json:"healthy"`
	ActiveStreams   int       `json:"activeStreams"`
	LastHeartbeatAt time.Time `json:"lastHeartbeatAt"`
}

func (a *API) handleAgentMetadata(w http.ResponseWriter, r *http.Request, p auth.Principal, agentID string) {
	// Resolve and authorize the parent agent before looking up metadata so a
	// caller cannot use metadata existence to probe another user's resources.
	agent, err := a.service.GetAgent(r.Context(), agentID)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	if !isAdmin(p) && agent.OwnerUserID != p.UserID {
		writeAPIError(w, http.StatusForbidden, "forbidden")
		return
	}
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	view, err := a.service.GetAgentMetadataView(r.Context(), agentID)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	if view.Stale && !strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("includeStale")), "true") {
		writeAPIError(w, http.StatusNotFound, "metadata not found")
		return
	}
	data := agentMetadataResponse{
		AgentID: view.AgentID, InstanceID: view.InstanceID, NodeID: view.NodeID, Epoch: view.Epoch, Revision: view.Revision,
		Stale: view.Stale, ReportedAt: view.ReportedAt, UpdatedAt: view.UpdatedAt, Items: view.Items,
	}
	for _, instance := range view.Instances {
		connectionCount := 0
		for _, connection := range a.agentConnections(agentID) {
			if connection.InstanceID == instance.InstanceID {
				connectionCount++
			}
		}
		data.Instances = append(data.Instances, agentMetadataInstanceResponse{
			InstanceID: instance.InstanceID, NodeID: instance.NodeID, Epoch: instance.Epoch, Revision: instance.Revision,
			Stale: instance.Stale, ReportedAt: instance.ReportedAt, UpdatedAt: instance.UpdatedAt, Items: instance.Items,
			ConnectionCount: connectionCount,
		})
	}
	data.Connections = a.agentConnections(agentID)
	a.audit(r.Context(), p, "agent.metadata.read", "agent_runtime_metadata", agentID)
	writeJSON(w, http.StatusOK, data)
}

func (a *API) agentConnections(agentID string) []agentConnectionResponse {
	if a == nil || a.agentSessions == nil {
		return nil
	}
	sessions := a.agentSessions.List(agentID)
	out := make([]agentConnectionResponse, 0, len(sessions))
	for _, session := range sessions {
		activeStreams := 0
		if a.localAgentRelay != nil {
			activeStreams = a.localAgentRelay.ActiveStreams(agentID, session.ConnectionID)
		}
		out = append(out, agentConnectionResponse{
			AgentID: session.AgentID, InstanceID: session.InstanceID, NodeID: session.NodeID,
			ConnectionID: session.ConnectionID, Epoch: session.Epoch, ConnectionEpoch: session.ConnectionEpoch,
			ServerNodeID: a.agentSessions.ServerNodeID(), Healthy: session.Healthy(),
			ActiveStreams: activeStreams, LastHeartbeatAt: session.LastHeartbeat(),
		})
	}
	return out
}

type agentRequest struct {
	Name         string   `json:"name"`
	Capabilities []string `json:"capabilities"`
	Enabled      *bool    `json:"enabled"`
}

func (a *API) listAgents(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	page, err := a.listAgentsForOwner(r.Context(), p, r.URL.Query().Get("cursor"), queryLimit(r))
	if err != nil {
		writeStorageError(w, err)
		return
	}
	items := make([]any, 0, len(page.Items))
	for _, agent := range page.Items {
		items = append(items, publicAgent(agent))
	}
	writeJSON(w, http.StatusOK, pageData(items, page.NextCursor, page.HasMore))
}

func (a *API) createAgent(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	var req agentRequest
	if err := decodeJSON(r, &req); err != nil || strings.TrimSpace(req.Name) == "" {
		writeAPIError(w, http.StatusBadRequest, "name is required")
		return
	}
	status, data, err := a.mutate(r, p, func() (int, any, error) {
		if a.service == nil {
			return 0, nil, errors.New("api service unavailable")
		}
		created, err := a.service.CreateAgent(r.Context(), p.UserID, req)
		if err != nil {
			return 0, nil, err
		}
		a.audit(r.Context(), p, "agent.created", "agent", created.ID)
		return http.StatusCreated, publicAgent(created), nil
	})
	if err != nil {
		writeStorageError(w, err)
		return
	}
	writeStored(w, status, data)
}

func (a *API) handlePolicies(w http.ResponseWriter, r *http.Request, p auth.Principal, agentID string, parts []string) {
	agent, err := a.service.GetAgent(r.Context(), agentID)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	if !isAdmin(p) && agent.OwnerUserID != p.UserID {
		writeAPIError(w, http.StatusForbidden, "forbidden")
		return
	}
	if len(parts) == 0 {
		if r.Method == http.MethodGet {
			status := storage.PolicyStatusActive
			if rawStatus := r.URL.Query().Get("status"); rawStatus != "" && rawStatus != string(storage.PolicyStatusActive) {
				status = storage.PolicyStatus(rawStatus)
				if !isAdmin(p) {
					writeAPIError(w, http.StatusForbidden, "admin role required")
					return
				}
			}
			if status != storage.PolicyStatusActive && status != storage.PolicyStatusDeleted && status != storage.PolicyStatusAll {
				writeAPIError(w, http.StatusBadRequest, "invalid policy status")
				return
			}
			page, err := a.service.ListPoliciesStatus(r.Context(), agentID, r.URL.Query().Get("cursor"), queryLimit(r), status)
			if err != nil {
				writeStorageError(w, err)
				return
			}
			items := make([]any, len(page.Items))
			for i := range page.Items {
				items[i] = publicPolicy(page.Items[i])
			}
			writeJSON(w, http.StatusOK, pageData(items, page.NextCursor, page.HasMore))
			return
		}
		if r.Method == http.MethodPost {
			if !isAdmin(p) {
				writeAPIError(w, http.StatusForbidden, "admin role required")
				return
			}
			var req policyRequest
			if err := decodeJSON(r, &req); err != nil {
				writeAPIError(w, http.StatusBadRequest, "invalid JSON")
				return
			}
			req.TargetHost = strings.TrimSpace(req.TargetHost)
			if req.TargetHost == "" || req.TargetPort == nil || !validAgentPolicyTargetHost(req.TargetHost) || !validAgentPolicyTargetPort(*req.TargetPort) {
				writeAPIError(w, http.StatusBadRequest, "target host and port are required")
				return
			}
			if _, err := routing.NewPolicy(req.AllowedCIDRs, req.AllowedPorts); err != nil {
				writeAPIError(w, http.StatusBadRequest, err.Error())
				return
			}
			v := storage.AgentPolicy{AgentID: agentID, TargetHost: req.TargetHost, TargetPort: *req.TargetPort, Protocol: strings.ToLower(req.Protocol), AllowedCIDRs: strings.Join(req.AllowedCIDRs, ","), AllowedPorts: intsCSV(req.AllowedPorts)}
			status, data, err := a.mutate(r, p, func() (int, any, error) {
				if err := a.service.CreatePolicy(r.Context(), v); err != nil {
					return 0, nil, err
				}
				created, err := a.findPolicy(r.Context(), v)
				if err != nil {
					return 0, nil, err
				}
				a.audit(r.Context(), p, "policy.created", "agent_policy", created.ID)
				return http.StatusCreated, publicPolicy(created), nil
			})
			if err != nil {
				writeStorageError(w, err)
				return
			}
			writeStored(w, status, data)
			return
		}
		writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	policyID := parts[0]
	policy, err := a.service.GetPolicy(r.Context(), policyID)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	if policy.AgentID != agentID {
		writeAPIError(w, http.StatusNotFound, "policy not found")
		return
	}
	if len(parts) >= 2 && parts[1] == "restore" {
		if r.Method != http.MethodPost {
			writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if !isAdmin(p) {
			writeAPIError(w, http.StatusForbidden, "admin role required")
			return
		}
		if err := a.service.RestorePolicy(r.Context(), policyID); err != nil {
			writeStorageError(w, err)
			return
		}
		restored, err := a.service.GetPolicy(r.Context(), policyID)
		if err != nil {
			writeStorageError(w, err)
			return
		}
		a.audit(r.Context(), p, "policy.restored", "agent_policy", policyID)
		writeJSON(w, http.StatusOK, publicPolicy(restored))
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, publicPolicy(policy))
	case http.MethodPut, http.MethodPatch:
		if !isAdmin(p) {
			writeAPIError(w, http.StatusForbidden, "admin role required")
			return
		}
		if policy.DeletedAt != nil {
			writeAPIError(w, http.StatusConflict, "policy is deleted")
			return
		}
		var req policyRequest
		if err := decodeJSON(r, &req); err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		req.TargetHost = strings.TrimSpace(req.TargetHost)
		if req.TargetHost != "" {
			policy.TargetHost = req.TargetHost
		}
		if req.TargetPort != nil {
			policy.TargetPort = *req.TargetPort
		}
		if req.Protocol != "" {
			policy.Protocol = strings.ToLower(req.Protocol)
		}
		if req.AllowedCIDRs != nil {
			policy.AllowedCIDRs = strings.Join(req.AllowedCIDRs, ",")
		}
		if req.AllowedPorts != nil {
			policy.AllowedPorts = intsCSV(req.AllowedPorts)
		}
		if policy.TargetHost == "" || !validAgentPolicyTargetHost(policy.TargetHost) || !validAgentPolicyTargetPort(policy.TargetPort) {
			writeAPIError(w, http.StatusBadRequest, "target host and port are required")
			return
		}
		if _, err := routing.NewPolicy(nonemptySplit(policy.AllowedCIDRs), decodeInts(policy.AllowedPorts)); err != nil {
			writeAPIError(w, http.StatusBadRequest, err.Error())
			return
		}
		policy.UpdatedAt = time.Now().UTC()
		if err := a.service.UpdatePolicy(r.Context(), policy); err != nil {
			writeStorageError(w, err)
			return
		}
		a.audit(r.Context(), p, "policy.updated", "agent_policy", policy.ID)
		writeJSON(w, http.StatusOK, publicPolicy(policy))
	case http.MethodDelete:
		if !isAdmin(p) {
			writeAPIError(w, http.StatusForbidden, "admin role required")
			return
		}
		if err := a.service.DeletePolicy(r.Context(), policyID); err != nil {
			writeStorageError(w, err)
			return
		}
		deleted, err := a.service.GetPolicy(r.Context(), policyID)
		if err != nil {
			writeStorageError(w, err)
			return
		}
		a.audit(r.Context(), p, "policy.deleted", "agent_policy", policyID)
		writeJSON(w, http.StatusOK, publicPolicy(deleted))
	default:
		writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

type policyRequest struct {
	TargetHost   string   `json:"targetHost"`
	TargetPort   *int     `json:"targetPort"`
	Protocol     string   `json:"protocol"`
	AllowedCIDRs []string `json:"allowedCIDRs"`
	AllowedPorts []int    `json:"allowedPorts"`
}

func validAgentPolicyTargetPort(port int) bool {
	return port == 0 || (port >= 1 && port <= 65535)
}

func validAgentPolicyTargetHost(host string) bool {
	return host == "*" || !strings.Contains(host, "*")
}

func (a *API) handleTunnels(w http.ResponseWriter, r *http.Request, p auth.Principal, parts []string) {
	if len(parts) == 0 {
		switch r.Method {
		case http.MethodGet:
			a.listTunnels(w, r, p)
		case http.MethodPost:
			if !isAdmin(p) {
				writeAPIError(w, http.StatusForbidden, "admin role required")
				return
			}
			a.createTunnel(w, r, p)
		default:
			writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
		return
	}
	v, err := a.service.GetTunnel(r.Context(), parts[0])
	if err != nil {
		writeStorageError(w, err)
		return
	}
	if !isAdmin(p) {
		agent, e := a.service.GetAgent(r.Context(), v.AgentID)
		if e != nil || agent.OwnerUserID != p.UserID {
			writeAPIError(w, http.StatusForbidden, "forbidden")
			return
		}
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, publicTunnel(v))
	case http.MethodDelete:
		if err := a.service.DeleteTunnel(r.Context(), v.ID); err != nil {
			writeStorageError(w, err)
			return
		}
		a.auditRoute(r.Context(), p, "route.deleted", v)
		writeJSON(w, http.StatusOK, map[string]string{"id": v.ID})
	case http.MethodPut, http.MethodPatch:
		a.updateTunnel(w, r, p, v, r.Method == http.MethodPut)
	default:
		writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *API) listTunnels(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	page, err := a.listTunnelsForOwner(r.Context(), p, r.URL.Query().Get("cursor"), queryLimit(r))
	if err != nil {
		writeStorageError(w, err)
		return
	}
	items := make([]any, 0, len(page.Items))
	for _, t := range page.Items {
		items = append(items, publicTunnel(t))
	}
	writeJSON(w, http.StatusOK, pageData(items, page.NextCursor, page.HasMore))
}

type tunnelRequest struct {
	AgentID       string         `json:"agentId"`
	Protocol      string         `json:"protocol"`
	Domain        string         `json:"domain"`
	PathPrefix    string         `json:"pathPrefix"`
	TargetHost    string         `json:"targetHost"`
	TargetPort    int            `json:"targetPort"`
	PublicPort    int            `json:"publicPort"`
	Status        string         `json:"status"`
	HostHeader    string         `json:"hostHeader"`
	TargetScheme  string         `json:"targetScheme"`
	TLSServerName string         `json:"tlsServerName"`
	Config        map[string]any `json:"config"`
	// Proxy policy fields, meaningful only when Protocol is http-proxy. They are
	// dedicated fields instead of Config entries so the OpenAPI contract and the
	// admin UI can validate each one individually.
	AuthMode             string   `json:"authMode"`
	CredentialID         string   `json:"credentialId"`
	SourceCIDRs          []string `json:"sourceCIDRs"`
	TargetCIDRs          []string `json:"targetCIDRs"`
	TargetPorts          []int    `json:"targetPorts"`
	AllowPrivateTargets  *bool    `json:"allowPrivateTargets"`
	MaxConcurrentTunnels *int     `json:"maxConcurrentTunnels"`
	Description          string   `json:"description"`
}

// tunnelUpdateRequest uses pointers so PATCH can distinguish an omitted field
// from an explicitly supplied empty or invalid value.
type tunnelUpdateRequest struct {
	AgentID       *string         `json:"agentId"`
	Protocol      *string         `json:"protocol"`
	Domain        *string         `json:"domain"`
	PathPrefix    *string         `json:"pathPrefix"`
	TargetHost    *string         `json:"targetHost"`
	TargetPort    *int            `json:"targetPort"`
	PublicPort    *int            `json:"publicPort"`
	Status        *string         `json:"status"`
	HostHeader    *string         `json:"hostHeader"`
	TargetScheme  *string         `json:"targetScheme"`
	TLSServerName *string         `json:"tlsServerName"`
	Config        *map[string]any `json:"config"`
	// Every proxy policy field is a pointer so PATCH can tell "not supplied"
	// from "explicitly cleared"; an empty sourceCIDRs list is a real change
	// (deny every client) and must not be mistaken for an omitted field.
	AuthMode             *string   `json:"authMode"`
	CredentialID         *string   `json:"credentialId"`
	SourceCIDRs          *[]string `json:"sourceCIDRs"`
	TargetCIDRs          *[]string `json:"targetCIDRs"`
	TargetPorts          *[]int    `json:"targetPorts"`
	AllowPrivateTargets  *bool     `json:"allowPrivateTargets"`
	MaxConcurrentTunnels *int      `json:"maxConcurrentTunnels"`
	Description          *string   `json:"description"`
}

type upstreamRouteConfig struct {
	HostHeader    string `json:"hostHeader,omitempty"`
	TargetScheme  string `json:"targetScheme,omitempty"`
	TLSServerName string `json:"tlsServerName,omitempty"`
}

// normalizeUpstreamRouteConfig validates and serializes upstream options while
// allowing callers to clear optional values. Empty means "use the documented
// default" rather than storing a redundant copy of targetHost.
func normalizeUpstreamRouteConfig(config map[string]any) (string, string) {
	hostHeader := strings.TrimSpace(stringValue(config["hostHeader"]))
	targetScheme := strings.ToLower(strings.TrimSpace(stringValue(config["targetScheme"])))
	tlsServerName := strings.TrimSpace(stringValue(config["tlsServerName"]))
	if targetScheme == "" {
		targetScheme = "http"
	}
	if targetScheme != "http" && targetScheme != "https" {
		return "", "targetScheme must be http or https"
	}
	if hostHeader != "" && !validAuthorityHost(hostHeader) {
		return "", "hostHeader must be a valid host or host:port"
	}
	if tlsServerName != "" && !validAuthorityHost(tlsServerName) {
		return "", "tlsServerName must be a valid host"
	}
	if targetScheme == "http" && tlsServerName != "" {
		return "", "tlsServerName requires targetScheme https"
	}
	if config == nil {
		config = map[string]any{}
	}
	config["hostHeader"] = hostHeader
	config["targetScheme"] = targetScheme
	config["tlsServerName"] = tlsServerName
	encoded, err := json.Marshal(config)
	if err != nil {
		return "", "invalid config"
	}
	return string(encoded), ""
}

func stringValue(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}

// validAuthorityHost accepts a host name, IPv4/IPv6 literal, or host:port. It
// intentionally rejects values containing schemes, user info, paths, or spaces.
func validAuthorityHost(value string) bool {
	if value == "" || strings.ContainsAny(value, " \t\r\n") {
		return false
	}
	parsed, err := url.Parse("//" + value)
	return err == nil && parsed.Host == value
}

func (a *API) updateTunnel(w http.ResponseWriter, r *http.Request, p auth.Principal, current storage.Tunnel, full bool) {
	var req tunnelUpdateRequest
	if err := decodeJSON(r, &req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	// The protocol cannot be switched to or from http-proxy, so the stored value
	// stays authoritative for the whole update.
	isProxyRoute := current.Protocol == storage.ProtocolHTTPProxy
	if full && (req.AgentID == nil || (!isProxyRoute && (req.TargetHost == nil || req.TargetPort == nil))) {
		writeAPIError(w, http.StatusBadRequest, "agentId, targetHost and targetPort are required")
		return
	}

	next := current
	if req.AgentID != nil {
		agentID := strings.TrimSpace(*req.AgentID)
		if agentID == "" {
			writeAPIError(w, http.StatusBadRequest, "agentId is required")
			return
		}
		agent, err := a.service.GetAgent(r.Context(), agentID)
		if err != nil {
			writeStorageError(w, err)
			return
		}
		if !isAdmin(p) && agent.OwnerUserID != p.UserID {
			writeAPIError(w, http.StatusForbidden, "forbidden")
			return
		}
		next.AgentID = agentID
	}
	if req.Protocol != nil {
		protocol := strings.ToLower(strings.TrimSpace(*req.Protocol))
		if protocol == "ws" {
			protocol = "websocket"
		}
		if protocol != "http" && protocol != "websocket" && protocol != storage.ProtocolHTTPProxy {
			writeAPIError(w, http.StatusBadRequest, "protocol must be http, websocket or http-proxy")
			return
		}
		// A tp-* route stores a sentinel target plus a proxy policy where a
		// reverse-proxy route stores upstream options, so switching in place
		// would leave either shape half populated.
		if protocol != current.Protocol && (protocol == storage.ProtocolHTTPProxy || isProxyRoute) {
			writeAPIError(w, http.StatusBadRequest, "protocol cannot be changed to or from http-proxy; create a new route")
			return
		}
		next.Protocol = protocol
	}
	if req.Domain != nil {
		domain := strings.ToLower(strings.TrimSpace(*req.Domain))
		if domain == "" {
			writeAPIError(w, http.StatusBadRequest, "domain is required")
			return
		}
		if err := routing.ValidateDomainPattern(domain); err != nil {
			writeAPIError(w, http.StatusBadRequest, err.Error())
			return
		}
		next.Domain = domain
	}
	if req.PathPrefix != nil {
		next.PathPrefix = strings.TrimSpace(*req.PathPrefix)
		if next.PathPrefix == "" {
			next.PathPrefix = "/"
		}
	}
	if isProxyRoute {
		// The sentinel target and the constant prefix are what make a tp-* route
		// a whole-host endpoint. Letting an update move them would silently turn
		// it into a reverse-proxy route the proxy entry can no longer serve.
		if req.TargetHost != nil && strings.TrimSpace(*req.TargetHost) != storage.ProxyTargetWildcard {
			writeAPIError(w, http.StatusBadRequest, "targetHost is fixed for http-proxy routes")
			return
		}
		if req.TargetPort != nil && *req.TargetPort != 0 {
			writeAPIError(w, http.StatusBadRequest, "targetPort is fixed for http-proxy routes")
			return
		}
		if req.Config != nil || req.HostHeader != nil || req.TargetScheme != nil || req.TLSServerName != nil {
			writeAPIError(w, http.StatusBadRequest, "http-proxy routes use dedicated fields; config, hostHeader, targetScheme and tlsServerName must be omitted")
			return
		}
		next.TargetHost = storage.ProxyTargetWildcard
		next.TargetPort = 0
		next.PathPrefix = "/"
	} else {
		if req.TargetHost != nil {
			next.TargetHost = strings.TrimSpace(*req.TargetHost)
			if next.TargetHost == "" {
				writeAPIError(w, http.StatusBadRequest, "targetHost is required")
				return
			}
		}
		if req.TargetPort != nil && (*req.TargetPort < 1 || *req.TargetPort > 65535) {
			writeAPIError(w, http.StatusBadRequest, "targetPort must be between 1 and 65535")
			return
		} else if req.TargetPort != nil {
			next.TargetPort = *req.TargetPort
		}
	}
	if req.PublicPort != nil {
		if *req.PublicPort < 0 || *req.PublicPort > 65535 {
			writeAPIError(w, http.StatusBadRequest, "publicPort must be between 0 and 65535")
			return
		}
		next.PublicPort = *req.PublicPort
	}
	if req.Status != nil {
		status := strings.ToLower(strings.TrimSpace(*req.Status))
		if status != "active" && status != "disabled" {
			writeAPIError(w, http.StatusBadRequest, "status must be active or disabled")
			return
		}
		next.Status = status
	}

	if isProxyRoute && (req.AuthMode != nil || req.CredentialID != nil || req.SourceCIDRs != nil ||
		req.TargetCIDRs != nil || req.TargetPorts != nil || req.AllowPrivateTargets != nil ||
		req.MaxConcurrentTunnels != nil || req.Description != nil) {
		// `policy` is deliberately not named `current`: updateTunnel already
		// takes a `current storage.Tunnel` parameter, and shadowing it would
		// silently change the meaning of every later `current.` reference.
		policy, err := proxyRouteConfigOf(next.Config)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "stored proxy route config is invalid")
			return
		}
		if req.AuthMode != nil {
			policy.AuthMode = strings.ToLower(strings.TrimSpace(*req.AuthMode))
		}
		if req.CredentialID != nil {
			policy.CredentialID = strings.TrimSpace(*req.CredentialID)
		}
		// The merged policy is validated, not the request: switching authMode to
		// basic without resending credentialId must still check the stored
		// reference instead of leaving an unauthenticated route behind.
		if credStatus, message := a.validateProxyCredential(r.Context(), p, policy.AuthMode, policy.CredentialID); message != "" {
			writeAPIError(w, credStatus, message)
			return
		}
		if req.SourceCIDRs != nil {
			policy.SourceCIDRs = *req.SourceCIDRs
		}
		if req.TargetCIDRs != nil {
			policy.TargetCIDRs = *req.TargetCIDRs
		}
		if req.TargetPorts != nil {
			policy.TargetPorts = *req.TargetPorts
		}
		if req.AllowPrivateTargets != nil {
			policy.AllowPrivateTargets = req.AllowPrivateTargets
		}
		if req.MaxConcurrentTunnels != nil {
			policy.MaxConcurrentTunnels = *req.MaxConcurrentTunnels
		}
		if req.Description != nil {
			policy.Description = *req.Description
		}
		encoded, message := normalizeProxyRouteConfig(policy)
		if message != "" {
			writeAPIError(w, http.StatusBadRequest, message)
			return
		}
		next.Config = encoded
	}
	if !isProxyRoute && (req.Config != nil || req.HostHeader != nil || req.TargetScheme != nil || req.TLSServerName != nil) {
		config := map[string]any{}
		if next.Config != "" && next.Config != "{}" {
			if err := json.Unmarshal([]byte(next.Config), &config); err != nil {
				writeAPIError(w, http.StatusBadRequest, "invalid config")
				return
			}
		}
		if req.Config != nil {
			config = map[string]any{}
			for key, value := range *req.Config {
				config[key] = value
			}
		}
		if req.HostHeader != nil {
			config["hostHeader"] = strings.TrimSpace(*req.HostHeader)
		}
		if req.TargetScheme != nil {
			config["targetScheme"] = strings.ToLower(strings.TrimSpace(*req.TargetScheme))
		}
		if req.TLSServerName != nil {
			config["tlsServerName"] = strings.TrimSpace(*req.TLSServerName)
		}
		configBytes, message := normalizeUpstreamRouteConfig(config)
		if message != "" {
			writeAPIError(w, http.StatusBadRequest, message)
			return
		}
		next.Config = configBytes
	}

	a.routeMu.Lock()
	defer a.routeMu.Unlock()
	conflict, err := a.tunnelRouteConflict(r.Context(), next.ID, next.Domain, next.PathPrefix, isProxyRoute)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	if conflict {
		writeAPIError(w, http.StatusConflict, "route already exists")
		return
	}
	next.UpdatedAt = time.Now().UTC()
	if err := a.service.UpdateTunnel(r.Context(), next); err != nil {
		writeStorageError(w, err)
		return
	}
	a.auditRoute(r.Context(), p, "route.updated", next)
	writeJSON(w, http.StatusOK, publicTunnel(next))
}

// errRouteConflict is returned from inside the idempotent mutation body so the
// caller can answer 409 with the established message. It is a sentinel rather
// than a storage error because the uniqueness rule lives in the application:
// UNIQUE(domain, path_prefix) cannot see that a tp-* route claims a whole
// hostname regardless of prefix.
var errRouteConflict = errors.New("route already exists")

// tunnelRouteConflict reports whether a route would collide with an existing
// one. id is the route being patched, or empty for a create.
//
// domainOnly ignores path_prefix, which is what a tp-* http-proxy route needs:
// it is a whole-host proxy endpoint whose stored prefix is a constant "/" with
// no routing meaning. An existing http-proxy route claims its whole hostname for
// the same reason, and in both directions: OpenResty matches an exact
// server_name before the tp-* wildcard, so a path-level reverse-proxy route on
// that domain would silently shadow the proxy endpoint while the
// UNIQUE(domain, path_prefix) constraint still permits the row.
func (a *API) tunnelRouteConflict(ctx context.Context, id, domain, pathPrefix string, domainOnly bool) (bool, error) {
	if domain == "" {
		return false, nil
	}
	routes, err := a.listAllTunnels(ctx)
	if err != nil {
		return false, err
	}
	for _, route := range routes {
		if route.ID == id || !strings.EqualFold(route.Domain, domain) {
			continue
		}
		if domainOnly || route.Protocol == storage.ProtocolHTTPProxy || route.PathPrefix == pathPrefix {
			return true, nil
		}
	}
	return false, nil
}

// validateProxyCredential checks that a basic-auth route references a
// proxy_basic credential the caller may actually see. The message is empty on
// success; otherwise the returned status is the code to answer with. Unknown,
// foreign, disabled, deleted and wrong-typed IDs all share one message so
// credential IDs cannot be enumerated through the routes endpoint.
func (a *API) validateProxyCredential(ctx context.Context, p auth.Principal, authMode, credentialID string) (int, string) {
	if strings.ToLower(strings.TrimSpace(authMode)) != proxyentry.AuthModeBasic {
		return 0, ""
	}
	if a.credentialService == nil {
		return http.StatusServiceUnavailable, "credential service unavailable"
	}
	id := strings.TrimSpace(credentialID)
	if id == "" {
		return http.StatusBadRequest, "credentialId is required when authMode is basic"
	}
	credential, err := a.credentialService.Get(ctx, p, id)
	if err != nil {
		return credentialErrorStatus(err), "credential is not usable"
	}
	if credential.Type != storage.CredentialTypeProxyBasic || !credential.Enabled || credential.DeletedAt != nil {
		return http.StatusBadRequest, "credentialId must reference an enabled proxy_basic credential"
	}
	return 0, ""
}

// buildProxyRoutePolicy is the create-path composition: validate the credential
// reference, then normalize the whole policy into the stored config JSON. The
// update path cannot use it because there the policy is "stored config plus
// pointer overrides" and must be merged before it can be validated.
func (a *API) buildProxyRoutePolicy(ctx context.Context, p auth.Principal, policy proxyRouteConfig) (string, int, string) {
	status, message := a.validateProxyCredential(ctx, p, policy.AuthMode, policy.CredentialID)
	if message != "" {
		return "", status, message
	}
	encoded, message := normalizeProxyRouteConfig(policy)
	if message != "" {
		return "", http.StatusBadRequest, message
	}
	return encoded, 0, ""
}

func concurrencyOrZero(v *int) int {
	if v == nil {
		return 0
	}
	return *v
}

func (a *API) createTunnel(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	var req tunnelRequest
	if err := decodeJSON(r, &req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	protocol := strings.ToLower(strings.TrimSpace(req.Protocol))
	isProxyRoute := protocol == storage.ProtocolHTTPProxy
	if isProxyRoute {
		// The upstream fields have no meaning on a whole-host proxy endpoint, so
		// they are refused rather than ignored: a caller who sends them believes
		// they pin the target, and silently discarding that would be a lie.
		if strings.TrimSpace(req.TargetHost) != "" && strings.TrimSpace(req.TargetHost) != storage.ProxyTargetWildcard {
			writeAPIError(w, http.StatusBadRequest, "targetHost must be omitted for http-proxy routes")
			return
		}
		if req.TargetPort != 0 {
			writeAPIError(w, http.StatusBadRequest, "targetPort must be omitted for http-proxy routes")
			return
		}
		if len(req.Config) > 0 {
			writeAPIError(w, http.StatusBadRequest, "http-proxy routes use dedicated fields; config must be omitted")
			return
		}
		req.Protocol = storage.ProtocolHTTPProxy
		req.PathPrefix = "/"
		req.TargetHost = storage.ProxyTargetWildcard
		req.TargetPort = 0
		req.Config = nil
	}
	if strings.TrimSpace(req.AgentID) == "" {
		writeAPIError(w, http.StatusBadRequest, "agentId is required")
		return
	}
	if !isProxyRoute && (req.TargetHost == "" || req.TargetPort < 1 || req.TargetPort > 65535) {
		writeAPIError(w, http.StatusBadRequest, "agentId, targetHost and valid targetPort are required")
		return
	}
	// Normalizing before validation keeps the stored domain, the conflict check
	// and the pattern the matcher will later apply to the same bytes.
	req.Domain = strings.ToLower(strings.TrimSpace(req.Domain))
	if err := routing.ValidateDomainPattern(req.Domain); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	if isProxyRoute {
		if err := validateProxyRouteDomain(req.Domain); err != nil {
			writeAPIError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if _, err := a.service.GetAgent(r.Context(), req.AgentID); err != nil {
		writeStorageError(w, err)
		return
	}
	a.routeMu.Lock()
	defer a.routeMu.Unlock()
	var configBytes string
	if isProxyRoute {
		encoded, credStatus, message := a.buildProxyRoutePolicy(r.Context(), p, proxyRouteConfig{
			AuthMode: req.AuthMode, CredentialID: req.CredentialID,
			SourceCIDRs: req.SourceCIDRs, TargetCIDRs: req.TargetCIDRs, TargetPorts: req.TargetPorts,
			AllowPrivateTargets:  req.AllowPrivateTargets,
			MaxConcurrentTunnels: concurrencyOrZero(req.MaxConcurrentTunnels),
			Description:          req.Description,
		})
		if message != "" {
			writeAPIError(w, credStatus, message)
			return
		}
		configBytes = encoded
	} else {
		config := req.Config
		if config == nil {
			config = map[string]any{}
		}
		config["hostHeader"] = strings.TrimSpace(req.HostHeader)
		config["targetScheme"] = strings.ToLower(strings.TrimSpace(req.TargetScheme))
		config["tlsServerName"] = strings.TrimSpace(req.TLSServerName)
		encoded, message := normalizeUpstreamRouteConfig(config)
		if message != "" {
			writeAPIError(w, http.StatusBadRequest, message)
			return
		}
		configBytes = encoded
	}
	v := storage.Tunnel{AgentID: req.AgentID, Protocol: strings.ToLower(req.Protocol), Domain: strings.ToLower(strings.TrimSpace(req.Domain)), PathPrefix: req.PathPrefix, TargetHost: req.TargetHost, TargetPort: req.TargetPort, PublicPort: req.PublicPort, Status: req.Status, Config: configBytes}
	if v.Status == "" {
		v.Status = "active"
	}
	status, data, err := a.mutate(r, p, func() (int, any, error) {
		if a.service == nil {
			return 0, nil, errors.New("api service unavailable")
		}
		// The conflict check belongs inside the idempotent body: a replayed key
		// must return the stored 201 rather than collide with the route the
		// first attempt already created.
		conflict, err := a.tunnelRouteConflict(r.Context(), "", v.Domain, v.PathPrefix, isProxyRoute)
		if err != nil {
			return 0, nil, err
		}
		if conflict {
			return 0, nil, errRouteConflict
		}
		created, err := a.service.CreateTunnel(r.Context(), v)
		if err != nil {
			return 0, nil, err
		}
		a.auditRoute(r.Context(), p, "route.created", created)
		return http.StatusCreated, publicTunnel(created), nil
	})
	if errors.Is(err, errRouteConflict) {
		writeAPIError(w, http.StatusConflict, errRouteConflict.Error())
		return
	}
	if err != nil {
		writeStorageError(w, err)
		return
	}
	writeStored(w, status, data)
}

func (a *API) listAllTunnels(ctx context.Context) ([]storage.Tunnel, error) {
	page, err := a.service.ListTunnels(ctx, "", 500)
	if err != nil {
		return nil, err
	}
	for page.HasMore && page.NextCursor != "" {
		next, err := a.service.ListTunnels(ctx, page.NextCursor, 500)
		if err != nil {
			return nil, err
		}
		page.Items = append(page.Items, next.Items...)
		page.HasMore, page.NextCursor = next.HasMore, next.NextCursor
	}
	return page.Items, nil
}

func (a *API) handleAudits(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	if !isAdmin(p) {
		writeAPIError(w, http.StatusForbidden, "admin role required")
		return
	}
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	filter, err := auditFilterFromQuery(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	page, err := a.service.ListAudits(r.Context(), filter, r.URL.Query().Get("cursor"), queryLimit(r))
	if err != nil {
		writeStorageError(w, err)
		return
	}
	items := make([]any, len(page.Items))
	for i := range page.Items {
		items[i] = publicAudit(page.Items[i])
	}
	writeJSON(w, http.StatusOK, pageData(items, page.NextCursor, page.HasMore))
}

func auditFilterFromQuery(r *http.Request) (storage.AuditFilter, error) {
	query := r.URL.Query()
	filter := storage.AuditFilter{
		ActorUserID:  strings.TrimSpace(query.Get("actorUserId")),
		Action:       strings.TrimSpace(query.Get("action")),
		ResourceType: strings.TrimSpace(query.Get("resourceType")),
		ResourceID:   strings.TrimSpace(query.Get("resourceId")),
	}
	for name, target := range map[string]**time.Time{
		"createdFrom": &filter.CreatedFrom,
		"createdTo":   &filter.CreatedTo,
	} {
		value := strings.TrimSpace(query.Get(name))
		if value == "" {
			continue
		}
		parsed, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			return storage.AuditFilter{}, fmt.Errorf("%s must be RFC3339 date-time", name)
		}
		*target = &parsed
	}
	if filter.CreatedFrom != nil && filter.CreatedTo != nil && filter.CreatedFrom.After(*filter.CreatedTo) {
		return storage.AuditFilter{}, fmt.Errorf("createdFrom must not be later than createdTo")
	}
	return filter, nil
}

func publicAudit(v storage.AuditLog) map[string]any {
	return map[string]any{
		"id": v.ID, "actorUserId": v.ActorUserID, "action": v.Action,
		"resourceType": v.ResourceType, "resourceId": v.ResourceID,
		"details": json.RawMessage(defaultJSON(v.Details)), "createdAt": v.CreatedAt,
	}
}

// Idempotency replay conflicts. The message text is preserved verbatim because
// writeStorageError matches on it for the legacy endpoints; the sentinels let the
// identity handlers map the same condition to a stable 409 code instead of an
// opaque 500.
var (
	ErrIdempotencyKeyConflict = errors.New("idempotency key belongs to another user")
	ErrIdempotencyInProgress  = errors.New("idempotency request is in progress")
)

func (a *API) mutate(r *http.Request, p auth.Principal, fn func() (int, any, error)) (int, []byte, error) {
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key != "" && len(key) <= 255 && a.idem != nil {
		if atomic, ok := a.idem.(storage.AtomicIdempotencyRepository); ok {
			rec, claimed, err := atomic.Claim(r.Context(), storage.IdempotencyRecord{Key: key, UserID: p.UserID})
			if err != nil {
				return 0, nil, fmt.Errorf("claim idempotency key: %w", err)
			}
			if !claimed {
				if rec.UserID != "" && rec.UserID != p.UserID {
					return 0, nil, ErrIdempotencyKeyConflict
				}
				if rec.StatusCode == 102 {
					return 0, nil, ErrIdempotencyInProgress
				}
				return rec.StatusCode, []byte(rec.Response), nil
			}
		} else if rec, err := a.idem.Get(r.Context(), key); err == nil {
			if rec.UserID != "" && rec.UserID != p.UserID {
				return 0, nil, ErrIdempotencyKeyConflict
			}
			return rec.StatusCode, []byte(rec.Response), nil
		}
	}
	status, data, err := fn()
	if err != nil {
		if key != "" && a.idem != nil {
			_ = a.idem.Delete(r.Context(), key)
		}
		return 0, nil, err
	}
	b, _ := json.Marshal(apiEnvelope{Code: status, Msg: http.StatusText(status), Data: data})
	if key != "" && len(key) <= 255 && a.idem != nil {
		exp := time.Now().UTC().Add(24 * time.Hour)
		if atomic, ok := a.idem.(storage.AtomicIdempotencyRepository); ok {
			if err := atomic.Update(r.Context(), storage.IdempotencyRecord{Key: key, UserID: p.UserID, Response: string(b), StatusCode: status, ExpiresAt: &exp}); err != nil {
				_ = a.idem.Delete(r.Context(), key)
				return 0, nil, fmt.Errorf("persist idempotency response: %w", err)
			}
		} else if err := a.idem.Put(r.Context(), storage.IdempotencyRecord{Key: key, UserID: p.UserID, Response: string(b), StatusCode: status, ExpiresAt: &exp}); err != nil {
			_ = a.idem.Delete(r.Context(), key)
			return 0, nil, fmt.Errorf("persist idempotency response: %w", err)
		}
	}
	return status, b, nil
}

func writeStored(w http.ResponseWriter, status int, b []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(b)
}
func decodeJSON(r *http.Request, dst any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}
func queryLimit(r *http.Request) int {
	n, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if n <= 0 {
		n = 50
	}
	if n > 500 {
		n = 500
	}
	return n
}
func pageData(items []any, cursor string, more bool) map[string]any {
	return map[string]any{"items": items, "nextCursor": cursor, "hasMore": more}
}
func isAdmin(p auth.Principal) bool   { return strings.EqualFold(p.Role, "admin") }
func encodeStrings(v []string) string { b, _ := json.Marshal(v); return string(b) }
func decodeStrings(s string) []string {
	var v []string
	if json.Unmarshal([]byte(s), &v) != nil && s != "" {
		return strings.Split(s, ",")
	}
	return v
}
func intsCSV(v []int) string {
	out := make([]string, len(v))
	for i, n := range v {
		out[i] = strconv.Itoa(n)
	}
	return strings.Join(out, ",")
}
func decodeInts(s string) []int {
	var out []int
	for _, x := range strings.Split(s, ",") {
		if n, e := strconv.Atoi(strings.TrimSpace(x)); e == nil && n > 0 {
			out = append(out, n)
		}
	}
	return out
}

// publicUser is the only projection of a user row that may reach a response. The
// password hash and every identity secret stay out of it by construction, and the
// two identity fields are included so an administrator can see at a glance how an
// account signs in and whether it carries a second-factor requirement.
func publicUser(v storage.User) map[string]any {
	return map[string]any{"id": v.ID, "username": v.Username, "role": v.Role, "disabled": v.Disabled, "deletedAt": v.DeletedAt, "createdAt": v.CreatedAt, "updatedAt": v.UpdatedAt, "mfaRequired": v.MFARequired, "authSource": string(v.AuthSource)}
}
func publicAgent(v storage.Agent) map[string]any {
	return map[string]any{"id": v.ID, "name": v.Name, "ownerUserId": v.OwnerUserID, "capabilities": decodeStrings(v.Capabilities), "enabled": v.Enabled, "createdAt": v.CreatedAt, "updatedAt": v.UpdatedAt}
}
func publicPolicy(v storage.AgentPolicy) map[string]any {
	return map[string]any{"id": v.ID, "agentId": v.AgentID, "targetHost": v.TargetHost, "targetPort": v.TargetPort, "protocol": v.Protocol, "allowedCIDRs": nonemptySplit(v.AllowedCIDRs), "allowedPorts": decodeInts(v.AllowedPorts), "deletedAt": v.DeletedAt, "createdAt": v.CreatedAt, "updatedAt": v.UpdatedAt}
}
func publicTunnel(v storage.Tunnel) map[string]any {
	if v.Protocol == storage.ProtocolHTTPProxy {
		return publicProxyTunnel(v)
	}
	upstream := publicUpstreamRouteConfig(v)
	return map[string]any{"id": v.ID, "agentId": v.AgentID, "protocol": v.Protocol, "domain": v.Domain, "pathPrefix": v.PathPrefix, "targetHost": v.TargetHost, "targetPort": v.TargetPort, "publicPort": v.PublicPort, "status": v.Status, "hostHeader": upstream.HostHeader, "targetScheme": upstream.TargetScheme, "tlsServerName": upstream.TLSServerName, "config": json.RawMessage(defaultJSON(v.Config)), "createdAt": v.CreatedAt, "updatedAt": v.UpdatedAt}
}

// publicProxyTunnel renders one tp-* route with its policy flattened next to the
// routing fields, so the admin UI never has to decode tunnels.config itself. The
// sentinel target is exposed on purpose: it is why targetHost/targetPort are not
// editable. proxyUrl is derived from the stored domain and needs no server
// configuration.
//
// hostHeader, targetScheme and tlsServerName are deliberately absent. They have
// no meaning on a proxy route, and reusing publicUpstreamRouteConfig would echo
// the "*" sentinel back as the default HostHeader and TLSServerName.
func publicProxyTunnel(v storage.Tunnel) map[string]any {
	cfg, err := proxyRouteConfigOf(v.Config)
	if err != nil {
		// A row whose config cannot be decoded is still listed: showing the
		// defaults beats hiding the route, and the data plane refuses it anyway.
		cfg = proxyRouteConfig{AuthMode: proxyentry.AuthModeNone, AllowPrivateTargets: boolPtr(true)}
	}
	allowPrivate := true
	if cfg.AllowPrivateTargets != nil {
		allowPrivate = *cfg.AllowPrivateTargets
	}
	return map[string]any{
		"id": v.ID, "agentId": v.AgentID, "protocol": v.Protocol, "domain": v.Domain,
		"pathPrefix": v.PathPrefix, "targetHost": v.TargetHost, "targetPort": v.TargetPort,
		"publicPort": v.PublicPort, "status": v.Status,
		"proxyUrl":     "https://" + v.Domain,
		"authMode":     cfg.AuthMode,
		"credentialId": cfg.CredentialID,
		// The lists are coerced to empty slices so the response carries [] and
		// never null, which is what the UI binds directly to a multi-select.
		"sourceCIDRs":          orEmptyStrings(cfg.SourceCIDRs),
		"targetCIDRs":          orEmptyStrings(cfg.TargetCIDRs),
		"targetPorts":          orEmptyInts(cfg.TargetPorts),
		"allowPrivateTargets":  allowPrivate,
		"maxConcurrentTunnels": cfg.MaxConcurrentTunnels,
		"description":          cfg.Description,
		"createdAt":            v.CreatedAt, "updatedAt": v.UpdatedAt,
	}
}

func orEmptyStrings(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

func orEmptyInts(v []int) []int {
	if v == nil {
		return []int{}
	}
	return v
}

func publicUpstreamRouteConfig(v storage.Tunnel) upstreamRouteConfig {
	var config upstreamRouteConfig
	_ = json.Unmarshal([]byte(v.Config), &config)
	if config.HostHeader == "" {
		config.HostHeader = v.TargetHost
	}
	if config.TargetScheme == "" {
		config.TargetScheme = "http"
	}
	if config.TLSServerName == "" {
		config.TLSServerName = v.TargetHost
	}
	return config
}
func nonemptySplit(s string) []string {
	// Policy repositories may persist an empty allowlist as either an empty
	// string or JSON's "[]"; both forms must decode back to an empty slice.
	trimmed := strings.TrimSpace(s)
	if trimmed == "" || trimmed == "[]" {
		return []string{}
	}
	if strings.HasPrefix(trimmed, "[") {
		return decodeStrings(trimmed)
	}
	return strings.Split(trimmed, ",")
}
func defaultJSON(s string) string {
	if strings.TrimSpace(s) == "" {
		return "{}"
	}
	return s
}
func (a *API) audit(ctx context.Context, p auth.Principal, action, typ, id string) {
	if a.service != nil {
		_ = a.service.CreateAudit(ctx, storage.AuditLog{ActorUserID: p.UserID, Action: action, ResourceType: typ, ResourceID: id, Details: "{}"})
	}
}

// auditRoute records the non-secret routing facts administrators need while
// investigating changes; credentials and free-form config are never included.
func (a *API) auditRoute(ctx context.Context, p auth.Principal, action string, route storage.Tunnel) {
	if a.service == nil {
		return
	}
	details := map[string]any{
		"agentId": route.AgentID, "domain": route.Domain, "pathPrefix": route.PathPrefix,
		"targetHost": route.TargetHost, "targetPort": route.TargetPort, "status": route.Status,
	}
	encoded, err := json.Marshal(details)
	if err != nil {
		encoded = []byte("{}")
	}
	_ = a.service.CreateAudit(ctx, storage.AuditLog{
		ActorUserID: p.UserID, Action: action, ResourceType: "tunnel",
		ResourceID: route.ID, Details: string(encoded),
	})
}
func writeStorageError(w http.ResponseWriter, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		writeAPIError(w, http.StatusNotFound, "not found")
		return
	}
	msg := err.Error()
	low := strings.ToLower(msg)
	if strings.Contains(low, "unique") || strings.Contains(low, "constraint") || strings.Contains(low, "idempotency key belongs") {
		writeAPIError(w, http.StatusConflict, msg)
		return
	}
	writeAPIError(w, http.StatusInternalServerError, msg)
}

func (a *API) findAgent(ctx context.Context, want storage.Agent) (storage.Agent, error) {
	p, err := a.service.ListAgents(ctx, "", 500)
	if err != nil {
		return storage.Agent{}, err
	}
	var found storage.Agent
	for _, v := range p.Items {
		if v.OwnerUserID == want.OwnerUserID && v.Name == want.Name && (found.CreatedAt.IsZero() || v.CreatedAt.After(found.CreatedAt)) {
			found = v
		}
	}
	if found.ID == "" {
		return storage.Agent{}, sql.ErrNoRows
	}
	return found, nil
}
func (a *API) findPolicy(ctx context.Context, want storage.AgentPolicy) (storage.AgentPolicy, error) {
	p, err := a.service.ListPolicies(ctx, want.AgentID, "", 500)
	if err != nil {
		return storage.AgentPolicy{}, err
	}
	for _, v := range p.Items {
		if v.TargetHost == want.TargetHost && v.TargetPort == want.TargetPort && v.Protocol == want.Protocol && v.CreatedAt.After(want.CreatedAt.Add(-time.Second)) {
			return v, nil
		}
	}
	if len(p.Items) > 0 {
		return p.Items[len(p.Items)-1], nil
	}
	return storage.AgentPolicy{}, sql.ErrNoRows
}
func (a *API) findTunnel(ctx context.Context, want storage.Tunnel) (storage.Tunnel, error) {
	p, err := a.service.ListTunnels(ctx, "", 500)
	if err != nil {
		return storage.Tunnel{}, err
	}
	var found storage.Tunnel
	for _, v := range p.Items {
		if v.AgentID == want.AgentID && v.Domain == want.Domain && v.PathPrefix == want.PathPrefix && (found.CreatedAt.IsZero() || v.CreatedAt.After(found.CreatedAt)) {
			found = v
		}
	}
	if found.ID == "" {
		return storage.Tunnel{}, sql.ErrNoRows
	}
	return found, nil
}

// listAgentsForOwner walks the underlying cursor until it has a complete page
// visible to the principal. Filtering after one global page would otherwise
// make cursor pagination appear to skip or duplicate records for normal users.
func (a *API) listAgentsForOwner(ctx context.Context, p auth.Principal, cursor string, limit int) (storage.Page[storage.Agent], error) {
	if isAdmin(p) {
		return a.service.ListAgents(ctx, cursor, limit)
	}
	if limit <= 0 {
		limit = 50
	}
	result := storage.Page[storage.Agent]{}
	for {
		page, err := a.service.ListAgents(ctx, cursor, 500)
		if err != nil {
			return result, err
		}
		for _, v := range page.Items {
			if v.OwnerUserID == p.UserID {
				result.Items = append(result.Items, v)
				if len(result.Items) == limit {
					result.HasMore = page.HasMore || len(page.Items) > len(result.Items)
					result.NextCursor = v.ID
					return result, nil
				}
			}
		}
		if !page.HasMore || page.NextCursor == "" {
			return result, nil
		}
		cursor = page.NextCursor
	}
}

func (a *API) listTunnelsForOwner(ctx context.Context, p auth.Principal, cursor string, limit int) (storage.Page[storage.Tunnel], error) {
	if isAdmin(p) {
		return a.service.ListTunnels(ctx, cursor, limit)
	}
	if limit <= 0 {
		limit = 50
	}
	result := storage.Page[storage.Tunnel]{}
	for {
		page, err := a.service.ListTunnels(ctx, cursor, 500)
		if err != nil {
			return result, err
		}
		for _, v := range page.Items {
			ag, e := a.service.GetAgent(ctx, v.AgentID)
			if e != nil && !errors.Is(e, sql.ErrNoRows) {
				return result, e
			}
			if e == nil && ag.OwnerUserID == p.UserID {
				result.Items = append(result.Items, v)
				if len(result.Items) == limit {
					result.HasMore = page.HasMore
					result.NextCursor = v.ID
					return result, nil
				}
			}
		}
		if !page.HasMore || page.NextCursor == "" {
			return result, nil
		}
		cursor = page.NextCursor
	}
}
