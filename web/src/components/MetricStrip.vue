<template>
  <div class="metric-strip">
    <component :is="item.to ? 'button' : 'div'" v-for="item in items" :key="item.label" class="metric" :type="item.to ? 'button' : undefined" @click="item.to && router.push(item.to)">
      <span class="metric-label">{{ item.label }}</span>
      <strong class="metric-value tm-num">{{ item.value }}</strong>
      <span v-if="item.note" class="metric-note">{{ item.note }}</span>
    </component>
  </div>
</template>

<script setup lang="ts">
import { useRouter } from 'vue-router'

defineProps<{ items: { label: string; value: number | string; note?: string; to?: string }[] }>()
const router = useRouter()
</script>

<style scoped>
/* One bordered strip divided by 1px gaps showing the line colour, like the readout of an instrument,
   rather than five separate cards. */
.metric-strip { display: grid; grid-template-columns: repeat(auto-fit, minmax(160px, 1fr)); gap: 1px; background: var(--tm-line); border: 1px solid var(--tm-line); border-radius: var(--tm-r-md); overflow: hidden; }
.metric { display: flex; flex-direction: column; gap: var(--tm-s-1); align-items: flex-start; padding: var(--tm-s-4) var(--tm-s-5); border: 0; background: var(--tm-paper); color: var(--tm-ink); font: inherit; text-align: left; }
button.metric { cursor: pointer; }
button.metric:hover { background: var(--tm-raised); }
.metric-label { color: var(--tm-ink-2); font-size: var(--tm-fs-xs); font-weight: 500; letter-spacing: 0.02em; }
.metric-value { font-family: var(--tm-font-mono); font-size: var(--tm-fs-display); line-height: var(--tm-lh-display); font-weight: 500; letter-spacing: -0.02em; }
.metric-note { color: var(--tm-ink-3); font-size: var(--tm-fs-xs); }
</style>
