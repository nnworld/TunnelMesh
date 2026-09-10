<template>
  <section class="tm-page">
    <PageHeader :title="t('agentDetail.title')" />
    <div class="tm-card">
      <DataState :loading="loading" :error="error" :empty="!metadata" :rows="5" :error-label="t('agentDetail.failed')" :retry-label="t('common.retry')" :empty-label="t('agentDetail.unavailable')" @retry="load">
        <template v-if="metadata">
          <div class="card-header"><span>{{ t('agentDetail.metadata') }}</span><StatusTag :kind="metadata.stale ? 'warning' : 'success'" :label="metadata.stale ? t('agentDetail.stale') : t('agentDetail.fresh')" /></div>
          <el-descriptions :column="3" border>
            <el-descriptions-item label="Agent ID">{{ metadata.agentId }}</el-descriptions-item>
            <el-descriptions-item :label="t('agentDetail.instanceId')">{{ metadata.instanceId || '—' }}</el-descriptions-item>
            <el-descriptions-item :label="t('agentDetail.nodeId')">{{ metadata.nodeId }}</el-descriptions-item>
            <el-descriptions-item :label="t('agentDetail.epoch')">{{ metadata.epoch }}</el-descriptions-item>
            <el-descriptions-item :label="t('agentDetail.revision')">{{ metadata.revision }}</el-descriptions-item>
            <el-descriptions-item :label="t('agentDetail.activeInstances')">{{ metadata.instances?.length || 0 }}</el-descriptions-item>
            <el-descriptions-item :label="t('agentDetail.activeConnections')">{{ clusterConnections.filter(connection => connection.healthy).length }}</el-descriptions-item>
            <el-descriptions-item :label="t('agentDetail.reportedAt')">{{ formatDate(metadata.reportedAt) }}</el-descriptions-item>
            <el-descriptions-item :label="t('agentDetail.updatedAt')">{{ formatDate(metadata.updatedAt) }}</el-descriptions-item>
          </el-descriptions>

          <div class="section-title">{{ t('agentDetail.instances') }}</div>
          <el-table :data="metadata.instances || []" class="metadata-table">
            <el-table-column prop="instanceId" :label="t('agentDetail.instanceId')" min-width="180" />
            <el-table-column prop="nodeId" :label="t('agentDetail.nodeId')" min-width="160" />
            <el-table-column prop="epoch" :label="t('agentDetail.epoch')" width="90" />
            <el-table-column prop="revision" :label="t('agentDetail.revision')" width="100" />
            <el-table-column :label="t('agentDetail.status')" width="110"><template #default="scope"><StatusTag :kind="scope.row.stale ? 'warning' : 'success'" :label="scope.row.stale ? t('agentDetail.stale') : t('agentDetail.fresh')" /></template></el-table-column>
            <el-table-column prop="connectionCount" :label="t('agentDetail.connectionCount')" width="110" />
            <el-table-column :label="t('agentDetail.reportedAt')" min-width="170"><template #default="scope">{{ formatDate(scope.row.reportedAt) }}</template></el-table-column>
          </el-table>

          <div class="connection-header">
            <div class="section-title">{{ t('agentDetail.connections') }}</div>
            <el-button data-test="refresh-connections" :loading="connectionsLoading" @click="loadConnections">{{ t('agentDetail.refresh') }}</el-button>
          </div>
          <el-alert v-if="connectionsError" type="error" :title="t('agentDetail.connectionsLoadFailed')" show-icon :closable="false" class="connection-error" />
          <el-table v-else :data="clusterConnections" class="metadata-table">
            <el-table-column prop="instanceId" :label="t('agentDetail.instanceId')" min-width="170" />
            <el-table-column prop="connectionId" :label="t('agentDetail.connectionId')" min-width="170" />
            <el-table-column prop="serverNodeId" :label="t('agentDetail.serverNodeId')" min-width="150" />
            <el-table-column prop="serverNodeAddress" :label="t('agentDetail.serverNodeAddress')" min-width="170"><template #default="scope">{{ scope.row.serverNodeAddress || '—' }}</template></el-table-column>
            <el-table-column prop="connectionEpoch" :label="t('agentDetail.connectionEpoch')" width="110" />
            <el-table-column :label="t('agentDetail.status')" width="100"><template #default="scope"><StatusTag :kind="scope.row.healthy ? 'success' : 'danger'" :label="scope.row.healthy ? t('agentDetail.healthy') : t('agentDetail.unhealthy')" /></template></el-table-column>
            <el-table-column prop="activeStreams" :label="t('agentDetail.activeStreams')" width="110" />
            <el-table-column :label="t('agentDetail.lastHeartbeatAt')" min-width="170"><template #default="scope">{{ formatDate(scope.row.lastHeartbeatAt) }}</template></el-table-column>
            <el-table-column :label="t('agentDetail.leaseExpiresAt')" min-width="170"><template #default="scope">{{ formatDate(scope.row.leaseExpiresAt) }}</template></el-table-column>
            <el-table-column :label="t('agentDetail.actions')" width="100" fixed="right">
              <template #default="scope">
                <el-button link type="danger" :data-test="`close-connection-${scope.row.connectionId}`" @click="openCloseConnection(scope.row)">{{ t('agentDetail.close') }}</el-button>
              </template>
            </el-table-column>
          </el-table>

          <el-empty v-if="!metadata.items.length" :description="t('agentDetail.empty')" />
          <el-table v-else :data="metadata.items" class="metadata-table">
            <el-table-column prop="name" :label="t('agentDetail.name')" />
            <el-table-column prop="source" :label="t('agentDetail.source')" />
            <el-table-column :label="t('agentDetail.value')"><template #default="scope"><StatusTag v-if="scope.row.redacted" kind="info" :label="t('agentDetail.redacted')" /><span v-else>{{ scope.row.value || '—' }}</span></template></el-table-column>
          </el-table>
        </template>
      </DataState>
    </div>

    <el-dialog v-model="closeVisible" :title="t('agentDetail.closeTitle')" width="min(520px,94vw)">
      <el-descriptions v-if="closeTarget" :column="1" border>
        <el-descriptions-item label="Agent ID">{{ closeTarget.agentId }}</el-descriptions-item>
        <el-descriptions-item :label="t('agentDetail.connectionId')">{{ closeTarget.connectionId }}</el-descriptions-item>
        <el-descriptions-item :label="t('agentDetail.connectionEpoch')">{{ closeTarget.connectionEpoch }}</el-descriptions-item>
        <el-descriptions-item :label="t('agentDetail.serverNodeId')">{{ closeTarget.serverNodeId }}</el-descriptions-item>
        <el-descriptions-item :label="t('agentDetail.activeStreams')">{{ closeTarget.activeStreams }}</el-descriptions-item>
      </el-descriptions>
      <div v-if="closeError" data-test="connection-close-error" class="close-error">{{ closeError }}</div>
      <template #footer>
        <el-button @click="closeVisible = false">{{ t('agentDetail.cancel') }}</el-button>
        <el-button type="danger" data-test="confirm-close-connection" :loading="closing" @click="confirmCloseConnection">{{ t('agentDetail.close') }}</el-button>
      </template>
    </el-dialog>
  </section>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import PageHeader from '../components/PageHeader.vue'
