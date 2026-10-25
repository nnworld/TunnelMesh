import { describe, expect, it, vi } from 'vitest'
// The browser bundle of zmodem.js is the only entry point that exposes both the
// Sentry and the Browser helpers from a single module instance.
// @ts-expect-error - zmodem.js ships without type declarations
import Zmodem from 'zmodem.js/src/zmodem_browser'
import { createZmodemBridge, type ZmodemBridge, type ZmodemEvent } from './zmodem'

type Remote = { consume(bytes: number[]): void }

type Harness = {
  events: ZmodemEvent[]
  terminal: number[]
  peer: number[]
  bridge: ZmodemBridge
  attachRemote(remote: Remote): void
}

function harness(): Harness {
  const events: ZmodemEvent[] = []
  const terminal: number[] = []
  const peer: number[] = []
  let remote: Remote | null = null
  const bridge = createZmodemBridge({
    toTerminal: (bytes) => terminal.push(...bytes),
    toPeer: (bytes) => {
      peer.push(...bytes)
      remote?.consume([...bytes])
    },
    onEvent: (event) => events.push(event),
  })
  return {
    events, terminal, peer, bridge,
    attachRemote(next: Remote) { remote = next },
  }
}

function eventTypes(h: Harness) { return h.events.map(event => event.type) }

// waitFor polls the event list instead of hooking the emitter, so a missed or
// duplicated event fails the test rather than hanging it forever.
async function waitFor(h: Harness, type: ZmodemEvent['type'], timeoutMs = 2000): Promise<ZmodemEvent> {
  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    const found = h.events.find(event => event.type === type)
    if (found) return found
    await new Promise(resolve => setTimeout(resolve, 5))
  }
  throw new Error(`timed out waiting for ${type}; got ${JSON.stringify(eventTypes(h))}`)
}

// A remote `sz`: it initiates with ZRQINIT, parses the ZRINIT the browser
// answers with, then offers one file over real ZMODEM frames.
async function remoteSendFile(h: Harness, name: string, payload: Uint8Array) {
  const before = h.peer.length
  h.bridge.consumeFromRemote(Uint8Array.from(Zmodem.Header.build('ZRQINIT').to_hex()))
  const zrinit = h.peer.slice(before)
  const session = new Zmodem.Session.Send(Zmodem.Header.parse_hex(zrinit))
  session.set_sender((bytes: number[]) => h.bridge.consumeFromRemote(Uint8Array.from(bytes)))
  h.attachRemote({ consume: (bytes) => session.consume(bytes) })
  const transfer = await session.send_offer({
    name, size: payload.length, mtime: new Date(), files_remaining: 1, bytes_remaining: payload.length,
  })
  expect(transfer, 'the browser must accept the offered file').toBeTruthy()
  transfer.send(payload)
  await transfer.end()
  await session.close()
}

// A remote `rz`: it solicits files with a full-duplex ZRINIT and spools
// whatever the browser sends.
function remoteReceiveFile(h: Harness) {
  const received: { name: string; bytes: Uint8Array }[] = []
  const session = new Zmodem.Session.Receive()
  session.set_sender((bytes: number[]) => h.bridge.consumeFromRemote(Uint8Array.from(bytes)))
  session.on('offer', (offer: any) => {
    const details = offer.get_details()
    offer.accept().then((spool: Uint8Array[]) => {
      received.push({ name: details.name, bytes: concat(spool) })
    })
  })
  h.attachRemote({ consume: (bytes) => session.consume(bytes) })
  // start() emits the ZRINIT that the browser's Sentry detects, exactly like a
  // real `rz` waiting for an offer.
  session.start()
  return { session, received }
}

function concat(chunks: Uint8Array[]): Uint8Array {
  const total = chunks.reduce((sum, chunk) => sum + chunk.length, 0)
  const out = new Uint8Array(total)
  let at = 0
  for (const chunk of chunks) { out.set(chunk, at); at += chunk.length }
  return out
}

