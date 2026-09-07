# Task 3 review package

Base HEAD: `163fe12121d2f839ea4bf4907f55a8e7dd835057`

## Changed files

```text
Only in .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-3-after/internal/auth: credential_service.go
Only in .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-3-before/internal/auth: credential_service.go.__ABSENT__
Only in .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-3-after/internal/auth: credential_service_test.go
Only in .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-3-before/internal/auth: credential_service_test.go.__ABSENT__
Files .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-3-before/internal/auth/password.go and .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-3-after/internal/auth/password.go differ
Files .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-3-before/internal/auth/service.go and .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-3-after/internal/auth/service.go differ
```

## Full task-only diff

```diff
diff -ruN .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-3-before/internal/auth/credential_service.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-3-after/internal/auth/credential_service.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-3-before/internal/auth/credential_service.go	1970-01-01 08:00:00
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-3-after/internal/auth/credential_service.go	2026-09-06 14:49:53
@@ -0,0 +1,661 @@
+package auth
+
+import (
+	"context"
+	"encoding/json"
+	"errors"
+	"fmt"
+	"net"
+	"sort"
+	"strconv"
+	"strings"
+	"sync"
+	"time"
+
+	"github.com/tunnelmesh/tunnelmesh/internal/storage"
+)
+
+const (
+	lastUsedQueueCapacity = 128
+	lastUsedWriteTimeout  = 2 * time.Second
+	policyPageSize        = 100
+)
+
+type TokenScope struct {
+	AgentIDs    []string `json:"agentIds,omitempty"`
+	Protocols   []string `json:"protocols,omitempty"`
+	TargetCIDRs []string `json:"targetCIDRs,omitempty"`
+	TargetPorts []int    `json:"targetPorts,omitempty"`
+}
+
+type TokenIdentity struct {
+	TokenID     string
+	OwnerUserID string
+	AgentID     string
+	NodeID      string
+	Prefix      string
+	Type        storage.TokenType
+	Scope       TokenScope
+}
+
+type CreateTokenInput struct {
+	Type        storage.TokenType `json:"type"`
+	OwnerUserID string            `json:"ownerUserId"`
+	AgentID     string            `json:"agentId,omitempty"`
+	NodeID      string            `json:"nodeId,omitempty"`
+	Scope       TokenScope        `json:"scope"`
+	ExpiresAt   *time.Time        `json:"expiresAt,omitempty"`
+}
+
+type CreatedToken struct {
+	TokenIdentity
+	Secret    string     `json:"secret"`
+	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
+}
+
+type StreamAuthorizationRequest struct {
+	AgentID    string
+	Protocol   string
+	TargetHost string
+	TargetPort int
+}
+
+type lastUsedUpdate struct {
+	tokenID string
+	when    time.Time
+}
+
+// CredentialService owns service-token lifecycle and stream authorization.
+// Management sessions remain exclusively owned by AuthService/api_tokens.
+type CredentialService struct {
+	tokens   storage.ServiceTokenRepository
+	users    storage.UserRepository
+	agents   storage.AgentRepository
+	nodes    storage.NodeRepository
+	policies storage.PolicyRepository
+
+	workerCtx context.Context
+	cancel    context.CancelFunc
+	lastUsed  chan lastUsedUpdate
+	wg        sync.WaitGroup
+	closeOnce sync.Once
+}
+
+// NewCredentialService accepts a storage.DB or repository interfaces so the
+// authorization rules remain independent of SQL and straightforward to test.
+func NewCredentialService(source any, repos ...any) *CredentialService {
+	ctx, cancel := context.WithCancel(context.Background())
+	s := &CredentialService{
+		workerCtx: ctx,
+		cancel:    cancel,
+		lastUsed:  make(chan lastUsedUpdate, lastUsedQueueCapacity),
+	}
+	bindCredentialRepository(s, source)
+	for _, repo := range repos {
+		bindCredentialRepository(s, repo)
+	}
+	if s.tokens != nil {
+		s.wg.Add(1)
+		go s.runLastUsedWorker()
+	}
+	return s
+}
+
+func bindCredentialRepository(s *CredentialService, source any) {
+	switch v := source.(type) {
+	case *storage.DB:
+		s.tokens, s.users, s.agents, s.nodes, s.policies = v.ServiceTokens(), v.Users(), v.Agents(), v.Nodes(), v.Policies()
+	case storage.ServiceTokenRepository:
+		s.tokens = v
+	case storage.UserRepository:
+		s.users = v
+	case storage.AgentRepository:
+		s.agents = v
+	case storage.NodeRepository:
+		s.nodes = v
+	case storage.PolicyRepository:
+		s.policies = v
+	}
+}
+
+func (s *CredentialService) Close() error {
+	if s == nil {
+		return nil
+	}
+	s.closeOnce.Do(func() {
+		s.cancel()
+		s.wg.Wait()
+	})
+	return nil
+}
+
+func (s *CredentialService) Create(ctx context.Context, in CreateTokenInput) (CreatedToken, error) {
+	if err := s.requireLifecycleRepositories(); err != nil {
+		return CreatedToken{}, err
+	}
+	in.OwnerUserID = strings.TrimSpace(in.OwnerUserID)
+	in.AgentID = strings.TrimSpace(in.AgentID)
+	in.NodeID = strings.TrimSpace(in.NodeID)
+	if err := validateTokenBinding(in); err != nil {
+		return CreatedToken{}, err
+	}
+	scope, err := normalizeTokenScope(in.Scope)
+	if err != nil {
+		return CreatedToken{}, err
+	}
+	if in.ExpiresAt != nil && !in.ExpiresAt.After(time.Now().UTC()) {
+		return CreatedToken{}, fmt.Errorf("expiration must be in the future")
+	}
+	if err := s.validateOwnerAndBinding(ctx, in.Type, in.OwnerUserID, in.AgentID, in.NodeID); err != nil {
+		return CreatedToken{}, err
+	}
+	if err := s.validateScopedAgents(ctx, in.Type, in.OwnerUserID, in.AgentID, scope.AgentIDs); err != nil {
+		return CreatedToken{}, err
+	}
+	raw, err := randomToken(32)
+	if err != nil {
+		return CreatedToken{}, fmt.Errorf("generate service token: %w", err)
+	}
+	idEntropy, err := randomToken(32)
+	if err != nil {
+		return CreatedToken{}, fmt.Errorf("generate service token id: %w", err)
+	}
+	id := "stok_" + idEntropy[:22]
+	scopeJSON, err := json.Marshal(scope)
+	if err != nil {
+		return CreatedToken{}, fmt.Errorf("encode token scope: %w", err)
+	}
+	now := time.Now().UTC()
+	record := storage.ServiceToken{
+		ID:          id,
+		Type:        in.Type,
+		OwnerUserID: in.OwnerUserID,
+		AgentID:     in.AgentID,
+		NodeID:      in.NodeID,
+		Prefix:      tokenPrefix(raw),
+		TokenHash:   hashToken(raw),
+		Scope:       string(scopeJSON),
+		ExpiresAt:   in.ExpiresAt,
+		CreatedAt:   now,
+		UpdatedAt:   now,
+	}
+	if err := s.tokens.Create(ctx, record); err != nil {
+		return CreatedToken{}, err
+	}
+	return createdToken(record, scope, raw), nil
+}
+
+func (s *CredentialService) ValidateAs(ctx context.Context, raw string, expected storage.TokenType) (TokenIdentity, error) {
+	if s == nil || s.tokens == nil || strings.TrimSpace(raw) == "" || !validTokenType(expected) {
+		return TokenIdentity{}, ErrUnauthenticated
+	}
+	record, err := s.tokens.GetByHash(ctx, hashToken(raw))
+	if err != nil || record.Type != expected {
+		return TokenIdentity{}, ErrUnauthenticated
+	}
+	id, err := s.identityFromRecord(ctx, record)
+	if err != nil {
+		return TokenIdentity{}, ErrUnauthenticated
+	}
+	s.enqueueLastUsed(record.ID)
+	return id, nil
+}
+
+func (s *CredentialService) AuthorizeStream(ctx context.Context, id TokenIdentity, req StreamAuthorizationRequest) error {
+	if s == nil || s.tokens == nil || s.users == nil || s.agents == nil || s.policies == nil || id.TokenID == "" || id.Type != storage.TokenTypeClient {
+		return ErrForbidden
+	}
+	record, err := s.tokens.Get(ctx, id.TokenID)
+	if err != nil || record.Type != storage.TokenTypeClient || record.OwnerUserID != id.OwnerUserID {
+		return ErrForbidden
+	}
+	current, err := s.identityFromRecord(ctx, record)
+	if err != nil {
+		return ErrForbidden
+	}
+	req.AgentID = strings.TrimSpace(req.AgentID)
+	req.Protocol = normalizeProtocol(req.Protocol)
+	req.TargetHost = strings.TrimSpace(req.TargetHost)
+	if req.AgentID == "" || req.Protocol == "" || req.TargetHost == "" || req.TargetPort < 1 || req.TargetPort > 65535 {
+		return ErrForbidden
+	}
+	agent, err := s.agents.Get(ctx, req.AgentID)
+	if err != nil || !agent.Enabled || agent.OwnerUserID != current.OwnerUserID {
+		return ErrForbidden
+	}
+	targetIP := net.ParseIP(req.TargetHost)
+	if !scopeAllows(current.Scope, req, targetIP) {
+		return ErrForbidden
+	}
+	allowed, err := s.agentPolicyAllows(ctx, req, targetIP)
+	if err != nil || !allowed {
+		return ErrForbidden
+	}
+	return nil
+}
+
+func (s *CredentialService) Rotate(ctx context.Context, id string) (CreatedToken, error) {
+	if err := s.requireLifecycleRepositories(); err != nil {
+		return CreatedToken{}, err
+	}
+	record, err := s.tokens.Get(ctx, strings.TrimSpace(id))
+	if err != nil {
+		return CreatedToken{}, err
+	}
+	identity, err := s.identityFromRecord(ctx, record)
+	if err != nil {
+		return CreatedToken{}, ErrUnauthenticated
+	}
+	raw, err := randomToken(32)
+	if err != nil {
+		return CreatedToken{}, fmt.Errorf("generate service token: %w", err)
+	}
+	idEntropy, err := randomToken(32)
+	if err != nil {
+		return CreatedToken{}, fmt.Errorf("generate service token id: %w", err)
+	}
+	now := time.Now().UTC()
+	replacement := record
+	replacement.ID = "stok_" + idEntropy[:22]
+	replacement.Prefix = tokenPrefix(raw)
+	replacement.TokenHash = hashToken(raw)
+	replacement.RevokedAt = nil
+	replacement.LastUsedAt = nil
+	replacement.CreatedAt = now
+	replacement.UpdatedAt = now
+	if err := s.tokens.Rotate(ctx, record.ID, replacement, now); err != nil {
+		return CreatedToken{}, err
+	}
+	return createdToken(replacement, identity.Scope, raw), nil
+}
+
+func (s *CredentialService) Revoke(ctx context.Context, id string) error {
+	if s == nil || s.tokens == nil {
+		return errors.New("service token repository is required")
+	}
+	return s.tokens.Revoke(ctx, strings.TrimSpace(id), time.Now().UTC())
+}
+
+func (s *CredentialService) requireLifecycleRepositories() error {
+	if s == nil || s.tokens == nil || s.users == nil || s.agents == nil || s.nodes == nil {
+		return errors.New("credential repositories are required")
+	}
+	return nil
+}
+
+func validateTokenBinding(in CreateTokenInput) error {
+	if !validTokenType(in.Type) {
+		return ErrInvalidTokenType
+	}
+	if in.OwnerUserID == "" {
+		return errors.New("owner user is required")
+	}
+	switch in.Type {
+	case storage.TokenTypeAgent:
+		if in.AgentID == "" || in.NodeID != "" {
+			return errors.New("agent token requires only an agent binding")
+		}
+	case storage.TokenTypeClient:
+		if in.AgentID != "" || in.NodeID != "" {
+			return errors.New("client token must be bound only to its owner")
+		}
+	case storage.TokenTypeServerNode:
+		if in.NodeID == "" || in.AgentID != "" {
+			return errors.New("server-node token requires only a node binding")
+		}
+	}
+	return nil
+}
+
+func validTokenType(tokenType storage.TokenType) bool {
+	return tokenType == storage.TokenTypeAgent || tokenType == storage.TokenTypeClient || tokenType == storage.TokenTypeServerNode
+}
+
+func (s *CredentialService) validateOwnerAndBinding(ctx context.Context, tokenType storage.TokenType, ownerID, agentID, nodeID string) error {
+	owner, err := s.users.Get(ctx, ownerID)
+	if err != nil || owner.Disabled {
+		return ErrUnauthenticated
+	}
+	switch tokenType {
+	case storage.TokenTypeAgent:
+		agent, err := s.agents.Get(ctx, agentID)
+		if err != nil || !agent.Enabled || agent.OwnerUserID != ownerID {
+			return ErrUnauthenticated
+		}
+	case storage.TokenTypeServerNode:
+		node, err := s.nodes.Get(ctx, nodeID)
+		if err != nil || (node.ExpiresAt != nil && !node.ExpiresAt.After(time.Now().UTC())) {
+			return ErrUnauthenticated
+		}
+	}
+	return nil
+}
+
+func (s *CredentialService) validateScopedAgents(ctx context.Context, tokenType storage.TokenType, ownerID, boundAgentID string, agentIDs []string) error {
+	for _, agentID := range agentIDs {
+		if tokenType == storage.TokenTypeAgent && agentID != boundAgentID {
+			return ErrInvalidTokenScope
+		}
+		agent, err := s.agents.Get(ctx, agentID)
+		if err != nil || !agent.Enabled || agent.OwnerUserID != ownerID {
+			return ErrInvalidTokenScope
+		}
+	}
+	return nil
+}
+
+func (s *CredentialService) identityFromRecord(ctx context.Context, record storage.ServiceToken) (TokenIdentity, error) {
+	now := time.Now().UTC()
+	if !validTokenType(record.Type) || record.ID == "" || record.OwnerUserID == "" || record.RevokedAt != nil || (record.ExpiresAt != nil && !record.ExpiresAt.After(now)) {
+		return TokenIdentity{}, ErrUnauthenticated
+	}
+	if err := validateTokenBinding(CreateTokenInput{Type: record.Type, OwnerUserID: record.OwnerUserID, AgentID: record.AgentID, NodeID: record.NodeID}); err != nil {
+		return TokenIdentity{}, ErrUnauthenticated
+	}
+	if s.users == nil || s.agents == nil || s.nodes == nil {
+		return TokenIdentity{}, ErrUnauthenticated
+	}
+	if err := s.validateOwnerAndBinding(ctx, record.Type, record.OwnerUserID, record.AgentID, record.NodeID); err != nil {
+		return TokenIdentity{}, ErrUnauthenticated
+	}
+	var scope TokenScope
+	if err := json.Unmarshal([]byte(record.Scope), &scope); err != nil {
+		return TokenIdentity{}, ErrUnauthenticated
+	}
+	scope, err := normalizeTokenScope(scope)
+	if err != nil {
+		return TokenIdentity{}, ErrUnauthenticated
+	}
+	return TokenIdentity{TokenID: record.ID, OwnerUserID: record.OwnerUserID, AgentID: record.AgentID, NodeID: record.NodeID, Prefix: record.Prefix, Type: record.Type, Scope: scope}, nil
+}
+
+func normalizeTokenScope(scope TokenScope) (TokenScope, error) {
+	var out TokenScope
+	seenAgents := make(map[string]struct{})
+	for _, raw := range scope.AgentIDs {
+		agentID := strings.TrimSpace(raw)
+		if agentID == "" {
+			return TokenScope{}, fmt.Errorf("%w: empty agent id", ErrInvalidTokenScope)
+		}
+		if _, ok := seenAgents[agentID]; !ok {
+			seenAgents[agentID] = struct{}{}
+			out.AgentIDs = append(out.AgentIDs, agentID)
+		}
+	}
+	seenProtocols := make(map[string]struct{})
+	for _, raw := range scope.Protocols {
+		protocol := normalizeProtocol(raw)
+		if protocol == "" {
+			return TokenScope{}, fmt.Errorf("%w: invalid protocol %q", ErrInvalidTokenScope, raw)
+		}
+		if _, ok := seenProtocols[protocol]; !ok {
+			seenProtocols[protocol] = struct{}{}
+			out.Protocols = append(out.Protocols, protocol)
+		}
+	}
+	seenCIDRs := make(map[string]struct{})
+	for _, raw := range scope.TargetCIDRs {
+		_, network, err := net.ParseCIDR(strings.TrimSpace(raw))
+		if err != nil {
+			return TokenScope{}, fmt.Errorf("%w: invalid CIDR %q", ErrInvalidTokenScope, raw)
+		}
+		canonical := network.String()
+		if _, ok := seenCIDRs[canonical]; !ok {
+			seenCIDRs[canonical] = struct{}{}
+			out.TargetCIDRs = append(out.TargetCIDRs, canonical)
+		}
+	}
+	seenPorts := make(map[int]struct{})
+	for _, port := range scope.TargetPorts {
+		if port < 1 || port > 65535 {
+			return TokenScope{}, fmt.Errorf("%w: invalid port %d", ErrInvalidTokenScope, port)
+		}
+		if _, ok := seenPorts[port]; !ok {
+			seenPorts[port] = struct{}{}
+			out.TargetPorts = append(out.TargetPorts, port)
+		}
+	}
+	sort.Strings(out.AgentIDs)
+	sort.Strings(out.Protocols)
+	sort.Strings(out.TargetCIDRs)
+	sort.Ints(out.TargetPorts)
+	return out, nil
+}
+
+func normalizeProtocol(raw string) string {
+	switch protocol := strings.ToLower(strings.TrimSpace(raw)); protocol {
+	case "tcp", "udp", "http", "ws":
+		return protocol
+	case "websocket":
+		return "ws"
+	default:
+		return ""
+	}
+}
+
+func scopeAllows(scope TokenScope, req StreamAuthorizationRequest, targetIP net.IP) bool {
+	if len(scope.AgentIDs) > 0 && !containsString(scope.AgentIDs, req.AgentID) {
+		return false
+	}
+	if len(scope.Protocols) > 0 && !containsString(scope.Protocols, req.Protocol) {
+		return false
+	}
+	if len(scope.TargetPorts) > 0 && !containsInt(scope.TargetPorts, req.TargetPort) {
+		return false
+	}
+	if len(scope.TargetCIDRs) > 0 {
+		if targetIP == nil {
+			return false
+		}
+		allowed := false
+		for _, raw := range scope.TargetCIDRs {
+			_, network, err := net.ParseCIDR(raw)
+			if err != nil {
+				return false
+			}
+			if network.Contains(targetIP) {
+				allowed = true
+			}
+		}
+		if !allowed {
+			return false
+		}
+	}
+	return true
+}
+
+func (s *CredentialService) agentPolicyAllows(ctx context.Context, req StreamAuthorizationRequest, targetIP net.IP) (bool, error) {
+	cursor := ""
+	for {
+		page, err := s.policies.ListByAgent(ctx, req.AgentID, cursor, policyPageSize)
+		if err != nil {
+			return false, err
+		}
+		for _, policy := range page.Items {
+			if policyAllows(policy, req, targetIP) {
+				return true, nil
+			}
+		}
+		if !page.HasMore {
+			return false, nil
+		}
+		if page.NextCursor == "" || page.NextCursor == cursor {
+			return false, errors.New("invalid policy pagination")
+		}
+		cursor = page.NextCursor
+	}
+}
+
+func policyAllows(policy storage.AgentPolicy, req StreamAuthorizationRequest, targetIP net.IP) bool {
+	if policy.AgentID != "" && policy.AgentID != req.AgentID {
+		return false
+	}
+	if protocol := normalizeProtocol(policy.Protocol); protocol == "" || protocol != req.Protocol {
+		return false
+	}
+	if host := strings.TrimSpace(policy.TargetHost); host != "" && !strings.EqualFold(host, req.TargetHost) {
+		return false
+	}
+	if policy.TargetPort != 0 && policy.TargetPort != req.TargetPort {
+		return false
+	}
+	cidrs, err := parseCIDRList(policy.AllowedCIDRs)
+	if err != nil {
+		return false
+	}
+	if len(cidrs) > 0 {
+		if targetIP == nil {
+			return false
+		}
+		allowed := false
+		for _, network := range cidrs {
+			if network.Contains(targetIP) {
+				allowed = true
+				break
+			}
+		}
+		if !allowed {
+			return false
+		}
+	}
+	ports, err := parsePortList(policy.AllowedPorts)
+	return err == nil && (len(ports) == 0 || containsInt(ports, req.TargetPort))
+}
+
+func parseCIDRList(raw string) ([]*net.IPNet, error) {
+	values, err := parseStringList(raw)
+	if err != nil {
+		return nil, err
+	}
+	out := make([]*net.IPNet, 0, len(values))
+	for _, value := range values {
+		_, network, err := net.ParseCIDR(value)
+		if err != nil {
+			return nil, err
+		}
+		out = append(out, network)
+	}
+	return out, nil
+}
+
+func parseStringList(raw string) ([]string, error) {
+	raw = strings.TrimSpace(raw)
+	if raw == "" || raw == "[]" {
+		return nil, nil
+	}
+	if strings.HasPrefix(raw, "[") {
+		var values []string
+		if err := json.Unmarshal([]byte(raw), &values); err != nil {
+			return nil, err
+		}
+		for i := range values {
+			values[i] = strings.TrimSpace(values[i])
+		}
+		return values, nil
+	}
+	parts := strings.Split(raw, ",")
+	out := make([]string, 0, len(parts))
+	for _, part := range parts {
+		if part = strings.TrimSpace(part); part != "" {
+			out = append(out, part)
+		}
+	}
+	return out, nil
+}
+
+func parsePortList(raw string) ([]int, error) {
+	raw = strings.TrimSpace(raw)
+	if raw == "" || raw == "[]" {
+		return nil, nil
+	}
+	if strings.HasPrefix(raw, "[") {
+		var ports []int
+		if err := json.Unmarshal([]byte(raw), &ports); err != nil {
+			return nil, err
+		}
+		for _, port := range ports {
+			if port < 1 || port > 65535 {
+				return nil, ErrInvalidTokenScope
+			}
+		}
+		return ports, nil
+	}
+	var ports []int
+	for _, part := range strings.Split(raw, ",") {
+		part = strings.TrimSpace(part)
+		if part == "" {
+			continue
+		}
+		if strings.Contains(part, "-") {
+			bounds := strings.SplitN(part, "-", 2)
+			lo, errLo := strconv.Atoi(strings.TrimSpace(bounds[0]))
+			hi, errHi := strconv.Atoi(strings.TrimSpace(bounds[1]))
+			if errLo != nil || errHi != nil || lo < 1 || hi > 65535 || lo > hi {
+				return nil, ErrInvalidTokenScope
+			}
+			for port := lo; port <= hi; port++ {
+				ports = append(ports, port)
+			}
+			continue
+		}
+		port, err := strconv.Atoi(part)
+		if err != nil || port < 1 || port > 65535 {
+			return nil, ErrInvalidTokenScope
+		}
+		ports = append(ports, port)
+	}
+	return ports, nil
+}
+
+func containsString(values []string, want string) bool {
+	for _, value := range values {
+		if value == want {
+			return true
+		}
+	}
+	return false
+}
+
+func containsInt(values []int, want int) bool {
+	for _, value := range values {
+		if value == want {
+			return true
+		}
+	}
+	return false
+}
+
+func createdToken(record storage.ServiceToken, scope TokenScope, raw string) CreatedToken {
+	return CreatedToken{
+		TokenIdentity: TokenIdentity{TokenID: record.ID, OwnerUserID: record.OwnerUserID, AgentID: record.AgentID, NodeID: record.NodeID, Prefix: record.Prefix, Type: record.Type, Scope: scope},
+		Secret:        raw,
+		ExpiresAt:     record.ExpiresAt,
+	}
+}
+
+func (s *CredentialService) enqueueLastUsed(tokenID string) {
+	if s == nil || tokenID == "" {
+		return
+	}
+	update := lastUsedUpdate{tokenID: tokenID, when: time.Now().UTC()}
+	select {
+	case <-s.workerCtx.Done():
+	case s.lastUsed <- update:
+	default:
+		// last_used_at is best-effort; dropping is safer than blocking auth.
+	}
+}
+
+func (s *CredentialService) runLastUsedWorker() {
+	defer s.wg.Done()
+	for {
+		select {
+		case <-s.workerCtx.Done():
+			return
+		case update := <-s.lastUsed:
+			ctx, cancel := context.WithTimeout(s.workerCtx, lastUsedWriteTimeout)
+			_ = s.tokens.TouchLastUsed(ctx, update.tokenID, update.when)
+			cancel()
+		}
+	}
+}
diff -ruN .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-3-before/internal/auth/credential_service_test.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-3-after/internal/auth/credential_service_test.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-3-before/internal/auth/credential_service_test.go	1970-01-01 08:00:00
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-3-after/internal/auth/credential_service_test.go	2026-09-06 14:49:53
@@ -0,0 +1,521 @@
+package auth
+
+import (
+	"context"
+	"database/sql"
+	"encoding/base64"
+	"errors"
+	"sync"
+	"testing"
+	"time"
+
+	"github.com/tunnelmesh/tunnelmesh/internal/storage"
+)
+
+func TestCredentialServiceCreatesAndValidatesEachTokenType(t *testing.T) {
+	ctx := context.Background()
+	repos := newCredentialRepos()
+	repos.users.items["owner"] = storage.User{ID: "owner"}
+	repos.agents.items["agent-a"] = storage.Agent{ID: "agent-a", OwnerUserID: "owner", Enabled: true}
+	repos.nodes.items["node-a"] = storage.ServerNode{ID: "node-a"}
+	service := newTestCredentialService(t, repos)
+
+	tests := []struct {
+		name string
+		in   CreateTokenInput
+	}{
+		{name: "agent", in: CreateTokenInput{Type: storage.TokenTypeAgent, OwnerUserID: "owner", AgentID: "agent-a"}},
+		{name: "client", in: CreateTokenInput{Type: storage.TokenTypeClient, OwnerUserID: "owner"}},
+		{name: "server node", in: CreateTokenInput{Type: storage.TokenTypeServerNode, OwnerUserID: "owner", NodeID: "node-a"}},
+	}
+	for _, tt := range tests {
+		t.Run(tt.name, func(t *testing.T) {
+			created, err := service.Create(ctx, tt.in)
+			if err != nil {
+				t.Fatalf("Create() error = %v", err)
+			}
+			decoded, err := base64.RawURLEncoding.DecodeString(created.Secret)
+			if err != nil || len(decoded) != 32 {
+				t.Fatalf("secret is not 32 random RawURL bytes: len=%d err=%v", len(decoded), err)
+			}
+			identity, err := service.ValidateAs(ctx, created.Secret, tt.in.Type)
+			if err != nil {
+				t.Fatalf("ValidateAs() error = %v", err)
+			}
+			if identity.TokenID != created.TokenID || identity.Type != tt.in.Type || identity.OwnerUserID != "owner" {
+				t.Fatalf("identity = %+v, created = %+v", identity, created)
+			}
+			record, err := repos.tokens.Get(ctx, created.TokenID)
+			if err != nil {
+				t.Fatal(err)
+			}
+			if record.TokenHash == "" || record.TokenHash == created.Secret || record.Scope == created.Secret || record.Prefix == created.Secret {
+				t.Fatalf("repository record leaked raw secret: %+v", record)
+			}
+			if record.TokenHash != hashToken(created.Secret) {
+				t.Fatalf("stored hash = %q, want SHA-256 of secret", record.TokenHash)
+			}
+		})
+	}
+}
+
+func TestCredentialServiceRejectsInvalidLifecycleStateAndRepositoryErrors(t *testing.T) {
+	ctx := context.Background()
+	now := time.Now().UTC()
+	tests := []struct {
+		name   string
+		mutate func(*credentialRepos, *storage.ServiceToken)
+	}{
+		{name: "revoked", mutate: func(_ *credentialRepos, token *storage.ServiceToken) { token.RevokedAt = &now }},
+		{name: "expired", mutate: func(_ *credentialRepos, token *storage.ServiceToken) {
+			expired := now.Add(-time.Second)
+			token.ExpiresAt = &expired
+		}},
+		{name: "disabled owner", mutate: func(r *credentialRepos, _ *storage.ServiceToken) {
+			u := r.users.items["owner"]
+			u.Disabled = true
+			r.users.items["owner"] = u
+		}},
+		{name: "missing owner", mutate: func(r *credentialRepos, _ *storage.ServiceToken) { delete(r.users.items, "owner") }},
+		{name: "disabled agent", mutate: func(r *credentialRepos, _ *storage.ServiceToken) {
+			a := r.agents.items["agent-a"]
+			a.Enabled = false
+			r.agents.items["agent-a"] = a
+		}},
+		{name: "missing agent", mutate: func(r *credentialRepos, _ *storage.ServiceToken) { delete(r.agents.items, "agent-a") }},
+		{name: "wrong owner binding", mutate: func(r *credentialRepos, _ *storage.ServiceToken) {
+			a := r.agents.items["agent-a"]
+			a.OwnerUserID = "other"
+			r.agents.items["agent-a"] = a
+		}},
+		{name: "token repository error", mutate: func(r *credentialRepos, _ *storage.ServiceToken) {
+			r.tokens.getByHashErr = errors.New("database unavailable")
+		}},
+		{name: "owner repository error", mutate: func(r *credentialRepos, _ *storage.ServiceToken) { r.users.getErr = errors.New("database unavailable") }},
+		{name: "agent repository error", mutate: func(r *credentialRepos, _ *storage.ServiceToken) {
+			r.agents.getErr = errors.New("database unavailable")
+		}},
+	}
+	for _, tt := range tests {
+		t.Run(tt.name, func(t *testing.T) {
+			repos := newCredentialRepos()
+			repos.users.items["owner"] = storage.User{ID: "owner"}
+			repos.agents.items["agent-a"] = storage.Agent{ID: "agent-a", OwnerUserID: "owner", Enabled: true}
+			raw := "agent-secret"
+			token := storage.ServiceToken{ID: "token-a", Type: storage.TokenTypeAgent, OwnerUserID: "owner", AgentID: "agent-a", TokenHash: hashToken(raw), Scope: `{}`}
+			tt.mutate(repos, &token)
+			repos.tokens.items[token.ID] = token
+			service := newTestCredentialService(t, repos)
+			if _, err := service.ValidateAs(ctx, raw, storage.TokenTypeAgent); !errors.Is(err, ErrUnauthenticated) {
+				t.Fatalf("ValidateAs() error = %v, want ErrUnauthenticated", err)
+			}
+		})
+	}
+}
+
+func TestCredentialServiceRejectsTypeCrossoverAndInvalidBindings(t *testing.T) {
+	ctx := context.Background()
+	repos := newCredentialRepos()
+	repos.users.items["owner"] = storage.User{ID: "owner"}
+	repos.agents.items["agent-a"] = storage.Agent{ID: "agent-a", OwnerUserID: "owner", Enabled: true}
+	service := newTestCredentialService(t, repos)
+	created, err := service.Create(ctx, CreateTokenInput{Type: storage.TokenTypeAgent, OwnerUserID: "owner", AgentID: "agent-a"})
+	if err != nil {
+		t.Fatal(err)
+	}
+	if _, err := service.ValidateAs(ctx, created.Secret, storage.TokenTypeClient); !errors.Is(err, ErrUnauthenticated) {
+		t.Fatalf("ValidateAs(type crossover) error = %v, want ErrUnauthenticated", err)
+	}
+
+	invalid := []CreateTokenInput{
+		{Type: "unknown", OwnerUserID: "owner"},
+		{Type: storage.TokenTypeClient},
+		{Type: storage.TokenTypeClient, OwnerUserID: "owner", AgentID: "agent-a"},
+		{Type: storage.TokenTypeAgent, OwnerUserID: "owner"},
+		{Type: storage.TokenTypeAgent, OwnerUserID: "owner", AgentID: "missing"},
+		{Type: storage.TokenTypeServerNode, OwnerUserID: "owner"},
+	}
+	for i, in := range invalid {
+		if _, err := service.Create(ctx, in); err == nil {
+			t.Fatalf("Create(invalid[%d]=%+v) succeeded", i, in)
+		}
+	}
+}
+
+func TestCredentialServiceAcceptsEmptyScopeAndRejectsInvalidScope(t *testing.T) {
+	ctx := context.Background()
+	repos := newCredentialRepos()
+	repos.users.items["owner"] = storage.User{ID: "owner"}
+	service := newTestCredentialService(t, repos)
+	if _, err := service.Create(ctx, CreateTokenInput{Type: storage.TokenTypeClient, OwnerUserID: "owner", Scope: TokenScope{}}); err != nil {
+		t.Fatalf("Create(empty scope) error = %v", err)
+	}
+
+	invalidScopes := []TokenScope{
+		{AgentIDs: []string{""}},
+		{Protocols: []string{"icmp"}},
+		{TargetCIDRs: []string{"not-a-cidr"}},
+		{TargetPorts: []int{0}},
+		{TargetPorts: []int{65536}},
+	}
+	for _, scope := range invalidScopes {
+		if _, err := service.Create(ctx, CreateTokenInput{Type: storage.TokenTypeClient, OwnerUserID: "owner", Scope: scope}); err == nil {
+			t.Fatalf("Create(invalid scope %+v) succeeded", scope)
+		}
+	}
+}
+
+func TestCredentialServiceRotateRevokeAndTouchLastUsed(t *testing.T) {
+	ctx := context.Background()
+	repos := newCredentialRepos()
+	repos.users.items["owner"] = storage.User{ID: "owner"}
+	service := newTestCredentialService(t, repos)
+	created, err := service.Create(ctx, CreateTokenInput{Type: storage.TokenTypeClient, OwnerUserID: "owner", Scope: TokenScope{Protocols: []string{"TCP"}}})
+	if err != nil {
+		t.Fatal(err)
+	}
+	if _, err := service.ValidateAs(ctx, created.Secret, storage.TokenTypeClient); err != nil {
+		t.Fatal(err)
+	}
+	select {
+	case touched := <-repos.tokens.touchCh:
+		if touched != created.TokenID {
+			t.Fatalf("touched token = %q, want %q", touched, created.TokenID)
+		}
+	case <-time.After(time.Second):
+		t.Fatal("last-used worker did not update token")
+	}
+
+	rotated, err := service.Rotate(ctx, created.TokenID)
+	if err != nil {
+		t.Fatalf("Rotate() error = %v", err)
+	}
+	if rotated.TokenID == created.TokenID || rotated.Secret == created.Secret || rotated.Scope.Protocols[0] != "tcp" {
+		t.Fatalf("rotated token did not preserve metadata with a new secret: %+v", rotated)
+	}
+	if _, err := service.ValidateAs(ctx, created.Secret, storage.TokenTypeClient); !errors.Is(err, ErrUnauthenticated) {
+		t.Fatalf("old secret remains valid: %v", err)
+	}
+	if _, err := service.ValidateAs(ctx, rotated.Secret, storage.TokenTypeClient); err != nil {
+		t.Fatalf("new secret rejected: %v", err)
+	}
+	if err := service.Revoke(ctx, rotated.TokenID); err != nil {
+		t.Fatalf("Revoke() error = %v", err)
+	}
+	if _, err := service.ValidateAs(ctx, rotated.Secret, storage.TokenTypeClient); !errors.Is(err, ErrUnauthenticated) {
+		t.Fatalf("revoked secret remains valid: %v", err)
+	}
+}
+
+func TestCredentialServiceServerNodeStateFailsClosed(t *testing.T) {
+	ctx := context.Background()
+	now := time.Now().UTC()
+	for _, tt := range []struct {
+		name   string
+		node   storage.ServerNode
+		getErr error
+	}{
+		{name: "missing"},
+		{name: "expired", node: storage.ServerNode{ID: "node-a", ExpiresAt: ptrTime(now.Add(-time.Second))}},
+		{name: "repository error", node: storage.ServerNode{ID: "node-a"}, getErr: errors.New("database unavailable")},
+	} {
+		t.Run(tt.name, func(t *testing.T) {
+			repos := newCredentialRepos()
+			repos.users.items["owner"] = storage.User{ID: "owner"}
+			if tt.node.ID != "" {
+				repos.nodes.items[tt.node.ID] = tt.node
+			}
+			repos.nodes.getErr = tt.getErr
+			raw := "node-secret"
+			repos.tokens.items["node-token"] = storage.ServiceToken{ID: "node-token", Type: storage.TokenTypeServerNode, OwnerUserID: "owner", NodeID: "node-a", TokenHash: hashToken(raw), Scope: `{}`}
+			service := newTestCredentialService(t, repos)
+			if _, err := service.ValidateAs(ctx, raw, storage.TokenTypeServerNode); !errors.Is(err, ErrUnauthenticated) {
+				t.Fatalf("ValidateAs() error = %v, want ErrUnauthenticated", err)
+			}
+		})
+	}
+}
+
+func TestScopeAuthorizationIntersectsTokenAndAgentPolicy(t *testing.T) {
+	ctx := context.Background()
+	repos := newCredentialRepos()
+	repos.users.items["owner"] = storage.User{ID: "owner"}
+	repos.agents.items["agent-a"] = storage.Agent{ID: "agent-a", OwnerUserID: "owner", Enabled: true}
+	repos.agents.items["agent-b"] = storage.Agent{ID: "agent-b", OwnerUserID: "owner", Enabled: true}
+	repos.policies.items["agent-a"] = []storage.AgentPolicy{{ID: "allow-ssh", AgentID: "agent-a", TargetHost: "10.0.0.8", TargetPort: 22, Protocol: "tcp", AllowedCIDRs: "10.0.0.0/24", AllowedPorts: "22"}}
+	service := newTestCredentialService(t, repos)
+	created, err := service.Create(ctx, CreateTokenInput{Type: storage.TokenTypeClient, OwnerUserID: "owner", Scope: TokenScope{AgentIDs: []string{"agent-a"}, Protocols: []string{"TCP"}, TargetCIDRs: []string{"10.0.0.0/24"}, TargetPorts: []int{22}}})
+	if err != nil {
+		t.Fatal(err)
+	}
+	id, err := service.ValidateAs(ctx, created.Secret, storage.TokenTypeClient)
+	if err != nil {
+		t.Fatal(err)
+	}
+	allowed := StreamAuthorizationRequest{AgentID: "agent-a", Protocol: "tcp", TargetHost: "10.0.0.8", TargetPort: 22}
+	if err := service.AuthorizeStream(ctx, id, allowed); err != nil {
+		t.Fatalf("AuthorizeStream(allowed) error = %v", err)
+	}
+
+	denied := []StreamAuthorizationRequest{
+		{AgentID: "agent-b", Protocol: "tcp", TargetHost: "10.0.0.8", TargetPort: 22},
+		{AgentID: "agent-a", Protocol: "udp", TargetHost: "10.0.0.8", TargetPort: 22},
+		{AgentID: "agent-a", Protocol: "tcp", TargetHost: "10.0.1.8", TargetPort: 22},
+		{AgentID: "agent-a", Protocol: "tcp", TargetHost: "10.0.0.8", TargetPort: 23},
+	}
+	for _, req := range denied {
+		if err := service.AuthorizeStream(ctx, id, req); !errors.Is(err, ErrForbidden) {
+			t.Fatalf("AuthorizeStream(%+v) error = %v, want ErrForbidden", req, err)
+		}
+	}
+
+	// A broader token scope cannot override the database-backed Agent Policy.
+	id.Scope = TokenScope{AgentIDs: []string{"agent-a"}, Protocols: []string{"tcp"}, TargetCIDRs: []string{"10.0.0.0/8"}, TargetPorts: []int{22, 23}}
+	if err := service.AuthorizeStream(ctx, id, StreamAuthorizationRequest{AgentID: "agent-a", Protocol: "tcp", TargetHost: "10.0.0.9", TargetPort: 22}); !errors.Is(err, ErrForbidden) {
+		t.Fatalf("token widened Agent Policy: %v", err)
+	}
+}
+
+func TestScopeAuthorizationRechecksStateAndFailsClosed(t *testing.T) {
+	ctx := context.Background()
+	repos := newCredentialRepos()
+	repos.users.items["owner"] = storage.User{ID: "owner"}
+	repos.agents.items["agent-a"] = storage.Agent{ID: "agent-a", OwnerUserID: "owner", Enabled: true}
+	repos.policies.items["agent-a"] = []storage.AgentPolicy{{AgentID: "agent-a", TargetHost: "10.0.0.8", TargetPort: 22, Protocol: "tcp"}}
+	service := newTestCredentialService(t, repos)
+	created, err := service.Create(ctx, CreateTokenInput{Type: storage.TokenTypeClient, OwnerUserID: "owner"})
+	if err != nil {
+		t.Fatal(err)
+	}
+	id, err := service.ValidateAs(ctx, created.Secret, storage.TokenTypeClient)
+	if err != nil {
+		t.Fatal(err)
+	}
+	req := StreamAuthorizationRequest{AgentID: "agent-a", Protocol: "tcp", TargetHost: "10.0.0.8", TargetPort: 22}
+
+	repos.tokens.getErr = errors.New("database unavailable")
+	if err := service.AuthorizeStream(ctx, id, req); !errors.Is(err, ErrForbidden) {
+		t.Fatalf("token repository error authorized stream: %v", err)
+	}
+	repos.tokens.getErr = nil
+	repos.policies.listErr = errors.New("database unavailable")
+	if err := service.AuthorizeStream(ctx, id, req); !errors.Is(err, ErrForbidden) {
+		t.Fatalf("policy repository error authorized stream: %v", err)
+	}
+	repos.policies.listErr = nil
+	revoked := time.Now().UTC()
+	token := repos.tokens.items[id.TokenID]
+	token.RevokedAt = &revoked
+	repos.tokens.items[id.TokenID] = token
+	if err := service.AuthorizeStream(ctx, id, req); !errors.Is(err, ErrForbidden) {
+		t.Fatalf("revoked token authorized a later stream: %v", err)
+	}
+}
+
+func newTestCredentialService(t *testing.T, repos *credentialRepos) *CredentialService {
+	t.Helper()
+	service := NewCredentialService(repos.tokens, repos.users, repos.agents, repos.nodes, repos.policies)
+	t.Cleanup(func() { _ = service.Close() })
+	return service
+}
+
+type credentialRepos struct {
+	tokens   *memoryServiceTokens
+	users    *memoryUsers
+	agents   *memoryAgents
+	nodes    *memoryNodes
+	policies *memoryPolicies
+}
+
+func newCredentialRepos() *credentialRepos {
+	return &credentialRepos{
+		tokens:   &memoryServiceTokens{items: make(map[string]storage.ServiceToken), touchCh: make(chan string, 16)},
+		users:    &memoryUsers{items: make(map[string]storage.User)},
+		agents:   &memoryAgents{items: make(map[string]storage.Agent)},
+		nodes:    &memoryNodes{items: make(map[string]storage.ServerNode)},
+		policies: &memoryPolicies{items: make(map[string][]storage.AgentPolicy)},
+	}
+}
+
+type memoryServiceTokens struct {
+	mu           sync.Mutex
+	items        map[string]storage.ServiceToken
+	getErr       error
+	getByHashErr error
+	touchCh      chan string
+}
+
+func (r *memoryServiceTokens) Create(_ context.Context, token storage.ServiceToken) error {
+	r.mu.Lock()
+	defer r.mu.Unlock()
+	if token.ID == "" {
+		return errors.New("token id is required")
+	}
+	for _, existing := range r.items {
+		if existing.TokenHash == token.TokenHash {
+			return errors.New("duplicate token hash")
+		}
+	}
+	r.items[token.ID] = token
+	return nil
+}
+func (r *memoryServiceTokens) Get(_ context.Context, id string) (storage.ServiceToken, error) {
+	r.mu.Lock()
+	defer r.mu.Unlock()
+	if r.getErr != nil {
+		return storage.ServiceToken{}, r.getErr
+	}
+	token, ok := r.items[id]
+	if !ok {
+		return storage.ServiceToken{}, sql.ErrNoRows
+	}
+	return token, nil
+}
+func (r *memoryServiceTokens) GetByHash(_ context.Context, hash string) (storage.ServiceToken, error) {
+	r.mu.Lock()
+	defer r.mu.Unlock()
+	if r.getByHashErr != nil {
+		return storage.ServiceToken{}, r.getByHashErr
+	}
+	for _, token := range r.items {
+		if token.TokenHash == hash {
+			return token, nil
+		}
+	}
+	return storage.ServiceToken{}, sql.ErrNoRows
+}
+func (r *memoryServiceTokens) List(context.Context, storage.ServiceTokenFilter, string, int) (storage.Page[storage.ServiceToken], error) {
+	return storage.Page[storage.ServiceToken]{}, nil
+}
+func (r *memoryServiceTokens) Revoke(_ context.Context, id string, when time.Time) error {
+	r.mu.Lock()
+	defer r.mu.Unlock()
+	token, ok := r.items[id]
+	if !ok {
+		return sql.ErrNoRows
+	}
+	token.RevokedAt = &when
+	r.items[id] = token
+	return nil
+}
+func (r *memoryServiceTokens) TouchLastUsed(_ context.Context, id string, when time.Time) error {
+	r.mu.Lock()
+	token, ok := r.items[id]
+	if ok {
+		token.LastUsedAt = &when
+		r.items[id] = token
+	}
+	r.mu.Unlock()
+	if !ok {
+		return sql.ErrNoRows
+	}
+	select {
+	case r.touchCh <- id:
+	default:
+	}
+	return nil
+}
+func (r *memoryServiceTokens) Rotate(_ context.Context, oldID string, replacement storage.ServiceToken, when time.Time) error {
+	r.mu.Lock()
+	defer r.mu.Unlock()
+	old, ok := r.items[oldID]
+	if !ok {
+		return sql.ErrNoRows
+	}
+	if old.RevokedAt != nil {
+		return storage.ErrServiceTokenRevoked
+	}
+	old.RevokedAt = &when
+	r.items[oldID] = old
+	r.items[replacement.ID] = replacement
+	return nil
+}
+
+type memoryUsers struct {
+	items  map[string]storage.User
+	getErr error
+}
+
+func (r *memoryUsers) Create(context.Context, storage.User) error { return nil }
+func (r *memoryUsers) Get(_ context.Context, id string) (storage.User, error) {
+	if r.getErr != nil {
+		return storage.User{}, r.getErr
+	}
+	u, ok := r.items[id]
+	if !ok {
+		return storage.User{}, sql.ErrNoRows
+	}
+	return u, nil
+}
+func (r *memoryUsers) GetByUsername(context.Context, string) (storage.User, error) {
+	return storage.User{}, sql.ErrNoRows
+}
+func (r *memoryUsers) Update(context.Context, storage.User) error { return nil }
+func (r *memoryUsers) Delete(context.Context, string) error       { return nil }
+func (r *memoryUsers) List(context.Context, string, int) (storage.Page[storage.User], error) {
+	return storage.Page[storage.User]{}, nil
+}
+
+type memoryAgents struct {
+	items  map[string]storage.Agent
+	getErr error
+}
+
+func (r *memoryAgents) Create(context.Context, storage.Agent) error { return nil }
+func (r *memoryAgents) Get(_ context.Context, id string) (storage.Agent, error) {
+	if r.getErr != nil {
+		return storage.Agent{}, r.getErr
+	}
+	a, ok := r.items[id]
+	if !ok {
+		return storage.Agent{}, sql.ErrNoRows
+	}
+	return a, nil
+}
+func (r *memoryAgents) Update(context.Context, storage.Agent) error { return nil }
+func (r *memoryAgents) Delete(context.Context, string) error        { return nil }
+func (r *memoryAgents) List(context.Context, string, int) (storage.Page[storage.Agent], error) {
+	return storage.Page[storage.Agent]{}, nil
+}
+
+type memoryNodes struct {
+	items  map[string]storage.ServerNode
+	getErr error
+}
+
+func (r *memoryNodes) Create(context.Context, storage.ServerNode) error { return nil }
+func (r *memoryNodes) Get(_ context.Context, id string) (storage.ServerNode, error) {
+	if r.getErr != nil {
+		return storage.ServerNode{}, r.getErr
+	}
+	n, ok := r.items[id]
+	if !ok {
+		return storage.ServerNode{}, sql.ErrNoRows
+	}
+	return n, nil
+}
+func (r *memoryNodes) Update(context.Context, storage.ServerNode) error { return nil }
+func (r *memoryNodes) Delete(context.Context, string) error             { return nil }
+func (r *memoryNodes) List(context.Context, string, int) (storage.Page[storage.ServerNode], error) {
+	return storage.Page[storage.ServerNode]{}, nil
+}
+
+type memoryPolicies struct {
+	items   map[string][]storage.AgentPolicy
+	listErr error
+}
+
+func (r *memoryPolicies) Create(context.Context, storage.AgentPolicy) error { return nil }
+func (r *memoryPolicies) Get(context.Context, string) (storage.AgentPolicy, error) {
+	return storage.AgentPolicy{}, sql.ErrNoRows
+}
+func (r *memoryPolicies) Update(context.Context, storage.AgentPolicy) error { return nil }
+func (r *memoryPolicies) Delete(context.Context, string) error              { return nil }
+func (r *memoryPolicies) ListByAgent(_ context.Context, agentID, _ string, _ int) (storage.Page[storage.AgentPolicy], error) {
+	if r.listErr != nil {
+		return storage.Page[storage.AgentPolicy]{}, r.listErr
+	}
+	return storage.Page[storage.AgentPolicy]{Items: append([]storage.AgentPolicy(nil), r.items[agentID]...)}, nil
+}
+
+func ptrTime(v time.Time) *time.Time { return &v }
diff -ruN .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-3-before/internal/auth/password.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-3-after/internal/auth/password.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-3-before/internal/auth/password.go	2026-09-06 08:25:14
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-3-after/internal/auth/password.go	2026-09-06 14:49:45
@@ -90,6 +90,13 @@
 	return fmt.Sprintf("%x", sum[:])
 }
 
+// tokenPrefix derives a short display identifier from the one-way hash. It is
+// safe to expose because it reveals no bytes from the bearer secret itself.
+func tokenPrefix(token string) string {
+	hash := hashToken(token)
+	return hash[:8]
+}
+
 func randomToken(n int) (string, error) {
 	if n < 32 {
 		n = 32
diff -ruN .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-3-before/internal/auth/service.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-3-after/internal/auth/service.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-3-before/internal/auth/service.go	2026-09-06 08:25:14
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-3-after/internal/auth/service.go	2026-09-06 14:49:45
@@ -15,6 +15,8 @@
 	ErrUnauthenticated    = errors.New("unauthenticated")
 	ErrForbidden          = errors.New("forbidden")
 	ErrInvalidRole        = errors.New("invalid role")
+	ErrInvalidTokenType   = errors.New("invalid token type")
+	ErrInvalidTokenScope  = errors.New("invalid token scope")
 )
 
 type Principal struct {
```
