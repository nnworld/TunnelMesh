<template>
  <header class="page-header">
    <div>
      <el-breadcrumb>
        <el-breadcrumb-item v-for="crumb in breadcrumbs" :key="crumb.key">{{ t(crumb.key) }}</el-breadcrumb-item>
      </el-breadcrumb>
      <h1>{{ title }}</h1>
      <p v-if="description">{{ description }}</p>
    </div>
    <div class="page-actions"><slot /></div>
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
.page-header { display: flex; align-items: flex-end; justify-content: space-between; gap: var(--tm-s-4); padding-bottom: var(--tm-s-2); }
.page-header h1 { margin: 0; color: var(--tm-ink); font-size: var(--tm-fs-xl); font-weight: 600; line-height: var(--tm-lh-xl); letter-spacing: -0.015em; }
.page-header p { margin: var(--tm-s-1) 0 0; max-width: 72ch; color: var(--tm-ink-2); font-size: var(--tm-fs-sm); line-height: var(--tm-lh-sm); }
.page-actions { display: flex; flex-wrap: wrap; align-items: center; gap: var(--tm-s-2); }
.page-header :deep(.el-breadcrumb) { margin-bottom: var(--tm-s-2); }
@media (max-width: 600px) { .page-header { align-items: stretch; flex-direction: column; } }
</style>
