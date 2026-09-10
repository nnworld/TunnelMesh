import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, nextTick } from 'vue'
import AgentDetail from '../views/AgentDetail.vue'
import { i18n } from '../i18n'
import { formatDateTime } from '../i18n/format'
import { APIError, closeAgentConnection, getAgentMetadata, listAgentConnections } from '../api/client'

vi.mock('vue-router', () => ({ useRoute: () => ({ path: '/agents/agent-1', params: { id: 'agent-1' } }) }))
vi.mock('../api/client', async () => {
  const actual = await vi.importActual<typeof import('../api/client')>('../api/client')
  return {
    ...actual,
    getAgentMetadata: vi.fn(),
    listAgentConnections: vi.fn(),
    closeAgentConnection: vi.fn(),
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

async function flush() {
  await nextTick()
  await new Promise(resolve => setTimeout(resolve, 0))
  await nextTick()
}

async function mountAgentDetail() {
  const container = document.createElement('div')
  document.body.appendChild(container)
  const app = createApp(AgentDetail)
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
})
