<template>
  <section class="tm-page">
    <PageHeader :title="t('routes.title')" :description="t('routes.description')">
      <el-button v-if="auth.isAdmin" type="primary" @click="openCreate">{{ t('routes.create') }}</el-button>
    </PageHeader>

    <div class="tm-card">
      <DataState
        :loading="loading"
        :error="error"
        :empty="!items.length"
        :error-label="t('common.loadFailed')"
        :retry-label="t('common.retry')"
        :empty-label="t('routes.empty')"
        @retry="load"
      >
        <el-table :data="items" row-key="id">
          <el-table-column prop="domain" :label="t('routes.domain')" min-width="220" />
          <el-table-column prop="pathPrefix" :label="t('routes.path')" width="120" />
          <el-table-column prop="agentId" :label="t('routes.agent')" min-width="180" />
          <el-table-column prop="protocol" :label="t('routes.protocol')" width="110" />
          <!-- The proxy columns render only once a tp-* route exists, so a
               console that never uses the proxy entry keeps its narrow table
               instead of scrolling horizontally for three empty columns. -->
          <el-table-column v-if="hasProxyRoutes" :label="t('routes.proxyUrl')" min-width="200">
            <template #default="{ row }">
              <template v-if="row.proxyUrl">
                <code class="proxy-url" :title="row.proxyUrl">{{ row.proxyUrl }}</code>
                <el-button link type="primary" @click="copyProxyUrl(row.proxyUrl)">{{ t('routes.proxyCopy') }}</el-button>
              </template>
              <span v-else>—</span>
            </template>
          </el-table-column>
          <el-table-column v-if="hasProxyRoutes" :label="t('routes.proxyAuthMode')" width="110">
            <template #default="{ row }">{{ authModeLabel(row) }}</template>
          </el-table-column>
          <el-table-column v-if="hasProxyRoutes" :label="t('routes.proxySourceCIDRs')" width="100">
            <template #default="{ row }">{{ sourceACLCount(row) }}</template>
          </el-table-column>
          <el-table-column :label="t('routes.target')" min-width="180">
            <template #default="{ row }">{{ formatTarget(row) }}</template>
          </el-table-column>
          <el-table-column prop="status" :label="t('routes.status')" width="110" />
          <el-table-column :label="t('routes.actions')" width="170" fixed="right">
            <template #default="{ row }">
              <el-button v-if="isProxyRow(row)" link type="primary" @click="openUsage(row)">{{ t('routes.proxyUsage') }}</el-button>
              <el-button link type="primary" @click="openEdit(row)">{{ t('routes.edit') }}</el-button>
            </template>
          </el-table-column>
        </el-table>
      </DataState>
    </div>

    <el-dialog v-model="dialogVisible" :title="editOpen ? t('routes.editTitle') : t('routes.create')" width="min(560px,92vw)" @closed="resetCreateForm">
      <el-form label-position="top" @submit.prevent="submit">
        <el-form-item :label="t('routes.agent')">
          <el-select
            v-model="form.agentId"
            filterable
            clearable
            :loading="agentsLoading"
            :filter-method="setAgentFilter"
            :placeholder="t('routes.searchAgents')"
          >
            <el-option
              v-for="agent in visibleAgents"
              :key="agent.id"
              :label="`${agent.name} · ${agent.id}`"
              :value="agent.id"
            />
          </el-select>
        </el-form-item>
        <el-form-item :label="t('routes.protocol')">
          <el-select v-model="form.protocol">
            <el-option label="HTTP" value="http" />
            <el-option label="WebSocket" value="websocket" />
            <el-option :label="t('routes.protocolHttpProxy')" value="http-proxy" />
          </el-select>
        </el-form-item>

        <template v-if="!isProxyRoute">
          <el-form-item :label="t('routes.domain')">
            <el-input v-model="form.domain" placeholder="tm-git.example.com" />
            <div class="field-help">{{ t('routes.domainHelp') }}</div>
          </el-form-item>
          <el-form-item :label="t('routes.path')">
            <el-input v-model="form.pathPrefix" placeholder="/" />
          </el-form-item>
          <el-form-item :label="t('routes.targetHost')">
            <el-input v-model="form.targetHost" placeholder="127.0.0.1" />
          </el-form-item>
          <el-form-item :label="t('routes.targetPort')">
            <el-input-number v-model="form.targetPort" :min="1" :max="65535" controls-position="right" />
          </el-form-item>
          <el-form-item :label="t('routes.targetScheme')">
            <el-select v-model="form.targetScheme">
              <el-option label="HTTP" value="http" />
              <el-option label="HTTPS" value="https" />
            </el-select>
            <div class="field-help">{{ t('routes.targetSchemeHelp') }}</div>
          </el-form-item>
          <el-form-item :label="t('routes.hostHeader')">
            <el-input v-model="form.hostHeader" placeholder="service.internal.example.com" />
            <div class="field-help">{{ t('routes.hostHeaderHelp') }}</div>
          </el-form-item>
          <el-form-item v-if="form.targetScheme === 'https'" :label="t('routes.tlsServerName')">
            <el-input v-model="form.tlsServerName" placeholder="service.internal.example.com" />
            <div class="field-help">{{ t('routes.tlsServerNameHelp') }}</div>
          </el-form-item>
        </template>

        <template v-else>
          <el-form-item v-if="proxyDomainSuffix" :label="t('routes.proxyName')">
            <el-input v-model="form.proxyName" placeholder="demo" maxlength="32" />
            <div class="field-help">{{ t('routes.proxyNameHelp') }}</div>
          </el-form-item>
          <el-form-item v-else :label="t('routes.domain')">
            <el-input v-model="form.domain" placeholder="tp-demo.tm.example.com" />
            <div class="field-help">{{ t('routes.proxyNameHelp') }}</div>
          </el-form-item>
          <el-form-item v-if="proxyDomainPreview" :label="t('routes.proxyDomainPreview')">
            <code class="domain-preview">{{ proxyDomainPreview }}</code>
          </el-form-item>
          <el-form-item :label="t('routes.proxyAuthMode')">
            <el-radio-group v-model="form.authMode">
              <el-radio-button value="none">{{ t('routes.proxyAuthNone') }}</el-radio-button>
              <el-radio-button value="basic">{{ t('routes.proxyAuthBasic') }}</el-radio-button>
            </el-radio-group>
          </el-form-item>
          <el-form-item v-if="form.authMode === 'basic'" :label="t('routes.proxyCredential')">
            <div class="inline-row">
              <el-select v-model="form.credentialId" filterable clearable :loading="credentialsLoading">
                <el-option
                  v-for="credential in proxyCredentials"
                  :key="credential.id"
                  :label="`${credential.name} · ${credential.username || credential.publicKey}`"
                  :value="credential.id"
                />
              </el-select>
              <el-button link type="primary" @click="goToCredentials">{{ t('routes.proxyCreateCredential') }}</el-button>
            </div>
          </el-form-item>
          <el-form-item :label="t('routes.proxySourceCIDRs')">
            <div class="inline-row">
              <el-input v-model="form.sourceCIDRs" placeholder="11.71.85.0/24, 10.0.0.0/8" />
              <el-button link type="primary" @click="allowAllSources">{{ t('routes.proxyAllowAll') }}</el-button>
            </div>
            <div class="field-help">{{ t('routes.proxySourceCIDRsHelp') }}</div>
          </el-form-item>
          <el-form-item :label="t('routes.proxyTargetCIDRs')">
            <el-input v-model="form.targetCIDRs" placeholder="10.10.0.0/16" />
          </el-form-item>
          <el-form-item :label="t('routes.proxyTargetPorts')">
            <el-input v-model="form.targetPorts" placeholder="443, 8443" />
          </el-form-item>
          <el-form-item :label="t('routes.proxyAllowPrivateTargets')">
            <el-switch v-model="form.allowPrivateTargets" />
          </el-form-item>
          <el-form-item :label="t('routes.proxyMaxConcurrentTunnels')">
            <el-input-number v-model="form.maxConcurrentTunnels" :min="0" :max="1048576" controls-position="right" />
          </el-form-item>
          <el-form-item :label="t('routes.proxyDescription')">
            <el-input v-model="form.description" type="textarea" :rows="2" maxlength="256" show-word-limit />
          </el-form-item>
        </template>

        <el-form-item v-if="editOpen" :label="t('routes.status')">
          <el-select v-model="form.status">
            <el-option :label="t('routes.active')" value="active" />
            <el-option :label="t('routes.disabled')" value="disabled" />
          </el-select>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialogVisible = false">{{ t('routes.cancel') }}</el-button>
        <el-button type="primary" :loading="saving" @click="submit">{{ t('common.save') }}</el-button>
      </template>
    </el-dialog>

    <el-drawer v-model="usageVisible" :title="t('routes.proxyUsageTitle')" size="min(560px,100vw)">
      <div v-if="usageRoute" class="usage-body">
        <el-descriptions :column="1" border>
          <el-descriptions-item :label="t('routes.proxyUrl')">
            <code class="proxy-url">{{ usageRoute.proxyUrl || usageRoute.domain }}</code>
            <el-button link type="primary" @click="copyProxyUrl(usageRoute.proxyUrl || usageRoute.domain)">{{ t('routes.proxyCopy') }}</el-button>
          </el-descriptions-item>
          <el-descriptions-item :label="t('routes.proxyAuthMode')">{{ authModeLabel(usageRoute) }}</el-descriptions-item>
          <el-descriptions-item :label="t('routes.proxySourceCIDRs')">{{ joinList(usageRoute.sourceCIDRs) }}</el-descriptions-item>
          <el-descriptions-item :label="t('routes.proxyTargetCIDRs')">{{ joinList(usageRoute.targetCIDRs) }}</el-descriptions-item>
          <el-descriptions-item :label="t('routes.proxyTargetPorts')">{{ joinList(usageRoute.targetPorts) }}</el-descriptions-item>
          <el-descriptions-item :label="t('routes.target')">{{ t('routes.proxyTargetDynamic') }}</el-descriptions-item>
        </el-descriptions>

        <h4>{{ t('routes.proxyUsageMacOS') }}</h4>
        <pre class="usage-code">{{ macOSCommand }}</pre>
        <h4>{{ t('routes.proxyUsageWindows') }}</h4>
        <p class="usage-text">{{ t('routes.proxyUsageWindowsHelp') }}</p>
        <h4>{{ t('routes.proxyUsagePAC') }}</h4>
        <pre class="usage-code">{{ pacScript }}</pre>
        <h4>{{ t('routes.proxyUsageCurl') }}</h4>
        <pre class="usage-code">{{ curlCommand }}</pre>

        <h4>{{ t('routes.proxyUsageErrors') }}</h4>
        <el-table :data="proxyErrorCodes" size="small">
          <el-table-column prop="status" label="HTTP" width="90" />
          <el-table-column prop="code" :label="t('routes.proxyUsageErrorCode')" min-width="220" />
        </el-table>
        <p class="usage-text">{{ t('routes.proxyActiveTunnelsHint') }}</p>
      </div>
    </el-drawer>
  </section>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { useAuthStore } from '../stores/auth'
