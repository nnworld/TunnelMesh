<template>
  <section class="tm-page credentials-page">
    <PageHeader :title="t('credentials.title')" :description="t('credentials.description')">
      <el-button type="primary" @click="openCreate">{{ t('credentials.create') }}</el-button>
    </PageHeader>

    <div class="tm-card table-card">
      <div class="toolbar">
        <el-input v-model="keyword" class="keyword-input" clearable :placeholder="t('credentials.keyword')" @keyup.enter="reload" @clear="reload" />
        <el-segmented v-model="filterStatus" :options="statusOptions" @change="reload" />
        <el-button :loading="loading" @click="reload">{{ t('credentials.refresh') }}</el-button>
      </div>
      <DataState :loading="loading" :error="loadError" :empty="!items.length" :error-label="t('credentials.loadFailed')" :retry-label="t('common.retry')" :empty-label="t('credentials.empty')" @retry="reload">
        <el-table :data="items" row-key="id">
          <el-table-column prop="name" :label="t('credentials.name')" min-width="150" />
          <el-table-column :label="t('credentials.type')" width="140">
            <template #default="{ row }">{{ credentialTypeLabel(row.type) }}</template>
          </el-table-column>
          <el-table-column :label="t('credentials.publicKey')" min-width="260">
            <template #default="{ row }"><code v-if="row.publicKey" class="key-text" :title="row.publicKey">{{ row.publicKey }}</code><span v-else>—</span></template>
          </el-table-column>
          <el-table-column :label="t('credentials.fingerprint')" min-width="200">
            <template #default="{ row }"><span v-if="row.fingerprint">{{ row.fingerprint }}</span><span v-else>—</span></template>
          </el-table-column>
          <!-- Capability flag only: the server never returns the stored secret. -->
          <el-table-column :label="t('credentials.secretColumn')" width="130">
            <template #default="{ row }"><StatusTag :kind="row.hasSecret ? 'success' : 'info'" :label="row.hasSecret ? t('credentials.secretStored') : t('credentials.secretNotStored')" /></template>
          </el-table-column>
          <el-table-column :label="t('credentials.enabled')" width="110">
            <template #default="{ row }"><StatusTag :kind="row.enabled ? 'success' : 'warning'" :label="row.enabled ? t('common.yes') : t('common.no')" /></template>
          </el-table-column>
          <el-table-column :label="t('credentials.status')" width="110">
            <template #default="{ row }"><StatusTag :kind="row.status === 'active' ? 'success' : 'info'" :label="row.status === 'active' ? t('credentials.active') : t('credentials.deleted')" /></template>
          </el-table-column>
          <el-table-column :label="t('credentials.actions')" width="230" fixed="right">
            <template #default="{ row }">
              <el-button link type="primary" @click="showDetail(row)">{{ t('credentials.detail') }}</el-button>
              <el-button v-if="row.status === 'active'" link type="primary" @click="openEdit(row)">{{ t('credentials.edit') }}</el-button>
              <el-button v-if="row.status === 'active'" link type="danger" @click="remove(row)">{{ t('credentials.delete') }}</el-button>
              <el-button v-else link type="primary" @click="restore(row)">{{ t('credentials.restore') }}</el-button>
            </template>
          </el-table-column>
        </el-table>
      </DataState>
      <div v-if="page.nextCursor" class="load-more"><el-button :loading="loadingMore" :disabled="!page.hasMore" @click="loadMore">{{ t('credentials.loadMore') }}</el-button></div>
    </div>

    <el-dialog v-model="formVisible" :title="editingCredential ? t('credentials.edit') : t('credentials.create')" width="min(680px,94vw)" @closed="clearSensitiveKeyInput">
      <el-form ref="formRef" :model="form" :rules="rules" label-position="top" @submit.prevent="save">
        <el-form-item :label="t('credentials.name')" prop="name"><el-input v-model="form.name" maxlength="128" /></el-form-item>
        <el-form-item :label="t('credentials.type')" prop="type">
          <!-- The type decides which secret shape the server accepts, so it is
               locked while editing: switching it would silently drop the stored
               secret on the server side. -->
          <el-radio-group v-model="form.type" :disabled="Boolean(editingCredential)">
            <el-radio-button value="ssh_public_key">{{ t('credentials.types.sshPublicKey') }}</el-radio-button>
            <el-radio-button value="password">{{ t('credentials.types.password') }}</el-radio-button>
          </el-radio-group>
        </el-form-item>

        <template v-if="form.type === 'ssh_public_key'">
          <el-form-item :label="t('credentials.publicKey')" prop="publicKey"><el-input v-model="form.publicKey" type="textarea" :rows="3" spellcheck="false" /></el-form-item>
          <el-alert class="private-key-warning" type="warning" show-icon :title="privateKeyWarningText" :closable="false" />
          <el-form-item :label="t('credentials.privateKey')" prop="privateKey"><el-input v-model="form.privateKey" type="textarea" :rows="4" spellcheck="false" /></el-form-item>
          <el-form-item :label="t('credentials.passphrase')"><el-input v-model="form.passphrase" type="password" show-password autocomplete="new-password" /></el-form-item>
          <el-form-item :label="t('credentials.storePrivateKey')">
            <el-switch v-model="form.storePrivateKey" />
            <p class="field-help">{{ t('credentials.storePrivateKeyHelp') }}</p>
          </el-form-item>
          <el-alert v-if="extractError" type="error" show-icon :title="extractError" :closable="false" />
          <p v-if="extractedFingerprint" class="extract-result">{{ t('credentials.fingerprint') }}: <code>{{ extractedFingerprint }}</code></p>
        </template>

        <template v-else>
          <el-form-item :label="t('credentials.password')" prop="secretPassword">
            <el-input v-model="form.secretPassword" type="password" show-password autocomplete="new-password" :placeholder="secretPasswordPlaceholder" />
          </el-form-item>
          <p class="field-help">{{ t('credentials.passwordHelp') }}</p>
        </template>

        <el-alert v-if="editingCredential?.hasSecret && !pendingSecret" type="info" show-icon :title="t('credentials.secretKept')" :closable="false" />
      </el-form>
      <template #footer>
        <el-button v-if="form.type === 'ssh_public_key'" :loading="extracting" @click="extractKey">{{ t('credentials.extract') }}</el-button>
        <el-button @click="formVisible = false">{{ t('credentials.cancel') }}</el-button>
        <el-button type="primary" :loading="saving" @click="save">{{ t('credentials.save') }}</el-button>
      </template>
    </el-dialog>

    <el-drawer v-model="detailVisible" :title="t('credentials.detailTitle')" size="min(460px,100vw)">
      <DataState :loading="detailLoading" :error="detailError" :empty="!detailCredential" :error-label="t('credentials.loadFailed')" :retry-label="t('common.retry')" :empty-label="t('credentials.empty')" @retry="retryDetail">
        <dl v-if="detailCredential" class="detail-list">
          <dt>{{ t('credentials.name') }}</dt><dd>{{ detailCredential.name }}</dd>
          <dt>{{ t('credentials.type') }}</dt><dd>{{ credentialTypeLabel(detailCredential.type) }}</dd>
          <dt>{{ t('credentials.publicKey') }}</dt><dd><code v-if="detailCredential.publicKey">{{ detailCredential.publicKey }}</code><span v-else>—</span></dd>
          <dt>{{ t('credentials.fingerprint') }}</dt><dd>{{ detailCredential.fingerprint || '—' }}</dd>
          <dt>{{ t('credentials.secretColumn') }}</dt><dd>{{ detailCredential.hasSecret ? t('credentials.secretStored') : t('credentials.secretNotStored') }}</dd>
          <dt>{{ t('credentials.enabled') }}</dt><dd>{{ detailCredential.enabled ? t('common.yes') : t('common.no') }}</dd>
          <dt>{{ t('credentials.createdAt') }}</dt><dd>{{ formatDate(detailCredential.createdAt) }}</dd>
          <dt>{{ t('credentials.updatedAt') }}</dt><dd>{{ formatDate(detailCredential.updatedAt) }}</dd>
          <dt v-if="detailCredential.deletedAt">{{ t('credentials.deletedAt') }}</dt><dd v-if="detailCredential.deletedAt">{{ formatDate(detailCredential.deletedAt) }}</dd>
        </dl>
      </DataState>
    </el-drawer>
  </section>
