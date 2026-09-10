<template>
  <section class="tokens-page">
    <PageHeader :title="t('tokens.title')" :description="t('tokens.description')"><el-button type="primary" @click="openCreate">{{t('tokens.create')}}</el-button></PageHeader>

    <div class="tm-card table-card">
      <div class="toolbar">
        <el-select v-model="filterType" clearable :placeholder="t('tokens.allTypes')" @change="reload">
          <el-option :label="t('tokens.agent')" value="agent" />
          <el-option :label="t('tokens.client')" value="client" />
          <el-option v-if="auth.isAdmin" :label="t('tokens.serverNode')" value="server_node" />
        </el-select>
        <el-button :loading="loading" @click="reload">{{t('tokens.refresh')}}</el-button>
      </div>
      <DataState :loading="loading" :error="loadError" :empty="!items.length" :error-label="t('common.loadFailed')" :retry-label="t('common.retry')" :empty-label="t('tokens.empty')" @retry="reload">
        <el-table :data="items" row-key="id">
          <el-table-column prop="prefix" :label="t('tokens.prefix')" width="130" />
          <el-table-column prop="type" :label="t('tokens.type')" width="130" />
          <el-table-column prop="ownerUserId" :label="t('tokens.owner')" min-width="150" />
          <el-table-column :label="t('tokens.binding')" min-width="160"><template #default="{ row }">{{ row.agentId || row.nodeId || t('tokens.scoped') }}</template></el-table-column>
          <el-table-column :label="t('tokens.state')" width="120"><template #default="{ row }"><StatusTag :kind="row.status === 'revoked' ? 'info' : row.status === 'active' ? 'success' : 'warning'" :label="row.status" /></template></el-table-column>
          <el-table-column :label="t('tokens.expires')" width="180"><template #default="{ row }">{{ formatDate(row.expiresAt) }}</template></el-table-column>
          <el-table-column :label="t('tokens.lastUsed')" width="180"><template #default="{ row }">{{ formatDate(row.lastUsedAt) }}</template></el-table-column>
          <el-table-column :label="t('tokens.actions')" width="340" fixed="right">
            <template #default="{ row }">
              <el-button link type="primary" @click="showDetail(row)">{{t('tokens.detail')}}</el-button>
              <el-button link type="primary" :disabled="row.status !== 'active'" @click="openExpiration(row)">{{t('tokens.expiration')}}</el-button>
              <el-button link type="primary" :disabled="row.status !== 'active'" @click="openScope(row)">{{t('tokens.scopeUpdate')}}</el-button>
              <el-button v-if="auth.isAdmin" link type="warning" :disabled="row.status === 'revoked'" @click="reveal(row)">{{t('tokens.reveal')}}</el-button>
              <el-button link type="primary" :disabled="row.status === 'revoked'" @click="rotate(row)">{{t('tokens.rotate')}}</el-button>
              <el-button link type="danger" :disabled="row.status === 'revoked'" @click="revoke(row)">{{t('tokens.revoke')}}</el-button>
            </template>
          </el-table-column>
        </el-table>
        <div v-if="nextCursor" class="pager"><el-button :loading="loading" @click="loadMore">{{t('tokens.loadMore')}}</el-button></div>
      </DataState>
    </div>

    <el-dialog v-model="createVisible" :title="t('tokens.create')" width="560px" @closed="resetForm">
      <el-form label-position="top">
        <el-form-item :label="t('tokens.type')"><el-select v-model="form.type" :disabled="!auth.isAdmin && form.type === 'server_node'"><el-option :label="t('tokens.agent')" value="agent" /><el-option :label="t('tokens.client')" value="client" /><el-option v-if="auth.isAdmin" :label="t('tokens.serverNode')" value="server_node" /></el-select></el-form-item>
        <el-form-item v-if="form.type === 'agent'" :label="t('tokens.agentId')">
          <el-select v-model="form.agentId" filterable clearable :loading="agentsLoading" :filter-method="setAgentFilter" :placeholder="t('tokens.searchAgents')">
            <el-option v-for="agent in visibleAgents" :key="agent.id" :label="`${agent.name} · ${agent.id}`" :value="agent.id" />
          </el-select>
        </el-form-item>
        <el-form-item v-if="form.type === 'server_node'" :label="t('tokens.nodeId')"><el-input v-model="form.nodeId" :placeholder="t('tokens.nodeId')" /></el-form-item>
        <el-form-item v-if="form.type === 'client'" :label="t('tokens.agentIds')"><el-input v-model="agentIdsText" type="textarea" :rows="3" placeholder="agent-a&#10;agent-b" /></el-form-item>
        <el-form-item :label="t('tokens.protocols')"><el-checkbox-group v-model="form.scope.protocols"><el-checkbox label="tcp" /><el-checkbox label="udp" /><el-checkbox label="http" /><el-checkbox label="websocket" /></el-checkbox-group></el-form-item>
        <el-form-item :label="t('tokens.cidrs')"><el-input v-model="cidrsText" placeholder="10.0.0.0/24, 192.168.1.0/24" /></el-form-item>
        <el-form-item :label="t('tokens.ports')">
          <el-input v-model="portsText" placeholder="22, 80, 443" />
          <div class="field-help">{{ t('tokens.allPorts') }}</div>
        </el-form-item>
        <el-form-item :label="t('tokens.expires')"><el-date-picker v-model="form.expiresAt" type="datetime" value-format="YYYY-MM-DDTHH:mm:ssZ" /></el-form-item>
      </el-form>
      <template #footer><el-button @click="createVisible = false">{{t('tokens.cancel')}}</el-button><el-button type="primary" :loading="saving" @click="create">{{t('tokens.create')}}</el-button></template>
    </el-dialog>

    <el-dialog v-model="detailVisible" :title="t('tokens.detailTitle')" width="min(760px,94vw)">
      <DataState :loading="detailLoading" :error="detailError" :empty="!detailToken" :error-label="t('common.loadFailed')" :retry-label="t('common.retry')" :empty-label="t('common.empty')" @retry="retryDetail">
        <el-descriptions v-if="detailToken" :column="2" border>
          <el-descriptions-item :label="t('tokens.id')" :span="2">{{ detailToken.id }}</el-descriptions-item>
          <el-descriptions-item :label="t('tokens.type')">{{ detailToken.type }}</el-descriptions-item>
          <el-descriptions-item :label="t('tokens.state')"><StatusTag :kind="detailToken.status === 'revoked' ? 'info' : detailToken.status === 'active' ? 'success' : 'warning'" :label="detailToken.status" /></el-descriptions-item>
          <el-descriptions-item :label="t('tokens.owner')">{{ detailToken.ownerUserId }}</el-descriptions-item>
          <el-descriptions-item :label="t('tokens.prefix')">{{ detailToken.prefix }}</el-descriptions-item>
          <el-descriptions-item :label="t('tokens.agentId')">{{ detailToken.agentId || '—' }}</el-descriptions-item>
          <el-descriptions-item :label="t('tokens.nodeId')">{{ detailToken.nodeId || '—' }}</el-descriptions-item>
          <el-descriptions-item :label="t('tokens.expires')">{{ formatDate(detailToken.expiresAt) }}</el-descriptions-item>
          <el-descriptions-item :label="t('tokens.revokedAt')">{{ formatDate(detailToken.revokedAt) }}</el-descriptions-item>
          <el-descriptions-item :label="t('tokens.lastUsed')">{{ formatDate(detailToken.lastUsedAt) }}</el-descriptions-item>
          <el-descriptions-item :label="t('tokens.createdAt')">{{ formatDate(detailToken.createdAt) }}</el-descriptions-item>
          <el-descriptions-item :label="t('tokens.updatedAt')">{{ formatDate(detailToken.updatedAt) }}</el-descriptions-item>
          <el-descriptions-item :label="t('tokens.replayed')">{{ formatBoolean(detailToken.replayed) }}</el-descriptions-item>
          <el-descriptions-item :label="t('tokens.scope')" :span="2">
            <div class="scope-list">
              <div><span>{{ t('tokens.agentIds') }}:</span>{{ formatList(detailToken.scope.agentIds) }}</div>
              <div><span>{{ t('tokens.protocols') }}:</span>{{ formatList(detailToken.scope.protocols) }}</div>
              <div><span>{{ t('tokens.cidrs') }}:</span>{{ formatList(detailToken.scope.targetCIDRs) }}</div>
              <div><span>{{ t('tokens.ports') }}:</span>{{ formatList(detailToken.scope.targetPorts) }}</div>
            </div>
          </el-descriptions-item>
        </el-descriptions>
      </DataState>
    </el-dialog>

    <el-dialog v-model="expirationVisible" :title="t('tokens.expirationTitle')" width="min(440px,92vw)">
      <el-form label-position="top">
        <el-form-item :label="t('tokens.expires')">
          <el-date-picker v-model="expirationForm" type="datetime" value-format="YYYY-MM-DDTHH:mm:ssZ" clearable />
          <div class="field-help">{{ t('tokens.noExpiration') }}</div>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="expirationVisible = false">{{t('tokens.cancel')}}</el-button>
        <el-button type="primary" :loading="expirationSaving" @click="updateExpiration">{{t('common.save')}}</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="scopeVisible" :title="t('tokens.scopeUpdateTitle')" width="min(520px,94vw)">
      <el-form label-position="top">
        <el-form-item :label="t('tokens.protocols')">
          <el-checkbox-group v-model="scopeForm.protocols">
            <el-checkbox label="tcp" />
            <el-checkbox label="udp" />
            <el-checkbox label="http" />
            <el-checkbox label="websocket" />
          </el-checkbox-group>
          <div class="field-help">{{ t('tokens.allProtocols') }}</div>
        </el-form-item>
        <el-form-item :label="t('tokens.cidrs')">
          <el-input v-model="scopeForm.cidrsText" placeholder="10.0.0.0/24, 192.168.1.0/24" />
          <div class="field-help">{{ t('tokens.allCIDRs') }}</div>
        </el-form-item>
        <el-form-item :label="t('tokens.ports')">
          <el-input v-model="scopeForm.portsText" placeholder="22, 80, 443" />
          <div class="field-help">{{ t('tokens.allPorts') }}</div>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="scopeVisible = false">{{t('tokens.cancel')}}</el-button>
        <el-button type="primary" :loading="scopeSaving" @click="updateScope">{{t('common.save')}}</el-button>
      </template>
    </el-dialog>
    <TokenSecretDialog :secret="secret" @close="clearSecret" />
  </section>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { createToken, getAgents, getTokenDetail, listTokens, revokeToken, rotateToken, revealToken, updateTokenExpiration, updateTokenScope, type Agent, type ServiceToken, type TokenType } from '../api/client'
