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
	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
	"github.com/tunnelmesh/tunnelmesh/internal/routing"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

// API is the HTTP application service. Repositories are kept behind the
// storage interfaces so the handler remains usable with SQLite, MySQL, and
// small in-memory fakes in tests.
type API struct {
	DB                 *storage.DB
	Auth               *auth.AuthService
	users              storage.UserRepository
	agents             storage.AgentRepository
	policies           storage.PolicyRepository
	tunnels            storage.TunnelRepository
	audits             storage.AuditRepository
	idem               storage.IdempotencyRepository
	dashboard          storage.DashboardRepository
	routeMu            sync.Mutex
	service            *apiService
	tokenService       *TokenService
	accounts           *auth.AccountService
	serverNodes        *ServerNodeService
	traceroute         *TracerouteService
	probeService       *ProbeService
	agentSessions      *AgentSessionManager
	localAgentRelay    *AgentRelayTransport
	clusterConnections AgentConnectionLister
	connectionCloser   AgentConnectionCloseService
	localNodeID        string
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
	if v.AgentID == "" || v.TargetHost == "" || v.TargetPort < 1 || v.TargetPort > 65535 {
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
func (s *apiService) CreatePolicy(ctx context.Context, v storage.AgentPolicy) error {
	return s.policies.Create(ctx, v)
}
func (s *apiService) UpdatePolicy(ctx context.Context, v storage.AgentPolicy) error {
	return s.policies.Update(ctx, v)
}
func (s *apiService) DeletePolicy(ctx context.Context, id string) error {
	return s.policies.Delete(ctx, id)
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
	if path == "/api/v1/auth/login" && r.Method == http.MethodPost {
		a.login(w, r)
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
	if parts[0] == "auth" && len(parts) == 2 && parts[1] == "me" {
		writeJSON(w, http.StatusOK, map[string]any{"id": p.UserID, "username": p.Username, "role": p.Role})
		return
	}
	if parts[0] == "auth" && len(parts) == 2 && parts[1] == "password" {
		a.changeOwnPassword(w, r, p)
		return
	}
	switch parts[0] {
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
	case "users":
		a.handleUsers(w, r, p, parts[1:])
	case "dashboard":
		a.handleDashboard(w, r, p, parts[1:])
	case "traces":
		a.handleTraces(w, r, p, parts[1:])
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

func (a *API) login(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	result, err := a.Auth.Login(r.Context(), req.Username, req.Password)
	if err != nil {
		writeAPIError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"token": result.Token, "user": publicUser(result.User)})
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
			page, err := a.service.ListPolicies(r.Context(), agentID, r.URL.Query().Get("cursor"), queryLimit(r))
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
			if req.TargetHost == "" || req.TargetPort < 1 || req.TargetPort > 65535 {
				writeAPIError(w, http.StatusBadRequest, "target host and port are required")
				return
			}
			if _, err := routing.NewPolicy(req.AllowedCIDRs, req.AllowedPorts); err != nil {
				writeAPIError(w, http.StatusBadRequest, err.Error())
				return
			}
			v := storage.AgentPolicy{AgentID: agentID, TargetHost: req.TargetHost, TargetPort: req.TargetPort, Protocol: strings.ToLower(req.Protocol), AllowedCIDRs: strings.Join(req.AllowedCIDRs, ","), AllowedPorts: intsCSV(req.AllowedPorts)}
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
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, publicPolicy(policy))
	case http.MethodPut, http.MethodPatch:
		if !isAdmin(p) {
			writeAPIError(w, http.StatusForbidden, "admin role required")
			return
		}
		var req policyRequest
		if err := decodeJSON(r, &req); err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		if req.TargetHost != "" {
			policy.TargetHost = req.TargetHost
		}
		if req.TargetPort != 0 {
			policy.TargetPort = req.TargetPort
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
		a.audit(r.Context(), p, "policy.deleted", "agent_policy", policyID)
		writeJSON(w, http.StatusOK, map[string]string{"id": policyID})
	default:
		writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

type policyRequest struct {
	TargetHost   string   `json:"targetHost"`
	TargetPort   int      `json:"targetPort"`
	Protocol     string   `json:"protocol"`
	AllowedCIDRs []string `json:"allowedCIDRs"`
	AllowedPorts []int    `json:"allowedPorts"`
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
	if full && (req.AgentID == nil || req.TargetHost == nil || req.TargetPort == nil) {
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
		if protocol != "http" && protocol != "websocket" {
			writeAPIError(w, http.StatusBadRequest, "protocol must be http or websocket")
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

	if req.Config != nil || req.HostHeader != nil || req.TargetScheme != nil || req.TLSServerName != nil {
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
	conflict, err := a.tunnelRouteConflict(r.Context(), next.ID, next.Domain, next.PathPrefix)
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

func (a *API) tunnelRouteConflict(ctx context.Context, id, domain, pathPrefix string) (bool, error) {
	if domain == "" {
		return false, nil
	}
	routes, err := a.listAllTunnels(ctx)
	if err != nil {
		return false, err
	}
	for _, route := range routes {
		if route.ID != id && strings.EqualFold(route.Domain, domain) && route.PathPrefix == pathPrefix {
			return true, nil
		}
	}
	return false, nil
}

func (a *API) createTunnel(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	var req tunnelRequest
	if err := decodeJSON(r, &req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.AgentID == "" || req.TargetHost == "" || req.TargetPort < 1 || req.TargetPort > 65535 {
		writeAPIError(w, http.StatusBadRequest, "agentId, targetHost and valid targetPort are required")
		return
	}
	if err := routing.ValidateDomainPattern(req.Domain); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := a.service.GetAgent(r.Context(), req.AgentID); err != nil {
		writeStorageError(w, err)
		return
	}
	a.routeMu.Lock()
	defer a.routeMu.Unlock()
	routes, err := a.listAllTunnels(r.Context())
	if err != nil {
		writeStorageError(w, err)
		return
	}
	for _, existing := range routes {
		if strings.EqualFold(existing.Domain, req.Domain) && existing.PathPrefix == req.PathPrefix && existing.Domain != "" {
			writeAPIError(w, http.StatusConflict, "route already exists")
			return
		}
	}
	config := req.Config
	if config == nil {
		config = map[string]any{}
	}
	config["hostHeader"] = strings.TrimSpace(req.HostHeader)
	config["targetScheme"] = strings.ToLower(strings.TrimSpace(req.TargetScheme))
	config["tlsServerName"] = strings.TrimSpace(req.TLSServerName)
	configBytes, message := normalizeUpstreamRouteConfig(config)
	if message != "" {
		writeAPIError(w, http.StatusBadRequest, message)
		return
	}
	v := storage.Tunnel{AgentID: req.AgentID, Protocol: strings.ToLower(req.Protocol), Domain: strings.ToLower(strings.TrimSpace(req.Domain)), PathPrefix: req.PathPrefix, TargetHost: req.TargetHost, TargetPort: req.TargetPort, PublicPort: req.PublicPort, Status: req.Status, Config: configBytes}
	if v.Status == "" {
		v.Status = "active"
	}
	status, data, err := a.mutate(r, p, func() (int, any, error) {
		if a.service == nil {
			return 0, nil, errors.New("api service unavailable")
		}
		created, err := a.service.CreateTunnel(r.Context(), v)
		if err != nil {
			return 0, nil, err
		}
		a.auditRoute(r.Context(), p, "route.created", created)
		return http.StatusCreated, publicTunnel(created), nil
	})
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
					return 0, nil, fmt.Errorf("idempotency key belongs to another user")
				}
				if rec.StatusCode == 102 {
					return 0, nil, fmt.Errorf("idempotency request is in progress")
				}
				return rec.StatusCode, []byte(rec.Response), nil
			}
		} else if rec, err := a.idem.Get(r.Context(), key); err == nil {
			if rec.UserID != "" && rec.UserID != p.UserID {
				return 0, nil, fmt.Errorf("idempotency key belongs to another user")
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
func publicUser(v storage.User) map[string]any {
	return map[string]any{"id": v.ID, "username": v.Username, "role": v.Role, "disabled": v.Disabled, "deletedAt": v.DeletedAt, "createdAt": v.CreatedAt, "updatedAt": v.UpdatedAt}
}
func publicAgent(v storage.Agent) map[string]any {
	return map[string]any{"id": v.ID, "name": v.Name, "ownerUserId": v.OwnerUserID, "capabilities": decodeStrings(v.Capabilities), "enabled": v.Enabled, "createdAt": v.CreatedAt, "updatedAt": v.UpdatedAt}
}
func publicPolicy(v storage.AgentPolicy) map[string]any {
	return map[string]any{"id": v.ID, "agentId": v.AgentID, "targetHost": v.TargetHost, "targetPort": v.TargetPort, "protocol": v.Protocol, "allowedCIDRs": nonemptySplit(v.AllowedCIDRs), "allowedPorts": decodeInts(v.AllowedPorts), "createdAt": v.CreatedAt, "updatedAt": v.UpdatedAt}
}
func publicTunnel(v storage.Tunnel) map[string]any {
	upstream := publicUpstreamRouteConfig(v)
	return map[string]any{"id": v.ID, "agentId": v.AgentID, "protocol": v.Protocol, "domain": v.Domain, "pathPrefix": v.PathPrefix, "targetHost": v.TargetHost, "targetPort": v.TargetPort, "publicPort": v.PublicPort, "status": v.Status, "hostHeader": upstream.HostHeader, "targetScheme": upstream.TargetScheme, "tlsServerName": upstream.TLSServerName, "config": json.RawMessage(defaultJSON(v.Config)), "createdAt": v.CreatedAt, "updatedAt": v.UpdatedAt}
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
	if strings.TrimSpace(s) == "" {
		return []string{}
	}
	return strings.Split(s, ",")
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
