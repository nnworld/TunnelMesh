import type {
  AboutView,
  AgentRef,
  RoutingSaveResult,
  RoutingUpdate,
  RoutingView,
  SettingsUpdate,
  SettingsView,
  StatsView,
  ValidationReport,
} from './types'

/**
 * The tray serves this page from 127.0.0.1 on a random port and hands the launch secret
 * to the webview once, in the query string of the initial URL.
 *
 * Every API call repeats it in a header. The secret is the only thing between another
 * process on this machine and the ability to read or rewrite the client token, so it is
 * kept in module state, never in localStorage, and scrubbed from the URL as soon as it
 * has been read.
 */
const SECRET_QUERY_KEY = 'secret'
const SECRET_HEADER = 'X-Tray-Secret'

let secret = ''

export function readSecretFromLocation(search: string = window.location.search): string {
  const parsed = new URLSearchParams(search)
  const value = parsed.get(SECRET_QUERY_KEY) ?? ''
  if (value) {
    parsed.delete(SECRET_QUERY_KEY)
    const rest = parsed.toString()
    const cleaned = `${window.location.pathname}${rest ? `?${rest}` : ''}${window.location.hash}`
    // Leaving the secret in the address bar would put it in window titles, screenshots
    // and any future navigation log.
    window.history.replaceState(null, '', cleaned)
  }
  return value
}

export function setSecret(value: string): void {
  secret = value
}

export function getSecret(): string {
  return secret
}

/** Captures the secret from the launch URL. Called once during bootstrap. */
export function initSecret(search?: string): string {
  const value = readSecretFromLocation(search)
  if (value) setSecret(value)
  return secret
}

export class ApiError extends Error {
  readonly status: number
  constructor(status: number, message: string) {
    super(message)
    this.name = 'ApiError'
    this.status = status
  }
}

interface Envelope<T> {
  code: number
  msg: string
  data: T
}

const MAX_ERROR_MESSAGE = 2000

export async function apiFetch<T>(path: string, init: RequestInit = {}): Promise<T> {
  const headers = new Headers(init.headers)
  if (secret) headers.set(SECRET_HEADER, secret)
  if (init.body && !headers.has('Content-Type')) headers.set('Content-Type', 'application/json')

  let response: Response
  try {
    response = await fetch(path, { ...init, headers })
  } catch (cause) {
    throw new ApiError(0, `cannot reach the tray: ${String(cause)}`)
  }

  const text = await response.text()
  let envelope: Envelope<T> | null = null
  if (text) {
    try {
      envelope = JSON.parse(text) as Envelope<T>
    } catch {
      envelope = null
    }
  }
  if (!response.ok) {
    const message = (envelope?.msg ?? text ?? `HTTP ${response.status}`).slice(0, MAX_ERROR_MESSAGE)
    throw new ApiError(response.status, message)
  }
  if (!envelope) {
    throw new ApiError(response.status, 'the tray returned a response that is not JSON')
  }
  return envelope.data
}

export const getSettings = () => apiFetch<SettingsView>('/api/settings')
export const saveSettings = (update: SettingsUpdate) =>
  apiFetch<SettingsView>('/api/settings', { method: 'PUT', body: JSON.stringify(update) })

export const getRouting = () => apiFetch<RoutingView>('/api/routing')
export const saveRouting = (update: RoutingUpdate) =>
  apiFetch<RoutingSaveResult>('/api/routing', { method: 'PUT', body: JSON.stringify(update) })

export const listAgents = (serverUrl?: string) => {
  const query = serverUrl ? `?serverUrl=${encodeURIComponent(serverUrl)}` : ''
  return apiFetch<AgentRef[] | null>(`/api/agents${query}`)
}

export const validateRouting = (update: RoutingUpdate | null) =>
  apiFetch<ValidationReport>('/api/validate', {
    method: 'POST',
    body: update ? JSON.stringify(update) : undefined,
  })

export const getStats = () => apiFetch<StatsView>('/api/stats')
export const getAbout = () => apiFetch<AboutView>('/api/about')

export const runAction = (action: string) =>
  apiFetch<Record<string, boolean>>(`/api/actions/${action}`, { method: 'POST' })
