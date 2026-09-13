// Process stack for the WebSSH end-to-end run: build the three product binaries
// from this repository, start a throwaway SSH host, wire an Agent to a local
// (SQLite) Server, and create the admin data the browser scenario needs.
//
// Everything machine-specific is resolved here so `run.mjs` stays a readable
// scenario script.
import { spawn, execFileSync } from 'node:child_process'
import crypto from 'node:crypto'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const here = path.dirname(fileURLToPath(import.meta.url))
export const repoRoot = path.resolve(here, '..', '..', '..', '..')

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))
const number = (value, fallback) => (Number.isFinite(Number(value)) && value !== '' && value != null ? Number(value) : fallback)

/** resolveConfig merges environment overrides onto safe local defaults. */
export function resolveConfig(env = process.env) {
  const workDir = env.TM_E2E_DIR || path.join(os.tmpdir(), 'tunnelmesh-webssh-e2e')
  const port = number(env.TM_E2E_PORT, 18199)
  const sshPort = number(env.TM_E2E_SSH_PORT, 2222)
  const cdpPort = number(env.TM_E2E_CDP_PORT, 19333)
  return {
    repoRoot: env.TM_E2E_REPO || repoRoot,
    workDir,
    binDir: env.TM_E2E_BIN_DIR || path.join(workDir, 'bin'),
    logDir: path.join(workDir, 'logs'),
    remoteFsDir: path.join(workDir, 'remote-fs'),
    uploadDir: path.join(workDir, 'uploads'),
    downloadDir: path.join(workDir, 'downloads'),
    profileDir: path.join(workDir, 'chrome-profile'),
    shellHomeDir: path.join(workDir, 'shell-home'),
    sqlitePath: path.join(workDir, 'server.db'),
    port,
    sshPort,
    cdpPort,
    origin: `http://127.0.0.1:${port}`,
    chromeBinary: env.TM_E2E_CHROME || '',
    downloadBytes: number(env.TM_E2E_DOWNLOAD_BYTES, 4 * 1024 * 1024),
    uploadBytes: number(env.TM_E2E_UPLOAD_BYTES, Math.round(1.5 * 1024 * 1024)),
    sftpUploadBytes: number(env.TM_E2E_SFTP_UPLOAD_BYTES, 3 * 1024 * 1024),
    skipBuild: env.TM_E2E_SKIP_BUILD === '1',
    screenshotPrefix: path.join(workDir, 'shot'),
  }
}

/** prepareWorkDir resets per-run state but keeps the built binaries and fixtures. */
export function prepareWorkDir(config) {
  fs.rmSync(config.logDir, { recursive: true, force: true })
  fs.rmSync(config.downloadDir, { recursive: true, force: true })
  fs.rmSync(config.profileDir, { recursive: true, force: true })
  fs.rmSync(config.sqlitePath, { force: true })
  for (const dir of [config.binDir, config.logDir, config.downloadDir, config.remoteFsDir, config.uploadDir, config.shellHomeDir]) {
    fs.mkdirSync(dir, { recursive: true })
  }
  // The rz destination lives next to the download fixture, so clear everything
  // but the fixture: a stale upload.bin from a previous run would make the
  // byte-for-byte assertion pass without transferring anything.
  for (const entry of fs.readdirSync(config.remoteFsDir)) {
    if (entry === 'payload.bin') continue
    fs.rmSync(path.join(config.remoteFsDir, entry), { recursive: true, force: true })
  }
  // A deterministic prompt keeps the terminal assertions independent of the
  // developer's own shell rc. HOME is redirected too, so bash and zsh both read
  // the file below instead of the real user profile.
  fs.writeFileSync(path.join(config.shellHomeDir, '.zshrc'), "PROMPT='tmhost$ '\nRPROMPT=''\nexport LC_ALL=C\n")
  fs.writeFileSync(path.join(config.shellHomeDir, '.bashrc'), "PS1='tmhost$ '\nexport LC_ALL=C\n")
}

/** buildBinaries compiles the three product binaries plus the throwaway SSH host. */
export function buildBinaries(config, log) {
  const targets = [
    ['tunnelmesh-server', './cmd/tunnelmesh-server', config.repoRoot],
    ['tunnelmesh-agent', './cmd/tunnelmesh-agent', config.repoRoot],
    ['tunnelmesh-client', './cmd/tunnelmesh-client', config.repoRoot],
    ['sshhost', '.', path.join(here, '..', 'sshhost')],
  ]
  const built = {}
  for (const [name, pkg, cwd] of targets) {
    if (config.skipBuild) {
      const existing = path.join(config.binDir, name)
      if (!fs.existsSync(existing)) throw new Error(`TM_E2E_SKIP_BUILD=1 but ${existing} is missing`)
      built[name] = existing
      continue
    }
    log(`building ${name}`)
    execFileSync('go', ['build', '-o', path.join(config.binDir, name), pkg], { cwd, stdio: ['ignore', 'ignore', 'inherit'] })
    built[name] = path.join(config.binDir, name)
  }
  return built
}

