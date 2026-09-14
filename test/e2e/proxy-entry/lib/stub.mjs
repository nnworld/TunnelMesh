// Scripted stand-in for the tunnelmesh-server internal proxy entry.
//
// 这不是 tunnelmesh-server：Server 的策略与转发已由 Task 9-12 的 Go 测试覆盖。
// 这里只需要一个行为可预期的回声入口，用来断言 OpenResty 搬运层做对了三件事：
// 请求头白名单、CONNECT 双向 splice、非 200 响应原样透传。
//
// 编排方式是 CONNECT 目标主机名（绝对形式则看 Host 头），不是请求头：Lua 只透传
// 白名单头，用请求头编排根本传不进来——而这正是要验证的行为。
import http from 'node:http'

const SCRIPTS = {
  'denied.test': { status: 403, reason: 'Forbidden', code: 'proxy_source_denied' },
  'authfail.test': {
    status: 407,
    reason: 'Proxy Authentication Required',
    code: 'proxy_auth_failed',
    extra: ['Proxy-Authenticate: Basic realm="TunnelMesh", charset="UTF-8"'],
  },
  'capacity.test': {
    status: 503,
    reason: 'Service Unavailable',
    code: 'proxy_capacity_exhausted',
    extra: ['Retry-After: 5'],
  },
}

const scriptFor = (host) => SCRIPTS[String(host || '').split(':')[0].toLowerCase()] || null
const envelope = (status, code) => JSON.stringify({ code: status, msg: code, data: null })

export function startStub(port, address = '0.0.0.0') {
  const seen = []

  const server = http.createServer((req, res) => {
    const record = { kind: 'http', method: req.method, url: req.url, headers: req.headers }
    seen.push(record)
    const script = scriptFor(req.headers.host)
    const body = script ? envelope(script.status, script.code) : JSON.stringify({ url: req.url, host: req.headers.host })
    const headers = { 'content-type': 'application/json' }
    for (const line of script?.extra || []) {
      const idx = line.indexOf(': ')
      headers[line.slice(0, idx).toLowerCase()] = line.slice(idx + 2)
    }
    res.writeHead(script ? script.status : 200, headers)
    res.end(body)
  })

  server.on('connect', (req, socket, head) => {
    const record = { kind: 'connect', url: req.url, headers: req.headers, bytesUp: 0, bytesDown: 0, closed: false }
    seen.push(record)
    socket.on('close', () => { record.closed = true })
    socket.on('error', () => {})

    const script = scriptFor(req.url)
    if (script) {
      const body = envelope(script.status, script.code)
      const lines = [
        `HTTP/1.1 ${script.status} ${script.reason}`,
        ...(script.extra || []),
        'content-type: application/json',
        `content-length: ${Buffer.byteLength(body)}`,
        'cache-control: no-store',
        '',
        body,
      ]
      socket.end(lines.join('\r\n'))
      return
    }

    socket.write('HTTP/1.1 200 Connection Established\r\n\r\n')
    if (head && head.length) {
      record.bytesDown += head.length
      socket.write(head)
    }
    // 回声：客户端发来的每个字节原样送回，用来验证双向 splice 与字节完整性。
    socket.on('data', (chunk) => {
      record.bytesUp += chunk.length
      socket.write(chunk, () => { record.bytesDown += chunk.length })
    })
  })

  return new Promise((resolve, reject) => {
    server.once('error', reject)
    server.listen(port, address, () => resolve({
      port: server.address().port,
      seen,
      find: (predicate) => seen.find(predicate),
      reset: () => { seen.length = 0 },
      close: () => new Promise((done) => { server.closeAllConnections?.(); server.close(done) }),
    }))
  })
}
