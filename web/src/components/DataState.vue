<template>
  <el-skeleton v-if="loading" :rows="rows" animated />
  <el-alert v-else-if="error" type="error" :title="errorLabel" show-icon>
    <el-button v-if="retryLabel" size="small" @click="$emit('retry')">{{ retryLabel }}</el-button>
  </el-alert>
  <el-empty v-else-if="empty" :description="emptyLabel" />
  <slot v-else />
</template>
<script setup lang="ts">
import { ElAlert, ElButton, ElEmpty, ElSkeleton } from 'element-plus'
// unplugin-vue-components only injects styles for components it resolves from
// templates. These four are imported explicitly, so their style entries must be
// imported here as well: without el-empty.css the empty-state SVG has no
// 100px image box and no fill variables, and list pages render a page-sized
// black shape instead of the illustration.
import 'element-plus/es/components/alert/style/css'
import 'element-plus/es/components/button/style/css'
import 'element-plus/es/components/empty/style/css'
import 'element-plus/es/components/skeleton/style/css'
withDefaults(defineProps<{loading?: boolean; error?: boolean; empty?: boolean; rows?: number; errorLabel?: string; retryLabel?: string; emptyLabel?: string}>(), {rows: 4})
defineEmits<{(event: 'retry'): void}>()
</script>
