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
withDefaults(defineProps<{loading?: boolean; error?: boolean; empty?: boolean; rows?: number; errorLabel?: string; retryLabel?: string; emptyLabel?: string}>(), {rows: 4})
defineEmits<{(event: 'retry'): void}>()
</script>
