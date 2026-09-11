import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, nextTick } from 'vue'
import { createPinia } from 'pinia'
import AgentDetail from '../views/AgentDetail.vue'
import { i18n } from '../i18n'
import { formatDateTime } from '../i18n/format'
import { APIError, closeAgentConnection, createAgentPolicy, deleteAgentPolicy, getAgentMetadata, listAgentConnections, listAgentPolicies, restoreAgentPolicy, updateAgentPolicy } from '../api/client'
import { useAuthStore } from '../stores/auth'

vi.mock('vue-router', () => ({ useRoute: () => ({ path: '/agents/agent-1', params: { id: 'agent-1' } }) }))
vi.mock('../api/client', async () => {
  const actual = await vi.importActual<typeof import('../api/client')>('../api/client')
  return {
    ...actual,
    getAgentMetadata: vi.fn(),
    listAgentConnections: vi.fn(),
    closeAgentConnection: vi.fn(),
    listAgentPolicies: vi.fn(),
    createAgentPolicy: vi.fn(),
    updateAgentPolicy: vi.fn(),
    deleteAgentPolicy: vi.fn(),
    restoreAgentPolicy: vi.fn(),
  }
})

const metadata = {
  agentId: 'agent-1', nodeId: 'node-1', epoch: 1, revision: 1, stale: false,
  reportedAt: '2026-09-09T10:00:00Z', updatedAt: '2026-09-09T10:00:00Z', items: [], instances: [],
}

const connections = [
  {
    agentId: 'agent-1', instanceId: 'instance-1', connectionId: 'conn-1', connectionEpoch: 7,
    serverNodeId: 'server-a', serverNodeEpoch: 1, serverNodeAddress: '10.0.0.1:9443',
    healthy: true, activeStreams: 2, healthScore: 100,
    lastHeartbeatAt: '2026-09-09T10:00:01Z', leaseExpiresAt: '2026-09-09T10:01:30Z', local: true,
  },
  {
    agentId: 'agent-1', instanceId: 'instance-2', connectionId: 'conn-2', connectionEpoch: 11,
    serverNodeId: 'server-b', serverNodeEpoch: 8, serverNodeAddress: '10.0.0.2:9443',
    healthy: true, activeStreams: 1, healthScore: 90,
    lastHeartbeatAt: '2026-09-09T10:00:02Z', leaseExpiresAt: '2026-09-09T10:01:31Z', local: false,
  },
]

const policies = [
  {
    id: 'policy-any', agentId: 'agent-1', protocol: 'tcp', targetHost: '*', targetPort: 0,
    allowedCIDRs: [], allowedPorts: [],
    createdAt: '2026-09-09T10:00:00Z', updatedAt: '2026-09-09T10:00:00Z',
  },
  {
    id: 'policy-https', agentId: 'agent-1', protocol: 'tcp', targetHost: 'service.internal', targetPort: 443,
    allowedCIDRs: ['10.20.0.0/16'], allowedPorts: [443, 8443],
    createdAt: '2026-09-09T10:01:00Z', updatedAt: '2026-09-09T10:01:00Z',
  },
]

const deletedPolicy = {
  ...policies[0],
  id: 'policy-deleted',
  deletedAt: '2026-09-10T10:00:00Z',
}

async function flush() {
  await nextTick()
  await new Promise(resolve => setTimeout(resolve, 0))
  await nextTick()
}

async function mountAgentDetail(role: 'admin' | 'user' = 'admin') {
  const container = document.createElement('div')
  document.body.appendChild(container)
  const app = createApp(AgentDetail)
  const pinia = createPinia()
  app.use(pinia)
  const auth = useAuthStore(pinia)
  auth.user = { id: 'user-1', username: 'tester', role }
  app.use(i18n)
  app.mount(container)
  await flush()
  return { container, unmount: () => { app.unmount(); container.remove() } }
}

