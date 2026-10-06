<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { initSecret } from './api/client'
import { useAboutStore } from './stores/about'
import { useRoutingStore } from './stores/routing'
import { useSettingsStore } from './stores/settings'
import { useStatsStore } from './stores/stats'
import AboutView from './views/AboutView.vue'
import GeneralView from './views/GeneralView.vue'
import RoutingView from './views/RoutingView.vue'
import StatsView from './views/StatsView.vue'

const { t } = useI18n()
const settings = useSettingsStore()
const routing = useRoutingStore()
const stats = useStatsStore()
const about = useAboutStore()

const activeTab = ref<'general' | 'stats' | 'routing' | 'about'>('general')
const bootstrapError = ref('')

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

async function act(action: 'start' | 'stop' | 'restart') {
  bootstrapError.value = ''
  try {
    await stats.act(action)
  } catch (cause) {
    bootstrapError.value = cause instanceof Error ? cause.message : String(cause)
  }
}

onMounted(async () => {
  // The secret arrives in the launch URL; capture it before the first API call.
  initSecret()
  // allSettled rather than all: a tray that answers one endpoint and not another still
  // has to render the tabs that did load, and Promise.all would leave the remaining
  // rejections unhandled.
  const results = await Promise.allSettled([settings.load(), routing.load(), about.load()])
  const failed = results.find((result) => result.status === 'rejected') as
    | PromiseRejectedResult
    | undefined
  if (failed) {
    bootstrapError.value = failed.reason instanceof Error ? failed.reason.message : String(failed.reason)
  }
  // Statistics poll for the whole window, not just the tab: the header badge has to
  // follow the client even while the operator reads the about page.
  stats.beginPolling()
})

onBeforeUnmount(() => {
  stats.endPolling()
  settings.unsubscribeSystemTheme?.()
})
</script>

<template>
  <div class="tm-shell">
    <header class="tm-header">
      <div class="tm-title">
        <h1>{{ t('app.title') }}</h1>
        <p>{{ t('app.tagline') }}</p>
      </div>
      <div class="tm-controls">
        <el-tag :type="statusType" effect="dark" data-test="status-tag">{{ statusText }}</el-tag>
        <el-button size="small" :disabled="stats.running || stats.acting" data-test="start-client" @click="act('start')">
          {{ t('app.actions.start') }}
        </el-button>
        <el-button size="small" :disabled="!stats.running || stats.acting" data-test="stop-client" @click="act('stop')">
          {{ t('app.actions.stop') }}
        </el-button>
        <el-button size="small" :disabled="!stats.running || stats.acting" data-test="restart-client" @click="act('restart')">
          {{ t('app.actions.restart') }}
        </el-button>
      </div>
    </header>

    <el-alert
      v-if="bootstrapError"
      class="tm-alert"
      type="error"
      :closable="false"
      :title="bootstrapError"
      data-test="bootstrap-error"
    />

    <el-tabs v-model="activeTab" class="tm-tabs" data-test="tabs">
      <el-tab-pane name="general" :label="t('app.tabs.general')">
        <GeneralView />
      </el-tab-pane>
      <el-tab-pane name="stats" :label="t('app.tabs.stats')">
        <StatsView />
      </el-tab-pane>
      <el-tab-pane name="routing" :label="t('app.tabs.routing')">
        <RoutingView />
      </el-tab-pane>
      <el-tab-pane name="about" :label="t('app.tabs.about')">
        <AboutView />
      </el-tab-pane>
    </el-tabs>
  </div>
</template>

<style scoped>
.tm-shell {
  display: flex;
  flex-direction: column;
  gap: 12px;
  min-height: 100vh;
  padding: 16px 20px 24px;
}
.tm-header {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}
.tm-title h1 {
  margin: 0;
  font-size: 18px;
  font-weight: 600;
}
.tm-title p {
  margin: 2px 0 0;
  font-size: 12px;
  color: var(--el-text-color-secondary);
}
.tm-controls {
  display: flex;
  align-items: center;
  gap: 8px;
}
.tm-alert {
  margin: 0;
}
</style>