/**
 * findChrome resolves a Chrome/Chromium binary. Headless CDP is the only
 * browser feature the harness needs, so any recent Chrome build works.
 */
export function findChrome(config) {
  if (config.chromeBinary) {
    if (!fs.existsSync(config.chromeBinary)) throw new Error(`TM_E2E_CHROME=${config.chromeBinary} does not exist`)
    return config.chromeBinary
  }
  const candidates = process.platform === 'darwin'
    ? [
      '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
      '/Applications/Chromium.app/Contents/MacOS/Chromium',
      '/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge',
    ]
    : ['google-chrome', 'google-chrome-stable', 'chromium', 'chromium-browser']
  for (const candidate of candidates) {
    if (candidate.startsWith('/')) {
      if (fs.existsSync(candidate)) return candidate
      continue
    }
    try {
      return execFileSync('which', [candidate], { encoding: 'utf8' }).trim()
    } catch {
      // Not installed; try the next candidate.
    }
  }
  throw new Error('no Chrome/Chromium binary found; set TM_E2E_CHROME=/path/to/chrome')
}

/**
 * findLrzsz locates sz/rz. The ZMODEM checks need the real lrzsz on the SSH
 * host; when it is absent the caller skips those checks instead of failing, so
 * the rest of the scenario stays usable on a bare machine.
 */
export function findLrzsz() {
  const extra = ['/opt/homebrew/bin', '/usr/local/bin', '/usr/bin', '/bin']
  const searchPath = [...(process.env.PATH || '').split(path.delimiter), ...extra]
  for (const tool of ['sz', 'rz']) {
    const found = searchPath.map((dir) => path.join(dir, tool)).find((file) => {
      try {
        fs.accessSync(file, fs.constants.X_OK)
        return true
      } catch {
        return false
      }
    })
    if (!found) return { available: false, missing: tool, extraPath: extra.join(path.delimiter) }
  }
  return { available: true, extraPath: extra.join(path.delimiter) }
}

