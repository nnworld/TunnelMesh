import createLibSSH2Module from '@verdigris/libssh2.js'
import type { DuplexByteStream } from './byte-stream'
import { SFTPError, type SFTPClient, type SFTPEntryType, type SFTPFileHandle, type SFTPOpenMode, type RawSFTPEntry } from './sftp'

export type { DuplexByteStream } from './byte-stream'

export type SSHModuleFactory = (options?: Record<string, unknown>) => Promise<SSHWASMModule>

export interface WebSSHClientOptions {
  stream: DuplexByteStream
  username: string
  password?: string
  privateKey?: string
  // Passphrase for an encrypted privateKey. It arrives with the one-time auth
  // payload of a stored credential and lives in memory only.
  passphrase?: string
  hostKeyVerifier: (fingerprint: string) => boolean | Promise<boolean>
}

// ShellExitInfo carries why the remote shell ended. Both fields are null when
// the underlying libssh2 build cannot report them, so the UI falls back to a
// generic "session ended" message instead of inventing an exit code.
// `transportError` is set when the local WebSocket transport failed: that is
// our side dying, not a remote exit, and must never be shown as an exit code.
export type ShellExitInfo = {
  status: number | null
  signal: string | null
  transportError?: string | null
}

export interface TerminalChannel {
  read(onData: (data: Uint8Array) => void): () => void
  // `stallLimitMs` caps how long a zero-progress write waits for channel window
  // credit before it is reported as a failure. 0 removes the cap, which bulk
  // protocol traffic needs: see writeShell.
  write(data: Uint8Array, options?: { stallLimitMs?: number }): Promise<void>
  resize(cols: number, rows: number): Promise<void>
  onExit(onExit: (info: ShellExitInfo) => void): () => void
  close(): Promise<void>
}

export interface SSHConnection {
  openShell(): Promise<TerminalChannel>
  openSFTP(): Promise<SFTPClient>
  close(): Promise<void>
}

type NumberResult = () => number

interface SSHWASMModule {
  HEAPU8: Uint8Array
  getValue(pointer: number, type: string): number
  _malloc(size: number): number
  _free(ptr: number): void
  _ssh2_init(): number
  _ssh2_exit(): void
  _ssh2_session_init(): number
  _ssh2_session_set_blocking(session: number, blocking: number): void
  _ssh2_session_callback_set_custom(session: number, callbackType: number): void
  _ssh2_session_handshake_custom(session: number): number
  _ssh2_session_hostkey?(session: number): { key: Uint8Array; type: number }
  _ssh2_session_last_errno(session: number): number
  _ssh2_session_last_error(session: number): number | string
  _ssh2_userauth_password(session: number, username: number, password: number): number
  _ssh2_userauth_publickey_frommemory(
    session: number,
    username: number,
    publicKeyData: number,
    publicKeyDataLength: number,
    privateKeyData: number,
    privateKeyDataLength: number,
    passphrase: number,
  ): number
  _ssh2_session_disconnect(session: number, reason: number): number
  _ssh2_session_free(session: number): void
  _ssh2_channel_open_session(session: number): number
  _ssh2_channel_request_pty(channel: number, term: string): number
  _ssh2_channel_request_pty_size(channel: number, width: number, height: number): number
  _ssh2_channel_shell(channel: number): number
  _ssh2_channel_read(channel: number, buffer: number, length: number): number
  _ssh2_channel_write(channel: number, buffer: number, length: number): number
  _ssh2_channel_eof(channel: number): number
  _ssh2_channel_close(channel: number): number
  _ssh2_channel_free(channel: number): void
  // Optional because the exit bindings are read defensively: a libssh2 build
  // without them must degrade to "no exit information" instead of throwing.
  _ssh2_channel_get_exit_status?(channel: number): number
  _ssh2_channel_get_exit_signal?(channel: number): number | string
  _ssh2_sftp_init(session: number): number
  _ssh2_sftp_shutdown(sftp: number): number
  _ssh2_sftp_last_error(sftp: number): number
  _ssh2_sftp_open(sftp: number, filename: number, flags: number, mode: number): number
  _ssh2_sftp_opendir(sftp: number, path: number): number
  _ssh2_sftp_close_handle(handle: number): number
  _ssh2_sftp_read(handle: number, buffer: number, maxLength: number): number
  _ssh2_sftp_write(handle: number, buffer: number, count: number): number
  _ssh2_sftp_readdir(handle: number, name: number, nameMaxLength: number, longEntry: number, longEntryMaxLength: number, attributes: number): number
  _ssh2_sftp_unlink(sftp: number, filename: number): number
  _ssh2_sftp_rmdir(sftp: number, path: number): number
  _ssh2_sftp_rename(sftp: number, source: number, destination: number): number
  _ssh2_sftp_realpath(sftp: number, path: number, target: number, maxLength: number): number
}

