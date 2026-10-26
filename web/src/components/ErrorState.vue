<template>
  <div class="error-state" role="alert">
    <TmIcon name="alert" :size="20" class="error-icon" />
    <div class="error-body">
      <p class="error-label">{{ label }}</p>
      <p v-if="hint" class="error-hint">{{ hint }}</p>
    </div>
    <el-button v-if="retryLabel" size="small" @click="emit('retry')">{{ retryLabel }}</el-button>
  </div>
</template>

<script setup lang="ts">
import { ElButton } from 'element-plus'
import 'element-plus/es/components/button/style/css'
import TmIcon from './icons/TmIcon.vue'

defineProps<{ label: string; hint?: string; retryLabel?: string }>()
const emit = defineEmits<{ retry: [] }>()
</script>

<style scoped>
.error-state { display: flex; align-items: center; gap: var(--tm-s-3); padding: var(--tm-s-4); background: var(--tm-danger-tint); border-left: 3px solid var(--tm-danger); border-radius: var(--tm-r-sm); color: var(--tm-danger-ink); }
.error-icon { flex: none; }
.error-body { flex: 1; min-width: 0; }
.error-label { margin: 0; font-size: var(--tm-fs-sm); font-weight: 600; }
.error-hint { margin: var(--tm-s-1) 0 0; font-size: var(--tm-fs-xs); }
</style>
