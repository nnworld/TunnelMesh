<template>
  <section class="tm-page">
    <PageHeader :title="t('security.title')" :description="t('security.description')" />

    <div class="tm-card form-card">
      <el-form label-position="top" @submit.prevent="submit">
        <el-form-item :label="t('security.currentPassword')"><el-input v-model="form.current" type="password" show-password autocomplete="current-password" /></el-form-item>
        <el-form-item :label="t('security.newPassword')"><el-input v-model="form.next" type="password" show-password autocomplete="new-password" /></el-form-item>
        <el-form-item :label="t('security.confirmPassword')"><el-input v-model="form.confirm" type="password" show-password autocomplete="new-password" /></el-form-item>
        <el-button type="primary" :loading="saving" native-type="submit">{{ t('security.save') }}</el-button>
      </el-form>
    </div>

    <div class="tm-card panel">
      <div class="panel-head">
        <div><h2>{{ t('security.mfa.title') }}</h2><p class="hint">{{ t('security.mfa.description') }}</p></div>
        <StatusTag v-if="mfa" :kind="statusKind" :label="statusLabel" />
      </div>
      <DataState :loading="mfaLoading" :error="mfaLoadError" :error-label="t('security.mfa.loadFailed')" :retry-label="t('common.retry')" @retry="loadMFA">
        <div class="panel-body">
          <dl v-if="mfa" class="detail-list">
            <dt>{{ t('security.mfa.statusLabel') }}</dt><dd data-test="mfa-status">{{ statusLabel }}</dd>
            <dt>{{ t('security.mfa.policyMode') }}</dt><dd>{{ policyLabel }}</dd>
            <template v-if="mfa.status === 'enabled'">
              <dt>{{ t('security.mfa.enabledAt') }}</dt><dd>{{ formatDate(mfa.enabledAt) }}</dd>
              <dt>{{ t('security.mfa.lastUsedAt') }}</dt><dd>{{ formatDate(mfa.lastUsedAt) }}</dd>
              <dt>{{ t('security.mfa.remainingCodes') }}</dt><dd data-test="remaining-codes">{{ mfa.remainingRecoveryCodes }}</dd>
            </template>
            <template v-else-if="enrollment">
              <dt>{{ t('security.mfa.expiresAt') }}</dt><dd>{{ formatDate(enrollment.expiresAt) }}</dd>
            </template>
          </dl>
          <el-alert v-if="mfa?.policy.required" class="notice" type="info" :closable="false" :title="t('security.mfa.policyRequiredHint')" show-icon />

          <div class="actions">
            <el-button v-if="mfa && mfa.status !== 'enabled'" type="primary" data-test="mfa-enroll" @click="openPanel('enroll')">{{ mfa.status === 'pending' ? t('security.mfa.restart') : t('security.mfa.enroll') }}</el-button>
            <el-button v-if="mfa?.status === 'enabled'" data-test="mfa-regenerate" @click="openPanel('regenerate')">{{ t('security.mfa.regenerate') }}</el-button>
            <el-button v-if="mfa?.status === 'enabled'" type="danger" plain data-test="mfa-disable" @click="openPanel('disable')">{{ t('security.mfa.disable') }}</el-button>
          </div>

          <el-form v-if="panel === 'enroll' && !enrollment" class="sub-form" label-position="top" @submit.prevent="enroll">
            <el-alert class="notice" type="info" :closable="false" :title="t('security.mfa.enrollHint')" />
            <el-form-item :label="t('security.currentPassword')">
              <el-input v-model="enrollPassword" type="password" show-password data-test="enroll-password" autocomplete="current-password" />
              <p class="hint">{{ t('security.mfa.currentPasswordHelp') }}</p>
            </el-form-item>
            <div class="actions">
              <el-button type="primary" :loading="working" data-test="mfa-enroll-submit" @click="enroll">{{ t('security.mfa.enroll') }}</el-button>
              <el-button link data-test="panel-cancel" @click="closePanel">{{ t('users.cancel') }}</el-button>
            </div>
          </el-form>

          <div v-if="enrollment" class="enrollment" data-test="enrollment">
            <dl class="detail-list">
              <dt>{{ t('security.mfa.enrollOtpauth') }}</dt>
              <dd class="secret-row"><code data-test="mfa-otpauth">{{ enrollment.otpauthUrl }}</code><el-button size="small" data-test="copy-otpauth" @click="copy(enrollment.otpauthUrl)">{{ t('security.mfa.copy') }}</el-button></dd>
              <dt>{{ t('security.mfa.enrollSecret') }}</dt>
              <dd class="secret-row"><code data-test="mfa-secret">{{ enrollment.secret }}</code><el-button size="small" data-test="copy-secret" @click="copy(enrollment.secret)">{{ t('security.mfa.copy') }}</el-button></dd>
            </dl>
          </div>

          <!-- Recovery codes exist exactly once: they are gated behind an explicit
               acknowledgement and wiped from memory as soon as MFA is active. -->
          <div v-if="recoveryCodes.length" class="codes">
            <el-checkbox v-model="acknowledged" data-test="recovery-ack">{{ t('security.mfa.recoveryAck') }}</el-checkbox>
            <div v-if="acknowledged" data-test="recovery-codes">
              <p class="codes-title">{{ t('security.mfa.recoveryTitle') }}</p>
              <ul class="code-list"><li v-for="item in recoveryCodes" :key="item"><code>{{ item }}</code></li></ul>
              <p class="hint">{{ t('security.mfa.recoveryHint') }}</p>
            </div>
          </div>

          <el-form v-if="panel === 'enroll' && enrollment" class="sub-form" label-position="top" @submit.prevent="enable">
            <el-form-item :label="t('security.mfa.code')"><el-input v-model="enableCode" data-test="mfa-enable-code" maxlength="64" autocomplete="one-time-code" /></el-form-item>
            <div class="actions">
              <el-button type="primary" :loading="working" :disabled="!acknowledged || !enableCode.trim()" data-test="mfa-enable" @click="enable">{{ t('security.mfa.enable') }}</el-button>
              <el-button link data-test="panel-cancel" @click="closePanel">{{ t('users.cancel') }}</el-button>
            </div>
          </el-form>

          <el-form v-else-if="panel === 'disable'" class="sub-form" label-position="top" @submit.prevent="disable">
            <p class="hint">{{ t('security.mfa.disableHint') }}</p>
            <el-form-item :label="t('security.mfa.disableConfirmCode')"><el-input v-model="disableCode" data-test="mfa-disable-code" maxlength="64" autocomplete="one-time-code" /></el-form-item>
            <div class="actions">
              <el-button type="danger" :loading="working" :disabled="!disableCode.trim()" data-test="mfa-disable-confirm" @click="disable">{{ t('security.mfa.disable') }}</el-button>
              <el-button link data-test="panel-cancel" @click="closePanel">{{ t('users.cancel') }}</el-button>
            </div>
          </el-form>

          <el-form v-else-if="panel === 'regenerate'" class="sub-form" label-position="top" @submit.prevent="regenerate">
            <p class="hint">{{ t('security.mfa.regenerateHint') }}</p>
            <el-form-item :label="t('security.mfa.code')"><el-input v-model="regenerateCode" data-test="mfa-regenerate-code" maxlength="64" autocomplete="one-time-code" /></el-form-item>
            <div class="actions">
              <el-button type="primary" :loading="working" :disabled="!regenerateCode.trim()" data-test="mfa-regenerate-confirm" @click="regenerate">{{ t('security.mfa.regenerate') }}</el-button>
              <el-button link data-test="panel-cancel" @click="closePanel">{{ t('users.cancel') }}</el-button>
            </div>
          </el-form>

          <el-alert v-if="mfaError" class="notice" type="error" :title="mfaError" show-icon :closable="false" data-test="mfa-error" />
        </div>
      </DataState>
    </div>

    <div class="tm-card panel">
      <div class="panel-head">
        <div><h2>{{ t('security.devices.title') }}</h2><p class="hint">{{ t('security.devices.description') }}</p></div>
        <el-button size="small" :loading="devicesLoading" @click="loadDevices">{{ t('security.devices.refresh') }}</el-button>
      </div>
      <DataState :loading="devicesLoading" :error="devicesLoadError" :empty="!devices.length" :error-label="t('security.devices.loadFailed')" :retry-label="t('common.retry')" :empty-label="t('security.devices.empty')" @retry="loadDevices">
        <el-table :data="devices" row-key="id">
          <el-table-column :label="t('security.devices.name')" min-width="200">
            <template #default="{ row }">
              <el-input v-if="renamingId === row.id" v-model="renameValue" size="small" maxlength="128" :data-test="`device-name-${row.id}`" />
              <span v-else>{{ row.name || '—' }}</span>
              <StatusTag v-if="row.current" class="current-tag" kind="success" :label="t('security.devices.current')" :data-test="`device-current-${row.id}`" />
            </template>
          </el-table-column>
          <el-table-column prop="ip" :label="t('security.devices.ip')" width="150" />
          <el-table-column :label="t('security.devices.userAgent')" min-width="200"><template #default="{ row }"><span class="ellipsis" :title="row.userAgent">{{ row.userAgent }}</span></template></el-table-column>
          <el-table-column :label="t('security.devices.lastSeenAt')" min-width="170"><template #default="{ row }">{{ formatDate(row.lastSeenAt) }}</template></el-table-column>
          <el-table-column :label="t('security.devices.expiresAt')" min-width="170"><template #default="{ row }">{{ formatDate(row.expiresAt) }}</template></el-table-column>
          <el-table-column :label="t('users.actions')" width="200" fixed="right">
            <template #default="{ row }">
              <template v-if="renamingId === row.id">
                <el-button link type="primary" :data-test="`device-rename-save-${row.id}`" @click="saveRename(row)">{{ t('security.devices.renameSave') }}</el-button>
                <el-button link @click="renamingId = ''">{{ t('users.cancel') }}</el-button>
              </template>
              <template v-else>
                <el-button link type="primary" :data-test="`device-rename-${row.id}`" @click="startRename(row)">{{ t('security.devices.rename') }}</el-button>
                <el-button link type="danger" :data-test="`device-revoke-${row.id}`" @click="revoke(row)">{{ t('security.devices.revoke') }}</el-button>
              </template>
            </template>
          </el-table-column>
        </el-table>
      </DataState>
      <el-alert v-if="deviceError" class="notice" type="error" :title="deviceError" show-icon :closable="false" data-test="device-error" />
    </div>

    <div class="tm-card panel">
      <div class="panel-head">
        <div><h2>{{ t('security.identities.title') }}</h2><p class="hint">{{ t('security.identities.description') }}</p></div>
      </div>
      <DataState :loading="identitiesLoading" :error="identitiesLoadError" :empty="!identities.length" :error-label="t('security.identities.loadFailed')" :retry-label="t('common.retry')" :empty-label="t('security.identities.empty')" @retry="loadIdentities">
        <el-table :data="identities" row-key="id">
          <el-table-column prop="providerName" :label="t('security.identities.provider')" min-width="150" />
          <el-table-column :label="t('security.identities.account')" min-width="220"><template #default="{ row }">{{ row.displayName || row.username || row.email || '—' }}<span v-if="row.email" class="muted"> {{ row.email }}</span></template></el-table-column>
          <el-table-column :label="t('security.identities.subject')" min-width="170"><template #default="{ row }"><span class="ellipsis" :title="row.subject">{{ row.subject }}</span></template></el-table-column>
          <el-table-column :label="t('security.identities.linkedAt')" min-width="170"><template #default="{ row }">{{ formatDate(row.linkedAt) }}</template></el-table-column>
          <el-table-column :label="t('security.identities.lastLoginAt')" min-width="170"><template #default="{ row }">{{ formatDate(row.lastLoginAt) }}</template></el-table-column>
          <el-table-column :label="t('users.actions')" width="130" fixed="right">
            <template #default="{ row }"><el-button link type="danger" :data-test="`identity-unlink-${row.id}`" @click="unlink(row)">{{ t('security.identities.unlink') }}</el-button></template>
          </el-table-column>
        </el-table>
      </DataState>
      <el-alert v-if="identityError" class="notice" type="error" :title="identityError" show-icon :closable="false" data-test="identity-error" />
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { useI18n } from 'vue-i18n'
import PageHeader from '../components/PageHeader.vue'
import DataState from '../components/DataState.vue'
import StatusTag from '../components/StatusTag.vue'
import { changePassword } from '../api/client'
import {
  disableMFA, enableMFA, enrollMFA, getMFAStatus, listDevices, listIdentities, regenerateRecoveryCodes,
  renameDevice, revokeDevice, unlinkIdentity,
} from '../api/auth'
import type { LinkedIdentity, MFAEnrollment, MFAStatus, TrustedDevice } from '../api/auth'
import { accountErrorMessage } from '../i18n/errors'
import { useFormatDateTime } from '../i18n/format'
import { authErrorMessage } from '../i18n/errors'