import PageHeader from '../components/PageHeader.vue'
import DataState from '../components/DataState.vue'
import {
  createRoute, getAgents, listRoutes, updateRoute,
  type Agent, type ManagedRoute, type ManagedRouteCreateInput, type ManagedRouteUpdateInput,
} from '../api/client'
import { listCredentials, type Credential } from '../api/credentials'
import { filterAgents, loadAgentsForSelection } from './token-form'

const { t } = useI18n()
const auth = useAuthStore()
const router = useRouter()
const items = ref<ManagedRoute[]>([])
const loading = ref(false)
const error = ref(false)
const createOpen = ref(false)
const editOpen = ref(false)
const saving = ref(false)
const editingRoute = ref<ManagedRoute | null>(null)
const agents = ref<Agent[]>([])
const agentsLoading = ref(false)
const agentFilter = ref('')
const idempotencyKey = ref('')
const proxyCredentials = ref<Credential[]>([])
const credentialsLoading = ref(false)
const usageVisible = ref(false)
const usageRoute = ref<ManagedRoute | null>(null)

const form = reactive({
  agentId: '',
  domain: '',
  pathPrefix: '/',
  protocol: 'http',
  targetHost: '',
  targetPort: 3000,
  hostHeader: '',
  targetScheme: 'http',
  tlsServerName: '',
  status: 'active',
  // Proxy policy. The three list fields are comma-separated strings so the form
  // keeps its scalar-only, field-by-field reset style, matching the Agent policy
  // form; submit() parses them into arrays.
  proxyName: '',
  authMode: 'none' as 'none' | 'basic',
  credentialId: '',
  sourceCIDRs: '',
  targetCIDRs: '',
  targetPorts: '',
  allowPrivateTargets: true,
  maxConcurrentTunnels: 0,
  description: '',
})

