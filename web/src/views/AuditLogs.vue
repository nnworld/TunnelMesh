<template>
  <section class="tm-page">
    <PageHeader :title="t('audits.title')" :description="t('audits.description')" />
    <div class="tm-card filter-card">
      <el-form label-position="top" @submit.prevent="search">
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
        <el-table :data="items" row-key="id">
          <el-table-column :label="t('audits.time')" width="220">
            <template #default="{ row }">{{ formatDate(row.createdAt) }}</template>
          </el-table-column>
          <el-table-column prop="actorUserId" :label="t('audits.actor')" min-width="180" />
          <el-table-column prop="action" :label="t('audits.action')" min-width="180" />
          <el-table-column prop="resourceType" :label="t('audits.resourceType')" min-width="150" />
          <el-table-column prop="resourceId" :label="t('audits.resourceId')" min-width="200" />
          <el-table-column :label="t('audits.actions')" width="110" fixed="right">
            <template #default="{ row }">
              <el-button link type="primary" @click="showAuditDetail(row)">{{ t('audits.detail') }}</el-button>
            </template>
          </el-table-column>
        </el-table>
      </DataState>
    </div>

    <el-dialog v-model="detailVisible" :title="t('audits.detailTitle')" width="min(680px,94vw)">
      <el-descriptions v-if="detailAudit" :column="1" border>
        <el-descriptions-item :label="t('audits.time')">{{ formatDate(detailAudit.createdAt) }}</el-descriptions-item>
        <el-descriptions-item :label="t('audits.actor')">{{ detailAudit.actorUserId || '—' }}</el-descriptions-item>
        <el-descriptions-item :label="t('audits.action')">{{ detailAudit.action }}</el-descriptions-item>
        <el-descriptions-item :label="t('audits.resourceType')">{{ detailAudit.resourceType }}</el-descriptions-item>
        <el-descriptions-item :label="t('audits.resourceId')">{{ detailAudit.resourceId || '—' }}</el-descriptions-item>
      </el-descriptions>
      <div class="detail-json">
        <pre>{{ formatDetails(detailAudit?.details) }}</pre>
      </div>
    </el-dialog>
  </section>
</template>

<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import PageHeader from '../components/PageHeader.vue'
import DataState from '../components/DataState.vue'
import { listAuditLogs, type AuditLog } from '../api/client'
import { useFormatDateTime } from '../i18n/format'

const { t } = useI18n()
const items = ref<AuditLog[]>([])
const loading = ref(false)
const error = ref(false)
const detailVisible = ref(false)
const detailAudit = ref<AuditLog | null>(null)
const formatDate = useFormatDateTime()
const filters = reactive({
  createdRange: [] as string[],
  actorUserId: '',
  action: '',
  resourceType: '',
  resourceId: '',
})

async function load() {
  loading.value = true
  error.value = false
  try {
    items.value = (await listAuditLogs({ ...auditFilter(), limit: 200 })).items
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
  void load()
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
.filter-card {
  margin-bottom: 16px;
}

.filter-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
  gap: 4px 16px;
}

.filter-grid :deep(.el-date-editor) {
  width: 100%;
}

.filter-actions {
  display: flex;
  justify-content: flex-end;
  gap: 12px;
}

.detail-json {
  margin-top: 16px;
  overflow: auto;
  max-height: 360px;
  padding: 12px;
  border: 1px solid var(--el-border-color-light);
  border-radius: 8px;
  background: var(--el-fill-color-lighter);
}

.detail-json pre {
  margin: 0;
  font-size: 12px;
  line-height: 1.6;
  white-space: pre-wrap;
}
</style>
