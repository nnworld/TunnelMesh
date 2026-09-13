<template>
  <section class="tm-page remote-servers-page">
    <PageHeader :title="t('remoteServers.title')" :description="t('remoteServers.description')">
      <el-button type="primary" @click="openCreate">{{ t('remoteServers.create') }}</el-button>
    </PageHeader>

    <div class="tm-card table-card">
      <div class="toolbar">
        <el-input v-model="filterKeyword" class="keyword-input" clearable :placeholder="t('remoteServers.keyword')" @keyup.enter="reload" @clear="reload" />
        <el-select v-model="filterAgentId" class="agent-filter" clearable filterable :placeholder="t('remoteServers.allAgents')" @change="reload">
          <el-option v-for="agent in enabledAgents" :key="agent.id" :label="agent.name || agent.id" :value="agent.id" />
        </el-select>
        <el-segmented v-model="filterStatus" :options="statusOptions" @change="reload" />
        <el-button :loading="loading" @click="reload">{{ t('remoteServers.refresh') }}</el-button>
      </div>
      <DataState :loading="loading" :error="loadError" :empty="!items.length" :error-label="t('remoteServers.loadFailed')" :retry-label="t('common.retry')" :empty-label="t('remoteServers.empty')" @retry="reload">
        <el-table :data="items" row-key="id">
          <el-table-column prop="name" :label="t('remoteServers.name')" min-width="150" />
          <el-table-column prop="host" :label="t('remoteServers.host')" min-width="140" />
          <el-table-column prop="port" :label="t('remoteServers.port')" width="90" />
          <el-table-column prop="defaultUsername" :label="t('remoteServers.username')" min-width="120" />
          <el-table-column :label="t('remoteServers.credential')" min-width="150">
            <template #default="{ row }">
              <div class="credential-cell">
                <span>{{ credentialName(row) }}</span>
                <StatusTag v-if="row.credentialId" :kind="row.credentialHasSecret ? 'success' : 'info'" :label="row.credentialHasSecret ? t('remoteServers.autoAuth') : t('remoteServers.manualAuth')" />
              </div>
            </template>
          </el-table-column>
          <el-table-column :label="t('remoteServers.agent')" min-width="170">
            <template #default="{ row }">
              <div class="agent-cell">
                <span>{{ agentName(row) }}</span>
                <StatusTag :kind="row.agentOnline ? 'success' : row.agentOnline === false ? 'warning' : 'info'" :label="agentStatusLabel(row)" />
              </div>
            </template>
          </el-table-column>
          <el-table-column :label="t('remoteServers.status')" width="110">
            <template #default="{ row }"><StatusTag :kind="row.status === 'enabled' ? 'success' : row.status === 'disabled' ? 'warning' : 'info'" :label="statusLabel(row.status)" /></template>
          </el-table-column>
          <el-table-column :label="t('remoteServers.lastError')" min-width="130">
            <template #default="{ row }"><span v-if="row.lastErrorClass" class="error-text">{{ row.lastErrorClass }}</span><span v-else>—</span></template>
          </el-table-column>
          <el-table-column :label="t('remoteServers.actions')" width="330" fixed="right">
            <template #default="{ row }">
              <el-button link type="primary" @click="showDetail(row)">{{ t('remoteServers.detail') }}</el-button>
              <el-button v-if="row.status !== 'deleted'" link type="primary" @click="openEdit(row)">{{ t('remoteServers.edit') }}</el-button>
              <el-button v-if="row.status === 'enabled' && row.enabled" link type="primary" :loading="sshAutoAuthLoading === row.id" @click="openSSH(row)">{{ t('remoteServers.ssh') }}</el-button>
              <el-button v-if="row.status === 'enabled' && row.enabled" link type="primary" :disabled="sshAutoAuthLoading === row.id" @click="openSFTP(row)">{{ t('remoteServers.sftp') }}</el-button>
              <el-button v-if="row.status !== 'deleted'" link type="danger" @click="remove(row)">{{ t('remoteServers.delete') }}</el-button>
              <el-button v-else link type="primary" @click="restore(row)">{{ t('remoteServers.restore') }}</el-button>
            </template>
          </el-table-column>
        </el-table>
      </DataState>
      <div v-if="page.nextCursor" class="load-more"><el-button :loading="loadingMore" :disabled="!page.hasMore" @click="loadMore">{{ t('remoteServers.loadMore') }}</el-button></div>
    </div>

    <!-- Active sessions sit below the server list: the list is the primary
         task on this page, and the sessions are a recovery surface a user only
         needs after hitting the active-session quota. -->
    <div class="tm-card sessions-card">
      <div class="sessions-head">
        <div class="sessions-heading">
          <div class="sessions-title">{{ t('remoteServers.sessions.title') }}</div>
          <div class="sessions-description">{{ t('remoteServers.sessions.description') }}</div>
        </div>
        <el-button size="small" :loading="sessionsLoading" @click="loadSessions">{{ t('remoteServers.sessions.refresh') }}</el-button>
      </div>
      <DataState :loading="sessionsLoading" :error="sessionsError" :empty="!activeSessions.length" :error-label="t('remoteServers.sessions.loadFailed')" :retry-label="t('common.retry')" :empty-label="t('remoteServers.sessions.empty')" @retry="loadSessions">
        <el-table :data="activeSessions" row-key="id">
          <el-table-column :label="t('remoteServers.sessions.target')" min-width="200">
            <template #default="{ row }">
              <div class="session-target">
                <span class="session-target-name">{{ sessionTargetName(row) }}</span>
                <el-tag v-if="sessionTargetName(row) === row.remoteServerId" size="small" type="info">{{ t('remoteServers.sessions.unknownTarget') }}</el-tag>
              </div>
              <div class="session-target-id">{{ row.remoteServerId }}</div>
            </template>
          </el-table-column>
          <el-table-column :label="t('remoteServers.sessions.createdAt')" min-width="170">
            <template #default="{ row }">{{ formatDate(row.createdAt) }}</template>
          </el-table-column>
          <el-table-column :label="t('remoteServers.sessions.expiresAt')" min-width="170">
            <template #default="{ row }">{{ formatDate(row.expiresAt) }}</template>
          </el-table-column>
          <el-table-column :label="t('remoteServers.sessions.actions')" width="120" fixed="right">
            <template #default="{ row }">
              <el-popconfirm :title="t('remoteServers.sessions.disconnectConfirm')" @confirm="disconnectSession(row)">
                <template #reference>
                  <el-button link type="danger" :loading="disconnectingId === row.id">{{ t('remoteServers.sessions.disconnect') }}</el-button>
                </template>
              </el-popconfirm>
            </template>
          </el-table-column>
        </el-table>
      </DataState>
    </div>

    <el-dialog v-model="formVisible" :title="editingServer ? t('remoteServers.editTitle') : t('remoteServers.createTitle')" width="min(560px,94vw)" @closed="resetForm">
      <el-form ref="formRef" :model="form" :rules="rules" label-position="top" @submit.prevent="save">
        <el-form-item :label="t('remoteServers.name')" prop="name"><el-input v-model="form.name" maxlength="128" /></el-form-item>
        <div class="form-grid">
          <el-form-item :label="t('remoteServers.host')" prop="host"><el-input v-model="form.host" maxlength="255" /></el-form-item>
          <el-form-item :label="t('remoteServers.port')" prop="port"><el-input-number v-model="form.port" :min="1" :max="65535" controls-position="right" /></el-form-item>
        </div>
        <el-form-item :label="t('remoteServers.username')" prop="defaultUsername"><el-input v-model="form.defaultUsername" maxlength="64" /></el-form-item>
        <el-form-item :label="t('remoteServers.agent')" prop="agentId">
          <el-select v-model="form.agentId" filterable :placeholder="t('remoteServers.agent')">
            <el-option v-for="agent in enabledAgents" :key="agent.id" :label="agent.name || agent.id" :value="agent.id" />
          </el-select>
        </el-form-item>
        <el-form-item :label="t('remoteServers.credential')">
          <el-select v-model="form.credentialId" clearable :placeholder="t('remoteServers.noneCredential')">
            <el-option v-for="credential in activeCredentials" :key="credential.id" :label="credential.name" :value="credential.id" />
          </el-select>
        </el-form-item>
        <el-form-item :label="t('remoteServers.enabled')"><el-switch v-model="form.enabled" /></el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="formVisible = false">{{ t('remoteServers.cancel') }}</el-button>
        <el-button type="primary" :loading="saving" @click="save">{{ t('remoteServers.save') }}</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="sshVisible" :title="sshDialogTitle" width="min(460px,94vw)" @closed="clearSSHPassword">
      <el-alert type="info" show-icon :title="t('remoteServers.sshDescription')" :closable="false" />
      <el-form label-position="top" @submit.prevent="connectSSH">
        <el-form-item :label="t('remoteServers.username')" required><el-input v-model="sshUsername" maxlength="64" /></el-form-item>
        <el-form-item :label="t('remoteServers.credential')">
          <el-select v-model="sshCredentialId" clearable :placeholder="t('remoteServers.noneCredential')">
            <el-option v-for="credential in activeCredentials" :key="credential.id" :label="credential.name" :value="credential.id" />
          </el-select>
        </el-form-item>
        <el-form-item :label="t('remoteServers.password')">
          <el-input v-model="sshPassword" type="password" show-password autocomplete="new-password" :placeholder="t('remoteServers.passwordHelp')" />
        </el-form-item>
      </el-form>
      <el-alert v-if="sshError" type="error" show-icon :title="sshError" :closable="false" />
      <template #footer>
        <el-button @click="sshVisible = false">{{ t('remoteServers.cancel') }}</el-button>
        <el-button type="primary" :loading="sshSubmitting" @click="connectSSH">{{ t('remoteServers.connect') }}</el-button>
      </template>
    </el-dialog>

    <el-drawer v-model="detailVisible" :title="t('remoteServers.detailTitle')" size="min(500px,100vw)">
      <DataState :loading="detailLoading" :error="detailError" :empty="!detailServer" :error-label="t('remoteServers.loadFailed')" :retry-label="t('common.retry')" :empty-label="t('remoteServers.empty')" @retry="retryDetail">
        <dl v-if="detailServer" class="detail-list">
          <dt>{{ t('remoteServers.id') }}</dt><dd>{{ detailServer.id }}</dd>
          <dt>{{ t('remoteServers.owner') }}</dt><dd>{{ detailServer.ownerUserId }}</dd>
          <dt>{{ t('remoteServers.name') }}</dt><dd>{{ detailServer.name }}</dd>
          <dt>{{ t('remoteServers.host') }}</dt><dd>{{ detailServer.host }}:{{ detailServer.port }}</dd>
          <dt>{{ t('remoteServers.username') }}</dt><dd>{{ detailServer.defaultUsername }}</dd>
        <dt>{{ t('remoteServers.credential') }}</dt><dd>{{ credentialName(detailServer) }}</dd>
        <dt>{{ t('remoteServers.authMode') }}</dt><dd>{{ detailServer.credentialHasSecret ? t('remoteServers.autoAuth') : t('remoteServers.manualAuth') }}</dd>
          <dt>{{ t('remoteServers.agent') }}</dt><dd>{{ agentName(detailServer) }}</dd>
          <dt>{{ t('remoteServers.status') }}</dt><dd>{{ statusLabel(detailServer.status) }}</dd>
          <dt>{{ t('remoteServers.lastConnected') }}</dt><dd>{{ formatDate(detailServer.lastConnectedAt) }}</dd>
          <dt>{{ t('remoteServers.lastResult') }}</dt><dd>{{ detailServer.lastResult || '—' }}</dd>
          <dt>{{ t('remoteServers.lastError') }}</dt><dd>{{ detailServer.lastErrorClass || '—' }}</dd>
          <dt>{{ t('remoteServers.createdAt') }}</dt><dd>{{ formatDate(detailServer.createdAt) }}</dd>
          <dt>{{ t('remoteServers.updatedAt') }}</dt><dd>{{ formatDate(detailServer.updatedAt) }}</dd>
          <dt v-if="detailServer.deletedAt">{{ t('remoteServers.deletedAt') }}</dt><dd v-if="detailServer.deletedAt">{{ formatDate(detailServer.deletedAt) }}</dd>
        </dl>
      </DataState>
    </el-drawer>
  </section>
