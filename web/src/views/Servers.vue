<template>
  <section class="tm-page">
    <PageHeader :title="t('servers.title')" :description="t('servers.description')">
      <el-button type="primary" :loading="loading" @click="load">{{ t('servers.refresh') }}</el-button>
    </PageHeader>

    <el-alert class="self-register-notice" type="info" show-icon :title="t('servers.selfRegister')" :closable="false" />

    <div class="tm-card">
      <DataState
        :loading="loading"
        :error="error"
        :empty="!items.length"
        :error-label="t('common.loadFailed')"
        :retry-label="t('common.retry')"
        :empty-label="t('servers.empty')"
        @retry="load"
      >
        <el-table :data="items" row-key="id">
          <el-table-column prop="name" :label="t('servers.name')" min-width="140" />
          <el-table-column prop="id" :label="t('servers.id')" min-width="190" />
          <el-table-column prop="address" :label="t('servers.address')" min-width="160" />
          <el-table-column prop="epoch" :label="t('servers.epoch')" width="90" />
          <el-table-column :label="t('servers.statusLabel')" width="110">
            <template #default="{ row }"><StatusTag :kind="statusKind(row.status)" :label="statusLabel(row.status)" /></template>
          </el-table-column>
          <el-table-column prop="activeConnections" :label="t('servers.activeConnections')" width="120" />
          <el-table-column prop="activeStreams" :label="t('servers.activeStreams')" width="110" />
          <el-table-column prop="healthScore" :label="t('servers.healthScore')" width="110" />
          <el-table-column :label="t('servers.lastSeen')" width="180"><template #default="{ row }">{{ formatDate(row.lastSeenAt) }}</template></el-table-column>
          <el-table-column :label="t('servers.leaseExpires')" width="180"><template #default="{ row }">{{ formatDate(row.expiresAt) }}</template></el-table-column>
          <el-table-column :label="t('servers.actions')" width="250" fixed="right">
            <template #default="{ row }">
              <el-button link type="primary" @click="showDetail(row)">{{ t('servers.detail') }}</el-button>
              <el-button
                link
                :type="row.enabled ? 'warning' : 'success'"
                :disabled="row.status === 'deleted' || busy"
                @click="setEnabled(row)"
              >{{ row.enabled ? t('servers.disable') : t('servers.enable') }}</el-button>
              <el-button
                link
                :type="row.status === 'deleted' ? 'success' : 'danger'"
                :disabled="busy"
                @click="row.status === 'deleted' ? restore(row) : remove(row)"
              >{{ row.status === 'deleted' ? t('servers.restore') : t('servers.delete') }}</el-button>
            </template>
          </el-table-column>
        </el-table>
        <div v-if="nextCursor" class="pager"><el-button :loading="loading" @click="loadMore">{{ t('servers.loadMore') }}</el-button></div>
      </DataState>
    </div>

    <el-drawer v-model="detailVisible" :title="t('servers.detailTitle')" size="min(520px,100vw)">
      <DataState
        :loading="detailLoading"
        :error="detailError"
        :empty="!detailNode"
        :error-label="t('common.loadFailed')"
        :retry-label="t('common.retry')"
        :empty-label="t('servers.empty')"
        @retry="retryDetail"
      >
        <el-descriptions v-if="detailNode" :column="1" border>
          <el-descriptions-item :label="t('servers.id')">{{ detailNode.id }}</el-descriptions-item>
          <el-descriptions-item :label="t('servers.name')">{{ detailNode.name }}</el-descriptions-item>
          <el-descriptions-item :label="t('servers.address')">{{ detailNode.address || '—' }}</el-descriptions-item>
          <el-descriptions-item :label="t('servers.epoch')">{{ detailNode.epoch }}</el-descriptions-item>
          <el-descriptions-item :label="t('servers.statusLabel')"><StatusTag :kind="statusKind(detailNode.status)" :label="statusLabel(detailNode.status)" /></el-descriptions-item>
          <el-descriptions-item :label="t('servers.enabled')">{{ detailNode.enabled ? t('common.yes') : t('common.no') }}</el-descriptions-item>
          <el-descriptions-item :label="t('servers.deletedAt')">{{ formatDate(detailNode.deletedAt) }}</el-descriptions-item>
          <el-descriptions-item :label="t('servers.lastSeen')">{{ formatDate(detailNode.lastSeenAt) }}</el-descriptions-item>
          <el-descriptions-item :label="t('servers.leaseExpires')">{{ formatDate(detailNode.expiresAt) }}</el-descriptions-item>
          <el-descriptions-item :label="t('servers.activeConnections')">{{ detailNode.activeConnections }}</el-descriptions-item>
          <el-descriptions-item :label="t('servers.activeStreams')">{{ detailNode.activeStreams }}</el-descriptions-item>
          <el-descriptions-item :label="t('servers.healthScore')">{{ detailNode.healthScore }}</el-descriptions-item>
          <el-descriptions-item :label="t('servers.createdAt')">{{ formatDate(detailNode.createdAt) }}</el-descriptions-item>
          <el-descriptions-item :label="t('servers.updatedAt')">{{ formatDate(detailNode.updatedAt) }}</el-descriptions-item>
        </el-descriptions>
      </DataState>
    </el-drawer>
  </section>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { useI18n } from 'vue-i18n'
