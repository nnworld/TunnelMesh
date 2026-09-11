<template>
  <section class="tm-page">
    <PageHeader :title="t('clients.title')" :description="t('clients.description')">
      <el-button type="primary" :loading="loading" @click="load">{{ t('clients.refresh') }}</el-button>
    </PageHeader>

    <div class="tm-card">
      <el-form class="filter-form" label-position="top" @submit.prevent="query">
        <div class="filter-grid">
          <el-form-item v-if="auth.isAdmin" :label="t('clients.owner')">
            <el-input v-model="filters.ownerUserId" :placeholder="t('clients.owner')" clearable />
          </el-form-item>
          <el-form-item :label="t('clients.token')">
            <el-input v-model="filters.tokenId" :placeholder="t('clients.token')" clearable />
          </el-form-item>
          <el-form-item :label="t('clients.serverNode')">
            <el-input v-model="filters.serverNodeId" :placeholder="t('clients.serverNode')" clearable />
          </el-form-item>
          <el-form-item :label="t('clients.status')">
            <el-select v-model="filters.status" clearable>
              <el-option v-for="option in statusOptions" :key="option" :value="option" :label="statusLabel(option)" />
            </el-select>
          </el-form-item>
          <el-form-item :label="t('clients.agent')">
            <el-input v-model="filters.agentId" :placeholder="t('clients.agent')" clearable />
          </el-form-item>
          <el-form-item :label="t('clients.keyword')">
            <el-input v-model="filters.keyword" :placeholder="t('clients.keyword')" clearable />
          </el-form-item>
        </div>
        <div class="filter-actions">
          <el-button @click="reset">{{ t('clients.reset') }}</el-button>
          <el-button type="primary" @click="query">{{ t('clients.query') }}</el-button>
        </div>
      </el-form>

      <div class="summary-grid">
        <div v-for="card in summaryCards" :key="card.label" class="summary-card">
          <span>{{ card.label }}</span>
          <strong>{{ card.value }}</strong>
        </div>
      </div>

      <DataState
        :loading="loading"
        :error="error"
        :empty="!items.length"
        :error-label="t('common.loadFailed')"
        :retry-label="t('common.retry')"
        :empty-label="t('clients.empty')"
        @retry="load"
      >
        <el-table :data="items" row-key="id">
          <el-table-column prop="instanceId" :label="t('clients.instanceId')" min-width="210" />
          <el-table-column prop="ownerUserId" :label="t('clients.owner')" min-width="170" />
          <el-table-column :label="t('clients.version')" width="120"><template #default="{ row }">{{ row.version || '—' }}</template></el-table-column>
          <el-table-column :label="t('clients.platform')" width="130"><template #default="{ row }">{{ row.platform || '—' }}</template></el-table-column>
          <el-table-column :label="t('clients.hostname')" min-width="150"><template #default="{ row }">{{ row.hostname || '—' }}</template></el-table-column>
          <el-table-column :label="t('clients.serverNodes')" min-width="150"><template #default="{ row }">{{ formatList(row.serverNodeIds) }}</template></el-table-column>
          <el-table-column prop="activeConnections" :label="t('clients.activeConnections')" width="110" />
          <el-table-column prop="activeStreams" :label="t('clients.activeStreams')" width="100" />
          <el-table-column :label="t('clients.status')" width="150">
            <template #default="{ row }"><StatusTag :kind="statusKind(row.status)" :label="statusLabel(row.status)" /></template>
          </el-table-column>
          <el-table-column :label="t('clients.lastSeen')" width="180"><template #default="{ row }">{{ formatDate(row.lastSeenAt) }}</template></el-table-column>
          <el-table-column :label="t('clients.actions')" width="100" fixed="right">
            <template #default="{ row }"><el-button link type="primary" @click="showDetail(row)">{{ t('clients.detail') }}</el-button></template>
          </el-table-column>
        </el-table>
        <div v-if="nextCursor" class="pager"><el-button :loading="loading" @click="loadMore">{{ t('clients.loadMore') }}</el-button></div>
      </DataState>
    </div>

    <el-drawer v-model="detailVisible" :title="clientDetailTitle" size="min(760px,100vw)">
      <DataState
        :loading="detailLoading"
        :error="detailError"
        :empty="!detailClient"
        :error-label="t('common.loadFailed')"
        :retry-label="t('common.retry')"
        :empty-label="t('clients.empty')"
        @retry="retryDetail"
      >
        <div v-if="detailClient" class="detail-content">
          <el-descriptions :column="2" border>
            <el-descriptions-item :label="t('clients.id')">{{ detailClient.id }}</el-descriptions-item>
            <el-descriptions-item :label="t('clients.instanceId')">{{ detailClient.instanceId }}</el-descriptions-item>
            <el-descriptions-item :label="t('clients.owner')">{{ detailClient.ownerUserId }}</el-descriptions-item>
            <el-descriptions-item :label="t('clients.status')"><StatusTag :kind="statusKind(detailClient.status)" :label="statusLabel(detailClient.status)" /></el-descriptions-item>
            <el-descriptions-item :label="t('clients.version')">{{ detailClient.version || '—' }}</el-descriptions-item>
            <el-descriptions-item :label="t('clients.commit')">{{ detailClient.commit || '—' }}</el-descriptions-item>
            <el-descriptions-item :label="t('clients.platform')">{{ detailClient.platform || '—' }}</el-descriptions-item>
            <el-descriptions-item :label="t('clients.hostname')">{{ detailClient.hostname || '—' }}</el-descriptions-item>
            <el-descriptions-item :label="t('clients.processStartAt')">{{ formatDate(detailClient.processStartAt) }}</el-descriptions-item>
            <el-descriptions-item :label="t('clients.lastSeen')">{{ formatDate(detailClient.lastSeenAt) }}</el-descriptions-item>
            <el-descriptions-item :label="t('clients.tokens')">{{ formatList(detailClient.tokenIds) }}</el-descriptions-item>
            <el-descriptions-item :label="t('clients.agents')">{{ formatList(detailClient.agentIds) }}</el-descriptions-item>
            <el-descriptions-item :label="t('clients.serverNodes')">{{ formatList(detailClient.serverNodeIds) }}</el-descriptions-item>
            <el-descriptions-item :label="t('clients.activeConnections')">{{ detailClient.activeConnections }}</el-descriptions-item>
            <el-descriptions-item :label="t('clients.activeStreams')">{{ detailClient.activeStreams }}</el-descriptions-item>
          </el-descriptions>

          <div class="section-title">{{ t('clients.metadata') }}</div>
          <el-table :data="metadataRows" class="section-table">
            <el-table-column prop="name" :label="t('clients.metadataName')" min-width="150" />
            <el-table-column prop="value" :label="t('clients.metadataValue')" min-width="220" />
            <template #empty>{{ t('clients.metadataEmpty') }}</template>
          </el-table>

          <div class="section-title">{{ t('clients.listeners') }}</div>
          <el-table :data="detailClient.listeners" class="section-table">
            <el-table-column prop="protocol" :label="t('clients.protocol')" width="100" />
            <el-table-column prop="listenAddress" :label="t('clients.listenAddress')" min-width="170" />
            <el-table-column prop="agentId" :label="t('clients.agent')" min-width="180" />
            <el-table-column :label="t('clients.enabled')" width="100"><template #default="{ row }">{{ row.enabled ? t('common.yes') : t('common.no') }}</template></el-table-column>
            <template #empty>{{ t('clients.listenersEmpty') }}</template>
          </el-table>

          <div class="connection-header">
            <div class="section-title">{{ t('clients.connections') }}</div>
            <el-button :loading="connectionsLoading" @click="loadConnections">{{ t('clients.refresh') }}</el-button>
          </div>
          <el-alert v-if="connectionsError" type="error" :title="t('clients.connectionsLoadFailed')" show-icon :closable="false" class="connection-error" />
          <el-table v-else :data="connections" class="section-table" v-loading="connectionsLoading">
            <el-table-column prop="connectionId" :label="t('clients.connectionId')" min-width="190" />
            <el-table-column prop="tokenId" :label="t('clients.token')" min-width="150" />
            <el-table-column prop="serverNodeId" :label="t('clients.serverNode')" min-width="150" />
            <el-table-column prop="connectionEpoch" :label="t('clients.connectionEpoch')" width="110" />
            <el-table-column prop="activeStreams" :label="t('clients.activeStreams')" width="100" />
            <el-table-column prop="healthScore" :label="t('clients.healthScore')" width="100" />
            <el-table-column :label="t('clients.acquiredAt')" width="180"><template #default="{ row }">{{ formatDate(row.acquiredAt) }}</template></el-table-column>
            <el-table-column :label="t('clients.lastHeartbeatAt')" width="180"><template #default="{ row }">{{ formatDate(row.lastHeartbeatAt) }}</template></el-table-column>
            <el-table-column :label="t('clients.expiresAt')" width="180"><template #default="{ row }">{{ formatDate(row.expiresAt) }}</template></el-table-column>
            <el-table-column :label="t('clients.local')" width="90"><template #default="{ row }">{{ row.local ? t('common.yes') : t('common.no') }}</template></el-table-column>
            <el-table-column :label="t('clients.actions')" width="120" fixed="right">
              <template #default="{ row }"><el-button link type="danger" @click="openCloseConnection(row)">{{ t('clients.closeConnection') }}</el-button></template>
            </el-table-column>
            <template #empty>{{ t('clients.connectionsEmpty') }}</template>
          </el-table>
        </div>
      </DataState>
    </el-drawer>

    <el-dialog v-model="closeVisible" :title="t('clients.closeTitle')" width="min(560px,94vw)">
      <el-descriptions v-if="closeTarget" :column="1" border>
        <el-descriptions-item :label="t('clients.instanceId')">{{ detailClient?.instanceId || '—' }}</el-descriptions-item>
        <el-descriptions-item :label="t('clients.connectionId')">{{ closeTarget.connectionId }}</el-descriptions-item>
        <el-descriptions-item :label="t('clients.connectionEpoch')">{{ closeTarget.connectionEpoch }}</el-descriptions-item>
        <el-descriptions-item :label="t('clients.serverNode')">{{ closeTarget.serverNodeId }}</el-descriptions-item>
        <el-descriptions-item :label="t('clients.activeStreams')">{{ closeTarget.activeStreams }}</el-descriptions-item>
      </el-descriptions>
      <p class="close-warning">{{ t('clients.closeConfirm') }}</p>
      <el-alert v-if="closeError" type="error" :title="closeError" show-icon :closable="false" />
      <template #footer>
        <el-button @click="closeVisible = false">{{ t('clients.cancel') }}</el-button>
        <el-button type="danger" :loading="closing" @click="closeConnection">{{ t('clients.closeConnection') }}</el-button>
      </template>
    </el-dialog>
  </section>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { useI18n } from 'vue-i18n'