</template>

<script setup lang="ts">
import { computed, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox, type FormInstance, type FormRules } from 'element-plus'
import { useI18n } from 'vue-i18n'
import PageHeader from '../components/PageHeader.vue'
import DataState from '../components/DataState.vue'
import StatusTag from '../components/StatusTag.vue'
import { useFormatDateTime } from '../i18n/format'
import {
  createCredential, deleteCredential, extractSSHPublicKey, getCredential, listCredentials,
  restoreCredential, updateCredential,
  type Credential, type CredentialInput, type CredentialSecretInput, type CredentialType,
} from '../api/credentials'
import { APIError } from '../api/client'

type StatusFilter = 'active' | 'deleted' | 'all'

const { t } = useI18n()
const formatDate = useFormatDateTime()
const page = ref<{ items: Credential[]; nextCursor?: string; hasMore?: boolean }>({ items: [] })
const items = computed(() => page.value.items)
const loading = ref(false)
const loadingMore = ref(false)
const loadError = ref(false)
const keyword = ref('')
const filterStatus = ref<StatusFilter>('active')
const statusOptions = computed(() => [
  { label: t('credentials.active'), value: 'active' },
  { label: t('credentials.deleted'), value: 'deleted' },
  { label: t('credentials.all'), value: 'all' },
])

