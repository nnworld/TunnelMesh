// Mirror of the Go view types in internal/tray. The two must stay in step: the tray
// rejects unknown JSON keys, so a field added here without the Go side is a 400.

export type LanguagePreference = 'system' | 'zh-CN' | 'en-US'
export type ThemePreference = 'system' | 'light' | 'dark'
export type ClientMode = 'local' | 'cluster'
export type TunnelProtocol = 'tcp' | 'udp' | 'http' | 'socks5' | 'http-proxy'

export interface SettingsView {
  language: LanguagePreference
  theme: ThemePreference
  configDir: string
  clientConfigPath: string
  prefsPath: string
  lockPath: string
  launchAtLogin: boolean
  launchAtLoginSupported: boolean
  launchAtLoginError?: string
  minimizeToTray: boolean
  /** Menu-bar quick panel. Off by default: the left click keeps showing the menu. */
  quickPanel: boolean
  /** "macos", "windows", or absent for a shell neither word describes. */
  platform?: string
  languages: LanguagePreference[]
  themes: ThemePreference[]
  modes: ClientMode[]
  protocols: TunnelProtocol[]
}

export interface SettingsUpdate {
  language?: LanguagePreference | ''
  theme?: ThemePreference | ''
  configDir?: string
  launchAtLogin?: boolean
  minimizeToTray?: boolean
  quickPanel?: boolean
}

export interface TunnelView {
  name: string
  protocol: TunnelProtocol | ''
  listen: string
  agentId: string
  targetHost: string
  targetPort: number
  authMode?: string
  allowRemote: boolean
  authUrl?: string
}

export interface RoutingView {
  mode: ClientMode
  serverUrl: string
  tokenPresent: boolean
  tunnels: TunnelView[]
  configPath: string
  running: boolean
  protocols: TunnelProtocol[]
}

export interface RoutingUpdate {
  mode: ClientMode | ''
  serverUrl: string
  /** null keeps the stored token, '' clears it, anything else replaces it. */
  token: string | null
  tunnels: TunnelView[]
}

export interface RoutingSaveResult {
  routing: RoutingView
  restarted: boolean
  runtimeError?: string
}

export interface AgentRef {
  id: string
  name: string
  online: boolean
}

export type CheckStatus = 'passed' | 'failed' | 'warning' | 'skipped'

export interface CheckResult {
  id: string
  status: CheckStatus
  message?: string
  tunnel?: string
  index?: number
}

export interface ValidationReport {
  valid: boolean
  checkedAt: string
  checks: CheckResult[]
  agents?: AgentRef[]
}

export interface TunnelStat {
  name: string
  protocol: string
  listen: string
  agentId: string
  target?: string
  state: string
  lastError?: string
}

export interface AgentStat {
  agentId: string
  openSlots: number
  readySessions: number
  activeStreams: number
}

export interface StatTotals {
  tunnels: number
  listening: number
  failed: number
  agents: number
  openSlots: number
  readySessions: number
  activeStreams: number
  reconnects: number
  bytesInbound: number
}

export interface StatsView {
  running: boolean
  startedAt?: string
  uptimeSeconds: number
  serverUrl?: string
  serverReachable: boolean
  serverProbe?: string
  connected: boolean
  lockBlocked: boolean
  runtimeError?: string
  tunnels: TunnelStat[]
  agents: AgentStat[]
  totals: StatTotals
  collectedAt: string
}

export interface SystemInfo {
  goos: string
  arch: string
  osVersion?: string
  /** The web view that draws this window, or "browser" when no embedded one was found. */
  renderer?: string
  /** Runtime version, or the reason there is not one. */
  rendererDetail?: string
}

export interface AboutView {
  version: string
  commit: string
  buildTime: string
  system: SystemInfo
  configDir: string
  clientConfigPath: string
  prefsPath: string
  lockPath: string
  instanceId?: string
  instanceIdPath?: string
  mode: ClientMode
  serverUrl?: string
  tunnelCount: number
  tokenPresent: boolean
  running: boolean
  websiteUrl: string
  releasesUrl: string
  docsUrl: string
  license: string
}
