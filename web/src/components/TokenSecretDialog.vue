<template>
  <el-dialog :model-value="Boolean(secret)" :title="t('tokens.secretTitle')" width="520px" @close="$emit('close')">
    <el-alert type="warning" :closable="false" :title="t('tokens.secretWarning')" />
    <!-- Masked by default so a shoulder-surfer or screen share sees nothing, but
         the value stays copyable: it is shown once and cannot be fetched again. -->
    <SecretBlock class="secret" :value="secret" />
    <template #footer><el-button type="primary" @click="$emit('close')">{{t('tokens.saved')}}</el-button></template>
  </el-dialog>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import SecretBlock from './SecretBlock.vue'
const {t}=useI18n()
defineProps<{ secret: string }>()
defineEmits<{ close: [] }>()
</script>

<style scoped>
.secret { margin-top: var(--tm-s-4); }
</style>
