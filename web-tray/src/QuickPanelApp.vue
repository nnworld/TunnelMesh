<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { initSecret, runAction } from './api/client'
import type { TunnelStat } from './api/types'
import { formatBytes, formatTimeOfDay, formatUptime } from './format'
import { describe, useSettingsStore } from './stores/settings'
import { useStatsStore } from './stores/stats'

/**
 * The compact menu-bar panel.
 *
 * It answers one question - "is it working, and is anything flowing" - and then hands
 * everything else to the settings window. That is why it polls the same /api/stats the
 * statistics tab does rather than keeping a second set of numbers: two sources would
 * disagree for the length of a polling interval, and the disagreement is what an operator
 * reads as the client being flaky.
 */
const { t, te } = useI18n()
const settings = useSettingsStore()
const stats = useStatsStore()

const actionError = ref('')

const statusType = computed(() => {
  if (stats.lockBlocked) return 'warning'
  if (stats.running) return 'success'
  return 'info'
})
const statusText = computed(() => {
  if (stats.lockBlocked) return t('stats.lockBlocked')
  if (stats.running) return t('app.status.running')
  return t('app.status.stopped')
})
const serverText = computed(() => {
  if (!stats.view?.serverUrl) return t('stats.server.unset')
  // A registered session is the strongest answer there is, and the panel has one line for
  // it. The health endpoint can be refused by a proxy in front of the Server while the
  // tunnels carry traffic, and "不可达" printed next to a running tunnel reads as a broken
  // client rather than as a blocked probe.
  if (stats.view?.connected) return t('stats.session.connected')
  return stats.view?.serverReachable ? t('stats.server.reachable') : t('stats.server.unreachable')
})
const totals = computed(() => stats.view?.totals)
const uptime = computed(() => formatUptime(stats.view?.uptimeSeconds ?? 0))
const tunnelSummary = computed(() => {
  const all = totals.value
  if (!all) return '—'
  return `${all.listening}/${all.tunnels}`
})
const bytesIn = computed(() => formatBytes(totals.value?.bytesInbound ?? 0))
const activeStreams = computed(() => totals.value?.activeStreams ?? 0)
const reconnects = computed(() => totals.value?.reconnects ?? 0)
const collectedAt = computed(() => formatTimeOfDay(stats.view?.collectedAt))
const tunnels = computed<TunnelStat[]>(() => stats.tunnels)
const error = computed(() => actionError.value || stats.error || stats.runtimeError || '')

// A state string the bundle has no translation for must render as itself rather than as a
// "stats.state.…" key, because the panel is mounted before the Server's vocabulary is known.
function stateText(state: string): string {
  const key = `stats.state.${state}`
  return te(key) ? t(key) : state
}
function stateType(state: string): 'success' | 'warning' | 'danger' | 'info' {
  if (state === 'listening') return 'success'
  if (state === 'failed') return 'danger'
  if (state === 'starting') return 'warning'
  return 'info'
}

async function act(action: 'show-window' | 'quit') {
  actionError.value = ''
  try {
    await runAction(action)
  } catch (cause) {
    actionError.value = describe(cause)
  }
}

async function toggle() {
  actionError.value = ''
  try {
    await stats.act(stats.running ? 'stop' : 'start')
  } catch (cause) {
    actionError.value = describe(cause)
  }
}

onMounted(async () => {
  initSecret()
  // The stored language and theme have to be applied here as well: the panel is its own
  // web view, so it inherits nothing from the settings window.
  await settings.load().catch(() => undefined)
  stats.beginPolling()
})

onBeforeUnmount(() => {
  stats.endPolling()
  settings.unsubscribeSystemTheme?.()
})
</script>

