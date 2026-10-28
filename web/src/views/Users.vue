<template>
  <section class="tm-page">
    <PageHeader :title="t('users.title')" :description="t('users.description')"><el-button type="primary" @click="createOpen = true">{{ t('users.create') }}</el-button></PageHeader>
    <div class="tm-card panel">
      <div class="toolbar"><el-input v-model="keyword" clearable class="keyword-input" :placeholder="t('users.username')" @keyup.enter="search" @clear="search" /><el-segmented v-model="status" :options="statusOptions" @change="search" /><el-button @click="search">{{ t('common.query') }}</el-button></div>
      <DataState :loading="loading" :error="loadError" :empty="!page.items.length" :error-label="t('users.loadFailed')" :retry-label="t('common.retry')" :empty-label="t('common.empty')" @retry="load">
        <el-table :data="page.items" @sort-change="onSortChange">
          <el-table-column prop="username" :label="t('users.username')" min-width="180" sortable="custom" />
          <el-table-column :label="t('users.status')" width="120"><template #default="{ row }"><StatusTag :kind="row.deletedAt ? 'info' : row.disabled ? 'warning' : 'success'" :label="row.deletedAt ? t('users.deleted') : row.disabled ? t('users.disabled') : t('users.active')" /></template></el-table-column>
          <!-- Per-account MFA requirement: it wins over the global auth policy, so
               it is an explicit switch instead of a side effect of another edit. -->
          <el-table-column :label="t('users.mfaRequired')" width="140"><template #default="{ row }"><el-switch v-model="row.mfaRequired" :disabled="Boolean(row.deletedAt)" :data-test="`mfa-required-${row.id}`" @change="value => toggleMfaRequired(row, value)" /></template></el-table-column>
          <el-table-column prop="createdAt" :label="t('users.createdAt')" min-width="180" sortable="custom"><template #default="{ row }">{{ formatDate(row.createdAt) }}</template></el-table-column>
          <el-table-column :label="t('users.actions')" width="420" fixed="right">
            <template #default="{ row }">
              <template v-if="row.deletedAt"><el-button link type="primary" @click="restore(row)">{{ t('users.restore') }}</el-button></template>
              <template v-else>
                <el-button link @click="toggle(row)">{{ row.disabled ? t('users.enable') : t('users.disable') }}</el-button>
                <el-button link @click="reset(row)">{{ t('users.resetPassword') }}</el-button>
                <el-button link type="warning" :data-test="`reset-mfa-${row.id}`" @click="resetMfa(row)">{{ t('users.resetMFA') }}</el-button>
                <el-button link type="danger" @click="remove(row)">{{ t('users.delete') }}</el-button>
              </template>
            </template>
          </el-table-column>
        </el-table>
        <div class="list-pager"><el-button :disabled="pageIndex === 0 || loading" @click="previous">{{ t('common.previousPage') }}</el-button><span>{{ t('common.page', { number: pageIndex + 1 }) }}</span><el-button :disabled="!nextCursor || loading" @click="next">{{ t('common.nextPage') }}</el-button><el-select v-model="pageSize" class="page-size" @change="search"><el-option v-for="size in [20, 50, 100]" :key="size" :label="String(size)" :value="size" /></el-select></div>
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
import { createUser, deleteUser, listUsers, resetUserPassword, restoreUser, updateUserMFARequired, updateUserStatus, type UserAccount, type UserPage } from '../api/client'
import { resetUserMFA } from '../api/auth'

const { t } = useI18n()
const status = ref<'active' | 'deleted' | 'all'>('active')
const keyword = ref('')
const pageIndex = ref(0)
const pageSize = ref(20)
const nextCursor = ref('')
const cursors = ref<string[]>([''])
const sort = ref('createdAt')
const order = ref<'asc' | 'desc'>('desc')
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
  try { page.value = await listUsers({ status: status.value, keyword: keyword.value.trim() || undefined, limit: pageSize.value, cursor: cursors.value[pageIndex.value] || undefined, sort: sort.value, order: order.value }); nextCursor.value = page.value.nextCursor || '' }
  catch { loadError.value = true }
  finally { loading.value = false }
}

function search() { pageIndex.value = 0; cursors.value = ['']; void load() }
function next() { if (!nextCursor.value) return; cursors.value[pageIndex.value + 1] = nextCursor.value; pageIndex.value += 1; void load() }
function previous() { if (pageIndex.value === 0) return; pageIndex.value -= 1; void load() }
function onSortChange(payload: { prop?: string; order?: 'ascending' | 'descending' | null }) { sort.value = payload.prop === 'username' ? 'id' : 'createdAt'; order.value = payload.order === 'ascending' ? 'asc' : 'desc'; search() }

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

// The switch is optimistic; a rejected update is rolled back so the table never
// claims a requirement the server did not accept.
async function toggleMfaRequired(row: UserAccount, value: string | number | boolean) {
  const next = Boolean(value)
  try {
    const updated = await updateUserMFARequired(row.id, next)
    row.mfaRequired = Boolean(updated.mfaRequired)
    ElMessage.success(t('users.mfaRequiredUpdated'))
  } catch (error) {
    row.mfaRequired = !next
    reportActionError(error)
  }
}

// Resetting clears the enrollment and every trusted device, so it is confirmed
// explicitly and carries an idempotency key like every other admin mutation.
async function resetMfa(row: UserAccount) {
  try {
    await ElMessageBox.confirm(t('users.confirmResetMFA'), t('users.resetMFA'), { type: 'warning' })
    await resetUserMFA(row.id, crypto.randomUUID())
    ElMessage.success(t('users.mfaReset'))
  } catch (error) { reportActionError(error) }
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

<style scoped>.panel { overflow: hidden; } .toolbar { display: flex; justify-content: flex-end; gap: 12px; padding: 14px 16px; border-bottom: 1px solid var(--tm-border); } .keyword-input { width: min(240px, 100%); } .list-pager { display:flex; align-items:center; justify-content:center; gap:12px; padding:16px; } :deep(.el-table) { --el-table-border-color: var(--tm-line); }</style>
