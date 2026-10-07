<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAboutStore } from '../stores/about'
import { useRoutingStore } from '../stores/routing'
import { useStatsStore } from '../stores/stats'
import { describe } from '../stores/settings'

const { t } = useI18n()
const about = useAboutStore()
const routing = useRoutingStore()
const stats = useStatsStore()
const failure = ref('')

const buildRows = computed(() => {
  const view = about.view
  return [
    { label: t('about.build.version'), value: view?.version ?? '' },
    { label: t('about.build.commit'), value: view?.commit ?? '' },
    { label: t('about.build.buildTime'), value: view?.buildTime ?? '' },
  ]
})

const systemRows = computed(() => {
  const view = about.view
  const rows = [
    { label: t('about.system.os'), value: view?.system.goos ?? '' },
    { label: t('about.system.osVersion'), value: view?.system.osVersion ?? t('common.unknown') },
    { label: t('about.system.arch'), value: view?.system.arch ?? '' },
  ]
  // Only named when the shell reports one: a tray built before the renderer existed has
  // nothing to say here, and a row of "unknown" would read as a fault rather than as an
  // older build.
  if (view?.system.renderer) {
    rows.push({ label: t('about.system.renderer'), value: view.system.renderer })
  }
  if (view?.system.rendererDetail) {
    rows.push({ label: t('about.system.rendererDetail'), value: view.system.rendererDetail })
  }
  return rows
})

// degradedRenderer is the Windows case where no WebView2 runtime was found and the shell
// opened the page in the system browser instead. It is worth a line of its own because the
// operator can fix it, and nothing else in the window hints that it exists.
const degradedRenderer = computed(() => about.view?.system.renderer === 'browser')

const fileRows = computed(() => {
  const view = about.view
  return [
    { label: t('about.files.configDir'), value: view?.configDir ?? '' },
    { label: t('about.files.clientConfig'), value: view?.clientConfigPath ?? '' },
    { label: t('about.files.prefs'), value: view?.prefsPath ?? '' },
    { label: t('about.files.lock'), value: view?.lockPath ?? '' },
    { label: t('about.files.instanceId'), value: view?.instanceId ?? t('common.unknown') },
    { label: t('about.files.instanceIdPath'), value: view?.instanceIdPath ?? '' },
  ]
})

const summaryRows = computed(() => {
  const view = about.view
  return [
    { label: t('about.summary.mode'), value: view?.mode ?? routing.mode },
    { label: t('about.summary.serverUrl'), value: view?.serverUrl || t('common.none') },
    { label: t('about.summary.tunnels'), value: String(view?.tunnelCount ?? routing.tunnels.length) },
    {
      label: t('about.summary.token'),
      value: view?.tokenPresent ? t('about.summary.tokenPresent') : t('about.summary.tokenAbsent'),
    },
    {
      label: t('about.summary.state'),
      value: stats.running ? t('app.status.running') : t('app.status.stopped'),
    },
  ]
})

async function open(target: 'open-website' | 'open-releases' | 'open-docs') {
  failure.value = ''
  try {
    await about.open(target)
  } catch (cause) {
    failure.value = t('about.openFailed', { error: describe(cause) })
  }
}

onMounted(() => {
  // The store records the failure for display; rethrowing into an unhandled promise
  // would surface as a console error in the webview for a routine offline start.
  if (!about.view) about.load().catch(() => undefined)
})
</script>

<template>
  <section class="tm-view" data-test="about-view">
    <header class="tm-view-head">
      <h2>{{ t('about.title') }}</h2>
      <el-button size="small" data-test="check-updates" @click="open('open-releases')">
        {{ t('about.links.releases') }}
      </el-button>
    </header>

    <el-alert
      v-if="degradedRenderer"
      type="warning"
      show-icon
      :closable="false"
      :title="t('about.renderer.fallbackTitle')"
      :description="t('about.renderer.fallbackBody')"
      data-test="renderer-fallback"
    />

    <el-descriptions :title="t('about.build.heading')" :column="3" border size="small" data-test="build-info">
      <el-descriptions-item v-for="row in buildRows" :key="row.label" :label="row.label">
        <code>{{ row.value || '—' }}</code>
      </el-descriptions-item>
    </el-descriptions>

    <el-descriptions :title="t('about.system.heading')" :column="3" border size="small">
      <el-descriptions-item v-for="row in systemRows" :key="row.label" :label="row.label">
        <code>{{ row.value || '—' }}</code>
      </el-descriptions-item>
    </el-descriptions>

    <el-descriptions :title="t('about.summary.heading')" :column="2" border size="small" data-test="config-summary">
      <el-descriptions-item v-for="row in summaryRows" :key="row.label" :label="row.label">
        <code>{{ row.value || '—' }}</code>
      </el-descriptions-item>
    </el-descriptions>

    <el-descriptions :title="t('about.files.heading')" :column="1" border size="small">
      <el-descriptions-item v-for="row in fileRows" :key="row.label" :label="row.label">
        <code class="tm-path">{{ row.value || '—' }}</code>
      </el-descriptions-item>
    </el-descriptions>

    <el-card shadow="never" class="tm-card">
      <template #header><span>{{ t('about.links.heading') }}</span></template>
      <div class="tm-links">
        <el-button link type="primary" data-test="open-website" @click="open('open-website')">
          {{ t('about.links.website') }}
        </el-button>
        <el-button link type="primary" data-test="open-docs" @click="open('open-docs')">
          {{ t('about.links.docs') }}
        </el-button>
        <span class="tm-hint">
          {{ t('about.links.license') }}: <code>{{ about.view?.license ?? 'Apache-2.0' }}</code>
        </span>
      </div>
      <p class="tm-hint">{{ t('about.links.notice') }}</p>
      <el-alert v-if="failure" type="error" :closable="false" :title="failure" data-test="about-error" />
    </el-card>
  </section>
</template>

<style scoped>
.tm-view {
  display: flex;
  flex-direction: column;
  gap: 14px;
}
.tm-view-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}
.tm-view-head h2 {
  margin: 0;
  font-size: 16px;
}
.tm-card :deep(.el-card__header) {
  padding: 10px 14px;
  font-size: 13px;
  font-weight: 600;
}
.tm-links {
  display: flex;
  align-items: center;
  gap: 16px;
  flex-wrap: wrap;
}
.tm-path {
  word-break: break-all;
}
.tm-hint {
  margin: 6px 0 0;
  font-size: 12px;
  color: var(--el-text-color-secondary);
}
</style>
