import { describe, expect, it, vi, afterEach } from 'vitest'
import { createWebSocketByteStream, websshWebSocketURL } from './byte-stream'

const hardBackpressureLimit = 64 * 1024 * 1024

class FakeWebSocket {
  static readonly CONNECTING = 0
  static readonly OPEN = 1
  static readonly CLOSING = 2
  static readonly CLOSED = 3

  readyState = FakeWebSocket.CONNECTING
  sent: (ArrayBufferView | ArrayBuffer | string)[] = []
  bufferedAmount = 0
  closeCount = 0
  onopen: (() => void) | null = null
  onmessage: ((event: { data: unknown }) => void) | null = null
  onclose: (() => void) | null = null
  onerror: (() => void) | null = null

  static last: FakeWebSocket | null = null

  constructor() {
    FakeWebSocket.last = this
  }

  send(data: ArrayBufferView | ArrayBuffer | string) {
    this.sent.push(data)
  }

  close() {
    this.closeCount += 1
    this.readyState = FakeWebSocket.CLOSED
    this.onclose?.()
  }
}

function installFakeWebSocket() {
  FakeWebSocket.last = null
  vi.stubGlobal('WebSocket', FakeWebSocket)
  return () => {
    const socket = FakeWebSocket.last
    if (!socket) throw new Error('Fake WebSocket was not constructed')
    return socket
  }
}

describe('WebSocket byte stream', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('converts binary ArrayBuffer messages to Uint8Array', () => {
    const getSocket = installFakeWebSocket()
    const stream = createWebSocketByteStream('wss://example.test/ws')
    const socket = getSocket()
    const chunks: Uint8Array[] = []
    stream.read((chunk) => chunks.push(chunk))
    socket.readyState = FakeWebSocket.OPEN
    socket.onmessage?.({ data: new Uint8Array([1, 2, 3]).buffer })

    expect(chunks).toEqual([new Uint8Array([1, 2, 3])])
    expect(chunks[0]).toBeInstanceOf(Uint8Array)
  })

  it('writes binary data only after the socket opens', async () => {
    const getSocket = installFakeWebSocket()
    const stream = createWebSocketByteStream('wss://example.test/ws')
    const socket = getSocket()

    socket.readyState = FakeWebSocket.OPEN
    await stream.write(new Uint8Array([4, 5]))
    expect(socket.sent).toEqual([new Uint8Array([4, 5])])
  })

  it('rejects a write if the WebSocket closes before it opens', async () => {
    const getSocket = installFakeWebSocket()
    const stream = createWebSocketByteStream('wss://example.test/ws')
    const socket = getSocket()

    const pendingWrite = stream.write(new Uint8Array([4, 5]))
    socket.readyState = FakeWebSocket.CLOSED
    socket.onclose?.()

    await expect(pendingWrite).rejects.toThrow('WebSocket stream is closed')
  })

  it('waits for the WebSocket to open before sending the SSH banner', async () => {
    const getSocket = installFakeWebSocket()
    const stream = createWebSocketByteStream('wss://example.test/ws')
    const socket = getSocket()

    const banner = new TextEncoder().encode('SSH-2.0-TunnelMesh\r\n')
    const pendingWrite = stream.write(banner)
    expect(socket.sent).toEqual([])

    socket.readyState = FakeWebSocket.OPEN
    socket.onopen?.()
    await pendingWrite

    expect(socket.sent).toEqual([banner])
  })

  it('waits for the socket to drain instead of failing a bulk transfer', async () => {
    vi.useFakeTimers()
    try {
      const getSocket = installFakeWebSocket()
      const stream = createWebSocketByteStream('wss://example.test/ws')
      const socket = getSocket()
      socket.readyState = FakeWebSocket.OPEN
      socket.bufferedAmount = 5 * 1024 * 1024

      const pending = stream.write(new Uint8Array([1, 2]))
      await vi.advanceTimersByTimeAsync(80)
      expect(socket.sent).toEqual([])

      socket.bufferedAmount = 0
      await vi.advanceTimersByTimeAsync(80)
      await pending
      expect(socket.sent).toEqual([new Uint8Array([1, 2])])
    } finally {
      vi.useRealTimers()
    }
  })

  it('preserves write order while waiting for the socket to drain', async () => {
    vi.useFakeTimers()
    try {
      const getSocket = installFakeWebSocket()
      const stream = createWebSocketByteStream('wss://example.test/ws')
      const socket = getSocket()
      socket.readyState = FakeWebSocket.OPEN
      socket.bufferedAmount = 5 * 1024 * 1024

      const first = stream.write(new Uint8Array([1]))
      const second = stream.write(new Uint8Array([2]))
      socket.bufferedAmount = 0
      await vi.advanceTimersByTimeAsync(200)
      await Promise.all([first, second])
      expect(socket.sent).toEqual([new Uint8Array([1]), new Uint8Array([2])])
    } finally {
      vi.useRealTimers()
    }
  })

  it('rejects writes past the hard backpressure guard', async () => {
    const getSocket = installFakeWebSocket()
    const stream = createWebSocketByteStream('wss://example.test/ws')
    const socket = getSocket()
    socket.readyState = FakeWebSocket.OPEN
    socket.bufferedAmount = hardBackpressureLimit + 1

    await expect(stream.write(new Uint8Array([1]))).rejects.toThrow('WebSocket backpressure limit exceeded')
  })

  it('closes idempotently and emits the close callback once', () => {
    const getSocket = installFakeWebSocket()
    const stream = createWebSocketByteStream('wss://example.test/ws')
    const socket = getSocket()
    const closed = vi.fn()
    stream.onClose(closed)

    stream.close()
    stream.close()

    expect(socket.closeCount).toBe(1)
    expect(closed).toHaveBeenCalledTimes(1)
  })
})

describe('websshWebSocketURL', () => {
  // The terminal and the SFTP-only entry must build exactly the same URL, so
  // the builder lives in one module instead of being copied per view.
  it('builds a same-origin ws URL carrying the encoded ticket', () => {
    const url = websshWebSocketURL({
      ticket: { ticket: 'a b/c+d=e', websocketPath: '/ws/v1/webssh/session' },
    })
    expect(url.startsWith('ws://')).toBe(true)
    expect(url).toContain(window.location.host)
    expect(url).toContain('/ws/v1/webssh/session?ticket=')
    expect(url).toContain(encodeURIComponent('a b/c+d=e'))
    expect(url).not.toContain('a b/c+d=e?')
  })
})
