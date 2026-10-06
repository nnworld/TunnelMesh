import { afterEach, describe, expect, it, vi } from 'vitest'
import {
  ApiError,
  apiFetch,
  getRouting,
  initSecret,
  listAgents,
  readSecretFromLocation,
  runAction,
  setSecret,
  validateRouting,
} from '../api/client'
import { fixtures, installFakeTray, TEST_SECRET } from './helpers'

describe('local API client', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
    window.history.replaceState(null, '', '/')
  })

  it('captures the launch secret from the URL and scrubs it from the address bar', () => {
    window.history.replaceState(null, '', '/?secret=launch-secret&other=1')
    expect(readSecretFromLocation()).toBe('launch-secret')
    expect(window.location.search).not.toContain('secret=launch-secret')
    expect(window.location.search).toContain('other=1')

    window.history.replaceState(null, '', '/?secret=second')
    expect(initSecret()).toBe('second')
    expect(window.location.search).toBe('')
  })

  it('sends the secret as a header on every call', async () => {
    setSecret(TEST_SECRET)
    const tray = installFakeTray({ 'GET /api/routing': fixtures.routing() })
    const view = await getRouting()
    expect(view.serverUrl).toBe('wss://mesh.example.com/ws/client')
    expect(tray.calls[0].headers['x-tray-secret']).toBe(TEST_SECRET)
    tray.restore()
  })

  it('unwraps the {code,msg,data} envelope and passes query parameters through', async () => {
    setSecret(TEST_SECRET)
    const tray = installFakeTray({ 'GET /api/agents': fixtures.agents() })
    const agents = await listAgents('wss://mesh.example.com/ws/client')
    expect(agents).toHaveLength(2)
    expect(tray.calls[0].path).toBe('/api/agents')
    tray.restore()
  })

  it('sends a JSON body for writes and undefined for an empty validation', async () => {
    setSecret(TEST_SECRET)
    const tray = installFakeTray({
      'POST /api/validate': fixtures.report(),
      'POST /api/actions/start': { running: true },
    })
    await validateRouting({ mode: 'local', serverUrl: 'wss://x/ws/client', token: null, tunnels: [] })
    expect(tray.calls[0].body).toEqual({ mode: 'local', serverUrl: 'wss://x/ws/client', token: null, tunnels: [] })
    expect(tray.calls[0].headers['content-type']).toContain('application/json')

    await validateRouting(null)
    expect(tray.calls[1].body).toBeUndefined()

    await runAction('start')
    expect(tray.calls[2].method).toBe('POST')
    tray.restore()
  })

  it('surfaces the tray message on a non-2xx response', async () => {
    setSecret(TEST_SECRET)
    const tray = installFakeTray({ 'PUT /api/routing': null }, { raw: true, status: 422 })
    globalThis.fetch = (async () =>
      new Response(JSON.stringify({ code: 422, msg: 'the routing configuration is not usable', data: null }), {
        status: 422,
        headers: { 'Content-Type': 'application/json' },
      })) as typeof fetch
    await expect(apiFetch('/api/routing', { method: 'PUT', body: '{}' })).rejects.toMatchObject({
      status: 422,
      message: 'the routing configuration is not usable',
    })
    tray.restore()
  })

  it('distinguishes a lost secret from a configuration problem', async () => {
    setSecret(TEST_SECRET)
    globalThis.fetch = (async () =>
      new Response(JSON.stringify({ code: 401, msg: 'no secret', data: null }), { status: 401 })) as typeof fetch
    const unauthorized = await apiFetch('/api/settings').catch((cause: unknown) => cause)
    expect(unauthorized).toBeInstanceOf(ApiError)
    expect((unauthorized as ApiError).status).toBe(401)

    globalThis.fetch = (async () =>
      new Response(JSON.stringify({ code: 409, msg: 'already running', data: null }), { status: 409 })) as typeof fetch
    const conflict = await apiFetch('/api/actions/start', { method: 'POST' }).catch((cause: unknown) => cause)
    expect((conflict as ApiError).status).toBe(409)
  })

  it('reports an unreachable tray without throwing a TypeError', async () => {
    setSecret(TEST_SECRET)
    globalThis.fetch = (async () => {
      throw new TypeError('Failed to fetch')
    }) as typeof fetch
    await expect(apiFetch('/api/stats')).rejects.toMatchObject({ status: 0 })
  })

  it('rejects a response that is not JSON', async () => {
    setSecret(TEST_SECRET)
    globalThis.fetch = (async () => new Response('<html></html>', { status: 200 })) as typeof fetch
    await expect(apiFetch('/api/settings')).rejects.toThrow(/not JSON/)
  })
})
