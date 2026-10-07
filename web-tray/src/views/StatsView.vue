<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { formatBytes, formatTimeOfDay, formatUptime } from '../format'
import { useStatsStore } from '../stores/stats'

const { t } = useI18n()
const stats = useStatsStore()

const summary = computed(() => {
  const view = stats.view
  return [
    {
      key: 'state',
      label: t('stats.summary.state'),
      value: view?.running ? t('app.status.running') : t('app.status.stopped'),
    },
    { key: 'uptime', label: t('stats.summary.uptime'), value: formatUptime(view?.uptimeSeconds ?? 0) },
    { key: 'server', label: t('stats.summary.server'), value: serverText.value },
    {
      key: 'connected',
      label: t('stats.summary.connected'),
      value: view?.connected ? t('stats.session.connected') : t('stats.session.disconnected'),
    },
    { key: 'reconnects', label: t('stats.summary.reconnects'), value: String(stats.totals?.reconnects ?? 0) },
    { key: 'tunnels', label: t('stats.summary.tunnels'), value: String(stats.totals?.tunnels ?? 0) },
    { key: 'listening', label: t('stats.summary.listening'), value: String(stats.totals?.listening ?? 0) },
    { key: 'failed', label: t('stats.summary.failed'), value: String(stats.totals?.failed ?? 0) },
    { key: 'streams', label: t('stats.summary.activeStreams'), value: String(stats.totals?.activeStreams ?? 0) },
    { key: 'slots', label: t('stats.summary.openSlots'), value: String(stats.totals?.openSlots ?? 0) },
    { key: 'sessions', label: t('stats.summary.readySessions'), value: String(stats.totals?.readySessions ?? 0) },
    {
      key: 'bytes',
      label: t('stats.summary.bytesInbound'),
      value: formatBytes(stats.totals?.bytesInbound ?? 0),
    },
  ]
})

const serverText = computed(() => {
  const view = stats.view
  if (!view?.serverUrl) return t('stats.server.unset')
  if (!view.collectedAt) return t('stats.server.unknown')
  return view.serverReachable ? t('stats.server.reachable') : t('stats.server.unreachable')
})

const serverTagType = computed(() => {
  const view = stats.view
  if (!view?.serverUrl) return 'info'
  return view.serverReachable ? 'success' : 'danger'
})

function stateTag(state: string) {
  switch (state) {
    case 'listening':
      return 'success'
    case 'failed':
      return 'danger'
    case 'starting':
      return 'warning'
    default:
      return 'info'
  }
}

function stateLabel(state: string) {
  const known = ['starting', 'listening', 'failed', 'stopped']
  return known.includes(state) ? t(`stats.state.${state}`) : state
}
</script>