<template>
  <div class="tm-panel" data-test="quick-panel">
    <header class="tm-panel-head">
      <h1>{{ t('app.title') }}</h1>
      <el-tag :type="statusType" effect="dark" size="small" data-test="panel-status">{{ statusText }}</el-tag>
    </header>

    <dl class="tm-panel-facts">
      <div class="tm-fact">
        <dt>{{ t('stats.summary.server') }}</dt>
        <dd data-test="panel-server">{{ serverText }}</dd>
      </div>
      <div class="tm-fact">
        <dt>{{ t('stats.summary.uptime') }}</dt>
        <dd>{{ uptime }}</dd>
      </div>
      <div class="tm-fact">
        <dt>{{ t('stats.summary.tunnels') }}</dt>
        <dd>{{ tunnelSummary }}</dd>
      </div>
      <div class="tm-fact">
        <dt>{{ t('stats.summary.activeStreams') }}</dt>
        <dd>{{ activeStreams }}</dd>
      </div>
      <div class="tm-fact">
        <dt>{{ t('stats.summary.bytesInbound') }}</dt>
        <dd>{{ bytesIn }}</dd>
      </div>
      <div class="tm-fact">
        <dt>{{ t('stats.summary.reconnects') }}</dt>
        <dd>{{ reconnects }}</dd>
      </div>
    </dl>

    <section class="tm-panel-tunnels">
      <h2>{{ t('panel.tunnels') }}</h2>
      <p v-if="!tunnels.length" class="tm-panel-empty">{{ t('panel.empty') }}</p>
      <ul v-else>
        <li v-for="row in tunnels" :key="row.name" data-test="panel-tunnel">
          <span class="tm-tunnel-name">{{ row.name }}</span>
          <span class="tm-tunnel-meta">{{ row.protocol }} · {{ row.listen }}</span>
          <el-tag size="small" :type="stateType(row.state)">{{ stateText(row.state) }}</el-tag>
          <p v-if="row.lastError" class="tm-tunnel-error">{{ row.lastError }}</p>
        </li>
      </ul>
    </section>

    <el-alert v-if="error" class="tm-panel-alert" type="error" :closable="false" :title="error" data-test="panel-error" />

    <footer class="tm-panel-actions">
      <el-button size="small" type="primary" data-test="panel-open-main" @click="act('show-window')">
        {{ t('panel.openMain') }}
      </el-button>
      <el-button size="small" :loading="stats.acting" data-test="panel-toggle" @click="toggle">
        {{ stats.running ? t('app.actions.stop') : t('app.actions.start') }}
      </el-button>
      <el-button size="small" data-test="panel-quit" @click="act('quit')">{{ t('panel.quit') }}</el-button>
    </footer>

    <p v-if="collectedAt" class="tm-panel-foot">{{ t('panel.updated', { time: collectedAt }) }}</p>
  </div>
</template>

<style scoped>
.tm-panel {
  display: flex;
  flex-direction: column;
  gap: 10px;
  padding: 12px 14px 14px;
  font-size: 12px;
}
.tm-panel-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}
.tm-panel-head h1 {
  margin: 0;
  font-size: 13px;
  font-weight: 600;
}
.tm-panel-facts {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 6px 12px;
  margin: 0;
  padding: 8px 10px;
  border-radius: 8px;
  background: var(--el-fill-color-light);
}
.tm-fact {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 8px;
  min-width: 0;
}
.tm-fact dt {
  color: var(--el-text-color-secondary);
}
.tm-fact dd {
  margin: 0;
  font-variant-numeric: tabular-nums;
  font-weight: 600;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.tm-panel-tunnels h2 {
  margin: 0 0 4px;
  font-size: 12px;
  font-weight: 600;
  color: var(--el-text-color-secondary);
}
.tm-panel-tunnels ul {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 6px;
  max-height: 168px;
  overflow-y: auto;
}
.tm-panel-tunnels li {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto auto;
  align-items: center;
  gap: 6px;
  padding: 6px 8px;
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 6px;
}
.tm-tunnel-name {
  font-weight: 600;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.tm-tunnel-meta {
  grid-column: 1 / 3;
  color: var(--el-text-color-secondary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.tm-panel-tunnels li .el-tag {
  grid-row: 1;
  grid-column: 3;
}
.tm-tunnel-error {
  grid-column: 1 / -1;
  margin: 0;
  color: var(--el-color-danger);
  word-break: break-all;
}
.tm-panel-empty {
  margin: 0;
  color: var(--el-text-color-secondary);
}
.tm-panel-alert {
  margin: 0;
}
.tm-panel-actions {
  display: flex;
  gap: 6px;
}
.tm-panel-actions .el-button {
  flex: 1;
  margin-left: 0;
}
.tm-panel-foot {
  margin: 0;
  text-align: right;
  color: var(--el-text-color-placeholder);
}
</style>
