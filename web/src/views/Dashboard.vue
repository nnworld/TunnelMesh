<template>
  <section class="tm-page">
    <PageHeader :title="t('dashboard.title')" :description="t('dashboard.description')" />
    <DataState :loading="loading" :error="error" :error-label="t('dashboard.unavailable')" :retry-label="t('dashboard.retry')" @retry="retry">
      <template v-if="summary">
        <div class="stats">
          <div v-for="item in stats" :key="item.label" class="tm-card stat"><span>{{ item.label }}</span><strong>{{ item.value }}</strong></div>
        </div>
        <div class="tm-card events">
          <h3>{{ t('dashboard.recentEvents') }}</h3>
          <el-empty v-if="!summary.recentEvents.length" :description="t('dashboard.noEvents')" />
          <div v-for="event in summary.recentEvents" :key="event.id" class="event"><code>{{ event.action }}</code><span>{{ event.resourceType }}</span><time>{{ formatDate(event.createdAt) }}</time></div>
        </div>
      </template>
    </DataState>
  </section>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import PageHeader from '../components/PageHeader.vue'
import DataState from '../components/DataState.vue'
import { getDashboardSummary, type DashboardSummary } from '../api/client'
import { useFormatDateTime } from '../i18n/format'

const { t } = useI18n()
const loading = ref(true)
const error = ref(false)
const summary = ref<DashboardSummary | null>(null)
const formatDate = useFormatDateTime()
const stats = computed(() => summary.value ? [
  { label: t('dashboard.agents'), value: summary.value.agentsTotal },
  { label: t('dashboard.online'), value: summary.value.agentsOnline },
  { label: t('dashboard.tunnels'), value: summary.value.activeTunnels },
  { label: t('dashboard.routes'), value: summary.value.managedRoutes },
  { label: t('dashboard.tokens'), value: summary.value.validServiceTokens },
] : [])

async function retry() {
  loading.value = true; error.value = false
  try { summary.value = await getDashboardSummary() }
  catch { error.value = true; summary.value = null }
  finally { loading.value = false }
}

onMounted(retry)
</script>

<style scoped>
.stats { display: grid; grid-template-columns: repeat(5, minmax(0, 1fr)); gap: 14px; }
.stat { padding: 18px; } .stat span { color: var(--tm-muted); font-size: 13px; } .stat strong { display: block; margin-top: 8px; font-size: 28px; }
.events { padding: 18px; } .events h3 { margin: 0 0 12px; }
.event { display: grid; grid-template-columns: 1fr 1fr auto; gap: 16px; padding: 10px 0; border-top: 1px solid #edf1f7; } .event time { color: var(--tm-muted); }
@media (max-width: 900px) { .stats { grid-template-columns: repeat(2, 1fr); } }
@media (max-width: 520px) { .stats { grid-template-columns: 1fr; } .event { grid-template-columns: 1fr; } }
</style>