import DataState from '../components/DataState.vue'
import StatusTag from '../components/StatusTag.vue'
import { useFormatDateTime } from '../i18n/format'
import { APIError, closeAgentConnection, getAgentMetadata, listAgentConnections, type AgentMetadata, type ClusterAgentConnection } from '../api/client'

const route = useRoute()
const { t } = useI18n()
const loading = ref(true)
const error = ref(false)
const metadata = ref<AgentMetadata | null>(null)
const clusterConnections = ref<ClusterAgentConnection[]>([])
const connectionsLoading = ref(false)
const connectionsError = ref(false)
const closeVisible = ref(false)
const closing = ref(false)
const closeError = ref('')
const closeTarget = ref<ClusterAgentConnection | null>(null)
const formatDate = useFormatDateTime()

async function load() {
  loading.value = true; error.value = false
  try {
    metadata.value = await getAgentMetadata(String(route.params.id), true)
    await loadConnections()
  }
  catch { error.value = true }
  finally { loading.value = false }
}

async function loadConnections() {
  connectionsLoading.value = true; connectionsError.value = false
  try { clusterConnections.value = await listAgentConnections(String(route.params.id)) }
  catch { connectionsError.value = true }
  finally { connectionsLoading.value = false }
}

function openCloseConnection(connection: ClusterAgentConnection) {
  closeTarget.value = connection
  closeError.value = ''
  closeVisible.value = true
}

async function confirmCloseConnection() {
  if (!closeTarget.value || closing.value) return
  closing.value = true; closeError.value = ''
  try {
    await closeAgentConnection(closeTarget.value.agentId, closeTarget.value.connectionId, closeTarget.value.connectionEpoch)
    closeVisible.value = false
    await loadConnections()
  } catch (error) {
    closeError.value = error instanceof APIError && error.status === 503
      ? t('agentDetail.remoteNodeUnavailable')
      : t('agentDetail.closeFailed')
  } finally {
    closing.value = false
  }
}

onMounted(load)
</script>

<style scoped>
.section-title {
  margin-top: 18px;
  font-size: 14px;
  font-weight: 600;
  color: #1f2937;
}
.connection-header {
  display: flex; align-items: center; justify-content: space-between; gap: 12px; margin-top: 18px;
}
.connection-header .section-title { margin-top: 0; }
.connection-error, .close-error { margin-top: 12px; }
.close-error { color: #d93026; font-size: 13px; }
</style>
