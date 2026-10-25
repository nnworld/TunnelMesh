import Zmodem from 'zmodem.js/src/zmodem_browser'
import type { ZmodemDetection, ZmodemOffer, ZmodemSession, ZmodemSessionType } from './zmodem-js'

// ZMODEM's own maximum subpacket length. Feeding the Sentry in bounded slices
// keeps its internal `push.apply` far away from the argument-count limit even
// when a tunnel frame carries 64 KiB of shell output.
const ZMODEM_CHUNK_BYTES = 8192

// Progress is reported in byte steps (or after a quiet interval) instead of
// once per ZDATA subpacket: a 17 MiB file is tens of thousands of subpackets,
// and tens of thousands of Vue updates starve the thread that has to write
// the ZACKs keeping lrzsz sending.
const progressByteThreshold = 64 * 1024
const progressIntervalMs = 500

// A ZMODEM sender streams without waiting for ACKs, so an upload must be paced
// by the SSH channel itself. Bytes handed to the channel stay bounded by this
// mark; anything above it makes the sender wait. Without the bound, a whole
// file is queued behind the channel's serialized writes: progress then means
// "read from disk", a cancel's CAN bytes wait behind the backlog, and once the
// remote rz is gone the shell echoes the leftover binary back as garbage.
const OUTBOX_HIGH_WATER = 256 * 1024
const SEND_SLICE_BYTES = 64 * 1024

// After a transfer is cut short the remote keeps streaming whatever was already
// in flight (bandwidth x RTT) until the CAN bytes reach it. A fresh Sentry sees
// that tail as shell output, so it is swallowed instead of printed. The window
// closes on the first plain-text chunk, after a quiet period, or at the cap.
const DEFAULT_DRAIN_QUIET_MS = 400
const DRAIN_MAX_MS = 30_000
const ZDLE = 0x18

// lrzsz ends a transfer by printing "OO" (over and out). It is a protocol
// artifact rather than shell output, so it is stripped from the first terminal
// write after a receive session ends; anything else the peer sends with it is
// preserved.
const OVER_AND_OUT = [0x4f, 0x4f]

// The receive spool arrives as one Uint8Array per ZDATA subpacket. Handing the
// view a single buffer keeps the download helper and the progress accounting
// trivial, and copies once instead of on every UI update.
function concatenate(chunks: Uint8Array[]): Uint8Array {
  const total = chunks.reduce((sum, chunk) => sum + chunk.length, 0)
  const out = new Uint8Array(total)
  let offset = 0
  for (const chunk of chunks) { out.set(chunk, offset); offset += chunk.length }
  return out
}

// looksLikeZmodemData tells in-flight protocol bytes from shell text. ZDLE
// (CAN, 0x18) terminates every subpacket and escapes control bytes, and shell
// output virtually never contains it; for a chunk that happens to carry none,
// the share of non-whitespace control bytes separates random binary (~11%) from
// text (~0%). High bytes are not counted because UTF-8 text is full of them.
function looksLikeZmodemData(octets: number[]): boolean {
  if (octets.includes(ZDLE)) return true
  if (octets.length < 32) return false
  let control = 0
  for (const byte of octets) {
    if ((byte < 0x20 && byte !== 0x09 && byte !== 0x0a && byte !== 0x0d && byte !== 0x1b && byte !== 0x08) || byte === 0x7f) control++
  }
  return control / octets.length > 0.05
}

// readSlice avoids Blob.arrayBuffer() only where it is missing (older engines
// and jsdom); real browsers take the first branch.
function readSlice(file: File, start: number, end: number): Promise<Uint8Array> {
  const blob = file.slice(start, end)
  if (typeof blob.arrayBuffer === 'function') return blob.arrayBuffer().then(buffer => new Uint8Array(buffer))
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.onerror = () => reject(reader.error ?? new Error('file read failed'))
    reader.onload = () => resolve(new Uint8Array(reader.result as ArrayBuffer))
    reader.readAsArrayBuffer(blob)
  })
}

export type ZmodemRole = ZmodemSessionType

