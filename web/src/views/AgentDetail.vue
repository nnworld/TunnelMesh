<template>
  <section class="tm-page">
    <PageHeader :title="t('agentDetail.title')" />
    <div class="tm-card">
      <DataState :loading="loading" :error="error" :empty="!metadata" :rows="5" :error-label="t('agentDetail.failed')" :retry-label="t('common.retry')" :empty-label="t('agentDetail.unavailable')" @retry="load">
        <template v-if="metadata">
          <div data-test="agent-logical-summary">
            <div class="card-header">
              <span>{{ t('agentDetail.logicalSummary') }}</span>
              <StatusTag :kind="logicalOnline ? 'success' : 'warning'" :label="logicalOnline ? t('agentDetail.fresh') : t('agentDetail.stale')" />
            </div>
            <el-descriptions :column="3" border>
              <el-descriptions-item label="Agent ID">{{ metadata.agentId }}</el-descriptions-item>
              <el-descriptions-item :label="t('agentDetail.activeInstances')">{{ activeInstanceCount }}</el-descriptions-item>
              <el-descriptions-item :label="t('agentDetail.activeConnections')">{{ activeConnectionCount }}</el-descriptions-item>
              <el-descriptions-item :label="t('agentDetail.reportedAt')">{{ formatDate(latestReportedAt) }}</el-descriptions-item>
              <el-descriptions-item :label="t('agentDetail.updatedAt')">{{ formatDate(latestUpdatedAt) }}</el-descriptions-item>
            </el-descriptions>
          </div>

          <div class="section-title">{{ t('agentDetail.instances') }}</div>
          <el-table :data="metadata.instances || []" class="metadata-table" data-test="agent-instance-table">
            <el-table-column prop="instanceId" :label="t('agentDetail.instanceId')" min-width="180" />
            <el-table-column prop="nodeId" :label="t('agentDetail.nodeId')" min-width="160" />
            <el-table-column prop="epoch" :label="t('agentDetail.epoch')" width="90" />
            <el-table-column prop="revision" :label="t('agentDetail.revision')" width="100" />
            <el-table-column :label="t('agentDetail.status')" width="110"><template #default="scope"><StatusTag :kind="instanceOnline(scope.row.instanceId) ? 'success' : 'warning'" :label="instanceOnline(scope.row.instanceId) ? t('agentDetail.fresh') : t('agentDetail.stale')" /></template></el-table-column>
            <el-table-column :label="t('agentDetail.connectionCount')" width="110">
              <template #default="scope">{{ instanceConnectionCount(scope.row.instanceId) }}</template>
            </el-table-column>
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

    <div class="tm-card">
      <div class="policy-header">
        <div>
          <div class="section-title policy-title">{{ t('agentDetail.policyTitle') }}</div>
          <div class="policy-description">{{ t('agentDetail.policyDescription') }}</div>
        </div>
        <div class="policy-actions">
          <div v-if="auth.isAdmin" class="policy-status-filter">
            <el-button
              v-for="option in policyStatusOptions"
              :key="option.value"
              :type="policyStatus === option.value ? 'primary' : 'default'"
              size="small"
              :data-test="`policy-status-${option.value}`"
              @click="setPolicyStatus(option.value)"
            >{{ t(option.labelKey) }}</el-button>
          </div>
          <el-button v-if="auth.isAdmin" type="primary" data-test="create-agent-policy" @click="openCreatePolicy">{{ t('agentDetail.policyCreate') }}</el-button>
        </div>
      </div>
      <DataState
        :loading="policiesLoading"
        :error="policiesError"
        :empty="false"
        :error-label="t('agentDetail.policyLoadFailed')"
        :retry-label="t('common.retry')"
        @retry="loadPolicies"
      >
        <el-empty v-if="!policies.length" :description="t('agentDetail.policyEmpty')" />
        <el-table v-else :data="policies" row-key="id">
          <el-table-column prop="protocol" :label="t('agentDetail.policyProtocol')" width="100" />
          <el-table-column :label="t('agentDetail.policyTargetHost')" min-width="180">
            <template #default="scope">{{ formatPolicyTargetHost(scope.row.targetHost) }}</template>
          </el-table-column>
          <el-table-column :label="t('agentDetail.policyTargetPort')" width="130">
            <template #default="scope">{{ formatPolicyTargetPort(scope.row.targetPort) }}</template>
          </el-table-column>
          <el-table-column :label="t('agentDetail.policyAllowedCIDRs')" min-width="180">
            <template #default="scope">{{ formatPolicyList(scope.row.allowedCIDRs) }}</template>
          </el-table-column>
          <el-table-column :label="t('agentDetail.policyAllowedPorts')" min-width="160">
            <template #default="scope">{{ formatPolicyList(scope.row.allowedPorts) }}</template>
          </el-table-column>
          <el-table-column :label="t('agentDetail.status')" width="110">
            <template #default="scope">
              <StatusTag :kind="scope.row.deletedAt ? 'danger' : 'success'" :label="scope.row.deletedAt ? t('agentDetail.policyStatusDeleted') : t('agentDetail.policyStatusActive')" />
            </template>
          </el-table-column>
          <el-table-column :label="t('agentDetail.updatedAt')" min-width="170">
            <template #default="scope">{{ formatDate(scope.row.updatedAt) }}</template>
          </el-table-column>
          <!-- Keep actions unfixed: sticky body cells can desynchronize from headers and make buttons unclickable. -->
          <el-table-column v-if="auth.isAdmin" :label="t('agentDetail.actions')" width="140">
            <template #default="scope">
              <template v-if="!scope.row.deletedAt">
                <el-button link type="primary" :data-test="`edit-agent-policy-${scope.row.id}`" @click="openEditPolicy(scope.row)">{{ t('agentDetail.policyEdit') }}</el-button>
                <el-button link type="danger" :data-test="`delete-agent-policy-${scope.row.id}`" @click="openDeletePolicy(scope.row)">{{ t('agentDetail.policyDelete') }}</el-button>
              </template>
              <el-button v-else link type="primary" :data-test="`restore-agent-policy-${scope.row.id}`" :loading="policyRestoringId === scope.row.id" @click="restorePolicy(scope.row)">{{ t('agentDetail.policyRestore') }}</el-button>
            </template>
          </el-table-column>
        </el-table>
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

    <el-dialog v-model="policyDialogVisible" :title="editingPolicy ? t('agentDetail.policyEditTitle') : t('agentDetail.policyCreateTitle')" width="min(560px,94vw)" @closed="resetPolicyForm">
      <el-form label-position="top">
        <el-form-item :label="t('agentDetail.policyProtocol')">
          <el-select v-model="policyForm.protocol">
            <el-option label="TCP" value="tcp" />
            <el-option label="UDP" value="udp" />
            <el-option label="HTTP" value="http" />
            <el-option label="WebSocket" value="ws" />
          </el-select>
        </el-form-item>
        <el-form-item :label="t('agentDetail.policyTargetHost')">
          <el-input v-model="policyForm.targetHost" data-test="policy-target-host" :placeholder="t('agentDetail.policyTargetHostPlaceholder')" />
          <div class="field-help">{{ t('agentDetail.policyTargetHostHelp') }}</div>
        </el-form-item>
        <el-form-item :label="t('agentDetail.policyTargetPort')">
          <el-input-number v-model="policyForm.targetPort" :min="0" :max="65535" :step="1" data-test="policy-target-port" />
          <div class="field-help">{{ t('agentDetail.policyTargetPortHelp') }}</div>
        </el-form-item>
        <el-form-item :label="t('agentDetail.policyAllowedCIDRs')">
          <el-input v-model="policyForm.cidrsText" :placeholder="t('agentDetail.policyCIDRsPlaceholder')" />
          <div class="field-help">{{ t('agentDetail.policyCIDRsHelp') }}</div>
        </el-form-item>
        <el-form-item :label="t('agentDetail.policyAllowedPorts')">
          <el-input v-model="policyForm.portsText" :placeholder="t('agentDetail.policyPortsPlaceholder')" />
          <div class="field-help">{{ t('agentDetail.policyPortsHelp') }}</div>
        </el-form-item>
      </el-form>
      <div v-if="policyFormError" data-test="policy-form-error" class="close-error">{{ policyFormError }}</div>
      <template #footer>
        <el-button @click="policyDialogVisible = false">{{ t('agentDetail.cancel') }}</el-button>
        <el-button type="primary" data-test="save-agent-policy" :loading="policySaving" @click="savePolicy">{{ t('common.save') }}</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="policyDeleteVisible" :title="t('agentDetail.policyDeleteTitle')" width="min(520px,94vw)">
      <el-descriptions v-if="policyDeleteTarget" :column="1" border>
        <el-descriptions-item :label="t('agentDetail.policyTargetHost')">{{ formatPolicyTargetHost(policyDeleteTarget.targetHost) }}</el-descriptions-item>
        <el-descriptions-item :label="t('agentDetail.policyTargetPort')">{{ formatPolicyTargetPort(policyDeleteTarget.targetPort) }}</el-descriptions-item>
        <el-descriptions-item :label="t('agentDetail.policyProtocol')">{{ policyDeleteTarget.protocol.toUpperCase() }}</el-descriptions-item>
      </el-descriptions>
      <p class="policy-delete-warning">{{ t('agentDetail.policyDeleteConfirm') }}</p>
      <div v-if="policyDeleteError" data-test="policy-delete-error" class="close-error">{{ policyDeleteError }}</div>
      <template #footer>
        <el-button @click="policyDeleteVisible = false">{{ t('agentDetail.cancel') }}</el-button>
        <el-button type="danger" data-test="confirm-delete-agent-policy" :loading="policyDeleting" @click="confirmDeletePolicy">{{ t('agentDetail.policyDelete') }}</el-button>
      </template>
    </el-dialog>
  </section>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { ElMessage } from 'element-plus'