const { t } = useI18n()
const formatDate = useFormatDateTime()
const saving = ref(false)
const form = reactive({ current: '', next: '', confirm: '' })

const mfa = ref<MFAStatus | null>(null)
const mfaLoading = ref(false)
const mfaLoadError = ref(false)
const mfaError = ref('')
const panel = ref<'none' | 'enroll' | 'disable' | 'regenerate'>('none')
const working = ref(false)
const enrollPassword = ref('')
const enrollment = ref<MFAEnrollment | null>(null)
const recoveryCodes = ref<string[]>([])
const acknowledged = ref(false)
const enableCode = ref('')
const disableCode = ref('')
const regenerateCode = ref('')

const devices = ref<TrustedDevice[]>([])
const devicesLoading = ref(false)
const devicesLoadError = ref(false)
const deviceError = ref('')
const renamingId = ref('')
const renameValue = ref('')

const identities = ref<LinkedIdentity[]>([])
const identitiesLoading = ref(false)
const identitiesLoadError = ref(false)
const identityError = ref('')

const statusLabel = computed(() => t(statusKey(mfa.value?.status)))
const statusKind = computed(() => mfa.value?.status === 'enabled' ? 'success' : mfa.value?.status === 'pending' ? 'warning' : 'info')
const policyLabel = computed(() => t(policyKey(mfa.value?.policy.mode)))

