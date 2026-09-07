package auth

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

func TestCredentialServiceCreatesAndValidatesEachTokenType(t *testing.T) {
	ctx := context.Background()
	repos := newCredentialRepos()
	repos.users.items["owner"] = storage.User{ID: "owner"}
	repos.agents.items["agent-a"] = storage.Agent{ID: "agent-a", OwnerUserID: "owner", Enabled: true}
	repos.nodes.items["node-a"] = storage.ServerNode{ID: "node-a"}
	service := newTestCredentialService(t, repos)

	tests := []struct {
		name string
		in   CreateTokenInput
	}{
		{name: "agent", in: CreateTokenInput{Type: storage.TokenTypeAgent, OwnerUserID: "owner", AgentID: "agent-a"}},
		{name: "client", in: CreateTokenInput{Type: storage.TokenTypeClient, OwnerUserID: "owner"}},
		{name: "server node", in: CreateTokenInput{Type: storage.TokenTypeServerNode, OwnerUserID: "owner", NodeID: "node-a"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			created, err := service.Create(ctx, tt.in)
			if err != nil {
				t.Fatalf("Create() error = %v", err)
			}
			decoded, err := base64.RawURLEncoding.DecodeString(created.Secret)
			if err != nil || len(decoded) != 32 {
				t.Fatalf("secret is not 32 random RawURL bytes: len=%d err=%v", len(decoded), err)
			}
			identity, err := service.ValidateAs(ctx, created.Secret, tt.in.Type)
			if err != nil {
				t.Fatalf("ValidateAs() error = %v", err)
			}
			if identity.TokenID != created.TokenID || identity.Type != tt.in.Type || identity.OwnerUserID != "owner" {
				t.Fatalf("identity = %+v, created = %+v", identity, created)
			}
			record, err := repos.tokens.Get(ctx, created.TokenID)
			if err != nil {
				t.Fatal(err)
			}
			if record.TokenHash == "" || record.TokenHash == created.Secret || record.Scope == created.Secret || record.Prefix == created.Secret {
				t.Fatalf("repository record leaked raw secret: %+v", record)
			}
			if record.TokenHash != hashToken(created.Secret) {
				t.Fatalf("stored hash = %q, want SHA-256 of secret", record.TokenHash)
			}
		})
	}
}

func TestCredentialServiceRejectsInvalidLifecycleStateAndRepositoryErrors(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	tests := []struct {
		name   string
		mutate func(*credentialRepos, *storage.ServiceToken)
	}{
		{name: "revoked", mutate: func(_ *credentialRepos, token *storage.ServiceToken) { token.RevokedAt = &now }},
		{name: "expired", mutate: func(_ *credentialRepos, token *storage.ServiceToken) {
			expired := now.Add(-time.Second)
			token.ExpiresAt = &expired
		}},
		{name: "disabled owner", mutate: func(r *credentialRepos, _ *storage.ServiceToken) {
			u := r.users.items["owner"]
			u.Disabled = true
			r.users.items["owner"] = u
		}},
		{name: "missing owner", mutate: func(r *credentialRepos, _ *storage.ServiceToken) { delete(r.users.items, "owner") }},
		{name: "disabled agent", mutate: func(r *credentialRepos, _ *storage.ServiceToken) {
			a := r.agents.items["agent-a"]
			a.Enabled = false
			r.agents.items["agent-a"] = a
		}},
		{name: "missing agent", mutate: func(r *credentialRepos, _ *storage.ServiceToken) { delete(r.agents.items, "agent-a") }},
		{name: "wrong owner binding", mutate: func(r *credentialRepos, _ *storage.ServiceToken) {
			a := r.agents.items["agent-a"]
			a.OwnerUserID = "other"
			r.agents.items["agent-a"] = a
		}},
		{name: "token repository error", mutate: func(r *credentialRepos, _ *storage.ServiceToken) {
			r.tokens.getByHashErr = errors.New("database unavailable")
		}},
		{name: "owner repository error", mutate: func(r *credentialRepos, _ *storage.ServiceToken) { r.users.getErr = errors.New("database unavailable") }},
		{name: "agent repository error", mutate: func(r *credentialRepos, _ *storage.ServiceToken) {
			r.agents.getErr = errors.New("database unavailable")
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repos := newCredentialRepos()
			repos.users.items["owner"] = storage.User{ID: "owner"}
			repos.agents.items["agent-a"] = storage.Agent{ID: "agent-a", OwnerUserID: "owner", Enabled: true}
			raw := "agent-secret"
			token := storage.ServiceToken{ID: "token-a", Type: storage.TokenTypeAgent, OwnerUserID: "owner", AgentID: "agent-a", TokenHash: hashToken(raw), Scope: `{}`}
			tt.mutate(repos, &token)
			repos.tokens.items[token.ID] = token
			service := newTestCredentialService(t, repos)
			if _, err := service.ValidateAs(ctx, raw, storage.TokenTypeAgent); !errors.Is(err, ErrUnauthenticated) {
				t.Fatalf("ValidateAs() error = %v, want ErrUnauthenticated", err)
			}
		})
	}
}

