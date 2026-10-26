<template>
  <div class="theme-toggle" role="radiogroup" :aria-label="t('shell.theme')">
    <button
      v-for="option in options"
      :key="option.value"
      type="button"
      role="radio"
      :data-test="`theme-${option.value}`"
      :aria-checked="preferences.theme === option.value"
      :aria-label="t(option.labelKey)"
      :title="t(option.labelKey)"
      :tabindex="preferences.theme === option.value ? 0 : -1"
      :class="{ 'is-on': preferences.theme === option.value }"
      @click="preferences.setTheme(option.value)"
      @keydown="onKeydown($event, option.value)"
    >
      <TmIcon :name="option.icon" :size="16" />
    </button>
  </div>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import TmIcon from './icons/TmIcon.vue'
import { usePreferencesStore } from '../stores/preferences'

type Choice = 'light' | 'dark' | 'system'
const { t } = useI18n()
const preferences = usePreferencesStore()
const options: Array<{ value: Choice; icon: string; labelKey: string }> = [
  { value: 'light', icon: 'sun', labelKey: 'shell.themeLight' },
  { value: 'dark', icon: 'moon', labelKey: 'shell.themeDark' },
  { value: 'system', icon: 'monitor', labelKey: 'shell.themeSystem' },
]

// Radio groups move with the arrow keys; Tab leaves the group.
function onKeydown(event: KeyboardEvent, current: Choice) {
  const step = event.key === 'ArrowRight' || event.key === 'ArrowDown' ? 1 : event.key === 'ArrowLeft' || event.key === 'ArrowUp' ? -1 : 0
  if (!step) return
  event.preventDefault()
  const index = options.findIndex(option => option.value === current)
  const next = options[(index + step + options.length) % options.length]
  preferences.setTheme(next.value)
  ;(event.currentTarget as HTMLElement).parentElement?.querySelector<HTMLElement>(`[data-test="theme-${next.value}"]`)?.focus()
}
</script>

<style scoped>
.theme-toggle { display: inline-flex; padding: 2px; gap: 2px; background: var(--tm-sunken); border-radius: var(--tm-r-sm); }
button { display: grid; place-items: center; width: 30px; height: 26px; padding: 0; border: 0; border-radius: var(--tm-r-xs); background: transparent; color: var(--tm-ink-2); cursor: pointer; }
button:hover { color: var(--tm-ink); }
button.is-on { background: var(--tm-raised); color: var(--tm-brand); box-shadow: var(--tm-shadow-float); }
</style>
