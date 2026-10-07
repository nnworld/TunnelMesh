<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import type { ClientMode, LanguagePreference, ThemePreference } from '../api/types'
import { applyLanguagePreference } from '../i18n'
import { applyTheme, watchSystemTheme } from '../theme'
import { useRoutingStore } from '../stores/routing'
import { describe, useSettingsStore } from '../stores/settings'
import { technicalInput } from '../textInput'
import { platformMessage } from '../platform'

const { t } = useI18n()
const settings = useSettingsStore()
const routing = useRoutingStore()

const saving = ref(false)
const notice = ref('')
const failure = ref('')

// A local mirror rather than a direct store binding: the appearance has to react the
// moment a radio button changes, and committing that to disk on every click would write
// tray.json for a choice the operator may still be browsing.
const language = ref<LanguagePreference>(settings.draft.language)
const theme = ref<ThemePreference>(settings.draft.theme)
const configDir = ref(settings.draft.configDir)
const launchAtLogin = ref(settings.draft.launchAtLogin)
const minimizeToTray = ref(settings.draft.minimizeToTray)
const quickPanel = ref(settings.draft.quickPanel)
const mode = ref<ClientMode>(routing.mode)

watch(
  () => settings.draft,
  (draft) => {
    language.value = draft.language
    theme.value = draft.theme
    configDir.value = draft.configDir
    launchAtLogin.value = draft.launchAtLogin
    minimizeToTray.value = draft.minimizeToTray
    quickPanel.value = draft.quickPanel
  },
  { deep: true, immediate: true },
)
watch(
  () => routing.mode,
  (value) => {
    if (value) mode.value = value
  },
  { immediate: true },
)

// Preview the appearance immediately so the operator sees the effect before saving. The
// system-theme watcher is re-created because an explicit choice must stop following the
// operating system.
let unsubscribe = watchSystemTheme(theme.value, () => {})
watch(language, (value) => applyLanguagePreference(value))
watch(theme, (value) => {
  unsubscribe()
  unsubscribe = watchSystemTheme(value, () => {})
  applyTheme(value)
})

const languageOptions = computed(() => [
  { value: 'system', label: t('general.language.system') },
  { value: 'zh-CN', label: t('general.language.zhCN') },
  { value: 'en-US', label: t('general.language.enUS') },
])
const themeOptions = computed(() => [
  { value: 'system', label: t('general.theme.system') },
  { value: 'light', label: t('general.theme.light') },
  { value: 'dark', label: t('general.theme.dark') },
])
const modeOptions = computed(() => [
  { value: 'local', label: t('general.mode.local') },
  { value: 'cluster', label: t('general.mode.cluster') },
])

// A two-way radio rather than a switch, because the requirement names both answers:
// "关闭" is the default and "开启" is the opt-in, and the tray's own menu behaviour differs
// between them. A switch would show one state and hide the other.
const quickPanelOptions = computed(() => [
  { value: false, label: t('general.quickPanel.off') },
  { value: true, label: t('general.quickPanel.on') },
])

// Nothing reaches disk until save() runs, so a clicked radio button changes the form and
// not the tray. That difference is invisible unless it is said: the quick panel is the
// clearest case, because the menu bar keeps behaving the way the stored value says.
const dirty = computed(() => {
  const draft = settings.draft
  return (
    language.value !== draft.language ||
    theme.value !== draft.theme ||
    configDir.value !== draft.configDir ||
    launchAtLogin.value !== draft.launchAtLogin ||
    minimizeToTray.value !== draft.minimizeToTray ||
    quickPanel.value !== draft.quickPanel ||
    mode.value !== routing.mode
  )
})

async function save() {
  saving.value = true
  notice.value = ''
  failure.value = ''
  settings.draft.language = language.value
  settings.draft.theme = theme.value
  settings.draft.configDir = configDir.value
  settings.draft.launchAtLogin = launchAtLogin.value
  settings.draft.minimizeToTray = minimizeToTray.value
  settings.draft.quickPanel = quickPanel.value
  try {
    await settings.save()
    notice.value = t('general.saved')
  } catch (cause) {
    failure.value = t('general.saveFailed', { error: describe(cause) })
    return
  } finally {
    saving.value = false
  }
  // The mode lives in client.yaml, so it is committed through the routing save. Doing it
  // after the preferences keeps a rejected mode from discarding an accepted preference.
  if (mode.value && mode.value !== routing.mode) {
    const previousMode = routing.mode
    saving.value = true
    try {
      routing.mode = mode.value
      const result = await routing.save()
      notice.value = result.restarted ? t('routing.save.restarted') : t('general.saved')
    } catch (cause) {
      // The save was rejected, so client.yaml still carries the previous mode and the
      // form has to agree with the file rather than with the operator's last click.
      routing.mode = previousMode
      mode.value = previousMode
      failure.value = t('routing.save.failed', { error: describe(cause) })
    } finally {
      saving.value = false
    }
  }
}
</script>