</template>

<script setup lang="ts">
import { computed, reactive, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage, ElMessageBox, type FormInstance, type FormRules } from 'element-plus'
import { useI18n } from 'vue-i18n'
import PageHeader from '../components/PageHeader.vue'
import DataState from '../components/DataState.vue'
import StatusTag from '../components/StatusTag.vue'
import { useFormatDateTime } from '../i18n/format'
import { getAgents, type Agent } from '../api/client'
import { listCredentials, type Credential } from '../api/credentials'
import {
  createRemoteServer, deleteRemoteServer, getRemoteServer, listRemoteServers,
  restoreRemoteServer, updateRemoteServer, type RemoteServer,
} from '../api/remote-servers'
import { closeWebSSHSession, createWebSSHSession, listWebSSHSessions, type WebSSHSession } from '../api/webssh'
import { useWebSSHStore } from '../stores/webssh'
import { sshErrorMessage } from './remote-server-errors'

type StatusFilter = 'enabled' | 'disabled' | 'deleted' | 'all'

const { t } = useI18n()
const router = useRouter()
const route = useRoute()
const websshStore = useWebSSHStore()
const formatDate = useFormatDateTime()
const page = ref<{ items: RemoteServer[]; nextCursor?: string; hasMore?: boolean }>({ items: [] })
const items = computed(() => page.value.items)
const loading = ref(false)
const loadingMore = ref(false)
const loadError = ref(false)
const filterKeyword = ref('')
const filterAgentId = ref('')
const filterStatus = ref<StatusFilter>('all')
const agents = ref<Agent[]>([])
const credentials = ref<Credential[]>([])
const resourcesError = ref(false)
const statusOptions = computed(() => [
  { label: t('remoteServers.enabled'), value: 'enabled' },
  { label: t('remoteServers.disabled'), value: 'disabled' },
  { label: t('remoteServers.deleted'), value: 'deleted' },
  { label: t('remoteServers.all'), value: 'all' },
])
const enabledAgents = computed(() => agents.value.filter(agent => agent.enabled))
const activeCredentials = computed(() => credentials.value.filter(credential => credential.enabled && credential.status === 'active'))