// Drives a real receive to the point where every file byte is spooled and
// `completed` has been reported, then detaches the peer so the browser's ZFIN
// is answered by the shell prompt instead of lrzsz's "OO" trailer. That is the
// exact shape of a finished `sz` whose last two bytes were lost, and it makes
// the vendored parser throw from _consume_first.
async function receiveUntilMissingOverAndOut(h: Harness, name: string): Promise<Uint8Array> {
  const payload = new TextEncoder().encode('small file')

  const before = h.peer.length
  h.bridge.consumeFromRemote(Uint8Array.from(Zmodem.Header.build('ZRQINIT').to_hex()))
  const zrinit = h.peer.slice(before)
  const session = new Zmodem.Session.Send(Zmodem.Header.parse_hex(zrinit))
  session.set_sender((bytes: number[]) => h.bridge.consumeFromRemote(Uint8Array.from(bytes)))
  h.attachRemote({ consume: (bytes) => session.consume(bytes) })
  const transfer = await session.send_offer({
    name, size: payload.length, mtime: new Date(), files_remaining: 1, bytes_remaining: payload.length,
  })
  if (!transfer) throw new Error('the browser must accept the offered file')
  transfer.send(payload)
  await transfer.end()
  await waitFor(h, 'completed')

  h.attachRemote({ consume: () => undefined })
  h.bridge.consumeFromRemote(Uint8Array.from(Zmodem.Header.build('ZFIN').to_hex()))
  const prompt = new TextEncoder().encode('\u001b]0;z@host: ~\u0007z@host:~$ ')
  h.bridge.consumeFromRemote(prompt)
  return prompt
}