export type ZmodemEvent =
  | { type: 'detected'; role: ZmodemRole }
  // The remote ran `rz`: the browser is the sender and needs local files.
  | { type: 'send-required' }
  | { type: 'receive-offer'; name: string; size: number }
  | { type: 'progress'; name: string; transferred: number; total: number }
  // `data` is set for a receive (remote `sz`) so the view can save the file; a
  // send only reports the name because the bytes already live on the remote host.
  | { type: 'completed'; name: string; data?: Uint8Array }
  | { type: 'aborted' }
  // The stream left over from a cut-short transfer has gone quiet and its tail,
  // possibly including the shell prompt, was swallowed. The view should nudge the
  // shell so a fresh prompt is drawn.
  | { type: 'drained' }
  | { type: 'ended' }
  | { type: 'error'; message: string }

export interface ZmodemBridgeOptions {
  toTerminal: (bytes: Uint8Array) => void
  // May return a promise that settles once the channel accepted the bytes; the
  // bridge uses it to pace uploads and never has more than one write in flight.
  toPeer: (bytes: Uint8Array) => Promise<void> | void
  onEvent: (event: ZmodemEvent) => void
  drainQuietMs?: number
}

export interface ZmodemBridge {
  consumeFromRemote(bytes: Uint8Array): void
  sendFiles(files: File[]): Promise<void>
  abort(): void
  // Ends the transfer locally without notifying the peer, for callers whose
  // transport just failed: a ZABORT cannot traverse a broken pipe, and trying
  // would only recurse into the same failure handler.
  abortLocal(): void
  readonly active: boolean
  dispose(): void
}

/**
 * createZmodemBridge wraps zmodem.js so the terminal view never touches the
 * protocol library. Raw channel bytes go in through consumeFromRemote; the
 * bridge splits them into "belongs on the screen" (toTerminal) and "belongs to
 * the ZMODEM peer" (toPeer), and reports user-visible state through onEvent.
 *
 * Detection is confirmed automatically: a ZMODEM magic sequence in a shell
 * stream can only come from the user running rz/sz, and a false positive is
 * recoverable through abort().
 */
