// WebSSH / WebSFTP browser end-to-end scenario.
//
// Drives the real admin SPA in headless Chrome over CDP against a real Server,
// a real Agent and a real SSH host, so the assertions cover the whole chain:
// credential auto-authentication, host-key confirmation, an interactive pty
// shell, ZMODEM (lrzsz sz/rz) in both directions, SFTP reuse of the same SSH
// transport, refresh recovery, and the manual-password fallback.
//
// Usage and prerequisites: see README.md next to this file.
import fs from 'node:fs'
import path from 'node:path'
import { launchChrome, openTab } from './lib/cdp.mjs'
import { sha256, writeFixtures } from './lib/fixtures.mjs'
import { buildBinaries, findChrome, findLrzsz, prepareWorkDir, resolveConfig, startStack } from './lib/stack.mjs'

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))
const log = (...args) => console.log('[e2e]', ...args)

// The SPA ships zh-CN as the default locale, but an operator may have switched
// it. Matching either label keeps the scenario locale-independent.
const label = (...candidates) => candidates.join('|')
const UI = {
  sshButton: label('SSH 连接', 'SSH'),
  autoAuth: label('自动认证', 'Auto-auth'),
  hostKeyTitle: label('确认服务器指纹', 'Verify server fingerprint'),
  hostKeyTrust: label('确认指纹', 'Trust fingerprint'),
  password: label('密码', 'Password'),
  disconnect: label('断开连接', 'Disconnect'),
  sftp: 'SFTP',
  missingSession: label('找不到当前浏览器会话中的 SSH 凭据', 'credentials for this browser session are missing'),
}
const VISIBLE_DIALOG = `[...document.querySelectorAll('.el-overlay')].find(o => getComputedStyle(o).display !== 'none' && o.querySelector('.el-dialog'))`

const config = resolveConfig()
prepareWorkDir(config)
const fixtures = writeFixtures({
  remoteFsDir: config.remoteFsDir,
  uploadDir: config.uploadDir,
  downloadBytes: config.downloadBytes,
  uploadBytes: config.uploadBytes,
  sftpUploadBytes: config.sftpUploadBytes,
})
log(`fixtures: download=${config.downloadBytes}B upload=${config.uploadBytes}B`)

const lrzsz = findLrzsz()
if (!lrzsz.available) log(`lrzsz (${lrzsz.missing}) not found; ZMODEM checks will be skipped`)

const binaries = buildBinaries(config, log)
const chromeBinary = findChrome(config)
const stack = await startStack(config, binaries, log)
const chrome = await launchChrome({ binary: chromeBinary, cdpPort: config.cdpPort, profileDir: config.profileDir })

const results = []
const check = (name, ok, detail) => {
  results.push({ name, ok: !!ok, detail })
  log(`${ok ? 'PASS' : 'FAIL'} ${name}${detail ? ` :: ${detail}` : ''}`)
}
const skip = (name, reason) => {
  results.push({ name, ok: true, skipped: true, detail: reason })
  log(`SKIP ${name} :: ${reason}`)
}

const openScenarioTab = (url, name) => openTab({
  cdpPort: config.cdpPort,
  origin: config.origin,
  url,
  token: stack.token,
  downloadsDir: config.downloadDir,
  screenshotPrefix: `${config.screenshotPrefix}-${name}`,
})

/** connectFromList clicks "SSH" on a row and clears the host-key prompt. */
async function connectFromList(tab, rowName) {
  await tab.wait(`!!document.querySelector('.table-card .el-table__row')`)
  await sleep(800)
  await tab.ev(`(() => {
    const row = [...document.querySelectorAll('.table-card .el-table__row')].find(tr => tr.innerText.includes(${JSON.stringify(rowName)}))
    if (!row) return false
    const button = [...row.querySelectorAll('button')].find(b => new RegExp(${JSON.stringify(`^(${UI.sshButton})$`)}).test(b.innerText.trim()))
    if (!button) return false
    button.click()
    return true
  })()`)
  const prompted = await tab.wait(`document.body.innerText.includes(${JSON.stringify(UI.hostKeyTitle.split('|')[0])}) || document.body.innerText.includes(${JSON.stringify(UI.hostKeyTitle.split('|')[1])})`, 25000)
  if (prompted) {
    await tab.ev(`(() => {
      const box = [...document.querySelectorAll('.el-message-box')].find(b => /${UI.hostKeyTitle}/.test(b.innerText))
      if (!box) return false
      const trust = [...box.querySelectorAll('button')].find(b => /${UI.hostKeyTrust}/.test(b.innerText.replace(/\\s/g, '')))
      if (!trust) return false
      trust.click()
      return true
    })()`)
  }
  return prompted
}

