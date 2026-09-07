<template>
  <section class="tokens-page">
    <div class="hero">
      <div>
        <p class="eyebrow">CREDENTIAL CONTROL</p>
        <h1>Service tokens</h1>
        <p class="lede">Issue narrow credentials for Agents, Clients, and server-node relay. Secrets appear once.</p>
      </div>
      <el-button type="primary" @click="openCreate">Create token</el-button>
    </div>

    <el-card shadow="never" class="table-card">
      <div class="toolbar">
        <el-select v-model="filterType" clearable placeholder="All types" @change="reload">
          <el-option label="Agent" value="agent" />
          <el-option label="Client" value="client" />
          <el-option v-if="auth.isAdmin" label="Server node" value="server_node" />
        </el-select>
        <el-button :loading="loading" @click="reload">Refresh</el-button>
      </div>
      <el-table :data="items" v-loading="loading" row-key="id">
        <el-table-column prop="prefix" label="Prefix" width="130" />
        <el-table-column prop="type" label="Type" width="130" />
        <el-table-column prop="ownerUserId" label="Owner" min-width="150" />
        <el-table-column label="Binding" min-width="160"><template #default="{ row }">{{ row.agentId || row.nodeId || 'Scoped' }}</template></el-table-column>
        <el-table-column prop="status" label="State" width="120" />
        <el-table-column label="Expires" width="180"><template #default="{ row }">{{ formatDate(row.expiresAt) }}</template></el-table-column>
        <el-table-column label="Last used" width="180"><template #default="{ row }">{{ formatDate(row.lastUsedAt) }}</template></el-table-column>
        <el-table-column label="Actions" width="220" fixed="right">
          <template #default="{ row }">
            <el-button link type="primary" :disabled="row.status === 'revoked'" @click="rotate(row)">Rotate</el-button>
            <el-button link type="danger" :disabled="row.status === 'revoked'" @click="revoke(row)">Revoke</el-button>
          </template>
        </el-table-column>
      </el-table>
      <div v-if="nextCursor" class="pager"><el-button :loading="loading" @click="loadMore">Load more</el-button></div>
      <el-empty v-if="!loading && items.length === 0" description="No service tokens yet" />
    </el-card>

    <el-dialog v-model="createVisible" title="Create service token" width="560px" @closed="resetForm">
      <el-form label-position="top">
        <el-form-item label="Type"><el-select v-model="form.type" :disabled="!auth.isAdmin && form.type === 'server_node'"><el-option label="Agent" value="agent" /><el-option label="Client" value="client" /><el-option v-if="auth.isAdmin" label="Server node" value="server_node" /></el-select></el-form-item>
        <el-form-item v-if="form.type === 'agent'" label="Agent ID"><el-input v-model="form.agentId" placeholder="agent id" /></el-form-item>
        <el-form-item v-if="form.type === 'server_node'" label="Node ID"><el-input v-model="form.nodeId" placeholder="server node id" /></el-form-item>
        <el-form-item v-if="form.type === 'client'" label="Agent IDs (one per line)"><el-input v-model="agentIdsText" type="textarea" :rows="3" placeholder="agent-a&#10;agent-b" /></el-form-item>
        <el-form-item label="Protocols"><el-checkbox-group v-model="form.scope.protocols"><el-checkbox label="tcp" /><el-checkbox label="udp" /><el-checkbox label="http" /><el-checkbox label="websocket" /></el-checkbox-group></el-form-item>
        <el-form-item label="Target CIDRs"><el-input v-model="cidrsText" placeholder="10.0.0.0/24, 192.168.1.0/24" /></el-form-item>
        <el-form-item label="Target ports"><el-input v-model="portsText" placeholder="22, 80, 443" /></el-form-item>
        <el-form-item label="Expires at (optional)"><el-date-picker v-model="form.expiresAt" type="datetime" value-format="YYYY-MM-DDTHH:mm:ssZ" /></el-form-item>
      </el-form>
      <template #footer><el-button @click="createVisible = false">Cancel</el-button><el-button type="primary" :loading="saving" @click="create">Create token</el-button></template>
    </el-dialog>
    <TokenSecretDialog :secret="secret" @close="clearSecret" />
  </section>
