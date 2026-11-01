<template>
  <section class="tm-page">
    <PageHeader :title="t('audits.title')" :description="t('audits.description')" />
    <div class="tm-card filter-card">
      <el-form class="filter-form" label-position="top" @submit.prevent="search">
        <div class="filter-grid">
          <el-form-item :label="t('audits.timeRange')">
            <el-date-picker
              v-model="filters.createdRange"
              type="datetimerange"
              value-format="YYYY-MM-DDTHH:mm:ssZ"
              clearable
            />
          </el-form-item>
          <el-form-item :label="t('audits.actor')">
            <el-input v-model="filters.actorUserId" clearable />
          </el-form-item>
          <el-form-item :label="t('audits.action')">
            <el-input v-model="filters.action" clearable />
          </el-form-item>
          <el-form-item :label="t('audits.resourceType')">
            <el-input v-model="filters.resourceType" clearable />
          </el-form-item>
          <el-form-item :label="t('audits.resourceId')">
            <el-input v-model="filters.resourceId" clearable />
          </el-form-item>
        </div>
        <div class="filter-actions">
          <el-button @click="resetFilters">{{ t('audits.reset') }}</el-button>
          <el-button type="primary" @click="search">{{ t('audits.query') }}</el-button>
        </div>
      </el-form>
    </div>
    <div v-if="activeFilters.length" class="applied-filters" data-test="applied-filters">
      <el-tag v-for="chip in activeFilters" :key="chip.key" closable disable-transitions @close="removeFilter(chip.key)">{{ chip.label }}: {{ chip.value }}</el-tag>
    </div>
    <div class="tm-card">
      <DataState
        :loading="loading"
        :error="error"
        :empty="!items.length"
        :error-label="t('common.loadFailed')"
        :retry-label="t('common.retry')"
        :empty-label="t('common.empty')"
        @retry="load"
      >
        <el-table :data="items" row-key="id" class="audit-table" @sort-change="onSortChange" @row-click="showAuditDetail">
          <el-table-column prop="createdAt" :label="t('audits.time')" width="220" sortable="custom">
            <template #default="{ row }">{{ formatDate(row.createdAt) }}</template>
          </el-table-column>
          <!-- The stored actor is an identifier; the name is resolved server-side
               over the rows this reader is already allowed to see. -->
          <el-table-column :label="t('audits.actor')" min-width="180">
            <template #default="{ row }">
              <div class="actor-name">{{ auditActorLabel(row, t('audits.actorSystem')) }}</div>
              <div v-if="row.actorUserId" class="actor-id">{{ row.actorUserId }}</div>
            </template>
          </el-table-column>
          <el-table-column prop="action" :label="t('audits.action')" min-width="240"><template #default="{ row }"><span class="action tone-chip" :class="`tone-${actionHue(row.action)}`">{{ row.action }}</span></template></el-table-column>
          <el-table-column prop="resourceType" :label="t('audits.resourceType')" min-width="150" />
          <el-table-column prop="resourceId" :label="t('audits.resourceId')" min-width="200"><template #default="{ row }"><IdCell v-if="row.resourceId" :value="row.resourceId" /></template></el-table-column>
          <!-- What changed, without opening a dialog for every row: the facts an
               action recorded, summarized, with the whole payload on hover. -->
          <el-table-column :label="t('audits.summary')" min-width="260">
            <template #default="{ row }">
              <span class="audit-summary" :title="auditDetailsTitle(row.details)">
                {{ auditDetailsSummary(row.details, summaryPairLimit) || t('audits.emptyDetails') }}
              </span>
            </template>
          </el-table-column>
          <el-table-column :label="t('audits.actions')" width="110" fixed="right">
            <template #default="{ row }">
              <el-button link type="primary" @click="showAuditDetail(row)">{{ t('audits.detail') }}</el-button>
            </template>
          </el-table-column>
        </el-table>
        <PagerBar :page-index="pageIndex" :has-previous="pageIndex > 0" :has-next="!!nextCursor" :page-size="pageSize" :loading="loading" @previous="previousPage" @next="nextPage" @size-change="changePageSize" />
      </DataState>
    </div>

    <DetailDrawer v-model="detailVisible" :title="t('audits.detailTitle')">
      <el-descriptions v-if="detailAudit" :column="1" border>
        <el-descriptions-item :label="t('audits.time')">{{ formatDate(detailAudit.createdAt) }}</el-descriptions-item>
        <el-descriptions-item :label="t('audits.actor')">
          {{ auditActorLabel(detailAudit, t('audits.actorSystem')) }}
          <template v-if="detailAudit.actorUserId"> · {{ detailAudit.actorUserId }}</template>
        </el-descriptions-item>
        <el-descriptions-item :label="t('audits.action')">{{ detailAudit.action }}</el-descriptions-item>
        <el-descriptions-item :label="t('audits.resourceType')">{{ detailAudit.resourceType }}</el-descriptions-item>
        <el-descriptions-item :label="t('audits.resourceId')">{{ detailAudit.resourceId || '—' }}</el-descriptions-item>
        <!-- The id that joins this row to the structured logs of the same request. -->
        <el-descriptions-item :label="t('audits.traceId')">{{ auditTraceId(detailAudit.details) || t('audits.noTrace') }}</el-descriptions-item>
      </el-descriptions>
      <CodeBlock class="detail-json" :code="formatDetails(detailAudit?.details)" />
    </DetailDrawer>
  </section>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import PageHeader from '../components/PageHeader.vue'