export function createZmodemBridge(options: ZmodemBridgeOptions): ZmodemBridge {
  let session: ZmodemSession | null = null
  let role: ZmodemRole | null = null
  let disposed = false
  let abortedByUser = false
  // The Sentry forwards the detected header to to_terminal right after
  // on_detect, so the next terminal write is held back. If the detection turns
  // out to be a false positive the bytes are replayed instead of being lost.
  let suppressNextTerminalWrite = false
  let suppressed: number[] | null = null
  let stripNextOverAndOut = false

  // Outbound pacing. `outbox` holds chunks not yet handed to the channel;
  // `inflightBytes` is the one chunk currently being written.
  const outbox: Uint8Array[] = []
  let queuedBytes = 0
  let inflightBytes = 0
  let pumping = false
  const backpressureWaiters: Array<() => void> = []
  const pendingPeerBytes = () => queuedBytes + inflightBytes
  const wakeBackpressureWaiters = () => { for (const wake of backpressureWaiters.splice(0)) wake() }
  const dropOutbox = () => { outbox.length = 0; queuedBytes = 0; wakeBackpressureWaiters() }

  const pump = async () => {
    if (pumping) return
    pumping = true
    try {
      while (outbox.length) {
        const next = outbox.shift()!
        queuedBytes -= next.length
        inflightBytes = next.length
        try {
          // A synchronous peer is not awaited, which keeps ordering and the
          // "answered before consumeFromRemote returns" behaviour of a plain
          // callback; only a real promise yields.
          const result = options.toPeer(next)
          if (result && typeof (result as Promise<void>).then === 'function') await result
        } catch {
          // The view reports a failed channel write itself; what is left
          // cannot be delivered either.
          dropOutbox()
        } finally {
          inflightBytes = 0
          wakeBackpressureWaiters()
        }
      }
    } finally {
      pumping = false
    }
  }

  const queueToPeer = (octets: number[]) => {
    const bytes = Uint8Array.from(octets)
    outbox.push(bytes)
    queuedBytes += bytes.length
    void pump()
  }

  const waitForChannel = async (owner: ZmodemSession) => {
    while (pendingPeerBytes() > OUTBOX_HIGH_WATER && session === owner && !disposed) {
      await new Promise<void>(resolve => backpressureWaiters.push(resolve))
    }
  }

  // Drain window after a transfer was cut short; see DEFAULT_DRAIN_QUIET_MS.
  const drainQuietMs = options.drainQuietMs ?? DEFAULT_DRAIN_QUIET_MS
  let draining = false
  let drainedBytes = 0
  let drainDeadline = 0
  let drainTimer: ReturnType<typeof setTimeout> | null = null
  const clearDrainTimer = () => { if (drainTimer) { clearTimeout(drainTimer); drainTimer = null } }
  const endDrain = (announce: boolean) => {
    clearDrainTimer()
    if (!draining) return
    draining = false
    const swallowed = drainedBytes
    drainedBytes = 0
    if (announce && swallowed > 0) emit({ type: 'drained' })
  }
  const armDrainTimer = () => {
    clearDrainTimer()
    drainTimer = setTimeout(() => endDrain(true), drainQuietMs)
  }
  const beginDrain = () => {
    draining = true
    drainedBytes = 0
    drainDeadline = Date.now() + DRAIN_MAX_MS
    armDrainTimer()
  }

  // writeToTerminal is the only path to the screen, so the drain filter cannot be
  // bypassed by the replay and retract paths below.
  const writeToTerminal = (octets: number[]) => {
    if (!octets.length) return
    if (draining) {
      if (Date.now() < drainDeadline && looksLikeZmodemData(octets)) {
        drainedBytes += octets.length
        armDrainTimer()
        return
      }
      // Plain text: the leftover stream is over and this text is the redraw.
      endDrain(false)
    }
    options.toTerminal(Uint8Array.from(octets))
  }

  const emit = (event: ZmodemEvent) => { if (!disposed) options.onEvent(event) }

  const toTerminal = (octets: number[]) => {
    if (suppressNextTerminalWrite) {
      suppressNextTerminalWrite = false
      suppressed = [...octets]
      return
    }
    let visible = octets
    // Empty writes happen while a session winds down, so the flag stays armed
    // until the trailer itself arrives.
    if (stripNextOverAndOut && visible.length) {
      stripNextOverAndOut = false
      if (visible.length >= 2 && visible[0] === OVER_AND_OUT[0] && visible[1] === OVER_AND_OUT[1]) {
        visible = visible.slice(2)
      }
    }
    writeToTerminal(visible)
  }

  const toPeer = queueToPeer

  const reportError = (error: unknown) => {
    emit({ type: 'error', message: error instanceof Error ? error.message : String(error) })
  }

  // finish clears the session exactly once so `ended` and `aborted` can never
  // both reach the UI for one transfer.
  const finish = (event: 'ended' | 'aborted') => {
    if (!session) return
    session = null
    role = null
    suppressed = null
    emit({ type: event })
  }

  const onOffer = (offer: ZmodemOffer) => {
    const details = offer.get_details()
    const name = details.name || 'download'
    const total = Number(details.size) || 0
    let transferred = 0
    let lastReported = 0
    let lastReportedAt = Date.now()
    emit({ type: 'receive-offer', name, size: total })
    offer.on('input', (payload) => {
      transferred += payload.length
      const now = Date.now()
      // The first subpacket always reports, so the panel leaves 0 B immediately
      // even for tiny files; after that only byte steps or quiet intervals do.
      if (lastReported === 0
        || transferred - lastReported >= progressByteThreshold
        || now - lastReportedAt >= progressIntervalMs) {
        lastReported = transferred
        lastReportedAt = now
        emit({ type: 'progress', name, transferred, total })
      }
    })
    // accept() resolves with the spooled chunks; the library has no
    // get_payloads() in this release, so the Blob is built here.
    offer.accept().then((spool) => {
      emit({ type: 'completed', name, data: concatenate(spool) })
    }).catch((error: unknown) => {
      if (!abortedByUser) reportError(error)
    })
  }

  const onSessionEnd = () => {
    // A finished receive leaves the peer's "OO" trailer in the byte stream.
    if (role === 'receive') stripNextOverAndOut = true
    finish('ended')
  }

  const onDetect = (detection: ZmodemDetection) => {
    suppressNextTerminalWrite = true
    let confirmed: ZmodemSession
    try {
      confirmed = detection.confirm()
    } catch (error) {
      reportError(error)
      return
    }
    session = confirmed
    role = confirmed.type
    abortedByUser = false
    emit({ type: 'detected', role })
    confirmed.on('session_end', onSessionEnd as never)
    if (confirmed.type === 'receive') {
      confirmed.on('offer', onOffer as never)
      // start() answers the peer's ZRQINIT with ZRINIT, which is what makes
      // lrzsz actually offer the file.
      confirmed.start()
      return
    }
    // Send sessions wait for the user to pick files; the view opens the file
    // dialog and then calls sendFiles().
    emit({ type: 'send-required' })
  }

  // A Sentry instance is single-use. The vendored library only clears its
  // internal session from that session's own `session_end` event, which a
  // protocol throw never fires: `consume()` keeps routing every later byte into
  // the dead session, which throws again. The symptom is a terminal that stops
  // echoing plus the identical alert stacking up once per keystroke. Any
  // failure path therefore has to build a fresh Sentry along with dropping the
  // session.
  const makeSentry = () => new Zmodem.Sentry({
    to_terminal: toTerminal,
    sender: toPeer,
    on_detect: onDetect,
    on_retract: () => {
      // A retracted detection means the bytes were ordinary shell output after
      // all, so they go back to the screen and the bridge returns to idle.
      if (suppressed && suppressed.length) writeToTerminal(suppressed)
      suppressed = null
      session = null
      role = null
    },
  })
  let sentry = makeSentry()

  return {
    get active() { return session !== null },

    consumeFromRemote(bytes: Uint8Array) {
      if (disposed) return
      let offset = 0
      try {
        for (; offset < bytes.length; offset += ZMODEM_CHUNK_BYTES) {
          sentry.consume(bytes.subarray(offset, offset + ZMODEM_CHUNK_BYTES))
        }
    } catch (error) {
        // Only a receive session has state worth inspecting; a send session's
        // leftover is the peer's protocol bytes and stays discarded.
        const dead = role === 'receive'
          ? (session as unknown as { _got_ZFIN?: boolean; _input_buffer?: number[] } | null)
          : null
        // lrzsz exchanges ZFIN only after ZEOF and prints "OO" only after
        // that, so a throw from this state means every file byte is already
        // spooled and `completed` has been reported. The missing trailer is
        // teardown noise from a peer that vanished, not a failed transfer:
        // alerting here tells the operator a finished download broke and
        // invites a retry of a file that is already on disk.
        const finishedBeforeThrow = !!dead?._got_ZFIN
        if (finishedBeforeThrow) stripNextOverAndOut = true
        else {
          reportError(error)
          // The peer may still be streaming; see DEFAULT_DRAIN_QUIET_MS.
          dropOutbox()
          beginDrain()
        }
        session = null
        role = null
        suppressed = null
        suppressNextTerminalWrite = false
        sentry = makeSentry()
        // The parser dies with unconsumed bytes in its buffer: typically the
        // shell prompt that arrived where the peer's "OO" belonged after an
        // aborted transfer. Dropping them with the session leaves the terminal
        // frozen on a blank line, so hand the leftover back to the screen.
        const replay: number[] = finishedBeforeThrow && dead?._input_buffer
          ? [...dead._input_buffer]
          : []
        // Chunks behind the one that killed the parser were never fed at all.
        for (let next = offset + ZMODEM_CHUNK_BYTES; next < bytes.length; next += ZMODEM_CHUNK_BYTES) {
          replay.push(...bytes.subarray(next, Math.min(next + ZMODEM_CHUNK_BYTES, bytes.length)))
        }
        writeToTerminal(replay)
        // Reported after the replay so the progress panel only disappears once
        // the prompt the user is waiting for is actually on screen.
        if (finishedBeforeThrow) emit({ type: 'ended' })
      }
    },

    async sendFiles(files: File[]) {
      const current = session
      if (disposed || !current || role !== 'send') {
        const error = new Error('no active ZMODEM send session')
        emit({ type: 'error', message: error.message })
        throw error
      }
      // A cancelled or ended session must stop the read loop at its next step.
      const ensureActive = () => {
        if (session !== current || disposed) throw new Error('ZMODEM transfer ended')
      }
      try {
        let bytesRemaining = files.reduce((sum, file) => sum + file.size, 0)
        for (let index = 0; index < files.length; index++) {
          const file = files[index]
          const transfer = await current.send_offer({
            name: file.name, size: file.size, mtime: new Date(file.lastModified),
            files_remaining: files.length - index, bytes_remaining: bytesRemaining,
          })
          ensureActive()
          bytesRemaining -= file.size
          // The receiver skipped this file (for example it already exists).
          if (!transfer) continue
          // Reported before any data moves, so the panel shows the file name and
          // size instead of "waiting" while the first slices are read.
          emit({ type: 'progress', name: file.name, transferred: 0, total: file.size })
          let lastReported = 0
          let lastReportedAt = Date.now()
          for (;;) {
            // Pace the read by the channel, not by the disk: see OUTBOX_HIGH_WATER.
            await waitForChannel(current)
            ensureActive()
            const offset = transfer.get_offset()
            const end = Math.min(offset + SEND_SLICE_BYTES, file.size)
            const piece = await readSlice(file, offset, end)
            ensureActive()
            if (end >= file.size) {
              // end() resolves once the receiver acknowledged the whole file.
              await transfer.end(piece)
              break
            }
            transfer.send(piece)
            // Bytes still queued for the channel are not "sent" yet.
            const sent = Math.max(0, end - pendingPeerBytes())
            const now = Date.now()
            if (sent - lastReported >= progressByteThreshold || now - lastReportedAt >= progressIntervalMs) {
              lastReported = sent
              lastReportedAt = now
              emit({ type: 'progress', name: file.name, transferred: sent, total: file.size })
            }
          }
          emit({ type: 'progress', name: file.name, transferred: file.size, total: file.size })
          emit({ type: 'completed', name: file.name })
        }
        // Closing sends ZFIN so the remote rz exits and the shell prompt comes
        // back; an empty selection is a valid "never mind" that still closes.
        ensureActive()
        await current.close()
      } catch (error) {
        if (!abortedByUser && session === current) reportError(error)
        if (session === current) {
          session = null
          role = null
        }
        return
      }
      finish('ended')
    },

    abort() {
      const current = session
      if (!current) return
      abortedByUser = true
      session = null
      role = null
      suppressed = null
      // Unsent upload data must not delay (or follow) the abort sequence: with
      // the backlog still queued the peer would see the CAN bytes only after the
      // whole file, and a shell that already took over would echo that file.
      dropOutbox()
      beginDrain()
      try {
        current.abort()
      } catch {
        // An already-finished session can throw; the peer is notified either way.
      }
      // abort() throwing leaves session_end unfired, so the Sentry would still
      // own a session nobody can talk to.
      sentry = makeSentry()
      emit({ type: 'aborted' })
    },

    abortLocal() {
      if (!session) return
      // Suppress the pending accept() rejection: the transfer is over by
      // transport failure, not by a protocol error worth surfacing twice.
      abortedByUser = true
      session = null
      role = null
      suppressed = null
      dropOutbox()
      beginDrain()
      // Nothing is sent to the peer here, so the session never ends on its own
      // and the Sentry would keep parsing shell output as ZMODEM.
      sentry = makeSentry()
      emit({ type: 'aborted' })
    },

    dispose() {
      if (disposed) return
      const current = session
      disposed = true
      session = null
      role = null
      suppressed = null
      clearDrainTimer()
      dropOutbox()
      // Abort silently: the view is going away, but the remote rz/sz must not
      // be left waiting for a peer that will never answer.
      if (current) {
        abortedByUser = true
        try { current.abort() } catch { /* the view is already gone */ }
      }
    },
  }
}
