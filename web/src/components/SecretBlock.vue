<template>
  <div class="secret-block">
    <code class="tm-mono" data-test="secret-value">{{ shown ? value : mask }}</code>
    <div class="secret-block__actions">
      <button type="button" data-test="secret-toggle" :aria-pressed="shown" :aria-label="shown ? t('common.hide') : t('common.show')" @click="shown = !shown">
        <TmIcon :name="shown ? 'eye-off' : 'eye'" :size="16" />
      </button>
      <button type="button" data-test="copy" :aria-label="t('common.copy')" @click="copy">
        <TmIcon :name="copied ? 'check' : 'copy'" :size="16" />
      </button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import TmIcon from './icons/TmIcon.vue'
import { copyText } from '../utils/clipboard'

const props = defineProps<{ value: string; revealed?: boolean }>()
const { t } = useI18n()
const shown = ref(props.revealed === true)
const copied = ref(false)
// A fixed-length mask: showing the real length would leak a little of the secret.
const mask = computed(() => '•'.repeat(24))

async function copy() {
  copied.value = await copyText(props.value)
  if (copied.value) setTimeout(() => (copied.value = false), 1600)
}
</script>

<style scoped>
.secret-block { display: flex; align-items: center; gap: var(--tm-s-2); padding: var(--tm-s-2) var(--tm-s-2) var(--tm-s-2) var(--tm-s-4); background: var(--tm-terminal-bg); border-radius: var(--tm-r-sm); }
code { flex: 1; min-width: 0; overflow-wrap: anywhere; color: var(--tm-ink); font-size: var(--tm-fs-sm); line-height: var(--tm-lh-md); }
.secret-block__actions { display: flex; gap: var(--tm-s-1); }
button { display: grid; place-items: center; width: 28px; height: 28px; padding: 0; border: 0; border-radius: var(--tm-r-xs); background: transparent; color: var(--tm-ink-2); cursor: pointer; }
button:hover { background: var(--tm-sunken); color: var(--tm-ink); }
</style>
