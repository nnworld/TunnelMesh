// tp-* HTTP proxy entry: OpenResty layer smoke test.
//
// 只验证 OpenResty 搬运层（deploy/openresty/ 的产物）。Server 的路由解析、ACL、
// 认证、目标校验、限额与转发由 internal/proxyentry 与 internal/server 的 Go 测试
// 覆盖，这里不重复。
//
// 用 Node 的 tls 模块而不是 curl：路由身份来自 SNI，Node 可以在连 127.0.0.1 的
// 同时指定 servername，不必改 /etc/hosts，也不依赖各版本 curl 对 https 代理的
// --resolve 行为。
//
// 运行条件与用法见同目录 README.md。
import crypto from 'node:crypto'
import fs from 'node:fs'
import path from 'node:path'
import tls from 'node:tls'
import { buildImage, missingPrerequisites, renderArtifacts, resolveConfig, startNginx, waitListening } from './lib/harness.mjs'
import { startStub } from './lib/stub.mjs'

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))
const log = (...args) => console.log('[e2e]', ...args)

const config = resolveConfig()
const results = []
const check = (name, ok, detail) => {
  results.push({ name, ok: !!ok, detail })
  log(`${ok ? 'PASS' : 'FAIL'} ${name}${detail ? ` :: ${detail}` : ''}`)
}

const credential = `Basic ${Buffer.from('e2e:s3cret').toString('base64')}`

function connectTLS() {
  return new Promise((resolve, reject) => {
    const socket = tls.connect({
      host: '127.0.0.1',
      port: config.listenPort,
      servername: config.proxyHost,
      rejectUnauthorized: false,
      ALPNProtocols: ['http/1.1'],
    })
    socket.once('secureConnect', () => resolve(socket))
    socket.once('error', reject)
  })
}

// readHead consumes everything up to the blank line that ends the proxy
// response head and hands back whatever body bytes rode along in the same read.
function readHead(socket, timeoutMs = 15000) {
  return new Promise((resolve, reject) => {
    const chunks = []
    const timer = setTimeout(() => { cleanup(); reject(new Error('timeout waiting for the proxy response head')) }, timeoutMs)
    const onData = (chunk) => {
      chunks.push(chunk)
      const buf = Buffer.concat(chunks)
      const idx = buf.indexOf('\r\n\r\n')
      if (idx < 0) return
      cleanup()
      resolve({ head: buf.subarray(0, idx).toString('utf8'), rest: buf.subarray(idx + 4) })
    }
    const onError = (err) => { cleanup(); reject(err) }
    const cleanup = () => { clearTimeout(timer); socket.off('data', onData); socket.off('error', onError) }
    socket.on('data', onData)
    socket.on('error', onError)
  })
}

function readBytes(socket, count, timeoutMs = 20000) {
  return new Promise((resolve, reject) => {
    const chunks = []
    let total = 0
    const timer = setTimeout(() => { cleanup(); reject(new Error(`timeout after ${total}/${count} bytes`)) }, timeoutMs)
    const onData = (chunk) => {
      chunks.push(chunk)
      total += chunk.length
      if (total >= count) { cleanup(); resolve(Buffer.concat(chunks).subarray(0, count)) }
    }
    const onError = (err) => { cleanup(); reject(err) }
    const cleanup = () => { clearTimeout(timer); socket.off('data', onData); socket.off('error', onError) }
    socket.on('data', onData)
    socket.on('error', onError)
  })
}

const parseHead = (head) => {
  const [statusLine, ...lines] = head.split('\r\n')
  const headers = {}
  for (const line of lines) {
    const idx = line.indexOf(':')
    if (idx > 0) headers[line.slice(0, idx).trim().toLowerCase()] = line.slice(idx + 1).trim()
  }
  return { statusLine, status: Number(statusLine.split(' ')[1]), headers }
}

async function readBody(socket, rest, headers) {
  const want = Number(headers['content-length'] || 0)
  if (!want || rest.length >= want) return rest.subarray(0, want).toString('utf8')
  const more = await readBytes(socket, want - rest.length)
  return Buffer.concat([rest, more]).toString('utf8')
}

async function sendConnect(target, extraHeaders = []) {
  const socket = await connectTLS()
  socket.write([
    `CONNECT ${target} HTTP/1.1`,
    `Host: ${target}`,
    `Proxy-Authorization: ${credential}`,
    ...extraHeaders,
    '', '',
  ].join('\r\n'))
  return socket
}

let stub
let nginx
let exitCode = 0

if (!config.enabled) {
  results.push({ name: 'openresty proxy entry smoke', ok: true, skipped: true, detail: 'TM_PROXY_E2E_NGINX is not 1' })
  log('SKIP openresty proxy entry smoke :: set TM_PROXY_E2E_NGINX=1 to run (needs docker and openssl)')
  console.log('\n== summary ==')
  for (const r of results) console.log(`SKIP  ${r.name}`)
  process.exit(0)
}

const missing = missingPrerequisites(config)
if (missing.length) {
  results.push({ name: 'openresty proxy entry smoke', ok: true, skipped: true, detail: missing.join('; ') })
  log(`SKIP openresty proxy entry smoke :: missing ${missing.join('; ')}`)
  console.log('\n== summary ==')
  for (const r of results) console.log(`SKIP  ${r.name}`)
  process.exit(0)
}