const formRef = ref<FormInstance>()
const formVisible = ref(false)
const editingCredential = ref<Credential | null>(null)
const saving = ref(false)
const extracting = ref(false)
const extractError = ref('')
const extractedFingerprint = ref('')
const form = reactive({
  name: '', type: 'ssh_public_key' as CredentialType, publicKey: '', privateKey: '', passphrase: '',
  storePrivateKey: false, secretPassword: '', enabled: true,
})

// A secret is uploaded only when the user actually typed one. Leaving the field
// empty while editing keeps the stored material, which is what the server does
// too: a PATCH without `secret` never clears it.
const pendingSecret = computed<CredentialSecretInput | null>(() => {
  if (form.type === 'password') return form.secretPassword ? { password: form.secretPassword } : null
  if (!form.storePrivateKey || !form.privateKey.trim()) return null
  return { privateKey: form.privateKey, passphrase: form.passphrase || undefined }
})

const privateKeyWarningText = computed(() => (
  form.storePrivateKey ? t('credentials.privateKeyStoredWarning') : t('credentials.privateKeyWarning')
))
const secretPasswordPlaceholder = computed(() => (
  editingCredential.value?.hasSecret ? t('credentials.secretKept') : ''
))

function credentialTypeLabel(type: CredentialType | string) {
  return type === 'password' ? t('credentials.types.password') : t('credentials.types.sshPublicKey')
}

// Both rules are functions because the required field depends on the selected
// type: a password credential has no public key and a key credential has no
// password.
function validatePublicKey(_rule: unknown, value: unknown, callback: (error?: Error) => void) {
  if (form.type === 'ssh_public_key' && !String(value ?? '').trim()) callback(new Error(t('credentials.publicKeyRequired')))
  else callback()
}

function validateSecretPassword(_rule: unknown, value: unknown, callback: (error?: Error) => void) {
  const needsPassword = form.type === 'password' && !editingCredential.value?.hasSecret
  if (needsPassword && !String(value ?? '').trim()) callback(new Error(t('credentials.passwordRequired')))
  else callback()
}

const rules = reactive<FormRules>({
  name: [{ required: true, message: t('credentials.name'), trigger: 'blur' }],
  publicKey: [{ validator: validatePublicKey, trigger: 'blur' }],
  secretPassword: [{ validator: validateSecretPassword, trigger: 'blur' }],
})
const detailVisible = ref(false)
const detailLoading = ref(false)
const detailError = ref(false)
const detailCredential = ref<Credential | null>(null)

async function load(cursor?: string) {
  const appending = Boolean(cursor)
  appending ? loadingMore.value = true : loading.value = true
  if (!appending) loadError.value = false
  try {
    const result = await listCredentials({
      keyword: keyword.value || undefined,
      status: filterStatus.value,
      cursor,
      limit: 20,
    })
    page.value = appending ? {
      items: [...page.value.items, ...result.items],
      nextCursor: result.nextCursor,
      hasMore: result.hasMore,
    } : result
  } catch {
    if (!appending) loadError.value = true
    ElMessage.error(t('credentials.loadFailed'))
  } finally {
    loading.value = false
    loadingMore.value = false
  }
}

function reload() { page.value = { items: [] }; void load() }
function loadMore() { if (page.value.nextCursor) void load(page.value.nextCursor) }

// Values only: the credential type and the "store private key" choice are form
// state rather than secret material, so extraction can wipe the key without
// resetting what the user asked for.
function clearSecretValues() {
  form.privateKey = ''
  form.passphrase = ''
  form.secretPassword = ''
}

function clearSensitiveKeyInput() {
  clearSecretValues()
  form.storePrivateKey = false
}