const visibleAgents = computed(() => filterAgents(agents.value, agentFilter.value))
const isProxyRoute = computed(() => form.protocol === 'http-proxy')
const hasProxyRoutes = computed(() => items.value.some(route => isProxyRow(route)))
const dialogVisible = computed({
  get: () => createOpen.value || editOpen.value,
  set: (value: boolean) => {
    if (!value) {
      createOpen.value = false
      editOpen.value = false
    }
  },
})

// The public suffix is derived from an existing tp-* route instead of being read
// from server configuration: the admin bundle has no config endpoint, and adding
// one just to render a placeholder would expose deployment details to every
// console user. With no proxy route yet the operator types the full domain and
// the preview stays hidden.
const proxyDomainSuffix = computed(() => {
  const existing = items.value.find(route => isProxyRow(route) && route.domain.startsWith('tp-'))
  if (!existing) return ''
  const dot = existing.domain.indexOf('.')
  return dot > 0 ? existing.domain.slice(dot + 1) : ''
})
const proxyDomainPreview = computed(() => {
  const name = form.proxyName.trim().toLowerCase()
  if (!proxyDomainSuffix.value || !name) return ''
  return `tp-${name}.${proxyDomainSuffix.value}`
})

const usageHost = computed(() => usageRoute.value?.domain ?? '')
const macOSCommand = computed(() => `networksetup -setsecurewebproxy "Wi-Fi" ${usageHost.value} 443`)
const pacScript = computed(() => `function FindProxyForURL(url, host) {\n  return "HTTPS ${usageHost.value}:443";\n}`)
const curlCommand = computed(() => (
  usageRoute.value?.authMode === 'basic'
    ? `curl -x https://${usageHost.value} --proxy-user '<username>:<password>' https://ifconfig.me`
    : `curl -x https://${usageHost.value} https://ifconfig.me`
))