describe('createZmodemBridge', () => {
  it('passes ordinary terminal bytes through and stays idle', () => {
    const h = harness()
    const bytes = new TextEncoder().encode('ls -la\r\n$ ')

    h.bridge.consumeFromRemote(bytes)

    expect(h.terminal).toEqual([...bytes])
    expect(h.peer).toEqual([])
    expect(h.events).toEqual([])
    expect(h.bridge.active).toBe(false)
  })

  it('detects a remote sz, hides the ZMODEM header from the terminal and answers ZRINIT', async () => {
    const h = harness()

    h.bridge.consumeFromRemote(Uint8Array.from(Zmodem.Header.build('ZRQINIT').to_hex()))

    expect(h.bridge.active).toBe(true)
    expect(eventTypes(h)).toContain('detected')
    expect(h.events[0]).toMatchObject({ type: 'detected', role: 'receive' })
    // The 21-byte hex header is protocol noise: showing it would corrupt the
    // shell prompt the user is looking at.
    expect(h.terminal).toEqual([])
    expect(String.fromCharCode(...h.peer.slice(0, 5))).toBe('**\u0018B0')
  })

  it('restores suppressed bytes when a detection is retracted', () => {
    const h = harness()
    // A truncated header looks like ZMODEM until non-protocol bytes arrive.
    h.bridge.consumeFromRemote(Uint8Array.from(Zmodem.Header.build('ZRQINIT').to_hex().slice(0, 12)))
    h.bridge.consumeFromRemote(new TextEncoder().encode('not zmodem at all\r\n'))

    expect(h.terminal.length).toBeGreaterThan(0)
    expect(h.bridge.active).toBe(false)
  })

  it('receives a file offered by a remote sz and reports progress', async () => {
    const h = harness()
    const payload = new TextEncoder().encode('hello zmodem from the remote host')

    await remoteSendFile(h, 'report.txt', payload)
    const completed = await waitFor(h, 'completed') as Extract<ZmodemEvent, { type: 'completed' }>

    expect(completed.name).toBe('report.txt')
    expect(new TextDecoder().decode(completed.data!)).toBe('hello zmodem from the remote host')
    expect(h.events).toContainEqual(expect.objectContaining({ type: 'receive-offer', name: 'report.txt', size: payload.length }))
    expect(h.events.some(event => event.type === 'progress')).toBe(true)
    await waitFor(h, 'ended')
    expect(h.bridge.active).toBe(false)
    expect(h.terminal).toEqual([])
  })

  // ZFIN is only exchanged after ZEOF, so by the time the missing-"OO"
  // protocol error can be thrown every file byte is already spooled and the
  // `completed` event has fired. Surfacing that as a transfer failure tells
  // the operator a download broke when it in fact finished, and invites a
  // retry of a 153 MiB file that is already on disk. The prompt bytes must
  // still reach the screen, or the terminal looks frozen on a blank line.
  it('reports a finished receive as ended when the peer skips the OO trailer', async () => {
    const h = harness()

    const prompt = await receiveUntilMissingOverAndOut(h, 'a.txt')

    await waitFor(h, 'ended')
    expect(h.events.filter(event => event.type === 'error')).toEqual([])
    expect(h.terminal).toEqual([...prompt])
    expect(h.bridge.active).toBe(false)
  })

  // The vendored Sentry only drops its session from the session's own
  // session_end event, which a protocol throw never fires: `consume()` keeps
  // routing every later byte into the dead session, which throws again. The
  // user sees the identical alert stacked once per keystroke and a terminal
  // that no longer echoes anything. A protocol failure therefore has to
  // discard the Sentry along with the session.
  it('returns to plain terminal passthrough after the parser session dies', async () => {
    const h = harness()
    await receiveUntilMissingOverAndOut(h, 'a.txt')
    await waitFor(h, 'ended')
    h.terminal.length = 0
    h.events.length = 0

    const next = new TextEncoder().encode('z@host:~$ ls -la\r\n')
    h.bridge.consumeFromRemote(next)
    await new Promise(resolve => setTimeout(resolve, 20))

    expect(h.terminal).toEqual([...next])
    expect(h.events).toEqual([])
  })

  // abortLocal() is what the view calls when the transport under a transfer
  // fails. It abandons the session without aborting it, so the Sentry is still
  // wired to a ZMODEM parser that will never see another valid frame.
  it('parses shell output normally after abortLocal', async () => {
    const h = harness()
    h.bridge.consumeFromRemote(Uint8Array.from(Zmodem.Header.build('ZRQINIT').to_hex()))
    expect(h.bridge.active).toBe(true)

    h.bridge.abortLocal()
    expect(h.bridge.active).toBe(false)
    h.terminal.length = 0

    const bytes = new TextEncoder().encode('sz: caught signal 15\r\nz@host:~$ ')
    h.bridge.consumeFromRemote(bytes)
    await new Promise(resolve => setTimeout(resolve, 20))

    expect(h.terminal).toEqual([...bytes])
    expect(h.events.filter(event => event.type === 'error')).toEqual([])
  })

  it('sends local files to a remote rz after the user picks them', async () => {
    const h = harness()
    const remote = remoteReceiveFile(h)

    await waitFor(h, 'send-required')
    expect(h.events[0]).toMatchObject({ type: 'detected', role: 'send' })

    const payload = new TextEncoder().encode('uploaded from the browser')
    const file = new File([payload], 'upload.txt', { type: 'text/plain' })
    await h.bridge.sendFiles([file])

    await vi.waitFor(() => expect(remote.received.length).toBe(1), { timeout: 2000 })
    expect(remote.received[0].name).toBe('upload.txt')
    expect(new TextDecoder().decode(remote.received[0].bytes)).toBe('uploaded from the browser')
    expect(h.events).toContainEqual(expect.objectContaining({ type: 'completed', name: 'upload.txt' }))
    expect(h.bridge.active).toBe(false)
  })

  // One progress event per ZDATA subpacket floods the UI thread on bulk
  // transfers (tens of thousands of Vue updates for a 17 MiB file), which
  // delays the ZACK writes that keep lrzsz sending. Progress is therefore
  // reported in byte-sized steps, not per subpacket.
  it('throttles progress events for bulk transfers', async () => {
    const h = harness()
    const payload = new Uint8Array(200 * 1024)

    await remoteSendFile(h, 'bulk.bin', payload)
    const completed = await waitFor(h, 'completed')
    expect(completed).toBeTruthy()

    const progressEvents = h.events.filter(event => event.type === 'progress')
    expect(progressEvents.length).toBeGreaterThan(0)
    // 200 KiB must not produce one event per ~1 KiB subpacket.
    expect(progressEvents.length).toBeLessThan(10)
    const last = progressEvents[progressEvents.length - 1] as Extract<ZmodemEvent, { type: 'progress' }>
    expect(last.transferred).toBeLessThanOrEqual(payload.length)
  })

  // When the transport itself fails mid-transfer the view drops the transfer
  // but keeps the shell, so the bridge must be able to end locally without
  // pushing a ZABORT into the very pipe that just proved undeliverable.
  it('abortLocal ends the transfer without sending to the peer', () => {
    const h = harness()
    h.bridge.consumeFromRemote(Uint8Array.from(Zmodem.Header.build('ZRQINIT').to_hex()))
    expect(h.bridge.active).toBe(true)
    const peerBytes = h.peer.length

    h.bridge.abortLocal()

    expect(h.bridge.active).toBe(false)
    expect(h.events).toContainEqual(expect.objectContaining({ type: 'aborted' }))
    expect(h.peer.length).toBe(peerBytes)
    // The bridge is idle again: ordinary shell output reaches the terminal.
    h.bridge.consumeFromRemote(new TextEncoder().encode('tmhost$ '))
    expect(h.terminal).toEqual([...new TextEncoder().encode('tmhost$ ')])
  })

  it('rejects sendFiles when no send session is active', async () => {
    const h = harness()

    await expect(h.bridge.sendFiles([new File(['x'], 'x.txt')])).rejects.toThrow(/no active ZMODEM send session/i)
    expect(eventTypes(h)).toContain('error')
  })

  it('aborts a detected receive and tells the peer', () => {
    const h = harness()
    h.bridge.consumeFromRemote(Uint8Array.from(Zmodem.Header.build('ZRQINIT').to_hex()))
    expect(h.bridge.active).toBe(true)

    h.bridge.abort()

    expect(h.bridge.active).toBe(false)
    expect(eventTypes(h)).toContain('aborted')
    // ZMODEM's abort is two CAN bytes repeated; the peer must see them.
    expect(h.peer.filter(byte => byte === 0x18).length).toBeGreaterThan(0)
  })

  it('stops forwarding after dispose', async () => {
    const h = harness()
    h.bridge.consumeFromRemote(Uint8Array.from(Zmodem.Header.build('ZRQINIT').to_hex()))
    expect(h.bridge.active).toBe(true)

    h.bridge.dispose()
    const before = h.terminal.length
    h.bridge.consumeFromRemote(new TextEncoder().encode('after dispose'))

    expect(h.bridge.active).toBe(false)
    expect(h.terminal.length).toBe(before)
  })
})

