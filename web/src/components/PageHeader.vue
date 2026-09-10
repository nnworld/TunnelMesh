<template>
  <header class="page-header">
    <div>
      <el-breadcrumb>
        <el-breadcrumb-item v-for="crumb in breadcrumbs" :key="crumb.key">{{ t(crumb.key) }}</el-breadcrumb-item>
      </el-breadcrumb>
      <h1>{{ title }}</h1>
      <p v-if="description">{{ description }}</p>
    </div>
    <div><slot /></div>
  </header>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { ElBreadcrumb, ElBreadcrumbItem } from 'element-plus'
import 'element-plus/es/components/breadcrumb/style/css'
import 'element-plus/es/components/breadcrumb-item/style/css'
import { useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { breadcrumbsFor } from '../layouts/breadcrumbs'

defineProps<{ title: string; description?: string }>()
const route = useRoute()
const { t } = useI18n()
const breadcrumbs = computed(() => breadcrumbsFor(route.path))
</script>

<style scoped>
.page-header { display: flex; align-items: flex-start; justify-content: space-between; gap: 16px; }
.page-header h1 { margin: 0; font-size: 22px; line-height: 32px; }
.page-header p { margin: 4px 0 0; color: var(--tm-muted); font-size: 14px; }
.page-header :deep(.el-breadcrumb) { margin-bottom: 8px; }
@media (max-width: 600px) { .page-header { align-items: stretch; flex-direction: column; } }
</style>
