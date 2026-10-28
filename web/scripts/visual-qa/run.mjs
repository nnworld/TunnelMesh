// Visual QA harness: boots a real local Server with seeded data, then screenshots
// the admin console in headless Chrome so a redesign can be judged by eye.
//
//   cd web && npm run build && node scripts/visual-qa/run.mjs --locale en-US
//
// Output goes to $TM_QA_OUT (default: <tmp>/tunnelmesh-visual-qa/shots), never into
// the repository. It reuses the process helpers of the WebSSH e2e harness and
// leaves them untouched.
import { spawn, execFileSync } from 'node:child_process'
import crypto from 'node:crypto'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { launchChrome, openTab } from '../../../test/e2e/webssh/lib/cdp.mjs'
import { findChrome } from '../../../test/e2e/webssh/lib/stack.mjs'
import { SCREENSHOT_MATRIX, VIEWPORTS, parseArgs, planSeed } from './seed.mjs'

const here = path.dirname(fileURLToPath(import.meta.url))
const repoRoot = path.resolve(here, '..', '..', '..')
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))
const log = (...args) => console.log('[visual-qa]', ...args)

const options = parseArgs(process.argv.slice(2))
const workDir = process.env.TM_QA_DIR || path.join(os.tmpdir(), 'tunnelmesh-visual-qa')
const outDir = process.env.TM_QA_OUT || path.join(workDir, 'shots')
const port = Number(process.env.TM_QA_PORT || 18299)
const cdpPort = Number(process.env.TM_QA_CDP_PORT || 19433)
const origin = process.env.TM_QA_ORIGIN || `http://127.0.0.1:${port}`
const binDir = path.join(workDir, 'bin')
const children = []

function track(child) {
  children.push(child)
  return child
}

async function stopAll() {
  for (const child of children) child.kill()
  await sleep(300)
}

fs.rmSync(path.join(workDir, 'logs'), { recursive: true, force: true })
fs.rmSync(path.join(workDir, 'chrome-profile'), { recursive: true, force: true })
fs.rmSync(path.join(workDir, 'server.db'), { force: true })
for (const dir of [binDir, path.join(workDir, 'logs'), outDir]) fs.mkdirSync(dir, { recursive: true })

if (!options.skipBuild) {
  if (!fs.existsSync(path.join(repoRoot, 'internal/server/web_dist/index.html'))) {
    throw new Error('internal/server/web_dist is empty; run `cd web && npm run build` first')
  }
  for (const name of ['tunnelmesh-server', 'tunnelmesh-agent']) {
    log('building', name)
    execFileSync('go', ['build', '-o', path.join(binDir, name), `./cmd/${name}`], { cwd: repoRoot, stdio: ['ignore', 'ignore', 'inherit'] })
  }
}

const serverEnv = {
  ...process.env,
  TUNNELMESH_MODE: 'local',
  TUNNELMESH_STORAGE_DRIVER: 'sqlite',
  TUNNELMESH_STORAGE_SQLITE_PATH: path.join(workDir, 'server.db'),
  TUNNELMESH_STORAGE_AUTO_INIT: 'true',
  TUNNELMESH_SERVER_HTTP_ADDR: `127.0.0.1:${port}`,
  TUNNELMESH_TOKEN_ENCRYPTION_KEY: crypto.randomBytes(32).toString('base64'),
  TUNNELMESH_TOKEN_ENCRYPTION_KEY_ID: 'qa-key',
}

