<template>
  <span class="status-led" :data-state="state">
    <span class="status-led__dot" :class="{ 'is-pulsing': state === 'online' }" aria-hidden="true" />
    <span class="status-led__label">{{ label }}</span>
  </span>
</template>

<script setup lang="ts">
// The LED is never the only signal: the label always travels with it, so the
// state survives colour-blindness, forced-colors mode and screen readers.
defineProps<{ state: 'online' | 'offline' | 'disabled'; label: string }>()
</script>

<style scoped>
.status-led { display: inline-flex; align-items: center; gap: var(--tm-s-2); font-size: var(--tm-fs-sm); line-height: var(--tm-lh-sm); color: var(--tm-ink-2); white-space: nowrap; }
.status-led__dot { position: relative; flex: none; width: 8px; height: 8px; border-radius: 50%; box-sizing: border-box; }
/* online = solid lit, offline = hollow ring, disabled = ring with a slash */
.status-led[data-state='online'] { color: var(--tm-ok-ink); }
.status-led[data-state='online'] .status-led__dot { background: var(--tm-led-ok); }
.status-led[data-state='offline'] .status-led__dot { border: 1.5px solid var(--tm-ink-3); }
.status-led[data-state='disabled'] .status-led__dot { border: 1.5px solid var(--tm-ink-4); }
.status-led[data-state='disabled'] .status-led__dot::after { content: ''; position: absolute; left: 50%; top: -3px; width: 1.5px; height: 12px; background: var(--tm-ink-4); transform: translateX(-50%) rotate(45deg); }
.is-pulsing::before { content: ''; position: absolute; inset: -3px; border-radius: 50%; background: var(--tm-led-ok); opacity: 0; animation: led-pulse 2.4s var(--tm-ease) infinite; }
@keyframes led-pulse { 0% { transform: scale(0.6); opacity: 0.45; } 70%, 100% { transform: scale(1.5); opacity: 0; } }
@media (prefers-reduced-motion: reduce) { .is-pulsing::before { animation: none; } }
</style>
