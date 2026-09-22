<template>
  <section class="tm-page vpn-page">
    <PageHeader :title="t('vpn.title')" :description="t('vpn.description')">
      <el-button class="vpn-create" type="primary" @click="openCreate">{{ t('vpn.create') }}</el-button>
      <el-button :loading="loading" @click="reload">{{ t('vpn.refresh') }}</el-button>
    </PageHeader>

    <!-- A peer issued today is managed and audited but cannot carry traffic, so
         the notice is on the page itself rather than buried in the docs. -->
    <el-alert class="phase-notice" type="info" show-icon :closable="false" :title="t('vpn.dataPlanePending')" />

    <div v-if="pool" class="tm-card pool-overview">
      <h3>{{ t('vpn.poolTitle') }}</h3>
      <dl>
        <dt>{{ t('vpn.poolNode') }}</dt><dd>{{ pool.nodeId || '—' }}</dd>
        <dt>{{ t('vpn.status') }}</dt><dd>{{ pool.enabled ? t('vpn.poolEnabled') : t('vpn.poolDisabled') }}</dd>
        <dt>{{ t('vpn.poolSubnet') }}</dt><dd>{{ pool.subnet || '—' }}</dd>
        <dt>{{ t('vpn.poolAllocated') }}</dt><dd>{{ pool.allocated ?? '—' }}</dd>
        <dt>{{ t('vpn.poolCapacity') }}</dt><dd>{{ pool.capacity ?? '—' }}</dd>
        <dt>{{ t('vpn.poolPeers') }}</dt><dd>{{ pool.peers ?? '—' }}</dd>
        <dt>{{ t('vpn.poolIcmp') }}</dt><dd>{{ pool.icmpCapable ? t('common.yes') : t('common.no') }}</dd>
      </dl>
    </div>
    <!-- 501 is a state, not a failure: the pool level is unknown, and rendering
         it as zero allocated would be a number nobody can act on. -->
    <p v-else class="pool-note">{{ t('vpn.poolUnavailable') }}</p>

    <div class="tm-card table-card">
      <div class="toolbar">
        <el-input v-model="keyword" class="keyword-input" clearable :placeholder="t('vpn.keyword')" @keyup.enter="reload" @clear="reload" />
        <el-segmented v-model="filterStatus" :options="statusOptions" @change="reload" />
        <el-button :loading="loading" @click="reload">{{ t('vpn.refresh') }}</el-button>
      </div>
      <DataState :loading="loading" :error="loadError" :empty="!items.length" :error-label="t('vpn.loadFailed')" :retry-label="t('common.retry')" :empty-label="t('vpn.empty')" @retry="reload">
        <el-table :data="items" row-key="id">
          <el-table-column prop="name" :label="t('vpn.name')" min-width="140" />
          <el-table-column :label="t('vpn.status')" width="110">
            <template #default="{ row }"><StatusTag :kind="statusKind(row.status)" :label="statusLabel(row.status)" /></template>
          </el-table-column>
          <el-table-column prop="vpnIp" :label="t('vpn.vpnIp')" width="140" />
          <el-table-column :label="t('vpn.agent')" min-width="150">
            <template #default="{ row }">{{ agentName(row.agentId) }}</template>
          </el-table-column>
          <!-- A count rather than the list: the canonical encoding is long and a
               wide column is what forces a horizontal scrollbar. -->
          <el-table-column :label="t('vpn.allowedIps')" width="120">
            <template #default="{ row }">{{ row.allowedIps?.length ?? 0 }}</template>
          </el-table-column>
          <el-table-column :label="t('vpn.icmp')" width="90">
            <template #default="{ row }"><StatusTag :kind="row.icmpEnabled ? 'success' : 'info'" :label="row.icmpEnabled ? t('common.yes') : t('common.no')" /></template>
          </el-table-column>
          <el-table-column :label="t('vpn.expiresAt')" width="180">
            <template #default="{ row }">{{ row.expiresAt ? formatDate(row.expiresAt) : t('vpn.never') }}</template>
          </el-table-column>
          <el-table-column :label="t('vpn.actions')" width="300" fixed="right">
            <template #default="{ row }">
              <el-button class="vpn-usage" link type="primary" @click="openUsage(row)">{{ t('vpn.usageOpen') }}</el-button>
              <el-button link type="primary" @click="showDetail(row)">{{ t('vpn.detail') }}</el-button>
              <el-button v-if="row.status !== 'revoked'" link type="primary" @click="openEdit(row)">{{ t('vpn.edit') }}</el-button>
              <el-button v-if="row.status !== 'revoked'" link type="warning" @click="rotate(row)">{{ t('vpn.rotate') }}</el-button>
              <el-button v-if="row.status !== 'revoked'" link type="danger" @click="revoke(row)">{{ t('vpn.revoke') }}</el-button>
            </template>
          </el-table-column>
        </el-table>
      </DataState>
      <div v-if="page.nextCursor" class="load-more"><el-button :loading="loadingMore" :disabled="!page.hasMore" @click="loadMore">{{ t('vpn.loadMore') }}</el-button></div>
    </div>

    <el-dialog v-model="formVisible" :title="editingPeer ? t('vpn.edit') : t('vpn.create')" width="min(680px,94vw)" @closed="resetForm">
      <el-form label-position="top" @submit.prevent="save">
        <el-form-item :label="t('vpn.form.name')" :error="submitted && nameInvalid ? t('vpn.form.nameInvalid') : ''">
          <el-input v-model="form.name" class="vpn-name" maxlength="255" />
          <p class="field-help">{{ t('vpn.form.nameHelp') }}</p>
        </el-form-item>
        <el-form-item :label="t('vpn.form.agent')" :error="submitted && agentInvalid ? t('vpn.form.agentRequired') : ''">
          <el-select v-model="form.agentId" class="vpn-agent" filterable clearable :loading="agentsLoading" :filter-method="setAgentFilter" :placeholder="t('vpn.form.searchAgents')">
            <el-option v-for="agent in visibleAgents" :key="agent.id" :label="`${agent.name} · ${agent.id}`" :value="agent.id" />
          </el-select>
        </el-form-item>
        <el-form-item :label="t('vpn.form.allowedIps')" :error="cidrInvalid ? t('vpn.form.cidrInvalid') : ''">
          <el-input v-model="form.allowedIpsText" class="vpn-cidrs" placeholder="10.0.0.0/8, 192.168.0.0/16" />
          <p class="field-help">{{ t('vpn.form.allowedIpsHelp') }}</p>
        </el-form-item>
        <el-form-item :label="t('vpn.form.allowedPorts')" :error="portsInvalid ? t('vpn.form.portInvalid') : ''">
          <el-input v-model="form.allowedPortsText" class="vpn-ports" placeholder="443, 8443" />
          <p class="field-help">{{ t('vpn.form.allowedPortsHelp') }}</p>
        </el-form-item>
        <el-form-item :label="t('vpn.form.allowPrivateTargets')">
          <el-switch v-model="form.allowPrivateTargets" class="vpn-private" />
          <p class="field-help">{{ t('vpn.form.allowPrivateTargetsHelp') }}</p>
        </el-form-item>
        <el-form-item :label="t('vpn.form.icmpEnabled')">
          <el-switch v-model="form.icmpEnabled" class="vpn-icmp" />
          <p class="field-help">{{ t('vpn.form.icmpEnabledHelp') }}</p>
        </el-form-item>
        <div class="limit-row">
          <el-form-item :label="t('vpn.form.maxConcurrentFlows')" :error="limitsInvalid ? t('vpn.form.limitInvalid') : ''">
            <el-input-number v-model="form.maxConcurrentFlows" class="vpn-flows" :min="0" :step="1" />
          </el-form-item>
          <el-form-item :label="t('vpn.form.packetRateLimit')" :error="limitsInvalid ? t('vpn.form.limitInvalid') : ''">
            <el-input-number v-model="form.packetRateLimit" class="vpn-rate" :min="0" :step="1" />
          </el-form-item>
        </div>
        <p class="field-help">{{ t('vpn.form.limitHelp') }}</p>
        <el-form-item :label="t('vpn.form.expiresAt')" :error="submitted && expiryInvalid ? t('vpn.form.expiryInvalid') : ''">
          <el-date-picker v-model="form.expiresAt" class="vpn-expiry" type="datetime" value-format="YYYY-MM-DDTHH:mm:ssZ" :placeholder="t('vpn.form.expiresAtHelp')" />
          <p class="field-help">{{ t('vpn.form.expiresAtHelp') }}</p>
        </el-form-item>
        <el-form-item :label="t('vpn.form.description')" :error="descriptionInvalid ? t('vpn.form.descriptionInvalid') : ''">
          <el-input v-model="form.description" class="vpn-description" type="textarea" :rows="2" maxlength="255" show-word-limit />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="formVisible = false">{{ t('vpn.cancel') }}</el-button>
        <el-button class="vpn-submit" type="primary" :loading="saving" @click="save">{{ t('vpn.save') }}</el-button>
      </template>
    </el-dialog>

    <el-drawer v-model="detailVisible" :title="t('vpn.detailTitle')" size="min(480px,100vw)">
      <el-descriptions v-if="detailPeer" :column="1" border>
        <el-descriptions-item :label="t('vpn.name')">{{ detailPeer.name }}</el-descriptions-item>
        <el-descriptions-item :label="t('vpn.status')"><StatusTag :kind="statusKind(detailPeer.status)" :label="statusLabel(detailPeer.status)" /></el-descriptions-item>
        <el-descriptions-item :label="t('vpn.vpnIp')">{{ detailPeer.vpnIp }}</el-descriptions-item>
        <el-descriptions-item :label="t('vpn.agent')">{{ agentName(detailPeer.agentId) }}</el-descriptions-item>
        <el-descriptions-item :label="t('vpn.usage.peerPublicKey')"><code class="key-text">{{ detailPeer.publicKey }}</code></el-descriptions-item>
        <el-descriptions-item :label="t('vpn.allowedIps')">{{ joinList(detailPeer.allowedIps) }}</el-descriptions-item>
        <el-descriptions-item :label="t('vpn.allowedPorts')">{{ detailPeer.allowedPorts?.length ? detailPeer.allowedPorts.join(', ') : t('vpn.all') }}</el-descriptions-item>
        <el-descriptions-item :label="t('vpn.form.allowPrivateTargets')">{{ detailPeer.allowPrivateTargets ? t('common.yes') : t('common.no') }}</el-descriptions-item>
        <el-descriptions-item :label="t('vpn.icmp')">{{ detailPeer.icmpEnabled ? t('common.yes') : t('common.no') }}</el-descriptions-item>
        <el-descriptions-item :label="t('vpn.form.maxConcurrentFlows')">{{ detailPeer.maxConcurrentFlows }}</el-descriptions-item>
        <el-descriptions-item :label="t('vpn.form.packetRateLimit')">{{ detailPeer.packetRateLimit }}</el-descriptions-item>
        <el-descriptions-item :label="t('vpn.expiresAt')">{{ detailPeer.expiresAt ? formatDate(detailPeer.expiresAt) : t('vpn.never') }}</el-descriptions-item>
        <el-descriptions-item :label="t('vpn.form.description')">{{ detailPeer.description || '—' }}</el-descriptions-item>
        <el-descriptions-item :label="t('vpn.createdAt')">{{ formatDate(detailPeer.createdAt) }}</el-descriptions-item>
        <el-descriptions-item :label="t('vpn.updatedAt')">{{ formatDate(detailPeer.updatedAt) }}</el-descriptions-item>
      </el-descriptions>
    </el-drawer>

    <el-drawer v-model="usageVisible" :title="t('vpn.usageTitle')" size="min(640px,100vw)" @closed="clearRevealedConfig">
      <template v-if="usagePeer">
        <el-descriptions :column="1" border class="usage-summary">
          <el-descriptions-item :label="t('vpn.vpnIp')">{{ usagePeer.vpnIp }}</el-descriptions-item>
          <el-descriptions-item :label="t('vpn.usage.peerPublicKey')"><code class="key-text">{{ usagePeer.publicKey }}</code></el-descriptions-item>
          <el-descriptions-item v-if="pool?.endpointHost" :label="t('vpn.usage.endpoint')">{{ pool.endpointHost }}</el-descriptions-item>
          <el-descriptions-item :label="t('vpn.usage.mtu')">{{ t('vpn.usage.mtuHelp') }}</el-descriptions-item>
        </el-descriptions>

        <div class="reveal-block">
          <h3>{{ t('vpn.reveal.title') }}</h3>
          <el-alert type="warning" show-icon :closable="false" :title="t('vpn.reveal.warning')" />
          <el-checkbox v-model="revealAck" class="vpn-reveal-ack">{{ t('vpn.reveal.acknowledge') }}</el-checkbox>
          <p class="field-help">{{ t('vpn.reveal.acknowledgeHelp') }}</p>
          <div class="reveal-row">
            <el-input v-model="revealConfirm" class="vpn-reveal-confirm" :placeholder="t('vpn.reveal.confirmPlaceholder')" autocomplete="off" />
            <el-button class="vpn-reveal-submit" type="primary" :loading="revealing" :disabled="!canReveal" @click="reveal">{{ t('vpn.reveal.submit') }}</el-button>
          </div>
          <p class="field-help">{{ t('vpn.reveal.confirmLabel') }}</p>
          <el-alert v-if="revealNotice" class="reveal-notice" :type="revealNoticeKind" show-icon :closable="false" :title="revealNotice" />
          <template v-if="revealedConfig">
            <pre class="vpn-config">{{ revealedConfig }}</pre>
            <div class="reveal-actions">
              <el-button @click="copyConfig">{{ t('vpn.reveal.copy') }}</el-button>
              <el-button @click="downloadConfig">{{ t('vpn.reveal.download') }}</el-button>
              <el-button type="danger" plain @click="clearRevealedConfig">{{ t('vpn.usageClose') }}</el-button>
            </div>
            <p class="field-help">{{ t('vpn.reveal.oneTime') }}</p>
          </template>
        </div>

        <div class="usage-block">
          <h3>{{ t('vpn.usage.platformsTitle') }}</h3>
          <ul>
            <li v-for="key in platformKeys" :key="key">{{ t(`vpn.usage.platforms.${key}`) }}</li>
          </ul>
          <p class="field-help">{{ t('vpn.usage.clientRoutes') }}</p>
        </div>

        <div class="usage-block">
          <h3>{{ t('vpn.usage.boundariesTitle') }}</h3>
          <ul>
            <li v-for="key in boundaryKeys" :key="key">{{ t(`vpn.usage.boundaries.${key}`) }}</li>
          </ul>
        </div>

        <div class="usage-block">
          <h3>{{ t('vpn.usage.troubleshootingTitle') }}</h3>
          <p>{{ t('vpn.usage.troubleshooting') }}</p>
          <p class="field-help">{{ t('vpn.usage.metrics') }}</p>
          <p class="field-help">{{ t('vpn.usage.auditEvent') }}</p>
          <p class="field-help">{{ t('vpn.usage.flowsHint') }}</p>
        </div>
      </template>
      <template #footer>
        <el-button class="vpn-usage-close" @click="closeUsage">{{ t('vpn.usageClose') }}</el-button>
      </template>
    </el-drawer>
  </section>