const EAGAIN = -37
// Emscripten/WASI uses 6 for EAGAIN, unlike Linux's 11.
const SYSTEM_EAGAIN = 6
const LIBSSH2_CALLBACK_SEND = 5
const LIBSSH2_CALLBACK_RECV = 6
// Bulk `sz` downloads and SFTP transfers legitimately queue tens of megabytes
// ahead of the terminal. The bound only exists to stop a genuinely stalled
// peer, so it sits far above any real transfer burst.
const transportLimit = 64 * 1024 * 1024
const sftpReadFlags = 0x00000001
const sftpWriteFlags = 0x00000002 | 0x00000008 | 0x00000010
// How long a zero-progress non-blocking write waits between attempts, and how
// long a completely stalled write is tolerated before it is reported as a
// failure instead of spinning the retry loop forever.
const writeRetryDelay = 10
// This bound is the "the channel is dead" decision, not a latency budget: a
// zero-progress write means the outbound window is full, which on a real WAN
// link is routine for the whole tail of a bulk `sz` or a congested SFTP
// upload. Five seconds closed living sessions mid-download ("SSH 通道已关闭");
// operations feedback requires at least thirty seconds of zero progress
// before a channel may be declared dead. A genuinely dead tunnel still
// surfaces earlier through the WebSocket close and the read loop.
const writeStallLimit = 30_000
const sftpNameLimit = 4096
const sftpLongEntryLimit = 4096
const sftpAttributesSize = 40
const sftpFlagSize = 0x00000001
const sftpFlagPermissions = 0x00000004
const sftpFlagTimes = 0x00000008
const sftpDirectoryMode = 0x4000
const sftpSymlinkMode = 0xa000
const sftpFileTypeMask = 0xf000

const defaultModuleFactory = createLibSSH2Module as unknown as SSHModuleFactory
const utf8Decoder = new TextDecoder('utf-8')
const utf8Encoder = new TextEncoder('utf-8')

function delay(ms: number) {
  return new Promise<void>((resolve) => setTimeout(resolve, ms))
}

// libssh2 channel writes are not atomic: a non-blocking write can consume part
// of the buffer and the remainder has to be resent by a later call. Two writers
// in flight at once would therefore interleave their bytes on the wire (a
// ZMODEM header landing inside a data subpacket, a ZFIN reply queued behind the
// next ZACK), which the peer reads as corruption and answers with ZRPOS or an
// abort. One gate per channel serializes the libssh2 calls while staying async,
// so fire-and-forget callers such as ZMODEM's synchronous sender callback still
// get strict byte order.
function createWriteGate() {
  let tail: Promise<unknown> = Promise.resolve()
  return function throughGate<T>(task: () => Promise<T>): Promise<T> {
    const started = tail.then(task, task)
    // A rejected task must not poison the queue for the writers behind it.
    tail = started.then(() => undefined, () => undefined)
    return started
  }
}

function toChunks(chunk: Uint8Array, maxLength: number) {
  const chunks: Uint8Array[] = []
  for (let offset = 0; offset < chunk.length; offset += maxLength) {
    chunks.push(chunk.slice(offset, offset + maxLength))
  }
  return chunks
}

async function fingerprint(key: Uint8Array) {
  const input = new Uint8Array(key)
  const digest = await crypto.subtle.digest('SHA-256', input)
  const bytes = new Uint8Array(digest)
  let binary = ''
  for (const byte of bytes) binary += String.fromCharCode(byte)
  return `sha256:${btoa(binary)}`
}

async function retryEAGAIN(operation: NumberResult, isCancelled?: () => boolean) {
  for (;;) {
    // A cancelled operation must never touch its handle again: the caller may
    // already have freed it. 0 is the "no progress" value the write loops that
    // pass a predicate already check for.
    if (isCancelled?.()) return 0
    const result = operation()
    if (result !== EAGAIN) return result
    await delay(10)
  }
}

