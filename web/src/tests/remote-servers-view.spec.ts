import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'

describe('remote servers management view', () => {
  it('renders filters, list, lifecycle, detail, and state surfaces', () => {
    const source = readFileSync('src/views/RemoteServers.vue', 'utf8')
    for (const marker of [
      'listRemoteServers', 'createRemoteServer', 'updateRemoteServer', 'deleteRemoteServer', 'restoreRemoteServer', 'getRemoteServer',
      'DataState', 'el-table', 'filterKeyword', 'filterAgentId', 'filterStatus', 'loadError', 'loading', 'items.length', 'detailVisible',
    ]) expect(source, marker).toContain(marker)
    expect(source).toContain('agentId: filterAgentId.value || undefined')
    expect(source).toContain('status: filterStatus.value')
  })

  it('validates and binds every remote-server form field', () => {
    const source = readFileSync('src/views/RemoteServers.vue', 'utf8')
    for (const marker of [
      'form.name', 'form.host', 'form.port', 'form.defaultUsername', 'form.credentialId', 'form.agentId', 'form.enabled', 'rules',
    ]) expect(source, marker).toContain(marker)
    expect(source).toContain("type: 'number'")
    expect(source).toContain('min: 1')
    expect(source).toContain('max: 65535')
  })

  it('selects only active credentials and enabled agents', () => {
    const source = readFileSync('src/views/RemoteServers.vue', 'utf8')
    expect(source).toContain('activeCredentials')
    expect(source).toContain('credential.enabled && credential.status === \'active\'')
    expect(source).toContain('enabledAgents')
    expect(source).toContain('agent.enabled')
  })

  it('supports edit, details, SSH, logical delete, restore, and server errors', () => {
    const source = readFileSync('src/views/RemoteServers.vue', 'utf8')
    for (const marker of [
      'openEdit', 'showDetail', 'openSSH', 'remove', 'restore', 'row.lastErrorClass', 'sshError', 'agentOnline',
    ]) expect(source, marker).toContain(marker)
  })

  it('surfaces the server-provided WebSSH session creation reason', () => {
    const source = readFileSync('src/views/RemoteServers.vue', 'utf8')
    const errors = readFileSync('src/views/remote-server-errors.ts', 'utf8')
    const zh = readFileSync('src/i18n/messages/zh-CN.ts', 'utf8')
    expect(source).toContain("from './remote-server-errors'")
    expect(source).toContain('sshErrorMessage(error, t)')
    expect(errors).toContain('error instanceof APIError')
    expect(errors).toContain('error.domain')
    expect(errors).toContain('remoteServers.errors.agentOffline')
    expect(errors).toContain('remoteServers.errors.limitReached')
    expect(zh).toContain('agentOffline:')
    expect(zh).toContain('limitReached:')
  })

  it('opens an SSH auth dialog without persisting the password', () => {
    const source = readFileSync('src/views/RemoteServers.vue', 'utf8')
    for (const marker of [
      'sshVisible', 'sshUsername', 'sshPassword', 'type="password"', 'createWebSSHSession', 'clearSSHPassword', 'router.push',
      'useWebSSHStore', 'setPendingSession',
    ]) expect(source, marker).toContain(marker)
    expect(source).not.toContain('localStorage')
    expect(source).not.toContain('sessionStorage')
  })

  it('supports opening the same authenticated session as SFTP', () => {
    const source = readFileSync('src/views/RemoteServers.vue', 'utf8')
    expect(source).toContain('openSFTP')
    expect(source).toContain("sshTargetMode.value = 'sftp'")
    expect(source).toContain("sshTargetMode.value === 'sftp'")
  })

  it('auto-authenticates with a stored credential secret and falls back to the dialog', () => {
    const source = readFileSync('src/views/RemoteServers.vue', 'utf8')
    for (const marker of [
      'canAutoAuth', 'connectWithStoredCredential', 'row.credentialHasSecret', 'ticket.auth',
      "auth.kind === 'private_key'", 'passphrase:', 'prepareSSHDialog(row)',
    ]) expect(source, marker).toContain(marker)
    // The one-time material must stay in the memory store, never in web storage.
    expect(source).toContain('setPendingSession')
    expect(source).not.toContain('localStorage')
    expect(source).not.toContain('sessionStorage')
  })

  it('reports the credential capability in the server list and detail views', () => {
    const source = readFileSync('src/views/RemoteServers.vue', 'utf8')
    expect(source).toContain('credentialHasSecret')
    expect(source).toContain("t('remoteServers.autoAuth')")
    expect(source).toContain("t('remoteServers.manualAuth')")
    expect(source).toContain('sshAutoAuthLoading')
  })

  it('lists the caller own active sessions so a stuck quota can be released', () => {
    const source = readFileSync('src/views/RemoteServers.vue', 'utf8')
    for (const marker of [
      'listWebSSHSessions', 'closeWebSSHSession', 'activeSessions', 'sessionsLoading', 'sessionsError',
      'disconnectSession', 'sessionTargetName', "t('remoteServers.sessions.title')", 'el-popconfirm',
    ]) expect(source, marker).toContain(marker)
    // The card must not depend on a session row carrying the server name: the
    // API only returns IDs, so the view resolves names from the loaded servers.
    expect(source).toContain('unknownTarget')
  })

  it('defines bilingual active-session messages', () => {
    for (const file of ['src/i18n/messages/zh-CN.ts', 'src/i18n/messages/en-US.ts']) {
      const messages = readFileSync(file, 'utf8')
      expect(messages, file).toContain('sessions: {')
      for (const key of ['title:', 'target:', 'disconnect:', 'disconnected:', 'disconnectFailed:', 'empty:', 'loadFailed:', 'unknownTarget:']) {
        expect(messages.slice(messages.indexOf('sessions: {')), `${file} ${key}`).toContain(key)
      }
    }
  })

  it('reopens the SSH dialog when a closed terminal routes back for reconnect', () => {
    const source = readFileSync('src/views/RemoteServers.vue', 'utf8')
    expect(source).toContain('useRoute')
    expect(source).toContain('openSSHFromQuery')
    expect(source).toContain('ssh: undefined')
    // The terminal can only reconnect to the server it was opened from, so the
    // pending session must carry the server ID (never the password).
    expect(source).toContain('remoteServerId: sshServer.value.id')
  })

  // The server table is the primary task on this page; active sessions are a
  // secondary recovery surface, so they must not push the list below the fold.
  it('renders the active-session card below the server table', () => {
    const source = readFileSync('src/views/RemoteServers.vue', 'utf8')
    const tableIndex = source.indexOf('class="tm-card table-card"')
    const sessionsIndex = source.indexOf('class="tm-card sessions-card"')
    expect(tableIndex).toBeGreaterThan(-1)
    expect(sessionsIndex).toBeGreaterThan(-1)
    expect(sessionsIndex).toBeGreaterThan(tableIndex)
  })

})
