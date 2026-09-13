<template>
  <section class="tm-page websftp-page">
    <PageHeader :title="t('websftp.title')" :description="t('websftp.description')">
      <div class="header-actions">
        <el-button :disabled="status !== 'connected'" @click="goTerminal">{{ t('websftp.terminal') }}</el-button>
        <el-button type="danger" :disabled="status === 'closed' || closing" :loading="closing" @click="disconnectAndLeave">
          {{ t('webssh.close') }}
        </el-button>
      </div>
    </PageHeader>

    <div class="tm-card file-card">
      <div class="path-bar">
        <el-button :disabled="currentPath === '/' || status !== 'connected'" @click="openParent">{{ t('websftp.parent') }}</el-button>
        <el-input v-model="currentPath" :disabled="status !== 'connected'" @keyup.enter="reload">
          <template #append>
            <el-button :disabled="status !== 'connected'" @click="reload">{{ t('websftp.open') }}</el-button>
          </template>
        </el-input>
        <label class="upload-button" :class="{ disabled: status !== 'connected' || transferring }">
          {{ transferring ? t('websftp.transferring') : t('websftp.upload') }}
          <input type="file" :disabled="status !== 'connected' || transferring" @change="upload" />
        </label>
      </div>

      <el-alert
        v-if="transferError"
        class="state-alert"
        type="error"
        show-icon
        :closable="false"
        :title="transferError"
      />

      <!-- A failed handshake used to fall through to the empty-directory state,
           which read as "this server has no files" instead of "we never got
           in". DataState now owns the error surface and its retry re-runs the
           connect instead of only re-listing a channel that does not exist. -->
      <DataState
        :loading="loading"
        :error="loadError"
        :empty="!loadError && !entries.length"
        :error-label="errorMessage || t('websftp.loadFailed')"
        :retry-label="t('common.retry')"
        :empty-label="t('websftp.empty')"
        @retry="retryLoad"
      >
        <el-table :data="entries" row-key="path" @row-click="activateRow">
          <el-table-column prop="name" min-width="220" :label="t('websftp.name')">
            <template #default="{ row }">
              <button class="entry-name" :data-type="row.type" @click.stop="openEntry(row)">{{ row.name }}</button>
            </template>
          </el-table-column>
          <el-table-column prop="type" width="110" :label="t('websftp.type')" />
          <el-table-column width="130" :label="t('websftp.size')">
            <template #default="{ row }">{{ formatSize(row.size) }}</template>
          </el-table-column>
          <el-table-column prop="permissions" width="130" :label="t('websftp.permissions')" />
          <el-table-column min-width="180" :label="t('websftp.modifiedAt')">
            <template #default="{ row }">{{ formatTime(row.modifiedAt) }}</template>
          </el-table-column>
          <el-table-column width="260" :label="t('websftp.actions')">
            <template #default="{ row }">
              <el-button size="small" :disabled="row.type === 'directory'" @click.stop="download(row)">{{ t('websftp.download') }}</el-button>
              <el-button size="small" @click.stop="rename(row)">{{ t('websftp.rename') }}</el-button>
              <el-button size="small" type="danger" @click.stop="remove(row)">{{ t('websftp.delete') }}</el-button>
            </template>
          </el-table-column>
        </el-table>
      </DataState>

      <div v-if="progress.total" class="transfer-panel">
        <span class="transfer-path">{{ progress.path }}</span>
        <el-progress :percentage="progressPercentage" :stroke-width="8" />
      </div>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { onBeforeRouteLeave, useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { ElMessage, ElMessageBox } from 'element-plus'
import PageHeader from '../components/PageHeader.vue'
import {
  forgetRememberedWebSSHTarget,
  readRememberedWebSSHTarget,
  rememberWebSSHTarget,
  useWebSSHStore,
} from '../stores/webssh'
import { createWebSocketByteStream, websshWebSocketURL, type DuplexByteStream } from '../webssh/byte-stream'
import { useHostKeyVerifier } from '../webssh/host-key'
import { connectSSH } from '../webssh/ssh-client'
import { downloadBlob } from '../webssh/download'
import {
  deleteEntry,
  downloadFile,
  isSFTPError,
  listDirectory,
  renameEntry,
  SFTP_MAX_TRANSFER_BYTES,
  uploadFile,
  type SFTPClient,
  type SFTPEntry,
} from '../webssh/sftp'

type WebSFTPStatus = 'connecting' | 'connected' | 'error' | 'closed'

const { t, locale } = useI18n()
const route = useRoute()
const router = useRouter()
const store = useWebSSHStore()
const verifyHostKey = useHostKeyVerifier()

const sessionId = computed(() => String(route.params.sessionId || ''))
const activeConnection = computed(() => (
  store.activeSession?.sessionId === sessionId.value ? store.activeSession.connection : null
))
const status = ref<WebSFTPStatus>('connecting')
const loading = ref(false)
const closing = ref(false)
const transferring = ref(false)
const currentPath = ref('/')
const entries = ref<SFTPEntry[]>([])
const selectedEntry = ref<SFTPEntry | null>(null)
const errorMessage = ref('')
const transferError = ref('')
const loadError = ref(false)
const progress = ref({ path: '', transferred: 0, total: 0 })

let client: SFTPClient | null = null
let stream: DuplexByteStream | null = null
let closeGuard = false
let unloadHandler: (() => void) | null = null

const progressPercentage = computed(() => (
  progress.value.total ? Math.floor((progress.value.transferred / progress.value.total) * 100) : 0
))

function stableError(error: unknown, fallback: string) {
  if (isSFTPError(error)) {
    if (error.kind === 'permission-denied') return t('websftp.permissionDenied')
    if (error.kind === 'not-found') return t('websftp.notFound')
    if (error.kind === 'closed') return t('websftp.closed')
  }
  return fallback
}

function formatSize(size: number) {
  if (size < 1024) return `${size} B`
  if (size < 1024 ** 2) return `${(size / 1024).toFixed(1)} KiB`
  if (size < 1024 ** 3) return `${(size / 1024 ** 2).toFixed(1)} MiB`
  return `${(size / 1024 ** 3).toFixed(1)} GiB`
}

function formatTime(value?: string) {
  if (!value) return '-'
  return new Intl.DateTimeFormat(locale.value, { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(value))
}

function activateRow(row: SFTPEntry) {
  selectedEntry.value = row
}

function resetProgress() {
  progress.value = { path: '', transferred: 0, total: 0 }
}

async function connect() {
  try {
    status.value = 'connecting'
    loadError.value = false
    errorMessage.value = ''

    // Opening SFTP straight from the server list only produces a pending
    // ticket: nobody has performed the SSH handshake yet. Bootstrap it here
    // exactly like the terminal does, otherwise the view dies on a missing
    // session and the user only sees a disabled toolbar and an empty list.
    if (!activeConnection.value) {
      const pending = store.takePendingSession(sessionId.value)
      if (!pending) {
        // A refresh drops the one-time ticket. Fall back to the remembered
        // target instead of dead-ending, exactly like the terminal view.
        const remembered = readRememberedWebSSHTarget(sessionId.value)
        if (remembered) {
          await router.replace({ path: '/remote-servers', query: { ssh: remembered } })
          return
        }
        throw new Error(t('webssh.missingSession'))
      }
      stream = createWebSocketByteStream(websshWebSocketURL(pending))
      const connection = await connectSSH({
        stream,
        username: pending.username,
        password: pending.password,
        privateKey: pending.privateKey,
        passphrase: pending.passphrase,
        hostKeyVerifier: verifyHostKey,
      })
      // Publish before opening the channel so a later navigation to the
      // terminal can reuse this transport instead of needing a new ticket.
      store.registerActiveSession(sessionId.value, pending, connection, pending.remoteServerId)
      rememberWebSSHTarget(sessionId.value, pending.remoteServerId || '')
    }
    if (!activeConnection.value) throw new Error(t('webssh.missingSession'))

    client = await activeConnection.value.openSFTP()
    currentPath.value = await client.realpath('.')
    status.value = 'connected'
    await reload()
  } catch (error) {
    status.value = 'error'
    loadError.value = true
    errorMessage.value = error instanceof Error ? error.message : t('websftp.connectFailed')
  }
}

// Retry has to distinguish "the listing failed on a live channel" from "we
// never connected": only the second one can be recovered by re-handshaking.
function retryLoad() {
  if (status.value === 'connected') {
    void reload()
    return
  }
  void connect()
}

async function reload() {
  if (!client || status.value === 'closed') return
  loading.value = true
  loadError.value = false
  errorMessage.value = ''
  try {
    entries.value = await listDirectory(client, currentPath.value)
  } catch (error) {
    loadError.value = true
    errorMessage.value = stableError(error, t('websftp.loadFailed'))
  } finally {
    loading.value = false
  }
}

function openEntry(entry: SFTPEntry) {
  selectedEntry.value = entry
  if (entry.type !== 'directory') return
  currentPath.value = entry.path
  void reload()
}

function openParent() {
  if (currentPath.value === '/') return
  const parent = currentPath.value.slice(0, currentPath.value.lastIndexOf('/'))
  currentPath.value = parent || '/'
  void reload()
}

async function upload(event: Event) {
  const input = event.target as HTMLInputElement
  const file = input.files?.[0]
  input.value = ''
  if (!file || !client) return
  if (file.size > SFTP_MAX_TRANSFER_BYTES) {
    transferError.value = t('websftp.tooLarge')
    return
  }

  transferring.value = true
  transferError.value = ''
  resetProgress()
  try {
    await uploadFile(client, file, `${currentPath.value}/${file.name}`, (eventData) => {
      progress.value = eventData
    })
    ElMessage.success(t('websftp.uploaded'))
    await reload()
  } catch (error) {
    transferError.value = stableError(error, t('websftp.transferFailed'))
  } finally {
    transferring.value = false
    progress.value = { path: '', transferred: 0, total: 0 }
  }
}

async function download(entry: SFTPEntry) {
  if (!client || entry.type === 'directory') return
  transferring.value = true
  transferError.value = ''
  resetProgress()
  try {
    const blob = await downloadFile(client, entry.path, entry.size, (eventData) => {
      progress.value = eventData
    })
    downloadBlob(entry.name, blob)
  } catch (error) {
    transferError.value = stableError(error, t('websftp.transferFailed'))
  } finally {
    transferring.value = false
    progress.value = { path: '', transferred: 0, total: 0 }
  }
}

async function rename(entry: SFTPEntry) {
  if (!client) return
  try {
    await ElMessageBox.confirm(t('websftp.renameConfirm', { name: entry.name }), t('websftp.renameTitle'), { type: 'warning' })
    const { value } = await ElMessageBox.prompt(t('websftp.renamePrompt'), t('websftp.renameTitle'), {
      inputValue: entry.name,
      inputValidator: (name: string) => Boolean(name.trim()) || t('websftp.nameRequired'),
    })
    await renameEntry(client, entry, `${currentPath.value}/${value.trim()}`)
    await reload()
    ElMessage.success(t('websftp.renamed'))
  } catch (error) {
    if (error === 'cancel' || error === 'close') return
    transferError.value = stableError(error, t('websftp.operationFailed'))
  }
}

async function remove(entry: SFTPEntry) {
  if (!client) return
  try {
    await ElMessageBox.confirm(t('websftp.deleteConfirm', { name: entry.name }), t('websftp.deleteTitle'), { type: 'warning' })
    await deleteEntry(client, entry)
    await reload()
    ElMessage.success(t('websftp.deleted'))
  } catch (error) {
    if (error === 'cancel' || error === 'close') return
    transferError.value = stableError(error, t('websftp.operationFailed'))
  }
}

function goTerminal() {
  void router.push({ name: 'webssh-terminal', params: { sessionId: sessionId.value } })
}

async function closeSFTP() {
  if (closeGuard) return
  closeGuard = true
  closing.value = true
  try {
    await store.closeActiveSession()
    stream?.close()
    stream = null
    client = null
    status.value = 'closed'
    entries.value = []
    transferError.value = ''
    resetProgress()
  } finally {
    closing.value = false
    closeGuard = false
  }
}

// An explicit disconnect has to leave the file manager. Staying on a closed
// session invites a reload that can never succeed, because the one-time ticket
// is already gone; the server list is where a new session can be started.
async function disconnectAndLeave() {
  await closeSFTP()
  // An explicit disconnect ends the target memory too: a later refresh should
  // not silently reopen a session the user deliberately closed.
  forgetRememberedWebSSHTarget(sessionId.value)
  await router.replace('/remote-servers')
}

// The SFTP channel is per-view, but the SSH transport is shared with the
// terminal. Handing the terminal a live connection means releasing only the
// SFTP subsystem here, never the authenticated session itself.
async function closeSFTPChannel() {
  await client?.close()
  client = null
}

onMounted(connect)
onBeforeUnmount(() => {
  if (unloadHandler) window.removeEventListener('beforeunload', unloadHandler)
})

unloadHandler = () => { void closeSFTP() }
window.addEventListener('beforeunload', unloadHandler)

onBeforeRouteLeave(async (to) => {
  if (to.name === 'webssh-terminal' && to.params.sessionId === sessionId.value) {
    await closeSFTPChannel()
    return true
  }
  await closeSFTP()
  return true
})
</script>

<style scoped>
.header-actions { display: flex; gap: 8px; }
.file-card { display: grid; grid-template-columns: minmax(0, 1fr); gap: 14px; padding: 16px; }
.path-bar { display: grid; grid-template-columns: auto minmax(260px, 1fr) auto; gap: 10px; align-items: center; }
.upload-button { display: inline-flex; align-items: center; justify-content: center; min-width: 88px; height: 32px; padding: 0 14px; border: 1px solid var(--tm-primary); border-radius: 6px; color: var(--tm-primary); font-size: 14px; cursor: pointer; }
.upload-button.disabled { border-color: var(--tm-border); color: var(--tm-muted); cursor: not-allowed; }
.upload-button input { display: none; }
.entry-name { border: 0; padding: 0; background: transparent; color: var(--tm-primary); cursor: pointer; text-align: left; }
.state-alert { margin: 0; }
.transfer-panel { display: grid; grid-template-columns: minmax(0, 1fr); gap: 8px; padding-top: 4px; }
.transfer-path { color: var(--tm-muted); font-size: 12px; overflow-wrap: anywhere; }
@media (max-width: 720px) {
  .path-bar { grid-template-columns: 1fr; }
  .header-actions { width: 100%; }
}
</style>
