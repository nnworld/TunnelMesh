<template>
  <div class="pager-bar">
    <el-button v-if="!hideUnavailable || hasPrevious" data-test="pager-previous" :disabled="!hasPrevious || loading" @click="emit('previous')">{{ t('common.previousPage') }}</el-button>
    <span v-if="!hideUnavailable || hasPrevious || hasNext" class="pager-label">{{ t('common.page', { number: pageIndex + 1 }) }}</span>
    <el-button v-if="!hideUnavailable || hasNext" data-test="pager-next" :disabled="!hasNext || loading" @click="emit('next')">{{ t('common.nextPage') }}</el-button>
    <el-pagination class="pager-sizes" layout="sizes" :page-size="pageSize" :page-sizes="pageSizes" :disabled="loading" @update:page-size="emit('sizeChange', $event)" />
  </div>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'

// Cursor paging knows no total and no page jump, so only previous/next and the
// page size are offered. `hideUnavailable` drops a direction that cannot be
// taken instead of showing it disabled.
withDefaults(defineProps<{ pageIndex: number; hasPrevious: boolean; hasNext: boolean; pageSize: number; pageSizes?: number[]; loading?: boolean; hideUnavailable?: boolean }>(), { pageSizes: () => [20, 50, 100] })
const emit = defineEmits<{ previous: []; next: []; sizeChange: [size: number] }>()
const { t } = useI18n()
</script>

<style scoped>
.pager-bar { display: flex; align-items: center; justify-content: center; gap: var(--tm-s-3); padding: var(--tm-s-4) 0 0; }
/* Rows-per-page sits on the same line as the buttons, never wrapped below them. */
.pager-bar :deep(.el-pagination) { flex: none; padding: 0; }
.pager-label { color: var(--tm-ink-2); font-size: var(--tm-fs-sm); font-variant-numeric: tabular-nums; }
</style>