/**
 * Handle-returning libssh2 calls (sftp init/open/opendir) signal "would block"
 * with a NULL handle plus LIBSSH2_ERROR_EAGAIN in the session errno; the
 * return value stays 0, so the int-oriented retryEAGAIN can never see it.
 * Without this retry the first non-blocking stall aborted SFTP startup with a
 * misleading "would block" error even though the tunnel was healthy.
 */
async function retryHandleEAGAIN(module: SSHWASMModule, session: number, operation: () => number) {
  for (;;) {
    const result = operation()
    if (result !== 0) return result
    if (module._ssh2_session_last_errno(session) !== EAGAIN) return 0
    await delay(10)
  }
}

function concatBytes(left: Uint8Array, right: Uint8Array) {
  const result = new Uint8Array(left.length + right.length)
  result.set(left)
  result.set(right, left.length)
  return result
}

interface WasmCString {
  pointer: number
  byteLength: number
}

function allocateCString(module: SSHWASMModule, value: string): WasmCString {
  const bytes = utf8Encoder.encode(value)
  const byteLength = bytes.length
  const pointer = module._malloc(byteLength + 1)
  if (!pointer) throw new Error('WASM allocation failed')
  module.HEAPU8.set(bytes, pointer)
  module.HEAPU8[pointer + byteLength] = 0
  return { pointer, byteLength }
}

function withCString<T>(
  module: SSHWASMModule,
  value: string,
  callback: (pointer: number, byteLength: number) => T,
): T {
  const string = allocateCString(module, value)
  try {
    return callback(string.pointer, string.byteLength)
  } finally {
    module._free(string.pointer)
  }
}

function assertResult(result: number, operation: string, module: SSHWASMModule, session: number) {
  if (result === 0) return
  const message = sessionErrorMessage(module, session, '')
  const errno = module._ssh2_session_last_errno(session) || result
  if (!message) throw new Error(`${operation} failed: libssh2 error ${errno}`)
  throw new Error(`${operation} failed: ${message}`)
}

async function openSessionChannel(module: SSHWASMModule, session: number) {
  for (;;) {
    const channel = module._ssh2_channel_open_session(session)
    if (channel) return channel
    if (module._ssh2_session_last_errno(session) !== EAGAIN) {
      const message = sessionErrorMessage(module, session, `libssh2 error ${module._ssh2_session_last_errno(session)}`)
      throw new Error(`SSH channel open failed: ${message}`)
    }
    await delay(10)
  }
}

// The libssh2 build returns a pointer into its heap; UTF8ToString is not one
// of its exported runtime methods, so decode the NUL-terminated buffer here.
function readWasmCString(module: SSHWASMModule, pointer: number, maxBytesToRead?: number) {
  if (pointer <= 0 || pointer >= module.HEAPU8.length) return ''
  const limit = Math.min(pointer + (maxBytesToRead ?? module.HEAPU8.length - pointer), module.HEAPU8.length)
  let end = pointer
  while (end < limit && module.HEAPU8[end] !== 0) end++
  return utf8Decoder.decode(module.HEAPU8.subarray(pointer, end))
}

function sessionErrorMessage(module: SSHWASMModule, session: number, fallback: string) {
  const error = module._ssh2_session_last_error(session)
  const message = typeof error === 'string' ? error : readWasmCString(module, error)
  return message || fallback
}

// readShellExitInfo must run while the channel handle is still valid: libssh2
// frees the exit status together with the channel. Like the session error text,
// the signal may come back as a heap pointer rather than a JS string.
function readShellExitInfo(module: SSHWASMModule | null, channel: number): ShellExitInfo {
  if (!module) return { status: null, signal: null }
  const rawStatus = typeof module._ssh2_channel_get_exit_status === 'function'
    ? module._ssh2_channel_get_exit_status(channel)
    : null
  let signal: string | null = null
  if (typeof module._ssh2_channel_get_exit_signal === 'function') {
    const rawSignal = module._ssh2_channel_get_exit_signal(channel)
    const decoded = typeof rawSignal === 'string' ? rawSignal : readWasmCString(module, rawSignal)
    signal = decoded || null
  }
  return { status: typeof rawStatus === 'number' ? rawStatus : null, signal }
}

function sftpErrorKind(code: number): SFTPError['kind'] {
  if (code === 2) return 'not-found'
  if (code === 3) return 'permission-denied'
  return 'failure'
}

