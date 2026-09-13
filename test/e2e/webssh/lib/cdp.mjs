// Minimal Chrome DevTools Protocol driver.
//
// The harness talks CDP directly instead of pulling Playwright/Puppeteer: the
// only things it needs are navigation, evaluation, real key events, file-input
// injection and screenshots, and a zero-dependency driver keeps `node run.mjs`
// the whole setup story.
import fs from 'node:fs'
import { spawn } from 'node:child_process'

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))

/**
 * openTab attaches to a fresh headless tab. Console errors and uncaught
 * exceptions are collected into `failures`, so a run that passes functionally
 * but throws in the browser still fails: silent transport errors are exactly
 * the class of bug this harness exists to catch.
 */
export async function openTab({ cdpPort, origin, url, token, downloadsDir, screenshotPrefix, width = 1521, height = 900 }) {
  const tab = await (await fetch(`http://127.0.0.1:${cdpPort}/json/new?about:blank`, { method: 'PUT' })).json()
  const ws = new WebSocket(tab.webSocketDebuggerUrl)
  let id = 0
  const pending = new Map()
  const failures = []
  const logs = []

  ws.onmessage = (event) => {
    const message = JSON.parse(event.data)
    if (message.id && pending.has(message.id)) {
      const waiter = pending.get(message.id)
      pending.delete(message.id)
      if (message.error) waiter.reject(new Error(JSON.stringify(message.error)))
      else waiter.resolve(message.result)
      return
    }
    if (message.method === 'Runtime.exceptionThrown') {
      failures.push(`EXCEPTION: ${message.params.exceptionDetails.exception?.description || message.params.exceptionDetails.text}`)
      return
    }
    if (message.method === 'Runtime.consoleAPICalled') {
      const line = (message.params.args || []).map((a) => a.value ?? a.description ?? '').join(' ')
      if (message.params.type === 'error') failures.push(`CONSOLE: ${line}`)
      else logs.push(`${message.params.type}: ${line}`)
    }
  }

  const send = (method, params = {}) => new Promise((resolve, reject) => {
    const requestId = ++id
    pending.set(requestId, { resolve, reject })
    ws.send(JSON.stringify({ id: requestId, method, params }))
  })
  await new Promise((resolve) => { ws.onopen = resolve })

  await send('Page.enable')
  await send('Runtime.enable')
  await send('Network.enable')
  await send('Log.enable')
  await send('DOM.enable')
  // The admin SPA reads its bearer token from localStorage, so seed it before
  // the first document loads instead of driving the login form.
  await send('Page.addScriptToEvaluateOnNewDocument', { source: `localStorage.setItem('tunnelmesh_token', ${JSON.stringify(token)})` })
  await send('Emulation.setDeviceMetricsOverride', { width, height, deviceScaleFactor: 1, mobile: false })
  await send('Page.setDownloadBehavior', { behavior: 'allow', downloadPath: downloadsDir, eventsEnabled: true })
  await send('Page.navigate', { url: origin + url })

  const ev = async (expression) => {
    const result = await send('Runtime.evaluate', { returnByValue: true, expression, awaitPromise: true })
    if (result.exceptionDetails) {
      failures.push(`EVAL[${expression.slice(0, 90).replace(/\n/g, ' ')}]: ${result.exceptionDetails.exception?.description || result.exceptionDetails.text || JSON.stringify(result.exceptionDetails)}`)
    }
    return result.result?.value
  }
  const wait = async (expression, timeoutMs = 20000) => {
    for (let i = 0; i < timeoutMs / 250; i++) {
      if (await ev(expression)) return true
      await sleep(250)
    }
    return false
  }
  const shot = async (name) => {
    const shot = await send('Page.captureScreenshot', { format: 'png' })
    fs.writeFileSync(`${screenshotPrefix}-${name}.png`, Buffer.from(shot.data, 'base64'))
  }
  const reload = async () => { await send('Page.reload', { ignoreCache: false }) }

  // Terminal typing goes through real key events so xterm sees exactly what a
  // user would produce. Input.insertText feeds xterm's hidden textarea the way
  // IME input does; synthesising per-character key events makes Chrome recompute
  // the character from `code` and silently corrupts the command line.
  const typeText = async (text) => { await send('Input.insertText', { text }) }
  const pressEnter = async () => {
    await send('Input.dispatchKeyEvent', { type: 'rawKeyDown', key: 'Enter', code: 'Enter', windowsVirtualKeyCode: 13, nativeVirtualKeyCode: 13 })
    await send('Input.dispatchKeyEvent', { type: 'char', text: '\r', key: 'Enter', code: 'Enter', windowsVirtualKeyCode: 13, nativeVirtualKeyCode: 13 })
    await send('Input.dispatchKeyEvent', { type: 'keyUp', key: 'Enter', code: 'Enter', windowsVirtualKeyCode: 13, nativeVirtualKeyCode: 13 })
  }
  const terminalText = async () => (await ev(`(() => (document.querySelector('.terminal') || {}).innerText || '')()`)) || ''
  const setFiles = async (selector, files) => {
    const { root } = await send('DOM.getDocument', { depth: -1, pierce: true })
    const { nodeId } = await send('DOM.querySelector', { nodeId: root.nodeId, selector })
    if (!nodeId) throw new Error(`selector ${selector} not found`)
    await send('DOM.setFileInputFiles', { files, nodeId })
  }

  return {
    ev,
    wait,
    shot,
    reload,
    send,
    typeText,
    pressEnter,
    terminalText,
    setFiles,
    failures,
    logs,
    close: async () => {
      ws.close()
      await fetch(`http://127.0.0.1:${cdpPort}/json/close/${tab.id}`).catch(() => {})
    },
  }
}

/** launchChrome starts a headless Chrome and waits for its debug endpoint. */
export async function launchChrome({ binary, cdpPort, profileDir, width = 1521, height = 900 }) {
  const chrome = spawn(binary, [
    '--headless=new',
    `--remote-debugging-port=${cdpPort}`,
    `--user-data-dir=${profileDir}`,
    '--no-first-run',
    '--no-default-browser-check',
    `--window-size=${width},${height}`,
    'about:blank',
  ], { stdio: 'ignore' })
  for (let i = 0; i < 60; i++) {
    try {
      await fetch(`http://127.0.0.1:${cdpPort}/json/version`)
      return chrome
    } catch {
      await sleep(500)
    }
  }
  chrome.kill()
  throw new Error(`Chrome did not open the CDP port ${cdpPort}`)
}
