# Task 5 Report: Admin UI

## Implementation

- Added `ClusterAgentConnection` and `AgentConnectionCloseResult` API types.
- Added `listAgentConnections(agentId)` and `closeAgentConnection(agentId, connectionId, connectionEpoch)`.
- Replaced the Agent detail page's local-only connection table with the cluster connection list.
- Added refresh, Server node, Server address, connection epoch, active streams, heartbeat, and lease expiry columns.
- Added a danger close action and confirmation dialog showing Agent ID, connection ID, epoch, owner Server node, and active stream count.
- Disabled duplicate submission while closing and reloads the cluster list after success.
- Shows a localized warning when the remote owner Server returns `503`; the lease-preserved semantics are explicit.
- Added Chinese and English strings.
- Enabled CSS processing and inlined Element Plus in Vitest so mounted component tests can resolve on-demand styles.

## TDD Evidence

RED:

```bash
cd web
npm test -- --run src/tests/agent-detail.spec.ts
```

Initial result:

```text
expected listAgentConnections to be called with agent-1; number of calls: 0
missing [data-test="refresh-connections"]
missing [data-test="close-connection-conn-1"]
missing [data-test="close-connection-conn-2"]
```

GREEN:

```bash
cd web
npm test -- --run src/tests/agent-detail.spec.ts
npm test -- --run
npm run build
```

Results:

```text
Test Files: 10 passed
Tests: 50 passed
vite build completed successfully
```

## Files Changed

- `web/src/api/client.ts`
- `web/src/views/AgentDetail.vue`
- `web/src/i18n/messages/zh-CN.ts`
- `web/src/i18n/messages/en-US.ts`
- `web/src/tests/agent-detail.spec.ts`
- `web/vite.config.ts`

## Self-Review

- The UI always sends the lease epoch returned by the cluster list, never a locally derived protocol epoch.
- The confirmation dialog exposes the exact identity needed to audit the action.
- Remote-node unavailability keeps the dialog open and explains that the durable lease was preserved.
- The connection table now reflects cluster-wide state rather than only the responding process.
- The build emits code-split chunks and no oversized warning was observed.
- No commit was created.
