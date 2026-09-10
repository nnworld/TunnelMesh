package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

	maxTokenScopeJSONBytes = 16 * 1024
	maxScopeAgentIDs       = 128
	maxScopeAgentIDBytes   = 255
	maxScopeServerNodeIDs  = 128
	maxScopeProtocols      = 4
	maxScopeCIDRs          = 128
	maxScopePorts          = 1024
	maxPolicyTargetBytes   = 255
)

type TokenScope struct {
	AgentIDs      []string `json:"agentIds,omitempty"`
	ServerNodeIDs []string `json:"serverNodeIds,omitempty"`
	Protocols     []string `json:"protocols,omitempty"`
	TargetCIDRs   []string `json:"targetCIDRs,omitempty"`
	TargetPorts   []int    `json:"targetPorts,omitempty"`
}

type TokenIdentity struct {
	TokenID       string
	OwnerUserID   string
	AgentID       string
	NodeID        string
	ServerNodeIDs []string
	Prefix        string
	Type          storage.TokenType
	Scope         TokenScope
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

type compiledTokenScope struct {
	scope         TokenScope
	agentIDs      map[string]struct{}
	serverNodeIDs map[string]struct{}
	protocols     map[string]struct{}
	cidrs         []*net.IPNet
	ports         map[int]struct{}
}

type portInterval struct {
	first int
	last  int
}

type portMatcher struct {
	intervals []portInterval
}

// CredentialService owns service-token lifecycle and stream authorization.
// Management sessions remain exclusively owned by AuthService/api_tokens.
type CredentialService struct {
	tokens      storage.ServiceTokenRepository
	users       storage.UserRepository
	agents      storage.AgentRepository
	nodes       storage.NodeRepository
	policies    storage.PolicyRepository
	secretStore *SecretStore

	workerCtx context.Context
	cancel    context.CancelFunc
	lastUsed  chan lastUsedUpdate
	wg        sync.WaitGroup
	closeOnce sync.Once
}

// SetSecretStore enables encrypted-at-rest recovery for newly issued and
// rotated service tokens. It is optional so validation-only callers keep the
// legacy hash-only behavior.
func (s *CredentialService) SetSecretStore(store *SecretStore) {
	if s != nil {
		s.secretStore = store
	}
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
	if err := s.validateScopedServerNodes(ctx, in.Type, scope.ServerNodeIDs); err != nil {
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
	if s.secretStore != nil {
		ciphertext, nonce, keyID, version, err := s.secretStore.Encrypt(raw)
		if err != nil {
			return CreatedToken{}, fmt.Errorf("encrypt service token: %w", err)
		}
		record.SecretCiphertext = base64.RawStdEncoding.EncodeToString(ciphertext)
		record.SecretNonce = base64.RawStdEncoding.EncodeToString(nonce)
		record.SecretKeyID, record.SecretVersion = keyID, version
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
	current, compiledScope, err := s.identityAndScopeFromRecord(ctx, record)
	if err != nil {
		return ErrForbidden
	}
	if len(req.AgentID) > maxScopeAgentIDBytes || len(req.Protocol) > 32 || len(req.TargetHost) > maxPolicyTargetBytes {
		return ErrForbidden
	}
	req.AgentID = strings.TrimSpace(req.AgentID)
	req.Protocol = normalizeProtocol(req.Protocol)
	req.TargetHost = strings.TrimSpace(req.TargetHost)
	if req.AgentID == "" || len(req.AgentID) > maxScopeAgentIDBytes || req.Protocol == "" || req.TargetHost == "" || len(req.TargetHost) > maxPolicyTargetBytes || req.TargetPort < 1 || req.TargetPort > 65535 {
		return ErrForbidden
	}
	agent, err := s.agents.Get(ctx, req.AgentID)
	if err != nil || !agent.Enabled || agent.OwnerUserID != current.OwnerUserID {
		return ErrForbidden
	}
	targetIP := net.ParseIP(req.TargetHost)
	if !scopeAllows(compiledScope, req, targetIP) {
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
	if s.secretStore != nil {
		ciphertext, nonce, keyID, version, err := s.secretStore.Encrypt(raw)
		if err != nil {
			return CreatedToken{}, fmt.Errorf("encrypt service token: %w", err)
		}
		replacement.SecretCiphertext = base64.RawStdEncoding.EncodeToString(ciphertext)
		replacement.SecretNonce = base64.RawStdEncoding.EncodeToString(nonce)
		replacement.SecretKeyID, replacement.SecretVersion = keyID, version
		replacement.SecretLastReadAt = nil
	}
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
		if in.NodeID != "" || in.AgentID != "" {
			return fmt.Errorf("%w: server-node token must use scope.serverNodeIds", ErrInvalidTokenScope)
		}
	}
	return nil
}

func validTokenType(tokenType storage.TokenType) bool {
	return tokenType == storage.TokenTypeAgent || tokenType == storage.TokenTypeClient || tokenType == storage.TokenTypeServerNode
}

func (s *CredentialService) validateOwnerAndBinding(ctx context.Context, tokenType storage.TokenType, ownerID, agentID, nodeID string) error {
	owner, err := s.users.Get(ctx, ownerID)
	if err != nil || owner.Disabled || owner.DeletedAt != nil {
		return ErrUnauthenticated
	}
	switch tokenType {
	case storage.TokenTypeAgent:
		agent, err := s.agents.Get(ctx, agentID)
		if err != nil || !agent.Enabled || agent.OwnerUserID != ownerID {
			return ErrUnauthenticated
		}
	case storage.TokenTypeServerNode:
		// Server-node authorization is defined by scope.serverNodeIds. An empty
		// list is a deliberate fleet credential; relay still verifies the
		// caller's mTLS identity and node epoch before accepting traffic.
	}
	return nil
}

func (s *CredentialService) validateScopedServerNodes(ctx context.Context, tokenType storage.TokenType, nodeIDs []string) error {
	if tokenType != storage.TokenTypeServerNode {
		return nil
	}
	now := time.Now().UTC()
	for _, nodeID := range nodeIDs {
		node, err := s.nodes.Get(ctx, nodeID)
		if err != nil || !node.Enabled || node.DeletedAt != nil || (node.ExpiresAt != nil && !node.ExpiresAt.After(now)) {
			return ErrInvalidTokenScope
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
	identity, _, err := s.identityAndScopeFromRecord(ctx, record)
	return identity, err
}

func (s *CredentialService) identityAndScopeFromRecord(ctx context.Context, record storage.ServiceToken) (TokenIdentity, compiledTokenScope, error) {
	now := time.Now().UTC()
	if !validTokenType(record.Type) || record.ID == "" || record.OwnerUserID == "" || record.RevokedAt != nil || (record.ExpiresAt != nil && !record.ExpiresAt.After(now)) {
		return TokenIdentity{}, compiledTokenScope{}, ErrUnauthenticated
	}
	if err := validateTokenBinding(CreateTokenInput{Type: record.Type, OwnerUserID: record.OwnerUserID, AgentID: record.AgentID, NodeID: record.NodeID}); err != nil {
		return TokenIdentity{}, compiledTokenScope{}, ErrUnauthenticated
	}
	if s.users == nil || s.agents == nil || s.nodes == nil {
		return TokenIdentity{}, compiledTokenScope{}, ErrUnauthenticated
	}
	if err := s.validateOwnerAndBinding(ctx, record.Type, record.OwnerUserID, record.AgentID, record.NodeID); err != nil {
		return TokenIdentity{}, compiledTokenScope{}, ErrUnauthenticated
	}
	scope, err := decodeTokenScope(record.Scope)
	if err != nil {
		return TokenIdentity{}, compiledTokenScope{}, ErrUnauthenticated
	}
	compiled, err := compileTokenScope(scope)
	if err != nil {
		return TokenIdentity{}, compiledTokenScope{}, ErrUnauthenticated
	}
	if err := s.validateScopedServerNodes(ctx, record.Type, compiled.scope.ServerNodeIDs); err != nil {
		return TokenIdentity{}, compiledTokenScope{}, ErrUnauthenticated
	}
	identity := TokenIdentity{TokenID: record.ID, OwnerUserID: record.OwnerUserID, AgentID: record.AgentID, NodeID: record.NodeID, ServerNodeIDs: compiled.scope.ServerNodeIDs, Prefix: record.Prefix, Type: record.Type, Scope: compiled.scope}
	return identity, compiled, nil
}

func normalizeTokenScope(scope TokenScope) (TokenScope, error) {
	compiled, err := compileTokenScope(scope)
	return compiled.scope, err
}

// NormalizeTokenScope is the single authorization-scope validator shared by
// creation and mutation paths so updates cannot bypass creation-time rules.
func NormalizeTokenScope(scope TokenScope) (TokenScope, error) {
	return normalizeTokenScope(scope)
}

func compileTokenScope(scope TokenScope) (compiledTokenScope, error) {
	if len(scope.AgentIDs) > maxScopeAgentIDs || len(scope.ServerNodeIDs) > maxScopeServerNodeIDs || len(scope.Protocols) > maxScopeProtocols || len(scope.TargetCIDRs) > maxScopeCIDRs || len(scope.TargetPorts) > maxScopePorts {
		return compiledTokenScope{}, fmt.Errorf("%w: scope entry limit exceeded", ErrInvalidTokenScope)
	}
	stringBytes := 0
	for _, value := range scope.AgentIDs {
		if len(value) > maxScopeAgentIDBytes {
			return compiledTokenScope{}, fmt.Errorf("%w: invalid agent id", ErrInvalidTokenScope)
		}
		stringBytes += len(value)
	}
	for _, value := range scope.ServerNodeIDs {
		if len(value) > maxScopeAgentIDBytes {
			return compiledTokenScope{}, fmt.Errorf("%w: invalid server node id", ErrInvalidTokenScope)
		}
		stringBytes += len(value)
	}
	for _, value := range scope.Protocols {
		if len(value) > maxTokenScopeJSONBytes {
			return compiledTokenScope{}, ErrInvalidTokenScope
		}
		stringBytes += len(value)
	}
	for _, value := range scope.TargetCIDRs {
		if len(value) > maxTokenScopeJSONBytes {
			return compiledTokenScope{}, ErrInvalidTokenScope
		}
		stringBytes += len(value)
	}
	if stringBytes > maxTokenScopeJSONBytes {
		return compiledTokenScope{}, fmt.Errorf("%w: serialized scope exceeds %d bytes", ErrInvalidTokenScope, maxTokenScopeJSONBytes)
	}
	out := compiledTokenScope{
		agentIDs:      make(map[string]struct{}, len(scope.AgentIDs)),
		serverNodeIDs: make(map[string]struct{}, len(scope.ServerNodeIDs)),
		protocols:     make(map[string]struct{}, len(scope.Protocols)),
		cidrs:         make([]*net.IPNet, 0, len(scope.TargetCIDRs)),
		ports:         make(map[int]struct{}, len(scope.TargetPorts)),
	}
	seenServerNodes := make(map[string]struct{})
	for _, raw := range scope.ServerNodeIDs {
		nodeID := strings.TrimSpace(raw)
		if nodeID == "" || len(nodeID) > maxScopeAgentIDBytes {
			return compiledTokenScope{}, fmt.Errorf("%w: invalid server node id", ErrInvalidTokenScope)
		}
		if _, ok := seenServerNodes[nodeID]; !ok {
			seenServerNodes[nodeID] = struct{}{}
			out.serverNodeIDs[nodeID] = struct{}{}
			out.scope.ServerNodeIDs = append(out.scope.ServerNodeIDs, nodeID)
		}
	}
	seenAgents := make(map[string]struct{})
	for _, raw := range scope.AgentIDs {
		agentID := strings.TrimSpace(raw)
		if agentID == "" || len(agentID) > maxScopeAgentIDBytes {
			return compiledTokenScope{}, fmt.Errorf("%w: invalid agent id", ErrInvalidTokenScope)
		}
		if _, ok := seenAgents[agentID]; !ok {
			seenAgents[agentID] = struct{}{}
			out.agentIDs[agentID] = struct{}{}
			out.scope.AgentIDs = append(out.scope.AgentIDs, agentID)
		}
	}
	seenProtocols := make(map[string]struct{})
	for _, raw := range scope.Protocols {
		protocol := normalizeProtocol(raw)
		if protocol == "" {
			return compiledTokenScope{}, fmt.Errorf("%w: invalid protocol %q", ErrInvalidTokenScope, raw)
		}
		if _, ok := seenProtocols[protocol]; !ok {
			seenProtocols[protocol] = struct{}{}
			out.protocols[protocol] = struct{}{}
			out.scope.Protocols = append(out.scope.Protocols, protocol)
		}
	}
	seenCIDRs := make(map[string]struct{})
	for _, raw := range scope.TargetCIDRs {
		_, network, err := net.ParseCIDR(strings.TrimSpace(raw))
		if err != nil {
			return compiledTokenScope{}, fmt.Errorf("%w: invalid CIDR %q", ErrInvalidTokenScope, raw)
		}
		canonical := network.String()
		if _, ok := seenCIDRs[canonical]; !ok {
			seenCIDRs[canonical] = struct{}{}
			out.cidrs = append(out.cidrs, network)
			out.scope.TargetCIDRs = append(out.scope.TargetCIDRs, canonical)
		}
	}
	seenPorts := make(map[int]struct{})
	for _, port := range scope.TargetPorts {
		if port < 1 || port > 65535 {
			return compiledTokenScope{}, fmt.Errorf("%w: invalid port %d", ErrInvalidTokenScope, port)
		}
		if _, ok := seenPorts[port]; !ok {
			seenPorts[port] = struct{}{}
			out.ports[port] = struct{}{}
			out.scope.TargetPorts = append(out.scope.TargetPorts, port)
		}
	}
	sort.Strings(out.scope.AgentIDs)
	sort.Strings(out.scope.ServerNodeIDs)
	sort.Strings(out.scope.Protocols)
	sort.Strings(out.scope.TargetCIDRs)
	sort.Ints(out.scope.TargetPorts)
	encoded, err := json.Marshal(out.scope)
	if err != nil || len(encoded) > maxTokenScopeJSONBytes {
		return compiledTokenScope{}, fmt.Errorf("%w: serialized scope exceeds %d bytes", ErrInvalidTokenScope, maxTokenScopeJSONBytes)
	}
	return out, nil
}

func decodeTokenScope(raw string) (TokenScope, error) {
	if len(raw) > maxTokenScopeJSONBytes {
		return TokenScope{}, ErrInvalidTokenScope
	}
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || trimmed[0] != '{' {
		return TokenScope{}, ErrInvalidTokenScope
	}
	decoder := json.NewDecoder(strings.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	var scope TokenScope
	if err := decoder.Decode(&scope); err != nil {
		return TokenScope{}, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return TokenScope{}, ErrInvalidTokenScope
	}
	return scope, nil
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

func scopeAllows(scope compiledTokenScope, req StreamAuthorizationRequest, targetIP net.IP) bool {
	if len(scope.agentIDs) > 0 {
		if _, ok := scope.agentIDs[req.AgentID]; !ok {
			return false
		}
	}
	if len(scope.protocols) > 0 {
		if _, ok := scope.protocols[req.Protocol]; !ok {
			return false
		}
	}
	if len(scope.ports) > 0 {
		if _, ok := scope.ports[req.TargetPort]; !ok {
			return false
		}
	}
	if len(scope.cidrs) == 0 {
		return true
	}
	if targetIP == nil {
		return false
	}
	for _, network := range scope.cidrs {
		if network.Contains(targetIP) {
			return true
		}
	}
	return false
}

func (s *CredentialService) agentPolicyAllows(ctx context.Context, req StreamAuthorizationRequest, targetIP net.IP) (bool, error) {
	cursor := ""
	matched := false
	for {
		page, err := s.policies.ListByAgent(ctx, req.AgentID, cursor, policyPageSize)
		if err != nil {
			return false, err
		}
		for _, policy := range page.Items {
			allowed, err := policyAllows(policy, req, targetIP)
			if err != nil {
				return false, err
			}
			if allowed {
				matched = true
			}
		}
		if !page.HasMore {
			return matched, nil
		}
		if page.NextCursor == "" || page.NextCursor == cursor {
			return false, errors.New("invalid policy pagination")
		}
		cursor = page.NextCursor
	}
}

func policyAllows(policy storage.AgentPolicy, req StreamAuthorizationRequest, targetIP net.IP) (bool, error) {
	if len(policy.AgentID) > maxScopeAgentIDBytes {
		return false, ErrForbidden
	}
	agentID := strings.TrimSpace(policy.AgentID)
	if agentID == "" {
		return false, ErrForbidden
	}
	if agentID != req.AgentID {
		return false, nil
	}
	if len(policy.Protocol) > 32 {
		return false, ErrForbidden
	}
	protocol := normalizeProtocol(policy.Protocol)
	if protocol == "" {
		return false, ErrForbidden
	}
	if protocol != req.Protocol {
		return false, nil
	}
	if len(policy.TargetHost) > maxPolicyTargetBytes {
		return false, ErrForbidden
	}
	host := strings.TrimSpace(policy.TargetHost)
	if host == "" {
		return false, ErrForbidden
	}
	if !strings.EqualFold(host, req.TargetHost) {
		return false, nil
	}
	if policy.TargetPort < 1 || policy.TargetPort > 65535 {
		return false, ErrForbidden
	}
	if policy.TargetPort != req.TargetPort {
		return false, nil
	}
	cidrs, err := parseCIDRList(policy.AllowedCIDRs)
	if err != nil {
		return false, err
	}
	if len(cidrs) > 0 {
		if targetIP == nil {
			return false, nil
		}
		allowed := false
		for _, network := range cidrs {
			if network.Contains(targetIP) {
				allowed = true
				break
			}
		}
		if !allowed {
			return false, nil
		}
	}
	ports, err := parsePortMatcher(policy.AllowedPorts)
	if err != nil {
		return false, err
	}
	return ports.allows(req.TargetPort), nil
}

func parseCIDRList(raw string) ([]*net.IPNet, error) {
	values, err := parseStringList(raw, maxScopeCIDRs)
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

func parseStringList(raw string, limit int) ([]string, error) {
	if len(raw) > maxTokenScopeJSONBytes {
		return nil, ErrInvalidTokenScope
	}
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "[]" {
		return nil, nil
	}
	if strings.HasPrefix(raw, "[") {
		var values []string
		if err := json.Unmarshal([]byte(raw), &values); err != nil {
			return nil, err
		}
		if len(values) > limit {
			return nil, ErrInvalidTokenScope
		}
		for i := range values {
			values[i] = strings.TrimSpace(values[i])
		}
		return values, nil
	}
	parts := strings.Split(raw, ",")
	if len(parts) > limit {
		return nil, ErrInvalidTokenScope
	}
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out, nil
}

func parsePortMatcher(raw string) (portMatcher, error) {
	if len(raw) > maxTokenScopeJSONBytes {
		return portMatcher{}, ErrInvalidTokenScope
	}
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "[]" {
		return portMatcher{}, nil
	}
	if strings.HasPrefix(raw, "[") {
		var ports []int
		if err := json.Unmarshal([]byte(raw), &ports); err != nil {
			return portMatcher{}, err
		}
		if len(ports) > maxScopePorts {
			return portMatcher{}, ErrInvalidTokenScope
		}
		matcher := portMatcher{intervals: make([]portInterval, 0, len(ports))}
		for _, port := range ports {
			if port < 1 || port > 65535 {
				return portMatcher{}, ErrInvalidTokenScope
			}
			matcher.intervals = append(matcher.intervals, portInterval{first: port, last: port})
		}
		return matcher, nil
	}
	parts := strings.Split(raw, ",")
	if len(parts) > maxScopePorts {
		return portMatcher{}, ErrInvalidTokenScope
	}
	matcher := portMatcher{intervals: make([]portInterval, 0, len(parts))}
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if strings.Contains(part, "-") {
			bounds := strings.SplitN(part, "-", 2)
			lo, errLo := strconv.Atoi(strings.TrimSpace(bounds[0]))
			hi, errHi := strconv.Atoi(strings.TrimSpace(bounds[1]))
			if errLo != nil || errHi != nil || lo < 1 || hi > 65535 || lo > hi {
				return portMatcher{}, ErrInvalidTokenScope
			}
			matcher.intervals = append(matcher.intervals, portInterval{first: lo, last: hi})
			continue
		}
		port, err := strconv.Atoi(part)
		if err != nil || port < 1 || port > 65535 {
			return portMatcher{}, ErrInvalidTokenScope
		}
		matcher.intervals = append(matcher.intervals, portInterval{first: port, last: port})
	}
	return matcher, nil
}

func (m portMatcher) allows(port int) bool {
	if len(m.intervals) == 0 {
		return true
	}
	for _, interval := range m.intervals {
		if port >= interval.first && port <= interval.last {
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
