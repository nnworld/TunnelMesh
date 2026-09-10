<template>
  <el-dialog :model-value="Boolean(secret)" :title="t('tokens.secretTitle')" width="520px" @close="$emit('close')">
    <el-alert type="warning" :closable="false" :title="t('tokens.secretWarning')" />
    <el-input class="secret" :model-value="secret" readonly>
      <template #append><el-button @click="copy">{{t('tokens.copy')}}</el-button></template>
    </el-input>
    <template #footer><el-button type="primary" @click="$emit('close')">{{t('tokens.saved')}}</el-button></template>
  </el-dialog>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
const {t}=useI18n()
const props = defineProps<{ secret: string }>()
const emit = defineEmits<{ close: [] }>()
async function copy() {
  await navigator.clipboard?.writeText(props.secret)
}
</script>

<style scoped>
.secret { margin-top: 18px; font-family: ui-monospace, SFMono-Regular, Menlo, monospace; }
</style>
