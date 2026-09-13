import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import {
  forgetRememberedWebSSHTarget,
  readRememberedWebSSHTarget,
  rememberWebSSHTarget,
  useWebSSHStore,
} from '../stores/webssh'

describe('WebSSH memory store', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
  })

  it('takes the pending session once and keeps its reconnect target', () => {
    const store = useWebSSHStore()
    store.setPendingSession({
      ticket: {
        sessionId: 'session-1',
        ticket: 'one-time-ticket',
        websocketPath: '/ws/ssh/session-1',
      },
      username: 'root',
      password: 'secret',
      credentialId: 'credential-1',
      remoteServerId: 'server-1',
    })

    // The terminal needs the server ID to offer a reconnect after the remote
    // shell exits; the password itself is still dropped on close.
    expect(store.takePendingSession('session-1'))
      .toMatchObject({ username: 'root', password: 'secret', remoteServerId: 'server-1' })
    expect(store.pendingSession).toBeNull()
    expect(store.takePendingSession('session-1')).toBeNull()
  })

  it('closes and clears an active connection without retaining credentials', async () => {
    const store = useWebSSHStore()
    const close = vi.fn(async () => undefined)
    store.registerActiveSession(
      'session-1',
      { username: 'root', password: 'secret' },
      { close, openShell: vi.fn(), openSFTP: vi.fn() },
      'server-1',
    )

    await store.closeActiveSession()

    expect(close).toHaveBeenCalledTimes(1)
    expect(store.activeSession).toBeNull()
  })

  // The terminal is remounted when the user comes back from SFTP, so the
  // reconnect target has to survive on the shared session, not in the view.
  it('keeps the reconnect target on the shared session', () => {
    const store = useWebSSHStore()
    store.registerActiveSession(
      'session-1',
      { username: 'root' },
      { close: vi.fn(async () => undefined), openShell: vi.fn(), openSFTP: vi.fn() },
      'server-1',
    )

    expect(store.activeSession?.remoteServerId).toBe('server-1')
  })
})

// A browser refresh drops the in-memory one-time ticket. Only the
// non-sensitive target id is remembered, so the terminal and SFTP views can
// route back to the server list instead of dead-ending on "credentials missing".
describe('remembered WebSSH reconnect target', () => {
  beforeEach(() => {
    window.sessionStorage.clear()
  })

  it('survives a refresh and stays scoped to its session', () => {
    rememberWebSSHTarget('session-1', 'server-1')

    expect(readRememberedWebSSHTarget('session-1')).toBe('server-1')
    expect(readRememberedWebSSHTarget('session-2')).toBe('')
  })

  it('ignores an empty target and forgets it on explicit disconnect', () => {
    rememberWebSSHTarget('session-1', '')
    expect(readRememberedWebSSHTarget('session-1')).toBe('')

    rememberWebSSHTarget('session-1', 'server-1')
    forgetRememberedWebSSHTarget('session-1')
    expect(readRememberedWebSSHTarget('session-1')).toBe('')
  })

  it('tolerates a browser that blocks sessionStorage', () => {
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new Error('denied')
    })
    try {
      rememberWebSSHTarget('session-1', 'server-1')
      expect(readRememberedWebSSHTarget('session-1')).toBe('')
    } finally {
      vi.restoreAllMocks()
    }
  })
})
