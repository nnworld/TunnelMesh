<template>
  <el-skeleton v-if="loading" :rows="rows" animated />
  <ErrorState v-else-if="error" :label="errorLabel ?? ''" :retry-label="retryLabel" @retry="$emit('retry')" />
  <EmptyState v-else-if="empty" :label="emptyLabel ?? ''" />
  <slot v-else />
</template>
<script setup lang="ts">
import { ElSkeleton } from 'element-plus'
import EmptyState from './EmptyState.vue'
import ErrorState from './ErrorState.vue'
// unplugin-vue-components only injects styles for components it resolves from
// templates, so the skeleton is imported explicitly and its style entry must be
// imported here too. The alert, button and empty entries stay for the views
// that still render those components next to DataState.
import 'element-plus/es/components/alert/style/css'
import 'element-plus/es/components/button/style/css'
import 'element-plus/es/components/empty/style/css'
import 'element-plus/es/components/skeleton/style/css'
withDefaults(defineProps<{loading?: boolean; error?: boolean; empty?: boolean; rows?: number; errorLabel?: string; retryLabel?: string; emptyLabel?: string}>(), {rows: 4})
defineEmits<{(event: 'retry'): void}>()
</script>
