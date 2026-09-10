### Task 5: Admin UI

**Files:**

- Modify: `web/src/api/client.ts`
- Modify: `web/src/views/AgentDetail.vue`
- Modify: `web/src/i18n/messages/zh-CN.ts`
- Modify: `web/src/i18n/messages/en-US.ts`
- Test: `web/src/tests/agent-detail.spec.ts`

**Interfaces:**

- Produces:

```ts
export type ClusterAgentConnection = {
  agentId: string
  instanceId: string
  connectionId: string
  connectionEpoch: number
  serverNodeId: string
  serverNodeEpoch: number
  serverNodeAddress?: string
  healthy: boolean
  activeStreams: number
  healthScore: number
  lastHeartbeatAt?: string
  leaseExpiresAt: string
  local: boolean
}

export function listAgentConnections(agentId: string): Promise<ClusterAgentConnection[]>
export function closeAgentConnection(
  agentId: string,
  connectionId: string,
  connectionEpoch: number,
): Promise<{ closed: boolean }>
```

- [ ] **Step 1: Write failing frontend tests**

Add tests that verify:

```ts
await wrapper.find('[data-test="refresh-connections"]').trigger('click')
expect(listAgentConnections).toHaveBeenCalledWith('agent-1')

await wrapper.find('[data-test="close-connection-conn-1"]').trigger('click')
await wrapper.find('[data-test="confirm-close-connection"]').trigger('click')
expect(closeAgentConnection).toHaveBeenCalledWith('agent-1', 'conn-1', 7)
```

Also test the remote-node column and the error message shown when close returns `503`.

- [ ] **Step 2: Run failing tests**

Run:

```bash
cd web
npm test -- --run src/tests/agent-detail.spec.ts
```

Expected result: failure because the UI actions and API helpers do not exist.

- [ ] **Step 3: Implement the UI**

Implementation requirements:

- Add a refresh button above the connection table.
- Add columns for Server node and lease expiry.
- Add a danger “Close” action for each connection.
- Confirm dialog shows Agent ID, connection ID, epoch, server node, and active stream count.
- Disable the confirm button while closing.
- Reload connections after success.
- Show a localized warning for remote-node unavailability.

- [ ] **Step 4: Verify task**

Run:

```bash
cd web
npm test -- --run
npm run build
```

Expected result: frontend tests and production build pass.
