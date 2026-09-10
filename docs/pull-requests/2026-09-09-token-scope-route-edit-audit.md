# feat(admin): edit token scope and managed routes

**PR title:** `feat(admin): edit token scope and managed routes`

**Base:** `main`

## Summary

This change closes two management-console gaps:

- Active service tokens can now update protocol, target CIDR, and target-port restrictions without revocation or re-creation.
- Managed routes can be edited after creation, and audit logs now expose a stable camelCase public contract with readable fields and structured details.

There is no database schema migration in this change.

## User-facing changes

### Tokens

- Adds a “修改范围 / Update scope” action for active tokens.
- Supports `protocols`, `targetCIDRs`, and `targetPorts`.
- Omitted fields keep their current values; empty arrays remove that restriction.
- Invalid ports are rejected in the UI and API instead of silently becoming unrestricted.

### Routes

- Adds an “编辑 / Edit” action on `/routes`.
- Supports Agent, domain, path, protocol, target host, target port, and status.
- Target-Agent ownership is rechecked when moving a route.
- Existing route cache semantics remain unchanged: updates take effect within its 5-second TTL.

### Audit logs

- Shows time, actor, action, resource type, and resource ID.
- Adds server-side filters for time range, actor, action, resource type, and resource ID, with Reset and Search actions at the bottom right of the filter card.
- Lists newest events first, with audit ID as the deterministic tie-breaker for identical timestamps.
- Keeps cursor pagination stable and accepts legacy ID-only cursors.
- Adds a details dialog with structured JSON.
- Uses a stable public API contract instead of storage-layer Go field names.

## API changes

### Update token scope

```http
PATCH /api/v1/tokens/{tokenId}
Content-Type: application/json

{
  "scope": {
    "protocols": ["tcp", "http"],
    "targetCIDRs": ["10.0.0.0/8"],
    "targetPorts": [22, 80]
  }
}
```

Semantics:

- Each scope field is optional and independently patchable.
- An omitted field preserves the current value.
- An empty array removes that restriction.
- `expiresAt` can still be updated in the same request.
- Token type, owner, and Agent/Node binding cannot be changed.

Errors: `400` invalid input, `403` non-owner/non-admin, `404` missing token, `409` revoked or expired token.

### Update managed route

```http
PATCH /api/v1/routes/{routeId}
Content-Type: application/json

{
  "agentId": "agent-devbox",
  "domain": "git.example.com",
  "pathPrefix": "/",
  "protocol": "websocket",
  "targetHost": "127.0.0.1",
  "targetPort": 3000,
  "status": "active"
}
```

Semantics:

- Only supplied fields are updated.
- `PUT` continues to require the full required route payload.
- Domain validation follows existing exact-domain and single-level `tm-*` rules.
- Duplicate domain/path combination returns `409`.

### Audit log public schema

```json
{
  "id": "audit-...",
  "actorUserId": "user-...",
  "action": "route.updated",
  "resourceType": "tunnel",
  "resourceId": "route-...",
  "details": {},
  "createdAt": "2026-09-09T00:00:00Z"
}
```

The endpoint returns `createdAt` descending and `id` descending. Cursor values are opaque and now encode the `(createdAt, id)` keyset; ID-only cursors from the previous version remain accepted.

Optional query filters are `actorUserId`, `action`, `resourceType`, `resourceId`, `createdFrom`, and `createdTo`. Text filters are exact matches; time bounds are inclusive.

## Security and authorization

- Authorization is enforced server-side; UI visibility is only an affordance.
- Token updates are limited to the owner or an administrator.
- Route updates recheck current and target-Agent ownership.
- Revoked and expired tokens fail closed with `409`.
- Token responses and audits never contain plaintext credentials or hashes.
- Route audits contain only non-secret routing facts and never include free-form config.

## Audit events

- `token.scope_updated`
- `token.expiration_updated` when expiration is changed in the same request
- `route.updated`

## Testing evidence

The implementation was verified with:

```bash
go test ./... -count=1
go test -race ./...
go vet ./...
git diff --check
npm --prefix web test -- --run
npm --prefix web run build
./scripts/verify-web-embed.sh
```

All passed. The frontend suite reported 42 tests. A timing-sensitive reconnect test (`TestClientWSReconnectBackoffGrowsAcrossRepeatedDisconnects`) failed once during an exploratory full run, then passed in 10 focused reruns and the subsequent full race run; it is unrelated to these changes.

Key focused tests:

- `TestTokenAPIUpdatesScopeForActiveTokensOnly`
- `TestServiceTokenRepositoryContractSQLite`
- `TestAPIRouteUpdateAndAuditView`
- `TestAuditRepositoryListsNewestFirstWithStablePagination`
- `TestAuditRepositoryAppliesFiltersWithStablePagination`
- `TestAuditAPIFiltersAndValidatesTimeRange`
- `web/src/tests/audit-logs.spec.ts`
- `web/src/tests/agent-token-workflows.spec.ts`
- `web/src/tests/routes.spec.ts`

## Rollout

1. Build and deploy the new `tunnelmesh-server` binary together with its embedded web assets.
2. No database migration is required.
3. Existing active sessions and Agent connections remain valid.
4. Token scope changes are applied when the token is next used for authorization.
5. Route changes are visible to the routing layer within the existing 5-second cache TTL.

## Rollback

1. Stop the new server binary.
2. Start the previous verified binary.
3. No schema rollback is needed.
4. Token and route changes already committed to the database remain readable by the previous binary; only the new edit APIs/UI disappear.
5. Audit records written by the new binary remain safe non-secret facts.

## Reviewer focus

- Confirm scope merge preserves omitted fields and treats empty arrays as unrestricted.
- Confirm revoked/expired tokens cannot be revived by any patch path.
- Confirm target-Agent ownership is checked after decoding and before persistence.
- Confirm domain/path uniqueness is enforced under concurrent updates.
- Confirm audit output and UI contain no secrets or internal-only fields.
- Confirm audit ordering and pagination remain stable when timestamps tie.
- Confirm OpenAPI matches actual status codes and request semantics.

## Integration status

This document is the PR body prepared in the repository. No `git commit`, `git push`, merge, or remote PR was executed.
