package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

const (
	lastUsedQueueCapacity = 128
	lastUsedWriteTimeout  = 2 * time.Second
	policyPageSize        = 100
)

type TokenScope struct {
	AgentIDs    []string `json:"agentIds,omitempty"`
	Protocols   []string `json:"protocols,omitempty"`
	TargetCIDRs []string `json:"targetCIDRs,omitempty"`
	TargetPorts []int    `json:"targetPorts,omitempty"`
}

type TokenIdentity struct {
	TokenID     string
	OwnerUserID string
	AgentID     string
	NodeID      string
	Prefix      string
	Type        storage.TokenType
	Scope       TokenScope
}

type CreateTokenInput struct {
	Type        storage.TokenType `json:"type"`
	OwnerUserID string            `json:"ownerUserId"`
	AgentID     string            `json:"agentId,omitempty"`
	NodeID      string            `json:"nodeId,omitempty"`
	Scope       TokenScope        `json:"scope"`
	ExpiresAt   *time.Time        `json:"expiresAt,omitempty"`
}

type CreatedToken struct {
	TokenIdentity
	Secret    string     `json:"secret"`
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
}

type StreamAuthorizationRequest struct {
	AgentID    string
	Protocol   string
	TargetHost string
	TargetPort int
}

type lastUsedUpdate struct {
	tokenID string
	when    time.Time
}

// CredentialService owns service-token lifecycle and stream authorization.
// Management sessions remain exclusively owned by AuthService/api_tokens.
type CredentialService struct {
	tokens   storage.ServiceTokenRepository
	users    storage.UserRepository
	agents   storage.AgentRepository
	nodes    storage.NodeRepository
	policies storage.PolicyRepository

	workerCtx context.Context
	cancel    context.CancelFunc
	lastUsed  chan lastUsedUpdate
	wg        sync.WaitGroup
	closeOnce sync.Once
}

// NewCredentialService accepts a storage.DB or repository interfaces so the
// authorization rules remain independent of SQL and straightforward to test.
func NewCredentialService(source any, repos ...any) *CredentialService {
	ctx, cancel := context.WithCancel(context.Background())
	s := &CredentialService{
		workerCtx: ctx,
		cancel:    cancel,
		lastUsed:  make(chan lastUsedUpdate, lastUsedQueueCapacity),
	}
	bindCredentialRepository(s, source)
	for _, repo := range repos {
		bindCredentialRepository(s, repo)
	}
	if s.tokens != nil {
		s.wg.Add(1)
		go s.runLastUsedWorker()
	}
	return s
}

func bindCredentialRepository(s *CredentialService, source any) {
	switch v := source.(type) {
	case *storage.DB:
		s.tokens, s.users, s.agents, s.nodes, s.policies = v.ServiceTokens(), v.Users(), v.Agents(), v.Nodes(), v.Policies()
	case storage.ServiceTokenRepository:
		s.tokens = v
	case storage.UserRepository:
		s.users = v
	case storage.AgentRepository:
		s.agents = v
	case storage.NodeRepository:
		s.nodes = v
	case storage.PolicyRepository:
		s.policies = v
	}
}

func (s *CredentialService) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		s.cancel()
		s.wg.Wait()
	})
	return nil
}

