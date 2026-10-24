<template>
  <el-form class="filter-bar" label-position="top" @submit.prevent="emit('search')">
    <div class="filter-grid">
      <el-form-item v-for="field in primaryFields" :key="field.key" :label="field.label">
        <FilterFieldControl :field="field" :model-value="model[field.key]" @update:model-value="model[field.key] = $event" @commit="emit('search')" />
      </el-form-item>
      <el-form-item v-if="moreFields.length" :label="t('common.moreFilters')">
        <el-popover placement="bottom-end" :width="400" trigger="click">
          <template #reference>
            <el-button :type="activeMoreCount ? 'primary' : 'default'" plain>{{ t('common.moreFilters') }}<span v-if="activeMoreCount"> · {{ activeMoreCount }}</span></el-button>
          </template>
          <el-form label-position="top" class="filter-more" @submit.prevent="emit('search')">
            <el-form-item v-for="field in moreFields" :key="field.key" :label="field.label">
              <FilterFieldControl :field="field" :model-value="model[field.key]" @update:model-value="model[field.key] = $event" @commit="emit('search')" />
            </el-form-item>
          </el-form>
        </el-popover>
      </el-form-item>
      <div class="filter-actions">
        <el-button @click="emit('reset')">{{ t('common.reset') }}</el-button>
        <el-button type="primary" @click="emit('search')">{{ t('common.query') }}</el-button>
      </div>
    </div>
  </el-form>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import FilterFieldControl from './FilterFieldControl.vue'
import type { FilterField } from '../composables/useListQuery'

defineProps<{ model: Record<string, unknown>; primaryFields: FilterField[]; moreFields: FilterField[]; activeMoreCount: number }>()
const emit = defineEmits<{ (event: 'search'): void; (event: 'reset'): void }>()
const { t } = useI18n()
</script>

<style scoped>
.filter-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(190px, 1fr)); gap: 4px 16px; align-items: start; }
.filter-grid :deep(.el-date-editor), .filter-grid :deep(.el-select) { width: 100%; }
.filter-actions { display: flex; justify-content: flex-end; align-items: flex-end; gap: 8px; }
.filter-more { display: grid; gap: 4px 16px; grid-template-columns: repeat(auto-fit, minmax(200px, 1fr)); }
</style>