// The session API only returns IDs, so names are resolved from the loaded server
// page first and fetched per missing ID second. Resolution is best effort: an
// unreachable or deleted server falls back to the raw ID plus an "unknown" tag.
const activeSessions = ref<WebSSHSession[]>([])
const sessionsLoading = ref(false)
const sessionsError = ref(false)
const disconnectingId = ref('')
const sessionServerNames = ref<Record<string, string>>({})

const formRef = ref<FormInstance>()
const formVisible = ref(false)
const editingServer = ref<RemoteServer | null>(null)
const saving = ref(false)
const form = reactive({ name: '', host: '', port: 22, defaultUsername: '', credentialId: '', agentId: '', enabled: true })
const rules = reactive<FormRules>({
  name: [{ required: true, message: t('remoteServers.name'), trigger: 'blur' }],
  host: [{ required: true, message: t('remoteServers.host'), trigger: 'blur' }],
  port: [{ required: true, type: 'number', min: 1, max: 65535, message: t('remoteServers.port'), trigger: 'change' }],
  defaultUsername: [{ required: true, message: t('remoteServers.username'), trigger: 'blur' }],
  agentId: [{ required: true, message: t('remoteServers.agent'), trigger: 'change' }],
})

const detailVisible = ref(false)
const detailLoading = ref(false)
const detailError = ref(false)
const detailServer = ref<RemoteServer | null>(null)
const sshVisible = ref(false)
const sshServer = ref<RemoteServer | null>(null)
const sshUsername = ref('')
const sshCredentialId = ref('')
const sshPassword = ref('')
const sshError = ref('')
const sshSubmitting = ref(false)
const sshTargetMode = ref<'terminal' | 'sftp'>('terminal')
// ID of the row whose stored-credential session is being created, so the action
// buttons show progress instead of letting the user double-connect.
const sshAutoAuthLoading = ref('')
const sshDialogTitle = computed(() => (
  sshTargetMode.value === 'sftp' ? t('remoteServers.sftpTitle') : t('remoteServers.sshTitle')
))