try {
  const serverLog = fs.createWriteStream(path.join(workDir, 'logs', 'server.log'))
  const server = track(spawn(path.join(binDir, 'tunnelmesh-server'), ['run'], { env: serverEnv, stdio: ['ignore', 'pipe', 'pipe'] }))
  server.stdout.pipe(serverLog)
  server.stderr.pipe(serverLog)
  const serverOrigin = `http://127.0.0.1:${port}`
  for (let i = 0; ; i++) {
    try {
      await fetch(`${serverOrigin}/api/v1/health`)
      break
    } catch {
      if (i >= 60) throw new Error(`server did not open port ${port}; see ${path.join(workDir, 'logs', 'server.log')}`)
      await sleep(500)
    }
  }
  const bootstrap = execFileSync(path.join(binDir, 'tunnelmesh-server'), ['admin', 'bootstrap'], { env: serverEnv, encoding: 'utf8' })
  const username = /username[:\s]+(\S+)/i.exec(bootstrap)?.[1]
  const password = /password[:\s]+(\S+)/i.exec(bootstrap)?.[1]
  if (!username || !password) throw new Error(`cannot parse bootstrap credentials:\n${bootstrap}`)

  let token = ''
  const api = async (urlPath, init = {}) => {
    const res = await fetch(serverOrigin + urlPath, {
      ...init,
      headers: { 'Content-Type': 'application/json', ...(token ? { Authorization: `Bearer ${token}` } : {}), ...(init.headers || {}) },
    })
    const body = await res.json().catch(() => null)
    if (!res.ok) throw new Error(`${urlPath} -> ${res.status} ${JSON.stringify(body)}`)
    return body.data ?? body
  }
  token = (await api('/api/v1/auth/login', { method: 'POST', body: JSON.stringify({ username, password }) })).token

  // Seed: every agent gets a token, and the planned-online ones really connect, so
  // the LED states in the screenshots come from the Server, not from fixtures.
  const plan = planSeed({ agents: options.agents, online: options.online })
  let index = 0
  for (const planned of plan.agents) {
    index += 1
    // A token can only be minted for an enabled agent, so every agent starts
    // enabled and the planned-disabled ones are switched off afterwards.
    const agent = await api('/api/v1/agents', { method: 'POST', body: JSON.stringify({ name: planned.name, enabled: true }) })
    const agentToken = await api('/api/v1/tokens', {
      method: 'POST',
      headers: { 'Idempotency-Key': `qa-token-${index}` },
      body: JSON.stringify({ type: 'agent', agentId: agent.id }),
    })
    if (!planned.enabled) {
      await api(`/api/v1/agents/${agent.id}`, { method: 'PATCH', body: JSON.stringify({ enabled: false }) })
    }
    if (!planned.online) continue
    const agentLog = fs.createWriteStream(path.join(workDir, 'logs', `agent-${index}.log`))
    const proc = track(spawn(path.join(binDir, 'tunnelmesh-agent'), ['run'], {
      env: {
        ...process.env,
        TUNNELMESH_MODE: 'local',
        TUNNELMESH_AGENT_UPDATE_ENABLED: 'false',
        TUNNELMESH_AGENT_UPDATE_SUPERVISE: 'false',
        TUNNELMESH_AGENT_SERVER_URL: `ws://127.0.0.1:${port}/ws/agent`,
        TUNNELMESH_AGENT_ID: agent.id,
        TUNNELMESH_AGENT_TOKEN: agentToken.secret,
        TUNNELMESH_AGENT_INSTANCE_ID: `qa-instance-${index}`,
      },
      stdio: ['ignore', 'pipe', 'pipe'],
    }))
    proc.stdout.pipe(agentLog)
    proc.stderr.pipe(agentLog)
  }
  log(`seeded ${plan.agents.length} agents, waiting for ${options.online} to connect`)
  for (let i = 0; i < 60; i++) {
    const page = await api('/api/v1/agents?status=online&limit=100')
    if ((page.items || []).length >= options.online) break
    if (i === 59) log('warning: fewer agents came online than planned')
    await sleep(500)
  }

  const chrome = track(await launchChrome({
    binary: findChrome({ chromeBinary: process.env.TM_QA_CHROME || '' }),
    cdpPort, profileDir: path.join(workDir, 'chrome-profile'),
  }))
  void chrome

  const wanted = options.only ? SCREENSHOT_MATRIX.filter((s) => s.page === options.only) : SCREENSHOT_MATRIX
  for (const shot of wanted) {
    const viewport = VIEWPORTS[shot.viewport]
    const tab = await openTab({
      cdpPort, origin, url: shot.path, token: shot.auth ? token : '',
      downloadsDir: path.join(workDir, 'downloads'), screenshotPrefix: path.join(outDir, `${options.locale}`),
      width: viewport.width, height: viewport.height,
    })
    // Preferences are read at startup, so they are planted before the document loads.
    await tab.send('Page.addScriptToEvaluateOnNewDocument', {
      source: `localStorage.setItem('tunnelmesh_theme', ${JSON.stringify(shot.theme)}); localStorage.setItem('tunnelmesh_locale', ${JSON.stringify(options.locale)})`,
    })
    await tab.send('Page.navigate', { url: origin + shot.path })
    await tab.wait(`document.querySelector('#app') && document.querySelector('#app').children.length > 0`, 20000)
    await sleep(1200)
    await tab.shot(`${shot.page}-${shot.theme}-${shot.viewport}`)
    if (tab.failures.length) log(`console problems on ${shot.page}/${shot.theme}/${shot.viewport}:`, tab.failures.slice(0, 3).join(' | '))
    await tab.close()
  }
  log(`wrote ${wanted.length} screenshots to ${outDir}`)
  if (options.keep) {
    log(`leaving the Server running at ${serverOrigin} (login ${username} / ${password}); press Ctrl+C to stop`)
    await new Promise(() => {})
  }
} finally {
  if (!options.keep) await stopAll()
}
process.exit(0)
