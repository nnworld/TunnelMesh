<template>
  <span class="id-cell">
    <span class="id-text tm-mono" :title="value">{{ middleTruncate(value) }}</span>
    <button type="button" class="id-copy" :aria-label="t('common.copy')" @click.stop="copy"><TmIcon :name="copied ? 'check' : 'copy'" :size="14" /></button>
  </span>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import TmIcon from './icons/TmIcon.vue'
import { copyText } from '../utils/clipboard'
import { middleTruncate } from '../utils/truncate'

const props = defineProps<{ value: string }>()
const { t } = useI18n()
const copied = ref(false)
async function copy() {
  copied.value = await copyText(props.value)
  if (copied.value) setTimeout(() => (copied.value = false), 1600)
}
</script>

<style scoped>
.id-cell { display: inline-flex; align-items: center; gap: var(--tm-s-1); max-width: 100%; }
.id-text { font-size: var(--tm-fs-xs); color: var(--tm-ink-2); white-space: nowrap; }
/* Visible on row hover and on keyboard focus; opacity (not display) keeps it
   reachable and keeps touch devices, which have no hover, showing it. */
.id-copy { display: inline-flex; align-items: center; justify-content: center; width: 22px; height: 22px; padding: 0; border: 0; border-radius: var(--tm-r-xs); background: transparent; color: var(--tm-ink-3); cursor: pointer; opacity: 0; transition: opacity var(--tm-dur-fast) var(--tm-ease); }
:global(.el-table__row:hover) .id-copy, .id-copy:focus-visible { opacity: 1; }
@media (hover: none) { .id-copy { opacity: 1; } }
.id-copy:hover { background: var(--tm-sunken); color: var(--tm-ink); }
</style>
