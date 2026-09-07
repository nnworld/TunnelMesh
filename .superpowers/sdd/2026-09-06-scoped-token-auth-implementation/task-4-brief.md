### Task 4: Expose Token management API and audit events

**Files:**
- Create: `internal/server/token_service.go`
- Create: `internal/server/token_api.go`
- Create: `internal/server/token_api_test.go`
- Modify: `internal/server/api.go`
- Modify: `docs/api/openapi.yaml`

**Interfaces:**
- Produces:

```text
GET  /api/v1/tokens?type=&owner=&cursor=&limit=
POST /api/v1/tokens
GET  /api/v1/tokens/{id}
POST /api/v1/tokens/{id}/rotate
POST /api/v1/tokens/{id}/revoke
```

- [ ] **Step 1: Write failing API tests**

Cover 401, admin create all types, Agent owner create Agent/Client Token only for owned Agents, non-admin Server-node denial, cursor pagination, redacted list/detail, expiry validation, scope validation, revoke/rotate, audit contents, and no hash/secret leakage.

For idempotency assert: first response has `secret`, replay has identical Token ID and metadata, `secret` is absent, `replayed=true`, and `idempotency_keys.response` contains no secret.

- [ ] **Step 2: Verify RED**

```bash
go test ./internal/server -run TokenAPI -count=1
```

- [ ] **Step 3: Implement Handler → Service → Repository flow**

Keep request decoding and owner/admin checks in `token_api.go`; lifecycle orchestration in `token_service.go`; all persistence in repositories. Audit actions are `token.created`, `token.rotated`, `token.revoked`, and `token.authorization_denied`, with details limited to type, prefix, owner/resource IDs, and reason code.

- [ ] **Step 4: Update OpenAPI**

Document request/response schemas, one-time secret behavior, replay behavior, Token types/scopes, pagination, and 400/401/403/404/409 responses.

- [ ] **Step 5: Verify GREEN**

```bash
go test ./internal/server -run TokenAPI -count=1
go test ./internal/server ./internal/auth ./internal/storage -count=1
```

