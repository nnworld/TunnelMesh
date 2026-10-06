import { defineStore } from 'pinia'
import { getRouting, listAgents, saveRouting, validateRouting } from '../api/client'
import type {
  AgentRef,
  RoutingSaveResult,
  RoutingUpdate,
  RoutingView,
  TunnelProtocol,
  TunnelView,
  ValidationReport,
} from '../api/types'
import { describe } from './settings'

/** Token sentinel: null keeps the stored secret, '' clears it. */
const KEEP_TOKEN: string | null = null

export function emptyTunnel(): TunnelView {
  return {
    name: '',
    protocol: 'tcp',
    listen: '127.0.0.1:',
    agentId: '',
    targetHost: '',
    targetPort: 0,
    authMode: 'none',
    allowRemote: false,
    authUrl: '',
  }
}

/**
 * The routing tab's state.
 *
 * The token is write-only here: the tray never sends it back, so the store keeps an edit
 * buffer that starts empty and only leaves the page as a replacement value. `tokenTouched`
 * is what tells the save whether to send null (keep) or a string (replace or clear).
 */
export const useRoutingStore = defineStore('routing', {
  state: () => ({
    view: null as RoutingView | null,
    loading: false,
    saving: false,
    validating: false,
    error: '' as string,
    serverUrl: '',
    mode: 'local' as RoutingView['mode'],
    tokenPresent: false,
    tokenEdit: '',
    tokenTouched: false,
    clearToken: false,
    tunnels: [] as TunnelView[],
    agents: [] as AgentRef[],
    agentsLoading: false,
    agentsError: '',
    report: null as ValidationReport | null,
    lastSave: null as RoutingSaveResult | null,
  }),
  getters: {
    protocols: (state): TunnelProtocol[] => state.view?.protocols ?? ['tcp', 'udp', 'http', 'socks5', 'http-proxy'],
    configPath: (state) => state.view?.configPath ?? '',
    running: (state) => state.view?.running ?? false,
    needsTarget: () => (protocol: string) => protocol === 'tcp' || protocol === 'udp' || protocol === 'http',
    isProxy: () => (protocol: string) => protocol === 'socks5' || protocol === 'http-proxy',
    failedChecks: (state) => (state.report?.checks ?? []).filter((check) => check.status === 'failed'),
  },
  actions: {
    adopt(view: RoutingView) {
      this.view = view
      this.serverUrl = view.serverUrl
      this.mode = view.mode
      this.tokenPresent = view.tokenPresent
      this.tokenEdit = ''
      this.tokenTouched = false
      this.clearToken = false
    // A file that omits auth_mode means "none" (config.Validate agrees), and the select has
    // to show that rather than an empty box the operator feels obliged to fill in.
    this.tunnels = view.tunnels.map((tunnel) => ({ ...tunnel, authMode: tunnel.authMode || 'none' }))
    },
    async load() {
      this.loading = true
      this.error = ''
      try {
        this.adopt(await getRouting())
      } catch (cause) {
        this.error = describe(cause)
        throw cause
      } finally {
        this.loading = false
      }
    },
    addTunnel() {
      this.tunnels.push(emptyTunnel())
    },
    removeTunnel(index: number) {
      this.tunnels.splice(index, 1)
    },
    /** The payload sent for both save and validate. */
    payload(token: string | null = this.tokenIntent()): RoutingUpdate {
      return {
        mode: this.mode,
        serverUrl: this.serverUrl,
        token,
        tunnels: this.tunnels.map((tunnel) => ({ ...tunnel })),
      }
    },
    tokenIntent(): string | null {
      if (this.clearToken) return ''
      if (!this.tokenTouched) return KEEP_TOKEN
      return this.tokenEdit
    },
    async loadAgents() {
      this.agentsLoading = true
      this.agentsError = ''
      try {
        this.agents = (await listAgents(this.serverUrl)) ?? []
      } catch (cause) {
        this.agentsError = describe(cause)
        throw cause
      } finally {
        this.agentsLoading = false
      }
    },
    async validate() {
      this.validating = true
      this.error = ''
      try {
        this.report = await validateRouting(this.payload())
        if (this.report.agents?.length) this.agents = this.report.agents
        return this.report
      } catch (cause) {
        this.error = describe(cause)
        throw cause
      } finally {
        this.validating = false
      }
    },
    async save() {
      this.saving = true
      this.error = ''
      try {
        this.lastSave = await saveRouting(this.payload())
        this.adopt(this.lastSave.routing)
        return this.lastSave
      } catch (cause) {
        this.error = describe(cause)
        throw cause
      } finally {
        this.saving = false
      }
    },
  },
})
