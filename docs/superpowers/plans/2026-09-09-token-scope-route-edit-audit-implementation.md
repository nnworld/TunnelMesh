# Token Scope and Managed Route Editing Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Enable authorized administrators and Token owners to update service-token authorization scope, edit managed routes, and read actionable audit logs.

**Architecture:** Token changes stay in the existing Handler → TokenService → ServiceTokenRepository flow and commit the scope update and audit event in one transaction. Route updates stay in the API service boundary, recheck target-Agent ownership, enforce route uniqueness, and emit a non-secret audit record. The audit API introduces a public camelCase view so the Vue management UI is decoupled from storage field tags.

**Tech Stack:** Go `net/http`, SQL repositories, Vue 3, TypeScript, Element Plus, Vitest, OpenAPI 3.

**Spec:** `docs/superpowers/specs/2026-09-09-token-scope-route-edit-audit-design.md`

## Global Constraints

- API prefix is `/api/v1`; responses use `{ code, msg, data }`.
- No database schema change is allowed for this feature.
- Token plaintext and hash are never returned by list, detail, update, or audit APIs.
- Token type, owner, and Agent/Node binding remain immutable.
- Route domain must pass the existing exact-domain or single-level `tm-*` validation.
- Authorization is server-side; UI visibility is never the security boundary.
- Web UI uses Element Plus and the existing light SaaS style.
- Route cache continues to use the existing 5-second TTL.
- Full verification is required before PR.

---

### Task 1: Service-token scope persistence and transaction

**Files:**

- Modify: `internal/storage/repository.go`
- Modify: `internal/storage/service_token_test.go`
- Modify: `internal/server/token_service.go`
- Modify: `internal/server/token_api.go`

**Interfaces:**

- Consumes: `ServiceTokenRepository.Get`, `ServiceTokenMutationTransaction`, and existing scope validation.
- Produces: `ServiceTokenRepository.UpdateScope(ctx context.Context, id string, scope string, when time.Time) error`.
- Produces: `TokenService.Update(ctx context.Context, actorUserID string, id string, update tokenUpdate) (TokenView, error)`.
- Produces: `tokenScopePatch` with optional `protocols`, `targetCIDRs`, and `targetPorts`.

- [x] **Step 1: Write the failing storage and service tests.**

  Extend `TestServiceTokenRepositoryContractSQLite` and add `TestTokenAPIUpdatesScopeForActiveTokensOnly`. The repository contract must assert:

  ```go
  err := repo.UpdateScope(ctx, active.ID, scopeJSON, when)
  // err == nil; re-read returns the exact scope and updated timestamp
  err = repo.UpdateScope(ctx, revoked.ID, scopeJSON, when)
  // errors.Is(err, storage.ErrServiceTokenRevoked) == true
  ```

  API assertions must cover omitted-field preservation, empty-array unrestricted semantics, invalid scope `400`, foreign owner `403`, expired/revoked `409`, and two `token.scope_updated` audit events.

- [x] **Step 2: Run the red-phase checks.**

  ```bash
  go test ./internal/storage -run TestServiceTokenRepositoryContractSQLite -count=1
  go test ./internal/server -run TestTokenAPIUpdatesScopeForActiveTokensOnly -count=1
  ```

  Expected result before implementation: compile failure because `UpdateScope` and the scope update request do not exist.

- [x] **Step 3: Implement the repository state transition.**

  Add `UpdateScope` to the service-token repository interface and implementation. The update must use the same active-token guard as expiration updates, replace only `scope` and `updated_at`, and map revoked/expired state to the existing repository errors.

- [x] **Step 4: Implement service merge and audit transaction.**

  In `TokenService.Update`, merge the optional patch over the current scope, validate it, marshal to JSON, update the scope, then write `token.scope_updated` inside the existing mutation transaction. Keep the active-token check before any write.

- [x] **Step 5: Decode the partial API request.**

  Use pointer fields in `tokenScopePatch` so omitted and empty array are distinguishable. Require either `expiresAt` or `scope`; reject unknown updates with `400`. Keep authorization in the existing token API boundary.

- [x] **Step 6: Run focused checks.**

  ```bash
  go test ./internal/storage -run TestServiceTokenRepositoryContractSQLite -count=1
  go test ./internal/server -run TestTokenAPIUpdatesScopeForActiveTokensOnly -count=1
  ```

  Expected result: both focused tests pass.

### Task 2: Token scope management UI

**Files:**

