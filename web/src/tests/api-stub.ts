import { nextTick } from 'vue'
import { vi } from 'vitest'

// Shared harness for mounted-view specs. Every request a view makes has to be
// declared up front: an undeclared path throws instead of resolving to
// `undefined`, so a missing or renamed endpoint fails the test that exercises
// it rather than hiding behind an empty render.
export type ApiStub = {
  method?: string
  path: string
  status?: number
  data?: unknown
  // Consecutive matching calls consume the queue in order; its last entry
  // repeats, which models "list, mutate, list again" without extra stubs.
  sequence?: unknown[]
  // Per-call statuses, consumed with the same semantics as `sequence`. A retry
  // path cannot be tested while one stub has one status: "fail, then succeed"
  // is exactly the case where a caller has to reuse its idempotency key.
  statuses?: number[]
}
export type ApiCall = { method: string; path: string; headers: Headers; body?: Record<string, unknown> }

export function stubApi(stubs: ApiStub[]): ApiCall[] {
  const calls: ApiCall[] = []
  const queues = stubs.map(stub => [...(stub.sequence ?? [])])
  const statusQueues = stubs.map(stub => [...(stub.statuses ?? [])])
  vi.stubGlobal('fetch', vi.fn(async (input: unknown, init?: RequestInit) => {
    const method = init?.method ?? 'GET'
    const path = String(input).replace('/api/v1', '')
    calls.push({ method, path, headers: new Headers(init?.headers), ...(init?.body ? { body: JSON.parse(String(init.body)) as Record<string, unknown> } : {}) })
    const index = stubs.findIndex(stub => (stub.method ?? 'GET') === method && (path === stub.path || path.startsWith(`${stub.path}?`)))
    if (index < 0) throw new Error(`unexpected request ${method} ${path}`)
    const stub = stubs[index] as ApiStub
    const queue = queues[index] as unknown[]
    const statuses = statusQueues[index] as number[]
    const status = statuses.length > 1 ? statuses.shift()! : statuses.length === 1 ? statuses[0]! : stub.status ?? 200
    const data = queue.length > 1 ? queue.shift() : queue.length === 1 ? queue[0] : stub.data
    return {
      ok: status < 300, status, statusText: String(status), headers: new Headers(),
      json: async () => ({ code: status, msg: status < 400 ? 'ok' : 'error', data }),
    }
  }))
  return calls
}

export function callsTo(calls: ApiCall[], method: string, path: string) {
  return calls.filter(call => call.method === method && (call.path === path || call.path.startsWith(`${path}?`)))
}

export async function flush() {
  await nextTick()
  await new Promise(resolve => setTimeout(resolve, 0))
  await nextTick()
}

export function click(selector: string, root: HTMLElement | Document) {
  const element = root.querySelector(selector)
  if (!element) throw new Error(`missing ${selector}`)
  element.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }))
  return flush()
}

// el-input forwards fall-through attributes to its inner <input>; the helper
// also accepts a wrapper element so specs never depend on that detail.
export function inputOf(selector: string, root: HTMLElement | Document) {
  const found = root.querySelector(selector)
  if (!found) throw new Error(`missing ${selector}`)
  const input = (found instanceof HTMLInputElement ? found : found.querySelector('input')) as HTMLInputElement | null
  if (!input) throw new Error(`missing input inside ${selector}`)
  return input
}

export function setValue(selector: string, root: HTMLElement | Document, value: string) {
  const input = inputOf(selector, root)
  input.value = value
  input.dispatchEvent(new Event('input', { bubbles: true }))
  return flush()
}

// el-switch and el-checkbox both emit on the native change event, and neither
// reads the DOM checked state, so one change dispatch is exactly one toggle.
export function check(selector: string, root: HTMLElement | Document, checked = true) {
  const input = inputOf(selector, root)
  input.checked = checked
  input.dispatchEvent(new Event('change', { bubbles: true }))
  return flush()
}
