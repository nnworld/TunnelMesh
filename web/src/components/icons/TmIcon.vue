<template>
  <svg
    v-if="markup"
    class="tm-icon"
    viewBox="0 0 24 24"
    :width="size"
    :height="size"
    fill="none"
    stroke="currentColor"
    stroke-width="1.5"
    stroke-linecap="square"
    stroke-linejoin="miter"
    focusable="false"
    :role="label ? 'img' : undefined"
    :aria-label="label || undefined"
    :aria-hidden="label ? undefined : 'true'"
    v-html="markup"
  />
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { iconPaths } from './paths'

const props = withDefaults(defineProps<{ name: string; size?: number; label?: string }>(), { size: 18, label: '' })

// Unknown names render nothing: a typo should never take a page down.
const markup = computed(() => (iconPaths as Record<string, string>)[props.name] ?? '')
</script>

<style scoped>
.tm-icon { flex: none; display: inline-block; vertical-align: -0.2em; }
</style>