</template>

<script setup lang="ts">
import { onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { createToken, listTokens, revokeToken, rotateToken, type ServiceToken, type TokenType } from '../api/client'
import { useAuthStore } from '../stores/auth'
import TokenSecretDialog from '../components/TokenSecretDialog.vue'

const auth = useAuthStore(); const items = ref<ServiceToken[]>([]); const nextCursor = ref(''); const loading = ref(false); const saving = ref(false); const filterType = ref<TokenType | ''>(''); const createVisible = ref(false); const secret = ref('')
const form = reactive<{ type: TokenType; agentId: string; nodeId: string; expiresAt: string; scope: { agentIds: string[]; protocols: string[]; targetCIDRs: string[]; targetPorts: number[] } }>({ type: 'client', agentId: '', nodeId: '', expiresAt: '', scope: { agentIds: [], protocols: ['tcp'], targetCIDRs: [], targetPorts: [] } })
const agentIdsText = ref(''); const cidrsText = ref(''); const portsText = ref('')
function formatDate(value?: string) { return value ? new Date(value).toLocaleString() : '—' }
function clearSecret() { secret.value = '' }
function openCreate() { createVisible.value = true }
function resetForm() { form.type = 'client'; form.agentId = ''; form.nodeId = ''; form.expiresAt = ''; form.scope = { agentIds: [], protocols: ['tcp'], targetCIDRs: [], targetPorts: [] }; agentIdsText.value = ''; cidrsText.value = ''; portsText.value = '' }
async function reload() { loading.value = true; try { const page = await listTokens({ type: filterType.value || undefined }); items.value = page.items; nextCursor.value = page.nextCursor || '' } finally { loading.value = false } }
async function loadMore() { if (!nextCursor.value) return; loading.value = true; try { const page = await listTokens({ type: filterType.value || undefined, cursor: nextCursor.value }); items.value.push(...page.items); nextCursor.value = page.nextCursor || '' } finally { loading.value = false } }
async function create() { saving.value = true; try { form.scope.agentIds = agentIdsText.value.split(/\r?\n/).map(v => v.trim()).filter(Boolean); form.scope.targetCIDRs = cidrsText.value.split(',').map(v => v.trim()).filter(Boolean); form.scope.targetPorts = portsText.value.split(',').map(v => Number(v.trim())).filter(v => Number.isInteger(v) && v > 0); const created = await createToken({ type: form.type, agentId: form.agentId || undefined, nodeId: form.nodeId || undefined, scope: form.scope, expiresAt: form.expiresAt || undefined }, crypto.randomUUID()); createVisible.value = false; secret.value = created.secret || ''; await reload(); ElMessage.success('Token created') } catch (error) { ElMessage.error(error instanceof Error ? error.message : 'Token creation failed') } finally { saving.value = false } }
async function rotate(row: ServiceToken) { await ElMessageBox.confirm('Rotating revokes the current token immediately. Continue?', 'Rotate token', { type: 'warning' }); const rotated = await rotateToken(row.id, crypto.randomUUID()); secret.value = rotated.secret || ''; await reload() }
async function revoke(row: ServiceToken) { await ElMessageBox.confirm('Revoked tokens cannot be used again. Continue?', 'Revoke token', { type: 'warning' }); await revokeToken(row.id); ElMessage.success('Token revoked'); await reload() }
onMounted(reload); onBeforeUnmount(clearSecret)
</script>

<style scoped>
.tokens-page { max-width: 1400px; margin: 0 auto; }
.hero { display: flex; justify-content: space-between; align-items: end; gap: 24px; margin-bottom: 24px; }
.eyebrow { color: #6b7a90; letter-spacing: .16em; font-size: 11px; font-weight: 700; margin: 0 0 8px; }
h1 { margin: 0; color: #14233b; font-size: 30px; }
.lede { color: #66758a; margin: 8px 0 0; }
.table-card { border: 1px solid #e7edf4; border-radius: 14px; }
.toolbar { display: flex; gap: 12px; margin-bottom: 18px; }
.pager { display: flex; justify-content: center; padding: 18px 0 4px; }
@media (max-width: 720px) { .hero { align-items: start; flex-direction: column; } }
</style>