function statusKey(status?: string) {
  return status === 'enabled' ? 'security.mfa.statusEnabled' : status === 'pending' ? 'security.mfa.statusPending' : 'security.mfa.statusNone'
}
function policyKey(mode?: string) {
  return mode === 'required' ? 'security.mfa.policyRequired' : mode === 'optional' ? 'security.mfa.policyOptional' : 'security.mfa.policyDisabled'
}

async function submit() {
  if (form.next.length < 12 || form.next.length > 128) { ElMessage.warning(t('security.length')); return }
  if (form.next !== form.confirm) { ElMessage.warning(t('security.mismatch')); return }
  saving.value = true
  try { await changePassword(form.current, form.next); form.current = form.next = form.confirm = ''; ElMessage.success(t('security.saved')) }
  catch (error) { ElMessage.error(accountErrorMessage(error)) }
  finally { saving.value = false }
}

async function loadMFA() {
  mfaLoading.value = true; mfaLoadError.value = false
  try { mfa.value = await getMFAStatus() }
  catch { mfaLoadError.value = true }
  finally { mfaLoading.value = false }
}

function openPanel(next: 'enroll' | 'disable' | 'regenerate') { panel.value = next; mfaError.value = ''; recoveryCodes.value = []; acknowledged.value = false }
// Leaving a panel always drops one-time material: a pending enrollment or a set
// of recovery codes must never survive in memory longer than the user needs it.
function closePanel() {
  panel.value = 'none'; enrollment.value = null; recoveryCodes.value = []
  acknowledged.value = false; enrollPassword.value = ''; enableCode.value = ''
  disableCode.value = ''; regenerateCode.value = ''; mfaError.value = ''
}

