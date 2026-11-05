<template>
  <span v-if="!agentId" class="agent-ref agent-ref--empty">—</span>
  <router-link v-else :to="`/agents/${agentId}`" :title="agentId" class="agent-ref" :class="{ 'tm-mono': !resolved }">{{ label }}</router-link>
</template>

<script setup lang="ts">
import { computed, onMounted } from 'vue'
import { useAgentIndex } from '../stores/agentIndex'

// One rendering exit for every Agent reference in the console. A caller that
// already has an authoritative name (the remote-server projection) passes it
// explicitly; everyone else resolves through the shared index, and an unknown
// id renders as itself so a reference is never silently dropped.
const props = defineProps<{ agentId?: string | null; name?: string | null }>()

const agentIndex = useAgentIndex()
const indexed = computed(() => agentIndex.nameOf(props.agentId))
const resolved = computed(() => Boolean(props.name || indexed.value))
const label = computed(() => props.name || indexed.value || props.agentId || '')

// Cells on one page mount together; the store dedupes them into a single list
// request, so a table with a hundred references costs one page of Agents. A
// cell with nothing to resolve must not trigger that request at all.
onMounted(() => { if (props.agentId) void agentIndex.ensureLoaded() })
</script>

<style scoped>
.agent-ref { color: var(--tm-brand); text-decoration: none; }
.agent-ref:hover { color: var(--tm-brand-hover); text-decoration: underline; }
/* Unresolved means "this is an id, not a name": it keeps the mono id treatment
   of IdCell so the two never look like the same kind of value. */
.agent-ref.tm-mono { color: var(--tm-ink-2); font-size: var(--tm-fs-xs); white-space: nowrap; }
.agent-ref.tm-mono:hover { color: var(--tm-ink); text-decoration: none; }
.agent-ref--empty { color: var(--tm-ink-3); }
</style>