function click(selector: string, container: HTMLElement) {
  const element = container.querySelector(selector)
  if (!element) throw new Error(`missing ${selector}`)
  element.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }))
  return flush()
}

describe('agent detail cluster connections', () => {
  beforeEach(() => {
    vi.mocked(getAgentMetadata).mockReset().mockResolvedValue(metadata)
    vi.mocked(listAgentConnections).mockReset().mockResolvedValue(connections)
    vi.mocked(closeAgentConnection).mockReset().mockResolvedValue({ closed: true })
    vi.mocked(listAgentPolicies).mockReset().mockImplementation(async (_agentId, params = {}) => ({
      items: params.status === 'deleted' ? [deletedPolicy]
        : params.status === 'all' ? [...policies, deletedPolicy]
        : policies,
    }))
    vi.mocked(createAgentPolicy).mockReset().mockResolvedValue(policies[0])
    vi.mocked(updateAgentPolicy).mockReset().mockResolvedValue(policies[1])
    vi.mocked(deleteAgentPolicy).mockReset().mockResolvedValue(deletedPolicy)
    vi.mocked(restoreAgentPolicy).mockReset().mockResolvedValue(policies[0])
    document.body.innerHTML = ''
  })

  it('lists local and remote connections with owner and lease expiry columns', async () => {
    const { container, unmount } = await mountAgentDetail()
    try {
      expect(listAgentConnections).toHaveBeenCalledWith('agent-1')
      expect(container.textContent).toContain('server-b')
      expect(container.textContent).toContain('10.0.0.2:9443')
      expect(container.textContent).toContain(formatDateTime(connections[1].leaseExpiresAt, i18n.global.locale.value as 'en-US'))
      expect(container.querySelector('[data-test="close-connection-conn-1"]')).not.toBeNull()
      expect(container.querySelector('[data-test="close-connection-conn-2"]')).not.toBeNull()
    } finally {
      unmount()
    }
  })

  it('derives logical agent status from healthy pooled connections', async () => {
    vi.mocked(listAgentConnections).mockReset().mockResolvedValue([
      { ...connections[0], instanceId: 'instance-live', healthy: true },
      { ...connections[1], instanceId: 'instance-offline', healthy: false },
    ])
    vi.mocked(getAgentMetadata).mockReset().mockResolvedValue({
      ...metadata,
      stale: true,
      instances: [
        {
          instanceId: 'instance-live', nodeId: 'node-live', epoch: 2, revision: 1, stale: true,
          reportedAt: '2026-09-09T10:00:00Z', updatedAt: '2026-09-09T10:00:00Z', items: [], connectionCount: 1,
        },
        {
          instanceId: 'instance-offline', nodeId: 'node-offline', epoch: 1, revision: 1, stale: true,
          reportedAt: '2026-09-08T10:00:00Z', updatedAt: '2026-09-08T10:00:00Z', items: [], connectionCount: 0,
        },
      ],
    })
    const { container, unmount } = await mountAgentDetail()
    try {
      const summary = container.querySelector('[data-test="agent-logical-summary"]')
      expect(summary).not.toBeNull()
      expect(summary?.textContent).toContain(i18n.global.t('agentDetail.fresh'))
      expect(summary?.textContent).not.toContain(i18n.global.t('agentDetail.instanceId'))
      expect(summary?.textContent).not.toContain(i18n.global.t('agentDetail.nodeId'))
      expect(summary?.textContent).not.toContain(i18n.global.t('agentDetail.epoch'))
      expect(summary?.textContent).not.toContain(i18n.global.t('agentDetail.revision'))

      const cells = [...summary?.querySelectorAll('td.el-descriptions__cell') || []]
      const labelCell = cells.find(cell => cell.textContent?.trim() === i18n.global.t('agentDetail.activeInstances'))
      expect(labelCell?.nextElementSibling?.textContent?.trim()).toBe('1')
      const connectionCell = cells.find(cell => cell.textContent?.trim() === i18n.global.t('agentDetail.activeConnections'))
      expect(connectionCell?.nextElementSibling?.textContent?.trim()).toBe('1')
    } finally {
      unmount()
    }
  })

  it('shows instance status from healthy connections', async () => {
    vi.mocked(listAgentConnections).mockReset().mockResolvedValue([
      { ...connections[0], instanceId: 'instance-live', healthy: true },
      { ...connections[1], instanceId: 'instance-offline', healthy: false },
    ])
    vi.mocked(getAgentMetadata).mockReset().mockResolvedValue({
      ...metadata,
      instances: [
        {
          instanceId: 'instance-live', nodeId: 'node-live', epoch: 2, revision: 1, stale: true,
          reportedAt: '2026-09-09T10:00:00Z', updatedAt: '2026-09-09T10:00:00Z', items: [], connectionCount: 1,
        },
        {
          instanceId: 'instance-offline', nodeId: 'node-offline', epoch: 1, revision: 1, stale: true,
          reportedAt: '2026-09-08T10:00:00Z', updatedAt: '2026-09-08T10:00:00Z', items: [], connectionCount: 0,
        },
      ],
    })
    const { container, unmount } = await mountAgentDetail()
    try {
      const table = container.querySelector('[data-test="agent-instance-table"]')
      expect(table).not.toBeNull()
      expect(table?.textContent).toContain(i18n.global.t('agentDetail.fresh'))
      expect(table?.textContent).toContain(i18n.global.t('agentDetail.stale'))
    } finally {
      unmount()
    }
  })

  it('shows per-instance connection counts from healthy connections', async () => {
    vi.mocked(listAgentConnections).mockReset().mockResolvedValue([
      { ...connections[0], instanceId: 'instance-live', healthy: true },
      { ...connections[1], instanceId: 'instance-live', healthy: true },
      { ...connections[0], instanceId: 'instance-offline', connectionId: 'conn-offline', healthy: false },
    ])
    vi.mocked(getAgentMetadata).mockReset().mockResolvedValue({
      ...metadata,
      instances: [
        {
          instanceId: 'instance-live', nodeId: 'node-live', epoch: 2, revision: 1, stale: true,
          reportedAt: '2026-09-09T10:00:00Z', updatedAt: '2026-09-09T10:00:00Z', items: [], connectionCount: 999,
        },
        {
          instanceId: 'instance-offline', nodeId: 'node-offline', epoch: 1, revision: 1, stale: true,
          reportedAt: '2026-09-08T10:00:00Z', updatedAt: '2026-09-08T10:00:00Z', items: [], connectionCount: 999,
        },
      ],
    })
    const { container, unmount } = await mountAgentDetail()
    try {
      const table = container.querySelector('[data-test="agent-instance-table"]')
      expect(table).not.toBeNull()
      const rows = [...table?.querySelectorAll('tbody tr') || []]
      expect(rows[0]?.querySelectorAll('td')[5]?.textContent?.trim()).toBe('2')
      expect(rows[1]?.querySelectorAll('td')[5]?.textContent?.trim()).toBe('0')
    } finally {
      unmount()
    }
  })

  it('refreshes the cluster connection list', async () => {
    const { container, unmount } = await mountAgentDetail()
    try {
      await click('[data-test="refresh-connections"]', container)
      expect(listAgentConnections).toHaveBeenCalledTimes(2)
      expect(listAgentConnections).toHaveBeenLastCalledWith('agent-1')
    } finally {
      unmount()
    }
  })

  it('confirms and closes one exact connection, then reloads the list', async () => {
    const { container, unmount } = await mountAgentDetail()
    try {
      await click('[data-test="close-connection-conn-1"]', container)
      expect(container.textContent).toContain('agent-1')
      expect(container.textContent).toContain('conn-1')
      expect(container.textContent).toContain('7')
      expect(container.textContent).toContain('server-a')
      expect(container.textContent).toContain('2')
      await click('[data-test="confirm-close-connection"]', container)
      expect(closeAgentConnection).toHaveBeenCalledWith('agent-1', 'conn-1', 7)
      expect(listAgentConnections).toHaveBeenCalledTimes(2)
    } finally {
      unmount()
    }
  })

  it('shows a remote-node warning when close returns 503', async () => {
    vi.mocked(closeAgentConnection).mockRejectedValue(new APIError('owner server node unavailable', 503))
    const { container, unmount } = await mountAgentDetail()
    try {
      await click('[data-test="close-connection-conn-2"]', container)
      await click('[data-test="confirm-close-connection"]', container)
      const warning = container.querySelector('[data-test="connection-close-error"]')
      expect(warning?.textContent).toContain(i18n.global.t('agentDetail.remoteNodeUnavailable'))
    } finally {
      unmount()
    }
  })

  it('shows agent policies with wildcard and unrestricted semantics', async () => {
    const { container, unmount } = await mountAgentDetail()
    try {
      expect(listAgentPolicies).toHaveBeenCalledWith('agent-1')
      expect(container.textContent).toContain('Any host')
      expect(container.textContent).toContain('Any port')
      expect(container.textContent).toContain('Unrestricted')
      expect(container.textContent).toContain('service.internal')
      expect(container.textContent).toContain('10.20.0.0/16')
      expect(container.textContent).toContain('443, 8443')
    } finally {
      unmount()
    }
  })

  it('renders policy actions as a normal column and opens the edit dialog', async () => {
    const { container, unmount } = await mountAgentDetail()
    try {
      const editButton = container.querySelector<HTMLButtonElement>('[data-test="edit-agent-policy-policy-any"]')
      const actionCell = editButton?.closest('td')
      expect(actionCell?.className).not.toContain('el-table-fixed-column--right')
      expect(actionCell?.getAttribute('style')).toBeNull()

      await click('[data-test="edit-agent-policy-policy-any"]', container)
      expect(container.textContent).toContain(i18n.global.t('agentDetail.policyEditTitle'))
    } finally {
      unmount()
    }
  })

  it('shows policy management controls only to admins', async () => {
    const admin = await mountAgentDetail('admin')
    try {
      expect(admin.container.querySelector('[data-test="create-agent-policy"]')).not.toBeNull()
      expect(admin.container.querySelector('[data-test="edit-agent-policy-policy-any"]')).not.toBeNull()
      expect(admin.container.querySelector('[data-test="delete-agent-policy-policy-any"]')).not.toBeNull()
      expect(admin.container.querySelector('[data-test="policy-status-deleted"]')).not.toBeNull()
    } finally {
      admin.unmount()
    }

    const user = await mountAgentDetail('user')
    try {
      expect(user.container.querySelector('[data-test="create-agent-policy"]')).toBeNull()
      expect(user.container.querySelector('[data-test="edit-agent-policy-policy-any"]')).toBeNull()
      expect(user.container.querySelector('[data-test="delete-agent-policy-policy-any"]')).toBeNull()
      expect(user.container.querySelector('[data-test="policy-status-deleted"]')).toBeNull()
    } finally {
      user.unmount()
    }
  })

  it('filters deleted policies and shows restore instead of edit or delete', async () => {
    const { container, unmount } = await mountAgentDetail('admin')
    try {
      await click('[data-test="policy-status-deleted"]', container)

      expect(listAgentPolicies).toHaveBeenLastCalledWith('agent-1', { status: 'deleted' })
      expect(container.textContent).toContain(i18n.global.t('agentDetail.policyStatusDeleted'))
      expect(container.querySelector('[data-test="restore-agent-policy-policy-deleted"]')).not.toBeNull()
      expect(container.querySelector('[data-test="edit-agent-policy-policy-deleted"]')).toBeNull()
      expect(container.querySelector('[data-test="delete-agent-policy-policy-deleted"]')).toBeNull()
    } finally {
      unmount()
    }
  })

  it('confirms logical deletion and reloads the active policy list', async () => {
    const { container, unmount } = await mountAgentDetail('admin')
    try {
      await click('[data-test="delete-agent-policy-policy-any"]', container)
      await click('[data-test="confirm-delete-agent-policy"]', container)

      expect(deleteAgentPolicy).toHaveBeenCalledWith('agent-1', 'policy-any')
      expect(listAgentPolicies).toHaveBeenCalledTimes(2)
    } finally {
      unmount()
    }
  })

  it('restores a deleted policy and reloads the list', async () => {
    const { container, unmount } = await mountAgentDetail('admin')
    try {
      await click('[data-test="policy-status-deleted"]', container)
      await click('[data-test="restore-agent-policy-policy-deleted"]', container)

      expect(restoreAgentPolicy).toHaveBeenCalledWith('agent-1', 'policy-deleted')
      expect(listAgentPolicies).toHaveBeenCalledTimes(3)
    } finally {
      unmount()
    }
  })

  it('creates a wildcard policy from the agent detail page', async () => {
    const { container, unmount } = await mountAgentDetail('admin')
    try {
      await click('[data-test="create-agent-policy"]', container)
      await click('[data-test="save-agent-policy"]', container)

      expect(createAgentPolicy).toHaveBeenCalledWith('agent-1', {
        protocol: 'tcp',
        targetHost: '*',
        targetPort: 0,
        allowedCIDRs: [],
        allowedPorts: [],
      }, expect.any(String))
      expect(listAgentPolicies).toHaveBeenCalledTimes(2)
    } finally {
      unmount()
    }
  })

  it('edits an existing policy with its current values', async () => {
    const { container, unmount } = await mountAgentDetail('admin')
    try {
      await click('[data-test="edit-agent-policy-policy-https"]', container)
      await click('[data-test="save-agent-policy"]', container)

      expect(updateAgentPolicy).toHaveBeenCalledWith('agent-1', 'policy-https', {
        protocol: 'tcp',
        targetHost: 'service.internal',
        targetPort: 443,
        allowedCIDRs: ['10.20.0.0/16'],
        allowedPorts: [443, 8443],
      })
      expect(listAgentPolicies).toHaveBeenCalledTimes(2)
    } finally {
      unmount()
    }
  })

  it('rejects invalid port lists before calling the API', async () => {
    const { container, unmount } = await mountAgentDetail('admin')
    try {
      await click('[data-test="create-agent-policy"]', container)
      const portsInput = container.querySelector<HTMLInputElement>('input[placeholder="443, 8443"]')
      if (!portsInput) throw new Error('missing ports input')
      portsInput.value = '443, abc'
      portsInput.dispatchEvent(new Event('input', { bubbles: true }))
      await flush()

      await click('[data-test="save-agent-policy"]', container)

      expect(container.querySelector('[data-test="policy-form-error"]')?.textContent).toContain(i18n.global.t('agentDetail.policyInvalidPortList'))
      expect(createAgentPolicy).not.toHaveBeenCalled()
    } finally {
      unmount()
    }
  })

  it('reuses the idempotency key when creating a policy fails', async () => {
    vi.mocked(createAgentPolicy).mockReset()
      .mockRejectedValueOnce(new Error('network timeout'))
      .mockResolvedValueOnce(policies[0])
    const { container, unmount } = await mountAgentDetail('admin')
    try {
      await click('[data-test="create-agent-policy"]', container)
      await click('[data-test="save-agent-policy"]', container)
      await click('[data-test="save-agent-policy"]', container)

      expect(createAgentPolicy).toHaveBeenCalledTimes(2)
      const firstKey = vi.mocked(createAgentPolicy).mock.calls[0][2]
      const secondKey = vi.mocked(createAgentPolicy).mock.calls[1][2]
      expect(firstKey).toBe(secondKey)
    } finally {
      unmount()
    }
  })
})
