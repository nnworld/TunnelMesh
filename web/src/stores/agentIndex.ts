import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { getAgents, type Agent } from '../api/client'
import { loadAgentsForSelection } from '../views/token-form'

export type AgentIndexStatus = 'idle' | 'loading' | 'ready' | 'error'

// Concurrent cells mount at once, so the dedupe has to live outside the store
// instance: two `ensureLoaded()` calls issued in the same tick must still be
// one request even before the first one has written `status`.
let inflight: Promise<void> | null = null

// One Agent inventory per console. Names are labels, not identities: callers
// keep using the id for routing, filtering and every request payload, and an id
// the index does not know simply renders as itself.
export const useAgentIndex = defineStore('agent-index', () => {
  const agents = ref<Agent[]>([])
  const status = ref<AgentIndexStatus>('idle')
  const names = computed(() => new Map(agents.value.map(agent => [agent.id, agent.name])))

  async function load(): Promise<void> {
    if (inflight) return inflight
    status.value = 'loading'
    inflight = (async () => {
      try {
        // Replaced only after the whole cursor walk succeeds, so a failed
        // refresh leaves the previous names readable instead of flashing ids.
        agents.value = await loadAgentsForSelection(getAgents)
        status.value = 'ready'
      } catch {
        status.value = 'error'
      } finally {
        inflight = null
      }
    })()
    return inflight
  }

  // A settled index never reloads on its own: a list page can carry hundreds of
  // references, and one failed request should degrade them all to bare ids
  // rather than restart a retry storm on every render.
  async function ensureLoaded(): Promise<void> {
    if (status.value !== 'idle') return
    await load()
  }

  // Explicit re-pull for the paths that change Agent identities, such as a
  // rename on the Agent page itself.
  async function refresh(): Promise<void> {
    await load()
  }

  function nameOf(id?: string | null): string {
    if (!id) return ''
    return names.value.get(id) || ''
  }

  return { agents, status, ensureLoaded, refresh, nameOf }
})
