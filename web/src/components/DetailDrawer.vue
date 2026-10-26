<template>
  <el-drawer :model-value="modelValue" class="detail-drawer" direction="rtl" :size="size" :title="title" @update:model-value="emit('update:modelValue', $event)">
    <slot />
  </el-drawer>
</template>

<script setup lang="ts">
// Read-only details live in a drawer, not a dialog: the list stays visible
// behind it so the reader keeps their place.
withDefaults(defineProps<{ modelValue: boolean; title: string; size?: string }>(), { size: 'min(560px, 100vw)' })
const emit = defineEmits<{ 'update:modelValue': [value: boolean] }>()
</script>

<style>
.detail-drawer .el-drawer__header { margin: 0; padding: var(--tm-s-4) var(--tm-s-5); border-bottom: 1px solid var(--tm-line); font-size: var(--tm-fs-lg); }
.detail-drawer .el-drawer__body { display: flex; flex-direction: column; gap: var(--tm-s-4); }
</style>