import PageHeader from '../components/PageHeader.vue'
import DataState from '../components/DataState.vue'
import StatusTag from '../components/StatusTag.vue'
import { useFormatDateTime } from '../i18n/format'
import { useAuthStore } from '../stores/auth'
import { APIError, closeClientConnection, getClientDetail, listClientConnections, listClients, type ClientConnection, type ClientInstance, type ClientStatus } from '../api/client'

const { t } = useI18n()
const auth = useAuthStore()
const formatDate = useFormatDateTime()
const items = ref<ClientInstance[]>([])
const loading = ref(false)
const error = ref(false)
const nextCursor = ref('')
const filters = reactive({ ownerUserId: '', tokenId: '', serverNodeId: '', status: '' as '' | ClientStatus, agentId: '', keyword: '' })
const detailVisible = ref(false)
const detailLoading = ref(false)
const detailError = ref(false)
const detailClient = ref<ClientInstance | null>(null)
const connections = ref<ClientConnection[]>([])
const connectionsLoading = ref(false)
const connectionsError = ref(false)
const closeVisible = ref(false)
const closing = ref(false)
const closeError = ref('')
const closeTarget = ref<ClientConnection | null>(null)
const statusOptions = ['online', 'offline', 'stale', 'metadata_unavailable'] as const