async function enroll() {
  working.value = true; mfaError.value = ''
  try {
    enrollment.value = await enrollMFA(enrollPassword.value || undefined)
    recoveryCodes.value = enrollment.value.recoveryCodes
    acknowledged.value = false
    enrollPassword.value = ''
  } catch (error) { mfaError.value = authErrorMessage(error, 'security.mfa.enrollFailed') }
  finally { working.value = false }
}

async function enable() {
  if (!acknowledged.value || !enableCode.value.trim()) return
  working.value = true; mfaError.value = ''
  try {
    await enableMFA(enableCode.value.trim())
    closePanel()
    await loadMFA()
    ElMessage.success(t('security.mfa.enabled'))
  } catch (error) { mfaError.value = authErrorMessage(error, 'security.mfa.enableFailed') }
  finally { working.value = false }
}

async function disable() {
  if (!disableCode.value.trim()) return
  working.value = true; mfaError.value = ''
  try {
    await disableMFA(disableCode.value.trim())
    closePanel()
    await Promise.all([loadMFA(), loadDevices()])
    ElMessage.success(t('security.mfa.disabled'))
  } catch (error) { mfaError.value = authErrorMessage(error, 'security.mfa.disableFailed') }
  finally { working.value = false }
}

async function regenerate() {
  if (!regenerateCode.value.trim()) return
  working.value = true; mfaError.value = ''
  try {
    const result = await regenerateRecoveryCodes(regenerateCode.value.trim())
    recoveryCodes.value = result.recoveryCodes
    acknowledged.value = false
    regenerateCode.value = ''
    await loadMFA()
    ElMessage.success(t('security.mfa.regenerated'))
  } catch (error) { mfaError.value = authErrorMessage(error, 'security.mfa.regenerateFailed') }
  finally { working.value = false }
}

