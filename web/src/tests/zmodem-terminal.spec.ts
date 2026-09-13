import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'

const terminal = readFileSync('src/views/WebSSHTerminal.vue', 'utf8')
const sftp = readFileSync('src/views/WebSFTP.vue', 'utf8')

describe('WebSSH terminal ZMODEM integration', () => {
  it('routes raw channel bytes through the bridge instead of writing them directly', () => {
    for (const marker of [
      'createZmodemBridge', 'consumeFromRemote', 'toTerminal', 'toPeer', 'onEvent: handleZmodemEvent',
    ]) expect(terminal, marker).toContain(marker)
    // Multi-byte UTF-8 sequences straddle WebSocket frames, so decoding has to
    // stay streaming rather than per chunk.
    expect(terminal).toContain('{ stream: true }')
    expect(terminal).not.toContain('channel.read((data) => terminal?.write(decoder.decode(data)))')
  })

  it('drops keystrokes while a transfer owns the channel', () => {
    expect(terminal).toContain('zmodem?.active')
    expect(terminal).toContain('terminal.onData')
  })

  it('shows transfer progress with a cancel path and saves received files', () => {
    for (const marker of [
      'transfer-panel', 'el-progress', 'cancelTransfer', 'downloadBytes',
      "t('webssh.zmodem.sending')", "t('webssh.zmodem.receiving')", "t('webssh.zmodem.cancel')",
    ]) expect(terminal, marker).toContain(marker)
    expect(terminal).toContain('type="file"')
    expect(terminal).toContain('multiple')
  })

  // The panel lives above the terminal on purpose: a scrolling terminal must
  // never push the progress and the cancel button out of view mid-transfer.
  it('keeps the transfer panel above the terminal', () => {
    const panel = terminal.indexOf('class="transfer-panel"')
    const term = terminal.indexOf('ref="terminalElement"')
    expect(panel).toBeGreaterThan(-1)
    expect(term).toBeGreaterThan(-1)
    expect(panel).toBeLessThan(term)
  })

  // A failed protocol write must kill the transfer, not the SSH channel: the
  // read loop owns channel liveness, and lrzsz pauses are recoverable.
  it('fails the transfer instead of the channel when a protocol write fails', () => {
    expect(terminal).toContain('bridge.abortLocal()')
    const toPeer = terminal.slice(terminal.indexOf('toPeer:'), terminal.indexOf('onEvent: handleZmodemEvent'))
    expect(toPeer).toContain('bridge?.active')
    // A protocol write can still be in flight when the transfer ends (it waits
    // for window credit without a deadline), and its late rejection says
    // nothing about the shell. Closing the session there turned the tail of a
    // healthy bulk download into the "SSH 通道已关闭" empty state.
    expect(toPeer).not.toContain('closeTerminal()')
  })

  // Protocol writes must not inherit the keystroke stall deadline: lrzsz
  // pauses to retransmit on a bulk transfer, and a deadline that fires there
  // kills a download that is still healthy.
  it('writes protocol bytes without a stall deadline', () => {
    const toPeer = terminal.slice(terminal.indexOf('toPeer:'), terminal.indexOf('onEvent: handleZmodemEvent'))
    expect(toPeer).toContain('stallLimitMs: 0')
  })

  it('disposes the bridge when the terminal closes', () => {
    expect(terminal).toContain('zmodem?.dispose()')
    expect(terminal.match(/zmodem = null/g)?.length ?? 0).toBeGreaterThanOrEqual(2)
  })

  it('shares one download helper between SFTP and ZMODEM', () => {
    expect(sftp).toContain("from '../webssh/download'")
    expect(sftp).toContain('downloadBlob(')
    expect(terminal).toContain("from '../webssh/download'")
  })
})