// Status to stable code only. The meanings live in
// docs/user-guide/http-proxy-entry.md; duplicating them here would drift from
// the server's error table, which is the authority.
const proxyErrorCodes = [
  { status: 400, code: 'proxy_target_invalid' },
  { status: 403, code: 'proxy_route_identity_invalid' },
  { status: 403, code: 'proxy_route_unavailable' },
  { status: 403, code: 'proxy_source_denied' },
  { status: 403, code: 'proxy_target_denied' },
  { status: 407, code: 'proxy_auth_required' },
  { status: 407, code: 'proxy_auth_failed' },
  { status: 407, code: 'proxy_auth_backoff' },
  { status: 502, code: 'proxy_egress_unavailable' },
  { status: 503, code: 'proxy_capacity_exhausted' },
  { status: 503, code: 'credential_secret_unavailable' },
  { status: 504, code: 'proxy_egress_timeout' },
]

// Credentials are fetched only when a proxy route is actually being edited, so
// opening the dialog for a plain reverse-proxy route costs no extra request.
watch(isProxyRoute, (value) => {
  if (value && !proxyCredentials.value.length) void loadProxyCredentials()
})

async function load() {
  loading.value = true
  error.value = false
  try {
    items.value = (await listRoutes({ limit: 500 })).items
  } catch {
    error.value = true
  } finally {
    loading.value = false
  }
}

