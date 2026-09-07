# Task 3 fix round 1 review package

## Changed files

```text
Files .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-3-after/internal/auth/credential_service.go and .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-3-fix1-after/internal/auth/credential_service.go differ
Files .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-3-after/internal/auth/credential_service_test.go and .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-3-fix1-after/internal/auth/credential_service_test.go differ
```

## Full fix-only diff

```diff
diff -ruN .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-3-after/internal/auth/credential_service.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-3-fix1-after/internal/auth/credential_service.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-3-after/internal/auth/credential_service.go	2026-09-06 14:49:53
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-3-fix1-after/internal/auth/credential_service.go	2026-09-06 15:08:37
@@ -5,6 +5,7 @@
 	"encoding/json"
 	"errors"
 	"fmt"
+	"io"
 	"net"
 	"sort"
 	"strconv"
@@ -19,6 +20,14 @@
 	lastUsedQueueCapacity = 128
 	lastUsedWriteTimeout  = 2 * time.Second
 	policyPageSize        = 100
+
+	maxTokenScopeJSONBytes = 16 * 1024
+	maxScopeAgentIDs       = 128
+	maxScopeAgentIDBytes   = 255
+	maxScopeProtocols      = 4
+	maxScopeCIDRs          = 128
+	maxScopePorts          = 1024
+	maxPolicyTargetBytes   = 255
 )
 
 type TokenScope struct {
@@ -65,6 +74,23 @@
 	when    time.Time
 }
 
+type compiledTokenScope struct {
+	scope     TokenScope
+	agentIDs  map[string]struct{}
+	protocols map[string]struct{}
+	cidrs     []*net.IPNet
+	ports     map[int]struct{}
+}
+
+type portInterval struct {
+	first int
+	last  int
+}
+
+type portMatcher struct {
+	intervals []portInterval
+}
+
 // CredentialService owns service-token lifecycle and stream authorization.
 // Management sessions remain exclusively owned by AuthService/api_tokens.
 type CredentialService struct {
@@ -209,14 +235,17 @@
 	if err != nil || record.Type != storage.TokenTypeClient || record.OwnerUserID != id.OwnerUserID {
 		return ErrForbidden
 	}
-	current, err := s.identityFromRecord(ctx, record)
+	current, compiledScope, err := s.identityAndScopeFromRecord(ctx, record)
 	if err != nil {
 		return ErrForbidden
 	}
+	if len(req.AgentID) > maxScopeAgentIDBytes || len(req.Protocol) > 32 || len(req.TargetHost) > maxPolicyTargetBytes {
+		return ErrForbidden
+	}
 	req.AgentID = strings.TrimSpace(req.AgentID)
 	req.Protocol = normalizeProtocol(req.Protocol)
 	req.TargetHost = strings.TrimSpace(req.TargetHost)
-	if req.AgentID == "" || req.Protocol == "" || req.TargetHost == "" || req.TargetPort < 1 || req.TargetPort > 65535 {
+	if req.AgentID == "" || len(req.AgentID) > maxScopeAgentIDBytes || req.Protocol == "" || req.TargetHost == "" || len(req.TargetHost) > maxPolicyTargetBytes || req.TargetPort < 1 || req.TargetPort > 65535 {
 		return ErrForbidden
 	}
 	agent, err := s.agents.Get(ctx, req.AgentID)
@@ -224,7 +253,7 @@
 		return ErrForbidden
 	}
 	targetIP := net.ParseIP(req.TargetHost)
-	if !scopeAllows(current.Scope, req, targetIP) {
+	if !scopeAllows(compiledScope, req, targetIP) {
 		return ErrForbidden
 	}
 	allowed, err := s.agentPolicyAllows(ctx, req, targetIP)
@@ -345,83 +374,152 @@
 }
 
 func (s *CredentialService) identityFromRecord(ctx context.Context, record storage.ServiceToken) (TokenIdentity, error) {
+	identity, _, err := s.identityAndScopeFromRecord(ctx, record)
+	return identity, err
+}
+
+func (s *CredentialService) identityAndScopeFromRecord(ctx context.Context, record storage.ServiceToken) (TokenIdentity, compiledTokenScope, error) {
 	now := time.Now().UTC()
 	if !validTokenType(record.Type) || record.ID == "" || record.OwnerUserID == "" || record.RevokedAt != nil || (record.ExpiresAt != nil && !record.ExpiresAt.After(now)) {
-		return TokenIdentity{}, ErrUnauthenticated
+		return TokenIdentity{}, compiledTokenScope{}, ErrUnauthenticated
 	}
 	if err := validateTokenBinding(CreateTokenInput{Type: record.Type, OwnerUserID: record.OwnerUserID, AgentID: record.AgentID, NodeID: record.NodeID}); err != nil {
-		return TokenIdentity{}, ErrUnauthenticated
+		return TokenIdentity{}, compiledTokenScope{}, ErrUnauthenticated
 	}
 	if s.users == nil || s.agents == nil || s.nodes == nil {
-		return TokenIdentity{}, ErrUnauthenticated
+		return TokenIdentity{}, compiledTokenScope{}, ErrUnauthenticated
 	}
 	if err := s.validateOwnerAndBinding(ctx, record.Type, record.OwnerUserID, record.AgentID, record.NodeID); err != nil {
-		return TokenIdentity{}, ErrUnauthenticated
+		return TokenIdentity{}, compiledTokenScope{}, ErrUnauthenticated
 	}
-	var scope TokenScope
-	if err := json.Unmarshal([]byte(record.Scope), &scope); err != nil {
-		return TokenIdentity{}, ErrUnauthenticated
+	scope, err := decodeTokenScope(record.Scope)
+	if err != nil {
+		return TokenIdentity{}, compiledTokenScope{}, ErrUnauthenticated
 	}
-	scope, err := normalizeTokenScope(scope)
+	compiled, err := compileTokenScope(scope)
 	if err != nil {
-		return TokenIdentity{}, ErrUnauthenticated
+		return TokenIdentity{}, compiledTokenScope{}, ErrUnauthenticated
 	}
-	return TokenIdentity{TokenID: record.ID, OwnerUserID: record.OwnerUserID, AgentID: record.AgentID, NodeID: record.NodeID, Prefix: record.Prefix, Type: record.Type, Scope: scope}, nil
+	identity := TokenIdentity{TokenID: record.ID, OwnerUserID: record.OwnerUserID, AgentID: record.AgentID, NodeID: record.NodeID, Prefix: record.Prefix, Type: record.Type, Scope: compiled.scope}
+	return identity, compiled, nil
 }
 
 func normalizeTokenScope(scope TokenScope) (TokenScope, error) {
-	var out TokenScope
+	compiled, err := compileTokenScope(scope)
+	return compiled.scope, err
+}
+
+func compileTokenScope(scope TokenScope) (compiledTokenScope, error) {
+	if len(scope.AgentIDs) > maxScopeAgentIDs || len(scope.Protocols) > maxScopeProtocols || len(scope.TargetCIDRs) > maxScopeCIDRs || len(scope.TargetPorts) > maxScopePorts {
+		return compiledTokenScope{}, fmt.Errorf("%w: scope entry limit exceeded", ErrInvalidTokenScope)
+	}
+	stringBytes := 0
+	for _, value := range scope.AgentIDs {
+		if len(value) > maxScopeAgentIDBytes {
+			return compiledTokenScope{}, fmt.Errorf("%w: invalid agent id", ErrInvalidTokenScope)
+		}
+		stringBytes += len(value)
+	}
+	for _, value := range scope.Protocols {
+		if len(value) > maxTokenScopeJSONBytes {
+			return compiledTokenScope{}, ErrInvalidTokenScope
+		}
+		stringBytes += len(value)
+	}
+	for _, value := range scope.TargetCIDRs {
+		if len(value) > maxTokenScopeJSONBytes {
+			return compiledTokenScope{}, ErrInvalidTokenScope
+		}
+		stringBytes += len(value)
+	}
+	if stringBytes > maxTokenScopeJSONBytes {
+		return compiledTokenScope{}, fmt.Errorf("%w: serialized scope exceeds %d bytes", ErrInvalidTokenScope, maxTokenScopeJSONBytes)
+	}
+	out := compiledTokenScope{
+		agentIDs:  make(map[string]struct{}, len(scope.AgentIDs)),
+		protocols: make(map[string]struct{}, len(scope.Protocols)),
+		cidrs:     make([]*net.IPNet, 0, len(scope.TargetCIDRs)),
+		ports:     make(map[int]struct{}, len(scope.TargetPorts)),
+	}
 	seenAgents := make(map[string]struct{})
 	for _, raw := range scope.AgentIDs {
 		agentID := strings.TrimSpace(raw)
-		if agentID == "" {
-			return TokenScope{}, fmt.Errorf("%w: empty agent id", ErrInvalidTokenScope)
+		if agentID == "" || len(agentID) > maxScopeAgentIDBytes {
+			return compiledTokenScope{}, fmt.Errorf("%w: invalid agent id", ErrInvalidTokenScope)
 		}
 		if _, ok := seenAgents[agentID]; !ok {
 			seenAgents[agentID] = struct{}{}
-			out.AgentIDs = append(out.AgentIDs, agentID)
+			out.agentIDs[agentID] = struct{}{}
+			out.scope.AgentIDs = append(out.scope.AgentIDs, agentID)
 		}
 	}
 	seenProtocols := make(map[string]struct{})
 	for _, raw := range scope.Protocols {
 		protocol := normalizeProtocol(raw)
 		if protocol == "" {
-			return TokenScope{}, fmt.Errorf("%w: invalid protocol %q", ErrInvalidTokenScope, raw)
+			return compiledTokenScope{}, fmt.Errorf("%w: invalid protocol %q", ErrInvalidTokenScope, raw)
 		}
 		if _, ok := seenProtocols[protocol]; !ok {
 			seenProtocols[protocol] = struct{}{}
-			out.Protocols = append(out.Protocols, protocol)
+			out.protocols[protocol] = struct{}{}
+			out.scope.Protocols = append(out.scope.Protocols, protocol)
 		}
 	}
 	seenCIDRs := make(map[string]struct{})
 	for _, raw := range scope.TargetCIDRs {
 		_, network, err := net.ParseCIDR(strings.TrimSpace(raw))
 		if err != nil {
-			return TokenScope{}, fmt.Errorf("%w: invalid CIDR %q", ErrInvalidTokenScope, raw)
+			return compiledTokenScope{}, fmt.Errorf("%w: invalid CIDR %q", ErrInvalidTokenScope, raw)
 		}
 		canonical := network.String()
 		if _, ok := seenCIDRs[canonical]; !ok {
 			seenCIDRs[canonical] = struct{}{}
-			out.TargetCIDRs = append(out.TargetCIDRs, canonical)
+			out.cidrs = append(out.cidrs, network)
+			out.scope.TargetCIDRs = append(out.scope.TargetCIDRs, canonical)
 		}
 	}
 	seenPorts := make(map[int]struct{})
 	for _, port := range scope.TargetPorts {
 		if port < 1 || port > 65535 {
-			return TokenScope{}, fmt.Errorf("%w: invalid port %d", ErrInvalidTokenScope, port)
+			return compiledTokenScope{}, fmt.Errorf("%w: invalid port %d", ErrInvalidTokenScope, port)
 		}
 		if _, ok := seenPorts[port]; !ok {
 			seenPorts[port] = struct{}{}
-			out.TargetPorts = append(out.TargetPorts, port)
+			out.ports[port] = struct{}{}
+			out.scope.TargetPorts = append(out.scope.TargetPorts, port)
 		}
 	}
-	sort.Strings(out.AgentIDs)
-	sort.Strings(out.Protocols)
-	sort.Strings(out.TargetCIDRs)
-	sort.Ints(out.TargetPorts)
+	sort.Strings(out.scope.AgentIDs)
+	sort.Strings(out.scope.Protocols)
+	sort.Strings(out.scope.TargetCIDRs)
+	sort.Ints(out.scope.TargetPorts)
+	encoded, err := json.Marshal(out.scope)
+	if err != nil || len(encoded) > maxTokenScopeJSONBytes {
+		return compiledTokenScope{}, fmt.Errorf("%w: serialized scope exceeds %d bytes", ErrInvalidTokenScope, maxTokenScopeJSONBytes)
+	}
 	return out, nil
 }
 
+func decodeTokenScope(raw string) (TokenScope, error) {
+	if len(raw) > maxTokenScopeJSONBytes {
+		return TokenScope{}, ErrInvalidTokenScope
+	}
+	trimmed := strings.TrimSpace(raw)
+	if trimmed == "" || trimmed[0] != '{' {
+		return TokenScope{}, ErrInvalidTokenScope
+	}
+	decoder := json.NewDecoder(strings.NewReader(trimmed))
+	decoder.DisallowUnknownFields()
+	var scope TokenScope
+	if err := decoder.Decode(&scope); err != nil {
+		return TokenScope{}, err
+	}
+	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
+		return TokenScope{}, ErrInvalidTokenScope
+	}
+	return scope, nil
+}
+
 func normalizeProtocol(raw string) string {
 	switch protocol := strings.ToLower(strings.TrimSpace(raw)); protocol {
 	case "tcp", "udp", "http", "ws":
@@ -433,51 +531,55 @@
 	}
 }
 
-func scopeAllows(scope TokenScope, req StreamAuthorizationRequest, targetIP net.IP) bool {
-	if len(scope.AgentIDs) > 0 && !containsString(scope.AgentIDs, req.AgentID) {
-		return false
+func scopeAllows(scope compiledTokenScope, req StreamAuthorizationRequest, targetIP net.IP) bool {
+	if len(scope.agentIDs) > 0 {
+		if _, ok := scope.agentIDs[req.AgentID]; !ok {
+			return false
+		}
 	}
-	if len(scope.Protocols) > 0 && !containsString(scope.Protocols, req.Protocol) {
-		return false
-	}
-	if len(scope.TargetPorts) > 0 && !containsInt(scope.TargetPorts, req.TargetPort) {
-		return false
-	}
-	if len(scope.TargetCIDRs) > 0 {
-		if targetIP == nil {
+	if len(scope.protocols) > 0 {
+		if _, ok := scope.protocols[req.Protocol]; !ok {
 			return false
 		}
-		allowed := false
-		for _, raw := range scope.TargetCIDRs {
-			_, network, err := net.ParseCIDR(raw)
-			if err != nil {
-				return false
-			}
-			if network.Contains(targetIP) {
-				allowed = true
-			}
-		}
-		if !allowed {
+	}
+	if len(scope.ports) > 0 {
+		if _, ok := scope.ports[req.TargetPort]; !ok {
 			return false
 		}
 	}
-	return true
+	if len(scope.cidrs) == 0 {
+		return true
+	}
+	if targetIP == nil {
+		return false
+	}
+	for _, network := range scope.cidrs {
+		if network.Contains(targetIP) {
+			return true
+		}
+	}
+	return false
 }
 
 func (s *CredentialService) agentPolicyAllows(ctx context.Context, req StreamAuthorizationRequest, targetIP net.IP) (bool, error) {
 	cursor := ""
+	matched := false
 	for {
 		page, err := s.policies.ListByAgent(ctx, req.AgentID, cursor, policyPageSize)
 		if err != nil {
 			return false, err
 		}
 		for _, policy := range page.Items {
-			if policyAllows(policy, req, targetIP) {
-				return true, nil
+			allowed, err := policyAllows(policy, req, targetIP)
+			if err != nil {
+				return false, err
 			}
+			if allowed {
+				matched = true
+			}
 		}
 		if !page.HasMore {
-			return false, nil
+			return matched, nil
 		}
 		if page.NextCursor == "" || page.NextCursor == cursor {
 			return false, errors.New("invalid policy pagination")
@@ -486,26 +588,50 @@
 	}
 }
 
-func policyAllows(policy storage.AgentPolicy, req StreamAuthorizationRequest, targetIP net.IP) bool {
-	if policy.AgentID != "" && policy.AgentID != req.AgentID {
-		return false
+func policyAllows(policy storage.AgentPolicy, req StreamAuthorizationRequest, targetIP net.IP) (bool, error) {
+	if len(policy.AgentID) > maxScopeAgentIDBytes {
+		return false, ErrForbidden
 	}
-	if protocol := normalizeProtocol(policy.Protocol); protocol == "" || protocol != req.Protocol {
-		return false
+	agentID := strings.TrimSpace(policy.AgentID)
+	if agentID == "" {
+		return false, ErrForbidden
 	}
-	if host := strings.TrimSpace(policy.TargetHost); host != "" && !strings.EqualFold(host, req.TargetHost) {
-		return false
+	if agentID != req.AgentID {
+		return false, nil
 	}
-	if policy.TargetPort != 0 && policy.TargetPort != req.TargetPort {
-		return false
+	if len(policy.Protocol) > 32 {
+		return false, ErrForbidden
 	}
+	protocol := normalizeProtocol(policy.Protocol)
+	if protocol == "" {
+		return false, ErrForbidden
+	}
+	if protocol != req.Protocol {
+		return false, nil
+	}
+	if len(policy.TargetHost) > maxPolicyTargetBytes {
+		return false, ErrForbidden
+	}
+	host := strings.TrimSpace(policy.TargetHost)
+	if host == "" {
+		return false, ErrForbidden
+	}
+	if !strings.EqualFold(host, req.TargetHost) {
+		return false, nil
+	}
+	if policy.TargetPort < 1 || policy.TargetPort > 65535 {
+		return false, ErrForbidden
+	}
+	if policy.TargetPort != req.TargetPort {
+		return false, nil
+	}
 	cidrs, err := parseCIDRList(policy.AllowedCIDRs)
 	if err != nil {
-		return false
+		return false, err
 	}
 	if len(cidrs) > 0 {
 		if targetIP == nil {
-			return false
+			return false, nil
 		}
 		allowed := false
 		for _, network := range cidrs {
@@ -515,15 +641,18 @@
 			}
 		}
 		if !allowed {
-			return false
+			return false, nil
 		}
 	}
-	ports, err := parsePortList(policy.AllowedPorts)
-	return err == nil && (len(ports) == 0 || containsInt(ports, req.TargetPort))
+	ports, err := parsePortMatcher(policy.AllowedPorts)
+	if err != nil {
+		return false, err
+	}
+	return ports.allows(req.TargetPort), nil
 }
 
 func parseCIDRList(raw string) ([]*net.IPNet, error) {
-	values, err := parseStringList(raw)
+	values, err := parseStringList(raw, maxScopeCIDRs)
 	if err != nil {
 		return nil, err
 	}
@@ -538,7 +667,10 @@
 	return out, nil
 }
 
-func parseStringList(raw string) ([]string, error) {
+func parseStringList(raw string, limit int) ([]string, error) {
+	if len(raw) > maxTokenScopeJSONBytes {
+		return nil, ErrInvalidTokenScope
+	}
 	raw = strings.TrimSpace(raw)
 	if raw == "" || raw == "[]" {
 		return nil, nil
@@ -548,12 +680,18 @@
 		if err := json.Unmarshal([]byte(raw), &values); err != nil {
 			return nil, err
 		}
+		if len(values) > limit {
+			return nil, ErrInvalidTokenScope
+		}
 		for i := range values {
 			values[i] = strings.TrimSpace(values[i])
 		}
 		return values, nil
 	}
 	parts := strings.Split(raw, ",")
+	if len(parts) > limit {
+		return nil, ErrInvalidTokenScope
+	}
 	out := make([]string, 0, len(parts))
 	for _, part := range parts {
 		if part = strings.TrimSpace(part); part != "" {
@@ -563,25 +701,37 @@
 	return out, nil
 }
 
-func parsePortList(raw string) ([]int, error) {
+func parsePortMatcher(raw string) (portMatcher, error) {
+	if len(raw) > maxTokenScopeJSONBytes {
+		return portMatcher{}, ErrInvalidTokenScope
+	}
 	raw = strings.TrimSpace(raw)
 	if raw == "" || raw == "[]" {
-		return nil, nil
+		return portMatcher{}, nil
 	}
 	if strings.HasPrefix(raw, "[") {
 		var ports []int
 		if err := json.Unmarshal([]byte(raw), &ports); err != nil {
-			return nil, err
+			return portMatcher{}, err
 		}
+		if len(ports) > maxScopePorts {
+			return portMatcher{}, ErrInvalidTokenScope
+		}
+		matcher := portMatcher{intervals: make([]portInterval, 0, len(ports))}
 		for _, port := range ports {
 			if port < 1 || port > 65535 {
-				return nil, ErrInvalidTokenScope
+				return portMatcher{}, ErrInvalidTokenScope
 			}
+			matcher.intervals = append(matcher.intervals, portInterval{first: port, last: port})
 		}
-		return ports, nil
+		return matcher, nil
 	}
-	var ports []int
-	for _, part := range strings.Split(raw, ",") {
+	parts := strings.Split(raw, ",")
+	if len(parts) > maxScopePorts {
+		return portMatcher{}, ErrInvalidTokenScope
+	}
+	matcher := portMatcher{intervals: make([]portInterval, 0, len(parts))}
+	for _, part := range parts {
 		part = strings.TrimSpace(part)
 		if part == "" {
 			continue
@@ -591,34 +741,26 @@
 			lo, errLo := strconv.Atoi(strings.TrimSpace(bounds[0]))
 			hi, errHi := strconv.Atoi(strings.TrimSpace(bounds[1]))
 			if errLo != nil || errHi != nil || lo < 1 || hi > 65535 || lo > hi {
-				return nil, ErrInvalidTokenScope
+				return portMatcher{}, ErrInvalidTokenScope
 			}
-			for port := lo; port <= hi; port++ {
-				ports = append(ports, port)
-			}
+			matcher.intervals = append(matcher.intervals, portInterval{first: lo, last: hi})
 			continue
 		}
 		port, err := strconv.Atoi(part)
 		if err != nil || port < 1 || port > 65535 {
-			return nil, ErrInvalidTokenScope
+			return portMatcher{}, ErrInvalidTokenScope
 		}
-		ports = append(ports, port)
+		matcher.intervals = append(matcher.intervals, portInterval{first: port, last: port})
 	}
-	return ports, nil
+	return matcher, nil
 }
 
-func containsString(values []string, want string) bool {
-	for _, value := range values {
-		if value == want {
-			return true
-		}
+func (m portMatcher) allows(port int) bool {
+	if len(m.intervals) == 0 {
+		return true
 	}
-	return false
-}
-
-func containsInt(values []int, want int) bool {
-	for _, value := range values {
-		if value == want {
+	for _, interval := range m.intervals {
+		if port >= interval.first && port <= interval.last {
 			return true
 		}
 	}
diff -ruN .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-3-after/internal/auth/credential_service_test.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-3-fix1-after/internal/auth/credential_service_test.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-3-after/internal/auth/credential_service_test.go	2026-09-06 14:49:53
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-3-fix1-after/internal/auth/credential_service_test.go	2026-09-06 15:09:51
@@ -4,7 +4,10 @@
 	"context"
 	"database/sql"
 	"encoding/base64"
+	"encoding/json"
 	"errors"
+	"fmt"
+	"strings"
 	"sync"
 	"testing"
 	"time"
@@ -304,14 +307,212 @@
 	}
 	repos.policies.listErr = nil
 	revoked := time.Now().UTC()
-	token := repos.tokens.items[id.TokenID]
-	token.RevokedAt = &revoked
-	repos.tokens.items[id.TokenID] = token
+	repos.tokens.mutate(id.TokenID, func(token *storage.ServiceToken) { token.RevokedAt = &revoked })
 	if err := service.AuthorizeStream(ctx, id, req); !errors.Is(err, ErrForbidden) {
 		t.Fatalf("revoked token authorized a later stream: %v", err)
 	}
 }
 
+func TestCredentialPolicyMalformedBindingsFailClosed(t *testing.T) {
+	cases := []struct {
+		name   string
+		policy storage.AgentPolicy
+		req    StreamAuthorizationRequest
+	}{
+		{name: "empty agent", policy: storage.AgentPolicy{TargetHost: "10.0.0.8", TargetPort: 22, Protocol: "tcp"}, req: streamRequest("10.0.0.8", 22)},
+		{name: "wrong agent", policy: storage.AgentPolicy{AgentID: "agent-b", TargetHost: "10.0.0.8", TargetPort: 22, Protocol: "tcp"}, req: streamRequest("10.0.0.8", 22)},
+		{name: "empty target host", policy: storage.AgentPolicy{AgentID: "agent-a", TargetPort: 22, Protocol: "tcp"}, req: streamRequest("10.0.0.8", 22)},
+		{name: "oversized target host", policy: storage.AgentPolicy{AgentID: "agent-a", TargetHost: strings.Repeat("a", 256), TargetPort: 22, Protocol: "tcp"}, req: streamRequest(strings.Repeat("a", 256), 22)},
+		{name: "wrong target host", policy: storage.AgentPolicy{AgentID: "agent-a", TargetHost: "10.0.0.9", TargetPort: 22, Protocol: "tcp"}, req: streamRequest("10.0.0.8", 22)},
+		{name: "zero target port", policy: storage.AgentPolicy{AgentID: "agent-a", TargetHost: "10.0.0.8", Protocol: "tcp"}, req: streamRequest("10.0.0.8", 22)},
+		{name: "oversized target port", policy: storage.AgentPolicy{AgentID: "agent-a", TargetHost: "10.0.0.8", TargetPort: 65536, Protocol: "tcp"}, req: streamRequest("10.0.0.8", 22)},
+		{name: "wrong target port", policy: storage.AgentPolicy{AgentID: "agent-a", TargetHost: "10.0.0.8", TargetPort: 23, Protocol: "tcp"}, req: streamRequest("10.0.0.8", 22)},
+		{name: "empty protocol", policy: storage.AgentPolicy{AgentID: "agent-a", TargetHost: "10.0.0.8", TargetPort: 22}, req: streamRequest("10.0.0.8", 22)},
+		{name: "invalid protocol", policy: storage.AgentPolicy{AgentID: "agent-a", TargetHost: "10.0.0.8", TargetPort: 22, Protocol: "icmp"}, req: streamRequest("10.0.0.8", 22)},
+	}
+	for _, tt := range cases {
+		t.Run(tt.name, func(t *testing.T) {
+			service, repos, id := authorizedCredential(t, TokenScope{})
+			repos.policies.items["agent-a"] = []storage.AgentPolicy{tt.policy}
+			if err := service.AuthorizeStream(context.Background(), id, tt.req); !errors.Is(err, ErrForbidden) {
+				t.Fatalf("AuthorizeStream() error = %v, want ErrForbidden for policy %+v", err, tt.policy)
+			}
+		})
+	}
+
+	t.Run("malformed record after matching record", func(t *testing.T) {
+		service, repos, id := authorizedCredential(t, TokenScope{})
+		repos.policies.items["agent-a"] = []storage.AgentPolicy{
+			{ID: "policy-a", AgentID: "agent-a", TargetHost: "10.0.0.8", TargetPort: 22, Protocol: "tcp"},
+			{ID: "policy-b", AgentID: "agent-a", TargetPort: 22, Protocol: "tcp"},
+		}
+		if err := service.AuthorizeStream(context.Background(), id, streamRequest("10.0.0.8", 22)); !errors.Is(err, ErrForbidden) {
+			t.Fatalf("AuthorizeStream() error = %v, want ErrForbidden when any loaded policy is malformed", err)
+		}
+	})
+}
+
+func TestCredentialScopeCreationLimits(t *testing.T) {
+	tests := []struct {
+		name  string
+		scope func(*credentialRepos) TokenScope
+	}{
+		{name: "too many agent ids", scope: func(repos *credentialRepos) TokenScope {
+			ids := make([]string, 129)
+			for i := range ids {
+				ids[i] = fmt.Sprintf("agent-%03d", i)
+				repos.agents.items[ids[i]] = storage.Agent{ID: ids[i], OwnerUserID: "owner", Enabled: true}
+			}
+			return TokenScope{AgentIDs: ids}
+		}},
+		{name: "oversized agent id", scope: func(repos *credentialRepos) TokenScope {
+			id := strings.Repeat("a", 256)
+			repos.agents.items[id] = storage.Agent{ID: id, OwnerUserID: "owner", Enabled: true}
+			return TokenScope{AgentIDs: []string{id}}
+		}},
+		{name: "too many protocols", scope: func(*credentialRepos) TokenScope {
+			return TokenScope{Protocols: []string{"tcp", "udp", "http", "ws", "tcp"}}
+		}},
+		{name: "too many cidrs", scope: func(*credentialRepos) TokenScope {
+			return TokenScope{TargetCIDRs: repeatedStrings("10.0.0.0/24", 129)}
+		}},
+		{name: "too many ports", scope: func(*credentialRepos) TokenScope {
+			return TokenScope{TargetPorts: repeatedInts(22, 1025)}
+		}},
+		{name: "serialized scope too large", scope: func(repos *credentialRepos) TokenScope {
+			ids := make([]string, 128)
+			for i := range ids {
+				prefix := fmt.Sprintf("agent-%03d-", i)
+				ids[i] = prefix + strings.Repeat("x", 255-len(prefix))
+				repos.agents.items[ids[i]] = storage.Agent{ID: ids[i], OwnerUserID: "owner", Enabled: true}
+			}
+			return TokenScope{AgentIDs: ids}
+		}},
+	}
+	for _, tt := range tests {
+		t.Run(tt.name, func(t *testing.T) {
+			repos := newCredentialRepos()
+			repos.users.items["owner"] = storage.User{ID: "owner"}
+			service := newTestCredentialService(t, repos)
+			if _, err := service.Create(context.Background(), CreateTokenInput{Type: storage.TokenTypeClient, OwnerUserID: "owner", Scope: tt.scope(repos)}); !errors.Is(err, ErrInvalidTokenScope) {
+				t.Fatalf("Create() error = %v, want ErrInvalidTokenScope", err)
+			}
+		})
+	}
+}
+
+func TestCredentialPersistedScopeLimitsFailClosed(t *testing.T) {
+	oversizedJSON := TokenScope{AgentIDs: make([]string, 128)}
+	for i := range oversizedJSON.AgentIDs {
+		prefix := fmt.Sprintf("agent-%03d-", i)
+		oversizedJSON.AgentIDs[i] = prefix + strings.Repeat("x", 255-len(prefix))
+	}
+	encodedOversized, err := json.Marshal(oversizedJSON)
+	if err != nil {
+		t.Fatal(err)
+	}
+	if len(encodedOversized) <= 16*1024 {
+		t.Fatalf("test fixture size = %d, want > 16 KiB", len(encodedOversized))
+	}
+
+	tests := []struct {
+		name  string
+		scope string
+	}{
+		{name: "too many protocols", scope: mustScopeJSON(t, TokenScope{Protocols: []string{"tcp", "udp", "http", "ws", "tcp"}})},
+		{name: "too many cidrs", scope: mustScopeJSON(t, TokenScope{TargetCIDRs: repeatedStrings("10.0.0.0/24", 129)})},
+		{name: "too many ports", scope: mustScopeJSON(t, TokenScope{TargetPorts: repeatedInts(22, 1025)})},
+		{name: "serialized scope too large", scope: string(encodedOversized)},
+	}
+	for _, tt := range tests {
+		t.Run(tt.name, func(t *testing.T) {
+			repos := newCredentialRepos()
+			repos.users.items["owner"] = storage.User{ID: "owner"}
+			raw := "oversized-persisted-scope"
+			repos.tokens.items["token-a"] = storage.ServiceToken{ID: "token-a", Type: storage.TokenTypeClient, OwnerUserID: "owner", Prefix: "prefix", TokenHash: hashToken(raw), Scope: tt.scope}
+			service := newTestCredentialService(t, repos)
+			if _, err := service.ValidateAs(context.Background(), raw, storage.TokenTypeClient); !errors.Is(err, ErrUnauthenticated) {
+				t.Fatalf("ValidateAs() error = %v, want ErrUnauthenticated", err)
+			}
+		})
+	}
+
+	service, repos, id := authorizedCredential(t, TokenScope{})
+	repos.tokens.mutate(id.TokenID, func(token *storage.ServiceToken) {
+		token.Scope = mustScopeJSON(t, TokenScope{TargetPorts: repeatedInts(22, 1025)})
+	})
+	repos.policies.items["agent-a"] = []storage.AgentPolicy{{AgentID: "agent-a", TargetHost: "10.0.0.8", TargetPort: 22, Protocol: "tcp"}}
+	if err := service.AuthorizeStream(context.Background(), id, streamRequest("10.0.0.8", 22)); !errors.Is(err, ErrForbidden) {
+		t.Fatalf("AuthorizeStream() error = %v, want ErrForbidden for oversized persisted scope", err)
+	}
+}
+
+func TestCredentialPolicyListParsingIsBoundedAndSupportsPortRanges(t *testing.T) {
+	service, repos, id := authorizedCredential(t, TokenScope{})
+	req := streamRequest("10.0.0.8", 22)
+
+	repos.policies.items["agent-a"] = []storage.AgentPolicy{{AgentID: "agent-a", TargetHost: "10.0.0.8", TargetPort: 22, Protocol: "tcp", AllowedPorts: "1-65535"}}
+	if err := service.AuthorizeStream(context.Background(), id, req); err != nil {
+		t.Fatalf("AuthorizeStream(valid range) error = %v", err)
+	}
+
+	repos.policies.items["agent-a"] = []storage.AgentPolicy{{AgentID: "agent-a", TargetHost: "10.0.0.8", TargetPort: 22, Protocol: "tcp", AllowedCIDRs: strings.Join(repeatedStrings("10.0.0.0/24", 129), ",")}}
+	if err := service.AuthorizeStream(context.Background(), id, req); !errors.Is(err, ErrForbidden) {
+		t.Fatalf("AuthorizeStream(too many policy CIDRs) error = %v, want ErrForbidden", err)
+	}
+
+	repos.policies.items["agent-a"] = []storage.AgentPolicy{{AgentID: "agent-a", TargetHost: "10.0.0.8", TargetPort: 22, Protocol: "tcp", AllowedPorts: strings.Join(repeatedStrings("22", 1025), ",")}}
+	if err := service.AuthorizeStream(context.Background(), id, req); !errors.Is(err, ErrForbidden) {
+		t.Fatalf("AuthorizeStream(too many policy ports) error = %v, want ErrForbidden", err)
+	}
+}
+
+func authorizedCredential(t *testing.T, scope TokenScope) (*CredentialService, *credentialRepos, TokenIdentity) {
+	t.Helper()
+	repos := newCredentialRepos()
+	repos.users.items["owner"] = storage.User{ID: "owner"}
+	repos.agents.items["agent-a"] = storage.Agent{ID: "agent-a", OwnerUserID: "owner", Enabled: true}
+	service := newTestCredentialService(t, repos)
+	created, err := service.Create(context.Background(), CreateTokenInput{Type: storage.TokenTypeClient, OwnerUserID: "owner", Scope: scope})
+	if err != nil {
+		t.Fatal(err)
+	}
+	id, err := service.ValidateAs(context.Background(), created.Secret, storage.TokenTypeClient)
+	if err != nil {
+		t.Fatal(err)
+	}
+	return service, repos, id
+}
+
+func streamRequest(host string, port int) StreamAuthorizationRequest {
+	return StreamAuthorizationRequest{AgentID: "agent-a", Protocol: "tcp", TargetHost: host, TargetPort: port}
+}
+
+func repeatedStrings(value string, count int) []string {
+	values := make([]string, count)
+	for i := range values {
+		values[i] = value
+	}
+	return values
+}
+
+func repeatedInts(value, count int) []int {
+	values := make([]int, count)
+	for i := range values {
+		values[i] = value
+	}
+	return values
+}
+
+func mustScopeJSON(t *testing.T, scope TokenScope) string {
+	t.Helper()
+	encoded, err := json.Marshal(scope)
+	if err != nil {
+		t.Fatal(err)
+	}
+	return string(encoded)
+}
+
 func newTestCredentialService(t *testing.T, repos *credentialRepos) *CredentialService {
 	t.Helper()
 	service := NewCredentialService(repos.tokens, repos.users, repos.agents, repos.nodes, repos.policies)
@@ -343,6 +544,14 @@
 	getErr       error
 	getByHashErr error
 	touchCh      chan string
+}
+
+func (r *memoryServiceTokens) mutate(id string, mutate func(*storage.ServiceToken)) {
+	r.mu.Lock()
+	defer r.mu.Unlock()
+	token := r.items[id]
+	mutate(&token)
+	r.items[id] = token
 }
 
 func (r *memoryServiceTokens) Create(_ context.Context, token storage.ServiceToken) error {
```