import PageHeader from '../components/PageHeader.vue'
import DataState from '../components/DataState.vue'
import StatusTag from '../components/StatusTag.vue'
import { useFormatDateTime } from '../i18n/format'
import { APIError, closeAgentConnection, createAgentPolicy, deleteAgentPolicy, getAgentMetadata, listAgentConnections, listAgentPolicies, restoreAgentPolicy, updateAgentPolicy, type AgentMetadata, type AgentPolicy, type ClusterAgentConnection } from '../api/client'
import { useAuthStore } from '../stores/auth'

const route = useRoute()
const { t } = useI18n()
const auth = useAuthStore()
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
const policies = ref<AgentPolicy[]>([])
const policiesLoading = ref(false)
const policiesError = ref(false)
const policyDialogVisible = ref(false)
const policySaving = ref(false)
const editingPolicy = ref<AgentPolicy | null>(null)
const policyFormError = ref('')
const policyForm = reactive({ protocol: 'tcp', targetHost: '*', targetPort: 0, cidrsText: '', portsText: '' })
const policyIdempotencyKey = ref('')
const policyStatus = ref<'active' | 'deleted' | 'all'>('active')
const policyStatusOptions = [
  { value: 'active', labelKey: 'agentDetail.policyStatusActive' },
  { value: 'deleted', labelKey: 'agentDetail.policyStatusDeleted' },
  { value: 'all', labelKey: 'agentDetail.policyStatusAll' },
] as const
const policyDeleteVisible = ref(false)
const policyDeleting = ref(false)
const policyDeleteError = ref('')
const policyDeleteTarget = ref<AgentPolicy | null>(null)
const policyRestoringId = ref('')
const formatDate = useFormatDateTime()

