import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'

describe('WebSSH terminal view', () => {
  it('wires xterm, fit, resize, and connection lifecycle', () => {
    const source = readFileSync('src/views/WebSSHTerminal.vue', 'utf8')
    for (const marker of [
      '@xterm/xterm',
      '@xterm/addon-fit',
      'FitAddon',
      'createWebSocketByteStream',
      'connectSSH',
      'openShell',
      'channel.resize',
      'closeActiveSession',
      'onBeforeRouteLeave',
      'beforeunload',
      "status.value = 'connected'",
      "status.value = 'error'",
      "status.value = 'closed'",
    ]) expect(source, marker).toContain(marker)
  })

  it('keeps credentials in memory and clears them on close', () => {
    const source = readFileSync('src/views/WebSSHTerminal.vue', 'utf8')
    expect(source).toContain('takePendingSession')
    expect(source).not.toContain('localStorage')
    expect(source).not.toContain('sessionStorage')
  })

  it('requires explicit host-key confirmation instead of auto-accepting', () => {
    const source = readFileSync('src/views/WebSSHTerminal.vue', 'utf8')
    const hostKey = readFileSync('src/webssh/host-key.ts', 'utf8')
    expect(source).toContain('verifyHostKey')
    expect(source).toContain('useHostKeyVerifier')
    // The confirmation dialog is shared with the SFTP-only entry point, so it
    // lives in one module instead of drifting per view.
    expect(hostKey).toContain('ElMessageBox.confirm')
    expect(source).not.toContain('hostKeyVerifier: () => true')
  })

  it('renders host-key fingerprints in a readable standalone code block', () => {
    const source = readFileSync('src/webssh/host-key.ts', 'utf8')
    expect(source).toContain("h('div', { class: 'webssh-host-key-confirm' }")
    expect(source).toContain("h('p', t('webssh.hostKeyPrompt'))")
    expect(source).toContain('}, fingerprint)')
    expect(source).not.toContain("t('webssh.hostKeyConfirm', { fingerprint })")
    expect(source).toContain('wordBreak: \'break-all\'')
    expect(source).toContain('whiteSpace: \'normal\'')
  })

  it('opens the terminal only after the page is connected and focuses it', () => {
    const source = readFileSync('src/views/WebSSHTerminal.vue', 'utf8')
    const openIndex = source.indexOf('terminal.open(')
    const connectedIndex = source.indexOf("status.value = 'connected'")
    expect(openIndex).toBeGreaterThan(-1)
    expect(connectedIndex).toBeGreaterThan(-1)
    expect(openIndex).toBeGreaterThan(connectedIndex)
    expect(source).toContain('terminal.focus()')
  })

  it('reports remote shell exit instead of staying connected forever', () => {
    const source = readFileSync('src/views/WebSSHTerminal.vue', 'utf8')
    expect(source).toContain('channel.onExit(')
    expect(source).toContain('remoteEnded')
    expect(source).toContain('webssh.remoteExited')
    expect(source).toContain('status === \'closed\' && !remoteEnded')
  })

  it('renders all terminal states and a close action', () => {
    const source = readFileSync('src/views/WebSSHTerminal.vue', 'utf8')
    for (const marker of [
      "status === 'connecting'",
      "status === 'connected'",
      "status === 'error'",
      "status === 'closed'",
      'closeTerminal',
    ]) expect(source, marker).toContain(marker)
  })

  it('keeps the shared SSH connection when navigating to SFTP', () => {
    const source = readFileSync('src/views/WebSSHTerminal.vue', 'utf8')
    expect(source).toContain("to.name === 'web-sftp'")
    expect(source).not.toContain('onBeforeUnmount(() => { void closeTerminal() })')
  })

  it('reports the remote exit reason and offers a reconnect path', () => {
    const source = readFileSync('src/views/WebSSHTerminal.vue', 'utf8')
    for (const marker of [
      'exitInfo', 'webssh.exitStatus', 'webssh.exitSignal', 'webssh.reconnect',
      'reconnect', 'remoteServerId',
    ]) expect(source, marker).toContain(marker)
    for (const file of ['src/i18n/messages/zh-CN.ts', 'src/i18n/messages/en-US.ts']) {
      const messages = readFileSync(file, 'utf8')
      for (const key of ['exitStatus:', 'exitSignal:', 'reconnect:']) expect(messages, `${file} ${key}`).toContain(key)
    }
  })

  // Returning from SFTP must not demand a fresh ticket: the store already owns
  // the authenticated connection, so the terminal opens another shell channel
  // on it instead of failing with a missing pending session.
  it('reuses the shared connection when returning from SFTP', () => {
    const source = readFileSync('src/views/WebSSHTerminal.vue', 'utf8')
    expect(source).toContain('store.activeSession?.sessionId === sessionId.value')
    expect(source).toContain('reusedConnection')
  })

  it('returns to the server list after disconnecting', () => {
    const source = readFileSync('src/views/WebSSHTerminal.vue', 'utf8')
    expect(source).toContain('disconnectAndLeave')
    expect(source).toContain("router.replace('/remote-servers')")
  })
})

import { vi } from 'vitest'
