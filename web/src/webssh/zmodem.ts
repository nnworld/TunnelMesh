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
  | { type: 'ended' }
  | { type: 'error'; message: string }

export interface ZmodemBridgeOptions {
  toTerminal: (bytes: Uint8Array) => void
  toPeer: (bytes: Uint8Array) => void
  onEvent: (event: ZmodemEvent) => void
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
    if (!visible.length) return
    options.toTerminal(Uint8Array.from(visible))
  }

  const toPeer = (octets: number[]) => { options.toPeer(Uint8Array.from(octets)) }

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
      if (suppressed && suppressed.length) options.toTerminal(Uint8Array.from(suppressed))
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
        else reportError(error)
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
        if (replay.length) options.toTerminal(Uint8Array.from(replay))
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
      try {
        if (files.length) {
          await Zmodem.Browser.send_files(current, files, {
            on_progress: (file, transfer) => {
              emit({ type: 'progress', name: file.name, transferred: transfer.get_offset(), total: file.size })
            },
            on_file_complete: file => emit({ type: 'completed', name: file.name }),
          })
        }
        // Closing sends ZFIN so the remote rz exits and the shell prompt comes
        // back; an empty selection is a valid "never mind" that still closes.
        await current.close()
      } catch (error) {
        if (!abortedByUser) reportError(error)
        session = null
        role = null
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
      // Abort silently: the view is going away, but the remote rz/sz must not
      // be left waiting for a peer that will never answer.
      if (current) {
        abortedByUser = true
        try { current.abort() } catch { /* the view is already gone */ }
      }
    },
  }
}
