<template><section class="tm-page"><PageHeader :title="t('tunnels.title')" /><div class="tm-card"><DataState :loading="loading" :error="error" :empty="!items.length" :error-label="t('common.loadFailed')" :retry-label="t('common.retry')" :empty-label="t('common.empty')" @retry="load"><el-table :data="items"><el-table-column prop="protocol" :label="t('tunnels.protocol')" /><el-table-column prop="targetHost" :label="t('tunnels.target')" /><el-table-column prop="status" :label="t('tunnels.status')" /></el-table></DataState></div></section></template>
<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import PageHeader from '../components/PageHeader.vue'
import DataState from '../components/DataState.vue'
import { api } from '../api/client'
const { t } = useI18n(); const items = ref<any[]>([]); const loading = ref(false); const error = ref(false)
async function load() { loading.value = true; error.value = false; try { items.value = (await api<any>('/tunnels')).items } catch { error.value = true } finally { loading.value = false } }
onMounted(load)
</script>