<template>
  <section class="tm-view" data-test="general-view">
    <header class="tm-view-head">
      <h2>{{ t('general.title') }}</h2>
      <p>{{ t('general.description') }}</p>
    </header>

    <el-form label-position="top" class="tm-form">
      <el-form-item :label="t('general.language.label')">
        <el-radio-group v-model="language" data-test="language-group">
          <el-radio-button v-for="option in languageOptions" :key="option.value" :value="option.value">
            {{ option.label }}
          </el-radio-button>
        </el-radio-group>
        <p class="tm-hint">{{ t('general.language.hint') }}</p>
      </el-form-item>

      <el-form-item :label="t('general.theme.label')">
        <el-radio-group v-model="theme" data-test="theme-group">
          <el-radio-button v-for="option in themeOptions" :key="option.value" :value="option.value">
            {{ option.label }}
          </el-radio-button>
        </el-radio-group>
        <p class="tm-hint">{{ t(platformMessage('general.theme.hint', settings.view?.platform)) }}</p>
      </el-form-item>

      <el-form-item :label="t('general.configDir.label')">
        <el-input
          v-model="configDir"
          :placeholder="t('general.configDir.placeholder')"
          data-test="config-dir"
          v-bind="technicalInput"
        />
        <p class="tm-hint">{{ t('general.configDir.hint') }}</p>
        <ul v-if="settings.view" class="tm-files">
          <li><span>{{ t('general.configDir.clientConfig') }}</span><code>{{ settings.view.clientConfigPath }}</code></li>
          <li><span>{{ t('general.configDir.prefs') }}</span><code>{{ settings.view.prefsPath }}</code></li>
          <li><span>{{ t('general.configDir.lock') }}</span><code>{{ settings.view.lockPath }}</code></li>
        </ul>
      </el-form-item>

      <el-form-item :label="t('general.mode.label')">
        <el-select v-model="mode" data-test="mode-select" style="width: 260px">
          <el-option v-for="option in modeOptions" :key="option.value" :label="option.label" :value="option.value" />
        </el-select>
        <p class="tm-hint">{{ t('general.mode.hint') }}</p>
      </el-form-item>

      <el-form-item>
        <div class="tm-switch">
          <el-switch
            v-model="launchAtLogin"
            :disabled="settings.view ? !settings.view.launchAtLoginSupported : false"
            data-test="launch-at-login"
          />
          <span>{{ t('general.launchAtLogin.label') }}</span>
        </div>
        <p class="tm-hint">{{ t(platformMessage('general.launchAtLogin.hint', settings.view?.platform)) }}</p>
        <el-alert
          v-if="settings.view && !settings.view.launchAtLoginSupported"
          type="info"
          :closable="false"
          :title="t('general.launchAtLogin.unsupported')"
        />
        <el-alert
          v-if="settings.view?.launchAtLoginError"
          type="warning"
          :closable="false"
          :title="t(platformMessage('general.launchAtLogin.failed', settings.view?.platform), {
            error: settings.view.launchAtLoginError,
          })"
          data-test="launch-at-login-error"
        />
      </el-form-item>

      <el-form-item>
        <div class="tm-switch">
          <el-switch v-model="minimizeToTray" data-test="minimize-to-tray" />
          <span>{{ t('general.minimizeToTray.label') }}</span>
        </div>
        <p class="tm-hint">{{ t('general.minimizeToTray.hint') }}</p>
      </el-form-item>

      <el-form-item :label="t('general.quickPanel.label')">
        <el-radio-group v-model="quickPanel" data-test="quick-panel-group">
          <el-radio-button v-for="option in quickPanelOptions" :key="String(option.value)" :value="option.value">
            {{ option.label }}
          </el-radio-button>
        </el-radio-group>
        <p class="tm-hint">{{ t(platformMessage('general.quickPanel.hint', settings.view?.platform)) }}</p>
      </el-form-item>
    </el-form>

    <footer class="tm-actions">
      <el-button type="primary" :loading="saving" data-test="save-general" @click="save">
        {{ saving ? t('app.actions.saving') : t('app.actions.save') }}
      </el-button>
      <p v-if="dirty" class="tm-unsaved" data-test="general-unsaved">{{ t('general.unsaved') }}</p>
      <el-alert v-if="notice" type="success" :closable="false" :title="notice" data-test="general-notice" />
      <el-alert v-if="failure" type="error" :closable="false" :title="failure" data-test="general-error" />
    </footer>
  </section>
</template>

<style scoped>
.tm-view {
  display: flex;
  flex-direction: column;
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
.tm-form :deep(.el-form-item) {
  margin-bottom: 18px;
}
.tm-hint {
  margin: 4px 0 0;
  font-size: 12px;
  color: var(--el-text-color-secondary);
}
.tm-files {
  margin: 8px 0 0;
  padding: 0;
  list-style: none;
  font-size: 12px;
}
.tm-files li {
  display: flex;
  gap: 8px;
  align-items: baseline;
}
.tm-files code {
  color: var(--el-text-color-regular);
  word-break: break-all;
}
.tm-switch {
  display: flex;
  align-items: center;
  gap: 8px;
}
.tm-actions {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
  /* The tab is taller than a half-screen window and the controls that need a save sit at
     the bottom of it. A footer that scrolls out of sight is how a changed radio button
     ends up looking like a setting that does nothing. */
  position: sticky;
  bottom: 0;
  padding: 8px 0;
  background: var(--el-bg-color);
}
.tm-unsaved {
  margin: 0;
  font-size: 12px;
  color: var(--el-color-warning);
}
</style>
