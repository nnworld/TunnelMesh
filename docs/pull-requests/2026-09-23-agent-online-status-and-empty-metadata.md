# Agent list connectivity status and empty metadata view

## Title

`fix(server): report agent connectivity and empty metadata`

## Target branch

`main`

## Summary

Two defects made a never-connected agent look healthy and then fail:

1. The agent list rendered the administrative `enabled` flag as the status column, and the Chinese label for that flag is "在线" (online). `publicAgent` (`internal/server/api.go:1750`) returned no connectivity field at all, so an agent created in the console showed as online before it ever dialed in.
2. The detail page requested `GET /api/v1/agents/{id}/metadata?includeStale=true`. When `agent_instance_metadata` holds no row for the agent, `scanAgentMetadata` returns `sql.ErrNoRows` (`internal/storage/repository.go:1607`), which `writeStorageError` maps to 404 (`internal/server/api.go:1875`). The console's catch turned that into the "元数据加载失败" error banner with a retry button, and the designed empty state (`web/src/views/AgentDetail.vue:56`) was unreachable. The 404 conflated "agent does not exist" with "agent exists but never reported", and the latter is a legitimate empty collection.

## User impact

- The list now shows connectivity and the administrative switch as two columns: 在线/离线 (Online/Offline) from unexpired connection leases, and 启用/已禁用 (Enabled/Disabled) from the flag. A never-connected agent reads as "offline + enabled" instead of "online".
- The detail page of a never-connected agent shows the "暂无元数据" empty state instead of an error banner.
- The create dialog switch now reads 启用/已禁用, matching what it actually controls.

## API, schema, and configuration impact

- `GET /api/v1/agents` items gain `status: "online" | "offline"`. No other field changes; `enabled` keeps its meaning.
- `GET /api/v1/agents/{agentId}/metadata` now answers 200 with empty `items` and `instances` arrays when the agent exists but never reported. 404 remains for "agent not found" and for "snapshot stale without includeStale=true". The envelope always carries arrays rather than JSON null, because the console reads `items.length` without a null guard.
- No database schema or configuration change. `docs/api/openapi.yaml` is updated for both.

## Security impact

No change to authentication or authorization. The metadata handler still resolves and authorizes the parent agent before touching metadata, so the empty-view branch cannot be used to probe another user's resources: an unauthorized caller gets the same 403 as before. The new batch lease lookup receives only the IDs already returned by the owner-filtered page query, so it cannot widen what a caller sees, in line with the rule that permission filtering happens inside pagination semantics.

## Changes

- `internal/storage/repository.go`: `LeaseRepository.ListActiveAgentIDs` plus its `leaseRepo` implementation, the batch form of `ListActiveByAgent`, mirroring the existing `StatsByNodeIDs` placeholder pattern. Empty input issues no query.
- `internal/server/api.go`: `listAgents` resolves connectivity for exactly one page via `onlineAgentIDs` and renders rows through `publicAgentWithStatus`; `handleAgentMetadata` returns the empty view on `sql.ErrNoRows` and normalizes nil slices to empty arrays.
- `internal/storage/storage_contract_test.go`: contract assertions for the batch lookup (expiry filtering, IN filtering, empty input, post-release state).
- `internal/server/agent_list_status_test.go`: new red-light test walking offline -> online -> offline across lease register and release.
- `internal/server/metadata_api_test.go`: the subtest that pinned the old 404 contract now asserts the 200 empty view.
- `web/src/views/Agents.vue`, `web/src/api/client.ts`, `web/src/i18n/messages/{zh-CN,en-US}.ts`: two status columns, `Agent.status` type, new i18n keys.
- `web/src/tests/agents-view.spec.ts` (new) and `web/src/tests/agent-detail.spec.ts`: view-level guards.
- Docs: `docs/user-guide/server-admin.md`, `docs/operations/troubleshooting.md`, `docs/api/openapi.yaml`, retroactive plan, this record, regenerated indexes, rebuilt and re-embedded web assets.

## Tests run

- `go build ./...`
- `go test ./internal/server ./internal/storage -count=1` - pass
- `go test ./... -count=1` - no failing package
- `go test -race ./internal/server ./internal/storage -count=1`
- `go vet ./...` - clean
- `cd web && npm test -- --run` - 34 files / 290 tests pass
- `cd web && npm run build` plus `rsync -a --delete web/dist/ internal/server/web_dist/` and `./scripts/verify-web-embed.sh` - mirrored and matching
- `git diff --check` - clean

Red-light evidence before the fix:

- `TestListAgentsReportsConnectivityStatus`: `never-connected agent status = "", want offline` (field absent).
- `TestMetadataAPI/missing_metadata_returns_an_empty_view`: `status = 404, body = {"code":404,"msg":"Not Found","data":{"error":"not found"}}`, the exact envelope reported from the console.

## Release steps

1. Merge after review and CI checks.
2. Deploy the Server, including the rebuilt embedded console. Agent and Client need no upgrade; there is no schema migration and no lockstep requirement.
3. Canary on one node: confirm the list online count matches the dashboard `agentsOnline`, and that a never-connected agent shows offline plus the empty metadata state.

## Rollback steps

Revert the merge commit and redeploy the previous Server binary and embedded assets. No persisted state to unwind. Rolling back restores both defects: the list renders the enabled flag as connectivity, and a never-connected agent's detail page shows the 404 error banner.

## Reviewer focus

- Whether `status` belongs on the list response or should be a separate endpoint; the batch lookup keeps it to one extra query per page.
- Whether the empty metadata view should carry `stale: false` and zero-valued timestamps, which is what the console's empty state expects.
- The i18n wording change: `agents.active` now means "enabled", so any other surface reusing that key would change meaning. Only `web/src/views/Agents.vue` uses it.
- Whether `ListActiveAgentIDs` should live on `LeaseRepository` or beside the dashboard aggregate; it is placed with the other lease queries because it filters the same table on the same expiry predicate.

## Integration status

Implementation and local validation are complete on `codex/agent-online-status-empty-metadata`. The plan was confirmed by the user, including the decision to render connectivity and the enabled flag as two columns, and is recorded in `docs/superpowers/plans/2026-09-23-agent-online-status-and-empty-metadata.md`. Pull-request review and merge are pending.
