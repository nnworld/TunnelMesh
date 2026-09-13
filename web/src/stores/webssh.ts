import { defineStore } from 'pinia'
import type { WebSSHTicket } from '../api/webssh'
import type { SFTPClient } from '../webssh/sftp'
import type { SSHConnection, TerminalChannel } from '../webssh/ssh-client'

export type PendingWebSSHSession = {
  ticket: WebSSHTicket
  username: string
  password?: string
  privateKey?: string
  passphrase?: string
  credentialId?: string
  // Non-sensitive target identifier kept so a closed terminal can offer a
  // reconnect. Credentials are still cleared when the session ends.
  remoteServerId?: string
}

type ActiveWebSSHCredentials = {
  username: string
  password?: string
  privateKey?: string
  passphrase?: string
  credentialId?: string
}

/**
 * A shared connection must be able to open both a shell and an SFTP session:
 * the terminal and the file manager are two views over one authenticated SSH
 * transport, so the store keeps the full connection rather than a narrowed
 * SFTP-only slice.
 */
interface ActiveSSHConnection {
  openShell(): Promise<TerminalChannel>
  openSFTP(): Promise<SFTPClient>
  close(): Promise<void>
}

type ActiveWebSSHSession = ActiveWebSSHCredentials & {
  sessionId: string
  connection: ActiveSSHConnection
  // Non-sensitive target ID, kept so a view remounted on an existing
  // connection (terminal -> SFTP -> terminal) can still offer a reconnect.
  remoteServerId?: string
}

/**
 * A browser refresh drops the in-memory one-time ticket, which used to dead-end
 * the terminal and SFTP views on "credentials missing". Remembering only the
 * non-sensitive target id lets either view route back to the server list with
 * the reconnect flow pre-targeted. Credentials are never persisted; auto-auth
 * still happens against the stored credential on the server.
 */
const rememberedTargetPrefix = 'tunnelmesh.webssh.target.'

export function rememberWebSSHTarget(sessionId: string, remoteServerId: string) {
  if (!sessionId || !remoteServerId) return
  try {
    window.sessionStorage.setItem(rememberedTargetPrefix + sessionId, remoteServerId)
  } catch {
    // Storage may be disabled; the in-page reconnect button still works.
  }
}

export function readRememberedWebSSHTarget(sessionId: string) {
  if (!sessionId) return ''
  try {
    return window.sessionStorage.getItem(rememberedTargetPrefix + sessionId) || ''
  } catch {
    return ''
  }
}

export function forgetRememberedWebSSHTarget(sessionId: string) {
  if (!sessionId) return
  try {
    window.sessionStorage.removeItem(rememberedTargetPrefix + sessionId)
  } catch {
    // Nothing to clean up when storage is unavailable.
  }
}

export const useWebSSHStore = defineStore('webssh', {
  state: () => ({
    pendingSession: null as PendingWebSSHSession | null,
    activeSession: null as ActiveWebSSHSession | null,
  }),
  actions: {
    setPendingSession(session: PendingWebSSHSession) { this.pendingSession = session },
    takePendingSession(sessionId: string) {
      const session = this.pendingSession
      if (session?.ticket.sessionId !== sessionId) return null
      this.pendingSession = null
      return session
    },
    clearPendingSession() { this.pendingSession = null },
    registerActiveSession(
      sessionId: string,
      session: ActiveWebSSHCredentials,
      connection: ActiveSSHConnection | SSHConnection,
      remoteServerId?: string,
    ) {
      if (this.activeSession?.sessionId !== sessionId) {
        this.activeSession = { ...session, sessionId, connection, remoteServerId }
      }
    },
    clearActiveSession() {
      this.activeSession = null
    },
    async closeActiveSession() {
      const session = this.activeSession
      this.activeSession = null
      if (session) await session.connection.close()
    },
  },
})