const clientDetailTitle = computed(() => t('clients.detailTitle'))
const summaryCards = computed(() => [
  { label: t('clients.onlineClients'), value: items.value.filter(item => item.activeConnections > 0).length },
  { label: t('clients.activeConnections'), value: items.value.reduce((total, item) => total + item.activeConnections, 0) },
  { label: t('clients.activeStreams'), value: items.value.reduce((total, item) => total + item.activeStreams, 0) },
  { label: t('clients.metadataUnavailable'), value: items.value.filter(item => item.status === 'metadata_unavailable').length },
])
const metadataRows = computed(() => Object.entries(detailClient.value?.metadata || {}).map(([name, value]) => ({ name, value })))

async function load() {
  loading.value = true; error.value = false
  try {
    const page = await listClients({
      ownerUserId: filters.ownerUserId || undefined,
      tokenId: filters.tokenId || undefined,
      serverNodeId: filters.serverNodeId || undefined,
      status: filters.status || undefined,
      agentId: filters.agentId || undefined,
      keyword: filters.keyword || undefined,
      limit: 100,
    })
    items.value = page.items
    nextCursor.value = page.nextCursor || ''
  } catch {
    error.value = true
  } finally {
    loading.value = false
  }
}

async function loadMore() {
  if (!nextCursor.value) return
  loading.value = true; error.value = false
  try {
    const page = await listClients({
      ownerUserId: filters.ownerUserId || undefined,
      tokenId: filters.tokenId || undefined,
      serverNodeId: filters.serverNodeId || undefined,
      status: filters.status || undefined,
      agentId: filters.agentId || undefined,
      keyword: filters.keyword || undefined,
      cursor: nextCursor.value,
      limit: 100,
    })
    items.value.push(...page.items)
    nextCursor.value = page.nextCursor || ''
  } catch {
    error.value = true
  } finally {
    loading.value = false
  }
}