try {
  buildImage(config, log)
  renderArtifacts(config, log)
  stub = await startStub(config.stubPort)
  nginx = startNginx(config, log)
  if (!await waitListening(config.listenPort)) {
    throw new Error(`openresty did not listen on 127.0.0.1:${config.listenPort}\n${nginx.tail(60)}`)
  }
  log(`openresty listening on 127.0.0.1:${config.listenPort}, stub on :${config.stubPort}`)

  // 1. CONNECT 搬运与请求头白名单
  stub.reset()
  {
    const socket = await sendConnect('ok.test:443', [
      'X-TunnelMesh-Route: tp-forged.example.com',
      'X-TunnelMesh-Client-IP: 203.0.113.7',
      'X-Evil: dropped',
    ])
    const { head } = await readHead(socket)
    const { status } = parseHead(head)
    const record = stub.find((item) => item.kind === 'connect')
    check('CONNECT is answered with 200 Connection Established',
      status === 200 && head.startsWith('HTTP/1.1 200'), `status=${status}`)
    check('route identity comes from SNI, not from a client header',
      record?.headers['x-tunnelmesh-route'] === config.proxyHost, `got=${record?.headers['x-tunnelmesh-route']}`)
    check('a forged client IP is replaced by the real peer',
      !!record?.headers['x-tunnelmesh-client-ip'] && record.headers['x-tunnelmesh-client-ip'] !== '203.0.113.7',
      `got=${record?.headers['x-tunnelmesh-client-ip']}`)
    check('Proxy-Authorization is passed through verbatim',
      record?.headers['proxy-authorization'] === credential,
      record?.headers['proxy-authorization'] ? 'value differs' : 'header missing')
    check('non-whitelisted client headers are dropped',
      record?.headers['x-evil'] === undefined, `got=${record?.headers['x-evil']}`)

    // 2. 双向字节完整性
    const payload = crypto.randomBytes(256 * 1024)
    const echoed = readBytes(socket, payload.length)
    socket.write(payload)
    const received = await echoed
    check('the tunnel carries 256 KiB in both directions byte-for-byte',
      received.equals(payload), `bytes=${received.length} upstream=${record?.bytesUp}`)

    // 3. 客户端断开后隧道被回收
    socket.destroy()
    await sleep(1500)
    check('the tunnel is released when the client goes away',
      record?.closed === true, `closed=${record?.closed}`)
  }

  // 4. 非 200 响应原样透传（状态行、头、body）
  for (const [target, wantStatus, wantHeader] of [
    ['denied.test:443', 403, null],
    ['authfail.test:443', 407, 'proxy-authenticate'],
    ['capacity.test:443', 503, 'retry-after'],
  ]) {
    const socket = await sendConnect(target)
    const { head, rest } = await readHead(socket)
    const { status, headers } = parseHead(head)
    const body = await readBody(socket, rest, headers)
    const ok = status === wantStatus
      && body.includes(`"code":${wantStatus}`)
      && (!wantHeader || !!headers[wantHeader])
    check(`${wantStatus} from the internal entry is relayed verbatim (${target})`, ok,
      `status=${status} ${wantHeader ? `${wantHeader}=${headers[wantHeader] || '<missing>'} ` : ''}body=${body.slice(0, 120)}`)
    socket.destroy()
  }

  // 5. 绝对形式（非 CONNECT）经 location / 到达内部入口
  stub.reset()
  {
    const socket = await connectTLS()
    socket.write([
      'GET http://ok.test/probe HTTP/1.1',
      'Host: ok.test',
      `Proxy-Authorization: ${credential}`,
      'X-TunnelMesh-Route: tp-forged.example.com',
      'Connection: close',
      '', '',
    ].join('\r\n'))
    const { head } = await readHead(socket)
    const { status } = parseHead(head)
    const record = stub.find((item) => item.kind === 'http')
    check('absolute-form requests arrive as origin-form with the trusted headers',
      status === 200
        && record?.url === '/probe'
        && record?.headers['host'] === 'ok.test'
        && record?.headers['proxy-authorization'] === credential
        && record?.headers['x-tunnelmesh-route'] === config.proxyHost,
      `status=${status} url=${record?.url} host=${record?.headers?.host} route=${record?.headers?.['x-tunnelmesh-route']}`)
    socket.destroy()
  }

  // 6. 日志既要能排障，也不能泄漏凭据
  {
    const text = nginx.tail(400)
    check('the error log records tunnel close with route and byte counters',
      text.includes(`tunnelmesh: tunnel closed route=${config.proxyHost}`), 'no tunnel close line found')
    check('the error log never contains the proxy credential',
      !text.includes(credential.split(' ')[1]), 'base64 credential leaked into the log')
  }

  exitCode = results.every((r) => r.ok) ? 0 : 1
} catch (error) {
  log('scenario crashed:', error?.stack || error)
  if (nginx) log(nginx.tail(60))
  exitCode = 1
} finally {
  console.log('\n== summary ==')
  for (const r of results) console.log(`${r.skipped ? 'SKIP' : r.ok ? 'PASS' : 'FAIL'}  ${r.name}`)
  fs.writeFileSync(path.join(config.workDir, 'results.json'), JSON.stringify({ results }, null, 2))
  if (nginx) await nginx.stop()
  if (stub) await stub.close()
  log(`workdir ${config.workDir} (rendered artifacts, container.log and results.json kept)`)
  process.exit(exitCode)
}
