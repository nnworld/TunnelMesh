import { describe, expect, it, vi } from 'vitest'
import { connectSSH, type DuplexByteStream, type SSHModuleFactory } from './ssh-client'

function fakeStream(): DuplexByteStream & { chunks: Uint8Array[]; closeCount: number } {
  const stream = {
    chunks: [] as Uint8Array[],
    closeCount: 0,
    read() { return () => undefined },
    async write(chunk: Uint8Array) { this.chunks.push(chunk) },
    close() { this.closeCount += 1 },
    onClose() { return () => undefined },
  }
  return stream
}

function fakeModule(overrides: Record<string, unknown> = {}) {
  return {
    HEAPU8: new Uint8Array(1024),
    _malloc: vi.fn(() => 16),
    _free: vi.fn(),
    _ssh2_init: vi.fn(() => 0),
    _ssh2_session_init: vi.fn(() => 101),
    _ssh2_session_set_blocking: vi.fn(),
    _ssh2_session_callback_set_custom: vi.fn(),
    _ssh2_session_handshake_custom: vi.fn(() => 0),
    _ssh2_session_hostkey: vi.fn(() => ({ key: new Uint8Array([1, 2, 3]), type: 0 })),
    _ssh2_session_last_errno: vi.fn(() => 0),
    _ssh2_session_last_error: vi.fn(() => ''),
    _ssh2_userauth_password: vi.fn(() => 0),
    _ssh2_userauth_publickey_frommemory: vi.fn(() => 0),
    _ssh2_session_disconnect: vi.fn(() => 0),
    _ssh2_session_free: vi.fn(),
    _ssh2_exit: vi.fn(),
    _ssh2_channel_open_session: vi.fn(() => 201),
    _ssh2_channel_request_pty: vi.fn(() => 0),
    _ssh2_channel_request_pty_size: vi.fn(() => 0),
    _ssh2_channel_shell: vi.fn(() => 0),
    _ssh2_channel_read: vi.fn(() => 0),
    _ssh2_channel_write: vi.fn((_channel: number, _buffer: number, length: number) => length),
    _ssh2_channel_eof: vi.fn(() => 1),
    _ssh2_channel_close: vi.fn(() => 0),
    _ssh2_channel_free: vi.fn(),
    ...overrides,
  }
}