async function loadResources() {
  resourcesError.value = false
  try {
    const [agentPage, credentialPage] = await Promise.all([
      getAgents({ limit: 200 }),
      listCredentials({ status: 'active', limit: 200 }),
    ])
    agents.value = agentPage.items
    credentials.value = credentialPage.items
  } catch {
    resourcesError.value = true
    ElMessage.error(t('remoteServers.loadFailed'))
  }
}

async function load(cursor?: string) {
  const appending = Boolean(cursor)
  appending ? loadingMore.value = true : loading.value = true
  if (!appending) loadError.value = false
  try {
    const result = await listRemoteServers({
      keyword: filterKeyword.value || undefined,
      agentId: filterAgentId.value || undefined,
      status: filterStatus.value,
      cursor,
      limit: 20,
    })
    page.value = appending ? {
      items: [...page.value.items, ...result.items],
      nextCursor: result.nextCursor,
      hasMore: result.hasMore,
    } : result
    if (!appending) void openSSHFromQuery()
  } catch {
    if (!appending) loadError.value = true
    ElMessage.error(t('remoteServers.loadFailed'))
  } finally {
    loading.value = false
    loadingMore.value = false
  }
}

function reload() { page.value = { items: [] }; void load() }
function loadMore() { if (page.value.nextCursor) void load(page.value.nextCursor) }

