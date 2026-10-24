<template>
  <el-input v-if="field.kind === 'text'" :model-value="text" :placeholder="field.placeholder" clearable @update:model-value="onText" @keyup.enter="emit('commit')" @clear="emit('commit')" />
  <el-select v-else-if="field.kind === 'select'" :model-value="list" multiple collapse-tags collapse-tags-tooltip filterable clearable :placeholder="field.label" @update:model-value="onList" @change="emit('commit')">
    <el-option v-for="option in field.options" :key="option.value" :label="option.label" :value="option.value" />
  </el-select>
  <el-date-picker v-else :model-value="list" type="datetimerange" value-format="YYYY-MM-DDTHH:mm:ssZ" clearable @update:model-value="onList" @change="emit('commit')" />
</template>

<script setup lang="ts">
import { computed } from 'vue'
import type { FilterField } from '../composables/useListQuery'

const props = defineProps<{ field: FilterField; modelValue: unknown }>()
const emit = defineEmits<{ (event: 'update:modelValue', value: unknown): void; (event: 'commit'): void }>()
const text = computed(() => String(props.modelValue ?? ''))
const list = computed(() => (Array.isArray(props.modelValue) ? props.modelValue as string[] : []))
function onText(value: string) { emit('update:modelValue', value) }
function onList(value: unknown) { emit('update:modelValue', Array.isArray(value) ? value : []) }
</script>