// A bridge whose peer writes complete only when the test says so, modelling an
// SSH channel whose window is full: the real channel serializes writes and the
// bridge must not pile a whole file behind them.
function gatedHarness(options: { drainQuietMs?: number } = {}) {
  const events: ZmodemEvent[] = []
  const terminal: number[] = []
  const peer: number[] = []
  let remote: Remote | null = null
  let open = false
  // A real channel accepts small writes at once and only blocks when its window
  // fills during bulk data, so the handshake must not be gated.
  let freeBytes = 16 * 1024
  const waiters: Array<() => void> = []
  const bridge = createZmodemBridge({
    toTerminal: (bytes) => terminal.push(...bytes),
    toPeer: (bytes) => {
      peer.push(...bytes)
      remote?.consume([...bytes])
      if (open || freeBytes >= bytes.length) {
        freeBytes -= bytes.length
        return Promise.resolve()
      }
      return new Promise<void>(resolve => waiters.push(resolve))
    },
    onEvent: (event) => events.push(event),
    ...options,
  })
  return {
    events, terminal, peer, bridge,
    attachRemote(next: Remote) { remote = next },
    release() { open = true; waiters.splice(0).forEach(resolve => resolve()) },
  }
}

// Starts a remote `sz` and returns the sender side so a test can keep pushing
// data after the browser gave up, which is what a real sz does until the CAN
// bytes finish crossing the link.
async function startRemoteSz(h: { bridge: ZmodemBridge; peer: number[]; attachRemote(r: Remote): void }, size: number) {
  const before = h.peer.length
  h.bridge.consumeFromRemote(Uint8Array.from(Zmodem.Header.build('ZRQINIT').to_hex()))
  const zrinit = h.peer.slice(before)
  const session = new Zmodem.Session.Send(Zmodem.Header.parse_hex(zrinit))
  session.set_sender((bytes: number[]) => h.bridge.consumeFromRemote(Uint8Array.from(bytes)))
  h.attachRemote({ consume: (bytes) => session.consume(bytes) })
  const transfer = await session.send_offer({ name: 'big.bin', size, mtime: new Date(), files_remaining: 1, bytes_remaining: size })
  if (!transfer) throw new Error('offer rejected')
  return transfer
}

function noise(length: number): Uint8Array<ArrayBuffer> {
  const out = new Uint8Array(new ArrayBuffer(length))
  for (let i = 0; i < length; i++) out[i] = (i * 131 + 7) & 0xff
  return out
}