async function loadSessions() {
  sessionsLoading.value = true
  sessionsError.value = false
  try {
    const result = await listWebSSHSessions({ limit: 50 })
    activeSessions.value = result.items
    await resolveSessionTargets()
  } catch {
    sessionsError.value = true
  } finally {
    sessionsLoading.value = false
  }
}

async function resolveSessionTargets() {
  const loaded = new Set(items.value.map(item => item.id))
  const missing = [...new Set(activeSessions.value.map(session => session.remoteServerId))]
    .filter(id => id && !loaded.has(id) && !sessionServerNames.value[id])
  if (!missing.length) return
  const resolved = await Promise.all(missing.map(async id => {
    try {
      const server = await getRemoteServer(id)
      return [id, server.name] as const
    } catch {
      // A deleted or inaccessible server must not break the session list; the
      // row falls back to the raw ID with an "unknown target" marker.
      return [id, ''] as const
    }
  }))
  sessionServerNames.value = { ...sessionServerNames.value, ...Object.fromEntries(resolved) }
}

function sessionTargetName(session: WebSSHSession) {
  const loaded = items.value.find(item => item.id === session.remoteServerId)
  return loaded?.name || sessionServerNames.value[session.remoteServerId] || session.remoteServerId
}

async function disconnectSession(session: WebSSHSession) {
  disconnectingId.value = session.id
  try {
    await closeWebSSHSession(session.id)
    ElMessage.success(t('remoteServers.sessions.disconnected'))
    await loadSessions()
  } catch {
    ElMessage.error(t('remoteServers.sessions.disconnectFailed'))
  } finally {
    disconnectingId.value = ''
  }
}

function resetForm() {
  form.name = ''
  form.host = ''
  form.port = 22
  form.defaultUsername = ''
  form.credentialId = ''
  form.agentId = ''
  form.enabled = true
  formRef.value?.clearValidate()
}

function openCreate() { editingServer.value = null; resetForm(); formVisible.value = true }
async function openEdit(row: RemoteServer) {
  editingServer.value = row
  resetForm()
  form.name = row.name
  form.host = row.host
  form.port = row.port
  form.defaultUsername = row.defaultUsername
  form.credentialId = row.credentialId || ''
  form.agentId = row.agentId
  form.enabled = row.enabled
  formVisible.value = true
}

