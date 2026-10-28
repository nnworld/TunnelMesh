<template>
  <span class="brand-mark">
    <svg viewBox="0 0 28 12" width="28" height="12" fill="none" stroke="currentColor" stroke-width="1.5" aria-hidden="true">
      <circle cx="3" cy="6" r="2.25" />
      <circle cx="14" cy="6" r="2.25" />
      <circle cx="25" cy="6" r="2.25" />
      <path d="M5.25 6h6.5M16.25 6h6.5" />
    </svg>
    <span class="brand-mark__name">{{ name }}</span>
  </span>
</template>

<script setup lang="ts">
// Three ports joined by a patch lead: the mesh, drawn the way a rack labels it.
defineProps<{ name: string }>()
</script>

<style scoped>
.brand-mark { display: inline-flex; align-items: center; gap: var(--tm-s-3); color: var(--tm-brand); }
.brand-mark__name { color: var(--tm-ink); font-size: var(--tm-fs-lg); font-weight: 600; letter-spacing: -0.01em; }
</style>
