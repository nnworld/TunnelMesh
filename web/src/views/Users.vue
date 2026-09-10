<template>
  <section class="tm-page">
    <PageHeader :title="t('users.title')" :description="t('users.description')"><el-button type="primary" @click="createOpen = true">{{ t('users.create') }}</el-button></PageHeader>
    <div class="tm-card panel">
      <div class="toolbar"><el-segmented v-model="status" :options="statusOptions" @change="load" /></div>
      <DataState :loading="loading" :error="loadError" :empty="!page.items.length" :error-label="t('users.loadFailed')" :retry-label="t('common.retry')" :empty-label="t('common.empty')" @retry="load">
        <el-table :data="page.items">
          <el-table-column prop="username" :label="t('users.username')" min-width="180" />
          <el-table-column :label="t('users.status')" width="120"><template #default="{ row }"><StatusTag :kind="row.deletedAt ? 'info' : row.disabled ? 'warning' : 'success'" :label="row.deletedAt ? t('users.deleted') : row.disabled ? t('users.disabled') : t('users.active')" /></template></el-table-column>
          <el-table-column :label="t('users.createdAt')" min-width="180"><template #default="{ row }">{{ formatDate(row.createdAt) }}</template></el-table-column>
          <el-table-column :label="t('users.actions')" width="330" fixed="right">
            <template #default="{ row }">
              <template v-if="row.deletedAt"><el-button link type="primary" @click="restore(row)">{{ t('users.restore') }}</el-button></template>
              <template v-else>
                <el-button link @click="toggle(row)">{{ row.disabled ? t('users.enable') : t('users.disable') }}</el-button>
                <el-button link @click="reset(row)">{{ t('users.resetPassword') }}</el-button>
                <el-button link type="danger" @click="remove(row)">{{ t('users.delete') }}</el-button>
              </template>
            </template>
          </el-table-column>
        </el-table>
      </DataState>
    </div>
    <el-dialog v-model="createOpen" :title="t('users.create')" width="min(440px,92vw)">
      <el-form @submit.prevent="create"><el-form-item :label="t('users.username')"><el-input v-model="username" maxlength="64" /></el-form-item></el-form>
      <template #footer><el-button @click="createOpen = false">{{ t('users.cancel') }}</el-button><el-button type="primary" :loading="saving" @click="create">{{ t('users.create') }}</el-button></template>
    </el-dialog>
    <OneTimePasswordDialog v-model="temporaryOpen" :password="temporaryPassword" @clear="clearTemporaryPassword" />
  </section>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { useI18n } from 'vue-i18n'
import PageHeader from '../components/PageHeader.vue'
import DataState from '../components/DataState.vue'
import StatusTag from '../components/StatusTag.vue'
import OneTimePasswordDialog from '../components/OneTimePasswordDialog.vue'
import { useFormatDateTime } from '../i18n/format'
import { accountErrorMessage } from '../i18n/errors'
import { createUser, deleteUser, listUsers, resetUserPassword, restoreUser, updateUserStatus, type UserAccount, type UserPage } from '../api/client'

const { t } = useI18n()
const status = ref<'active' | 'deleted' | 'all'>('active')
const page = ref<UserPage>({ items: [] })
const loading = ref(false)
const loadError = ref(false)
const saving = ref(false)
const createOpen = ref(false)
const username = ref('')
const temporaryOpen = ref(false)
const temporaryPassword = ref('')
const formatDate = useFormatDateTime()
const statusOptions = computed(() => [{ label: t('users.active'), value: 'active' }, { label: t('users.deleted'), value: 'deleted' }, { label: t('users.all'), value: 'all' }])

async function load() {
  loading.value = true; loadError.value = false
  try { page.value = await listUsers({ status: status.value, limit: 50 }) }
  catch { loadError.value = true }
  finally { loading.value = false }
}

async function create() {
  saving.value = true
  try {
    const result = await createUser(username.value); createOpen.value = false; username.value = ''
    temporaryPassword.value = result.temporaryPassword; temporaryOpen.value = true; await load(); ElMessage.success(t('users.created'))
  } catch (error) { ElMessage.error(accountErrorMessage(error)) }
  finally { saving.value = false }
}

async function toggle(row: UserAccount) {
  try { await ElMessageBox.confirm(t(row.disabled ? 'users.confirmEnable' : 'users.confirmDisable')); await updateUserStatus(row.id, !row.disabled); await load() }
  catch (error) { reportActionError(error) }
}

async function reset(row: UserAccount) {
  try { await ElMessageBox.confirm(t('users.confirmReset')); const result = await resetUserPassword(row.id); temporaryPassword.value = result.temporaryPassword; temporaryOpen.value = true }
  catch (error) { reportActionError(error) }
}

async function remove(row: UserAccount) {
  try { await ElMessageBox.confirm(t('users.confirmDelete')); await deleteUser(row.id); await load() }
  catch (error) { reportActionError(error) }
}

async function restore(row: UserAccount) {
  try { await restoreUser(row.id); await load(); ElMessage.success(t('users.restored')) }
  catch (error) { reportActionError(error) }
}

function reportActionError(error: unknown) {
  if (error === 'cancel' || error === 'close') return
  ElMessage.error(accountErrorMessage(error))
}

function clearTemporaryPassword() { temporaryPassword.value = ''; temporaryOpen.value = false }
onMounted(load); onBeforeUnmount(clearTemporaryPassword)
</script>

<style scoped>.panel { overflow: hidden; } .toolbar { display: flex; justify-content: flex-end; padding: 14px 16px; border-bottom: 1px solid var(--tm-border); } :deep(.el-table) { --el-table-border-color: #edf1f7; }</style>