async function save() {
  const valid = await formRef.value?.validate().catch(() => false)
  if (!valid) return
  saving.value = true
  try {
    const input = {
      name: form.name.trim(), host: form.host.trim(), port: form.port,
      defaultUsername: form.defaultUsername.trim(), credentialId: form.credentialId || undefined,
      agentId: form.agentId, enabled: form.enabled,
    }
    const saved = editingServer.value
      ? await updateRemoteServer(editingServer.value.id, input, crypto.randomUUID())
      : await createRemoteServer(input, crypto.randomUUID())
    formVisible.value = false
    ElMessage.success(t(editingServer.value ? 'remoteServers.updated' : 'remoteServers.created'))
    if (page.value.items.some(item => item.id === saved.id) || (!editingServer.value && filterStatus.value !== 'deleted')) await reload()
  } catch {
    ElMessage.error(t('remoteServers.operationFailed'))
  } finally { saving.value = false }
}

async function showDetail(row: RemoteServer) {
  detailVisible.value = true
  detailLoading.value = true
  detailError.value = false
  detailServer.value = row
  try { detailServer.value = await getRemoteServer(row.id) }
  catch { detailError.value = true }
  finally { detailLoading.value = false }
}
async function retryDetail() { if (detailServer.value) await showDetail(detailServer.value) }

function openSSH(row: RemoteServer) {
  sshTargetMode.value = 'terminal'
  void startSSH(row)
}

// A closed terminal routes back with ?ssh=<serverId> so the user can reconnect
// without searching for the server again. The parameter is cleared right away so
// a later refresh does not reopen the dialog.
async function openSSHFromQuery() {
  const wanted = typeof route.query.ssh === 'string' ? route.query.ssh : ''
  if (!wanted) return
  const target = items.value.find(item => item.id === wanted)
  if (!target) return
  await router.replace({ query: { ...route.query, ssh: undefined } })
  openSSH(target)
}

function openSFTP(row: RemoteServer) {
  sshTargetMode.value = 'sftp'
  void startSSH(row)
}

// Auto-authentication: when the bound credential stores a secret, the Server
// hands it over once together with the ticket, so the user lands directly in the
// terminal or the file manager. Every other case (no credential, public key
// only, no encryption key configured on the Server) keeps the manual dialog.
function canAutoAuth(row: RemoteServer) {
  return Boolean(row.credentialId && row.credentialHasSecret && row.defaultUsername.trim())
}

async function startSSH(row: RemoteServer) {
  sshServer.value = row
  sshError.value = ''
  if (!canAutoAuth(row)) {
    prepareSSHDialog(row)
    return
  }
  sshAutoAuthLoading.value = row.id
  try {
    if (await connectWithStoredCredential(row)) return
    // No auth material came back, so fall back to the manual prompt.
    prepareSSHDialog(row)
  } catch (error) {
    // Session creation failed (agent offline, quota reached, forbidden). The
    // dialog still opens so the user sees the reason and can retry by hand.
    prepareSSHDialog(row)
    sshError.value = sshErrorMessage(error, t)
  } finally {
    sshAutoAuthLoading.value = ''
  }
}

// Returns false when the Server issued a ticket without auth material, which
// means the stored credential cannot authenticate on its own.
async function connectWithStoredCredential(row: RemoteServer): Promise<boolean> {
  const ticket = await createWebSSHSession(row.id, {
    username: row.defaultUsername.trim(),
    credentialId: row.credentialId,
  }, crypto.randomUUID())
  const auth = ticket.auth
  if (!auth) return false
  websshStore.setPendingSession({
    ticket,
    username: row.defaultUsername.trim(),
    password: auth.kind === 'password' ? auth.password : undefined,
    privateKey: auth.kind === 'private_key' ? auth.privateKey : undefined,
    passphrase: auth.kind === 'private_key' ? auth.passphrase : undefined,
    credentialId: row.credentialId,
    remoteServerId: row.id,
  })
  await router.push(sshTargetMode.value === 'sftp'
    ? `/webssh/${ticket.sessionId}/sftp`
    : `/webssh/${ticket.sessionId}`)
  return true
}

function prepareSSHDialog(row: RemoteServer) {
  sshServer.value = row
  sshUsername.value = row.defaultUsername
  sshCredentialId.value = row.credentialId || ''
  sshError.value = ''
  clearSSHPassword()
  sshVisible.value = true
}

function clearSSHPassword() { sshPassword.value = '' }

