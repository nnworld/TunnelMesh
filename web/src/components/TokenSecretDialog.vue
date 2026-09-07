<template>
  <el-dialog :model-value="Boolean(secret)" title="Copy this secret now" width="520px" @close="$emit('close')">
    <el-alert type="warning" :closable="false" title="The secret is shown once. It is not stored in the browser or returned by later requests." />
    <el-input class="secret" :model-value="secret" readonly>
      <template #append><el-button @click="copy">Copy</el-button></template>
    </el-input>
    <template #footer><el-button type="primary" @click="$emit('close')">I saved it</el-button></template>
  </el-dialog>
</template>

<script setup lang="ts">
const props = defineProps<{ secret: string }>()
const emit = defineEmits<{ close: [] }>()
async function copy() {
  await navigator.clipboard?.writeText(props.secret)
}
</script>

<style scoped>
.secret { margin-top: 18px; font-family: ui-monospace, SFMono-Regular, Menlo, monospace; }
</style>