import { useAuthStore } from '../stores/auth'
import { useI18n } from 'vue-i18n'
import TokenSecretDialog from '../components/TokenSecretDialog.vue'
import PageHeader from '../components/PageHeader.vue'
import DataState from '../components/DataState.vue'
import StatusTag from '../components/StatusTag.vue'
import { useFormatDateTime } from '../i18n/format'
import { defaultTokenExpiration, filterAgents, loadAgentsForSelection, tokenPayloadFromForm, tokenScopePatchFromForm } from './token-form'

const auth = useAuthStore(); const {t}=useI18n(); const items = ref<ServiceToken[]>([]); const nextCursor = ref(''); const loading = ref(false); const loadError = ref(false); const saving = ref(false); const filterType = ref<TokenType | ''>(''); const createVisible = ref(false); const secret = ref('')
const detailVisible = ref(false); const detailLoading = ref(false); const detailError = ref(false); const detailToken = ref<ServiceToken | null>(null)
const expirationVisible = ref(false); const expirationSaving = ref(false); const expirationForm = ref(''); const expirationToken = ref<ServiceToken | null>(null)
const scopeVisible = ref(false); const scopeSaving = ref(false); const scopeToken = ref<ServiceToken | null>(null)
const scopeForm = reactive<{ protocols: string[]; cidrsText: string; portsText: string }>({ protocols: [], cidrsText: '', portsText: '' })
const agents = ref<Agent[]>([]); const agentsLoading = ref(false); const agentFilter = ref(''); const visibleAgents = computed(() => filterAgents(agents.value, agentFilter.value)); const selectedAgent = computed(() => agents.value.find(agent => agent.id === form.agentId))
const form = reactive<{ type: TokenType; agentId: string; nodeId: string; expiresAt: string; scope: { agentIds: string[]; protocols: string[]; targetCIDRs: string[]; targetPorts: number[] } }>({ type: 'client', agentId: '', nodeId: '', expiresAt: defaultTokenExpiration(), scope: { agentIds: [], protocols: ['tcp'], targetCIDRs: [], targetPorts: [] } })
const agentIdsText = ref(''); const cidrsText = ref(''); const portsText = ref('')
const formatDate = useFormatDateTime()
function clearSecret() { secret.value = '' }
function openCreate() { resetForm(); createVisible.value = true }
function resetForm() { form.type = 'client'; form.agentId = ''; form.nodeId = ''; form.expiresAt = defaultTokenExpiration(); form.scope = { agentIds: [], protocols: ['tcp'], targetCIDRs: [], targetPorts: [] }; agentIdsText.value = ''; cidrsText.value = ''; portsText.value = ''; agentFilter.value = '' }
function setAgentFilter(query: string) { agentFilter.value = query }
function formatList(values?: Array<string | number>) { return values?.length ? values.join(', ') : '—' }
function formatBoolean(value?: boolean) { return value ? t('common.yes') : t('common.no') }
async function showDetail(row: ServiceToken) {
  detailVisible.value = true; detailLoading.value = true; detailError.value = false
  if (!detailToken.value || detailToken.value.id !== row.id) detailToken.value = row
  try { detailToken.value = await getTokenDetail(row.id) }
  catch { detailError.value = true }
  finally { detailLoading.value = false }
}
async function retryDetail() { if (detailToken.value) await showDetail(detailToken.value) }
function openExpiration(row: ServiceToken) { expirationToken.value = row; expirationForm.value = row.expiresAt || ''; expirationVisible.value = true }
function openScope(row: ServiceToken) {
  scopeToken.value = row
  scopeForm.protocols = [...(row.scope.protocols || [])]
  scopeForm.cidrsText = (row.scope.targetCIDRs || []).join(', ')
  scopeForm.portsText = (row.scope.targetPorts || []).join(', ')
  scopeVisible.value = true
}
async function updateExpiration() {
  if (!expirationToken.value) return
  expirationSaving.value = true
  try {
    const updated = await updateTokenExpiration(expirationToken.value.id, expirationForm.value || null)
    expirationVisible.value = false
    const index = items.value.findIndex(item => item.id === updated.id)
    if (index >= 0) items.value[index] = updated
    ElMessage.success(t('tokens.expirationUpdated'))
  } catch { ElMessage.error(t('tokens.updateFailed')) }
  finally { expirationSaving.value = false }
}
async function updateScope() {
  if (!scopeToken.value) return
  scopeSaving.value = true
  try {
    const updated = await updateTokenScope(scopeToken.value.id, tokenScopePatchFromForm({
      protocols: scopeForm.protocols,
      cidrsText: scopeForm.cidrsText,
      portsText: scopeForm.portsText,
    }))
    scopeVisible.value = false
    const index = items.value.findIndex(item => item.id === updated.id)
    if (index >= 0) items.value[index] = updated
    if (detailToken.value?.id === updated.id) detailToken.value = updated
    ElMessage.success(t('tokens.scopeUpdated'))
  } catch { ElMessage.error(t('tokens.updateFailed')) }
  finally { scopeSaving.value = false }
}
async function loadAgents() { agentsLoading.value = true; try { agents.value = await loadAgentsForSelection(getAgents) } finally { agentsLoading.value = false } }
async function reload() { loading.value = true; loadError.value = false; try { const page = await listTokens({ type: filterType.value || undefined }); items.value = page.items; nextCursor.value = page.nextCursor || '' } catch { loadError.value = true } finally { loading.value = false } }
async function loadMore() { if (!nextCursor.value) return; loading.value = true; loadError.value = false; try { const page = await listTokens({ type: filterType.value || undefined, cursor: nextCursor.value }); items.value.push(...page.items); nextCursor.value = page.nextCursor || '' } catch { loadError.value = true } finally { loading.value = false } }
async function create() {
  if (form.type === 'agent' && !form.agentId) { ElMessage.warning(t('tokens.agentRequired')); return }
  saving.value = true
  try {
    const payload = tokenPayloadFromForm({ type: form.type, agentId: form.agentId, nodeId: form.nodeId, expiresAt: form.expiresAt, agentIdsText: agentIdsText.value, cidrsText: cidrsText.value, portsText: portsText.value, selectedAgent: selectedAgent.value, scope: { protocols: form.scope.protocols } })
    const created = await createToken(payload, crypto.randomUUID()); createVisible.value = false; secret.value = created.secret || ''; await reload(); ElMessage.success(t('tokens.created'))
  } catch { ElMessage.error(t('tokens.createFailed')) }
  finally { saving.value = false }
}
async function rotate(row: ServiceToken) { try { await ElMessageBox.confirm(t('tokens.rotateConfirm'), t('tokens.rotateTitle'), { type: 'warning' }); const rotated = await rotateToken(row.id, crypto.randomUUID()); secret.value = rotated.secret || ''; await reload() } catch (error) { if (error === 'cancel' || error === 'close') return; ElMessage.error(t('tokens.rotateFailed')) } }
async function reveal(row: ServiceToken) { try { const confirmation = await ElMessageBox.prompt(t('tokens.revealConfirm'), t('tokens.revealTitle'), { inputPattern: /^REVEAL$/, inputErrorMessage: t('tokens.revealInput'), type: 'warning' }); const revealed = await revealToken(row.id, confirmation.value, crypto.randomUUID()); secret.value = revealed.secret; } catch (error) { if (error === 'cancel' || error === 'close') return; ElMessage.error(t('tokens.revealFailed')) } }
async function revoke(row: ServiceToken) { try { await ElMessageBox.confirm(t('tokens.revokeConfirm'), t('tokens.revokeTitle'), { type: 'warning' }); await revokeToken(row.id); ElMessage.success(t('tokens.revoked')); await reload() } catch (error) { if (error === 'cancel' || error === 'close') return; ElMessage.error(t('tokens.revokeFailed')) } }
onMounted(() => { void reload(); void loadAgents() }); onBeforeUnmount(clearSecret)
</script>

<style scoped>
.tokens-page { display: grid; gap: 16px; }
.table-card { padding: 16px; }
.toolbar { display: flex; gap: 12px; margin-bottom: 18px; }
.pager { display: flex; justify-content: center; padding: 18px 0 4px; }
.field-help { margin-top: 4px; color: var(--tm-muted); font-size: 12px; }
.scope-list { display: grid; gap: 6px; }
.scope-list span { display: inline-block; min-width: 120px; color: var(--tm-muted); }
</style>