</template>

<script setup lang="ts">
import { computed, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { useI18n } from 'vue-i18n'
import PageHeader from '../components/PageHeader.vue'
import DataState from '../components/DataState.vue'
import StatusTag from '../components/StatusTag.vue'
import { useFormatDateTime } from '../i18n/format'
import { getAgents, type Agent } from '../api/client'
import {
  createVpnPeer, listVpnNodes, listVpnPeers, patchVpnPeer, revealVpnPeerConfig, revokeVpnPeer,
  rotateVpnPeer, type VpnNodeStatus, type VpnPeer, type VpnPeerStatus,
} from '../api/vpn'
import { filterAgents, loadAgentsForSelection } from './token-form'
import {
  isRevealConfirmation, maxPeerDescriptionLength, maxPeerNameLength, normalizeVpnExpiry,
  parseVpnCidrList, parseVpnPortList, vpnPatchFromForm, vpnPeerInputFromForm, type VpnPeerForm,
} from './vpn-form'
import { isVpnUnavailableState, vpnErrorMessage } from './vpn-errors'

type StatusFilter = VpnPeerStatus | 'all'

const { t } = useI18n()
const formatDate = useFormatDateTime()

const page = ref<{ items: VpnPeer[]; nextCursor?: string; hasMore?: boolean }>({ items: [] })
const items = computed(() => page.value.items)
const loading = ref(false)
const loadingMore = ref(false)
const loadError = ref(false)
const keyword = ref('')
const filterStatus = ref<StatusFilter>('active')
const statusOptions = computed(() => [
  { label: t('vpn.active'), value: 'active' },
  { label: t('vpn.disabled'), value: 'disabled' },
  { label: t('vpn.revoked'), value: 'revoked' },
  { label: t('vpn.all'), value: 'all' },
])

// The pool overview comes from the gateway runtime, so a 501 leaves it null and
// the page says the level is unknown instead of inventing one.
const pool = ref<VpnNodeStatus | null>(null)

const agents = ref<Agent[]>([])
const agentsLoading = ref(false)
const agentFilter = ref('')
const visibleAgents = computed(() => filterAgents(agents.value, agentFilter.value))
function setAgentFilter(value: string) { agentFilter.value = value }
function agentName(agentId: string) {
  return agents.value.find(agent => agent.id === agentId)?.name || agentId || '—'
}

function statusKind(status: VpnPeerStatus) {
  if (status === 'active') return 'success'
  return status === 'disabled' ? 'warning' : 'info'
}
function statusLabel(status: VpnPeerStatus) {
  if (status === 'active') return t('vpn.active')
  return status === 'disabled' ? t('vpn.disabled') : t('vpn.revoked')
}
function joinList(values: string[] | undefined) {
  return values?.length ? values.join(', ') : '—'
}

const emptyForm = (): VpnPeerForm => ({
  name: '', description: '', agentId: '', allowedIpsText: '', allowedPortsText: '',
  allowPrivateTargets: false, icmpEnabled: false, maxConcurrentFlows: 0, packetRateLimit: 0, expiresAt: '',
})
const form = reactive<VpnPeerForm>(emptyForm())
const formVisible = ref(false)
const saving = ref(false)
const submitted = ref(false)
const editingPeer = ref<VpnPeer | null>(null)

// Field level errors use the same parsers the payload builder uses, so the form
// cannot accept something the server would refuse: one rule, two consumers.
const nameInvalid = computed(() => !form.name.trim() || [...form.name.trim()].length > maxPeerNameLength)
const descriptionInvalid = computed(() => [...form.description].length > maxPeerDescriptionLength)
const agentInvalid = computed(() => !form.agentId.trim())
const cidrInvalid = computed(() => parseVpnCidrList(form.allowedIpsText) === null)
const portsInvalid = computed(() => parseVpnPortList(form.allowedPortsText) === null)
const limitsInvalid = computed(() => (
  !Number.isInteger(form.maxConcurrentFlows) || form.maxConcurrentFlows < 0 ||
  !Number.isInteger(form.packetRateLimit) || form.packetRateLimit < 0
))
const expiryInvalid = computed(() => Boolean(form.expiresAt) && normalizeVpnExpiry(form.expiresAt) === null)
const formInvalid = computed(() => (
  nameInvalid.value || descriptionInvalid.value || agentInvalid.value || cidrInvalid.value ||
  portsInvalid.value || limitsInvalid.value || expiryInvalid.value
))

const detailVisible = ref(false)
const detailPeer = ref<VpnPeer | null>(null)

const usageVisible = ref(false)
const usagePeer = ref<VpnPeer | null>(null)
const revealAck = ref(false)
const revealConfirm = ref('')
const revealing = ref(false)
const revealedConfig = ref('')
const revealNotice = ref('')
// An unavailable state is information: there is nothing for the operator to
// correct, so it must not wear the red of a validation failure.
const revealNoticeKind = ref<'info' | 'error'>('info')
const canReveal = computed(() => revealAck.value && isRevealConfirmation(revealConfirm.value))
const platformKeys = ['windows', 'macos', 'ios', 'android', 'linux']
const boundaryKeys = ['sourceAddress', 'icmpOnly', 'protocols', 'fragmentation', 'dataPlaneErrors', 'noL2']

async function loadPool() {
  try {
    const result = await listVpnNodes()
    pool.value = result.items?.[0] ?? null
  } catch {
    // A node that does not serve VPN yet is the expected answer in this
    // release, so the level stays unknown and no toast is raised.
    pool.value = null
  }
}

async function load(cursor?: string) {
  const appending = Boolean(cursor)
  if (appending) loadingMore.value = true
  else loading.value = true
  if (!appending) loadError.value = false
  try {
    const result = await listVpnPeers({
      keyword: keyword.value.trim() || undefined,
      status: filterStatus.value,
      cursor,
      limit: 20,
    })
    page.value = appending
      ? { items: [...page.value.items, ...result.items], nextCursor: result.nextCursor, hasMore: result.hasMore }
      : { items: result.items ?? [], nextCursor: result.nextCursor, hasMore: result.hasMore }
  } catch {
    if (!appending) loadError.value = true
  } finally {
    loading.value = false
    loadingMore.value = false
  }
}

async function loadAgents() {
  if (agents.value.length) return
  agentsLoading.value = true
  try {
    agents.value = await loadAgentsForSelection(getAgents)
  } catch {
    agents.value = []
  } finally {
    agentsLoading.value = false
  }
}

function reload() { void load(); void loadPool() }
function loadMore() { void load(page.value.nextCursor) }

// One key per dialog session, minted when the form is (re)opened. A save that
// fails on a lost response is retried in place, and a retry carrying a fresh
// key is a second operation the server cannot deduplicate: a second peer and a
// second burned address. Rotate, revoke and reveal keep minting a key per
// click, because each of those is a new explicit confirmation rather than a
// retry of the one before it.
const formIdempotencyKey = ref('')

function resetForm() {
  Object.assign(form, emptyForm())
  submitted.value = false
  editingPeer.value = null
  formIdempotencyKey.value = crypto.randomUUID()
}

function openCreate() {
  resetForm()
  formVisible.value = true
  void loadAgents()
}

function openEdit(peer: VpnPeer) {
  resetForm()
  editingPeer.value = peer
  Object.assign(form, {
    name: peer.name,
    description: peer.description ?? '',
    agentId: peer.agentId,
    allowedIpsText: (peer.allowedIps ?? []).join(', '),
    allowedPortsText: (peer.allowedPorts ?? []).join(', '),
    allowPrivateTargets: peer.allowPrivateTargets,
    icmpEnabled: peer.icmpEnabled,
    maxConcurrentFlows: peer.maxConcurrentFlows,
    packetRateLimit: peer.packetRateLimit,
    expiresAt: peer.expiresAt ?? '',
  })
  formVisible.value = true
  void loadAgents()
}

async function save() {
  submitted.value = true
  // Validation is not a trust boundary, the server repeats all of it; refusing
  // here only saves a round trip and names the offending field.
  if (formInvalid.value) return
  saving.value = true
  try {
    if (editingPeer.value) {
      const original = editingPeer.value
      const patch = vpnPatchFromForm(form, {
        name: original.name, description: original.description ?? '', agentId: original.agentId,
        allowedIps: original.allowedIps ?? [], allowedPorts: original.allowedPorts ?? [],
        allowPrivateTargets: original.allowPrivateTargets, icmpEnabled: original.icmpEnabled,
        maxConcurrentFlows: original.maxConcurrentFlows, packetRateLimit: original.packetRateLimit,
        expiresAt: original.expiresAt,
      })
      if (patch === null) return
      await patchVpnPeer(original.id, patch, formIdempotencyKey.value)
      ElMessage.success(t('vpn.updated'))
    } else {
      const input = vpnPeerInputFromForm(form)
      if (input === null) return
      await createVpnPeer(input, formIdempotencyKey.value)
      ElMessage.success(t('vpn.created'))
    }
    formVisible.value = false
    reload()
  } catch (error) {
    ElMessage.error(vpnErrorMessage(error, t))
  } finally {
    saving.value = false
  }
}

function showDetail(peer: VpnPeer) {
  detailPeer.value = peer
  detailVisible.value = true
}

function openUsage(peer: VpnPeer) {
  usagePeer.value = peer
  clearRevealedConfig()
  usageVisible.value = true
}

function closeUsage() {
  usageVisible.value = false
  // Clearing on the way out rather than on the transition's closed event: the
  // plaintext key must not outlive the click that hid it.
  clearRevealedConfig()
}

// clearRevealedConfig drops every copy of the rendered file. The private key
// lives in this ref and nowhere else: no store, no persistent storage, no
// download cache beyond the blob the user asked for.
function clearRevealedConfig() {
  revealedConfig.value = ''
  revealConfirm.value = ''
  revealAck.value = false
  revealNotice.value = ''
  revealNoticeKind.value = 'info'
}

async function reveal() {
  if (!usagePeer.value || !canReveal.value) return
  revealing.value = true
  revealNotice.value = ''
  try {
    const result = await revealVpnPeerConfig(usagePeer.value.id, revealConfirm.value, crypto.randomUUID())
    revealedConfig.value = result.config
  } catch (error) {
    revealedConfig.value = ''
    revealNotice.value = vpnErrorMessage(error, t, 'vpn.reveal.failed')
    revealNoticeKind.value = isVpnUnavailableState(error) ? 'info' : 'error'
  } finally {
    revealing.value = false
  }
}

async function copyConfig() {
  try {
    await navigator.clipboard?.writeText(revealedConfig.value)
    ElMessage.success(t('vpn.reveal.copied'))
  } catch {
    ElMessage.warning(t('vpn.reveal.copyFailed'))
  }
}

function downloadConfig() {
  if (!revealedConfig.value || !usagePeer.value) return
  try {
    const blob = new Blob([revealedConfig.value], { type: 'text/plain' })
    const url = URL.createObjectURL(blob)
    const anchor = document.createElement('a')
    anchor.href = url
    anchor.download = `tunnelmesh-${usagePeer.value.name || usagePeer.value.id}.conf`
    anchor.click()
    URL.revokeObjectURL(url)
  } catch {
    ElMessage.warning(t('vpn.reveal.copyFailed'))
  }
}

async function rotate(peer: VpnPeer) {
  try {
    await ElMessageBox.confirm(t('vpn.rotateConfirm'), t('vpn.rotateTitle'), { type: 'warning' })
  } catch { return }
  try {
    await rotateVpnPeer(peer.id, crypto.randomUUID())
    ElMessage.success(t('vpn.rotated'))
    reload()
  } catch (error) {
    ElMessage.error(vpnErrorMessage(error, t))
  }
}

async function revoke(peer: VpnPeer) {
  try {
    await ElMessageBox.confirm(t('vpn.revokeConfirm'), t('vpn.revokeTitle'), { type: 'warning' })
  } catch { return }
  try {
    await revokeVpnPeer(peer.id, crypto.randomUUID())
    ElMessage.success(t('vpn.revokedMessage'))
    reload()
  } catch (error) {
    ElMessage.error(vpnErrorMessage(error, t))
  }
}

reload()
</script>

<style scoped>
.phase-notice{margin-bottom:14px}.pool-overview{margin-bottom:14px}.pool-overview h3{margin:0 0 10px;font-size:15px}.pool-overview dl{display:grid;grid-template-columns:repeat(auto-fit,minmax(150px,1fr));gap:8px 16px;margin:0}.pool-overview dt{color:var(--tm-muted);font-size:12px}.pool-overview dd{margin:0;font-weight:600}.pool-note{margin:0 0 14px;color:var(--tm-muted);font-size:13px}.toolbar{display:flex;gap:10px;align-items:center;margin-bottom:12px;flex-wrap:wrap}.keyword-input{width:220px}.load-more{margin-top:12px;text-align:center}.field-help{margin:4px 0 0;color:var(--tm-muted);font-size:12px;line-height:1.6}.key-text{word-break:break-all;font-family:ui-monospace,SFMono-Regular,Menlo,monospace;font-size:12px}.limit-row{display:grid;grid-template-columns:1fr 1fr;gap:12px}.usage-summary{margin-bottom:16px}.reveal-block,.usage-block{margin-bottom:20px}.reveal-block h3,.usage-block h3{margin:0 0 10px;font-size:15px}.reveal-row{display:flex;gap:10px;align-items:center;margin-top:10px}.reveal-row .el-input{max-width:220px}.reveal-notice{margin-top:10px}.vpn-config{margin:12px 0 0;padding:12px;background:#0f172a;color:#e2e8f0;border-radius:8px;font-family:ui-monospace,SFMono-Regular,Menlo,monospace;font-size:12px;line-height:1.7;white-space:pre-wrap;word-break:break-all}.reveal-actions{display:flex;gap:8px;margin-top:10px;flex-wrap:wrap}.usage-block ul{margin:0;padding-left:18px;color:var(--tm-text);font-size:13px;line-height:1.8}
</style>