describe('connectSSH', () => {
  it('rejects a missing username without initializing SSH', async () => {
    const factory = vi.fn(async () => fakeModule())
    await expect(connectSSH({
      stream: fakeStream(),
      username: '',
      password: 'password',
      hostKeyVerifier: () => true,
    }, factory as unknown as SSHModuleFactory)).rejects.toThrow('SSH username is required')
    expect(factory).not.toHaveBeenCalled()
  })

  it('rejects when neither password nor private key is provided', async () => {
    const factory = vi.fn(async () => fakeModule())
    await expect(connectSSH({
      stream: fakeStream(),
      username: 'root',
      hostKeyVerifier: () => true,
    }, factory as unknown as SSHModuleFactory)).rejects.toThrow('SSH password or private key is required')
    expect(factory).not.toHaveBeenCalled()
  })

  it('closes the WebSocket stream when SSH handshake fails', async () => {
    const stream = fakeStream()
    const factory = vi.fn(async () => fakeModule({
      _ssh2_session_handshake_custom: vi.fn(() => -18),
      _ssh2_session_last_errno: vi.fn(() => -18),
      _ssh2_session_last_error: vi.fn(() => 'Authentication failed'),
    }))

    await expect(connectSSH({
      stream,
      username: 'root',
      password: 'password',
      hostKeyVerifier: () => true,
    }, factory as unknown as SSHModuleFactory)).rejects.toThrow('SSH handshake failed: Authentication failed')
    expect(stream.closeCount).toBe(1)
  })

  it('converts a WASM error-message pointer to readable SSH error text', async () => {
    const heap = new Uint8Array(1024)
    heap.set(new TextEncoder().encode('Unable to receive banner data'), 100)
    const factory = vi.fn(async () => fakeModule({
      HEAPU8: heap,
      _ssh2_session_handshake_custom: vi.fn(() => -43),
      _ssh2_session_last_errno: vi.fn(() => -43),
      _ssh2_session_last_error: vi.fn(() => 100),
    }))

    await expect(connectSSH({
      stream: fakeStream(),
      username: 'root',
      password: 'password',
      hostKeyVerifier: () => true,
    }, factory as unknown as SSHModuleFactory)).rejects.toThrow('SSH handshake failed: Unable to receive banner data')
  })

  it('registers custom transport callbacks before starting the SSH handshake', async () => {
    const module = fakeModule()
    const factory = vi.fn(async () => module)
    await connectSSH({
      stream: fakeStream(),
      username: 'root',
      password: 'password',
      hostKeyVerifier: () => true,
    }, factory as unknown as SSHModuleFactory)

    expect(module._ssh2_session_callback_set_custom).toHaveBeenNthCalledWith(1, 101, 5)
    expect(module._ssh2_session_callback_set_custom).toHaveBeenNthCalledWith(2, 101, 6)
    expect(module._ssh2_session_handshake_custom.mock.invocationCallOrder[0])
      .toBeGreaterThan(module._ssh2_session_callback_set_custom.mock.invocationCallOrder[1])
  })

  it('reports an empty WebSocket receive queue as WASI EAGAIN', async () => {
    const module = fakeModule()
    let customRecv: ((buffer: number, length: number) => number) | undefined
    const factory = vi.fn(async (options?: Record<string, unknown>) => {
      customRecv = options?.customRecv as typeof customRecv
      return module
    })
    await connectSSH({
      stream: fakeStream(),
      username: 'root',
      password: 'password',
      hostKeyVerifier: () => true,
    }, factory as unknown as SSHModuleFactory)

    expect(customRecv).toBeDefined()
    expect(customRecv!(500, 16)).toBe(-6)
  })

  // A bulk `sz` download queues megabytes before the terminal drains them.
  // Treating that as a transport failure killed the session mid-transfer, so
  // the bound only exists to stop a genuinely stalled peer.
  it('keeps the session alive when the receive queue grows past four MiB', async () => {
    const module = fakeModule({ HEAPU8: new Uint8Array(8 * 1024 * 1024) })
    let customRecv: ((buffer: number, length: number) => number) | undefined
    let pushToClient: ((chunk: Uint8Array) => void) | undefined
    const stream = fakeStream()
    stream.read = ((onChunk: (chunk: Uint8Array) => void) => {
      pushToClient = onChunk
      return () => undefined
    }) as typeof stream.read
    const factory = vi.fn(async (options?: Record<string, unknown>) => {
      customRecv = options?.customRecv as typeof customRecv
      return module
    })
    await connectSSH({
      stream,
      username: 'root',
      password: 'password',
      hostKeyVerifier: () => true,
    }, factory as unknown as SSHModuleFactory)

    expect(pushToClient).toBeDefined()
    pushToClient!(new Uint8Array(5 * 1024 * 1024).fill(0x41))

    expect(stream.closeCount).toBe(0)
    expect(customRecv!(0, 1024)).toBe(1024)
  })

  it('verifies the host key and sends terminal resize dimensions', async () => {
    const module = fakeModule()
    const factory = vi.fn(async () => module)
    const verifier = vi.fn(() => true)
    const connection = await connectSSH({
      stream: fakeStream(),
      username: 'root',
      password: 'password',
      hostKeyVerifier: verifier,
    }, factory as unknown as SSHModuleFactory)

    expect(verifier).toHaveBeenCalledWith('sha256:A5BYxvLAy0ksUzsKTRTvd8wPeKvMztUofYShogEc+4E=')
    const channel = await connection.openShell()
    await channel.resize(120, 30)

    expect(module._ssh2_channel_request_pty_size).toHaveBeenCalledWith(expect.any(Number), 120, 30)
    await connection.close()
  })

  it('falls back to the KEX server host key when the WASM binding is unavailable', async () => {
    const key = new Uint8Array([1, 2, 3])
    const kexPayload = new Uint8Array([
      31,
      0, 0, 0, key.length, ...key,
      0, 0, 0, 1, 4,
      0, 0, 0, 1, 5,
    ])
    const padding = new Uint8Array(4)
    const packetLength = 1 + kexPayload.length + padding.length
    const packet = new Uint8Array([
      packetLength >>> 24 & 0xff,
      packetLength >>> 16 & 0xff,
      packetLength >>> 8 & 0xff,
      packetLength & 0xff,
      padding.length,
      ...kexPayload,
      ...padding,
    ])
    const module = fakeModule()
    delete (module as Record<string, unknown>)._ssh2_session_hostkey
    const verifier = vi.fn(() => true)
    const stream = {
      ...fakeStream(),
      read(onChunk: (chunk: Uint8Array) => void) {
        onChunk(new TextEncoder().encode('SSH-2.0-OpenSSH\r\n'))
        onChunk(packet)
        return () => undefined
      },
    }

    await connectSSH({
      stream,
      username: 'root',
      password: 'password',
      hostKeyVerifier: verifier,
    }, vi.fn(async () => module) as unknown as SSHModuleFactory)

    expect(verifier).toHaveBeenCalledWith('sha256:A5BYxvLAy0ksUzsKTRTvd8wPeKvMztUofYShogEc+4E=')
  })

  it('passes username and password to WASM as C string pointers', async () => {
    const heap = new Uint8Array(1024)
    const module = fakeModule({
      HEAPU8: heap,
      _malloc: vi.fn()
        .mockReturnValueOnce(700)
        .mockReturnValueOnce(800),
      _ssh2_userauth_password: vi.fn(() => 0),
    })

    await connectSSH({
      stream: fakeStream(),
      username: 'root',
      password: 'secret',
      hostKeyVerifier: () => true,
    }, vi.fn(async () => module) as unknown as SSHModuleFactory)

    expect(module._ssh2_userauth_password).toHaveBeenCalledWith(101, 700, 800)
    expect([...heap.slice(700, 705)]).toEqual([114, 111, 111, 116, 0])
    expect([...heap.slice(800, 807)]).toEqual([115, 101, 99, 114, 101, 116, 0])
    expect(module._free).toHaveBeenCalledWith(700)
    expect(module._free).toHaveBeenCalledWith(800)
  })

  it('passes public-key authentication data to WASM as C strings and lengths', async () => {
    const heap = new Uint8Array(4096)
    const module = fakeModule({
      HEAPU8: heap,
      _malloc: vi.fn()
        .mockReturnValueOnce(700)
        .mockReturnValueOnce(800)
        .mockReturnValueOnce(900)
        .mockReturnValueOnce(1000),
      _ssh2_userauth_publickey_frommemory: vi.fn(() => 0),
    })

    await connectSSH({
      stream: fakeStream(),
      username: 'root',
      privateKey: 'PRIVATE KEY DATA',
      hostKeyVerifier: () => true,
    }, vi.fn(async () => module) as unknown as SSHModuleFactory)

    expect(module._ssh2_userauth_publickey_frommemory).toHaveBeenCalledWith(
      101,
      700,
      800,
      0,
      900,
      16,
      1000,
    )
    expect(new TextDecoder().decode(heap.subarray(700, 705))).toBe('root\0')
    expect(new TextDecoder().decode(heap.subarray(800, 801))).toBe('\0')
    expect(new TextDecoder().decode(heap.subarray(900, 917))).toBe('PRIVATE KEY DATA\0')
    expect(new TextDecoder().decode(heap.subarray(1000, 1001))).toBe('\0')
    expect(module._free).toHaveBeenCalledWith(700)
    expect(module._free).toHaveBeenCalledWith(800)
    expect(module._free).toHaveBeenCalledWith(900)
    expect(module._free).toHaveBeenCalledWith(1000)
  })

  it('passes the PTY terminal type to WASM as a C string pointer', async () => {
    const heap = new Uint8Array(1024)
    const module = fakeModule({
      HEAPU8: heap,
      _malloc: vi.fn()
        .mockReturnValueOnce(700)
        .mockReturnValueOnce(800)
        .mockReturnValueOnce(900),
      _ssh2_channel_request_pty: vi.fn(() => 0),
    })
    const connection = await connectSSH({
      stream: fakeStream(),
      username: 'root',
      password: 'password',
      hostKeyVerifier: () => true,
    }, vi.fn(async () => module) as unknown as SSHModuleFactory)

    await connection.openShell()

    expect(module._ssh2_channel_request_pty).toHaveBeenCalledWith(201, 900)
    expect([...heap.slice(900, 915)]).toEqual([
      120, 116, 101, 114, 109, 45, 50, 53, 54, 99, 111, 108, 111, 114, 0,
    ])
    expect(module._free).toHaveBeenCalledWith(900)
  })

  it('retries opening a shell channel while libssh2 reports EAGAIN', async () => {
    const heap = new Uint8Array(1024)
    const module = fakeModule({
      HEAPU8: heap,
      _ssh2_channel_open_session: vi.fn()
        .mockReturnValueOnce(0)
        .mockReturnValueOnce(201),
      _ssh2_session_last_errno: vi.fn(() => -37),
      _malloc: vi.fn(() => 700),
      _ssh2_channel_request_pty: vi.fn(() => 0),
    })
    const connection = await connectSSH({
      stream: fakeStream(),
      username: 'root',
      password: 'password',
      hostKeyVerifier: () => true,
    }, vi.fn(async () => module) as unknown as SSHModuleFactory)

    await connection.openShell()

    expect(module._ssh2_channel_open_session).toHaveBeenCalledTimes(2)
    expect(module._ssh2_channel_request_pty).toHaveBeenCalledWith(201, 700)
  })

  it('buffers shell output received before a reader is registered', async () => {
    const heap = new Uint8Array(1024)
    heap.set(new TextEncoder().encode('hello'), 900)
    const module = fakeModule({
      HEAPU8: heap,
      _malloc: vi.fn()
        .mockReturnValueOnce(600)
        .mockReturnValueOnce(700)
        .mockReturnValueOnce(700)
        .mockReturnValueOnce(900),
      _ssh2_channel_open_session: vi.fn(() => 201),
      _ssh2_channel_read: vi.fn()
        .mockReturnValueOnce(5)
        .mockReturnValue(-37),
    })
    const connection = await connectSSH({
      stream: fakeStream(),
      username: 'root',
      password: 'password',
      hostKeyVerifier: () => true,
    }, vi.fn(async () => module) as unknown as SSHModuleFactory)
    const channel = await connection.openShell()

    const received: string[] = []
    channel.read((data) => received.push(new TextDecoder().decode(data)))

    expect(received).toEqual(['hello'])
  })

  it('notifies listeners when the remote shell exits', async () => {
    const module = fakeModule({
      _ssh2_channel_read: vi.fn(() => 0),
    })
    const connection = await connectSSH({
      stream: fakeStream(),
      username: 'root',
      password: 'password',
      hostKeyVerifier: () => true,
    }, vi.fn(async () => module) as unknown as SSHModuleFactory)
    const channel = await connection.openShell()
    const onExit = vi.fn()

    channel.onExit(onExit)
    await new Promise((resolve) => setTimeout(resolve, 30))

    expect(onExit).toHaveBeenCalledTimes(1)
  })

  it('does not report shell exit while reads keep returning EAGAIN', async () => {
    const module = fakeModule({
      _ssh2_channel_read: vi.fn(() => -37),
    })
    const connection = await connectSSH({
      stream: fakeStream(),
      username: 'root',
      password: 'password',
      hostKeyVerifier: () => true,
    }, vi.fn(async () => module) as unknown as SSHModuleFactory)
    const channel = await connection.openShell()
    const onExit = vi.fn()

    channel.onExit(onExit)
    await new Promise((resolve) => setTimeout(resolve, 60))

    expect(onExit).not.toHaveBeenCalled()
  })

  // The shell channel is non-blocking too: libssh2_channel_write can consume
  // only part of the buffer when the channel window is momentarily full.
  // Ignoring the remainder silently dropped bytes from long pastes.
  it('resends the remainder after a partial non-blocking shell write', async () => {
    const writes: { pointer: number; length: number }[] = []
    const module = fakeModule({
      _malloc: vi.fn(() => 32),
      _ssh2_channel_write: vi.fn((_channel: number, pointer: number, length: number) => {
        writes.push({ pointer, length })
        return writes.length === 1 ? 3 : length
      }),
    })
    const connection = await connectSSH({
      stream: fakeStream(),
      username: 'root',
      password: 'password',
      hostKeyVerifier: () => true,
    }, vi.fn(async () => module) as unknown as SSHModuleFactory)
    const channel = await connection.openShell()

    await channel.write(new Uint8Array([1, 2, 3, 4, 5]))

    expect(writes).toEqual([
      { pointer: 32, length: 5 },
      { pointer: 35, length: 2 },
    ])
  })

  // ZMODEM feeds the channel from synchronous protocol callbacks, so several
  // write() calls are in flight at the same time. When libssh2 consumes only
  // part of a buffer, an ungated continuation lets the second writer's bytes
  // land inside the first writer's frame; the peer reads that as corruption
  // (rz answers ZRPOS, sz aborts before "OO"). One writer at a time per
  // channel, without blocking the event loop.
  it('never interleaves concurrent shell writes when libssh2 consumes partially', async () => {
    const heap = new Uint8Array(1 << 17)
    const wire: number[] = []
    let nextPointer = 1024
    const module = fakeModule({
      HEAPU8: heap,
      _malloc: vi.fn((size: number) => {
        const pointer = nextPointer
        nextPointer += size
        return pointer
      }),
      _ssh2_channel_write: vi.fn((_channel: number, pointer: number, length: number) => {
        const consumed = Math.max(1, Math.floor(length / 2))
        wire.push(...heap.subarray(pointer, pointer + consumed))
        return consumed
      }),
    })
    const connection = await connectSSH({
      stream: fakeStream(),
      username: 'root',
      password: 'password',
      hostKeyVerifier: () => true,
    }, vi.fn(async () => module) as unknown as SSHModuleFactory)
    const channel = await connection.openShell()

    const first = channel.write(new Uint8Array([1, 1, 1, 1]))
    const second = channel.write(new Uint8Array([2, 2, 2, 2]))
    await Promise.all([first, second])

    expect(wire).toEqual([1, 1, 1, 1, 2, 2, 2, 2])
  })

  // A bulk `sz` pushes tens of megabytes of ZACK backlog into the outbound
  // channel window while lrzsz pauses to retransmit. The 5 s stall deadline
  // reads that pause as a dead peer and throws, and the rejection closes the
  // channel: a healthy 153 MiB download dies mid-transfer. Protocol writes
  // therefore have to be able to opt out of the deadline entirely.
  it('waits for window credit without a deadline when the stall limit is disabled', async () => {
    vi.useFakeTimers()
    try {
      const module = fakeModule({
        _malloc: vi.fn(() => 32),
        // Zero progress forever: the channel window is full and libssh2
        // accepts nothing.
        _ssh2_channel_write: vi.fn(() => 0),
      })
      const connection = await connectSSH({
        stream: fakeStream(),
        username: 'root',
        password: 'password',
        hostKeyVerifier: () => true,
      }, vi.fn(async () => module) as unknown as SSHModuleFactory)
      const channel = await connection.openShell()

      let failure: unknown = null
      let settled = false
      void channel.write(new Uint8Array([1, 2, 3]), { stallLimitMs: 0 })
        .then(() => { settled = true }, (error) => { failure = error })

      await vi.advanceTimersByTimeAsync(60_000)
      expect(failure).toBeNull()
      expect(settled).toBe(false)

      // Teardown must not queue behind a write that has no deadline, or
      // closeTerminal() hangs forever and leaks the libssh2 handles.
      let closed = false
      void channel.close().then(() => { closed = true })
      await vi.advanceTimersByTimeAsync(50)
      expect(closed).toBe(true)
      expect(module._ssh2_channel_free).toHaveBeenCalled()

      // The abandoned writer has to unwind instead of spinning on a channel
      // that has already been freed.
      await vi.advanceTimersByTimeAsync(50)
      expect(settled).toBe(true)
      expect(failure).toBeNull()
    } finally {
      vi.useRealTimers()
    }
  })

  // Guard for the change above: dropping the deadline has to be opt-in, and
  // the default bound IS the "this channel is dead" decision. Five seconds is
  // far too eager on a real WAN link: the tail of a bulk `sz` keeps the
  // outbound window congested well past that, and the rejection closes the
  // whole session ("SSH 通道已关闭") in the middle of a healthy download. The
  // decision now needs thirty seconds of zero progress.
  it('only reports a stalled shell write after thirty seconds of zero progress', async () => {
    vi.useFakeTimers()
    try {
      const module = fakeModule({
        _malloc: vi.fn(() => 32),
        _ssh2_channel_write: vi.fn(() => 0),
      })
      const connection = await connectSSH({
        stream: fakeStream(),
        username: 'root',
        password: 'password',
        hostKeyVerifier: () => true,
      }, vi.fn(async () => module) as unknown as SSHModuleFactory)
      const channel = await connection.openShell()

      let failure: unknown = null
      void channel.write(new Uint8Array([1, 2, 3])).then(() => undefined, (error) => { failure = error })

      await vi.advanceTimersByTimeAsync(20_000)
      expect(failure).toBeNull()
      await vi.advanceTimersByTimeAsync(15_000)
      expect(String(failure)).toContain('stalled')
    } finally {
      vi.useRealTimers()
    }
  })

  it('opens an SFTP channel on the authenticated SSH session and closes it with the connection', async () => {
    const module = fakeModule({
      _ssh2_sftp_init: vi.fn(() => 301),
      _ssh2_sftp_shutdown: vi.fn(() => 0),
    })
    const connection = await connectSSH({
      stream: fakeStream(),
      username: 'root',
      password: 'password',
      hostKeyVerifier: () => true,
    }, vi.fn(async () => module) as unknown as SSHModuleFactory)

    const sftp = await connection.openSFTP()
    expect(module._ssh2_sftp_init).toHaveBeenCalledWith(101)

    await connection.close()
    expect(module._ssh2_sftp_shutdown).toHaveBeenCalledWith(301)
  })

  // A bare "SFTP initialization failed" left operators guessing whether the
  // target lacks the subsystem, an agent policy denied it, or the tunnel
  // broke. The alert has to carry libssh2's own errno and message.
  it('reports the libssh2 message when the SFTP subsystem cannot be initialized', async () => {
    const module = fakeModule({
      _ssh2_sftp_init: vi.fn(() => 0),
      _ssh2_session_last_errno: vi.fn(() => -18),
      _ssh2_session_last_error: vi.fn(() => 'subsystem request failed'),
    })
    const connection = await connectSSH({
      stream: fakeStream(),
      username: 'root',
      password: 'password',
      hostKeyVerifier: () => true,
    }, vi.fn(async () => module) as unknown as SSHModuleFactory)

    await expect(connection.openSFTP()).rejects.toThrow('SFTP initialization failed: subsystem request failed')
  })

  it('falls back to the libssh2 errno when a failed SFTP init has no message', async () => {
    const module = fakeModule({
      _ssh2_sftp_init: vi.fn(() => 0),
      _ssh2_session_last_errno: vi.fn(() => -18),
      _ssh2_session_last_error: vi.fn(() => ''),
    })
    const connection = await connectSSH({
      stream: fakeStream(),
      username: 'root',
      password: 'password',
      hostKeyVerifier: () => true,
    }, vi.fn(async () => module) as unknown as SSHModuleFactory)

    await expect(connection.openSFTP()).rejects.toThrow('SFTP initialization failed: libssh2 error -18')
  })

  // libssh2 reports "would block" for handle-returning calls with a NULL
  // handle plus the session errno, never with -37 as the return value. Without
  // a handle-aware retry the very first non-blocking stall killed SFTP.
  it('retries SFTP init while libssh2 reports EAGAIN on the session', async () => {
    const init = vi.fn()
      .mockReturnValueOnce(0)
      .mockReturnValue(301)
    const module = fakeModule({
      _ssh2_sftp_init: init,
      _ssh2_session_last_errno: vi.fn().mockReturnValueOnce(-37).mockReturnValue(0),
      _ssh2_sftp_shutdown: vi.fn(() => 0),
    })
    const connection = await connectSSH({
      stream: fakeStream(),
      username: 'root',
      password: 'password',
      hostKeyVerifier: () => true,
    }, vi.fn(async () => module) as unknown as SSHModuleFactory)

    await expect(connection.openSFTP()).resolves.toBeTruthy()
    expect(init).toHaveBeenCalledTimes(2)
  })

  it('retries SFTP opendir while libssh2 reports EAGAIN on the session', async () => {
    const opendir = vi.fn()
      .mockReturnValueOnce(0)
      .mockReturnValue(501)
    const module = fakeModule({
      _ssh2_sftp_init: vi.fn(() => 301),
      _ssh2_sftp_opendir: opendir,
      _ssh2_session_last_errno: vi.fn().mockReturnValueOnce(-37).mockReturnValue(0),
      _ssh2_sftp_readdir: vi.fn(() => 0),
      _ssh2_sftp_close_handle: vi.fn(() => 0),
    })
    const connection = await connectSSH({
      stream: fakeStream(),
      username: 'root',
      password: 'password',
      hostKeyVerifier: () => true,
    }, vi.fn(async () => module) as unknown as SSHModuleFactory)
    const sftp = await connection.openSFTP()

    await expect(sftp.readDirectory('/')).resolves.toEqual([])
    expect(opendir).toHaveBeenCalledTimes(2)
  })

  // libssh2_sftp_realpath reports success as the resolved string length, not
  // zero: treating every non-zero result as failure turned a perfectly good
  // "/home/admin" into an error and left the file manager empty.
  it('reads the resolved path when realpath returns its length', async () => {
    const heap = new Uint8Array(4096)
    const module = fakeModule({
      HEAPU8: heap,
      _ssh2_sftp_init: vi.fn(() => 301),
      _ssh2_sftp_realpath: vi.fn((_sftp: number, _source: number, target: number) => {
        heap.set([...new TextEncoder().encode('/home/admin'), 0], target)
        return '/home/admin'.length
      }),
    })
    const connection = await connectSSH({
      stream: fakeStream(),
      username: 'root',
      password: 'password',
      hostKeyVerifier: () => true,
    }, vi.fn(async () => module) as unknown as SSHModuleFactory)
    const sftp = await connection.openSFTP()

    await expect(sftp.realpath('.')).resolves.toBe('/home/admin')
  })

  it('passes SFTP operation paths to WASM as NUL-terminated UTF-8 pointers', async () => {
    const heap = new Uint8Array(4096)
    let allocations = 0
    const module = fakeModule({
      HEAPU8: heap,
      _malloc: vi.fn(() => {
        allocations += 1
        return allocations === 3 ? 700 : 100 + allocations * 100
      }),
      _ssh2_sftp_init: vi.fn(() => 301),
      _ssh2_sftp_unlink: vi.fn(() => 0),
    })
    const connection = await connectSSH({
      stream: fakeStream(),
      username: 'root',
      password: 'password',
      hostKeyVerifier: () => true,
    }, vi.fn(async () => module) as unknown as SSHModuleFactory)
    const sftp = await connection.openSFTP()

    await sftp.unlink('/tmp/报告.log')

    expect(module._ssh2_sftp_unlink).toHaveBeenCalledWith(301, 700)
    expect([...heap.slice(700, 716)]).toEqual([
      ...new TextEncoder().encode('/tmp/报告.log'), 0,
    ])
    expect(module._free).toHaveBeenCalledWith(700)
  })

  // libssh2 runs non-blocking here, so libssh2_sftp_write returns the bytes it
  // consumed so far whenever the channel window or the tunnel stalls: a
  // positive value smaller than the request, not EAGAIN. Treating that as
  // completion truncated every upload bigger than one flush, and the UI could
  // only say "transfer failed". The write must feed the remainder back.
  it('keeps writing the remainder after a partial non-blocking SFTP write', async () => {
    const heap = new Uint8Array(8192)
    const writes: { pointer: number; length: number; bytes: number[] }[] = []
    const module = fakeModule({
      HEAPU8: heap,
      _malloc: vi.fn(() => 16),
      _ssh2_sftp_init: vi.fn(() => 301),
      _ssh2_sftp_open: vi.fn(() => 401),
      _ssh2_sftp_close_handle: vi.fn(() => 0),
      _ssh2_sftp_write: vi.fn((_handle: number, pointer: number, length: number) => {
        writes.push({ pointer, length, bytes: [...heap.subarray(pointer, pointer + length)] })
        // The first call only flushes 100 of the 256 requested bytes.
        return writes.length === 1 ? 100 : length
      }),
    })
    const connection = await connectSSH({
      stream: fakeStream(),
      username: 'root',
      password: 'password',
      hostKeyVerifier: () => true,
    }, vi.fn(async () => module) as unknown as SSHModuleFactory)
    const sftp = await connection.openSFTP()
    const handle = await sftp.openFile('/tmp/up.bin', 'write')

    const chunk = new Uint8Array(256)
    for (let i = 0; i < chunk.length; i++) chunk[i] = i % 251
    await expect(handle.write(chunk)).resolves.toBe(256)

    expect(writes).toHaveLength(2)
    expect(writes[0]).toMatchObject({ pointer: 16, length: 256 })
    // The retry must resume at the first unconsumed byte, not resend the head.
    expect(writes[1]).toMatchObject({ pointer: 116, length: 156 })
    expect(writes[1].bytes).toEqual([...chunk.subarray(100)])
    await handle.close()
  })

  // The SFTP channel is the same non-blocking session: an upload chunk that
  // is only partially flushed must not let a concurrent request (a listing
  // refresh or a rename while an upload runs) splice its packet bytes.
  it('never interleaves an SFTP write with a concurrent SFTP request', async () => {
    const log: string[] = []
    const module = fakeModule({
      _malloc: vi.fn(() => 16),
      _ssh2_sftp_init: vi.fn(() => 301),
      _ssh2_sftp_open: vi.fn(() => 401),
      _ssh2_sftp_close_handle: vi.fn(() => 0),
      _ssh2_sftp_write: vi.fn((_handle: number, _pointer: number, length: number) => {
        const consumed = Math.max(1, Math.floor(length / 2))
        log.push(`write:${consumed}`)
        return consumed
      }),
      _ssh2_sftp_unlink: vi.fn(() => {
        log.push('unlink')
        return 0
      }),
    })
    const connection = await connectSSH({
      stream: fakeStream(),
      username: 'root',
      password: 'password',
      hostKeyVerifier: () => true,
    }, vi.fn(async () => module) as unknown as SSHModuleFactory)
    const sftp = await connection.openSFTP()
    const handle = await sftp.openFile('/tmp/up.bin', 'write')

    const writing = handle.write(new Uint8Array(64))
    const unlinking = sftp.unlink('/tmp/other.bin')
    await Promise.all([writing, unlinking])

    // Every continuation of the write must precede the unrelated request.
    expect(log[log.length - 1]).toBe('unlink')
    expect(log.filter(entry => entry.startsWith('write:')).length).toBeGreaterThan(1)
    await handle.close()
  })

  // A peer that accepts nothing at all must surface as an error instead of
  // spinning the retry loop forever on a dead tunnel - but only after the same
  // thirty-second window the shell write uses. A congested tunnel during a
  // bulk transfer is not dead, and SFTP uploads share that congestion.
  it('fails an SFTP write only after thirty seconds of zero progress', async () => {
    vi.useFakeTimers()
    try {
      const module = fakeModule({
        _ssh2_sftp_init: vi.fn(() => 301),
        _ssh2_sftp_open: vi.fn(() => 401),
        _ssh2_sftp_close_handle: vi.fn(() => 0),
        _ssh2_sftp_write: vi.fn(() => 0),
      })
      const connection = await connectSSH({
        stream: fakeStream(),
        username: 'root',
        password: 'password',
        hostKeyVerifier: () => true,
      }, vi.fn(async () => module) as unknown as SSHModuleFactory)
      const sftp = await connection.openSFTP()
      const handle = await sftp.openFile('/tmp/up.bin', 'write')

      // Attach the handler synchronously: the rejection fires while the timers
      // are still being advanced, and a late attach would surface as an
      // unhandled rejection and fail the run.
      let caught: unknown = null
      const pending = handle.write(new Uint8Array(64)).catch((error: unknown) => {
        caught = error
        return 0
      })
      for (let i = 0; i < 2000; i++) await vi.advanceTimersByTimeAsync(10)
      expect(caught).toBeNull()
      for (let i = 0; i < 1600; i++) await vi.advanceTimersByTimeAsync(10)
      await pending
      expect(caught).toBeInstanceOf(Error)
      expect((caught as Error).message).toContain('SFTP write stalled')
    } finally {
      vi.useRealTimers()
    }
  })

  it('passes rename and real-path arguments to WASM as C string pointers', async () => {
    const heap = new Uint8Array(8192)
    let allocations = 0
    const module = fakeModule({
      HEAPU8: heap,
      _malloc: vi.fn((size: number) => {
        if (size === 4096) return 4000
        allocations += 1
        if (allocations === 3) return 700
        if (allocations === 4) return 800
        if (allocations === 5) return 900
        return 100 + allocations * 100
      }),
      _ssh2_sftp_init: vi.fn(() => 301),
      _ssh2_sftp_rename: vi.fn(() => 0),
      _ssh2_sftp_realpath: vi.fn(() => 0),
    })
    const connection = await connectSSH({
      stream: fakeStream(),
      username: 'root',
      password: 'password',
      hostKeyVerifier: () => true,
    }, vi.fn(async () => module) as unknown as SSHModuleFactory)
    const sftp = await connection.openSFTP()

    await sftp.rename('/tmp/old', '/tmp/new')
    await expect(sftp.realpath('/tmp/link')).resolves.toBe('')

    expect(module._ssh2_sftp_rename).toHaveBeenCalledWith(301, 700, 800)
    expect(new TextDecoder().decode(heap.subarray(700, 708))).toBe('/tmp/old')
    expect(new TextDecoder().decode(heap.subarray(800, 808))).toBe('/tmp/new')
    expect(module._ssh2_sftp_realpath).toHaveBeenCalledWith(301, 900, 4000, 4096)
    expect(module._free).toHaveBeenCalledWith(700)
    expect(module._free).toHaveBeenCalledWith(800)
    expect(module._free).toHaveBeenCalledWith(900)
    expect(module._free).toHaveBeenCalledWith(4000)
  })

  it('passes the disconnect reason to WASM as a C string pointer', async () => {
    const heap = new Uint8Array(1024)
    const module = fakeModule({ HEAPU8: heap, _malloc: vi.fn(() => 700) })
    const connection = await connectSSH({
      stream: fakeStream(),
      username: 'root',
      password: 'password',
      hostKeyVerifier: () => true,
    }, vi.fn(async () => module) as unknown as SSHModuleFactory)

    await connection.close()

    expect(module._ssh2_session_disconnect).toHaveBeenCalledWith(101, 700)
    expect(new TextDecoder().decode(heap.subarray(700, 714))).toBe('WebSSH closed\0')
    expect(module._free).toHaveBeenCalledWith(700)
  })

  it('converts WASM error-message pointers in SFTP errors', async () => {
    const heap = new Uint8Array(1024)
    heap.set(new TextEncoder().encode('Permission denied'), 100)
    const module = fakeModule({
      HEAPU8: heap,
      _ssh2_sftp_init: vi.fn(() => 301),
      _ssh2_sftp_last_error: vi.fn(() => 3),
      _ssh2_session_last_error: vi.fn(() => 100),
      _ssh2_sftp_unlink: vi.fn(() => -32),
    })
    const connection = await connectSSH({
      stream: fakeStream(),
      username: 'root',
      password: 'password',
      hostKeyVerifier: () => true,
    }, vi.fn(async () => module) as unknown as SSHModuleFactory)
    const sftp = await connection.openSFTP()

    await expect(sftp.unlink('/tmp/protected')).rejects.toThrow('SFTP delete failed: Permission denied')
  })

  it('reads SFTP directory entry names without UTF8ToString', async () => {
    const heap = new Uint8Array(1024)
    heap.set(new TextEncoder().encode('report.log'), 100)
    const view = new DataView(heap.buffer)
    view.setInt32(300, 13, true)
    view.setInt32(308, 42, true)
    view.setInt32(312, 0, true)
    view.setInt32(324, 0o100644, true)
    view.setInt32(332, 1783008000, true)
    let namePointer = 100
    const module = fakeModule({
      HEAPU8: heap,
      getValue: vi.fn((pointer: number, type: string) => {
        if (type !== 'i32') throw new Error(`unsupported type: ${type}`)
        return view.getInt32(pointer, true)
      }),
      _malloc: vi.fn((size: number) => {
        if (size === 4096) return namePointer++
        if (size === 40) return 300
        return 400
      }),
      _ssh2_sftp_init: vi.fn(() => 301),
      _ssh2_sftp_opendir: vi.fn(() => 401),
      _ssh2_sftp_readdir: vi.fn()
        .mockReturnValueOnce(1)
        .mockReturnValueOnce(0),
      _ssh2_sftp_close_handle: vi.fn(() => 0),
      _ssh2_sftp_last_error: vi.fn(() => 0),
    })
    const connection = await connectSSH({
      stream: fakeStream(),
      username: 'root',
      password: 'password',
      hostKeyVerifier: () => true,
    }, vi.fn(async () => module) as unknown as SSHModuleFactory)
    const sftp = await connection.openSFTP()

    await expect(sftp.readDirectory('/var/log')).resolves.toEqual([{
      name: 'report.log',
      type: 'file',
      size: 42,
      modifiedAt: new Date(1783008000 * 1000).toISOString(),
      permissions: '-rw-r--r--',
    }])
  })

  it('reports the remote exit status when the shell ends', async () => {
    const module = fakeModule({
      _ssh2_channel_read: vi.fn(() => 0),
      _ssh2_channel_get_exit_status: vi.fn(() => 1),
      _ssh2_channel_get_exit_signal: vi.fn(() => ''),
    })
    const connection = await connectSSH({
      stream: fakeStream(),
      username: 'root',
      password: 'password',
      hostKeyVerifier: () => true,
    }, vi.fn(async () => module) as unknown as SSHModuleFactory)
    const channel = await connection.openShell()
    const onExit = vi.fn()

    channel.onExit(onExit)
    await new Promise((resolve) => setTimeout(resolve, 30))

    expect(onExit).toHaveBeenCalledWith({ status: 1, signal: null, transportError: null })
    expect(module._ssh2_channel_get_exit_status).toHaveBeenCalledWith(201)

    // A listener attached after the shell already ended must still receive the
    // exit information, otherwise a late render shows no reason at all.
    const late = vi.fn()
    channel.onExit(late)
    expect(late).toHaveBeenCalledWith({ status: 1, signal: null, transportError: null })
  })

  it('decodes an exit signal returned as a WASM heap pointer', async () => {
    const heap = new Uint8Array(1024)
    heap.set(new TextEncoder().encode('SIGTERM'), 200)
    const module = fakeModule({
      HEAPU8: heap,
      _ssh2_channel_read: vi.fn(() => 0),
      _ssh2_channel_get_exit_status: vi.fn(() => 0),
      _ssh2_channel_get_exit_signal: vi.fn(() => 200),
    })
    const connection = await connectSSH({
      stream: fakeStream(),
      username: 'root',
      password: 'password',
      hostKeyVerifier: () => true,
    }, vi.fn(async () => module) as unknown as SSHModuleFactory)
    const channel = await connection.openShell()
    const onExit = vi.fn()

    channel.onExit(onExit)
    await new Promise((resolve) => setTimeout(resolve, 30))

    expect(onExit).toHaveBeenCalledWith({ status: 0, signal: 'SIGTERM', transportError: null })
  })

  it('degrades to null exit information when the WASM build lacks the bindings', async () => {
    const module = fakeModule({ _ssh2_channel_read: vi.fn(() => 0) })
    const connection = await connectSSH({
      stream: fakeStream(),
      username: 'root',
      password: 'password',
      hostKeyVerifier: () => true,
    }, vi.fn(async () => module) as unknown as SSHModuleFactory)
    const channel = await connection.openShell()
    const onExit = vi.fn()

    channel.onExit(onExit)
    await new Promise((resolve) => setTimeout(resolve, 30))

    expect(onExit).toHaveBeenCalledWith({ status: null, signal: null, transportError: null })
  })

  // Auto-authentication stores an encrypted private key on the server, and an
  // encrypted key is useless without its passphrase, so the passphrase must
  // reach libssh2 instead of being hardcoded to an empty C string.
  it('passes the stored private key and passphrase to publickey authentication', async () => {
    const heap = new Uint8Array(4096)
    let cursor = 8
    const module = fakeModule({
      HEAPU8: heap,
      _malloc: vi.fn((size: number) => {
        const pointer = cursor
        cursor += size + 8
        return pointer
      }),
    })

    await connectSSH({
      stream: fakeStream(),
      username: 'root',
      privateKey: 'private-key-material',
      passphrase: 'key-passphrase',
      hostKeyVerifier: () => true,
    }, vi.fn(async () => module) as unknown as SSHModuleFactory)

    const call = module._ssh2_userauth_publickey_frommemory.mock.calls[0]
    expect(readCString(heap, call[4] as number)).toBe('private-key-material')
    expect(readCString(heap, call[6] as number)).toBe('key-passphrase')
    expect(module._ssh2_userauth_password).not.toHaveBeenCalled()
  })

  it('authenticates with an unencrypted private key when no passphrase is stored', async () => {
    const heap = new Uint8Array(4096)
    let cursor = 8
    const module = fakeModule({
      HEAPU8: heap,
      _malloc: vi.fn((size: number) => {
        const pointer = cursor
        cursor += size + 8
        return pointer
      }),
    })

    await connectSSH({
      stream: fakeStream(),
      username: 'root',
      privateKey: 'private-key-material',
      hostKeyVerifier: () => true,
    }, vi.fn(async () => module) as unknown as SSHModuleFactory)

    const call = module._ssh2_userauth_publickey_frommemory.mock.calls[0]
    expect(readCString(heap, call[6] as number)).toBe('')
  })
})

function readCString(heap: Uint8Array, pointer: number): string {
  let end = pointer
  while (end < heap.length && heap[end] !== 0) end += 1
  return new TextDecoder().decode(heap.subarray(pointer, end))
}