describe('createZmodemBridge after a transfer is cut short', () => {
  it('keeps in-flight sz data off the terminal after the user aborts a download', async () => {
    const h = gatedHarness()
    h.release()
    const transfer = await startRemoteSz(h, 1 << 20)
    transfer.send(noise(40_000))
    h.bridge.abort()
    h.terminal.length = 0

    // The remote has not seen the CAN yet and keeps streaming.
    for (let i = 0; i < 20; i++) transfer.send(noise(30_000))

    expect(h.terminal).toEqual([])
  })

  it('resumes ordinary output as soon as plain text arrives and asks the view to redraw the prompt otherwise', async () => {
    const h = gatedHarness({ drainQuietMs: 20 })
    h.release()
    const transfer = await startRemoteSz(h, 1 << 20)
    transfer.send(noise(40_000))
    h.bridge.abort()
    h.terminal.length = 0
    transfer.send(noise(30_000))
    expect(h.terminal).toEqual([])

    // Silence after the burst: the stream is over, so the prompt that was
    // swallowed with it has to be re-requested.
    await waitFor(h as unknown as Harness, 'drained')

    const text = new TextEncoder().encode('z@host:~$ ')
    h.bridge.consumeFromRemote(text)
    expect(h.terminal).toEqual([...text])
  })

  it('does not swallow plain text that follows an abort immediately', async () => {
    const h = gatedHarness()
    h.release()
    const transfer = await startRemoteSz(h, 1 << 20)
    transfer.send(noise(40_000))
    h.bridge.abort()
    h.terminal.length = 0

    const text = new TextEncoder().encode('\r\nsz: Transfer incomplete\r\nz@host:~$ ')
    h.bridge.consumeFromRemote(text)

    expect(h.terminal).toEqual([...text])
  })
})

describe('createZmodemBridge upload backpressure', () => {
  const MIB = 1024 * 1024

  it('does not read a whole file ahead of a channel that has not accepted its bytes', async () => {
    const h = gatedHarness()
    remoteReceiveFile(h as unknown as Harness)
    await waitFor(h as unknown as Harness, 'send-required')
    const file = new File([noise(4 * MIB)], 'big.bin')
    const slice = vi.spyOn(file, 'slice')

    void h.bridge.sendFiles([file]).catch(() => undefined)
    await new Promise(resolve => setTimeout(resolve, 200))

    const requested = slice.mock.calls.reduce((sum, [start, end]) => sum + ((end ?? file.size) - (start ?? 0)), 0)
    expect(requested).toBeLessThan(MIB)
    h.bridge.abort()
  })

  it('reports progress for bytes the channel accepted, not bytes merely read', async () => {
    const h = gatedHarness()
    remoteReceiveFile(h as unknown as Harness)
    await waitFor(h as unknown as Harness, 'send-required')
    const file = new File([noise(4 * MIB)], 'big.bin')

    void h.bridge.sendFiles([file]).catch(() => undefined)
    await new Promise(resolve => setTimeout(resolve, 200))

    const progress = h.events.filter((event): event is Extract<ZmodemEvent, { type: 'progress' }> => event.type === 'progress')
    expect(progress.length).toBeGreaterThan(0)
    expect(Math.max(...progress.map(event => event.transferred))).toBeLessThan(MIB / 2)
    expect(progress[0].total).toBe(4 * MIB)
    h.bridge.abort()
  })

  it('drops the unsent backlog on abort so the peer sees the CAN, not the file', async () => {
    const h = gatedHarness()
    remoteReceiveFile(h as unknown as Harness)
    await waitFor(h as unknown as Harness, 'send-required')
    const file = new File([noise(4 * MIB)], 'big.bin')

    void h.bridge.sendFiles([file]).catch(() => undefined)
    await new Promise(resolve => setTimeout(resolve, 200))
    h.bridge.abort()
    h.release()
    await new Promise(resolve => setTimeout(resolve, 100))

    expect(h.peer.length).toBeLessThan(MIB)
    expect(h.peer.filter(byte => byte === 0x18).length).toBeGreaterThan(0)
  })

  it('still delivers a whole file to the peer once the channel keeps up', async () => {
    const h = gatedHarness()
    h.release()
    const remote = remoteReceiveFile(h as unknown as Harness)
    await waitFor(h as unknown as Harness, 'send-required')
    const payload = noise(MIB + 17)

    await h.bridge.sendFiles([new File([payload], 'whole.bin')])

    await vi.waitFor(() => expect(remote.received.length).toBe(1), { timeout: 25_000 })
    expect(remote.received[0].bytes.length).toBe(payload.length)
    expect(remote.received[0].bytes).toEqual(payload)
  }, 30_000)
})
