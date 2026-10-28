<template>
  <section class="tm-page">
    <PageHeader :title="t('dashboard.title')" :description="t('dashboard.description')" />
    <DataState :loading="loading" :error="error" :error-label="t('dashboard.unavailable')" :retry-label="t('dashboard.retry')" @retry="retry">
      <template v-if="summary">
        <MetricStrip :items="stats" />
        <div class="tm-card patch">
          <div class="events-head">
            <h3>{{ t('dashboard.patchTitle') }}</h3>
            <el-button link type="primary" @click="router.push('/agents')">{{ t('dashboard.patchOpen') }}</el-button>
          </div>
          <ErrorState v-if="agentsFailed" :label="t('dashboard.patchFailed')" />
          <EmptyState v-else-if="agentsLoaded && !agents.length" :label="t('dashboard.patchEmpty')" />
          <PatchPanel v-else-if="agents.length" :agents="agents" :total="summary.agentsTotal" :online="summary.agentsOnline" />
        </div>
        <div class="tm-card events">
          <div class="events-head">
            <h3>{{ t('dashboard.recentEvents') }}</h3>
            <!-- /audit-logs is admin-only and the router guard bounces anyone else back to
                 this page, so the link is only an exit for accounts that can use it. -->
            <el-button v-if="auth.isAdmin" link type="primary" @click="router.push('/audit-logs')">{{ t('dashboard.viewAudits') }}</el-button>
          </div>
          <EmptyState v-if="!summary.recentEvents.length" :label="t('dashboard.noEvents')" />
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
import MetricStrip from '../components/MetricStrip.vue'
import PatchPanel from '../components/PatchPanel.vue'
import EmptyState from '../components/EmptyState.vue'
import ErrorState from '../components/ErrorState.vue'
import { getAgents, getDashboardSummary, type Agent, type DashboardSummary } from '../api/client'
import { useFormatDateTime } from '../i18n/format'
import { auditActorLabel, auditDetailsSummary, auditDetailsTitle } from '../utils/audit'
import { useAuthStore } from '../stores/auth'

const { t } = useI18n()
const router = useRouter()
const auth = useAuthStore()
const loading = ref(true)
const error = ref(false)
const summary = ref<DashboardSummary | null>(null)
const formatDate = useFormatDateTime()
// The patch panel is a sample of the fleet: the newest agents (the list endpoint
// cannot sort by name), ordered by name for scanning. It loads on its own so a failure
// here never hides the metrics or the event list.
const PATCH_LIMIT = 32
const agents = ref<Agent[]>([])
const agentsLoaded = ref(false)
const agentsFailed = ref(false)
async function loadAgents() {
  agentsFailed.value = false
  try { agents.value = [...((await getAgents({ limit: PATCH_LIMIT })).items ?? [])].sort((a, b) => a.name.localeCompare(b.name)) }
  catch { agentsFailed.value = true; agents.value = [] }
  finally { agentsLoaded.value = true }
}
const stats = computed(() => summary.value ? [
  { label: t('dashboard.agents'), value: summary.value.agentsTotal, to: '/agents' },
  { label: t('dashboard.online'), value: summary.value.agentsOnline, to: '/agents' },
  { label: t('dashboard.tunnels'), value: summary.value.activeTunnels },
  { label: t('dashboard.routes'), value: summary.value.managedRoutes, to: '/routes' },
  { label: t('dashboard.tokens'), value: summary.value.validServiceTokens, to: '/tokens' },
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

onMounted(() => { void retry(); void loadAgents() })
</script>

<style scoped>
.patch, .events { padding: var(--tm-s-5); }
.events-head { display: flex; align-items: center; justify-content: space-between; gap: var(--tm-s-3); margin-bottom: var(--tm-s-4); }
.events-head h3 { margin: 0; font-size: var(--tm-fs-md); font-weight: 600; }
/* minmax(0,1fr) keeps the row from being widened by a long resource id or details
   string, which is what used to push a page-level scrollbar. */
.event { display: grid; grid-template-columns: minmax(0, 1fr); gap: var(--tm-s-1); padding: var(--tm-s-3) 0; border-top: 1px solid var(--tm-line); }
.event-main, .event-meta { display: flex; align-items: baseline; gap: var(--tm-s-4); min-width: 0; }
.event-main { justify-content: flex-start; }
.event-main time { margin-left: auto; color: var(--tm-ink-3); font-size: var(--tm-fs-xs); white-space: nowrap; }
.event-action { font-family: var(--tm-font-mono); font-size: var(--tm-fs-sm); font-weight: 500; color: var(--tm-ink); }
.event-resource { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--tm-ink-2); font-size: var(--tm-fs-sm); }
.event-actor { color: var(--tm-ink-2); font-size: var(--tm-fs-xs); white-space: nowrap; }
.event-details { min-width: 0; color: var(--tm-ink-3); font-family: var(--tm-font-mono); font-size: var(--tm-fs-xs); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
@media (max-width: 520px) { .event-main, .event-meta { flex-wrap: wrap; } .event-main time { margin-left: 0; } }
</style>
