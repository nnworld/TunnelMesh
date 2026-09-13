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
          <el-table-column :label="t('routes.target')" min-width="180">
            <template #default="{ row }">{{ formatTarget(row) }}</template>
          </el-table-column>
          <el-table-column prop="status" :label="t('routes.status')" width="110" />
          <el-table-column :label="t('routes.actions')" width="110" fixed="right">
            <template #default="{ row }">
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
        <el-form-item :label="t('routes.domain')">
          <el-input v-model="form.domain" placeholder="tm-git.example.com" />
          <div class="field-help">{{ t('routes.domainHelp') }}</div>
        </el-form-item>
        <el-form-item :label="t('routes.path')">
          <el-input v-model="form.pathPrefix" placeholder="/" />
        </el-form-item>
        <el-form-item :label="t('routes.protocol')">
          <el-select v-model="form.protocol">
            <el-option label="HTTP" value="http" />
            <el-option label="WebSocket" value="websocket" />
          </el-select>
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
  </section>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { useI18n } from 'vue-i18n'
import { useAuthStore } from '../stores/auth'
import PageHeader from '../components/PageHeader.vue'
import DataState from '../components/DataState.vue'
import { createRoute, getAgents, listRoutes, updateRoute, type Agent, type ManagedRoute } from '../api/client'
import { filterAgents, loadAgentsForSelection } from './token-form'

const { t } = useI18n()
const auth = useAuthStore()
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
})

const visibleAgents = computed(() => filterAgents(agents.value, agentFilter.value))
const dialogVisible = computed({
  get: () => createOpen.value || editOpen.value,
  set: (value: boolean) => {
    if (!value) {
      createOpen.value = false
      editOpen.value = false
    }
  },
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
  if (!agents.value.length) void loadAgents()
  editOpen.value = true
}

function setAgentFilter(query: string) {
  agentFilter.value = query
}

function formatTarget(route: ManagedRoute) {
  return `${route.targetHost}:${route.targetPort}`
}

function validateForm() {
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

async function submit() {
  if (!validateForm()) return
  if (!idempotencyKey.value) idempotencyKey.value = crypto.randomUUID()

  saving.value = true
  try {
    const input = {
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
    if (editOpen.value && editingRoute.value) {
      const updated = await updateRoute(editingRoute.value.id, { ...input, status: form.status })
      const index = items.value.findIndex(item => item.id === updated.id)
      if (index >= 0) items.value[index] = updated
      ElMessage.success(t('routes.updated'))
    } else {
      await createRoute(input, idempotencyKey.value)
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
</style>