- Modify: `web/src/api/client.ts`
- Modify: `web/src/views/Tokens.vue`
- Modify: `web/src/views/token-form.ts`
- Modify: `web/src/i18n/messages/zh-CN.ts`
- Modify: `web/src/i18n/messages/en-US.ts`
- Test: `web/src/tests/agent-token-workflows.spec.ts`

**Interfaces:**

- Consumes: `PATCH /api/v1/tokens/{tokenId}` and `ServiceToken.scope`.
- Produces: `updateTokenScope(id: string, scope: TokenScopePatch)`.
- Produces: `tokenScopePatchFromForm(input: TokenScopePatchInput): TokenScopePatch`.

- [x] **Step 1: Write failing frontend workflow tests.**

  Assert that `updateTokenScope` sends:

  ```ts
  {
    scope: {
      protocols: ['tcp', 'http'],
      targetCIDRs: ['10.0.0.0/8'],
      targetPorts: [22, 80],
    },
  }
  ```

  Also assert invalid port text throws, the Tokens view exposes `openScope` and `scopeVisible`, and only active tokens show the action.

- [x] **Step 2: Run the red-phase check.**

  ```bash
  npm --prefix web test -- --run src/tests/agent-token-workflows.spec.ts
  ```

  Expected result before implementation: the API helper and view workflow are absent.

- [x] **Step 3: Implement the typed client and form parser.**

  Add `TokenScopePatch` and `updateTokenScope`. Parse comma-separated CIDRs and ports, trim values, reject non-numeric and out-of-range ports, and preserve empty arrays so they explicitly remove restrictions.

- [x] **Step 4: Implement the edit dialog.**

  Add a “修改范围 / Update scope” action for active tokens. Pre-fill protocols, CIDRs, and ports from the current token; show help text explaining that omitted fields are unchanged by the API and empty form fields mean unrestricted. Refresh the row from the response and show localized success/error feedback.

- [x] **Step 5: Run focused frontend checks.**

  ```bash
  npm --prefix web test -- --run src/tests/agent-token-workflows.spec.ts
  ```

  Expected result: the workflow test passes.

### Task 3: Managed-route partial update API

**Files:**

- Modify: `internal/server/api.go`
- Test: `internal/server/api_test.go`
- Modify: `docs/api/openapi.yaml`
- Modify: `docs/user-guide/managed-http-route.md`

**Interfaces:**

- Consumes: `apiService.GetAgent`, `apiService.UpdateTunnel`, and `routing.ValidateDomainPattern`.
- Produces: `PATCH /api/v1/routes/{routeId}` with `TunnelUpdateRequest`.
- Produces: `tunnelUpdateRequest` with pointer fields for every optional route field.

- [x] **Step 1: Write the failing API test.**

  Add `TestAPIRouteUpdateAndAuditView`. Create a route, then patch:

  ```json
  {
    "agentId": "agent-two",
    "domain": "after.example.com",
    "pathPrefix": "/git",
    "protocol": "websocket",
    "targetHost": "127.0.0.1",
    "targetPort": 3001,
    "status": "disabled"
  }
  ```

  Assert the response reflects every field, `targetPort: 0` returns `400`, and a foreign target Agent returns `403`.

- [x] **Step 2: Run the red-phase check.**

  ```bash
  go test ./internal/server -run TestAPIRouteUpdateAndAuditView -count=1
  ```

  Expected result before implementation: PATCH only changes the limited legacy fields and cannot update Agent, protocol, target, or port.

- [x] **Step 3: Implement pointer-based partial merge.**

  Decode `tunnelUpdateRequest` with pointers. For `PATCH`, apply only supplied fields; for `PUT`, continue requiring `agentId`, `targetHost`, and `targetPort`. Normalize `ws` to `websocket`, validate protocol/status/domain/ports, and preserve `config` as validated JSON.

- [x] **Step 4: Recheck ownership and uniqueness.**

  When `agentId` changes, load the target Agent and require admin or matching owner. Serialize route mutations with the existing route mutex and reject another route with the same domain and path using `409`.

- [x] **Step 5: Write route audit and public response.**

  On successful update, call `auditRoute(..., "route.updated", next)` and return `publicTunnel(next)`. Audit details contain Agent, domain, path, target host, port, and status only.

- [x] **Step 6: Update API and user documentation.**

  Document `PATCH`, request fields, error responses, ownership behavior, 5-second cache effect, and the non-secret audit event.

- [x] **Step 7: Run focused checks.**

  ```bash
  go test ./internal/server -run TestAPIRouteUpdateAndAuditView -count=1
  ```

  Expected result: the route update test passes.

### Task 4: Audit-log public contract and UI

**Files:**

