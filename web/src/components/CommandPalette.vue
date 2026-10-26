<template>
  <el-dialog
    :model-value="modelValue"
    class="command-palette"
    :show-close="false"
    :append-to-body="true"
    width="560px"
    align-center
    :aria-label="t('shell.search')"
    @update:model-value="emit('update:modelValue', $event)"
    @opened="focusInput"
    @closed="restoreFocus"
  >
    <template #header><span class="tm-sr-only">{{ t('shell.search') }}</span></template>
    <div class="palette-field">
      <TmIcon name="search" :size="18" />
      <input
        ref="inputEl"
        v-model="query"
        role="combobox"
        type="text"
        autocomplete="off"
        spellcheck="false"
        aria-autocomplete="list"
        :aria-expanded="results.length > 0"
        :aria-controls="listId"
        :aria-activedescendant="results.length ? optionId(active) : undefined"
        :aria-label="t('shell.search')"
        :placeholder="t('shell.search')"
        @keydown="onKeydown"
      />
      <kbd>Esc</kbd>
    </div>
    <ul :id="listId" class="palette-list" role="listbox" :aria-label="t('shell.search')">
      <li
        v-for="(entry, index) in results"
        :id="optionId(index)"
        :key="entryKey(entry)"
        role="option"
        :aria-selected="index === active"
        :class="{ 'is-active': index === active }"
        @mousemove="active = index"
        @click="run(entry)"
      >
        <TmIcon :name="entry.icon" :size="18" />
        <span class="palette-label">{{ labelFor(entry) }}</span>
        <span class="palette-kind">{{ entry.kind === 'page' ? t('shell.pages') : t('shell.actions') }}</span>
      </li>
    </ul>
    <p v-if="!results.length" class="palette-empty" role="status">{{ t('shell.noResults', { query: query.trim() }) }}</p>
    <p v-else class="palette-hint">{{ t('shell.searchHint') }}</p>
  </el-dialog>
</template>

<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { ElDialog } from 'element-plus'
import 'element-plus/es/components/dialog/style/css'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import TmIcon from './icons/TmIcon.vue'
import { useAuthStore } from '../stores/auth'
import { usePreferencesStore } from '../stores/preferences'
import { paletteEntries, type PaletteEntry } from '../layouts/navigation'
import { rankEntries } from '../layouts/paletteSearch'
import zhCN from '../i18n/messages/zh-CN'
import enUS from '../i18n/messages/en-US'

const props = defineProps<{ modelValue: boolean }>()
const emit = defineEmits<{ 'update:modelValue': [value: boolean] }>()

const { t, locale } = useI18n()
const router = useRouter()
const auth = useAuthStore()
const preferences = usePreferencesStore()

const query = ref('')
const active = ref(0)
const inputEl = ref<HTMLInputElement>()
const listId = `palette-list-${Math.random().toString(36).slice(2, 8)}`
let opener: HTMLElement | null = null

const lookup = (tree: unknown, key: string) => key.split('.').reduce<any>((node, part) => node?.[part], tree) as string
const otherTree = computed(() => (locale.value === 'zh-CN' ? enUS : zhCN))

const results = computed(() => rankEntries(
  paletteEntries(auth.isAdmin), query.value, key => t(key), key => lookup(otherTree.value, key) ?? '',
))

// Theme actions read as "Theme: Dark" so a bare "Dark" is never ambiguous.
function labelFor(entry: PaletteEntry) {
  return entry.kind === 'action' && entry.action.startsWith('theme-') ? `${t('shell.theme')}: ${t(entry.labelKey)}` : t(entry.labelKey)
}
const entryKey = (entry: PaletteEntry) => (entry.kind === 'page' ? `page:${entry.path}` : `action:${entry.action}`)
const optionId = (index: number) => `${listId}-opt-${index}`

watch(query, () => { active.value = 0 })
watch(() => props.modelValue, open => {
  if (open) { opener = document.activeElement as HTMLElement | null; query.value = ''; active.value = 0 }
})

function focusInput() { nextTick(() => inputEl.value?.focus()) }
function restoreFocus() { opener?.focus?.(); opener = null }

function onKeydown(event: KeyboardEvent) {
  const count = results.value.length
  if (event.key === 'ArrowDown') { event.preventDefault(); if (count) active.value = (active.value + 1) % count }
  else if (event.key === 'ArrowUp') { event.preventDefault(); if (count) active.value = (active.value - 1 + count) % count }
  else if (event.key === 'Home') { event.preventDefault(); active.value = 0 }
  else if (event.key === 'End') { event.preventDefault(); active.value = Math.max(count - 1, 0) }
  else if (event.key === 'Enter') { event.preventDefault(); const entry = results.value[active.value]; if (entry) run(entry) }
  active.value = Math.min(active.value, Math.max(count - 1, 0))
  nextTick(() => document.getElementById(optionId(active.value))?.scrollIntoView?.({ block: 'nearest' }))
}

function run(entry: PaletteEntry) {
  emit('update:modelValue', false)
  if (entry.kind === 'page') { void router.push(entry.path); return }
  if (entry.action === 'theme-light') preferences.setTheme('light')
  else if (entry.action === 'theme-dark') preferences.setTheme('dark')
  else if (entry.action === 'theme-system') preferences.setTheme('system')
  else preferences.setLocale(locale.value === 'zh-CN' ? 'en-US' : 'zh-CN')
}
</script>

<style>
/* Unscoped: el-dialog teleports to <body>, outside this component's scope id. */
.command-palette.el-dialog { padding: 0; align-self: flex-start; margin-top: 12vh; }
.command-palette .el-dialog__header { display: contents; }
.command-palette .el-dialog__body { padding: 0; }
.command-palette .palette-field { display: flex; align-items: center; gap: var(--tm-s-3); padding: 0 var(--tm-s-4); height: 52px; border-bottom: 1px solid var(--tm-line); color: var(--tm-ink-3); }
.command-palette .palette-field input { flex: 1; min-width: 0; height: 100%; border: 0; outline: 0; background: transparent; color: var(--tm-ink); font: inherit; font-size: var(--tm-fs-md); }
.command-palette kbd { font-family: var(--tm-font-mono); font-size: var(--tm-fs-xs); padding: 2px 6px; border: 1px solid var(--tm-line-strong); border-radius: var(--tm-r-xs); color: var(--tm-ink-3); }
.command-palette .palette-list { list-style: none; margin: 0; padding: var(--tm-s-2); max-height: min(50vh, 380px); overflow-y: auto; }
.command-palette .palette-list li { display: flex; align-items: center; gap: var(--tm-s-3); padding: 0 var(--tm-s-3); height: 40px; border-radius: var(--tm-r-sm); color: var(--tm-ink); font-size: var(--tm-fs-sm); cursor: pointer; }
.command-palette .palette-list li.is-active { background: var(--tm-brand-tint); color: var(--tm-brand); }
.command-palette .palette-label { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.command-palette .palette-kind { color: var(--tm-ink-3); font-size: var(--tm-fs-xs); }
.command-palette .palette-empty, .command-palette .palette-hint { margin: 0; padding: var(--tm-s-3) var(--tm-s-5); color: var(--tm-ink-3); font-size: var(--tm-fs-xs); }
.command-palette .palette-empty { padding-block: var(--tm-s-8); text-align: center; font-size: var(--tm-fs-sm); }
.command-palette .palette-hint { border-top: 1px solid var(--tm-line); }
</style>