func (s *CredentialService) Create(ctx context.Context, in CreateTokenInput) (CreatedToken, error) {
	if err := s.requireLifecycleRepositories(); err != nil {
		return CreatedToken{}, err
	}
	in.OwnerUserID = strings.TrimSpace(in.OwnerUserID)
	in.AgentID = strings.TrimSpace(in.AgentID)
	in.NodeID = strings.TrimSpace(in.NodeID)
	if err := validateTokenBinding(in); err != nil {
		return CreatedToken{}, err
	}
	scope, err := normalizeTokenScope(in.Scope)
	if err != nil {
		return CreatedToken{}, err
	}
	if in.ExpiresAt != nil && !in.ExpiresAt.After(time.Now().UTC()) {
		return CreatedToken{}, fmt.Errorf("expiration must be in the future")
	}
	if err := s.validateOwnerAndBinding(ctx, in.Type, in.OwnerUserID, in.AgentID, in.NodeID); err != nil {
		return CreatedToken{}, err
	}
	if err := s.validateScopedAgents(ctx, in.Type, in.OwnerUserID, in.AgentID, scope.AgentIDs); err != nil {
		return CreatedToken{}, err
	}
	raw, err := randomToken(32)
	if err != nil {
		return CreatedToken{}, fmt.Errorf("generate service token: %w", err)
	}
	idEntropy, err := randomToken(32)
	if err != nil {
		return CreatedToken{}, fmt.Errorf("generate service token id: %w", err)
	}
	id := "stok_" + idEntropy[:22]
	scopeJSON, err := json.Marshal(scope)
	if err != nil {
		return CreatedToken{}, fmt.Errorf("encode token scope: %w", err)
	}
	now := time.Now().UTC()
	record := storage.ServiceToken{
		ID:          id,
		Type:        in.Type,
		OwnerUserID: in.OwnerUserID,
		AgentID:     in.AgentID,
		NodeID:      in.NodeID,
		Prefix:      tokenPrefix(raw),
		TokenHash:   hashToken(raw),
		Scope:       string(scopeJSON),
		ExpiresAt:   in.ExpiresAt,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := s.tokens.Create(ctx, record); err != nil {
		return CreatedToken{}, err
	}
	return createdToken(record, scope, raw), nil
}

func (s *CredentialService) ValidateAs(ctx context.Context, raw string, expected storage.TokenType) (TokenIdentity, error) {
	if s == nil || s.tokens == nil || strings.TrimSpace(raw) == "" || !validTokenType(expected) {
		return TokenIdentity{}, ErrUnauthenticated
	}
	record, err := s.tokens.GetByHash(ctx, hashToken(raw))
	if err != nil || record.Type != expected {
		return TokenIdentity{}, ErrUnauthenticated
	}
	id, err := s.identityFromRecord(ctx, record)
	if err != nil {
		return TokenIdentity{}, ErrUnauthenticated
	}
	s.enqueueLastUsed(record.ID)
	return id, nil
}

func (s *CredentialService) AuthorizeStream(ctx context.Context, id TokenIdentity, req StreamAuthorizationRequest) error {
	if s == nil || s.tokens == nil || s.users == nil || s.agents == nil || s.policies == nil || id.TokenID == "" || id.Type != storage.TokenTypeClient {
		return ErrForbidden
	}
	record, err := s.tokens.Get(ctx, id.TokenID)
	if err != nil || record.Type != storage.TokenTypeClient || record.OwnerUserID != id.OwnerUserID {
		return ErrForbidden
	}
	current, err := s.identityFromRecord(ctx, record)
	if err != nil {
		return ErrForbidden
	}
	req.AgentID = strings.TrimSpace(req.AgentID)
	req.Protocol = normalizeProtocol(req.Protocol)
	req.TargetHost = strings.TrimSpace(req.TargetHost)
	if req.AgentID == "" || req.Protocol == "" || req.TargetHost == "" || req.TargetPort < 1 || req.TargetPort > 65535 {
		return ErrForbidden
	}
	agent, err := s.agents.Get(ctx, req.AgentID)
	if err != nil || !agent.Enabled || agent.OwnerUserID != current.OwnerUserID {
		return ErrForbidden
	}
	targetIP := net.ParseIP(req.TargetHost)
	if !scopeAllows(current.Scope, req, targetIP) {
		return ErrForbidden
	}
	allowed, err := s.agentPolicyAllows(ctx, req, targetIP)
	if err != nil || !allowed {
		return ErrForbidden
	}
	return nil
}

func (s *CredentialService) Rotate(ctx context.Context, id string) (CreatedToken, error) {
	if err := s.requireLifecycleRepositories(); err != nil {
		return CreatedToken{}, err
	}
	record, err := s.tokens.Get(ctx, strings.TrimSpace(id))
	if err != nil {
		return CreatedToken{}, err
	}
	identity, err := s.identityFromRecord(ctx, record)
	if err != nil {
		return CreatedToken{}, ErrUnauthenticated
	}
	raw, err := randomToken(32)
	if err != nil {
		return CreatedToken{}, fmt.Errorf("generate service token: %w", err)
	}
	idEntropy, err := randomToken(32)
	if err != nil {
		return CreatedToken{}, fmt.Errorf("generate service token id: %w", err)
	}
	now := time.Now().UTC()
	replacement := record
	replacement.ID = "stok_" + idEntropy[:22]
	replacement.Prefix = tokenPrefix(raw)
	replacement.TokenHash = hashToken(raw)
	replacement.RevokedAt = nil
	replacement.LastUsedAt = nil
	replacement.CreatedAt = now
	replacement.UpdatedAt = now
	if err := s.tokens.Rotate(ctx, record.ID, replacement, now); err != nil {
		return CreatedToken{}, err
	}
	return createdToken(replacement, identity.Scope, raw), nil
}

func (s *CredentialService) Revoke(ctx context.Context, id string) error {
	if s == nil || s.tokens == nil {
		return errors.New("service token repository is required")
	}
	return s.tokens.Revoke(ctx, strings.TrimSpace(id), time.Now().UTC())
}

func (s *CredentialService) requireLifecycleRepositories() error {
	if s == nil || s.tokens == nil || s.users == nil || s.agents == nil || s.nodes == nil {
		return errors.New("credential repositories are required")
	}
	return nil
}

func validateTokenBinding(in CreateTokenInput) error {
	if !validTokenType(in.Type) {
		return ErrInvalidTokenType
	}
	if in.OwnerUserID == "" {
		return errors.New("owner user is required")
	}
	switch in.Type {
	case storage.TokenTypeAgent:
		if in.AgentID == "" || in.NodeID != "" {
			return errors.New("agent token requires only an agent binding")
		}
	case storage.TokenTypeClient:
		if in.AgentID != "" || in.NodeID != "" {
			return errors.New("client token must be bound only to its owner")
		}
	case storage.TokenTypeServerNode:
		if in.NodeID == "" || in.AgentID != "" {
			return errors.New("server-node token requires only a node binding")
		}
	}
	return nil
}

func validTokenType(tokenType storage.TokenType) bool {
	return tokenType == storage.TokenTypeAgent || tokenType == storage.TokenTypeClient || tokenType == storage.TokenTypeServerNode
}

func (s *CredentialService) validateOwnerAndBinding(ctx context.Context, tokenType storage.TokenType, ownerID, agentID, nodeID string) error {
	owner, err := s.users.Get(ctx, ownerID)
	if err != nil || owner.Disabled {
		return ErrUnauthenticated
	}
	switch tokenType {
	case storage.TokenTypeAgent:
		agent, err := s.agents.Get(ctx, agentID)
		if err != nil || !agent.Enabled || agent.OwnerUserID != ownerID {
			return ErrUnauthenticated
		}
	case storage.TokenTypeServerNode:
		node, err := s.nodes.Get(ctx, nodeID)
		if err != nil || (node.ExpiresAt != nil && !node.ExpiresAt.After(time.Now().UTC())) {
			return ErrUnauthenticated
		}
	}
	return nil
}

func (s *CredentialService) validateScopedAgents(ctx context.Context, tokenType storage.TokenType, ownerID, boundAgentID string, agentIDs []string) error {
	for _, agentID := range agentIDs {
		if tokenType == storage.TokenTypeAgent && agentID != boundAgentID {
			return ErrInvalidTokenScope
		}
		agent, err := s.agents.Get(ctx, agentID)
		if err != nil || !agent.Enabled || agent.OwnerUserID != ownerID {
			return ErrInvalidTokenScope
		}
	}
	return nil
}

func (s *CredentialService) identityFromRecord(ctx context.Context, record storage.ServiceToken) (TokenIdentity, error) {
	now := time.Now().UTC()
	if !validTokenType(record.Type) || record.ID == "" || record.OwnerUserID == "" || record.RevokedAt != nil || (record.ExpiresAt != nil && !record.ExpiresAt.After(now)) {
		return TokenIdentity{}, ErrUnauthenticated
	}
	if err := validateTokenBinding(CreateTokenInput{Type: record.Type, OwnerUserID: record.OwnerUserID, AgentID: record.AgentID, NodeID: record.NodeID}); err != nil {
		return TokenIdentity{}, ErrUnauthenticated
	}
	if s.users == nil || s.agents == nil || s.nodes == nil {
		return TokenIdentity{}, ErrUnauthenticated
	}
	if err := s.validateOwnerAndBinding(ctx, record.Type, record.OwnerUserID, record.AgentID, record.NodeID); err != nil {
		return TokenIdentity{}, ErrUnauthenticated
	}
	var scope TokenScope
	if err := json.Unmarshal([]byte(record.Scope), &scope); err != nil {
		return TokenIdentity{}, ErrUnauthenticated
	}
	scope, err := normalizeTokenScope(scope)
	if err != nil {
		return TokenIdentity{}, ErrUnauthenticated
	}
	return TokenIdentity{TokenID: record.ID, OwnerUserID: record.OwnerUserID, AgentID: record.AgentID, NodeID: record.NodeID, Prefix: record.Prefix, Type: record.Type, Scope: scope}, nil
}

func normalizeTokenScope(scope TokenScope) (TokenScope, error) {
	var out TokenScope
	seenAgents := make(map[string]struct{})
	for _, raw := range scope.AgentIDs {
		agentID := strings.TrimSpace(raw)
		if agentID == "" {
			return TokenScope{}, fmt.Errorf("%w: empty agent id", ErrInvalidTokenScope)
		}
		if _, ok := seenAgents[agentID]; !ok {
			seenAgents[agentID] = struct{}{}
			out.AgentIDs = append(out.AgentIDs, agentID)
		}
	}
	seenProtocols := make(map[string]struct{})
	for _, raw := range scope.Protocols {
		protocol := normalizeProtocol(raw)
		if protocol == "" {
			return TokenScope{}, fmt.Errorf("%w: invalid protocol %q", ErrInvalidTokenScope, raw)
		}
		if _, ok := seenProtocols[protocol]; !ok {
			seenProtocols[protocol] = struct{}{}
			out.Protocols = append(out.Protocols, protocol)
		}
	}
	seenCIDRs := make(map[string]struct{})
	for _, raw := range scope.TargetCIDRs {
		_, network, err := net.ParseCIDR(strings.TrimSpace(raw))
		if err != nil {
			return TokenScope{}, fmt.Errorf("%w: invalid CIDR %q", ErrInvalidTokenScope, raw)
		}
		canonical := network.String()
		if _, ok := seenCIDRs[canonical]; !ok {
			seenCIDRs[canonical] = struct{}{}
			out.TargetCIDRs = append(out.TargetCIDRs, canonical)
		}
	}
	seenPorts := make(map[int]struct{})
	for _, port := range scope.TargetPorts {
		if port < 1 || port > 65535 {
			return TokenScope{}, fmt.Errorf("%w: invalid port %d", ErrInvalidTokenScope, port)
		}
		if _, ok := seenPorts[port]; !ok {
			seenPorts[port] = struct{}{}
			out.TargetPorts = append(out.TargetPorts, port)
		}
	}
	sort.Strings(out.AgentIDs)
	sort.Strings(out.Protocols)
	sort.Strings(out.TargetCIDRs)
	sort.Ints(out.TargetPorts)
	return out, nil
}

func normalizeProtocol(raw string) string {
	switch protocol := strings.ToLower(strings.TrimSpace(raw)); protocol {
	case "tcp", "udp", "http", "ws":
		return protocol
	case "websocket":
		return "ws"
	default:
		return ""
	}
}

func scopeAllows(scope TokenScope, req StreamAuthorizationRequest, targetIP net.IP) bool {
	if len(scope.AgentIDs) > 0 && !containsString(scope.AgentIDs, req.AgentID) {
		return false
	}
	if len(scope.Protocols) > 0 && !containsString(scope.Protocols, req.Protocol) {
		return false
	}
	if len(scope.TargetPorts) > 0 && !containsInt(scope.TargetPorts, req.TargetPort) {
		return false
	}
	if len(scope.TargetCIDRs) > 0 {
		if targetIP == nil {
			return false
		}
		allowed := false
		for _, raw := range scope.TargetCIDRs {
			_, network, err := net.ParseCIDR(raw)
			if err != nil {
				return false
			}
			if network.Contains(targetIP) {
				allowed = true
			}
		}
		if !allowed {
			return false
		}
	}
	return true
}

func (s *CredentialService) agentPolicyAllows(ctx context.Context, req StreamAuthorizationRequest, targetIP net.IP) (bool, error) {
	cursor := ""
	for {
		page, err := s.policies.ListByAgent(ctx, req.AgentID, cursor, policyPageSize)
		if err != nil {
			return false, err
		}
		for _, policy := range page.Items {
			if policyAllows(policy, req, targetIP) {
				return true, nil
			}
		}
		if !page.HasMore {
			return false, nil
		}
		if page.NextCursor == "" || page.NextCursor == cursor {
			return false, errors.New("invalid policy pagination")
		}
		cursor = page.NextCursor
	}
}

func policyAllows(policy storage.AgentPolicy, req StreamAuthorizationRequest, targetIP net.IP) bool {
	if policy.AgentID != "" && policy.AgentID != req.AgentID {
		return false
	}
	if protocol := normalizeProtocol(policy.Protocol); protocol == "" || protocol != req.Protocol {
		return false
	}
	if host := strings.TrimSpace(policy.TargetHost); host != "" && !strings.EqualFold(host, req.TargetHost) {
		return false
	}
	if policy.TargetPort != 0 && policy.TargetPort != req.TargetPort {
		return false
	}
	cidrs, err := parseCIDRList(policy.AllowedCIDRs)
	if err != nil {
		return false
	}
	if len(cidrs) > 0 {
		if targetIP == nil {
			return false
		}
		allowed := false
		for _, network := range cidrs {
			if network.Contains(targetIP) {
				allowed = true
				break
			}
		}
		if !allowed {
			return false
		}
	}
	ports, err := parsePortList(policy.AllowedPorts)
	return err == nil && (len(ports) == 0 || containsInt(ports, req.TargetPort))
}

func parseCIDRList(raw string) ([]*net.IPNet, error) {
	values, err := parseStringList(raw)
	if err != nil {
		return nil, err
	}
	out := make([]*net.IPNet, 0, len(values))
	for _, value := range values {
		_, network, err := net.ParseCIDR(value)
		if err != nil {
			return nil, err
		}
		out = append(out, network)
	}
	return out, nil
}

func parseStringList(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "[]" {
		return nil, nil
	}
	if strings.HasPrefix(raw, "[") {
		var values []string
		if err := json.Unmarshal([]byte(raw), &values); err != nil {
			return nil, err
		}
		for i := range values {
			values[i] = strings.TrimSpace(values[i])
		}
		return values, nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out, nil
}

func parsePortList(raw string) ([]int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "[]" {
		return nil, nil
	}
	if strings.HasPrefix(raw, "[") {
		var ports []int
		if err := json.Unmarshal([]byte(raw), &ports); err != nil {
			return nil, err
		}
		for _, port := range ports {
			if port < 1 || port > 65535 {
				return nil, ErrInvalidTokenScope
			}
		}
		return ports, nil
	}
	var ports []int
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if strings.Contains(part, "-") {
			bounds := strings.SplitN(part, "-", 2)
			lo, errLo := strconv.Atoi(strings.TrimSpace(bounds[0]))
			hi, errHi := strconv.Atoi(strings.TrimSpace(bounds[1]))
			if errLo != nil || errHi != nil || lo < 1 || hi > 65535 || lo > hi {
				return nil, ErrInvalidTokenScope
			}
			for port := lo; port <= hi; port++ {
				ports = append(ports, port)
			}
			continue
		}
		port, err := strconv.Atoi(part)
		if err != nil || port < 1 || port > 65535 {
			return nil, ErrInvalidTokenScope
		}
		ports = append(ports, port)
	}
	return ports, nil
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func containsInt(values []int, want int) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func createdToken(record storage.ServiceToken, scope TokenScope, raw string) CreatedToken {
	return CreatedToken{
		TokenIdentity: TokenIdentity{TokenID: record.ID, OwnerUserID: record.OwnerUserID, AgentID: record.AgentID, NodeID: record.NodeID, Prefix: record.Prefix, Type: record.Type, Scope: scope},
		Secret:        raw,
		ExpiresAt:     record.ExpiresAt,
	}
}

func (s *CredentialService) enqueueLastUsed(tokenID string) {
	if s == nil || tokenID == "" {
		return
	}
	update := lastUsedUpdate{tokenID: tokenID, when: time.Now().UTC()}
	select {
	case <-s.workerCtx.Done():
	case s.lastUsed <- update:
	default:
		// last_used_at is best-effort; dropping is safer than blocking auth.
	}
}

func (s *CredentialService) runLastUsedWorker() {
	defer s.wg.Done()
	for {
		select {
		case <-s.workerCtx.Done():
			return
		case update := <-s.lastUsed:
			ctx, cancel := context.WithTimeout(s.workerCtx, lastUsedWriteTimeout)
			_ = s.tokens.TouchLastUsed(ctx, update.tokenID, update.when)
			cancel()
		}
	}
}