const healthyConnections = computed(() => clusterConnections.value.filter(connection => connection.healthy))
const activeInstanceIds = computed(() => new Set(healthyConnections.value.map(connection => connection.instanceId)))
const logicalOnline = computed(() => healthyConnections.value.length > 0)
const activeInstanceCount = computed(() => activeInstanceIds.value.size)
const activeConnectionCount = computed(() => healthyConnections.value.length)
const connectionsByInstance = computed(() => {
  const counts = new Map<string, number>()
  for (const connection of healthyConnections.value) {
    counts.set(connection.instanceId, (counts.get(connection.instanceId) || 0) + 1)
  }
  return counts
})
const latestReportedAt = computed(() => latestTimestamp((metadata.value?.instances || []).map(instance => instance.reportedAt), metadata.value?.reportedAt))
const latestUpdatedAt = computed(() => latestTimestamp((metadata.value?.instances || []).map(instance => instance.updatedAt), metadata.value?.updatedAt))

function latestTimestamp(values: string[], fallback?: string) {
  const parsed = values.filter(Boolean)
  if (!parsed.length) return fallback || ''
  return parsed.reduce((latest, value) => (Date.parse(latest) >= Date.parse(value) ? latest : value))
}

function instanceOnline(instanceId: string) {
  return activeInstanceIds.value.has(instanceId)
}

function instanceConnectionCount(instanceId: string) {
  return connectionsByInstance.value.get(instanceId) || 0
}

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

async function loadPolicies() {
  policiesLoading.value = true; policiesError.value = false
  try {
    policies.value = policyStatus.value === 'active'
      ? (await listAgentPolicies(String(route.params.id))).items
      : (await listAgentPolicies(String(route.params.id), { status: policyStatus.value })).items
  }
  catch { policiesError.value = true }
  finally { policiesLoading.value = false }
}

