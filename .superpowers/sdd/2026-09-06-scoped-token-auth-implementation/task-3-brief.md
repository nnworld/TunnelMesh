### Task 3: Add CredentialService lifecycle and scope authorization

**Files:**
- Create: `internal/auth/credential_service.go`
- Create: `internal/auth/credential_service_test.go`
- Modify: `internal/auth/service.go`
- Modify: `internal/auth/password.go`

**Interfaces:**
- Produces:

```go
type TokenScope struct {
    AgentIDs []string `json:"agentIds,omitempty"`
    Protocols []string `json:"protocols,omitempty"`
    TargetCIDRs []string `json:"targetCIDRs,omitempty"`
    TargetPorts []int `json:"targetPorts,omitempty"`
}
type TokenIdentity struct {
    TokenID, OwnerUserID, AgentID, NodeID, Prefix string
    Type storage.TokenType
    Scope TokenScope
}
func (s *CredentialService) Create(ctx context.Context, in CreateTokenInput) (CreatedToken, error)
func (s *CredentialService) ValidateAs(ctx context.Context, raw string, expected storage.TokenType) (TokenIdentity, error)
func (s *CredentialService) AuthorizeStream(ctx context.Context, id TokenIdentity, req StreamAuthorizationRequest) error
func (s *CredentialService) Rotate(ctx context.Context, id string) (CreatedToken, error)
func (s *CredentialService) Revoke(ctx context.Context, id string) error
```

- [ ] **Step 1: Write failing lifecycle tests**

Test valid types, invalid type crossover, owner/resource binding, empty/invalid scopes, expiration, revoke, rotate, disabled owner, disabled Agent, last-used updates, and that repository records never contain the returned raw secret.

- [ ] **Step 2: Write failing scope-intersection tests**

Use a client Token allowing `agent-a`, `tcp`, `10.0.0.0/24`, and port 22. Assert it accepts `10.0.0.8:22`, rejects another Agent/protocol/CIDR/port, and remains rejected when Token scope allows a target that Agent Policy denies.

- [ ] **Step 3: Verify RED**

```bash
go test ./internal/auth -run 'Credential|Scope' -count=1
```

- [ ] **Step 4: Implement lifecycle and authorization**

Generate 32 random bytes, encode URL-safe, hash with the existing token hash function, and expose only a short non-secret prefix. Validate scope at creation, normalize protocol names, parse CIDRs once per authorization request, and update `last_used_at` asynchronously through a bounded worker so authentication does not block on a best-effort timestamp write.

Keep `AuthService.ValidateToken` restricted to `api_tokens`; do not make management API accept a service Token.

- [ ] **Step 5: Verify GREEN and race safety**

```bash
go test ./internal/auth -run 'Credential|Scope' -count=1
go test -race ./internal/auth
```

