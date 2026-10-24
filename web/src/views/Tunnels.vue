<template>
  <section class="tm-page">
    <PageHeader :title="t('tunnels.title')" />
    <div class="tm-card">
      <div class="toolbar">
        <el-input v-model="keyword" class="keyword-input" clearable :placeholder="t('tunnels.keyword')" @keyup.enter="reload" @clear="reload" />
        <el-button :loading="loading" @click="reload">{{ t('common.query') }}</el-button>
      </div>
      <DataState :loading="loading" :error="error" :empty="!items.length" :error-label="t('common.loadFailed')" :retry-label="t('common.retry')" :empty-label="t('common.empty')" @retry="load">
        <el-table :data="items" row-key="id" @sort-change="onSortChange">
          <el-table-column prop="protocol" :label="t('tunnels.protocol')" width="120" />
          <el-table-column prop="targetHost" :label="t('tunnels.target')" min-width="220" />
          <el-table-column prop="targetPort" :label="t('tunnels.targetPort')" width="100" />
          <el-table-column prop="status" :label="t('tunnels.status')" width="120" />
          <el-table-column prop="createdAt" :label="t('common.createdAt')" width="190" sortable="custom">
            <template #default="{ row }">{{ row.createdAt ? new Date(row.createdAt).toLocaleString() : '—' }}</template>
          </el-table-column>
        </el-table>
        <div class="list-pager">
          <el-button :disabled="pageIndex === 0 || loading" @click="previous">{{ t('common.previousPage') }}</el-button>
          <span>{{ t('common.page', { number: pageIndex + 1 }) }}</span>
          <el-button :disabled="!nextCursor || loading" @click="next">{{ t('common.nextPage') }}</el-button>
          <el-select v-model="pageSize" class="page-size" @change="reload">
            <el-option v-for="size in [20, 50, 100]" :key="size" :label="String(size)" :value="size" />
          </el-select>
        </div>
      </DataState>
    </div>
  </section>
</template>
<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import PageHeader from '../components/PageHeader.vue'
import DataState from '../components/DataState.vue'
import { listTunnels, type ManagedRoute } from '../api/client'
const { t } = useI18n()
const items = ref<ManagedRoute[]>([])
const loading = ref(false)
const error = ref(false)
const keyword = ref('')
const pageIndex = ref(0)
const pageSize = ref(20)
const pageCursors = ref([''])
const nextCursor = ref('')
const sort = ref('createdAt')
const order = ref<'asc' | 'desc'>('desc')

async function load() {
  loading.value = true; error.value = false
  try {
    const page = await listTunnels({ keyword: keyword.value.trim() || undefined, cursor: pageCursors.value[pageIndex.value], limit: pageSize.value, sort: sort.value, order: order.value })
    items.value = page.items
    nextCursor.value = page.nextCursor || ''
  } catch { error.value = true }
  finally { loading.value = false }
}
function reload() { pageIndex.value = 0; pageCursors.value = ['']; void load() }
function next() { if (!nextCursor.value) return; pageCursors.value[pageIndex.value + 1] = nextCursor.value; pageIndex.value += 1; void load() }
function previous() { if (!pageIndex.value) return; pageIndex.value -= 1; void load() }
function onSortChange(payload: { prop?: string; order?: 'ascending' | 'descending' | null }) {
  sort.value = payload.prop === 'createdAt' ? 'createdAt' : 'createdAt'
  order.value = payload.order === 'ascending' ? 'asc' : 'desc'
  reload()
}
onMounted(load)
</script>
<style scoped>.toolbar{display:flex;gap:12px;align-items:center;flex-wrap:wrap;padding:14px 16px;border-bottom:1px solid var(--tm-border)}.keyword-input{width:min(260px,100%)}.list-pager{display:flex;align-items:center;justify-content:center;gap:12px;padding:16px}.page-size{width:100px}</style>
