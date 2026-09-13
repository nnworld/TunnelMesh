<template><el-tag :type="kind" disable-transitions>{{ label }}</el-tag></template>
<script setup lang="ts">
import { ElTag } from 'element-plus'
// ElTag is imported explicitly, so its style entry must be imported here too;
// otherwise routes whose chunk never uses <el-tag> in a template render
// unstyled tags.
import 'element-plus/es/components/tag/style/css'
defineProps<{kind: 'success'|'warning'|'info'|'danger'; label: string}>()
</script>
