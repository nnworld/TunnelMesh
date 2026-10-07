import { vi } from 'vitest'
import { createPinia, setActivePinia, type Pinia } from 'pinia'
import { mount, type VueWrapper } from '@vue/test-utils'
import type { Component, DefineComponent } from 'vue'
import { i18n, setLocale, type SupportedLocale } from '../i18n'
import { applyTheme } from '../theme'
import { setSecret } from '../api/client'
import type {
  AboutView,
  AgentRef,
  RoutingSaveResult,
  RoutingView,
  SettingsView,
  StatsView,
  ValidationReport,
} from '../api/types'

export const TEST_SECRET = 'test-secret-value'

/**
 * jsdom implements neither ResizeObserver nor matchMedia, and Element Plus uses both
 * during mount. Without them a table renders no body at all, which would make every
 * statistics assertion silently pass against an empty DOM.
 */
class ResizeObserverStub {
  observe(): void {}
  unobserve(): void {}
  disconnect(): void {}
}

if (typeof globalThis.ResizeObserver === 'undefined') {
  vi.stubGlobal('ResizeObserver', ResizeObserverStub)
}
if (typeof window !== 'undefined' && typeof window.matchMedia !== 'function') {
  vi.stubGlobal('matchMedia', (query: string) => ({
    matches: false,
    media: query,
    onchange: null,
    addEventListener: () => {},
    removeEventListener: () => {},
    addListener: () => {},
    removeListener: () => {},
    dispatchEvent: () => false,
  }))
}

/** Route table keyed by "METHOD /path". Query strings are ignored when matching. */
export type TrayRoutes = Record<string, unknown | ((body: unknown) => unknown)>

/** Failure table with the same keys, for exercising the error paths. */
export type TrayFailures = Record<string, { status: number; msg: string }>

export interface FakeTray {
  calls: Array<{ method: string; path: string; headers: Record<string, string>; body: unknown }>
  restore: () => void
}

function jsonResponse(status: number, payload: unknown) {
  return new Response(JSON.stringify(payload), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })
}

/**
 * Stubs fetch with an in-memory tray.
 *
 * The tests drive the same JSON contract the Go side serves, so a rename on either side
 * breaks a test instead of silently shipping a blank tab.
 */
export function installFakeTray(routes: TrayRoutes = {}, failures: TrayFailures = {}): FakeTray {
  const calls: FakeTray['calls'] = []
  const previous = globalThis.fetch

  globalThis.fetch = (async (input: unknown, init: RequestInit = {}) => {
    const url = new URL(String(input), 'http://127.0.0.1:1')
    const method = (init.method ?? 'GET').toUpperCase()
    const path = url.pathname
    const headers: Record<string, string> = {}
    new Headers(init.headers).forEach((value, key) => {
      headers[key] = value
    })
    let body: unknown
    if (typeof init.body === 'string' && init.body) {
      try {
        body = JSON.parse(init.body)
      } catch {
        body = init.body
      }
    }
    calls.push({ method, path, headers, body })

    const key = `${method} ${path}`
    const failure = failures[key]
    if (failure) {
      return jsonResponse(failure.status, { code: failure.status, msg: failure.msg, data: null })
    }
    const handler = routes[key] ?? routes[`ANY ${path}`]
    if (handler === undefined) {
      return jsonResponse(404, { code: 404, msg: `no fake route for ${key}`, data: null })
    }
    const data = typeof handler === 'function' ? (handler as (value: unknown) => unknown)(body) : handler
    return jsonResponse(200, { code: 200, msg: 'OK', data })
  }) as typeof fetch

  return {
    calls,
    restore: () => {
      globalThis.fetch = previous
    },
  }
}

export interface MountOptions {
  locale?: SupportedLocale
  secret?: string
  props?: Record<string, unknown>
  /** Seeds the stores before the component mounts, with the fresh pinia already active. */
  prepare?: (pinia: Pinia) => void
}

export interface Mounted {
  wrapper: VueWrapper
  pinia: Pinia
}

export function mountView(component: Component | DefineComponent, options: MountOptions = {}): Mounted {
  const pinia = createPinia()
  setActivePinia(pinia)
  setSecret(options.secret ?? TEST_SECRET)
  document.documentElement.classList.remove('dark')
  options.prepare?.(pinia)
  // Applied after the stores are seeded: adopting settings re-applies the stored
  // appearance, and a test that pins the locale has to win over that.
  setLocale(options.locale ?? 'en-US')
  applyTheme('light')
  const wrapper = mount(component as Component, {
    global: { plugins: [pinia, i18n] },
    props: options.props ?? {},
    attachTo: document.body,
  })
  return { wrapper, pinia }
}

export function flush(times = 4): Promise<void> {
  // One microtask per awaited call in the component chain, plus a spare for watchers.
  let chain = Promise.resolve()
  for (let index = 0; index < times; index += 1) {
    chain = chain.then(() => Promise.resolve())
  }
  return chain
}

