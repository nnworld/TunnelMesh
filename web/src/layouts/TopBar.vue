<template>
  <header class="top-bar">
    <button type="button" class="menu-trigger" data-test="menu-open" :aria-label="t('shell.openMenu')" @click="emit('menu')">
      <TmIcon name="menu" :size="20" />
    </button>
    <button type="button" class="search-trigger" data-test="palette-open" :aria-label="t('shell.search')" @click="emit('search')">
      <TmIcon name="search" :size="16" />
      <span class="search-text">{{ t('shell.search') }}</span>
      <kbd>{{ modifier }} K</kbd>
    </button>
    <AccountMenu />
  </header>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import TmIcon from '../components/icons/TmIcon.vue'
import AccountMenu from './AccountMenu.vue'

const emit = defineEmits<{ menu: []; search: [] }>()
const { t } = useI18n()
// Shown as a hint only; the shortcut itself accepts both Ctrl and Cmd.
const modifier = typeof navigator !== 'undefined' && /Mac|iPhone|iPad/.test(navigator.platform) ? '⌘' : 'Ctrl'
</script>

<style scoped>
.top-bar { display: flex; align-items: center; justify-content: flex-end; gap: var(--tm-s-3); height: var(--tm-topbar-h); padding: 0 var(--tm-s-6); background: var(--tm-paper); border-bottom: 1px solid var(--tm-line); }
.menu-trigger { display: none; align-items: center; justify-content: center; width: 34px; height: 34px; margin-right: auto; padding: 0; border: 0; border-radius: var(--tm-r-sm); background: transparent; color: var(--tm-ink); cursor: pointer; }
.search-trigger { display: inline-flex; align-items: center; gap: var(--tm-s-3); width: 320px; max-width: 100%; height: 34px; padding: 0 var(--tm-s-3); border: 1px solid var(--tm-line-strong); border-radius: var(--tm-r-sm); background: var(--tm-canvas); color: var(--tm-ink-3); font: inherit; font-size: var(--tm-fs-sm); cursor: pointer; margin-right: auto; }
.search-trigger:hover { border-color: var(--tm-ink-3); color: var(--tm-ink-2); }
.search-text { flex: 1; text-align: left; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
kbd { font-family: var(--tm-font-mono); font-size: var(--tm-fs-xs); padding: 1px 6px; border: 1px solid var(--tm-line-strong); border-radius: var(--tm-r-xs); background: var(--tm-paper); color: var(--tm-ink-3); }
@media (max-width: 760px) {
  .top-bar { padding: 0 var(--tm-s-3); }
  .menu-trigger { display: flex; margin-right: 0; }
  .search-trigger { width: 34px; padding: 0; justify-content: center; margin-right: auto; }
  .search-text, kbd { display: none; }
}
</style>
