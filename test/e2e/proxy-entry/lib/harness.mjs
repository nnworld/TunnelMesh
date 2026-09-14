// OpenResty runtime for the tp-* proxy entry smoke test.
//
// 渲染的对象就是 deploy/openresty/ 下的真实产物：conf 按占位符替换，Lua 按行尾
// 标记替换值。测试因此覆盖发布物本身，而不是一份手抄副本。
import { execFileSync, spawn } from 'node:child_process'
import fs from 'node:fs'
import net from 'node:net'
import os from 'node:os'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const here = path.dirname(fileURLToPath(import.meta.url))
export const repoRoot = path.resolve(here, '..', '..', '..', '..')

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))
const number = (value, fallback) => (Number.isFinite(Number(value)) && value !== '' && value != null ? Number(value) : fallback)
const runs = (cmd, args) => { try { execFileSync(cmd, args, { stdio: 'ignore' }); return true } catch { return false } }

/** resolveConfig merges environment overrides onto safe local defaults. */
export function resolveConfig(env = process.env) {
  const root = env.TM_PROXY_E2E_REPO || repoRoot
  const workDir = env.TM_PROXY_E2E_DIR || path.join(os.tmpdir(), 'tunnelmesh-proxy-entry-e2e')
  const domainSuffix = env.TM_PROXY_E2E_DOMAIN || 'proxy.test'
  const routeName = env.TM_PROXY_E2E_ROUTE || 'e2e'
  return {
    enabled: env.TM_PROXY_E2E_NGINX === '1',
    repoRoot: root,
    workDir,
    listenPort: number(env.TM_PROXY_E2E_PORT, 18443),
    stubPort: number(env.TM_PROXY_E2E_STUB_PORT, 18089),
    image: env.TM_PROXY_E2E_IMAGE || 'tunnelmesh/openresty-proxy-connect:1.25.3.1',
    skipBuild: env.TM_PROXY_E2E_SKIP_BUILD === '1',
    containerName: env.TM_PROXY_E2E_CONTAINER || `tunnelmesh-proxy-entry-e2e-${process.pid}`,
    domainSuffix,
    routeName,
    proxyHost: `tp-${routeName}.${domainSuffix}`,
    artifacts: {
      lua: path.join(root, 'deploy/openresty/tunnelmesh_proxy_entry.lua'),
      conf: path.join(root, 'deploy/openresty/tunnelmesh-proxy.conf.example'),
      dockerfile: path.join(root, 'deploy/openresty/Dockerfile.proxy-connect'),
    },
  }
}

/** missingPrerequisites lists everything that would make the run impossible. */
export function missingPrerequisites(config) {
  const missing = []
  if (!runs('docker', ['info'])) missing.push('docker CLI with a reachable daemon')
  if (!runs('openssl', ['version'])) missing.push('openssl')
  for (const [name, file] of Object.entries(config.artifacts)) {
    if (!fs.existsSync(file)) missing.push(`${name} artifact ${file}`)
  }
  return missing
}

export function buildImage(config, log) {
  if (config.skipBuild) {
    log(`TM_PROXY_E2E_SKIP_BUILD=1; using image ${config.image} as-is`)
    return
  }
  log(`building ${config.image} (the first build compiles OpenResty and takes minutes)`)
  execFileSync('docker', ['build', '-f', config.artifacts.dockerfile, '-t', config.image, config.repoRoot], { stdio: 'inherit' })
}

