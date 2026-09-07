package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/auth"
	"github.com/tunnelmesh/tunnelmesh/internal/routing"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

// API is the HTTP application service. Repositories are kept behind the
// storage interfaces so the handler remains usable with SQLite, MySQL, and
// small in-memory fakes in tests.
type API struct {
	DB           *storage.DB
	Auth         *auth.AuthService
	users        storage.UserRepository
	agents       storage.AgentRepository
	policies     storage.PolicyRepository
	tunnels      storage.TunnelRepository
	audits       storage.AuditRepository
	idem         storage.IdempotencyRepository
	routeMu      sync.Mutex
	service      *apiService
	tokenService *TokenService
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
		a.users, a.agents, a.policies, a.tunnels, a.audits, a.idem = db.Users(), db.Agents(), db.Policies(), db.Tunnels(), db.Audits(), db.Idempotency()
		a.service = &apiService{agents: a.agents, metadata: NewAgentMetadataService(db.Metadata()), policies: a.policies, tunnels: a.tunnels, audits: a.audits}
		a.tokenService = NewTokenService(db)
	}
	return a
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
	return s.tunnels.Update(ctx, v)
}
func (s *apiService) DeleteTunnel(ctx context.Context, id string) error {
	return s.tunnels.Delete(ctx, id)
}
func (s *apiService) ListAudits(ctx context.Context, cursor string, limit int) (storage.Page[storage.AuditLog], error) {
	return s.audits.List(ctx, cursor, limit)
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
		writeJSON(w, http.StatusOK, p)
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
	if len(parts) >= 2 && parts[1] == "metadata" {
		a.handleAgentMetadata(w, r, p, id)
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

type agentMetadataResponse struct {
	AgentID    string         `json:"agentId"`
	NodeID     string         `json:"nodeId"`
	Epoch      int64          `json:"epoch"`
	Revision   int64          `json:"revision"`
	Stale      bool           `json:"stale"`
	ReportedAt time.Time      `json:"reportedAt"`
	UpdatedAt  time.Time      `json:"updatedAt"`
	Items      []MetadataItem `json:"items"`
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
		AgentID: view.AgentID, NodeID: view.NodeID, Epoch: view.Epoch, Revision: view.Revision,
		Stale: view.Stale, ReportedAt: view.ReportedAt, UpdatedAt: view.UpdatedAt, Items: view.Items,
	}
	a.audit(r.Context(), p, "agent.metadata.read", "agent_runtime_metadata", agentID)
	writeJSON(w, http.StatusOK, data)
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
		a.audit(r.Context(), p, "route.deleted", "tunnel", v.ID)
		writeJSON(w, http.StatusOK, map[string]string{"id": v.ID})
	case http.MethodPut, http.MethodPatch:
		var req tunnelRequest
		if err := decodeJSON(r, &req); err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		if req.Domain != "" {
			v.Domain = req.Domain
		}
		if req.PathPrefix != "" {
			v.PathPrefix = req.PathPrefix
		}
		if req.Status != "" {
			v.Status = req.Status
		}
		v.UpdatedAt = time.Now().UTC()
		if err := a.service.UpdateTunnel(r.Context(), v); err != nil {
			writeStorageError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, publicTunnel(v))
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
	AgentID    string         `json:"agentId"`
	Protocol   string         `json:"protocol"`
	Domain     string         `json:"domain"`
	PathPrefix string         `json:"pathPrefix"`
	TargetHost string         `json:"targetHost"`
	TargetPort int            `json:"targetPort"`
	PublicPort int            `json:"publicPort"`
	Status     string         `json:"status"`
	Config     map[string]any `json:"config"`
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
	if _, err := a.service.GetAgent(r.Context(), req.AgentID); err != nil {
		writeStorageError(w, err)
		return
	}
	a.routeMu.Lock()
	defer a.routeMu.Unlock()
	page, err := a.service.ListTunnels(r.Context(), "", 500)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	for page.HasMore && page.NextCursor != "" {
		next, e := a.service.ListTunnels(r.Context(), page.NextCursor, 500)
		if e != nil {
			writeStorageError(w, e)
			return
		}
		page.Items = append(page.Items, next.Items...)
		page.HasMore, page.NextCursor = next.HasMore, next.NextCursor
	}
	for _, existing := range page.Items {
		if strings.EqualFold(existing.Domain, req.Domain) && existing.PathPrefix == req.PathPrefix && existing.Domain != "" {
			writeAPIError(w, http.StatusConflict, "route already exists")
			return
		}
	}
	configBytes, _ := json.Marshal(req.Config)
	if len(configBytes) == 0 {
		configBytes = []byte(`{}`)
	}
	v := storage.Tunnel{AgentID: req.AgentID, Protocol: strings.ToLower(req.Protocol), Domain: strings.ToLower(strings.TrimSpace(req.Domain)), PathPrefix: req.PathPrefix, TargetHost: req.TargetHost, TargetPort: req.TargetPort, PublicPort: req.PublicPort, Status: req.Status, Config: string(configBytes)}
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
		a.audit(r.Context(), p, "route.created", "tunnel", created.ID)
		return http.StatusCreated, publicTunnel(created), nil
	})
	if err != nil {
		writeStorageError(w, err)
		return
	}
	writeStored(w, status, data)
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
	page, err := a.service.ListAudits(r.Context(), r.URL.Query().Get("cursor"), queryLimit(r))
	if err != nil {
		writeStorageError(w, err)
		return
	}
	items := make([]any, len(page.Items))
	for i := range page.Items {
		items[i] = page.Items[i]
	}
	writeJSON(w, http.StatusOK, pageData(items, page.NextCursor, page.HasMore))
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
	return map[string]any{"id": v.ID, "username": v.Username, "role": v.Role, "disabled": v.Disabled, "createdAt": v.CreatedAt}
}
func publicAgent(v storage.Agent) map[string]any {
	return map[string]any{"id": v.ID, "name": v.Name, "ownerUserId": v.OwnerUserID, "capabilities": decodeStrings(v.Capabilities), "enabled": v.Enabled, "createdAt": v.CreatedAt, "updatedAt": v.UpdatedAt}
}
func publicPolicy(v storage.AgentPolicy) map[string]any {
	return map[string]any{"id": v.ID, "agentId": v.AgentID, "targetHost": v.TargetHost, "targetPort": v.TargetPort, "protocol": v.Protocol, "allowedCIDRs": nonemptySplit(v.AllowedCIDRs), "allowedPorts": decodeInts(v.AllowedPorts), "createdAt": v.CreatedAt, "updatedAt": v.UpdatedAt}
}
func publicTunnel(v storage.Tunnel) map[string]any {
	return map[string]any{"id": v.ID, "agentId": v.AgentID, "protocol": v.Protocol, "domain": v.Domain, "pathPrefix": v.PathPrefix, "targetHost": v.TargetHost, "targetPort": v.TargetPort, "publicPort": v.PublicPort, "status": v.Status, "config": json.RawMessage(defaultJSON(v.Config)), "createdAt": v.CreatedAt, "updatedAt": v.UpdatedAt}
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