import DataState from '../components/DataState.vue'
import DetailDrawer from '../components/DetailDrawer.vue'
import CodeBlock from '../components/CodeBlock.vue'
import IdCell from '../components/IdCell.vue'
import PagerBar from '../components/PagerBar.vue'
import { listAuditLogs, type AuditLog } from '../api/client'
import { useFormatDateTime } from '../i18n/format'
import { auditActorLabel, auditDetailsSummary, auditDetailsTitle, auditTraceId } from '../utils/audit'
import { actionHue } from '../utils/actionHue'

// How many facts fit on a table row before the line is marked as truncated.
const summaryPairLimit = 3

const { t } = useI18n()
const items = ref<AuditLog[]>([])
const loading = ref(false)
const error = ref(false)
const detailVisible = ref(false)
const detailAudit = ref<AuditLog | null>(null)
const formatDate = useFormatDateTime()
// Applied conditions are echoed back as removable chips so a filtered list is
// never a mystery; removing one re-runs the search without it.
const FILTER_LABELS = { actorUserId: 'audits.actor', action: 'audits.action', resourceType: 'audits.resourceType', resourceId: 'audits.resourceId' } as const
const filters = reactive({
  createdRange: [] as string[],
  actorUserId: '',
  action: '',
  resourceType: '',
  resourceId: '',
})
const pageSize = ref(20)
const pageIndex = ref(0)
const pageCursors = ref([''])
const nextCursor = ref('')
const sort = ref('createdAt')
const order = ref<'asc' | 'desc'>('desc')

const applied = ref<Partial<Record<keyof typeof FILTER_LABELS, string>>>({})
const activeFilters = computed(() => (Object.keys(FILTER_LABELS) as Array<keyof typeof FILTER_LABELS>)
  .filter(key => applied.value[key])
  .map(key => ({ key, label: t(FILTER_LABELS[key]), value: applied.value[key] as string })))
function removeFilter(key: keyof typeof FILTER_LABELS) {
  filters[key] = ''
  search()
}

async function load() {
  loading.value = true
  error.value = false
  applied.value = { actorUserId: filters.actorUserId.trim(), action: filters.action.trim(), resourceType: filters.resourceType.trim(), resourceId: filters.resourceId.trim() }
  try {
    const page = await listAuditLogs({ ...auditFilter(), limit: pageSize.value, cursor: pageCursors.value[pageIndex.value], sort: sort.value, order: order.value })
    items.value = page.items
    nextCursor.value = page.nextCursor || ''
  } catch {
    error.value = true
  } finally {
    loading.value = false
  }
}

function auditFilter() {
  return {
    actorUserId: filters.actorUserId.trim() || undefined,
    action: filters.action.trim() || undefined,
    resourceType: filters.resourceType.trim() || undefined,
    resourceId: filters.resourceId.trim() || undefined,
    createdFrom: filters.createdRange[0],
    createdTo: filters.createdRange[1],
  }
}

function search() {
  pageCursors.value = ['']
  pageIndex.value = 0
  void load()
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

function changePageSize(size: number) {
  pageSize.value = size
  search()
}

function onSortChange(payload: { order?: 'ascending' | 'descending' | null }) {
  order.value = payload.order === 'ascending' ? 'asc' : 'desc'
  search()
}

function resetFilters() {
  filters.createdRange = []
  filters.actorUserId = ''
  filters.action = ''
  filters.resourceType = ''
  filters.resourceId = ''
  void load()
}

function showAuditDetail(row: AuditLog) {
  detailAudit.value = row
  detailVisible.value = true
}

function formatDetails(details?: AuditLog['details']) {
  if (!details || !Object.keys(details).length) return t('audits.emptyDetails')
  return JSON.stringify(details, null, 2)
}

onMounted(load)
</script>

<style scoped>
.filter-card { margin-bottom: var(--tm-s-4); }
.filter-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(220px, 1fr)); gap: var(--tm-s-1) var(--tm-s-4); }
.filter-grid :deep(.el-date-editor) { width: 100%; }
.filter-actions { display: flex; justify-content: flex-end; gap: var(--tm-s-3); }
.applied-filters { display: flex; flex-wrap: wrap; gap: var(--tm-s-2); }
.audit-table :deep(.el-table__row) { cursor: pointer; }
.actor-name { font-weight: 500; }
.action { white-space: nowrap; }
.actor-id, .audit-summary { color: var(--tm-ink-2); font-size: var(--tm-fs-xs); overflow-wrap: anywhere; }
.detail-json { max-height: 50vh; overflow: auto; }
</style>
