<template><el-tag :type="kind" disable-transitions>{{ label }}</el-tag></template>
<script setup lang="ts">
import { ElTag } from 'element-plus'
defineProps<{kind: 'success'|'warning'|'info'|'danger'; label: string}>()
</script>