export const fixtures = {
  settings(overrides: Partial<SettingsView> = {}): SettingsView {
    return {
      language: 'system',
      theme: 'system',
      configDir: '/Users/tester/.config/tunnelmesh',
      clientConfigPath: '/Users/tester/.config/tunnelmesh/client.yaml',
      prefsPath: '/Users/tester/.config/tunnelmesh/tray.json',
      lockPath: '/Users/tester/.config/tunnelmesh/client.lock',
      launchAtLogin: true,
      launchAtLoginSupported: true,
      minimizeToTray: true,
      quickPanel: false,
      languages: ['system', 'zh-CN', 'en-US'],
      themes: ['system', 'light', 'dark'],
      modes: ['local', 'cluster'],
      protocols: ['tcp', 'udp', 'http', 'socks5', 'http-proxy'],
      ...overrides,
    }
  },
  routing(overrides: Partial<RoutingView> = {}): RoutingView {
    return {
      mode: 'local',
      serverUrl: 'wss://mesh.example.com/ws/client',
      tokenPresent: true,
      configPath: '/Users/tester/.config/tunnelmesh/client.yaml',
      running: false,
      protocols: ['tcp', 'udp', 'http', 'socks5', 'http-proxy'],
      tunnels: [
        {
          name: 'web',
          protocol: 'tcp',
          listen: '127.0.0.1:8080',
          agentId: 'agent-a',
          targetHost: '10.0.0.8',
          targetPort: 80,
          allowRemote: false,
        },
        {
          name: 'proxy',
          protocol: 'socks5',
          listen: '127.0.0.1:1080',
          agentId: 'agent-b',
          targetHost: '',
          targetPort: 0,
          authMode: 'password',
          allowRemote: false,
        },
      ],
      ...overrides,
    }
  },
  agents(): AgentRef[] {
    return [
      { id: 'agent-a', name: 'office', online: true },
      { id: 'agent-b', name: 'lab', online: false },
    ]
  },
  report(overrides: Partial<ValidationReport> = {}): ValidationReport {
    return {
      valid: false,
      checkedAt: '2026-10-05T10:00:00Z',
      checks: [
        { id: 'serverUrl.format', status: 'passed' },
        { id: 'token.valid', status: 'failed', message: 'the server rejected the client token' },
        { id: 'tunnel.listen', status: 'warning', tunnel: 'web', index: 1, message: 'agent "office" is currently offline' },
        { id: 'tunnel.target', status: 'skipped', tunnel: 'proxy', index: 2 },
      ],
      ...overrides,
    }
  },
  stats(overrides: Partial<StatsView> = {}): StatsView {
    return {
      running: true,
      startedAt: '2026-10-05T09:00:00Z',
      uptimeSeconds: 3725,
      serverUrl: 'wss://mesh.example.com/ws/client',
      serverReachable: true,
      connected: true,
      lockBlocked: false,
      tunnels: [
        { name: 'web', protocol: 'tcp', listen: '127.0.0.1:8080', agentId: 'agent-a', target: '10.0.0.8:80', state: 'listening' },
        { name: 'proxy', protocol: 'socks5', listen: '127.0.0.1:1080', agentId: 'agent-b', state: 'failed', lastError: 'dial refused' },
      ],
      agents: [{ agentId: 'agent-a', openSlots: 2, readySessions: 2, activeStreams: 3 }],
      totals: {
        tunnels: 2,
        listening: 1,
        failed: 1,
        agents: 1,
        openSlots: 2,
        readySessions: 2,
        activeStreams: 3,
        reconnects: 1,
        bytesInbound: 2048,
      },
      collectedAt: '2026-10-05T10:00:05Z',
      ...overrides,
    }
  },
  about(overrides: Partial<AboutView> = {}): AboutView {
    return {
      version: '1.4.0',
      commit: 'abc1234',
      buildTime: '2026-10-05T08:00:00Z',
      system: { goos: 'darwin', arch: 'arm64', osVersion: '14.6.1' },
      configDir: '/Users/tester/.config/tunnelmesh',
      clientConfigPath: '/Users/tester/.config/tunnelmesh/client.yaml',
      prefsPath: '/Users/tester/.config/tunnelmesh/tray.json',
      lockPath: '/Users/tester/.config/tunnelmesh/client.lock',
      instanceId: 'client-instance-1',
      instanceIdPath: '/Users/tester/Library/Application Support/TunnelMesh/client-instance-id',
      mode: 'local',
      serverUrl: 'wss://mesh.example.com/ws/client',
      tunnelCount: 2,
      tokenPresent: true,
      running: true,
      websiteUrl: 'https://github.com/nnworld/TunnelMesh',
      releasesUrl: 'https://github.com/nnworld/TunnelMesh/releases',
      docsUrl: 'https://github.com/nnworld/TunnelMesh/tree/main/docs',
      license: 'Apache-2.0',
      ...overrides,
    }
  },
  saveResult(overrides: Partial<RoutingSaveResult> = {}): RoutingSaveResult {
    return { routing: fixtures.routing(), restarted: false, ...overrides }
  },
}
