// Pure helpers for the visual QA harness: what to seed and what to screenshot.
// They live apart from run.mjs so the plan can be unit-tested without a browser.

const SITES = ['hk', 'sg', 'tyo', 'sha', 'fra', 'lon', 'nyc', 'sfo', 'syd', 'bom']
const ROLES = ['edge', 'gw', 'nas', 'build', 'lab', 'ci', 'db', 'cam']

/** agentNames returns n unique, deterministic, DNS-style agent names. */
export function agentNames(n) {
  const names = []
  for (let i = 0; i < n; i++) {
    const role = ROLES[i % ROLES.length]
    const site = SITES[Math.floor(i / ROLES.length) % SITES.length]
    names.push(`${role}-${site}-${String(i + 1).padStart(2, '0')}`)
  }
  return names
}

/**
 * planSeed decides which agents exist and which of them connect for real.
 * Roughly one in nine is disabled, so the panel shows all three LED states, and
 * a disabled agent is never online.
 */
export function planSeed({ agents, online }) {
  const planned = agentNames(agents).map((name, index) => ({
    name,
    enabled: (index + 1) % 9 !== 0,
    online: false,
  }))
  let remaining = online
  for (const agent of planned) {
    if (remaining <= 0) break
    // Leave every other enabled agent offline so offline-but-enabled is visible.
    if (agent.enabled && planned.indexOf(agent) % 2 === 0) {
      agent.online = true
      remaining -= 1
    }
  }
  for (const agent of planned) {
    if (remaining <= 0) break
    if (agent.enabled && !agent.online) {
      agent.online = true
      remaining -= 1
    }
  }
  return { agents: planned }
}

export const VIEWPORTS = {
  desktop: { width: 1521, height: 900 },
  mobile: { width: 390, height: 844 },
}

const PAGES = [
  { page: 'login', path: '/login', auth: false },
  { page: 'dashboard', path: '/', auth: true },
  { page: 'agents', path: '/agents', auth: true },
  { page: 'tokens', path: '/tokens', auth: true },
  { page: 'audit-logs', path: '/audit-logs', auth: true },
  { page: 'security', path: '/account/security', auth: true },
]

/** SCREENSHOT_MATRIX is every page in both themes and both viewports. */
export const SCREENSHOT_MATRIX = PAGES.flatMap((p) =>
  ['light', 'dark'].flatMap((theme) => ['desktop', 'mobile'].map((viewport) => ({ ...p, theme, viewport }))),
)

const LOCALES = ['zh-CN', 'en-US']

function intFlag(name, value) {
  const parsed = Number(value)
  if (!Number.isInteger(parsed) || parsed < 0) throw new Error(`${name} needs a non-negative integer, got "${value}"`)
  return parsed
}

/** parseArgs reads the harness command line. */
export function parseArgs(argv) {
  const options = { locale: 'zh-CN', agents: 40, online: 8, keep: false, skipBuild: false, only: '' }
  for (let i = 0; i < argv.length; i++) {
    const flag = argv[i]
    const next = () => {
      if (i + 1 >= argv.length) throw new Error(`${flag} needs a value`)
      return argv[++i]
    }
    switch (flag) {
      case '--locale': {
        const value = next()
        if (!LOCALES.includes(value)) throw new Error(`--locale must be one of ${LOCALES.join(', ')}`)
        options.locale = value
        break
      }
      case '--agents': options.agents = intFlag('--agents', next()); break
      case '--online': options.online = intFlag('--online', next()); break
      case '--only': options.only = next(); break
      case '--keep': options.keep = true; break
      case '--skip-build': options.skipBuild = true; break
      default: throw new Error(`unknown argument ${flag}`)
    }
  }
  return options
}
