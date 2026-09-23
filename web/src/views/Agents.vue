<template>
  <section class="tm-page">
    <PageHeader :title="t('agents.title')"><el-button v-if="auth.isAdmin" type="primary" @click="createOpen = true">{{ t('agents.create') }}</el-button></PageHeader>
    <div class="tm-card">
      <DataState :loading="loading" :error="error" :empty="!items.length" :error-label="t('common.loadFailed')" :retry-label="t('common.retry')" :empty-label="t('common.empty')" @retry="load">
        <el-table :data="items">
          <el-table-column prop="name" :label="t('agents.name')" />
          <el-table-column prop="id" label="ID" />
          <el-table-column :label="t('agents.status')">
            <template #default="scope"><StatusTag :kind="scope.row.status === 'online' ? 'success' : 'info'" :label="scope.row.status === 'online' ? t('agents.online') : t('agents.offline')" /></template>
          </el-table-column>
          <el-table-column :label="t('agents.enabledColumn')">
            <template #default="scope"><StatusTag :kind="scope.row.enabled ? 'success' : 'warning'" :label="scope.row.enabled ? t('agents.active') : t('agents.disabled')" /></template>
          </el-table-column>
          <el-table-column :label="t('agents.actions')">
            <template #default="scope"><el-button link type="primary" @click="$router.push(`/agents/${scope.row.id}`)">{{ t('agents.details') }}</el-button></template>
          </el-table-column>
        </el-table>
      </DataState>
    </div>
  </section>

  <el-dialog v-model="createOpen" :title="t('agents.create')" width="min(440px,92vw)" @closed="resetCreateForm">
    <el-form label-position="top" @submit.prevent="create">
      <el-form-item :label="t('agents.name')"><el-input v-model="createName" maxlength="255" /></el-form-item>
      <el-form-item :label="t('agents.status')"><el-switch v-model="createEnabled" :active-text="t('agents.active')" :inactive-text="t('agents.disabled')" /></el-form-item>
    </el-form>
    <template #footer>
      <el-button @click="createOpen = false">{{ t('users.cancel') }}</el-button>
      <el-button type="primary" :loading="saving" @click="create">{{ t('agents.create') }}</el-button>
    </template>
  </el-dialog>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { useI18n } from 'vue-i18n'
import { useAuthStore } from '../stores/auth'
import PageHeader from '../components/PageHeader.vue'
import DataState from '../components/DataState.vue'
import StatusTag from '../components/StatusTag.vue'
import { createAgent, getAgents, type Agent } from '../api/client'

const { t } = useI18n()
const auth = useAuthStore()
const items = ref<Agent[]>([])
const loading = ref(false)
const error = ref(false)
const saving = ref(false)
const createOpen = ref(false)
const createName = ref('')
const createEnabled = ref(true)

async function load() {
  loading.value = true; error.value = false
  try { items.value = (await getAgents({ limit: 500 })).items }
  catch { error.value = true }
  finally { loading.value = false }
}

async function create() {
  if (!createName.value.trim()) {
    ElMessage.warning(t('agents.nameRequired'))
    return
  }
  saving.value = true
  try {
    await createAgent({ name: createName.value.trim(), enabled: createEnabled.value })
    createOpen.value = false
    await load()
    ElMessage.success(t('agents.created'))
  } catch {
    ElMessage.error(t('agents.createFailed'))
  } finally {
    saving.value = false
  }
}

function resetCreateForm() {
  createName.value = ''
  createEnabled.value = true
}

onMounted(load)
</script>