import PageHeader from '../components/PageHeader.vue'
import DataState from '../components/DataState.vue'
import StatusTag from '../components/StatusTag.vue'
import { deleteServerNode, getServerNode, getServerNodes, restoreServerNode, updateServerNode, type ServerNode, type ServerNodeStatus } from '../api/client'
import { useFormatDateTime } from '../i18n/format'

const { t } = useI18n()
const items = ref<ServerNode[]>([])
const loading = ref(false)
const error = ref(false)
const nextCursor = ref('')
const busy = ref(false)
const detailVisible = ref(false)
const detailLoading = ref(false)
const detailError = ref(false)
const detailNode = ref<ServerNode | null>(null)
const formatDate = useFormatDateTime()

async function load() {
  loading.value = true; error.value = false
  try {
    const page = await getServerNodes({ limit: 100 })
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
    const page = await getServerNodes({ cursor: nextCursor.value, limit: 100 })
    items.value.push(...page.items)
    nextCursor.value = page.nextCursor || ''
  } catch {
    error.value = true
  } finally {
    loading.value = false
  }
}

async function setEnabled(row: ServerNode) {
  busy.value = true
  try {
    const updated = await updateServerNode(row.id, { enabled: !row.enabled })
    replace(updated)
    ElMessage.success(updated.enabled ? t('servers.enabledMessage') : t('servers.disabledMessage'))
  } catch {
    ElMessage.error(t('servers.operationFailed'))
  } finally {
    busy.value = false
  }
}

async function remove(row: ServerNode) {
  try {
    await ElMessageBox.confirm(t('servers.deleteConfirm'), t('servers.deleteTitle'), { type: 'warning' })
  } catch {
    return
  }
  busy.value = true
  try {
    const updated = await deleteServerNode(row.id)
    replace(updated)
    ElMessage.success(t('servers.deletedMessage'))
  } catch {
    ElMessage.error(t('servers.operationFailed'))
  } finally {
    busy.value = false
  }
}

async function restore(row: ServerNode) {
  busy.value = true
  try {
    const updated = await restoreServerNode(row.id)
    replace(updated)
    ElMessage.success(t('servers.restoredMessage'))
  } catch {
    ElMessage.error(t('servers.operationFailed'))
  } finally {
    busy.value = false
  }
}

async function showDetail(row: ServerNode) {
  detailVisible.value = true; detailLoading.value = true; detailError.value = false
  if (!detailNode.value || detailNode.value.id !== row.id) detailNode.value = row
  try { detailNode.value = await getServerNode(row.id) }
  catch { detailError.value = true }
  finally { detailLoading.value = false }
}

async function retryDetail() {
  if (detailNode.value) await showDetail(detailNode.value)
}

function replace(updated: ServerNode) {
  const index = items.value.findIndex(item => item.id === updated.id)
  if (index >= 0) items.value[index] = updated
  if (detailNode.value?.id === updated.id) detailNode.value = updated
}

function statusKind(status: ServerNodeStatus) {
  if (status === 'online') return 'success' as const
  if (status === 'disabled') return 'warning' as const
  if (status === 'deleted') return 'danger' as const
  return 'info' as const
}

function statusLabel(status: ServerNodeStatus) { return t(`servers.status.${status}`) }

onMounted(load)
</script>

<style scoped>
.self-register-notice { margin-bottom: 16px; }
.pager { display: flex; justify-content: center; padding: 18px 0 4px; }
</style>
