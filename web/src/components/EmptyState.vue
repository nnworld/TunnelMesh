<template>
  <div class="empty-state">
    <!-- An empty patch panel: ports waiting for a lead. Drawn with currentColor
         so it follows the theme, and decorative because the text says it all. -->
    <svg viewBox="0 0 120 48" width="120" height="48" fill="none" stroke="currentColor" stroke-width="1.5" aria-hidden="true">
      <rect x="1.5" y="6.5" width="117" height="35" rx="2" />
      <path d="M1.5 14.5h117" opacity="0.5" />
      <g>
        <rect v-for="n in 8" :key="n" :x="10 + (n - 1) * 13" y="21" width="9" height="11" rx="1" />
      </g>
      <circle cx="8" cy="10.5" r="1.25" fill="currentColor" stroke="none" />
    </svg>
    <p class="empty-label">{{ label }}</p>
    <p v-if="hint" class="empty-hint">{{ hint }}</p>
    <div v-if="$slots.default" class="empty-action"><slot /></div>
  </div>
</template>

<script setup lang="ts">
defineProps<{ label: string; hint?: string }>()
</script>

<style scoped>
.empty-state { display: flex; flex-direction: column; align-items: center; gap: var(--tm-s-2); padding: var(--tm-s-10) var(--tm-s-4); color: var(--tm-ink-4); text-align: center; }
.empty-label { margin: var(--tm-s-3) 0 0; color: var(--tm-ink); font-size: var(--tm-fs-md); font-weight: 500; }
.empty-hint { margin: 0; max-width: 44ch; color: var(--tm-ink-2); font-size: var(--tm-fs-sm); }
.empty-action { margin-top: var(--tm-s-3); }
</style>