- Modify: `internal/server/api.go`
- Test: `internal/server/api_test.go`
- Modify: `web/src/api/client.ts`
- Modify: `web/src/views/AuditLogs.vue`
- Modify: `web/src/i18n/messages/zh-CN.ts`
- Modify: `web/src/i18n/messages/en-US.ts`
- Test: `web/src/tests/routes.spec.ts`

**Interfaces:**

- Consumes: `AuditRepository.List`.
- Produces: `AuditLog` public view with `id`, `actorUserId`, `action`, `resourceType`, `resourceId`, `details`, and `createdAt`.
- Produces: `listAuditLogs(params: { cursor?: string; limit?: number })`.

- [x] **Step 1: Write failing API and frontend tests.**

  Assert the audit API emits camelCase fields and that the Vue source binds `actorUserId`, `action`, `resourceType`, `resourceId`, `details`, and `createdAt`. Add a detail-dialog assertion.

- [x] **Step 2: Run the red-phase check.**

  ```bash
  go test ./internal/server -run TestAPIRouteUpdateAndAuditView -count=1
  npm --prefix web test -- --run src/tests/routes.spec.ts
  ```

  Expected result before implementation: the route audit assertion and frontend field assertions fail because the public view/UI do not expose the full contract.

- [x] **Step 3: Implement the audit public view.**

  Decode storage `details` JSON into an object and serialize a dedicated public struct with stable camelCase tags. Never expose internal Go field names or repository details.

- [x] **Step 4: Implement the audit page.**

  Show the five key columns, localized date formatting, a details dialog, and a localized empty-details state. Preserve cursor pagination parameters.

- [x] **Step 5: Run focused checks.**

  ```bash
  go test ./internal/server -run TestAPIRouteUpdateAndAuditView -count=1
  npm --prefix web test -- --run src/tests/routes.spec.ts
  ```

  Expected result: both audit and route frontend checks pass.

### Task 5: Managed-route edit UI

**Files:**

- Modify: `web/src/api/client.ts`
- Modify: `web/src/views/Routes.vue`
- Modify: `web/src/i18n/messages/zh-CN.ts`
- Modify: `web/src/i18n/messages/en-US.ts`
- Test: `web/src/tests/routes.spec.ts`

**Interfaces:**

- Consumes: `PATCH /api/v1/routes/{routeId}` and `ManagedRoute`.
- Produces: `updateRoute(id: string, input: ManagedRouteUpdateInput)`.

- [x] **Step 1: Write the failing route UI test.**

  Assert the API method/path/body and that `Routes.vue` contains `openEdit`, `editOpen`, `editingRoute`, and `updateRoute`.

- [x] **Step 2: Run the red-phase check.**

  ```bash
  npm --prefix web test -- --run src/tests/routes.spec.ts
  ```

  Expected result before implementation: the route edit workflow is absent.

- [x] **Step 3: Implement the edit dialog.**

  Reuse the create dialog for edit mode. Pre-fill Agent, domain, path, protocol, target, and status; show status only in edit mode; submit a partial payload to `PATCH`; replace the updated row locally and show localized feedback.

- [x] **Step 4: Run focused frontend checks.**

  ```bash
  npm --prefix web test -- --run src/tests/routes.spec.ts
  ```

  Expected result: route API and UI tests pass.

### Task 6: Full verification and PR record

**Files:**

- Modify: `internal/server/web_dist/` after building web assets.
- Create: `docs/pull-requests/2026-09-09-token-scope-route-edit-audit.md`

**Interfaces:**

- Consumes: all implementation and documentation changes.
- Produces: a PR-ready description with verification, rollout, and rollback evidence.

- [x] **Step 1: Run full Go verification.**

  ```bash
  go test ./... -count=1
  go test -race ./...
  go vet ./...
  git diff --check
  ```

  Expected result: all commands pass.

- [x] **Step 2: Run full frontend verification and refresh embedded assets.**

  ```bash
  npm --prefix web test -- --run
  npm --prefix web run build
  rsync -a --delete web/dist/ internal/server/web_dist/
  ./scripts/verify-web-embed.sh
  ```

  Expected result: 40 frontend tests pass, the production build succeeds, and the embedded assets verify.

- [x] **Step 3: Write the PR description.**

  Use `docs/pull-requests/2026-09-09-token-scope-route-edit-audit.md`; include user impact, API changes, security behavior, verification evidence, rollout, rollback, and reviewer focus.

## Completion Record

All implementation tasks were completed before this plan was written. The plan is a retroactive record; checkbox states describe the work that has already been verified. No commit, push, merge, or remote PR was performed because the user has not explicitly authorized those Git operations.
