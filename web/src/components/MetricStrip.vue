<template>
  <div class="metric-strip">
    <component :is="item.to ? 'button' : 'div'" v-for="item in items" :key="item.label" class="metric" :class="`hue-${item.hue ?? 'brand'}`" :type="item.to ? 'button' : undefined" @click="item.to && router.push(item.to)">
      <span class="metric-label"><i class="metric-dot" aria-hidden="true" />{{ item.label }}</span>
      <strong class="metric-value tm-num">{{ item.value }}</strong>
      <span v-if="item.note" class="metric-note">{{ item.note }}</span>
    </component>
  </div>
</template>

<script setup lang="ts">
import { useRouter } from 'vue-router'

type Hue = 'brand' | 'ok' | 'sky' | 'violet' | 'warn' | 'rose' | 'lime'
defineProps<{ items: { label: string; value: number | string; note?: string; to?: string; hue?: Hue }[] }>()
const router = useRouter()
</script>

<style scoped>
/* One bordered strip divided by 1px gaps showing the line colour, like the readout of an instrument,
   rather than five separate cards. */
.metric-strip { display: grid; grid-template-columns: repeat(auto-fit, minmax(160px, 1fr)); gap: 1px; background: var(--tm-line); border: 1px solid var(--tm-line); border-radius: var(--tm-r-md); overflow: hidden; }
.metric { position: relative; display: flex; flex-direction: column; gap: var(--tm-s-1); align-items: flex-start; padding: var(--tm-s-4) var(--tm-s-5); border: 0; background: var(--tm-paper); color: var(--tm-ink); font: inherit; text-align: left; }
button.metric { cursor: pointer; }
button.metric:hover { background: var(--tm-raised); }
.metric::before { content: ''; position: absolute; left: 0; right: 0; top: 0; height: 2px; background: var(--hue); }
.metric-label { display: inline-flex; align-items: center; gap: var(--tm-s-2); color: var(--tm-ink-2); font-size: var(--tm-fs-xs); font-weight: 500; letter-spacing: 0.02em; }
.metric-dot { width: 6px; height: 6px; border-radius: 50%; background: var(--hue); }
.metric-value { color: var(--hue-ink); }
.hue-brand { --hue: var(--tm-brand); --hue-ink: var(--tm-ink); }
.hue-ok { --hue: var(--tm-ok); --hue-ink: var(--tm-ink); }
.hue-sky { --hue: var(--tm-sky); --hue-ink: var(--tm-ink); }
.hue-violet { --hue: var(--tm-violet); --hue-ink: var(--tm-ink); }
.hue-warn { --hue: var(--tm-warn); --hue-ink: var(--tm-ink); }
.hue-rose { --hue: var(--tm-rose); --hue-ink: var(--tm-ink); }
.hue-lime { --hue: var(--tm-lime); --hue-ink: var(--tm-ink); }
.metric-value { font-family: var(--tm-font-mono); font-size: var(--tm-fs-display); line-height: var(--tm-lh-display); font-weight: 500; letter-spacing: -0.02em; }
.metric-note { color: var(--tm-ink-3); font-size: var(--tm-fs-xs); }
</style>
