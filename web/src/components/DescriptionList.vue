<template>
  <dl class="description-list">
    <template v-for="item in items" :key="item.label">
      <dt>{{ item.label }}</dt>
      <dd :class="{ 'tm-mono': item.mono }">{{ display(item.value) }}</dd>
    </template>
  </dl>
</template>

<script setup lang="ts">
defineProps<{ items: { label: string; value: string | number | null | undefined; mono?: boolean }[] }>()

// An empty value renders as an em dash so a missing field is visibly missing.
function display(value: string | number | null | undefined) {
  return value === '' || value === null || value === undefined ? '—' : String(value)
}
</script>

<style scoped>
.description-list { display: grid; grid-template-columns: minmax(96px, max-content) minmax(0, 1fr); gap: var(--tm-s-2) var(--tm-s-5); margin: 0; font-size: var(--tm-fs-sm); line-height: var(--tm-lh-sm); }
dt { color: var(--tm-ink-2); }
dd { margin: 0; color: var(--tm-ink); overflow-wrap: anywhere; }
</style>
