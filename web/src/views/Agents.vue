<template>
  <section class="tm-page">
    <PageHeader :title="t('agents.title')"><el-button v-if="auth.isAdmin" type="primary" @click="createOpen = true">{{ t('agents.create') }}</el-button></PageHeader>
    <div class="tm-card">
      <el-form class="filter-form" label-position="top" @submit.prevent="query">
        <div class="filter-grid">
          <el-form-item :label="t('agents.name')">
            <el-input v-model="keyword" :placeholder="t('agents.keyword')" clearable @keyup.enter="query" @clear="query" />
          </el-form-item>
        </div>
        <div class="filter-actions">
          <el-button @click="reset">{{ t('agents.reset') }}</el-button>
          <el-button type="primary" @click="query">{{ t('agents.query') }}</el-button>
        </div>
      </el-form>

      <DataState :loading="loading" :error="error" :empty="!items.length" :error-label="t('common.loadFailed')" :retry-label="t('common.retry')" :empty-label="t('common.empty')" @retry="load">
        <el-table :data="items" :default-sort="{ prop: 'createdAt', order: 'descending' }">
          <el-table-column prop="name" :label="t('agents.name')" />
          <el-table-column prop="id" label="ID" />
          <el-table-column :label="t('agents.status')">
            <template #default="scope"><StatusTag :kind="scope.row.status === 'online' ? 'success' : 'info'" :label="scope.row.status === 'online' ? t('agents.online') : t('agents.offline')" /></template>
          </el-table-column>
          <el-table-column prop="createdAt" :label="t('agents.createdAt')" width="190" sortable :sort-method="byCreatedAt">
            <template #default="scope">{{ formatDate(scope.row.createdAt) }}</template>
          </el-table-column>
          <el-table-column prop="updatedAt" :label="t('agents.updatedAt')" width="190" sortable :sort-method="byUpdatedAt">
            <template #default="scope">{{ formatDate(scope.row.updatedAt) }}</template>
          </el-table-column>
          <el-table-column :label="t('agents.enabledColumn')">
            <template #default="scope"><StatusTag :kind="scope.row.enabled ? 'success' : 'warning'" :label="scope.row.enabled ? t('agents.active') : t('agents.disabled')" /></template>
          </el-table-column>
          <el-table-column :label="t('agents.actions')">
            <template #default="scope">
              <el-button link type="primary" @click="$router.push(`/agents/${scope.row.id}`)">{{ t('agents.details') }}</el-button>
              <el-button v-if="auth.isAdmin" link type="primary" @click="openEdit(scope.row)">{{ t('agents.edit') }}</el-button>
            </template>
          </el-table-column>
        </el-table>
        <div v-if="pageIndex > 0 || nextCursor" class="pager">
          <el-button v-if="pageIndex > 0" :loading="loading" @click="previousPage">{{ t('agents.previousPage') }}</el-button>
          <span class="pager-label">{{ t('agents.page', { number: pageIndex + 1 }) }}</span>
          <el-button v-if="nextCursor" :loading="loading" @click="nextPage">{{ t('agents.nextPage') }}</el-button>
        </div>
      </DataState>
    </div>
  </section>

  <el-dialog v-model="editOpen" :title="t('agents.editTitle')" width="min(440px,92vw)" @closed="resetEditForm">
    <el-form label-position="top" @submit.prevent="saveEdit">
      <el-form-item :label="t('agents.name')"><el-input v-model="editName" maxlength="255" /></el-form-item>
      <el-form-item :label="t('agents.enabledColumn')">
        <el-switch v-model="editEnabled" :active-text="t('agents.active')" :inactive-text="t('agents.disabled')" />
        <!-- el-form-item lays its content out as a flex row, so the hint needs
             the full width to land under the switch instead of beside it. -->
        <p class="hint">{{ t('agents.enabledHint') }}</p>
      </el-form-item>
    </el-form>
    <template #footer>
      <el-button @click="editOpen = false">{{ t('users.cancel') }}</el-button>
      <el-button type="primary" :loading="saving" @click="saveEdit">{{ t('common.save') }}</el-button>
    </template>
  </el-dialog>

  <el-dialog v-model="createOpen" :title="t('agents.create')" width="min(440px,92vw)" @closed="resetCreateForm">
    <el-form label-position="top" @submit.prevent="create">
      <el-form-item :label="t('agents.name')"><el-input v-model="createName" maxlength="255" /></el-form-item>
      <el-form-item :label="t('agents.enabledColumn')"><el-switch v-model="createEnabled" :active-text="t('agents.active')" :inactive-text="t('agents.disabled')" /></el-form-item>
    </el-form>
    <template #footer>
      <el-button @click="createOpen = false">{{ t('users.cancel') }}</el-button>
      <el-button type="primary" :loading="saving" @click="create">{{ t('agents.create') }}</el-button>
    </template>
  </el-dialog>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useFormatDateTime } from '../i18n/format'
