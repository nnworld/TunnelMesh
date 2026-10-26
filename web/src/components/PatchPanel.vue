<template>
  <div class="patch-panel">
    <div class="patch-grid" role="group" :aria-label="t('dashboard.patchTitle')" @keydown="onKeydown">
      <button
        v-for="(agent, index) in agents"
        :key="agent.id"
        type="button"
        class="port"
        data-test="port"
        :data-state="stateOf(agent)"
        :tabindex="index === focusIndex ? 0 : -1"
        :aria-label="`${agent.name}, ${t(`dashboard.patchState.${stateOf(agent)}`)}`"
        :title="agent.id"
        @focus="focusIndex = index"
        @click="router.push(`/agents/${agent.id}`)"
      >
        <i class="port-led" aria-hidden="true" />
        <span class="port-name tm-mono">{{ agent.name }}</span>
      </button>
      <button v-if="hidden > 0" type="button" class="port port-more" data-test="port-more" :tabindex="-1" @click="router.push('/agents')">
        <span class="port-name tm-mono">{{ t('dashboard.patchMore', { count: hidden }) }}</span>
      </button>
    </div>
    <p class="legend" data-test="legend">
      <span class="legend-item"><i class="port-led" data-state="online" aria-hidden="true" />{{ online }} {{ t('dashboard.patchOnline') }}</span>
      <span class="legend-total">{{ t('dashboard.patchOf', { total }) }}</span>
    </p>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import type { Agent } from '../api/client'

const props = defineProps<{ agents: Agent[]; total: number; online: number }>()
const { t } = useI18n()
const router = useRouter()
const focusIndex = ref(0)

// The legend and the "+K" cell use the summary total: the agent list is only a
// truncated sample, so counting it would understate a large fleet.
const hidden = computed(() => Math.max(props.total - props.agents.length, 0))

// A disabled agent is shown as disabled even if a stale connection reports online.
function stateOf(agent: Agent): 'online' | 'offline' | 'disabled' {
  if (!agent.enabled) return 'disabled'
  return agent.status === 'online' ? 'online' : 'offline'
}

function onKeydown(event: KeyboardEvent) {
  const step = event.key === 'ArrowRight' || event.key === 'ArrowDown' ? 1 : event.key === 'ArrowLeft' || event.key === 'ArrowUp' ? -1 : 0
  const edge = event.key === 'Home' ? 0 : event.key === 'End' ? props.agents.length - 1 : null
  if (!step && edge === null) return
  event.preventDefault()
  const last = props.agents.length - 1
  const next = edge ?? Math.min(Math.max(focusIndex.value + step, 0), last)
  focusIndex.value = next
  ;(event.currentTarget as HTMLElement).querySelectorAll<HTMLElement>('[data-test="port"]')[next]?.focus()
}
</script>

<style scoped>
.patch-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(148px, 1fr)); gap: var(--tm-s-2); }
.port { display: flex; align-items: center; gap: var(--tm-s-2); min-width: 0; height: 36px; padding: 0 var(--tm-s-3); border: 1px solid var(--tm-line-strong); border-radius: var(--tm-r-xs); background: var(--tm-raised); color: var(--tm-ink); font: inherit; cursor: pointer; transition: border-color var(--tm-dur-fast) var(--tm-ease), background-color var(--tm-dur-fast) var(--tm-ease); }
.port:hover { border-color: var(--tm-brand); background: var(--tm-brand-tint); }
.port-name { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: var(--tm-fs-xs); }
.port-more { justify-content: center; border-style: dashed; color: var(--tm-ink-2); }
.port-led { flex: none; width: 8px; height: 8px; border-radius: 50%; box-sizing: border-box; }
[data-state='online'] > .port-led, .port-led[data-state='online'] { background: var(--tm-led-ok); }
[data-state='offline'] > .port-led { border: 1.5px solid var(--tm-ink-3); }
[data-state='disabled'] > .port-led { border: 1.5px solid var(--tm-ink-4); background: linear-gradient(135deg, transparent 44%, var(--tm-ink-4) 44% 56%, transparent 56%); }
[data-state='disabled'] .port-name { color: var(--tm-ink-3); text-decoration: line-through; text-decoration-thickness: 1px; }
.legend { display: flex; flex-wrap: wrap; gap: var(--tm-s-4); margin: var(--tm-s-4) 0 0; color: var(--tm-ink-2); font-size: var(--tm-fs-xs); }
.legend-item { display: inline-flex; align-items: center; gap: var(--tm-s-2); color: var(--tm-ok-ink); font-weight: 500; }
</style>