<template>
  <section class="tm-view" data-test="stats-view">
    <header class="tm-view-head">
      <div>
        <h2>{{ t('stats.title') }}</h2>
        <p>{{ t('stats.description') }}</p>
      </div>
      <div class="tm-tools">
        <el-switch
          :model-value="stats.autoRefresh"
          size="small"
          data-test="auto-refresh"
          @update:model-value="(value: any) => stats.setAutoRefresh(Boolean(value))"
        />
        <span class="tm-hint">{{ t('stats.autoRefresh') }}</span>
        <el-button size="small" data-test="refresh-stats" @click="stats.refresh()">
          {{ t('app.actions.refresh') }}
        </el-button>
      </div>
    </header>

    <el-alert
      v-if="stats.lockBlocked"
      type="warning"
      :closable="false"
      :title="t('stats.lockBlocked')"
      data-test="lock-blocked"
    />
    <el-alert
      v-else-if="stats.runtimeError"
      type="error"
      :closable="false"
      :title="t('stats.runtimeError', { error: stats.runtimeError })"
      data-test="runtime-error"
    />

    <el-descriptions :column="3" border size="small" data-test="summary">
      <el-descriptions-item v-for="item in summary" :key="item.key" :label="item.label">
        <el-tag v-if="item.key === 'server'" :type="serverTagType" size="small" effect="plain">{{ item.value }}</el-tag>
        <template v-else>{{ item.value }}</template>
      </el-descriptions-item>
    </el-descriptions>
    <!-- The reachability answer is a probe, not a verdict: a proxy in front of the Server
         can refuse /health/ready while every tunnel works. Showing what it answered is
         what turns "可达" from a guess into a fact the operator can act on. -->
    <p v-if="stats.view?.serverProbe" class="tm-hint" data-test="stats-server-probe">
      {{ t('stats.server.probe', { detail: stats.view.serverProbe }) }}
    </p>

    <!--
      Plain tables rather than el-table: the statistics tab is a compact read-out in a
      window that is half the screen, it needs no sorting, virtual scrolling or column
      resizing, and el-table measures its container before painting a body. Rendering
      semantics directly keeps the rows present in the DOM on the first tick - both for
      the operator and for the tests that assert on them.
    -->
    <el-card shadow="never" class="tm-card">
      <template #header>
        <span>{{ t('stats.tunnels.heading') }}</span>
      </template>
      <table v-if="stats.tunnels.length" class="tm-table" data-test="tunnel-table">
        <thead>
          <tr>
            <th>{{ t('stats.tunnels.name') }}</th>
            <th>{{ t('stats.tunnels.protocol') }}</th>
            <th>{{ t('stats.tunnels.listen') }}</th>
            <th>{{ t('stats.tunnels.agent') }}</th>
            <th>{{ t('stats.tunnels.target') }}</th>
            <th>{{ t('stats.tunnels.state') }}</th>
            <th>{{ t('stats.tunnels.lastError') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="tunnel in stats.tunnels" :key="`${tunnel.name}-${tunnel.listen}`">
            <td>{{ tunnel.name }}</td>
            <td><code>{{ tunnel.protocol }}</code></td>
            <td><code>{{ tunnel.listen }}</code></td>
            <td><code>{{ tunnel.agentId }}</code></td>
            <td><code>{{ tunnel.target || '—' }}</code></td>
            <td>
              <el-tag :type="stateTag(tunnel.state)" size="small" effect="plain">
                {{ stateLabel(tunnel.state) }}
              </el-tag>
            </td>
            <td class="tm-error">{{ tunnel.lastError || '—' }}</td>
          </tr>
        </tbody>
      </table>
      <p v-else class="tm-hint" data-test="tunnels-empty">{{ t('stats.tunnels.empty') }}</p>
    </el-card>

    <el-card shadow="never" class="tm-card">
      <template #header>
        <span>{{ t('stats.pool.heading') }}</span>
      </template>
      <table v-if="stats.agents.length" class="tm-table" data-test="pool-table">
        <thead>
          <tr>
            <th>{{ t('stats.pool.agent') }}</th>
            <th>{{ t('stats.pool.openSlots') }}</th>
            <th>{{ t('stats.pool.readySessions') }}</th>
            <th>{{ t('stats.pool.activeStreams') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="agent in stats.agents" :key="agent.agentId">
            <td><code>{{ agent.agentId }}</code></td>
            <td>{{ agent.openSlots }}</td>
            <td>{{ agent.readySessions }}</td>
            <td>{{ agent.activeStreams }}</td>
          </tr>
        </tbody>
      </table>
      <p v-else class="tm-hint" data-test="pool-empty">{{ t('stats.pool.empty') }}</p>
    </el-card>

    <p class="tm-footnote">
      {{ stats.view ? t('stats.collectedAt', { time: formatTimeOfDay(stats.view.collectedAt) }) : '' }}
      <span v-if="stats.view && !stats.running" class="tm-error">{{ t('stats.notRunning') }}</span>
    </p>
  </section>
</template>

<style scoped>
.tm-view {
  display: flex;
  flex-direction: column;
  gap: 12px;
}
.tm-view-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 12px;
}
.tm-view-head h2 {
  margin: 0;
  font-size: 16px;
}
.tm-view-head p {
  margin: 4px 0 0;
  font-size: 12px;
  color: var(--el-text-color-secondary);
}
.tm-tools {
  display: flex;
  align-items: center;
  gap: 8px;
}
.tm-card :deep(.el-card__header) {
  padding: 10px 14px;
  font-size: 13px;
  font-weight: 600;
}
.tm-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 12px;
}
.tm-table th,
.tm-table td {
  padding: 6px 8px;
  text-align: left;
  border-bottom: 1px solid var(--el-border-color-lighter);
  vertical-align: middle;
}
.tm-table th {
  font-weight: 600;
  color: var(--el-text-color-secondary);
  white-space: nowrap;
}
.tm-table tbody tr:last-child td {
  border-bottom: none;
}
.tm-hint {
  margin: 0;
  font-size: 12px;
  color: var(--el-text-color-secondary);
}
.tm-error {
  font-size: 12px;
  color: var(--el-color-danger);
  word-break: break-all;
}
.tm-footnote {
  margin: 0;
  display: flex;
  gap: 12px;
  align-items: baseline;
  font-size: 12px;
  color: var(--el-text-color-secondary);
}
</style>