import { ElMessage } from 'element-plus'
import { useI18n } from 'vue-i18n'
import { useAuthStore } from '../stores/auth'
import PageHeader from '../components/PageHeader.vue'
import DataState from '../components/DataState.vue'
import StatusTag from '../components/StatusTag.vue'
import { createAgent, getAgents, updateAgent, type Agent } from '../api/client'

const { t } = useI18n()
const auth = useAuthStore()
const items = ref<Agent[]>([])
const loading = ref(false)
const error = ref(false)
const saving = ref(false)
const createOpen = ref(false)
const createName = ref('')
const createEnabled = ref(true)
const editOpen = ref(false)
const editName = ref('')
const editEnabled = ref(true)
const editing = ref<Agent | null>(null)
const formatDate = useFormatDateTime()
const pageSize = 20
const keyword = ref('')
const nextCursor = ref('')
// Cursor paging has no page number on the wire, so the view keeps the cursor it
// used for each reachable page. That is what makes 上一页 an exact repeat of the
// earlier request instead of a second, unfiltered scan.
const pageCursors = ref([''])
const pageIndex = ref(0)

// The API pages agents newest-first, so the table only has to reorder the rows
// already on this page. Rows without a usable timestamp sort last rather than
// pretending to be the oldest registration.
function createdTimestamp(value?: string) {
  const parsed = value ? Date.parse(value) : NaN
  return Number.isNaN(parsed) ? -1 : parsed
}
function byCreatedAt(left: Agent, right: Agent) {
  return createdTimestamp(left.createdAt) - createdTimestamp(right.createdAt)
}
function byUpdatedAt(left: Agent, right: Agent) {
  return createdTimestamp(left.updatedAt) - createdTimestamp(right.updatedAt)
}

async function load() {
  loading.value = true; error.value = false
  // An absent filter or cursor is left out of the request rather than sent as an
  // empty value, so the server sees the same query it would see from the CLI.
  const params: { limit: number; keyword?: string; cursor?: string } = { limit: pageSize }
  const trimmed = keyword.value.trim()
  if (trimmed) params.keyword = trimmed
  const cursor = pageCursors.value[pageIndex.value]
  if (cursor) params.cursor = cursor
  try {
    const page = await getAgents(params)
    items.value = page.items
    nextCursor.value = page.nextCursor || ''
  } catch {
    error.value = true
  } finally {
    loading.value = false
  }
}

function nextPage() {
  if (!nextCursor.value) return
  pageCursors.value[pageIndex.value + 1] = nextCursor.value
  pageIndex.value += 1
  void load()
}

function previousPage() {
  if (pageIndex.value === 0) return
  pageIndex.value -= 1
  void load()
}

// A new filter is a new result set: page two of the old keyword is meaningless,
// so the walk restarts at the first cursor.
function query() {
  pageCursors.value = ['']
  pageIndex.value = 0
  void load()
}

function reset() {
  keyword.value = ''
  query()
}

async function create() {
  if (!createName.value.trim()) {
    ElMessage.warning(t('agents.nameRequired'))
    return
  }
  saving.value = true
  try {
    await createAgent({ name: createName.value.trim(), enabled: createEnabled.value })
    createOpen.value = false
    await load()
    ElMessage.success(t('agents.created'))
  } catch {
    ElMessage.error(t('agents.createFailed'))
  } finally {
    saving.value = false
  }
}

function resetCreateForm() {
  createName.value = ''
  createEnabled.value = true
}

function openEdit(row: Agent) {
  editing.value = row
  editName.value = row.name
  // Seeded from the row so the switch shows what is stored: a form that always
  // opened enabled would re-enable a node that was only meant to be renamed.
  editEnabled.value = row.enabled
  editOpen.value = true
}

function resetEditForm() {
  editing.value = null
  editName.value = ''
  editEnabled.value = true
}

async function saveEdit() {
  const agent = editing.value
  if (!agent) return
  const name = editName.value.trim()
  if (!name) {
    ElMessage.warning(t('agents.nameRequired'))
    return
  }
  const enabled = editEnabled.value
  if (name === agent.name && enabled === agent.enabled) {
    editOpen.value = false
    return
  }
  saving.value = true
  try {
    // Both fields go on every request: the dialog states the full intent it
    // shows, so a rename can never be interpreted as an implicit re-enable.
    await updateAgent(agent.id, { name, enabled })
    editOpen.value = false
    await load()
    ElMessage.success(t('agents.updated'))
  } catch {
    ElMessage.error(t('agents.updateFailed'))
  } finally {
    saving.value = false
  }
}

onMounted(load)
</script>

<style scoped>
.filter-form { margin-bottom: 18px; }
.filter-grid { display:grid; grid-template-columns:repeat(auto-fit,minmax(170px,1fr)); gap:12px; }
.filter-actions { display:flex; justify-content:flex-end; gap:8px; margin-top:4px; }
.pager { display: flex; align-items: center; justify-content: center; gap: 14px; padding: 18px 0 4px; }
.pager-label { color: var(--tm-muted); font-size: 13px; }
.hint { margin: 4px 0 0; color: var(--tm-muted); font-size: 12px; line-height: 1.5; flex-basis: 100%; }
</style>