function query() { void load() }
function reset() {
  filters.ownerUserId = ''; filters.tokenId = ''; filters.serverNodeId = ''
  filters.status = ''; filters.agentId = ''; filters.keyword = ''
  void load()
}

async function showDetail(row: ClientInstance) {
  detailVisible.value = true; detailLoading.value = true; detailError.value = false
  detailClient.value = row; connections.value = []; connectionsError.value = false; connectionsLoading.value = true
  try {
    const [client, clientConnections] = await Promise.all([getClientDetail(row.id), listClientConnections(row.id)])
    detailClient.value = client
    connections.value = clientConnections
  } catch {
    detailError.value = true
  } finally {
    detailLoading.value = false; connectionsLoading.value = false
  }
}

async function retryDetail() {
  if (detailClient.value) await showDetail(detailClient.value)
}

async function loadConnections() {
  if (!detailClient.value) return
  connectionsLoading.value = true; connectionsError.value = false
  try { connections.value = await listClientConnections(detailClient.value.id) }
  catch { connectionsError.value = true }
  finally { connectionsLoading.value = false }
}

function openCloseConnection(connection: ClientConnection) {
  closeTarget.value = connection; closeError.value = ''; closeVisible.value = true
}

async function closeConnection() {
  if (!detailClient.value || !closeTarget.value) return
  closing.value = true; closeError.value = ''
  try {
    await closeClientConnection(detailClient.value.id, closeTarget.value.connectionId, closeTarget.value.connectionEpoch)
    ElMessage.success(t('clients.closed'))
    closeVisible.value = false
    await Promise.all([loadConnections(), load()])
  } catch (err) {
    if (err instanceof APIError && err.status === 503) closeError.value = t('clients.remoteNodeUnavailable')
    else if (err instanceof APIError && err.status === 409) closeError.value = t('clients.staleEpoch')
    else closeError.value = t('clients.closeFailed')
  } finally {
    closing.value = false
  }
}

function statusKind(status: ClientStatus) {
  if (status === 'online') return 'success' as const
  if (status === 'stale' || status === 'metadata_unavailable') return 'warning' as const
  return 'info' as const
}
function statusLabel(status: ClientStatus) { return t(`clients.statusLabel.${status}`) }
function formatList(values: string[]) { return values.length ? values.join('、') : '—' }

onMounted(load)
</script>

<style scoped>
.filter-form { margin-bottom: 18px; }
.filter-grid { display:grid; grid-template-columns:repeat(auto-fit,minmax(170px,1fr)); gap:12px; }
.filter-actions { display:flex; justify-content:flex-end; gap:8px; margin-top:4px; }
.summary-grid { display:grid; grid-template-columns:repeat(4,minmax(0,1fr)); gap:12px; margin-bottom:18px; }
.summary-card { display:flex; flex-direction:column; gap:6px; padding:16px; background:#f7fbff; border:1px solid var(--tm-border); border-radius:10px; }
.summary-card span { color:var(--tm-muted); font-size:13px; }
.summary-card strong { font-size:24px; line-height:1; }
.detail-content { display:flex; flex-direction:column; gap:18px; }
.section-title { margin-top:4px; font-size:15px; font-weight:650; }
.section-table { width:100%; }
.connection-header { display:flex; align-items:center; justify-content:space-between; gap:12px; }
.connection-error { margin-bottom:12px; }
.close-warning { margin:14px 0 0; color:var(--tm-muted); }
.pager { display:flex; justify-content:center; padding:18px 0 4px; }
@media(max-width:760px){.summary-grid{grid-template-columns:repeat(2,minmax(0,1fr))}.filter-actions{justify-content:stretch}.filter-actions .el-button{flex:1}}
</style>