async function copy(value: string) {
  try { await navigator.clipboard?.writeText(value); ElMessage.success(t('security.mfa.copied')) }
  catch { ElMessage.warning(t('security.mfa.copy')) }
}

async function loadDevices() {
  devicesLoading.value = true; devicesLoadError.value = false
  // max_trusted_devices is capped at 100 by policy, so one page covers every
  // device the account can hold and the list never needs a pager.
  try { devices.value = (await listDevices({ limit: 100 })).items }
  catch { devicesLoadError.value = true }
  finally { devicesLoading.value = false; renamingId.value = '' }
}

function startRename(row: TrustedDevice) { renamingId.value = row.id; renameValue.value = row.name; deviceError.value = '' }

async function saveRename(row: TrustedDevice) {
  const name = renameValue.value.trim()
  if (!name) return
  deviceError.value = ''
  try {
    const renamed = await renameDevice(row.id, name)
    // Updating the row in place keeps the table stable; a full reload would only
    // re-read data this response already carries.
    row.name = renamed.name
    renamingId.value = ''
    ElMessage.success(t('security.devices.renamed'))
  } catch (error) { deviceError.value = deviceErrorMessage(error) }
}

async function revoke(row: TrustedDevice) {
  deviceError.value = ''
  try { await revokeDevice(row.id); await loadDevices(); ElMessage.success(t('security.devices.revoked')) }
  catch (error) { deviceError.value = deviceErrorMessage(error); await loadDevices() }
}

function deviceErrorMessage(error: unknown) {
  return authErrorMessage(error, 'security.devices.revokeFailed')
}

async function loadIdentities() {
  identitiesLoading.value = true; identitiesLoadError.value = false
  try { identities.value = await listIdentities() }
  catch { identitiesLoadError.value = true }
  finally { identitiesLoading.value = false }
}

async function unlink(row: LinkedIdentity) {
  identityError.value = ''
  try { await unlinkIdentity(row.id); await loadIdentities(); ElMessage.success(t('security.identities.unlinked')) }
  catch (error) {
    identityError.value = authErrorMessage(error, 'security.identities.unlinkFailed')
  }
}

onMounted(() => { void loadMFA(); void loadDevices(); void loadIdentities() })
</script>

<style scoped>.form-card { max-width: 620px; padding: 24px; }
.panel { padding: 20px 24px; }
.panel-head { display: flex; align-items: flex-start; justify-content: space-between; gap: 16px; }
.panel-head h2 { margin: 0; font-size: 17px; }
.panel-body { padding-top: 16px; }
.hint { margin: 4px 0 0; color: var(--tm-muted); font-size: 13px; line-height: 1.5; }
.muted { color: var(--tm-muted); }
.notice { margin-top: 14px; }
.actions { display: flex; gap: 8px; align-items: center; margin-top: 16px; }
.sub-form { max-width: 460px; margin-top: 18px; padding-top: 16px; border-top: 1px dashed var(--tm-border); }
.detail-list { display: grid; grid-template-columns: 150px minmax(0, 1fr); gap: 10px 16px; margin: 0; }
.detail-list dt { color: var(--tm-muted); font-weight: 600; font-size: 13px; }
.detail-list dd { margin: 0; font-size: 13px; word-break: break-all; }
.secret-row { display: flex; gap: 10px; align-items: center; }
.secret-row code { flex: 1; padding: 8px 10px; border: 1px solid var(--tm-border); border-radius: 6px; background: #f7f9fc; font: 600 12px ui-monospace, SFMono-Regular, Menlo, monospace; }
.codes { margin-top: 18px; }
.codes-title { margin: 12px 0 8px; font-weight: 650; }
.code-list { display: grid; grid-template-columns: repeat(auto-fill, minmax(220px, 1fr)); gap: 8px; margin: 0; padding: 0; list-style: none; }
.code-list code { display: block; padding: 8px 10px; border: 1px solid var(--tm-border); border-radius: 6px; background: #f7f9fc; font: 600 12px ui-monospace, SFMono-Regular, Menlo, monospace; }
.enrollment { margin-top: 18px; }
.current-tag { margin-left: 8px; }
.ellipsis { display: block; overflow: hidden; max-width: 260px; text-overflow: ellipsis; white-space: nowrap; }
:deep(.el-table) { --el-table-border-color: #edf1f7; }</style>
