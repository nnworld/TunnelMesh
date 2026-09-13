<template>
  <section class="tm-page webssh-page">
    <PageHeader :title="t('webssh.title')" :description="t('webssh.description')">
      <div class="header-actions">
        <el-button :disabled="status !== 'connected'" @click="goSFTP">{{ t('webssh.sftp') }}</el-button>
        <el-button type="danger" :disabled="status === 'closed' || closing" :loading="closing" @click="disconnectAndLeave">
          {{ t('webssh.close') }}
        </el-button>
      </div>
    </PageHeader>

    <div class="tm-card terminal-card">
      <div class="terminal-toolbar">
        <el-tag :type="statusTagType" effect="plain">{{ t(`webssh.status.${status}`) }}</el-tag>
        <span v-if="sessionId" class="session-id">{{ sessionId }}</span>
      </div>

      <el-alert
        v-if="transportFailure"
        class="transport-alert"
        type="error"
        show-icon
        :closable="false"
      :title="t('webssh.transportFailed')"
      :description="transportFailure"
    />

      <!-- lrzsz support: `sz` downloads to the browser, `rz` uploads a picked
           file. The panel sits above the terminal so the progress stays in
           view while the terminal scrolls. While a transfer owns the channel,
           keystrokes are dropped and the only way out is cancel, which sends
           ZMODEM's abort sequence. -->
      <div v-if="transfer" class="transfer-panel">
        <div class="transfer-head">
          <el-tag :type="transfer.role === 'send' ? 'warning' : 'success'" effect="plain">
            {{ transfer.role === 'send' ? t('webssh.zmodem.sending') : t('webssh.zmodem.receiving') }}
          </el-tag>
          <span class="transfer-name">{{ transfer.name || t('webssh.zmodem.waiting') }}</span>
          <el-button size="small" type="danger" plain @click="cancelTransfer">{{ t('webssh.zmodem.cancel') }}</el-button>
        </div>
        <el-progress :percentage="transferPercent" :stroke-width="8" :show-text="false" />
        <div class="transfer-meta">
          <span>{{ formatBytes(transfer.transferred) }} / {{ formatBytes(transfer.total) }}</span>
          <span class="transfer-hint">{{ t('webssh.zmodem.inputBlocked') }}</span>
        </div>
      </div>

      <div
        ref="terminalElement"
        class="terminal"
        :class="{ hidden: status === 'connecting' || status === 'error' || (status === 'closed' && !remoteEnded) }"
        :data-connected="status === 'connected'"
      ></div>
      <el-empty v-if="status === 'connecting'" :description="t('webssh.connecting')" />
      <el-alert v-else-if="status === 'error'" type="error" show-icon :closable="false" :title="errorMessage" />
      <el-empty v-else-if="status === 'closed' && !remoteEnded" :description="t('webssh.closedDescription')" />
      <div v-else-if="status === 'closed' && remoteEnded" class="remote-ended">
        <span v-if="exitReason(exitInfo)" class="remote-ended-reason">{{ exitReason(exitInfo) }}</span>
        <el-button type="primary" size="small" @click="reconnect">{{ t('webssh.reconnect') }}</el-button>
      </div>

    <input ref="fileInput" type="file" multiple class="zmodem-file-input" @change="onFilesPicked" />
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref } from 'vue'
import { onBeforeRouteLeave, useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'
import { ElMessage } from 'element-plus'
import PageHeader from '../components/PageHeader.vue'
import {
  forgetRememberedWebSSHTarget,
  readRememberedWebSSHTarget,
  rememberWebSSHTarget,
  useWebSSHStore,
  type PendingWebSSHSession,
} from '../stores/webssh'
import { createWebSocketByteStream, websshWebSocketURL, type DuplexByteStream } from '../webssh/byte-stream'
import { useHostKeyVerifier } from '../webssh/host-key'
import { connectSSH, type ShellExitInfo, type SSHConnection, type TerminalChannel } from '../webssh/ssh-client'
import { createZmodemBridge, type ZmodemBridge, type ZmodemEvent } from '../webssh/zmodem'
import { downloadBytes } from '../webssh/download'

type WebSSHStatus = 'connecting' | 'connected' | 'error' | 'closed'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const store = useWebSSHStore()
const verifyHostKey = useHostKeyVerifier()
const terminalElement = ref<HTMLElement>()
const status = ref<WebSSHStatus>('connecting')
const errorMessage = ref('')
const closing = ref(false)
const remoteEnded = ref(false)
const exitInfo = ref<ShellExitInfo | null>(null)
// Set when the browser-side WebSocket transport died. Keeping it separate from
// the exit info stops a local failure from being rendered as "remote exit 0".
const transportFailure = ref('')
// Only the non-sensitive target ID survives a close, which is exactly what the
// reconnect button needs; credentials are cleared with the session.
const remoteServerId = ref('')

let terminal: Terminal | null = null
let fitAddon: FitAddon | null = null
let stream: DuplexByteStream | null = null
let connection: SSHConnection | null = null
let channel: TerminalChannel | null = null
let sessionCredentials: PendingWebSSHSession | null = null
let unloadHandler: (() => void) | null = null
let resizeHandler: (() => void) | null = null
let closeGuard = false
let zmodem: ZmodemBridge | null = null

const fileInput = ref<HTMLInputElement>()
const transfer = ref<{ role: 'send' | 'receive'; name: string; transferred: number; total: number } | null>(null)
const transferPercent = computed(() => {
  const current = transfer.value
  if (!current || !current.total) return 0
  return Math.min(100, Math.round((current.transferred / current.total) * 100))
})

function formatBytes(value: number) {
  if (!value) return '0 B'
  const units = ['B', 'KiB', 'MiB', 'GiB']
  const exponent = Math.min(units.length - 1, Math.floor(Math.log(value) / Math.log(1024)))
  return `${(value / 1024 ** exponent).toFixed(exponent ? 1 : 0)} ${units[exponent]}`
}

const sessionId = computed(() => String(route.params.sessionId || ''))
const statusTagType = computed(() => ({
  connecting: 'warning',
  connected: 'success',
  error: 'danger',
  closed: 'info',
} as const)[status.value])

function goSFTP() {
  void router.push({ name: 'web-sftp', params: { sessionId: sessionId.value } })
}

// exitReason renders the remote exit code or terminating signal when libssh2
// reported one. A build without the exit bindings yields an empty string so the
// UI keeps the generic "session ended" message instead of inventing a code.
function exitReason(info: ShellExitInfo | null) {
  if (!info) return ''
  if (info.transportError) return ''
  if (info.signal) return t('webssh.exitSignal', { signal: info.signal })
  if (info.status !== null) return t('webssh.exitStatus', { status: info.status })
  return ''
}

function reconnect() {
  void router.replace({
    path: '/remote-servers',
    query: remoteServerId.value ? { ssh: remoteServerId.value } : {},
  })
}

function clearCredentials() {
  if (sessionCredentials) {
    sessionCredentials.password = undefined
    sessionCredentials.privateKey = undefined
    sessionCredentials.passphrase = undefined
  }
  sessionCredentials = null
}

async function connect() {
  try {
    status.value = 'connecting'
    remoteEnded.value = false
    exitInfo.value = null

    // Returning from SFTP remounts this view, but the authenticated transport
    // is still owned by the store. Reopening a shell channel on it is the only
    // way back in: the one-time ticket was already consumed by the first
    // handshake, so demanding a pending session here would always fail.
    const reusedConnection = store.activeSession?.sessionId === sessionId.value
      ? store.activeSession.connection
      : null

    if (reusedConnection) {
      connection = reusedConnection
      remoteServerId.value = store.activeSession?.remoteServerId || ''
      rememberWebSSHTarget(sessionId.value, remoteServerId.value)
    } else {
      sessionCredentials = store.takePendingSession(sessionId.value)
      if (!sessionCredentials) {
        const remembered = readRememberedWebSSHTarget(sessionId.value)
        if (remembered) {
          await router.replace({ path: '/remote-servers', query: { ssh: remembered } })
          return
        }
        throw new Error(t('webssh.missingSession'))
      }
      remoteServerId.value = sessionCredentials.remoteServerId || ''
      rememberWebSSHTarget(sessionId.value, remoteServerId.value)

      stream = createWebSocketByteStream(websshWebSocketURL(sessionCredentials))
      stream.onClose(() => {
        if (status.value === 'connected') {
          void closeTerminal()
        }
      })

      connection = await connectSSH({
        stream,
        username: sessionCredentials.username,
        password: sessionCredentials.password,
        privateKey: sessionCredentials.privateKey,
        passphrase: sessionCredentials.passphrase,
        hostKeyVerifier: verifyHostKey,
      })
    }
    channel = await connection.openShell()

    // The terminal must be opened while its container is visible, otherwise
    // xterm measures zero-size cells and never receives keyboard focus.
    status.value = 'connected'
    await nextTick()
    terminal = new Terminal({
      cursorBlink: true,
      fontSize: 13,
      fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace',
      theme: { background: '#0b1220' },
    })
    fitAddon = new FitAddon()
    terminal.loadAddon(fitAddon)
    if (!terminalElement.value) throw new Error(t('webssh.containerMissing'))
    terminal.open(terminalElement.value)
    // Only a freshly authenticated connection carries credentials to publish;
    // a reused one is already registered by whoever created it.
    if (sessionCredentials) {
      store.registerActiveSession(sessionId.value, sessionCredentials, connection, remoteServerId.value)
    }

    const encoder = new TextEncoder()
    const decoder = new TextDecoder()
    // Every byte from the shell goes through the ZMODEM sentry first: it either
    // belongs to a transfer (and is answered on the channel) or it is terminal
    // output. Decoding stays streaming because a multi-byte character is
    // regularly split across tunnel frames.
    zmodem = createZmodemBridge({
      toTerminal: bytes => terminal?.write(decoder.decode(bytes, { stream: true })),
      toPeer: (bytes) => {
        // Protocol traffic only flows while the bridge owns the channel, and
        // lrzsz regularly pauses to retransmit a ZACK on a bulk transfer. The
        // keystroke stall deadline would read that pause as a dead peer and
        // kill a healthy 153 MiB download, so these writes wait for window
        // credit indefinitely. Teardown stays bounded because close() no longer
        // queues behind them.
        void channel?.write(bytes, { stallLimitMs: 0 }).catch((error) => {
          const bridge = zmodem
          if (bridge?.active) {
            // A stalled or rejected protocol write ends the transfer, not the
            // shell: lrzsz pauses while waiting for a ZACK retry are legal, and
            // channel liveness belongs to the read loop. abortLocal skips the
            // ZABORT because this pipe just proved undeliverable.
            bridge.abortLocal()
            ElMessage.error(t('webssh.zmodem.failed', {
              message: error instanceof Error ? error.message : String(error),
            }))
            return
          }
          // Reaching here means no ZMODEM session owns the channel, which
          // happens for a leftover protocol write that was still waiting for
          // window credit when the transfer ended: its late rejection says
          // nothing about the shell. Channel liveness belongs to the read loop
          // and to the keystroke path, and closing here is what turned the tail
          // of a healthy bulk download into the "SSH 通道已关闭" empty state.
        })
      },
      onEvent: handleZmodemEvent,
    })
    terminal.onData((data) => {
      // A running transfer expects protocol bytes from us, so keystrokes are
      // dropped instead of corrupting the file.
      if (zmodem?.active) return
      void channel?.write(encoder.encode(data)).catch(() => { void closeTerminal() })
    })
    channel.read((data) => {
      if (zmodem) zmodem.consumeFromRemote(data)
      else terminal?.write(decoder.decode(data, { stream: true }))
    })
    channel.onExit((info) => {
      exitInfo.value = info
      remoteEnded.value = true
      terminal?.writeln('')
      if (info.transportError) {
        // The local tunnel broke, not the remote shell, so report the transport
        // reason instead of a misleading exit status.
        transportFailure.value = info.transportError
        terminal?.writeln(t('webssh.transportFailed'))
        terminal?.writeln(info.transportError)
      } else {
        terminal?.writeln(t('webssh.remoteExited'))
        const reason = exitReason(info)
        if (reason) terminal?.writeln(reason)
      }
      void closeTerminal(true)
    })

    fitAddon.fit()
    await channel.resize(terminal.cols, terminal.rows)
    resizeHandler = () => {
      fitAddon?.fit()
      if (terminal) void channel?.resize(terminal.cols, terminal.rows)
    }
    window.addEventListener('resize', resizeHandler)
    terminal.focus()
  } catch (error) {
    status.value = 'error'
    errorMessage.value = error instanceof Error ? error.message : t('webssh.connectFailed')
    await closeTerminal()
  }
}

function handleZmodemEvent(event: ZmodemEvent) {
  switch (event.type) {
    case 'detected':
      transfer.value = { role: event.role, name: '', transferred: 0, total: 0 }
      break
    case 'send-required':
      // The remote ran `rz`: it is waiting for an offer, so ask for files now.
      fileInput.value?.click()
      break
    case 'receive-offer':
      transfer.value = { role: 'receive', name: event.name, transferred: 0, total: event.size }
      break
    case 'progress':
      if (transfer.value) {
        transfer.value = { ...transfer.value, name: event.name, transferred: event.transferred, total: event.total }
      }
      break
    case 'completed':
      if (event.data) downloadBytes(event.name, event.data)
      if (transfer.value) {
        transfer.value = { ...transfer.value, name: event.name, transferred: transfer.value.total }
      }
      break
    case 'ended':
    case 'aborted':
      transfer.value = null
      break
    case 'error':
      transfer.value = null
      ElMessage.error(t('webssh.zmodem.failed', { message: event.message }))
      break
  }
}

async function onFilesPicked(event: Event) {
  const input = event.target as HTMLInputElement
  const files = Array.from(input.files ?? [])
  // Reset immediately so picking the same file twice still fires change.
  input.value = ''
  const bridge = zmodem
  if (!bridge) return
  if (!files.length) {
    // Nothing chosen means the remote rz would wait forever, so cancel it.
    bridge.abort()
    return
  }
  try {
    await bridge.sendFiles(files)
  } catch {
    // The bridge already reported the reason through onEvent.
  }
}

function cancelTransfer() { zmodem?.abort() }

async function closeTerminal(keepTerminal = false) {
  if (closeGuard) return
  closeGuard = true
  closing.value = true
  try {
    if (resizeHandler) window.removeEventListener('resize', resizeHandler)
    // Aborts a running transfer so the remote rz/sz is not left waiting, and
    // stops the bridge from writing into a disposed terminal.
    zmodem?.dispose()
    zmodem = null
    transfer.value = null
    await channel?.close()
    channel = null
    await store.closeActiveSession()
    connection = null
    stream?.close()
    stream = null
    clearCredentials()
    if (status.value !== 'error') status.value = 'closed'
    if (!keepTerminal) {
      terminal?.dispose()
      terminal = null
      fitAddon = null
    }
  } finally {
    closing.value = false
    closeGuard = false
  }
}

// An explicit disconnect has to leave the terminal page. Staying on a closed
// session invites a reload that can never succeed, because the one-time ticket
// is already gone; the server list is where a new session can be started.
async function disconnectAndLeave() {
  await closeTerminal()
  // An explicit disconnect ends the target memory too: a later refresh should
  // not silently reopen a session the user deliberately closed.
  forgetRememberedWebSSHTarget(sessionId.value)
  await router.replace('/remote-servers')
}

onMounted(connect)
onBeforeUnmount(() => {
  if (unloadHandler) window.removeEventListener('beforeunload', unloadHandler)
  if (resizeHandler) window.removeEventListener('resize', resizeHandler)
  zmodem?.dispose()
  zmodem = null
  terminal?.dispose()
  terminal = null
  fitAddon = null
})

unloadHandler = () => { void closeTerminal() }
window.addEventListener('beforeunload', unloadHandler)

onBeforeRouteLeave(async (to) => {
  if (to.name === 'web-sftp' && to.params.sessionId === sessionId.value) return true
  await closeTerminal()
  return true
})
</script>

<style scoped>
.terminal-card { display: grid; grid-template-columns: minmax(0, 1fr); gap: 14px; padding: 16px; }
.header-actions { display: flex; gap: 8px; }
.terminal-toolbar { display: flex; align-items: center; justify-content: space-between; gap: 12px; }
.transport-alert { overflow-wrap: anywhere; }
.session-id { color: var(--tm-muted); font-size: 12px; overflow-wrap: anywhere; }
.transfer-panel { display: grid; grid-template-columns: minmax(0, 1fr); gap: 8px; padding: 12px; border: 1px solid var(--tm-border); border-radius: 8px; }
.transfer-head { display: flex; align-items: center; gap: 10px; }
.transfer-name { flex: 1; min-width: 0; overflow: hidden; font-weight: 600; text-overflow: ellipsis; white-space: nowrap; }
.transfer-meta { display: flex; align-items: center; justify-content: space-between; gap: 12px; color: var(--tm-muted); font-size: 12px; }
.transfer-hint { overflow-wrap: anywhere; }
.zmodem-file-input { display: none; }
.terminal { min-height: 520px; padding: 12px; background: #0b1220; border-radius: 10px; overflow: hidden; }
.terminal.hidden { display: none; }
.remote-ended { display: flex; align-items: center; justify-content: space-between; gap: 12px; flex-wrap: wrap; }
.remote-ended-reason { color: var(--tm-muted); font-size: 13px; overflow-wrap: anywhere; }
@media (max-width: 760px) { .terminal { min-height: 420px; } }
</style>
