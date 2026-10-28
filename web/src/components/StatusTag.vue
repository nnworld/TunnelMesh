<template>
  <el-tag :type="kind" class="status-tag" disable-transitions><i class="tag-led" aria-hidden="true" />{{ label }}</el-tag>
</template>
<script setup lang="ts">
import { ElTag } from 'element-plus'
// ElTag is imported explicitly, so its style entry must be imported here too;
// otherwise routes whose chunk never uses <el-tag> in a template render
// unstyled tags.
import 'element-plus/es/components/tag/style/css'
defineProps<{kind: 'success'|'warning'|'info'|'danger'; label: string}>()
</script>
<style scoped>
.status-tag :deep(.el-tag__content) { display: inline-flex; align-items: center; gap: 6px; }
.tag-led { flex: none; width: 6px; height: 6px; border-radius: 50%; background: currentColor; }
/* info (offline, disabled-ish) is a hollow ring, matching StatusLed */
.status-tag.el-tag--info .tag-led { background: transparent; box-shadow: inset 0 0 0 1.25px currentColor; }
</style>
