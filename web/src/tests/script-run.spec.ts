import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, h, nextTick } from 'vue'
import { createI18n } from 'vue-i18n'
import messages from '../i18n/messages/en-US'
import ScriptRunDialog from '../components/ScriptRunDialog.vue'
import { createAgentExecSession, openAgentExecSocket, type AgentExecTicket } from '../api/agentExec'
import { canRunScript, createOutputDecoder } from '../utils/scriptRun'
import { callsTo, flush, stubApi } from './api-stub'

describe('canRunScript', () => {
  it('shows the action only to administrators on online agents that advertise exec', () => {
    const online = { status: 'online', enabled: true, execCapable: true }
    expect(canRunScript(online, true)).toBe(true)
    expect(canRunScript(online, false)).toBe(false)
    expect(canRunScript({ ...online, execCapable: false }, true)).toBe(false)
    expect(canRunScript({ ...online, execCapable: undefined }, true)).toBe(false)
    expect(canRunScript({ ...online, status: 'offline' }, true)).toBe(false)
    expect(canRunScript({ ...online, enabled: false }, true)).toBe(false)
  })
})

describe('createOutputDecoder', () => {
  it('keeps a multi-byte character that arrives split across chunks intact', () => {
    const decode = createOutputDecoder()
    const bytes = new TextEncoder().encode('中文')
    const first = btoa(String.fromCharCode(...bytes.slice(0, 2)))
    const second = btoa(String.fromCharCode(...bytes.slice(2)))
    expect(decode(first) + decode(second)).toBe('中文')
  })
})

class FakeWebSocket {
  static OPEN = 1
  static last: FakeWebSocket | null = null
  readyState = 0
  sent: string[] = []
  onopen: (() => void) | null = null
  onmessage: ((event: { data: unknown }) => void) | null = null
  onclose: (() => void) | null = null
  onerror: (() => void) | null = null
  url: string
  constructor(url: string) {
    this.url = url
    FakeWebSocket.last = this
  }
  send(data: string) { this.sent.push(data) }
  close() { this.readyState = 3; this.onclose?.() }
}

const ticket: AgentExecTicket = { sessionId: 'sess-1', ticket: 'tk-1', websocketPath: '/ws/agent-exec/sess-1', expiresAt: '2026-11-02T00:00:30Z' }

describe('script run requests and frames', () => {
  beforeEach(() => {
    vi.stubGlobal('WebSocket', FakeWebSocket)
    FakeWebSocket.last = null
  })
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('creates a session with an idempotency key against the agent exec route', async () => {
    const calls = stubApi([{ method: 'POST', path: '/agents/agent-1/exec-sessions', data: ticket }])
    const created = await createAgentExecSession('agent-1', 'key-1')
    expect(created.sessionId).toBe('sess-1')
    expect(callsTo(calls, 'POST', '/agents/agent-1/exec-sessions')[0].headers.get('Idempotency-Key')).toBe('key-1')
    vi.unstubAllGlobals()
  })

  it('sends the run request on open and decodes each frame it receives', () => {
    const frames: unknown[] = []
    openAgentExecSocket(ticket, 'echo hi', 30, (frame) => frames.push(frame), () => {})
    const socket = FakeWebSocket.last!
    expect(socket.url).toContain('/ws/agent-exec/sess-1?ticket=tk-1')
    socket.onopen?.()
    expect(JSON.parse(socket.sent[0])).toEqual({ type: 'run', script: 'echo hi', timeoutSec: 30 })
    socket.onmessage?.({ data: JSON.stringify({ type: 'exit', exitCode: 2, signaled: false, timedOut: false, truncated: false, durationMs: 9 }) })
    socket.onmessage?.({ data: new ArrayBuffer(4) })
    expect(frames).toEqual([{ type: 'exit', exitCode: 2, signaled: false, timedOut: false, truncated: false, durationMs: 9 }])
  })
})

describe('ScriptRunDialog', () => {
  beforeEach(() => {
    document.body.replaceChildren()
    vi.stubGlobal('WebSocket', FakeWebSocket)
    FakeWebSocket.last = null
  })
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('streams stdout and reports the exit code in the output panel', async () => {
    const calls = stubApi([{ method: 'POST', path: '/agents/agent-1/exec-sessions', data: ticket }])
    const host = document.createElement('div')
    document.body.append(host)
    createApp({ render: () => h(ScriptRunDialog, { agent: { id: 'agent-1', name: 'edge-1' } }) })
      .use(createI18n({ legacy: false, locale: 'en-US', messages: { 'en-US': messages } }))
      .mount(host)
    await nextTick()

    const textarea = document.querySelector<HTMLTextAreaElement>('#script-run-input')!
    textarea.value = 'echo hi'
    textarea.dispatchEvent(new Event('input', { bubbles: true }))
    await nextTick()
    document.querySelector<HTMLButtonElement>('[data-test="script-run-submit"]')!.click()
    await flush()
    expect(callsTo(calls, 'POST', '/agents/agent-1/exec-sessions')).toHaveLength(1)

    const socket = FakeWebSocket.last!
    socket.onopen?.()
    socket.onmessage?.({ data: JSON.stringify({ type: 'stdout', data: btoa('hi\n') }) })
    socket.onmessage?.({ data: JSON.stringify({ type: 'exit', exitCode: 0, signaled: false, timedOut: false, truncated: false, durationMs: 4 }) })
    await nextTick()
    expect(document.querySelector('[data-test="script-output"]')!.textContent).toContain('hi')
    expect(document.querySelector('[data-test="script-status"]')!.textContent).toContain('exit code 0')
  })
})