async function setPolicyStatus(status: 'active' | 'deleted' | 'all') {
  if (policyStatus.value === status) return
  policyStatus.value = status
  await loadPolicies()
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

function formatPolicyTargetHost(host: string) {
  return host === '*' ? t('agentDetail.policyAnyHost') : host
}

function formatPolicyTargetPort(port: number) {
  return port === 0 ? t('agentDetail.policyAnyPort') : String(port)
}

function formatPolicyList(values: Array<string | number>) {
  return values.length ? values.join(', ') : t('agentDetail.policyUnrestricted')
}

function splitPolicyValues(value: string) {
  return value.split(',').map(item => item.trim()).filter(Boolean)
}

function parsePolicyPorts(value: string) {
  return splitPolicyValues(value).map(item => /^\d+$/.test(item) ? Number(item) : Number.NaN)
}

function resetPolicyForm() {
  policyForm.protocol = 'tcp'
  policyForm.targetHost = '*'
  policyForm.targetPort = 0
  policyForm.cidrsText = ''
  policyForm.portsText = ''
  policyFormError.value = ''
  policyIdempotencyKey.value = ''
}

function openCreatePolicy() {
  editingPolicy.value = null
  resetPolicyForm()
  policyDialogVisible.value = true
}

function openEditPolicy(policy: AgentPolicy) {
  editingPolicy.value = policy
  policyForm.protocol = policy.protocol
  policyForm.targetHost = policy.targetHost
  policyForm.targetPort = policy.targetPort
  policyForm.cidrsText = policy.allowedCIDRs.join(', ')
  policyForm.portsText = policy.allowedPorts.join(', ')
  policyFormError.value = ''
  policyDialogVisible.value = true
}

function openDeletePolicy(policy: AgentPolicy) {
  policyDeleteTarget.value = policy
  policyDeleteError.value = ''
  policyDeleteVisible.value = true
}

async function confirmDeletePolicy() {
  if (!policyDeleteTarget.value || policyDeleting.value) return
  policyDeleting.value = true; policyDeleteError.value = ''
  try {
    await deleteAgentPolicy(String(route.params.id), policyDeleteTarget.value.id)
    ElMessage.success(t('agentDetail.policyDeleted'))
    policyDeleteVisible.value = false
    await loadPolicies()
  } catch {
    policyDeleteError.value = t('agentDetail.policyDeleteFailed')
  } finally {
    policyDeleting.value = false
  }
}

async function restorePolicy(policy: AgentPolicy) {
  if (policyRestoringId.value) return
  policyRestoringId.value = policy.id
  try {
    await restoreAgentPolicy(String(route.params.id), policy.id)
    ElMessage.success(t('agentDetail.policyRestored'))
    await loadPolicies()
  } catch {
    ElMessage.error(t('agentDetail.policyRestoreFailed'))
  } finally {
    policyRestoringId.value = ''
  }
}

function validatePolicyForm() {
  const targetHost = policyForm.targetHost.trim()
  if (!targetHost || (targetHost !== '*' && targetHost.includes('*'))) return t('agentDetail.policyInvalidTargetHost')
  if (!Number.isInteger(policyForm.targetPort) || policyForm.targetPort < 0 || policyForm.targetPort > 65535) return t('agentDetail.policyInvalidTargetPort')
  const cidrs = splitPolicyValues(policyForm.cidrsText)
  if (cidrs.some(cidr => !cidr.includes('/'))) return t('agentDetail.policyInvalidCIDR')
  const ports = parsePolicyPorts(policyForm.portsText)
  if (ports.some(port => !Number.isInteger(port) || port < 1 || port > 65535)) return t('agentDetail.policyInvalidPortList')
  return ''
}

async function savePolicy() {
  if (policySaving.value) return
  policyFormError.value = validatePolicyForm()
  if (policyFormError.value) return

  const input = {
    protocol: policyForm.protocol,
    targetHost: policyForm.targetHost.trim(),
    targetPort: policyForm.targetPort,
    allowedCIDRs: splitPolicyValues(policyForm.cidrsText),
    allowedPorts: parsePolicyPorts(policyForm.portsText),
  }
  policySaving.value = true
  try {
    if (editingPolicy.value) {
      await updateAgentPolicy(String(route.params.id), editingPolicy.value.id, input)
      ElMessage.success(t('agentDetail.policyUpdated'))
    } else {
      if (!policyIdempotencyKey.value) policyIdempotencyKey.value = crypto.randomUUID()
      await createAgentPolicy(String(route.params.id), input, policyIdempotencyKey.value)
      ElMessage.success(t('agentDetail.policyCreated'))
    }
    policyDialogVisible.value = false
    await loadPolicies()
  } catch {
    ElMessage.error(editingPolicy.value ? t('agentDetail.policyUpdateFailed') : t('agentDetail.policyCreateFailed'))
  } finally {
    policySaving.value = false
  }
}

onMounted(() => {
  void load()
  void loadPolicies()
})
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
.policy-header { display: flex; align-items: flex-start; justify-content: space-between; gap: 16px; margin-bottom: 14px; }
.policy-title { margin-top: 0; font-size: 16px; }
.policy-description { margin-top: 4px; max-width: 720px; color: #6b7280; font-size: 13px; line-height: 1.5; }
.policy-actions { display: flex; align-items: center; gap: 12px; }
.policy-status-filter { display: flex; align-items: center; gap: 4px; }
.policy-delete-warning { margin: 14px 0 0; color: #4b5563; font-size: 13px; line-height: 1.5; }
.field-help { margin-top: 5px; color: #6b7280; font-size: 12px; line-height: 1.5; }
</style>
