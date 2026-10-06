import { defineStore } from 'pinia'
import { getSettings, saveSettings } from '../api/client'
import type { ClientMode, LanguagePreference, SettingsUpdate, SettingsView, ThemePreference } from '../api/types'
import { applyLanguagePreference } from '../i18n'
import { applyTheme, watchSystemTheme } from '../theme'

/**
 * The general tab's state.
 *
 * Language and theme are applied here rather than in the components so the effect is
 * identical whether the value came from the tray at start-up or from a save in this
 * window. The system-theme watcher is torn down and re-created on every change, because
 * an explicit light or dark choice must stop reacting to the OS.
 */
export const useSettingsStore = defineStore('settings', {
  state: () => ({
    view: null as SettingsView | null,
    loading: false,
    saving: false,
    error: '' as string,
    /** Draft edited by the form; committed by save(). */
    draft: {
      language: 'system' as LanguagePreference,
      theme: 'system' as ThemePreference,
      configDir: '',
      mode: 'local' as ClientMode,
      launchAtLogin: true,
      minimizeToTray: true,
    },
    unsubscribeSystemTheme: null as null | (() => void),
  }),
  getters: {
    loaded: (state) => state.view !== null,
    launchAtLoginSupported: (state) => state.view?.launchAtLoginSupported ?? false,
  },
  actions: {
    adopt(view: SettingsView) {
      this.view = view
      this.draft = {
        language: view.language,
        theme: view.theme,
        configDir: view.configDir,
        // The mode belongs to client.yaml, not to tray.json, so the routing store owns
        // the authoritative value; the general tab mirrors it for display and editing.
        mode: this.draft.mode,
        launchAtLogin: view.launchAtLogin,
        minimizeToTray: view.minimizeToTray,
      }
      this.applyAppearance()
    },
    applyAppearance() {
      applyLanguagePreference(this.draft.language)
      this.unsubscribeSystemTheme?.()
      this.unsubscribeSystemTheme = watchSystemTheme(this.draft.theme, () => {
        /* applyTheme already ran inside the watcher. */
      })
      applyTheme(this.draft.theme)
    },
    async load() {
      this.loading = true
      this.error = ''
      try {
        this.adopt(await getSettings())
      } catch (cause) {
        this.error = describe(cause)
        throw cause
      } finally {
        this.loading = false
      }
    },
    async save(extra: SettingsUpdate = {}) {
      this.saving = true
      this.error = ''
      try {
        const update: SettingsUpdate = {
          language: this.draft.language,
          theme: this.draft.theme,
          configDir: this.draft.configDir,
          launchAtLogin: this.draft.launchAtLogin,
          minimizeToTray: this.draft.minimizeToTray,
          ...extra,
        }
        this.adopt(await saveSettings(update))
      } catch (cause) {
        this.error = describe(cause)
        throw cause
      } finally {
        this.saving = false
      }
    },
  },
})

export function describe(cause: unknown): string {
  if (cause instanceof Error) return cause.message
  return String(cause)
}