function symbolicPermissions(mode: number) {
  const typeBits = mode & sftpFileTypeMask
  const type = typeBits === sftpSymlinkMode ? 'l' : typeBits === sftpDirectoryMode ? 'd' : '-'
  const groups = [
    [mode & 0o400, mode & 0o200, mode & 0o100],
    [mode & 0o040, mode & 0o020, mode & 0o010],
    [mode & 0o004, mode & 0o002, mode & 0o001],
  ]
  return type + groups.map(([read, write, execute]) => `${read ? 'r' : '-'}${write ? 'w' : '-'}${execute ? 'x' : '-'}`).join('')
}

function entryType(mode: number): SFTPEntryType {
  const typeBits = mode & sftpFileTypeMask
  if (typeBits === sftpSymlinkMode) return 'symlink'
  if (typeBits === sftpDirectoryMode) return 'directory'
  return 'file'
}

/**
 * connectSSH owns one libssh2 session and maps its custom transport callbacks
 * onto a WebSocket byte stream. The queue is bounded so a stalled SSH peer can
 * never make the browser retain unbounded terminal data.
 */
export async function connectSSH(
  options: WebSSHClientOptions,
  moduleFactory: SSHModuleFactory = defaultModuleFactory,
): Promise<SSHConnection> {
  if (!options.username) throw new Error('SSH username is required')
  if (!options.password && !options.privateKey) {
    throw new Error('SSH password or private key is required')
  }

  const receiveQueue: Uint8Array[] = []
  let queuedBytes = 0
  let transportError: Error | null = null
  let streamClosed = false
  let handshakeCaptureDone = false
  let bannerConsumed = false
  let handshakeBuffer = new Uint8Array(0)
  let capturedHostKey: Uint8Array | null = null

  const consumeHandshakeBuffer = () => {
    if (!bannerConsumed) {
      let newline = handshakeBuffer.indexOf(10)
      if (newline < 0) return
      bannerConsumed = true
      handshakeBuffer = handshakeBuffer.slice(newline + 1)
    }

    while (handshakeBuffer.length >= 4) {
      const packetLength = new DataView(
        handshakeBuffer.buffer,
        handshakeBuffer.byteOffset,
        handshakeBuffer.byteLength,
      ).getUint32(0)
      if (packetLength < 2 || packetLength > 35000) {
        handshakeCaptureDone = true
        return
      }
      if (handshakeBuffer.length < 4 + packetLength) return

      const packet = handshakeBuffer.slice(4, 4 + packetLength)
      handshakeBuffer = handshakeBuffer.slice(4 + packetLength)
      const paddingLength = packet[0]
      if (paddingLength >= packet.length) continue
      const payload = packet.slice(1, packet.length - paddingLength)

      // SSH_MSG_KEX_ECDH_REPLY and the other KEXREPLY messages all begin
      // with the server public-key blob. These packets are captured before
      // the transport turns on encryption, so the key can be fingerprinted
      // without relying on a missing libssh2.js hostkey binding.
      if (payload.length > 5 && payload[0] === 31) {
        const keyLength = new DataView(
          payload.buffer,
          payload.byteOffset,
          payload.byteLength,
        ).getUint32(1)
        if (keyLength > 0 && 5 + keyLength <= payload.length) {
          capturedHostKey = payload.slice(5, 5 + keyLength)
          handshakeCaptureDone = true
          return
        }
      }
    }
  }

  options.stream.read((chunk) => {
    if (streamClosed) return
    if (!handshakeCaptureDone) {
      handshakeBuffer = concatBytes(handshakeBuffer, chunk)
      consumeHandshakeBuffer()
    }
    if (queuedBytes + chunk.length > transportLimit) {
      transportError = new Error('SSH receive buffer limit exceeded')
      options.stream.close()
      return
    }
    receiveQueue.push(chunk)
    queuedBytes += chunk.length
  })
  options.stream.onClose(() => {
    streamClosed = true
  })

  let module: SSHWASMModule | null = null
  let session = 0
  let initialized = false

  try {
    module = await moduleFactory({
      customSend: (buffer: number, length: number) => {
        const chunk = module!.HEAPU8.slice(buffer, buffer + length)
        void options.stream.write(chunk).catch((error: Error) => {
          transportError = error
        })
        return length
      },
      customRecv: (buffer: number, length: number) => {
        if (transportError || streamClosed) return -45
        const chunk = receiveQueue.shift()
        // libssh2's transport callback expects -errno. An empty WebSocket
        // queue is a WASI EAGAIN, not the public LIBSSH2_ERROR_EAGAIN.
        if (!chunk) return -SYSTEM_EAGAIN
        queuedBytes -= chunk.length
        const copy = chunk.length > length ? chunk.slice(0, length) : chunk
        module!.HEAPU8.set(copy, buffer)
        if (copy.length < chunk.length) receiveQueue.unshift(chunk.slice(copy.length))
        return copy.length
      },
    })

    assertResult(module._ssh2_init(), 'SSH initialization', module, 0)
    initialized = true
    session = module._ssh2_session_init()
    module._ssh2_session_set_blocking(session, 0)
    module._ssh2_session_callback_set_custom(session, LIBSSH2_CALLBACK_SEND)
    module._ssh2_session_callback_set_custom(session, LIBSSH2_CALLBACK_RECV)
    assertResult(
      await retryEAGAIN(() => module!._ssh2_session_handshake_custom(session)),
      'SSH handshake',
      module,
      session,
    )
    handshakeCaptureDone = true

    const hostKey = module._ssh2_session_hostkey?.(session) ?? { key: capturedHostKey ?? new Uint8Array() }
    if (!hostKey.key.length) throw new Error('SSH server host key was not captured')
    const accepted = await options.hostKeyVerifier(await fingerprint(hostKey.key))
    if (!accepted) throw new Error('SSH host key rejected')

    const authenticationStrings: WasmCString[] = []
    try {
      let authResult: number
      if (options.privateKey) {
        // The libssh2.js type declarations are misleading: its exported function
        // is the raw C ABI and requires pointers plus explicit byte lengths.
        const username = allocateCString(module, options.username)
        const publicKeyData = allocateCString(module, '')
        const privateKeyData = allocateCString(module, options.privateKey)
        const passphrase = allocateCString(module, options.passphrase ?? '')
        authenticationStrings.push(username, publicKeyData, privateKeyData, passphrase)
        authResult = await retryEAGAIN(() =>
          module!._ssh2_userauth_publickey_frommemory(
            session,
            username.pointer,
            publicKeyData.pointer,
            publicKeyData.byteLength,
            privateKeyData.pointer,
            privateKeyData.byteLength,
            passphrase.pointer,
          ),
        )
      } else {
        const username = allocateCString(module, options.username)
        const password = allocateCString(module, options.password!)
        authenticationStrings.push(username, password)
        authResult = await retryEAGAIN(() =>
          module!._ssh2_userauth_password(session, username.pointer, password.pointer),
        )
      }
      assertResult(authResult, 'SSH authentication', module, session)
    } finally {
      for (const string of authenticationStrings.reverse()) module._free(string.pointer)
    }

    let closed = false
    const sftpClients = new Set<SFTPClient>()
    const close = async () => {
      if (closed) return
      closed = true
      for (const client of [...sftpClients]) await client.close()
      sftpClients.clear()
      options.stream.close()
      if (module && session) {
        withCString(module, 'WebSSH closed', (reason) => {
          module._ssh2_session_disconnect(session, reason)
        })
        module._ssh2_session_free(session)
      }
      if (module && initialized) module._ssh2_exit()
    }

    return {
      async openShell() {
      if (!module) throw new Error('SSH connection is closed')
      const channel = await openSessionChannel(module, session)
      const terminalType = allocateCString(module, 'xterm-256color')
      try {
        assertResult(
          await retryEAGAIN(() => module!._ssh2_channel_request_pty(channel, terminalType.pointer)),
          'SSH PTY request',
          module,
          session,
        )
        assertResult(
          await retryEAGAIN(() => module!._ssh2_channel_request_pty_size(channel, 80, 24)),
          'SSH PTY size request',
          module,
          session,
        )
        assertResult(
          await retryEAGAIN(() => module!._ssh2_channel_shell(channel)),
          'SSH shell request',
          module,
          session,
        )
      } finally {
        module._free(terminalType.pointer)
      }

        const readers = new Set<(data: Uint8Array) => void>()
        const pendingOutput: Uint8Array[] = []
        const exitListeners = new Set<(info: ShellExitInfo) => void>()
        let shellExited = false
        let exitInfo: ShellExitInfo = { status: null, signal: null }
        const bufferLength = 32 * 1024
        const buffer = module._malloc(bufferLength)
        let shellClosed = false

        // Every libssh2 call that writes to this channel goes through one
        // gate; see createWriteGate for why partial writes make concurrent
        // writers unsafe.
        const shellGate = createWriteGate()

        const notifyShellExit = (transportFailure: string | null) => {
          if (shellExited) return
          shellExited = true
          // Read the exit reason before close() frees the channel handle.
          exitInfo = { ...readShellExitInfo(module, channel), transportError: transportFailure }
          for (const listener of [...exitListeners]) listener(exitInfo)
        }

        void (async () => {
          while (!shellClosed && module) {
            const result = module._ssh2_channel_read(channel, buffer, bufferLength)
            if (result > 0) {
              const data = module.HEAPU8.slice(buffer, buffer + result)
              if (readers.size) {
                for (const reader of [...readers]) reader(data)
              } else {
                pendingOutput.push(data)
              }
            } else if (result === EAGAIN) {
              await delay(20)
            } else {
              break
            }
          }
          if (!shellClosed) notifyShellExit(transportError?.message ?? null)
        })()

        // `stallLimitMs` bounds how long a write with zero progress is
        // tolerated before it is reported as a failure instead of spinning the
        // retry loop forever. Callers that own a bulk protocol exchange pass 0
        // for "no deadline": lrzsz pauses while it retransmits a ZACK, and the
        // outbound window can legitimately stay full far longer than a
        // keystroke ever should.
        const writeShell = async (data: Uint8Array, stallLimitMs: number) => {
          if (!module || shellClosed) throw new Error('SSH shell is closed')
          for (const chunk of toChunks(data, bufferLength)) {
            const pointer = module._malloc(chunk.length)
            try {
              module.HEAPU8.set(chunk, pointer)
              // Non-blocking channel writes can consume only part of the
              // buffer when the channel window is momentarily full; the
              // remainder must be resent or a long paste loses keystrokes.
              let sent = 0
              let stalledAt = 0
              while (sent < chunk.length) {
                // close() can run while this write waits for window credit;
                // writing past it would corrupt the close handshake.
                if (shellClosed) return
                const result = await retryEAGAIN(
                  () => module!._ssh2_channel_write(channel, pointer + sent, chunk.length - sent),
                  // close() no longer waits for the gate, so the channel can be
                  // freed while this retry loop is between attempts.
                  () => shellClosed,
                )
                if (shellClosed) return
                if (result < 0) {
                  assertResult(result, 'SSH shell write', module, session)
                }
                if (result > 0) {
                  sent += result
                  stalledAt = 0
                  continue
                }
                stalledAt += writeRetryDelay
                if (stallLimitMs > 0 && stalledAt > stallLimitMs) {
                  throw new Error(`SSH shell write stalled after ${sent} of ${chunk.length} bytes`)
                }
                await delay(writeRetryDelay)
              }
            } finally {
              module._free(pointer)
            }
          }
        }

        const resizeShell = async (cols: number, rows: number) => {
          if (!module || shellClosed) return
          assertResult(
            await retryEAGAIN(() => module!._ssh2_channel_request_pty_size(channel, cols, rows)),
            'SSH terminal resize',
            module,
            session,
          )
        }

        const closeShell = async () => {
          if (shellClosed) return
          shellClosed = true
          readers.clear()
          if (module) {
            module._ssh2_channel_close(channel)
            module._ssh2_channel_free(channel)
            module._free(buffer)
          }
        }

        return {
          read(onData) {
            readers.add(onData)
            for (const data of pendingOutput.splice(0)) onData(data)
            return () => readers.delete(onData)
          },
          onExit(onExit) {
            exitListeners.add(onExit)
            if (shellExited) onExit(exitInfo)
            return () => exitListeners.delete(onExit)
          },
          write(data, options) {
            return shellGate(() => writeShell(data, options?.stallLimitMs ?? writeStallLimit))
          },
          resize(cols, rows) {
            return shellGate(() => resizeShell(cols, rows))
          },
          close() {
            // Teardown must never queue behind a writer: a bulk protocol write
            // waits for window credit without a deadline, so a gated close()
            // could hang forever and leak the libssh2 handles. closeShell()
            // flips shellClosed synchronously, which is what makes the
            // in-flight writer unwind at its next check instead of writing to
            // a freed channel.
            return closeShell()
          },
        }
      },
      async openSFTP() {
        if (!module || closed) throw new Error('SSH connection is closed')
        const sftp = await retryHandleEAGAIN(module, session, () => module!._ssh2_sftp_init(session))
        if (!sftp) {
          // Without libssh2's own errno and message this failure is a dead
          // end for operators: the same alert covers "server has no sftp
          // subsystem", "agent policy denied the target" and a broken tunnel.
          const errno = module._ssh2_session_last_errno(session)
          throw new Error(`SFTP initialization failed: ${sessionErrorMessage(module, session, `libssh2 error ${errno}`)}`)
        }
        let sftpClosed = false

        const requireOpen = () => {
          if (sftpClosed || !module) throw new SFTPError('closed', 'SFTP connection is closed')
        }

        // One writer at a time on the SFTP channel as well: an upload whose
        // write is only partially flushed must not let a concurrent listing,
        // rename or delete splice its request bytes. See createWriteGate.
        const sftpGate = createWriteGate()

        const retrySFTP = async (operation: () => number) => {
          for (;;) {
            requireOpen()
            const result = operation()
            if (result !== EAGAIN) return result
            await delay(10)
          }
        }

        const assertSFTPResult = (result: number, operation: string) => {
          if (result === 0) return
          const code = module!._ssh2_sftp_last_error(sftp)
          const message = sessionErrorMessage(module!, session, `SFTP error ${code || result}`)
          throw new SFTPError(sftpErrorKind(code), `${operation} failed: ${message}`)
        }

        const withPath = async (path: string, operation: (pointer: number) => Promise<number> | number) => {
          requireOpen()
          return withCString(module!, path, operation)
        }

        const openFile = async (path: string, mode: SFTPOpenMode): Promise<SFTPFileHandle> => {
          const handle = await sftpGate(() => withPath(path, (pointer) => retryHandleEAGAIN(module!, session, () => module!._ssh2_sftp_open(
            sftp,
            pointer,
            mode === 'read' ? sftpReadFlags : sftpWriteFlags,
            0o644,
          ))))
          if (!handle) {
            const code = module!._ssh2_sftp_last_error(sftp)
            const message = sessionErrorMessage(module!, session, `SFTP error ${code}`)
            throw new SFTPError(sftpErrorKind(code), `SFTP open failed: ${message}`)
          }

          let handleClosed = false
          const closeHandle = async () => {
            if (handleClosed || !module) return
            handleClosed = true
            await sftpGate(async () => {
              assertSFTPResult(await retrySFTP(() => module!._ssh2_sftp_close_handle(handle)), 'SFTP close')
            })
          }

          return {
            async read(buffer) {
              return sftpGate(async () => {
                requireOpen()
                if (handleClosed) throw new SFTPError('closed', 'SFTP file is closed')
                const pointer = module!._malloc(buffer.length)
                try {
                  const result = await retrySFTP(() => module!._ssh2_sftp_read(handle, pointer, buffer.length))
                  if (result < 0) assertSFTPResult(result, 'SFTP read')
                  const count = Math.min(result, buffer.length)
                  buffer.set(module!.HEAPU8.slice(pointer, pointer + count))
                  return count
                } finally {
                  module!._free(pointer)
                }
              })
            },
            async write(chunk) {
              return sftpGate(async () => {
                requireOpen()
                if (handleClosed) throw new SFTPError('closed', 'SFTP file is closed')
                const pointer = module!._malloc(chunk.length)
                try {
                  module!.HEAPU8.set(chunk, pointer)
                  // The session is non-blocking, so libssh2_sftp_write returns the
                  // bytes it consumed so far whenever the channel window or the
                  // tunnel stalls: a positive value smaller than the request, not
                  // EAGAIN. Returning that as "done" truncated every upload bigger
                  // than one flush, so keep feeding the unconsumed remainder.
                  let written = 0
                  let stalledAt = 0
                  while (written < chunk.length) {
                    const result = await retrySFTP(() => module!._ssh2_sftp_write(
                      handle,
                      pointer + written,
                      chunk.length - written,
                    ))
                    if (result < 0) assertSFTPResult(result, 'SFTP write')
                    if (result > 0) {
                      written += result
                      stalledAt = 0
                      continue
                    }
                    // Zero bytes with no error means the peer accepted nothing
                    // this round. Bound it so a dead tunnel errors out instead of
                    // spinning the retry loop forever.
                    stalledAt += writeRetryDelay
                    if (stalledAt > writeStallLimit) {
                      throw new SFTPError('failure', `SFTP write stalled after ${written} of ${chunk.length} bytes`)
                    }
                    await delay(writeRetryDelay)
                  }
                  return written
                } finally {
                  module!._free(pointer)
                }
              })
            },
            close: closeHandle,
          }
        }

        const client: SFTPClient = {
          async readDirectory(path) {
            // The gate is held for the whole listing: opendir, every readdir
            // round and the close are one logical operation, and a partially
            // flushed readdir request must resume before anybody else writes
            // to the channel.
            return sftpGate(async () => {
              const handle = await withPath(path, (pointer) => retryHandleEAGAIN(module!, session, () => module!._ssh2_sftp_opendir(sftp, pointer)))
              if (!handle) {
                const code = module!._ssh2_sftp_last_error(sftp)
                const message = sessionErrorMessage(module!, session, `SFTP error ${code}`)
                throw new SFTPError(sftpErrorKind(code), `SFTP directory open failed: ${message}`)
              }

              const name = module!._malloc(sftpNameLimit)
              const longEntry = module!._malloc(sftpLongEntryLimit)
              const attributes = module!._malloc(sftpAttributesSize)
              const entries: RawSFTPEntry[] = []
              try {
                for (;;) {
                  const result = await retrySFTP(() => module!._ssh2_sftp_readdir(
                    handle,
                    name,
                    sftpNameLimit,
                    longEntry,
                    sftpLongEntryLimit,
                    attributes,
                  ))
                  if (result === 0) break
                  if (result < 0) assertSFTPResult(result, 'SFTP directory read')

                  const flags = module!.getValue(attributes, 'i32')
                  const sizeLow = module!.getValue(attributes + 8, 'i32')
                  const sizeHigh = module!.getValue(attributes + 12, 'i32')
                  const permissions = module!.getValue(attributes + 24, 'i32')
                  const modified = module!.getValue(attributes + 32, 'i32')
                  const entryName = readWasmCString(module!, name, sftpNameLimit)
                  if (!entryName || entryName === '.' || entryName === '..') continue
                  entries.push({
                    name: entryName,
                    type: entryType(permissions),
                    size: flags & sftpFlagSize ? sizeLow + sizeHigh * 2 ** 32 : 0,
                    modifiedAt: flags & sftpFlagTimes ? new Date(modified * 1000).toISOString() : undefined,
                    permissions: flags & sftpFlagPermissions ? symbolicPermissions(permissions) : undefined,
                  })
                }
                return entries
              } finally {
                module!._free(name)
                module!._free(longEntry)
                module!._free(attributes)
                await retrySFTP(() => module!._ssh2_sftp_close_handle(handle))
              }
            })
          },
          openFile,
          async unlink(path) {
            await sftpGate(async () => {
              assertSFTPResult(await withPath(path, (pointer) => retrySFTP(() => module!._ssh2_sftp_unlink(sftp, pointer))), 'SFTP delete')
            })
          },
          async rmdir(path) {
            await sftpGate(async () => {
              assertSFTPResult(await withPath(path, (pointer) => retrySFTP(() => module!._ssh2_sftp_rmdir(sftp, pointer))), 'SFTP directory delete')
            })
          },
          async rename(path, nextPath) {
            return sftpGate(() => withCString(module!, path, (source) => withCString(module!, nextPath, (destination) =>
              retrySFTP(() => module!._ssh2_sftp_rename(sftp, source, destination))
            ).then((result) => assertSFTPResult(result, 'SFTP rename'))))
          },
          async realpath(path) {
            return sftpGate(() => withCString(module!, path, async (source) => {
              const target = module!._malloc(sftpNameLimit)
              try {
                // libssh2 reports realpath success as the resolved string
                // length, so only negative results are failures; asserting
                // "non-zero is error" here turned every successful resolve
                // into a spurious SFTP error.
                const realpathResult = await retrySFTP(() => module!._ssh2_sftp_realpath(sftp, source, target, sftpNameLimit))
                if (realpathResult < 0) assertSFTPResult(realpathResult, 'SFTP real path')
                return readWasmCString(module!, target, sftpNameLimit)
              } finally {
                module!._free(target)
              }
            }))
          },
          close() {
            // Shutdown waits for in-flight operations so the channel cannot
            // be torn down inside a partially flushed request.
            return sftpGate(async () => {
              if (sftpClosed || !module) return
              sftpClosed = true
              module._ssh2_sftp_shutdown(sftp)
              sftpClients.delete(client)
            })
          },
        }
        sftpClients.add(client)
        return client
      },
      close,
    }
  } catch (error) {
    if (module && session) {
      module._ssh2_session_free(session)
    }
    if (module && initialized) module._ssh2_exit()
    options.stream.close()
    throw error
  }
}
