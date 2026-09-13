import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'

describe('Web SFTP view', () => {
  it('reuses the authenticated SSH connection and exposes file operations', () => {
    const source = readFileSync('src/views/WebSFTP.vue', 'utf8')
    for (const marker of [
      'useWebSSHStore',
      'store.activeSession',
      'activeConnection.value.openSFTP',
      'listDirectory',
      'uploadFile',
      'downloadFile',
      'deleteEntry',
      'renameEntry',
      'SFTP_MAX_TRANSFER_BYTES',
      'progress',
      'currentPath',
      'el-table',
      'type="file"',
    ]) expect(source, marker).toContain(marker)
    expect(source).not.toContain("fetch('/api")
    expect(source).not.toContain('localStorage')
    expect(source).not.toContain('sessionStorage')
  })

  it('requires confirmation for destructive operations and resets progress', () => {
    const source = readFileSync('src/views/WebSFTP.vue', 'utf8')
    expect(source).toContain('ElMessageBox.confirm')
    expect(source).toContain('deleteConfirm')
    expect(source).toContain('renameTitle')
    expect(source).toContain('progress.value = { path: \'\', transferred: 0, total: 0 }')
  })

  it('maps SFTP failures to stable interface states and closes the shared connection', () => {
    const source = readFileSync('src/views/WebSFTP.vue', 'utf8')
    for (const marker of [
      'SFTPError',
      "'permission-denied'",
      "'not-found'",
      'transferError',
      'loadError',
      'closeActiveSession',
      'onBeforeRouteLeave',
    ]) expect(source, marker).toContain(marker)
  })

  it('registers the SFTP route without replacing the terminal route', () => {
    const router = readFileSync('src/router.ts', 'utf8')
    expect(router).toContain("path:'/webssh/:sessionId/sftp'")
    expect(router).toContain('WebSFTP')
  })

  it('provides bilingual labels for every SFTP surface', () => {
    for (const locale of ['zh-CN', 'en-US']) {
      const source = readFileSync(`src/i18n/messages/${locale}.ts`, 'utf8')
      expect(source).toContain('websftp: {')
      expect(source).toContain('title:')
      expect(source).toContain('permissionDenied:')
      expect(source).toContain('notFound:')
    }
  })

  // Opening SFTP straight from the server list creates a ticket but no shell,
  // so the view used to fail with a missing active session and render a silent
  // empty directory. It must bootstrap the SSH connection from the pending
  // session exactly like the terminal does.
  it('bootstraps its own SSH connection when opened directly from the server list', () => {
    const source = readFileSync('src/views/WebSFTP.vue', 'utf8')
    for (const marker of [
      'takePendingSession', 'connectSSH', 'createWebSocketByteStream', 'registerActiveSession',
      'websshWebSocketURL', 'useHostKeyVerifier',
    ]) expect(source, marker).toContain(marker)
  })

  it('surfaces connect failures instead of a silent empty directory', () => {
    const source = readFileSync('src/views/WebSFTP.vue', 'utf8')
    const connectBlock = source.slice(source.indexOf('async function connect()'), source.indexOf('async function reload()'))
    expect(connectBlock).toContain("status.value = 'error'")
    expect(connectBlock).toContain('loadError.value = true')
  })

  it('returns to the server list after disconnecting', () => {
    const source = readFileSync('src/views/WebSFTP.vue', 'utf8')
    expect(source).toContain('disconnectAndLeave')
    expect(source).toContain("router.replace('/remote-servers')")
  })
})
