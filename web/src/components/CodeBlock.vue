<template>
  <div class="code-block">
    <pre class="tm-mono">{{ code }}</pre>
    <button type="button" class="code-block__copy" data-test="copy" :aria-label="t('common.copy')" @click="copy">
      <TmIcon :name="copied ? 'check' : 'copy'" :size="16" />
    </button>
  </div>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import TmIcon from './icons/TmIcon.vue'
import { copyText } from '../utils/clipboard'

const props = defineProps<{ code: string }>()
const { t } = useI18n()
const copied = ref(false)

async function copy() {
  copied.value = await copyText(props.code)
  if (copied.value) setTimeout(() => (copied.value = false), 1600)
}
</script>

<style scoped>
.code-block { position: relative; background: var(--tm-terminal-bg); border-radius: var(--tm-r-sm); }
pre { margin: 0; padding: var(--tm-s-3) var(--tm-s-12) var(--tm-s-3) var(--tm-s-4); overflow-x: auto; color: var(--tm-ink); font-size: var(--tm-fs-sm); line-height: var(--tm-lh-md); white-space: pre; }
.code-block__copy { position: absolute; top: var(--tm-s-2); right: var(--tm-s-2); display: grid; place-items: center; width: 28px; height: 28px; padding: 0; border: 0; border-radius: var(--tm-r-xs); background: transparent; color: var(--tm-ink-2); cursor: pointer; }
.code-block__copy:hover { background: var(--tm-sunken); color: var(--tm-ink); }
</style>