async function connectSSH() {
  if (!sshServer.value) return
  if (!sshUsername.value.trim()) { sshError.value = t('remoteServers.usernameRequired'); return }
  sshSubmitting.value = true
  sshError.value = ''
  try {
    const ticket = await createWebSSHSession(sshServer.value.id, {
      username: sshUsername.value.trim(),
      credentialId: sshCredentialId.value || undefined,
    }, crypto.randomUUID())
    websshStore.setPendingSession({
      ticket,
      username: sshUsername.value.trim(),
      password: sshPassword.value || undefined,
      credentialId: sshCredentialId.value || undefined,
      remoteServerId: sshServer.value.id,
    })
    clearSSHPassword()
    sshVisible.value = false
    await router.push(sshTargetMode.value === 'sftp'
      ? `/webssh/${ticket.sessionId}/sftp`
      : `/webssh/${ticket.sessionId}`)
  } catch (error) {
    sshError.value = sshErrorMessage(error, t)
  } finally {
    clearSSHPassword()
    sshSubmitting.value = false
  }
}

async function remove(row: RemoteServer) {
  try {
    await ElMessageBox.confirm(t('remoteServers.deleteConfirm'), t('remoteServers.deleteTitle'), { type: 'warning' })
    await deleteRemoteServer(row.id)
    await reload()
    ElMessage.success(t('remoteServers.deletedMessage'))
  } catch (error) { reportActionError(error) }
}

async function restore(row: RemoteServer) {
  try {
    await restoreRemoteServer(row.id)
    await reload()
    ElMessage.success(t('remoteServers.restored'))
  } catch (error) { reportActionError(error) }
}

function reportActionError(error: unknown) {
  if (error === 'cancel' || error === 'close') return
  ElMessage.error(t('remoteServers.operationFailed'))
}

function statusLabel(status: RemoteServer['status']) {
  if (status === 'enabled') return t('remoteServers.enabled')
  if (status === 'disabled') return t('remoteServers.disabled')
  return t('remoteServers.deleted')
}
function agentName(row: RemoteServer) { return row.agentName || row.agentId }
function credentialName(row: RemoteServer) { return row.credentialName || row.credentialId || t('remoteServers.noneCredential') }
function agentStatusLabel(row: RemoteServer) {
  if (row.agentOnline) return t('remoteServers.agentOnline')
  if (row.agentOnline === false) return t('remoteServers.agentOffline')
  return t('remoteServers.agentStatusUnknown')
}

// The server list must land before sessions are resolved, otherwise every
// session row triggers a redundant per-ID fetch for a server the list already
// contains.
async function bootstrap() {
  await load()
  await loadSessions()
}

void loadResources()
void bootstrap()
</script>

<style scoped>
.toolbar{display:flex;gap:12px;align-items:center;flex-wrap:wrap;padding:14px 16px;border-bottom:1px solid var(--tm-border)}.keyword-input{width:min(240px,100%)}.agent-filter{width:min(220px,100%)}.load-more{display:flex;justify-content:center;padding:14px}.agent-cell{display:flex;gap:8px;align-items:center}.credential-cell{display:flex;gap:8px;align-items:center;min-width:0}.error-text{color:var(--el-color-danger)}.form-grid{display:grid;grid-template-columns:1fr 140px;gap:12px}.detail-list{display:grid;grid-template-columns:120px 1fr;gap:12px 16px;margin:0}.detail-list dt{color:var(--tm-muted);font-weight:600}.detail-list dd{margin:0;word-break:break-all}:deep(.el-table){--el-table-border-color:#edf1f7}@media(max-width:640px){.form-grid{grid-template-columns:1fr}}
.sessions-card{overflow:hidden}.sessions-head{display:flex;gap:12px;align-items:flex-start;justify-content:space-between;flex-wrap:wrap;padding:14px 16px;border-bottom:1px solid var(--tm-border)}.sessions-heading{min-width:0}.sessions-title{font-weight:600}.sessions-description{margin-top:4px;color:var(--tm-muted);font-size:13px}.session-target{display:flex;gap:8px;align-items:center}.session-target-name{font-weight:600;word-break:break-all}.session-target-id{margin-top:2px;color:var(--tm-muted);font-size:12px;word-break:break-all}
</style>
