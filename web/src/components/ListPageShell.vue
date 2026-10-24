<template>
  <section class="tm-page">
    <PageHeader :title="title" :description="description"><slot name="toolbar" /></PageHeader>
    <div v-if="slots.filters" class="tm-card filter-card"><slot name="filters" /></div>
    <div class="tm-card">
      <DataState :loading="loading" :error="error" :empty="empty" :error-label="t('common.loadFailed')" :retry-label="t('common.retry')" :empty-label="t('common.empty')" @retry="emit('retry')">
        <slot name="table" />
        <div class="list-pager">
          <el-button :disabled="!hasPrevious || loading" @click="emit('previous')">{{ t('common.previousPage') }}</el-button>
          <span class="pager-label">{{ t('common.page', { number: pageIndex + 1 }) }}</span>
          <el-button :disabled="!hasNext || loading" @click="emit('next')">{{ t('common.nextPage') }}</el-button>
          <el-pagination class="pager-sizes" background layout="sizes" :page-size="pageSize" :page-sizes="pageSizes" :disabled="loading" @size-change="emit('sizeChange', $event)" />
        </div>
      </DataState>
    </div>
  </section>
</template>

<script setup lang="ts">
import { useSlots } from 'vue'
import { useI18n } from 'vue-i18n'
import PageHeader from './PageHeader.vue'
import DataState from './DataState.vue'

withDefaults(defineProps<{ title: string; description?: string; loading?: boolean; error?: boolean; empty?: boolean; pageIndex: number; hasPrevious: boolean; hasNext: boolean; pageSize: number; pageSizes?: number[] }>(), { pageSizes: () => [20, 50, 100] })
const emit = defineEmits<{ (event: 'retry'): void; (event: 'previous'): void; (event: 'next'): void; (event: 'sizeChange', size: number): void }>()
const slots = useSlots()
const { t } = useI18n()
</script>

<style scoped>
.filter-card { margin-bottom: 16px; }
.list-pager { display: flex; align-items: center; justify-content: center; gap: 14px; padding: 18px 0 4px; }
.pager-label { color: var(--tm-muted); font-size: 13px; }
.pager-sizes { margin-left: 4px; }
</style>