async function loadAgents() {
  agentsLoading.value = true
  try {
    agents.value = await loadAgentsForSelection(getAgents)
  } catch {
    ElMessage.error(t('routes.agentLoadFailed'))
  } finally {
    agentsLoading.value = false
  }
}

async function loadProxyCredentials() {
  credentialsLoading.value = true
  try {
    proxyCredentials.value = (await listCredentials({ type: 'proxy_basic', status: 'active', limit: 200 })).items
  } catch {
    // An unavailable credential list must not block route editing: the select
    // stays empty and the operator can create one through the linked page.
    proxyCredentials.value = []
  } finally {
    credentialsLoading.value = false
  }
}

function openCreate() {
  if (!agents.value.length) void loadAgents()
  createOpen.value = true
}

function openEdit(route: ManagedRoute) {
  editingRoute.value = route
  form.agentId = route.agentId
  form.domain = route.domain
  form.pathPrefix = route.pathPrefix || '/'
  form.protocol = route.protocol
  form.targetHost = route.targetHost
  form.targetPort = route.targetPort
  form.hostHeader = route.hostHeader || ''
  form.targetScheme = route.targetScheme || 'http'
  form.tlsServerName = route.tlsServerName || ''
  form.status = route.status
  form.proxyName = proxyNameOf(route.domain)
  form.authMode = route.authMode === 'basic' ? 'basic' : 'none'
  form.credentialId = route.credentialId || ''
  form.sourceCIDRs = joinList(route.sourceCIDRs, ', ')
  form.targetCIDRs = joinList(route.targetCIDRs, ', ')
  form.targetPorts = joinList(route.targetPorts, ', ')
  form.allowPrivateTargets = route.allowPrivateTargets !== false
  form.maxConcurrentTunnels = route.maxConcurrentTunnels ?? 0
  form.description = route.description || ''
  if (!agents.value.length) void loadAgents()
  editOpen.value = true
}

function openUsage(route: ManagedRoute) {
  usageRoute.value = route
  usageVisible.value = true
}

function goToCredentials() {
  dialogVisible.value = false
  void router.push('/credentials')
}

function allowAllSources() {
  form.sourceCIDRs = '0.0.0.0/0'
}

async function copyProxyUrl(value: string) {
  if (!value) return
  try {
    await navigator.clipboard.writeText(value)
    ElMessage.success(t('routes.proxyCopied'))
  } catch {
    ElMessage.error(t('routes.proxyCopyFailed'))
  }
}

function setAgentFilter(query: string) {
  agentFilter.value = query
}

function isProxyRow(route: ManagedRoute) {
  return route.protocol === 'http-proxy'
}

function proxyNameOf(domain: string) {
  const label = domain.split('.')[0] ?? ''
  return label.startsWith('tp-') ? label.slice(3) : ''
}

function joinList(values: readonly (string | number)[], separator = ', ') {
  if (!values || !values.length) return '—'
  return values.join(separator)
}

function authModeLabel(route: ManagedRoute) {
  if (!isProxyRow(route)) return '—'
  return route.authMode === 'basic' ? t('routes.proxyAuthBasic') : t('routes.proxyAuthNone')
}

function sourceACLCount(route: ManagedRoute) {
  return isProxyRow(route) ? String((route.sourceCIDRs ?? []).length) : '—'
}