let exitCode = 1
try {
  // ---- 1. the server list surfaces which hosts can auto-authenticate
  const list = await openScenarioTab('/remote-servers', 'list')
  await list.wait(`!!document.querySelector('.table-card .el-table__row')`)
  await sleep(1000)
  const rows = await list.ev(`(() => [...document.querySelectorAll('.table-card .el-table__row')].map(tr => tr.innerText.replace(/\\n/g, ' | ')))()`)
  // A server without a credential shows no capability tag at all: that is how
  // the UI distinguishes "manual password" from "auto-authentication available".
  check(
    'list marks the auto-auth capable server',
    (rows || []).some((r) => r.includes('e2e-host') && new RegExp(UI.autoAuth).test(r))
      && !(rows || []).some((r) => r.includes('e2e-plain') && new RegExp(UI.autoAuth).test(r)),
    JSON.stringify(rows),
  )
  await list.shot('01-list')

  // ---- 2. a stored credential skips the auth dialog entirely
  await connectFromList(list, 'e2e-host')
  const dialogAppeared = await list.ev(`!!(${VISIBLE_DIALOG})`)
  check('stored credential skips the auth dialog', !dialogAppeared, dialogAppeared ? await list.ev(`((${VISIBLE_DIALOG})||{}).innerText`) : '')
  const connected = await list.wait(`document.querySelector('.terminal') && document.querySelector('.terminal').dataset.connected === 'true'`, 25000)
  await sleep(1500)
  let text = await list.terminalText()
  check('terminal connects with the stored password', connected && /tmhost\$/.test(text), JSON.stringify(text).slice(0, 200))
  await list.shot('02-terminal')

  // ---- 3. keystrokes reach the pty (a swallowed keyboard is a dead terminal)
  await list.ev(`(() => { const ta = document.querySelector('.terminal textarea, .terminal .xterm-helper-textarea'); if (ta) ta.focus(); return true })()`)
  await list.typeText('echo AUTOAUTH_OK')
  await list.pressEnter()
  await sleep(1200)
  text = await list.terminalText()
  check('terminal accepts keystrokes', text.includes('AUTOAUTH_OK'), JSON.stringify(text).slice(-200))

  // ---- 4. sz: remote -> browser over real ZMODEM.
  // The payload is multi-megabyte on purpose: it has to cross both the server
  // relay receive window and the browser transport guard.
  if (!lrzsz.available) {
    skip('sz downloads the file byte-for-byte', `lrzsz ${lrzsz.missing} missing`)
    skip('rz uploads the picked file byte-for-byte', `lrzsz ${lrzsz.missing} missing`)
  } else {
    const wantPayload = sha256(fixtures.downloadPath)
    // A fast in-process transfer can finish between two polls, so the panel is
    // observed with a MutationObserver instead of polled for.
    await list.ev(`(() => { window.__panelSeen = false; window.__panelText = ''; new MutationObserver(() => { const el = document.querySelector('.transfer-panel'); if (el) { window.__panelSeen = true; window.__panelText = el.innerText.replace(/\\n/g, ' | ') } }).observe(document.body, { childList: true, subtree: true }); return true })()`)
    await list.typeText('sz payload.bin')
    await list.pressEnter()
    const panel = await list.wait(`window.__panelSeen === true`, 20000)
    await list.shot('03-sz-panel')
    let downloaded = ''
    for (let i = 0; i < 120; i++) {
      if (fs.existsSync(path.join(config.downloadDir, 'payload.bin'))) {
        downloaded = path.join(config.downloadDir, 'payload.bin')
        break
      }
      await sleep(250)
    }
    const sameBytes = downloaded !== '' && sha256(downloaded) === wantPayload
    check('sz downloads the file byte-for-byte', panel && sameBytes,
      `panel=${panel} file=${downloaded} match=${sameBytes} text=${JSON.stringify(await list.ev(`window.__panelText`))}`)
    await sleep(1500)
    text = await list.terminalText()
    // A transfer that kills the session shows up here as a dead prompt.
    check('transfer panel closes and the prompt returns',
      !(await list.ev(`!!document.querySelector('.transfer-panel')`)) && /tmhost\$/.test(text),
      JSON.stringify(text).slice(-160))
    await list.shot('04-after-sz')

    // ---- 5. rz: browser -> remote over real ZMODEM
    await list.typeText('rz')
    await list.pressEnter()
    const sendPanel = await list.wait(`!!document.querySelector('.transfer-panel')`, 20000)
    await sleep(800)
    await list.shot('05-rz-panel')
    await list.setFiles('.zmodem-file-input', [fixtures.uploadPath])
    const wantUpload = sha256(fixtures.uploadPath)
    const uploadedPath = path.join(config.remoteFsDir, 'upload.bin')
    let uploaded = ''
    for (let i = 0; i < 120; i++) {
      if (fs.existsSync(uploadedPath)) { uploaded = uploadedPath; break }
      await sleep(250)
    }
    await sleep(500)
    const uploadMatches = uploaded !== '' && sha256(uploaded) === wantUpload
    check('rz uploads the picked file byte-for-byte', sendPanel && uploadMatches, `panel=${sendPanel} path=${uploaded} match=${uploadMatches}`)
    await sleep(1200)
    text = await list.terminalText()
    check('prompt returns after rz', /tmhost\$/.test(text), JSON.stringify(text).slice(-160))
    await list.shot('06-after-rz')
  }

  // ---- 6. SFTP reuses the authenticated SSH transport
  await list.ev(`[...document.querySelectorAll('.header-actions button')].find(b => b.innerText.includes(${JSON.stringify(UI.sftp)})).click()`)
  const sftpReady = await list.wait(`location.pathname.includes('/sftp') && (document.querySelectorAll('.file-card .el-table__row').length > 0 || !!document.querySelector('.file-card .el-alert'))`, 25000)
  await sleep(1200)
  const sftpRows = await list.ev(`(() => [...document.querySelectorAll('.file-card .el-table__row')].map(tr => tr.querySelector('td').innerText.trim()))()`)
  check('SFTP reuses the authenticated connection and lists the remote file',
    sftpReady && (sftpRows || []).includes('payload.bin'), JSON.stringify(sftpRows))
  await list.shot('07-sftp')

  // ---- 6b. SFTP upload must survive non-blocking partial writes.
  // libssh2_sftp_write returns the bytes consumed so far when the channel
  // window stalls; a client that treats that as completion truncates the file
  // and the UI can only say "transfer failed". The payload spans many 256 KiB
  // chunks so the loop is exercised, and the on-disk hash proves completeness.
  const sftpName = path.basename(fixtures.sftpUploadPath)
  await list.setFiles('.file-card .upload-button input[type="file"]', [fixtures.sftpUploadPath])
  const listedAfterUpload = await list.wait(
    `[...document.querySelectorAll('.file-card .el-table__row')].some(tr => tr.querySelector('td').innerText.trim() === ${JSON.stringify(sftpName)})`,
    40000,
  )
  await sleep(800)
  const sftpOnDisk = path.join(config.remoteFsDir, sftpName)
  const sftpBytesMatch = fs.existsSync(sftpOnDisk) && sha256(sftpOnDisk) === sha256(fixtures.sftpUploadPath)
  const sftpNoError = !(await list.ev(`!!document.querySelector('.file-card .el-alert')`))
  check('SFTP upload writes the file byte-for-byte',
    listedAfterUpload && sftpBytesMatch && sftpNoError,
    `listed=${listedAfterUpload} onDisk=${sftpOnDisk} match=${sftpBytesMatch} noError=${sftpNoError}`)
  await list.shot('07b-sftp-upload')

  // ---- 7. an explicit disconnect leaves the terminal page
  await list.ev(`[...document.querySelectorAll('.header-actions button')].find(b => new RegExp(${JSON.stringify(UI.disconnect)}).test(b.innerText)).click()`)
  check('disconnect returns to the server list', await list.wait(`location.pathname === '/remote-servers'`, 15000))
  await list.close()

  // ---- 8. a server without a credential still asks for a password
  const fallback = await openScenarioTab('/remote-servers', 'fallback')
  await fallback.wait(`!!document.querySelector('.table-card .el-table__row')`)
  await sleep(800)
  await fallback.ev(`(() => {
    const row = [...document.querySelectorAll('.table-card .el-table__row')].find(tr => tr.innerText.includes('e2e-plain'))
    if (!row) return false
    const button = [...row.querySelectorAll('button')].find(b => new RegExp(${JSON.stringify(`^(${UI.sshButton})$`)}).test(b.innerText.trim()))
    if (!button) return false
    button.click()
    return true
  })()`)
  const fallbackDialog = await fallback.wait(`!!(${VISIBLE_DIALOG})`, 10000)
  const dialogText = (await fallback.ev(`((${VISIBLE_DIALOG})||{}).innerText`)) || ''
  check('no credential falls back to the manual dialog',
    fallbackDialog && new RegExp(UI.password).test(dialogText), JSON.stringify(dialogText).slice(0, 160))
  await fallback.shot('08-fallback-dialog')
  await fallback.close()

  // ---- 9. refreshing a live terminal must not dead-end.
  // The one-time ticket lives in memory only, so a reload used to strand the
  // user on "credentials missing". The view now routes back to the server list
  // with the reconnect flow pre-targeted.
  const refresh = await openScenarioTab('/remote-servers', 'refresh')
  // The host-key prompt only appears when this browser profile has not trusted
  // the key yet, so it is not asserted here.
  await connectFromList(refresh, 'e2e-host')
  const refreshConnected = await refresh.wait(`document.querySelector('.terminal') && document.querySelector('.terminal').dataset.connected === 'true'`, 25000)
  check('second session connects before the refresh check', refreshConnected, `connected=${refreshConnected}`)
  await refresh.shot('09-before-reload')
  const sessionBeforeReload = await refresh.ev(`location.pathname`)
  await refresh.reload()
  // Recovery means "left the dead session and no credentials-missing error".
  // Where it lands is deliberately not pinned down: the server list routes
  // straight back into a fresh auto-auth session, so the terminal may reappear
  // before the assertion is polled.
  const recovered = await refresh.wait(
    `location.pathname !== ${JSON.stringify(sessionBeforeReload)} && !new RegExp(${JSON.stringify(UI.missingSession)}).test(document.body.innerText)`,
    25000,
  )
  await sleep(1200)
  const bodyText = await refresh.ev(`document.body.innerText`)
  check('refresh routes back instead of dead-ending on missing credentials',
    recovered && !new RegExp(UI.missingSession).test(bodyText || ''),
    `recovered=${recovered} path=${await refresh.ev(`location.pathname`)} body=${JSON.stringify((bodyText || '').slice(0, 200))}`)
  await refresh.shot('10-after-reload')
  await refresh.close()

  const exceptions = [...list.failures, ...fallback.failures, ...refresh.failures]
  check('browser console stayed clean', exceptions.length === 0, exceptions.join('\n'))

  exitCode = results.every((r) => r.ok) ? 0 : 1
} catch (error) {
  log('scenario crashed:', error?.stack || error)
  log(stack.tailLogs(60))
  exitCode = 1
} finally {
  console.log('\n== summary ==')
  for (const r of results) console.log(`${r.skipped ? 'SKIP' : r.ok ? 'PASS' : 'FAIL'}  ${r.name}`)
  fs.writeFileSync(path.join(config.workDir, 'results.json'), JSON.stringify({ results }, null, 2))
  await chrome.kill()
  await stack.stop()
  // The whole workdir is kept: binaries and fixtures are expensive to rebuild,
  // and the logs plus screenshots are what a failure needs. prepareWorkDir
  // clears the volatile parts on the next run.
  log(`workdir ${config.workDir} (logs, screenshots and fixtures kept)`)
  process.exit(exitCode)
}