/** startStack boots Server, the throwaway SSH host and the Agent, then seeds admin data. */
export async function startStack(config, binaries, log) {
  const children = []
  const openLog = (name) => fs.createWriteStream(path.join(config.logDir, name))

  const track = (child, name) => {
    children.push({ child, name })
    return child
  }

  const serverEnv = {
    ...process.env,
    TUNNELMESH_MODE: 'local',
    TUNNELMESH_STORAGE_DRIVER: 'sqlite',
    TUNNELMESH_STORAGE_SQLITE_PATH: config.sqlitePath,
    TUNNELMESH_STORAGE_AUTO_INIT: 'true',
    TUNNELMESH_SERVER_HTTP_ADDR: `127.0.0.1:${config.port}`,
    TUNNELMESH_SERVER_DYNAMIC_SUFFIX: 'apps.local',
    // Credential secrets (WebSSH auto-authentication) are AES-GCM sealed, so the
    // Server refuses to start that feature without a key. Standard base64 or hex
    // only; base64url is rejected by decodeSecretKey.
    TUNNELMESH_TOKEN_ENCRYPTION_KEY: crypto.randomBytes(32).toString('base64'),
    TUNNELMESH_TOKEN_ENCRYPTION_KEY_ID: 'e2e-key',
  }

  const serverLog = openLog('server.log')
  const server = track(spawn(binaries['tunnelmesh-server'], ['run'], { env: serverEnv, stdio: ['ignore', 'pipe', 'pipe'] }), 'server')
  server.stdout.pipe(serverLog)
  server.stderr.pipe(serverLog)

  for (let i = 0; i < 60; i++) {
    try {
      await fetch(`${config.origin}/api/v1/health`)
      break
    } catch {
      await sleep(500)
    }
    if (i === 59) throw new Error(`server did not open port ${config.port}; see ${path.join(config.logDir, 'server.log')}`)
  }
  log('server up')

  // First boot has no admin, so the harness uses the same recovery path an
  // operator would: `admin bootstrap` prints one-time credentials.
  const bootstrap = execFileSync(binaries['tunnelmesh-server'], ['admin', 'bootstrap'], { env: serverEnv, encoding: 'utf8' })
  fs.writeFileSync(path.join(config.logDir, 'bootstrap.txt'), bootstrap)
  const username = /username[:\s]+(\S+)/i.exec(bootstrap)?.[1]
  const password = /password[:\s]+(\S+)/i.exec(bootstrap)?.[1]
  if (!username || !password) throw new Error(`cannot parse bootstrap credentials:\n${bootstrap}`)

  let token = ''
  const api = async (urlPath, options = {}) => {
    const res = await fetch(config.origin + urlPath, {
      ...options,
      headers: {
        'Content-Type': 'application/json',
        ...(token ? { Authorization: `Bearer ${token}` } : {}),
        ...(options.headers || {}),
      },
    })
    const body = await res.json().catch(() => null)
    if (!res.ok) throw new Error(`${urlPath} -> ${res.status} ${JSON.stringify(body)}`)
    return body.data ?? body
  }
  token = (await api('/api/v1/auth/login', { method: 'POST', body: JSON.stringify({ username, password }) })).token
  log('logged in as', username)

  const sshLog = openLog('sshhost.log')
  const sshhost = track(spawn(binaries.sshhost, [], {
    env: {
      ...process.env,
      TM_SSHHOST_CWD: config.remoteFsDir,
      TM_SSHHOST_LISTEN: `127.0.0.1:${config.sshPort}`,
      // lrzsz is commonly installed outside the default PATH; harmless when the
      // directory does not exist.
      TM_SSHHOST_EXTRA_PATH: '/opt/homebrew/bin:/usr/local/bin',
      // The SFTP root is the harness temp dir, so the upload check needs writes.
      TM_SSHHOST_SFTP_READONLY: '0',
      HOME: config.shellHomeDir,
      ZDOTDIR: config.shellHomeDir,
    },
    stdio: ['ignore', 'pipe', 'pipe'],
  }), 'sshhost')
  sshhost.stdout.pipe(sshLog)
  sshhost.stderr.pipe(sshLog)

  const agent = await api('/api/v1/agents', { method: 'POST', body: JSON.stringify({ name: 'e2e-agent', enabled: true }) })
  const agentToken = await api('/api/v1/tokens', {
    method: 'POST',
    headers: { 'Idempotency-Key': 'e2e-agent-token-1' },
    body: JSON.stringify({ type: 'agent', agentId: agent.id }),
  })
  // The policy is the security boundary under test: the Agent may only reach the
  // throwaway SSH host, never the rest of the machine.
  await api(`/api/v1/agents/${agent.id}/policies`, {
    method: 'POST',
    body: JSON.stringify({
      targetHost: '*',
      targetPort: 0,
      protocol: 'tcp',
      allowedCIDRs: ['127.0.0.1/32'],
      allowedPorts: [config.sshPort],
    }),
  })

  const agentLog = openLog('agent.log')
  const agentProc = track(spawn(binaries['tunnelmesh-agent'], ['run'], {
    env: {
      ...process.env,
      TUNNELMESH_MODE: 'local',
      TUNNELMESH_AGENT_SERVER_URL: `ws://127.0.0.1:${config.port}/ws/agent`,
      TUNNELMESH_AGENT_ID: agent.id,
      TUNNELMESH_AGENT_TOKEN: agentToken.secret,
      TUNNELMESH_AGENT_INSTANCE_ID: 'e2e-instance',
    },
    stdio: ['ignore', 'pipe', 'pipe'],
  }), 'agent')
  agentProc.stdout.pipe(agentLog)
  agentProc.stderr.pipe(agentLog)

  // A password credential with a stored secret is what enables auto-auth.
  const credential = await api('/api/v1/credentials', {
    method: 'POST',
    headers: { 'Idempotency-Key': 'e2e-cred-password' },
    body: JSON.stringify({ name: 'e2e-password', type: 'password', enabled: true, secret: { password: 'tmpass' } }),
  })
  if (!credential.hasSecret) throw new Error(`credential was created without a secret: ${JSON.stringify(credential)}`)

  const remoteServer = { host: '127.0.0.1', port: config.sshPort, defaultUsername: 'tmuser', agentId: agent.id, enabled: true }
  const autoAuthServer = await api('/api/v1/remote-servers', {
    method: 'POST',
    headers: { 'Idempotency-Key': 'e2e-remote-1' },
    body: JSON.stringify({ ...remoteServer, name: 'e2e-host', credentialId: credential.id }),
  })
  const manualServer = await api('/api/v1/remote-servers', {
    method: 'POST',
    headers: { 'Idempotency-Key': 'e2e-remote-2' },
    body: JSON.stringify({ ...remoteServer, name: 'e2e-plain' }),
  })
  log('remote servers', autoAuthServer.id, manualServer.id)

  for (let i = 0; i < 40; i++) {
    const page = await api('/api/v1/remote-servers?limit=50')
    const row = (page.items || []).find((item) => item.id === autoAuthServer.id)
    if (row?.agentOnline) break
    if (i === 39) throw new Error(`agent never came online; see ${path.join(config.logDir, 'agent.log')}`)
    await sleep(500)
  }
  log('agent online')

  return {
    api,
    token,
    agentId: agent.id,
    autoAuthServer,
    manualServer,
    credential,
    stop: async () => {
      for (const { child } of children) child.kill()
      for (const { child } of children) {
        await new Promise((resolve) => {
          if (child.exitCode != null || child.signalCode != null) return resolve()
          child.once('exit', resolve)
          setTimeout(resolve, 3000)
        })
      }
    },
    tailLogs: (lines = 40) => {
      const out = []
      for (const file of fs.readdirSync(config.logDir)) {
        if (!file.endsWith('.log')) continue
        const content = fs.readFileSync(path.join(config.logDir, file), 'utf8').split('\n')
        out.push(`--- ${file} (last ${lines}) ---`, ...content.slice(-lines))
      }
      return out.join('\n')
    },
  }
}
