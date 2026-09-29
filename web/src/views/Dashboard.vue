<template>
  <section class="tm-page">
    <PageHeader :title="t('dashboard.title')" :description="t('dashboard.description')" />
    <DataState :loading="loading" :error="error" :error-label="t('dashboard.unavailable')" :retry-label="t('dashboard.retry')" @retry="retry">
      <template v-if="summary">
        <div class="stats">
          <div v-for="item in stats" :key="item.label" class="tm-card stat"><span>{{ item.label }}</span><strong>{{ item.value }}</strong></div>
        </div>
        <div class="tm-card events">
          <div class="events-head">
            <h3>{{ t('dashboard.recentEvents') }}</h3>
            <el-button link type="primary" @click="router.push('/audits')">{{ t('dashboard.viewAudits') }}</el-button>
          </div>
          <el-empty v-if="!summary.recentEvents.length" :description="t('dashboard.noEvents')" />
          <article v-for="event in summary.recentEvents" :key="event.id" class="event">
            <div class="event-main">
              <code class="event-action">{{ event.action }}</code>
              <span class="event-resource">{{ event.resourceType }}<template v-if="event.resourceId"> · {{ event.resourceId }}</template></span>
              <time>{{ formatDate(event.createdAt) }}</time>
            </div>
            <div class="event-meta">
              <span class="event-actor">{{ auditActorLabel(event, t('dashboard.systemActor')) }}</span>
              <span v-if="auditDetailsSummary(event.details, detailPairLimit)" class="event-details" :title="auditDetailsTitle(event.details)">{{ auditDetailsSummary(event.details, detailPairLimit) }}</span>
            </div>
          </article>
        </div>
      </template>
    </DataState>
  </section>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import PageHeader from '../components/PageHeader.vue'
import DataState from '../components/DataState.vue'
import { getDashboardSummary, type DashboardSummary } from '../api/client'
import { useFormatDateTime } from '../i18n/format'
import { auditActorLabel, auditDetailsSummary, auditDetailsTitle } from '../utils/audit'

const { t } = useI18n()
const router = useRouter()
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

// The card has one line of room for the audit payload, so details are summarized
// rather than dumped as JSON. Three pairs is enough to tell two same-named
// actions apart, and the title attribute keeps the whole record readable. The
// formatting itself lives in utils/audit so the audit table says it the same way.
const detailPairLimit = 3

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
.events { padding: 18px; }
.events-head { display: flex; align-items: center; justify-content: space-between; gap: 12px; margin-bottom: 12px; } .events-head h3 { margin: 0; }
/* minmax(0,1fr) keeps the row from being widened by a long resource id or details
   string, which is what used to push a page-level scrollbar. */
.event { display: grid; grid-template-columns: minmax(0, 1fr); gap: 4px; padding: 10px 0; border-top: 1px solid #edf1f7; }
.event-main, .event-meta { display: flex; align-items: baseline; gap: 14px; min-width: 0; }
.event-main { justify-content: flex-start; }
.event-main time { margin-left: auto; color: var(--tm-muted); white-space: nowrap; }
.event-action { font-weight: 650; }
.event-resource { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.event-actor { color: var(--tm-muted); font-size: 12px; white-space: nowrap; }
.event-details { min-width: 0; color: var(--tm-muted); font-size: 12px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
@media (max-width: 900px) { .stats { grid-template-columns: repeat(2, 1fr); } }
@media (max-width: 520px) { .stats { grid-template-columns: 1fr; } .event-main, .event-meta { flex-wrap: wrap; } .event-main time { margin-left: 0; } }
</style>