function formatTarget(route: ManagedRoute) {
  // The stored * / 0 sentinel is an implementation detail; a tp-* route resolves
  // its target per request, so showing *:0 would only confuse operators.
  if (isProxyRow(route)) return t('routes.proxyTargetDynamic')
  return `${route.targetHost}:${route.targetPort}`
}

const proxyNamePattern = /^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$/
const proxyDomainPattern = /^tp-[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?\..+$/
const cidrPattern = /^[\da-fA-F:.]+\/\d{1,3}$/
const bareIPPattern = /^[\da-fA-F:.]+$/

// parseCIDRList returns null for an invalid entry and an empty array for an
// empty field: "no allowlist" is a valid choice, "typo" is not. A bare IP is
// accepted because the server normalizes it to /32 or /128.
function parseCIDRList(raw: string): string[] | null {
  const out: string[] = []
  for (const item of raw.split(',')) {
    const value = item.trim()
    if (!value) continue
    const match = cidrPattern.exec(value)
    if (!match) {
      if (!bareIPPattern.test(value)) return null
      out.push(value)
      continue
    }
    const limit = value.includes(':') ? 128 : 32
    if (Number(value.slice(value.indexOf('/') + 1)) > limit) return null
    out.push(value)
  }
  return out
}

function parsePortList(raw: string): number[] | null {
  const out: number[] = []
  for (const item of raw.split(',')) {
    const value = item.trim()
    if (!value) continue
    if (!/^\d+$/.test(value)) return null
    const port = Number(value)
    if (port < 1 || port > 65535) return null
    out.push(port)
  }
  return out
}

function validateProxyForm() {
  if (!form.agentId) {
    ElMessage.warning(t('routes.agentRequired'))
    return false
  }
  const name = form.proxyName.trim().toLowerCase()
  const domain = form.domain.trim().toLowerCase()
  if (proxyDomainSuffix.value ? !proxyNamePattern.test(name) : !proxyDomainPattern.test(domain)) {
    ElMessage.warning(t('routes.proxyNameInvalid'))
    return false
  }
  if (form.authMode === 'basic' && !form.credentialId) {
    ElMessage.warning(t('routes.proxyCredentialRequired'))
    return false
  }
  if (parseCIDRList(form.sourceCIDRs) === null || parseCIDRList(form.targetCIDRs) === null) {
    ElMessage.warning(t('routes.proxyCIDRInvalid'))
    return false
  }
  if (parsePortList(form.targetPorts) === null) {
    ElMessage.warning(t('routes.proxyPortInvalid'))
    return false
  }
  return true
}

function validateForm() {
  if (isProxyRoute.value) return validateProxyForm()
  if (!form.agentId) {
    ElMessage.warning(t('routes.agentRequired'))
    return false
  }
  if (!form.domain.trim()) {
    ElMessage.warning(t('routes.domainRequired'))
    return false
  }
  if (!form.targetHost.trim()) {
    ElMessage.warning(t('routes.targetRequired'))
    return false
  }
  return true
}

function resolvedDomain() {
  if (!isProxyRoute.value) return form.domain.trim()
  return proxyDomainPreview.value || form.domain.trim().toLowerCase()
}

function proxyPolicyFields() {
  return {
    authMode: form.authMode,
    // An unauthenticated route must not carry a stale credential reference; the
    // server drops it too, so the UI never sends one it does not mean.
    ...(form.authMode === 'basic' ? { credentialId: form.credentialId } : {}),
    sourceCIDRs: parseCIDRList(form.sourceCIDRs) ?? [],
    targetCIDRs: parseCIDRList(form.targetCIDRs) ?? [],
    targetPorts: parsePortList(form.targetPorts) ?? [],
    allowPrivateTargets: form.allowPrivateTargets,
    maxConcurrentTunnels: form.maxConcurrentTunnels,
    description: form.description.trim(),
  }
}

// The wildcard target is owned by the server: a create sends an empty
// targetHost and port 0, which is what the API accepts for http-proxy, so the
// form can never pin a tp-* route to a single upstream.
function buildProxyRouteInput(): ManagedRouteCreateInput {
  return {
    agentId: form.agentId,
    domain: resolvedDomain(),
    pathPrefix: '/',
    protocol: 'http-proxy',
    targetHost: '',
    targetPort: 0,
    ...proxyPolicyFields(),
  }
}

// PATCH carries the policy only. The server refuses any targetHost other than
// its own sentinel on a proxy route, so an update must not send one at all.
function buildProxyRouteUpdate(): ManagedRouteUpdateInput {
  return { ...proxyPolicyFields(), status: form.status }
}

function buildReverseProxyInput(): ManagedRouteCreateInput {
  return {
    agentId: form.agentId,
    domain: form.domain.trim(),
    pathPrefix: form.pathPrefix.trim() || '/',
    protocol: form.protocol,
    targetHost: form.targetHost.trim(),
    targetPort: form.targetPort,
    hostHeader: form.hostHeader.trim(),
    targetScheme: form.targetScheme,
    tlsServerName: form.targetScheme === 'https' ? form.tlsServerName.trim() : '',
  }
}

async function submit() {
  if (!validateForm()) return
  if (!idempotencyKey.value) idempotencyKey.value = crypto.randomUUID()

  saving.value = true
  try {
    if (editOpen.value && editingRoute.value) {
      const payload = isProxyRoute.value
        ? buildProxyRouteUpdate()
        : { ...buildReverseProxyInput(), status: form.status }
      const updated = await updateRoute(editingRoute.value.id, payload)
      const index = items.value.findIndex(item => item.id === updated.id)
      if (index >= 0) items.value[index] = updated
      ElMessage.success(t('routes.updated'))
    } else {
      await createRoute(isProxyRoute.value ? buildProxyRouteInput() : buildReverseProxyInput(), idempotencyKey.value)
      await load()
      ElMessage.success(t('routes.created'))
    }
    dialogVisible.value = false
  } catch {
    ElMessage.error(editOpen.value ? t('routes.updateFailed') : t('routes.createFailed'))
  } finally {
    saving.value = false
  }
}

function resetCreateForm() {
  form.agentId = ''
  form.domain = ''
  form.pathPrefix = '/'
  form.protocol = 'http'
  form.targetHost = ''
  form.targetPort = 3000
  form.hostHeader = ''
  form.targetScheme = 'http'
  form.tlsServerName = ''
  form.status = 'active'
  form.proxyName = ''
  form.authMode = 'none'
  form.credentialId = ''
  form.sourceCIDRs = ''
  form.targetCIDRs = ''
  form.targetPorts = ''
  form.allowPrivateTargets = true
  form.maxConcurrentTunnels = 0
  form.description = ''
  editingRoute.value = null
  agentFilter.value = ''
  idempotencyKey.value = ''
}

onMounted(() => {
  void load()
  void loadAgents()
})
</script>

<style scoped>
.field-help {
  margin-top: 4px;
  color: var(--tm-muted);
  font-size: 12px;
}
.inline-row {
  display: flex;
  gap: 8px;
  align-items: center;
  width: 100%;
}
.proxy-url {
  margin-right: 8px;
  word-break: break-all;
}
.domain-preview {
  word-break: break-all;
}
.usage-body h4 {
  margin: 18px 0 6px;
  font-size: 13px;
}
.usage-code {
  margin: 0;
  padding: 10px 12px;
  overflow-x: auto;
  background: var(--tm-bg);
  border-radius: 6px;
  font-size: 12px;
  line-height: 1.6;
  white-space: pre-wrap;
  word-break: break-all;
}
.usage-text {
  margin: 0;
  color: var(--tm-muted);
  font-size: 12px;
  line-height: 1.6;
}
</style>