function resetForm() {
  form.name = ''
  form.type = 'ssh_public_key'
  form.publicKey = ''
  form.enabled = true
  extractError.value = ''
  extractedFingerprint.value = ''
  clearSensitiveKeyInput()
  formRef.value?.clearValidate()
}

function openCreate() {
  editingCredential.value = null
  resetForm()
  formVisible.value = true
}

async function openEdit(row: Credential) {
  editingCredential.value = row
  resetForm()
  form.name = row.name
  form.type = row.type === 'password' ? 'password' : 'ssh_public_key'
  form.publicKey = row.publicKey
  form.enabled = row.enabled
  formVisible.value = true
}

async function extractKey() {
  if (!form.privateKey.trim()) {
    extractError.value = t('credentials.extractFailed')
    return
  }
  const privateKey = form.privateKey
  const passphrase = form.passphrase
  // Storing the key for auto-authentication needs it in the form, so it is only
  // wiped when the user asked for extraction alone.
  if (!form.storePrivateKey) clearSecretValues()
  extracting.value = true
  extractError.value = ''
  try {
    const extracted = await extractSSHPublicKey(privateKey, passphrase || undefined)
    form.publicKey = extracted.publicKey
    extractedFingerprint.value = extracted.fingerprint
    ElMessage.success(t('credentials.extractSuccess'))
  } catch {
    extractError.value = t('credentials.extractFailed')
  } finally {
    extracting.value = false
    if (!form.storePrivateKey) clearSecretValues()
  }
}

async function save() {
  const valid = await formRef.value?.validate().catch(() => false)
  if (!valid) return
  saving.value = true
  try {
    const input: CredentialInput = {
      name: form.name.trim(),
      type: form.type,
      publicKey: form.type === 'ssh_public_key' ? form.publicKey.trim() : '',
      enabled: form.enabled,
      ...(pendingSecret.value ? { secret: pendingSecret.value } : {}),
    }
    const saved = editingCredential.value
      ? await updateCredential(editingCredential.value.id, input, crypto.randomUUID())
      : await createCredential(input, crypto.randomUUID())
    formVisible.value = false
    ElMessage.success(t(editingCredential.value ? 'credentials.updated' : 'credentials.created'))
    if (page.value.items.some(item => item.id === saved.id)) await reload()
  } catch (error) {
    // A rejected secret (no encryption key configured, oversized value, type
    // mismatch) carries a specific server reason the generic text would hide.
    ElMessage.error(error instanceof APIError && error.message ? error.message : t('credentials.operationFailed'))
  } finally {
    saving.value = false
    clearSensitiveKeyInput()
  }
}

async function showDetail(row: Credential) {
  detailVisible.value = true
  detailLoading.value = true
  detailError.value = false
  detailCredential.value = row
  try { detailCredential.value = await getCredential(row.id) }
  catch { detailError.value = true }
  finally { detailLoading.value = false }
}

async function retryDetail() { if (detailCredential.value) await showDetail(detailCredential.value) }

async function remove(row: Credential) {
  try {
    await ElMessageBox.confirm(t('credentials.deleteConfirm'), t('credentials.deleteTitle'), { type: 'warning' })
    await deleteCredential(row.id)
    await reload()
    ElMessage.success(t('credentials.deletedMessage'))
  } catch (error) { reportActionError(error) }
}

async function restore(row: Credential) {
  try {
    await restoreCredential(row.id)
    await reload()
    ElMessage.success(t('credentials.restored'))
  } catch (error) { reportActionError(error) }
}

function reportActionError(error: unknown) {
  if (error === 'cancel' || error === 'close') return
  ElMessage.error(t('credentials.operationFailed'))
}

reload()
</script>

<style scoped>
.toolbar{display:flex;gap:12px;align-items:center;flex-wrap:wrap;padding:14px 16px;border-bottom:1px solid var(--tm-border)}.keyword-input{width:min(280px,100%)}.load-more{display:flex;justify-content:center;padding:14px}.key-text{display:block;overflow:hidden;max-width:340px;text-overflow:ellipsis;white-space:nowrap}.private-key-warning{margin:4px 0 16px}.extract-result{margin:0 0 12px;color:var(--tm-muted);font-size:13px;word-break:break-all}.detail-list{display:grid;grid-template-columns:110px 1fr;gap:12px 16px;margin:0}.detail-list dt{color:var(--tm-muted);font-weight:600}.detail-list dd{margin:0;word-break:break-all}:deep(.el-table){--el-table-border-color:#edf1f7}
.field-help{margin:6px 0 0;color:var(--tm-muted);font-size:12px;line-height:1.5}
</style>
