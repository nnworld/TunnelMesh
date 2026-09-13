export interface DuplexByteStream {
  read(onChunk: (chunk: Uint8Array) => void): () => void
  write(chunk: Uint8Array): Promise<void>
  close(): void
  onClose(handler: () => void): () => void
}

type BinaryMessage = ArrayBuffer | ArrayBufferView | Blob

interface WebSocketLike {
  binaryType: string
  readyState: number
  bufferedAmount: number
  send(data: ArrayBufferView | ArrayBuffer | string): void
  close(): void
  onopen: (() => void) | null
  onmessage: ((event: { data: unknown }) => void) | null
  onclose: (() => void) | null
  onerror: (() => void) | null
}

/** Above this the writer waits for the socket to drain instead of sending. */
const backpressureHighWatermark = 1024 * 1024
/** Hard guard: a peer that never drains would otherwise grow the tab without bound. */
const backpressureLimit = 64 * 1024 * 1024
const backpressurePollInterval = 20

function delay(milliseconds: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, milliseconds))
}

/** Ticket reference needed to build the relay URL. Structurally compatible
 * with the store's pending session so this module stays free of a store import. */
export interface WebSSHSessionTicketRef {
  ticket: { ticket: string; websocketPath: string }
}

/**
 * Build the same-origin WebSocket URL for a WebSSH session ticket. Both the
 * terminal and the SFTP-only view connect over this URL, so it lives next to
 * the stream factory instead of being duplicated per view.
 */
export function websshWebSocketURL(session: WebSSHSessionTicketRef): string {
  const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
  const query = `ticket=${encodeURIComponent(session.ticket.ticket)}`
  return `${protocol}//${window.location.host}${session.ticket.websocketPath}?${query}`
}

/**
 * Wrap a browser WebSocket in the byte-oriented interface expected by the
 * SSH WASM client. Read handlers receive copies so later heap mutations can
 * never change data already delivered to SSH or the terminal.
 */
export function createWebSocketByteStream(url: string): DuplexByteStream {
  const socket = new WebSocket(url) as WebSocketLike
  socket.binaryType = 'arraybuffer'

  let closed = false
  const readHandlers = new Set<(chunk: Uint8Array) => void>()
  const closeHandlers = new Set<() => void>()
  // Writes are chained so a sender that waits for backpressure can never be
  // overtaken by a later one: reordered bytes would corrupt the SSH stream.
  let writeChain: Promise<void> = Promise.resolve()
  const openWaiters = new Set<{
    resolve: () => void
    reject: (error: Error) => void
  }>()

  function emitClose() {
    if (closed) return
    closed = true
    const waiters = [...openWaiters]
    openWaiters.clear()
    for (const waiter of waiters) waiter.reject(new Error('WebSocket stream is closed'))
    const handlers = [...closeHandlers]
    readHandlers.clear()
    closeHandlers.clear()
    for (const handler of handlers) handler()
  }

  function deliver(message: BinaryMessage) {
    if (closed) return
    if (message instanceof Blob) {
      void message.arrayBuffer().then((buffer) => {
        if (closed) return
        const blobChunk = new Uint8Array(buffer)
        for (const handler of [...readHandlers]) handler(blobChunk)
      })
      return
    }

    const chunk =
      message instanceof ArrayBuffer
          ? new Uint8Array(message.slice(0))
          : new Uint8Array(
              message.buffer.slice(
                message.byteOffset,
                message.byteOffset + message.byteLength,
              ) as ArrayBuffer,
            )
    for (const handler of [...readHandlers]) handler(chunk)
  }

  socket.onopen = () => {
    const waiters = [...openWaiters]
    openWaiters.clear()
    for (const waiter of waiters) waiter.resolve()
  }
  socket.onmessage = (event) => {
    if (typeof event.data === 'string') return
    deliver(event.data as BinaryMessage)
  }
  socket.onclose = () => emitClose()
  socket.onerror = () => emitClose()

  return {
    read(onChunk) {
      if (closed) {
        onChunk(new Uint8Array(0))
        return () => undefined
      }
      readHandlers.add(onChunk)
      return () => readHandlers.delete(onChunk)
    },
    write(chunk) {
      const sendChunk = async () => {
        if (closed) throw new Error('WebSocket stream is closed')
        if (socket.readyState === WebSocket.CONNECTING) {
          await new Promise<void>((resolve, reject) => {
            const waiter = { resolve, reject }
            openWaiters.add(waiter)
          })
        }
        // Bulk transfers (sz/rz, SFTP) legitimately queue megabytes. Failing on
        // a transient burst killed the whole SSH session mid-download, so wait
        // for the socket to drain and only give up past the hard guard.
        while (socket.bufferedAmount > backpressureHighWatermark) {
          if (socket.bufferedAmount > backpressureLimit) {
            throw new Error('WebSocket backpressure limit exceeded')
          }
          await delay(backpressurePollInterval)
          if (closed || socket.readyState !== WebSocket.OPEN) {
            throw new Error('WebSocket is not open')
          }
        }
        if (closed || socket.readyState !== WebSocket.OPEN) throw new Error('WebSocket is not open')
        socket.send(chunk)
      }
      const current = writeChain.then(sendChunk, sendChunk)
      writeChain = current.catch(() => undefined)
      return current
    },
    close() {
      if (closed) return
      emitClose()
      if (socket.readyState !== WebSocket.CLOSED) socket.close()
    },
    onClose(handler) {
      if (closed) {
        handler()
        return () => undefined
      }
      closeHandlers.add(handler)
      return () => closeHandlers.delete(handler)
    },
  }
}