func TestCredentialServiceRejectsTypeCrossoverAndInvalidBindings(t *testing.T) {
	ctx := context.Background()
	repos := newCredentialRepos()
	repos.users.items["owner"] = storage.User{ID: "owner"}
	repos.agents.items["agent-a"] = storage.Agent{ID: "agent-a", OwnerUserID: "owner", Enabled: true}
	service := newTestCredentialService(t, repos)
	created, err := service.Create(ctx, CreateTokenInput{Type: storage.TokenTypeAgent, OwnerUserID: "owner", AgentID: "agent-a"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ValidateAs(ctx, created.Secret, storage.TokenTypeClient); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("ValidateAs(type crossover) error = %v, want ErrUnauthenticated", err)
	}

	invalid := []CreateTokenInput{
		{Type: "unknown", OwnerUserID: "owner"},
		{Type: storage.TokenTypeClient},
		{Type: storage.TokenTypeClient, OwnerUserID: "owner", AgentID: "agent-a"},
		{Type: storage.TokenTypeAgent, OwnerUserID: "owner"},
		{Type: storage.TokenTypeAgent, OwnerUserID: "owner", AgentID: "missing"},
		{Type: storage.TokenTypeServerNode, OwnerUserID: "owner"},
	}
	for i, in := range invalid {
		if _, err := service.Create(ctx, in); err == nil {
			t.Fatalf("Create(invalid[%d]=%+v) succeeded", i, in)
		}
	}
}

func TestCredentialServiceAcceptsEmptyScopeAndRejectsInvalidScope(t *testing.T) {
	ctx := context.Background()
	repos := newCredentialRepos()
	repos.users.items["owner"] = storage.User{ID: "owner"}
	service := newTestCredentialService(t, repos)
	if _, err := service.Create(ctx, CreateTokenInput{Type: storage.TokenTypeClient, OwnerUserID: "owner", Scope: TokenScope{}}); err != nil {
		t.Fatalf("Create(empty scope) error = %v", err)
	}

	invalidScopes := []TokenScope{
		{AgentIDs: []string{""}},
		{Protocols: []string{"icmp"}},
		{TargetCIDRs: []string{"not-a-cidr"}},
		{TargetPorts: []int{0}},
		{TargetPorts: []int{65536}},
	}
	for _, scope := range invalidScopes {
		if _, err := service.Create(ctx, CreateTokenInput{Type: storage.TokenTypeClient, OwnerUserID: "owner", Scope: scope}); err == nil {
			t.Fatalf("Create(invalid scope %+v) succeeded", scope)
		}
	}
}

func TestCredentialServiceRotateRevokeAndTouchLastUsed(t *testing.T) {
	ctx := context.Background()
	repos := newCredentialRepos()
	repos.users.items["owner"] = storage.User{ID: "owner"}
	service := newTestCredentialService(t, repos)
	created, err := service.Create(ctx, CreateTokenInput{Type: storage.TokenTypeClient, OwnerUserID: "owner", Scope: TokenScope{Protocols: []string{"TCP"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ValidateAs(ctx, created.Secret, storage.TokenTypeClient); err != nil {
		t.Fatal(err)
	}
	select {
	case touched := <-repos.tokens.touchCh:
		if touched != created.TokenID {
			t.Fatalf("touched token = %q, want %q", touched, created.TokenID)
		}
	case <-time.After(time.Second):
		t.Fatal("last-used worker did not update token")
	}

	rotated, err := service.Rotate(ctx, created.TokenID)
	if err != nil {
		t.Fatalf("Rotate() error = %v", err)
	}
	if rotated.TokenID == created.TokenID || rotated.Secret == created.Secret || rotated.Scope.Protocols[0] != "tcp" {
		t.Fatalf("rotated token did not preserve metadata with a new secret: %+v", rotated)
	}
	if _, err := service.ValidateAs(ctx, created.Secret, storage.TokenTypeClient); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("old secret remains valid: %v", err)
	}
	if _, err := service.ValidateAs(ctx, rotated.Secret, storage.TokenTypeClient); err != nil {
		t.Fatalf("new secret rejected: %v", err)
	}
	if err := service.Revoke(ctx, rotated.TokenID); err != nil {
		t.Fatalf("Revoke() error = %v", err)
	}
	if _, err := service.ValidateAs(ctx, rotated.Secret, storage.TokenTypeClient); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("revoked secret remains valid: %v", err)
	}
}

func TestCredentialServiceServerNodeStateFailsClosed(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	for _, tt := range []struct {
		name   string
		node   storage.ServerNode
		getErr error
	}{
		{name: "missing"},
		{name: "expired", node: storage.ServerNode{ID: "node-a", ExpiresAt: ptrTime(now.Add(-time.Second))}},
		{name: "repository error", node: storage.ServerNode{ID: "node-a"}, getErr: errors.New("database unavailable")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			repos := newCredentialRepos()
			repos.users.items["owner"] = storage.User{ID: "owner"}
			if tt.node.ID != "" {
				repos.nodes.items[tt.node.ID] = tt.node
			}
			repos.nodes.getErr = tt.getErr
			raw := "node-secret"
			repos.tokens.items["node-token"] = storage.ServiceToken{ID: "node-token", Type: storage.TokenTypeServerNode, OwnerUserID: "owner", NodeID: "node-a", TokenHash: hashToken(raw), Scope: `{}`}
			service := newTestCredentialService(t, repos)
			if _, err := service.ValidateAs(ctx, raw, storage.TokenTypeServerNode); !errors.Is(err, ErrUnauthenticated) {
				t.Fatalf("ValidateAs() error = %v, want ErrUnauthenticated", err)
			}
		})
	}
}

func TestScopeAuthorizationIntersectsTokenAndAgentPolicy(t *testing.T) {
	ctx := context.Background()
	repos := newCredentialRepos()
	repos.users.items["owner"] = storage.User{ID: "owner"}
	repos.agents.items["agent-a"] = storage.Agent{ID: "agent-a", OwnerUserID: "owner", Enabled: true}
	repos.agents.items["agent-b"] = storage.Agent{ID: "agent-b", OwnerUserID: "owner", Enabled: true}
	repos.policies.items["agent-a"] = []storage.AgentPolicy{{ID: "allow-ssh", AgentID: "agent-a", TargetHost: "10.0.0.8", TargetPort: 22, Protocol: "tcp", AllowedCIDRs: "10.0.0.0/24", AllowedPorts: "22"}}
	service := newTestCredentialService(t, repos)
	created, err := service.Create(ctx, CreateTokenInput{Type: storage.TokenTypeClient, OwnerUserID: "owner", Scope: TokenScope{AgentIDs: []string{"agent-a"}, Protocols: []string{"TCP"}, TargetCIDRs: []string{"10.0.0.0/24"}, TargetPorts: []int{22}}})
	if err != nil {
		t.Fatal(err)
	}
	id, err := service.ValidateAs(ctx, created.Secret, storage.TokenTypeClient)
	if err != nil {
		t.Fatal(err)
	}
	allowed := StreamAuthorizationRequest{AgentID: "agent-a", Protocol: "tcp", TargetHost: "10.0.0.8", TargetPort: 22}
	if err := service.AuthorizeStream(ctx, id, allowed); err != nil {
		t.Fatalf("AuthorizeStream(allowed) error = %v", err)
	}

	denied := []StreamAuthorizationRequest{
		{AgentID: "agent-b", Protocol: "tcp", TargetHost: "10.0.0.8", TargetPort: 22},
		{AgentID: "agent-a", Protocol: "udp", TargetHost: "10.0.0.8", TargetPort: 22},
		{AgentID: "agent-a", Protocol: "tcp", TargetHost: "10.0.1.8", TargetPort: 22},
		{AgentID: "agent-a", Protocol: "tcp", TargetHost: "10.0.0.8", TargetPort: 23},
	}
	for _, req := range denied {
		if err := service.AuthorizeStream(ctx, id, req); !errors.Is(err, ErrForbidden) {
			t.Fatalf("AuthorizeStream(%+v) error = %v, want ErrForbidden", req, err)
		}
	}

	// A broader token scope cannot override the database-backed Agent Policy.
	id.Scope = TokenScope{AgentIDs: []string{"agent-a"}, Protocols: []string{"tcp"}, TargetCIDRs: []string{"10.0.0.0/8"}, TargetPorts: []int{22, 23}}
	if err := service.AuthorizeStream(ctx, id, StreamAuthorizationRequest{AgentID: "agent-a", Protocol: "tcp", TargetHost: "10.0.0.9", TargetPort: 22}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("token widened Agent Policy: %v", err)
	}
}

func TestScopeAuthorizationRechecksStateAndFailsClosed(t *testing.T) {
	ctx := context.Background()
	repos := newCredentialRepos()
	repos.users.items["owner"] = storage.User{ID: "owner"}
	repos.agents.items["agent-a"] = storage.Agent{ID: "agent-a", OwnerUserID: "owner", Enabled: true}
	repos.policies.items["agent-a"] = []storage.AgentPolicy{{AgentID: "agent-a", TargetHost: "10.0.0.8", TargetPort: 22, Protocol: "tcp"}}
	service := newTestCredentialService(t, repos)
	created, err := service.Create(ctx, CreateTokenInput{Type: storage.TokenTypeClient, OwnerUserID: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	id, err := service.ValidateAs(ctx, created.Secret, storage.TokenTypeClient)
	if err != nil {
		t.Fatal(err)
	}
	req := StreamAuthorizationRequest{AgentID: "agent-a", Protocol: "tcp", TargetHost: "10.0.0.8", TargetPort: 22}

	repos.tokens.getErr = errors.New("database unavailable")
	if err := service.AuthorizeStream(ctx, id, req); !errors.Is(err, ErrForbidden) {
		t.Fatalf("token repository error authorized stream: %v", err)
	}
	repos.tokens.getErr = nil
	repos.policies.listErr = errors.New("database unavailable")
	if err := service.AuthorizeStream(ctx, id, req); !errors.Is(err, ErrForbidden) {
		t.Fatalf("policy repository error authorized stream: %v", err)
	}
	repos.policies.listErr = nil
	revoked := time.Now().UTC()
	token := repos.tokens.items[id.TokenID]
	token.RevokedAt = &revoked
	repos.tokens.items[id.TokenID] = token
	if err := service.AuthorizeStream(ctx, id, req); !errors.Is(err, ErrForbidden) {
		t.Fatalf("revoked token authorized a later stream: %v", err)
	}
}

func newTestCredentialService(t *testing.T, repos *credentialRepos) *CredentialService {
	t.Helper()
	service := NewCredentialService(repos.tokens, repos.users, repos.agents, repos.nodes, repos.policies)
	t.Cleanup(func() { _ = service.Close() })
	return service
}

type credentialRepos struct {
	tokens   *memoryServiceTokens
	users    *memoryUsers
	agents   *memoryAgents
	nodes    *memoryNodes
	policies *memoryPolicies
}

func newCredentialRepos() *credentialRepos {
	return &credentialRepos{
		tokens:   &memoryServiceTokens{items: make(map[string]storage.ServiceToken), touchCh: make(chan string, 16)},
		users:    &memoryUsers{items: make(map[string]storage.User)},
		agents:   &memoryAgents{items: make(map[string]storage.Agent)},
		nodes:    &memoryNodes{items: make(map[string]storage.ServerNode)},
		policies: &memoryPolicies{items: make(map[string][]storage.AgentPolicy)},
	}
}

type memoryServiceTokens struct {
	mu           sync.Mutex
	items        map[string]storage.ServiceToken
	getErr       error
	getByHashErr error
	touchCh      chan string
}

func (r *memoryServiceTokens) Create(_ context.Context, token storage.ServiceToken) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if token.ID == "" {
		return errors.New("token id is required")
	}
	for _, existing := range r.items {
		if existing.TokenHash == token.TokenHash {
			return errors.New("duplicate token hash")
		}
	}
	r.items[token.ID] = token
	return nil
}
func (r *memoryServiceTokens) Get(_ context.Context, id string) (storage.ServiceToken, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.getErr != nil {
		return storage.ServiceToken{}, r.getErr
	}
	token, ok := r.items[id]
	if !ok {
		return storage.ServiceToken{}, sql.ErrNoRows
	}
	return token, nil
}
func (r *memoryServiceTokens) GetByHash(_ context.Context, hash string) (storage.ServiceToken, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.getByHashErr != nil {
		return storage.ServiceToken{}, r.getByHashErr
	}
	for _, token := range r.items {
		if token.TokenHash == hash {
			return token, nil
		}
	}
	return storage.ServiceToken{}, sql.ErrNoRows
}
func (r *memoryServiceTokens) List(context.Context, storage.ServiceTokenFilter, string, int) (storage.Page[storage.ServiceToken], error) {
	return storage.Page[storage.ServiceToken]{}, nil
}
func (r *memoryServiceTokens) Revoke(_ context.Context, id string, when time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	token, ok := r.items[id]
	if !ok {
		return sql.ErrNoRows
	}
	token.RevokedAt = &when
	r.items[id] = token
	return nil
}
func (r *memoryServiceTokens) TouchLastUsed(_ context.Context, id string, when time.Time) error {
	r.mu.Lock()
	token, ok := r.items[id]
	if ok {
		token.LastUsedAt = &when
		r.items[id] = token
	}
	r.mu.Unlock()
	if !ok {
		return sql.ErrNoRows
	}
	select {
	case r.touchCh <- id:
	default:
	}
	return nil
}
func (r *memoryServiceTokens) Rotate(_ context.Context, oldID string, replacement storage.ServiceToken, when time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	old, ok := r.items[oldID]
	if !ok {
		return sql.ErrNoRows
	}
	if old.RevokedAt != nil {
		return storage.ErrServiceTokenRevoked
	}
	old.RevokedAt = &when
	r.items[oldID] = old
	r.items[replacement.ID] = replacement
	return nil
}

type memoryUsers struct {
	items  map[string]storage.User
	getErr error
}

func (r *memoryUsers) Create(context.Context, storage.User) error { return nil }
func (r *memoryUsers) Get(_ context.Context, id string) (storage.User, error) {
	if r.getErr != nil {
		return storage.User{}, r.getErr
	}
	u, ok := r.items[id]
	if !ok {
		return storage.User{}, sql.ErrNoRows
	}
	return u, nil
}
func (r *memoryUsers) GetByUsername(context.Context, string) (storage.User, error) {
	return storage.User{}, sql.ErrNoRows
}
func (r *memoryUsers) Update(context.Context, storage.User) error { return nil }
func (r *memoryUsers) Delete(context.Context, string) error       { return nil }
func (r *memoryUsers) List(context.Context, string, int) (storage.Page[storage.User], error) {
	return storage.Page[storage.User]{}, nil
}

type memoryAgents struct {
	items  map[string]storage.Agent
	getErr error
}

func (r *memoryAgents) Create(context.Context, storage.Agent) error { return nil }
func (r *memoryAgents) Get(_ context.Context, id string) (storage.Agent, error) {
	if r.getErr != nil {
		return storage.Agent{}, r.getErr
	}
	a, ok := r.items[id]
	if !ok {
		return storage.Agent{}, sql.ErrNoRows
	}
	return a, nil
}
func (r *memoryAgents) Update(context.Context, storage.Agent) error { return nil }
func (r *memoryAgents) Delete(context.Context, string) error        { return nil }
func (r *memoryAgents) List(context.Context, string, int) (storage.Page[storage.Agent], error) {
	return storage.Page[storage.Agent]{}, nil
}

type memoryNodes struct {
	items  map[string]storage.ServerNode
	getErr error
}

func (r *memoryNodes) Create(context.Context, storage.ServerNode) error { return nil }
func (r *memoryNodes) Get(_ context.Context, id string) (storage.ServerNode, error) {
	if r.getErr != nil {
		return storage.ServerNode{}, r.getErr
	}
	n, ok := r.items[id]
	if !ok {
		return storage.ServerNode{}, sql.ErrNoRows
	}
	return n, nil
}
func (r *memoryNodes) Update(context.Context, storage.ServerNode) error { return nil }
func (r *memoryNodes) Delete(context.Context, string) error             { return nil }
func (r *memoryNodes) List(context.Context, string, int) (storage.Page[storage.ServerNode], error) {
	return storage.Page[storage.ServerNode]{}, nil
}

type memoryPolicies struct {
	items   map[string][]storage.AgentPolicy
	listErr error
}

func (r *memoryPolicies) Create(context.Context, storage.AgentPolicy) error { return nil }
func (r *memoryPolicies) Get(context.Context, string) (storage.AgentPolicy, error) {
	return storage.AgentPolicy{}, sql.ErrNoRows
}
func (r *memoryPolicies) Update(context.Context, storage.AgentPolicy) error { return nil }
func (r *memoryPolicies) Delete(context.Context, string) error              { return nil }
func (r *memoryPolicies) ListByAgent(_ context.Context, agentID, _ string, _ int) (storage.Page[storage.AgentPolicy], error) {
	if r.listErr != nil {
		return storage.Page[storage.AgentPolicy]{}, r.listErr
	}
	return storage.Page[storage.AgentPolicy]{Items: append([]storage.AgentPolicy(nil), r.items[agentID]...)}, nil
}

func ptrTime(v time.Time) *time.Time { return &v }