export function renderArtifacts(config, log) {
  fs.rmSync(config.workDir, { recursive: true, force: true })
  fs.mkdirSync(config.workDir, { recursive: true })

  execFileSync('openssl', [
    'req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '1',
    '-keyout', path.join(config.workDir, 'tls.key'),
    '-out', path.join(config.workDir, 'tls.crt'),
    '-subj', `/CN=${config.proxyHost}`,
    '-addext', `subjectAltName=DNS:${config.proxyHost},DNS:*.${config.domainSuffix}`,
  ], { stdio: 'ignore' })

  // 容器里的 OpenResty 通过 host.docker.internal 回到宿主机上的 stub。
  const lua = fs.readFileSync(config.artifacts.lua, 'utf8')
    .replace(/host = "[^"]*", -- __TM_INTERNAL_HOST__/, 'host = "host.docker.internal", -- __TM_INTERNAL_HOST__')
    .replace(/port = \d+, -- __TM_INTERNAL_PORT__/, `port = ${config.stubPort}, -- __TM_INTERNAL_PORT__`)
  fs.writeFileSync(path.join(config.workDir, 'tunnelmesh_proxy_entry.lua'), lua)

  const escapedSuffix = config.domainSuffix.replaceAll('.', '\\.')
  const conf = fs.readFileSync(config.artifacts.conf, 'utf8')
    .replaceAll('__LISTEN__', String(config.listenPort))
    .replaceAll('__SERVER_NAME_REGEX__', `~^tp-[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?\\.${escapedSuffix}$`)
    .replaceAll('__SSL_CERT__', '/etc/tunnelmesh/tls.crt')
    .replaceAll('__SSL_CERT_KEY__', '/etc/tunnelmesh/tls.key')
    .replaceAll('__LUA_FILE__', '/etc/tunnelmesh/tunnelmesh_proxy_entry.lua')
    .replaceAll('__INTERNAL_UPSTREAM__', `host.docker.internal:${config.stubPort}`)
    .replaceAll('__EDGE_ALLOW__', 'allow all;')
  fs.writeFileSync(path.join(config.workDir, 'tunnelmesh-proxy.conf'), conf)

  // 这份 wrapper 是测试夹具而不是仓库产物：生产环境把渲染后的 server 块并进既有
  // nginx.conf。日志走 stdout/stderr 并由 harness 落成 container.log，挂载目录
  // 因此可以保持只读。
  fs.writeFileSync(path.join(config.workDir, 'nginx.conf'), [
    'worker_processes 1;',
    'error_log /dev/stderr info;',
    'pid /tmp/nginx.pid;',
    'events { worker_connections 256; }',
    'http {',
    '    access_log /dev/stdout;',
    '    client_body_temp_path /tmp/nginx-client-body;',
    '    proxy_temp_path /tmp/nginx-proxy;',
    '    fastcgi_temp_path /tmp/nginx-fastcgi;',
    '    uwsgi_temp_path /tmp/nginx-uwsgi;',
    '    scgi_temp_path /tmp/nginx-scgi;',
    '    include /etc/tunnelmesh/tunnelmesh-proxy.conf;',
    '}',
    '',
  ].join('\n'))

  log(`rendered artifacts into ${config.workDir}`)
}

export function startNginx(config, log) {
  const logFile = path.join(config.workDir, 'container.log')
  const out = fs.openSync(logFile, 'a')
  const child = spawn('docker', [
    'run', '--rm', '--name', config.containerName,
    '-p', `127.0.0.1:${config.listenPort}:${config.listenPort}`,
    '-v', `${config.workDir}:/etc/tunnelmesh:ro`,
    '--add-host', 'host.docker.internal:host-gateway',
    config.image,
    '-c', '/etc/tunnelmesh/nginx.conf', '-g', 'daemon off;',
  ], { stdio: ['ignore', out, out] })
  child.on('exit', (code) => log(`nginx container exited with code ${code}`))
  return {
    child,
    logFile,
    tail: (lines = 80) => {
      try { return fs.readFileSync(logFile, 'utf8').split('\n').slice(-lines).join('\n') } catch { return '' }
    },
    stop: async () => {
      try { execFileSync('docker', ['rm', '-f', config.containerName], { stdio: 'ignore' }) } catch { /* already gone */ }
      await sleep(200)
      try { fs.closeSync(out) } catch { /* already closed */ }
    },
  }
}

export async function waitListening(port, timeoutMs = 60000) {
  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    const ok = await new Promise((resolve) => {
      const probe = net.connect(port, '127.0.0.1')
      probe.once('connect', () => { probe.destroy(); resolve(true) })
      probe.once('error', () => { probe.destroy(); resolve(false) })
    })
    if (ok) return true
    await sleep(300)
  }
  return false
}
