<template>
  <section class="tm-page sso-page">
    <PageHeader :title="t('sso.title')" :description="t('sso.description')">
      <el-button type="primary" data-test="provider-create" @click="openCreate">{{ t('sso.create') }}</el-button>
    </PageHeader>

    <div class="tm-card panel">
      <div class="panel-head"><h2>{{ t('sso.policyTitle') }}</h2><p class="hint">{{ t('sso.policyDescription') }}</p></div>
      <DataState :loading="policyLoading" :error="policyLoadError" :error-label="t('sso.policyLoadFailed')" :retry-label="t('common.retry')" @retry="loadPolicy">
        <el-form class="policy-form" label-position="top" @submit.prevent="savePolicy">
          <el-form-item :label="t('sso.policyMfaMode')">
            <el-radio-group v-model="policy.mfaMode">
              <el-radio-button value="disabled" data-test="policy-mfa-disabled">{{ t('sso.policyMfaDisabled') }}</el-radio-button>
              <el-radio-button value="optional" data-test="policy-mfa-optional">{{ t('sso.policyMfaOptional') }}</el-radio-button>
              <el-radio-button value="required" data-test="policy-mfa-required">{{ t('sso.policyMfaRequired') }}</el-radio-button>
            </el-radio-group>
          </el-form-item>
          <el-form-item :label="t('sso.policyDeviceTrust')">
            <el-switch v-model="policy.deviceTrustEnabled" data-test="policy-device-trust" />
            <p class="hint">{{ t('sso.policyDeviceTrustHelp') }}</p>
          </el-form-item>
          <el-form-item :label="t('sso.policyAllowBypass')"><el-switch v-model="policy.allowTrustedDeviceBypass" data-test="policy-allow-bypass" /></el-form-item>
          <el-form-item :label="t('sso.policyDeviceTtl')"><el-input-number v-model="policy.deviceTrustTtlSeconds" :min="3600" :max="7776000" :step="3600" data-test="policy-device-ttl" /></el-form-item>
          <el-form-item :label="t('sso.policyMaxDevices')"><el-input-number v-model="policy.maxTrustedDevices" :min="1" :max="100" data-test="policy-max-devices" /></el-form-item>
          <el-form-item :label="t('sso.policySessionTtl')"><el-input-number v-model="policy.sessionTokenTtlSeconds" :min="0" :step="600" data-test="policy-session-ttl" /></el-form-item>
          <div class="actions">
            <el-button type="primary" :loading="policySaving" data-test="save-policy" @click="savePolicy">{{ t('sso.policySave') }}</el-button>
          </div>
          <el-alert v-if="policyError" class="notice" type="error" :title="policyError" show-icon :closable="false" data-test="policy-error" />
        </el-form>
      </DataState>
    </div>

    <div class="tm-card panel table-panel">
      <div class="toolbar"><el-button :loading="loading" @click="reload">{{ t('sso.refresh') }}</el-button></div>
      <DataState :loading="loading" :error="loadError" :empty="!items.length" :error-label="t('sso.loadFailed')" :retry-label="t('common.retry')" :empty-label="t('sso.empty')" @retry="reload">
        <el-table :data="items" row-key="id">
          <el-table-column prop="displayName" :label="t('sso.displayName')" min-width="150" />
          <el-table-column prop="name" :label="t('sso.name')" width="130" />
          <el-table-column :label="t('sso.issuer')" min-width="240"><template #default="{ row }"><span class="ellipsis" :title="row.issuer">{{ row.issuer }}</span></template></el-table-column>
          <el-table-column :label="t('sso.clientId')" min-width="180"><template #default="{ row }"><span class="ellipsis" :title="row.clientId">{{ row.clientId }}</span></template></el-table-column>
          <!-- Capability flag only: ProviderView never carries the secret, so the
               column states whether one is stored and nothing else. -->
          <el-table-column :label="t('sso.secretColumn')" width="110">
            <template #default="{ row }"><StatusTag :kind="row.hasSecret ? 'success' : 'info'" :label="row.hasSecret ? t('sso.secretStored') : t('sso.secretNotStored')" /></template>
          </el-table-column>
          <el-table-column :label="t('sso.enabled')" width="100">
            <template #default="{ row }"><StatusTag :kind="row.enabled ? 'success' : 'warning'" :label="row.enabled ? t('common.yes') : t('common.no')" /></template>
          </el-table-column>
          <el-table-column :label="t('sso.publicListed')" width="120">
            <template #default="{ row }">{{ row.publicListed ? t('common.yes') : t('common.no') }}</template>
          </el-table-column>
          <el-table-column :label="t('sso.defaultRole')" width="110">
            <template #default="{ row }">{{ row.defaultRole === 'admin' ? t('sso.roleAdmin') : t('sso.roleUser') }}</template>
          </el-table-column>
          <el-table-column :label="t('sso.updatedAt')" min-width="170"><template #default="{ row }">{{ formatDate(row.updatedAt) }}</template></el-table-column>
          <el-table-column :label="t('sso.actions')" width="240" fixed="right">
            <template #default="{ row }">
              <el-button link type="primary" :data-test="`provider-edit-${row.id}`" @click="openEdit(row)">{{ t('sso.edit') }}</el-button>
              <el-button link type="primary" :data-test="`provider-test-${row.id}`" @click="runTest(row)">{{ t('sso.test') }}</el-button>
              <el-button link type="danger" :data-test="`provider-delete-${row.id}`" @click="openDelete(row)">{{ t('sso.delete') }}</el-button>
            </template>
          </el-table-column>
        </el-table>
      </DataState>
      <div v-if="page.nextCursor" class="load-more"><el-button :loading="loadingMore" :disabled="!page.hasMore" @click="loadMore">{{ t('sso.loadMore') }}</el-button></div>
    </div>

    <el-dialog v-model="formVisible" :title="editing ? t('sso.edit') : t('sso.create')" width="min(720px,94vw)">
      <el-form label-position="top" @submit.prevent="save">
        <el-form-item :label="t('sso.name')">
          <!-- The slug is part of the public callback URL, so it is locked once
               created: changing it would silently break the registered redirect. -->
          <el-input v-model="form.name" :disabled="Boolean(editing)" data-test="provider-name" maxlength="63" />
          <p class="hint">{{ t('sso.nameHelp') }}</p>
        </el-form-item>
        <el-form-item :label="t('sso.displayName')"><el-input v-model="form.displayName" data-test="provider-display-name" maxlength="128" /></el-form-item>
        <el-form-item :label="t('sso.issuer')">
          <el-input v-model="form.issuer" data-test="provider-issuer" placeholder="https://idp.example.com" spellcheck="false" />
          <p class="hint">{{ t('sso.issuerHelp') }}</p>
        </el-form-item>
        <el-form-item :label="t('sso.clientId')"><el-input v-model="form.clientId" data-test="provider-client-id" spellcheck="false" /></el-form-item>
        <el-form-item :label="t('sso.clientSecret')">
          <el-input v-model="form.clientSecret" type="password" show-password data-test="provider-secret" autocomplete="off" :placeholder="editing ? t('sso.secretPlaceholderKeep') : t('sso.secretPlaceholderNew')" />
          <p class="hint">{{ editing ? t('sso.secretKept') : t('sso.secretPlaceholderNew') }}</p>
        </el-form-item>
        <el-form-item :label="t('sso.scopes')">
          <el-input v-model="form.scopesText" data-test="provider-scopes" placeholder="openid, profile, email" spellcheck="false" />
          <p class="hint">{{ t('sso.scopesHelp') }}</p>
        </el-form-item>
        <el-form-item :label="t('sso.redirectUri')">
          <el-input v-model="form.redirectUri" data-test="provider-redirect-uri" spellcheck="false" />
          <p class="hint">{{ t('sso.redirectUriHelp') }}</p>
        </el-form-item>
        <el-form-item :label="t('sso.idTokenAlgs')">
          <el-input v-model="form.algsText" data-test="provider-algs" placeholder="RS256" spellcheck="false" />
          <p class="hint">{{ t('sso.idTokenAlgsHelp') }}</p>
        </el-form-item>
        <el-form-item :label="t('sso.usernameClaim')"><el-input v-model="form.usernameClaim" data-test="provider-username-claim" placeholder="preferred_username" spellcheck="false" /></el-form-item>
        <el-form-item :label="t('sso.defaultRole')">
          <el-radio-group v-model="form.defaultRole">
            <el-radio-button value="user" data-test="default-role-user">{{ t('sso.roleUser') }}</el-radio-button>
            <el-radio-button value="admin" data-test="default-role-admin">{{ t('sso.roleAdmin') }}</el-radio-button>
          </el-radio-group>
        </el-form-item>
        <el-form-item :label="t('sso.roleMappings')">
          <div class="mappings">
            <div v-for="(mapping, index) in form.roleMappings" :key="index" class="mapping-row">
              <el-input v-model="mapping.claim" size="small" :data-test="`mapping-claim-${index}`" :placeholder="t('sso.mappingClaim')" />
              <el-input v-model="mapping.value" size="small" :data-test="`mapping-value-${index}`" :placeholder="t('sso.mappingValue')" />
              <el-select v-model="mapping.role" size="small" class="mapping-role" :data-test="`mapping-role-${index}`">
                <el-option :label="t('sso.roleUser')" value="user" /><el-option :label="t('sso.roleAdmin')" value="admin" />
              </el-select>
              <el-button link type="danger" size="small" :data-test="`mapping-remove-${index}`" @click="form.roleMappings.splice(index, 1)">{{ t('sso.delete') }}</el-button>
            </div>
            <div class="actions"><el-button size="small" data-test="mapping-add" @click="form.roleMappings.push({ claim: '', value: '', role: 'user' })">{{ t('sso.mappingAdd') }}</el-button></div>
            <p class="hint">{{ t('sso.mappingHelp') }}</p>
          </div>
        </el-form-item>
        <el-form-item :label="t('sso.autoCreateUsers')"><el-switch v-model="form.autoCreateUsers" data-test="provider-auto-create" /></el-form-item>
        <el-form-item :label="t('sso.publicListed')"><el-switch v-model="form.publicListed" data-test="provider-public-listed" /></el-form-item>
        <el-form-item :label="t('sso.authoritativeRoles')"><el-switch v-model="form.authoritativeRoles" data-test="provider-authoritative" /></el-form-item>
        <el-form-item :label="t('sso.enabled')"><el-switch v-model="form.enabled" data-test="provider-enabled" /></el-form-item>
      </el-form>
      <div v-if="formError" class="form-error" data-test="provider-form-error">{{ formError }}</div>
      <template #footer>
        <el-button @click="formVisible = false">{{ t('sso.cancel') }}</el-button>
        <el-button type="primary" :loading="saving" data-test="provider-save" @click="save">{{ t('sso.save') }}</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="deleteVisible" :title="t('sso.deleteTitle')" width="min(520px,94vw)">
      <p class="confirm-text"><strong>{{ deleteTarget?.displayName }}</strong></p>
      <p class="confirm-text">{{ t('sso.deleteConfirm') }}</p>
      <div v-if="deleteError" class="form-error" data-test="provider-delete-error">{{ deleteError }}</div>
      <template #footer>
        <el-button @click="deleteVisible = false">{{ t('sso.cancel') }}</el-button>
        <el-button type="danger" :loading="deleting" data-test="confirm-delete-provider" @click="confirmDelete">{{ t('sso.delete') }}</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="testVisible" :title="t('sso.testTitle')" width="min(640px,94vw)">
      <DataState :loading="testing" :error="testLoadError" :error-label="t('sso.testFailedLoad')" :retry-label="t('common.retry')" @retry="retryTest">
        <div v-if="testReport" data-test="test-report">
          <el-alert class="notice" :type="reportOk ? 'success' : 'error'" :closable="false" :title="reportOk ? t('sso.testOk') : t('sso.testFailed')" show-icon />
          <dl class="detail-list">
            <dt>{{ t('sso.testDiscoveryOk') }}</dt><dd><StatusTag :kind="testReport.discoveryOk ? 'success' : 'danger'" :label="testReport.discoveryOk ? t('common.yes') : t('common.no')" /></dd>
            <dt>{{ t('sso.testJwksOk') }}</dt><dd><StatusTag :kind="testReport.jwksOk ? 'success' : 'danger'" :label="testReport.jwksOk ? t('common.yes') : t('common.no')" /></dd>
            <dt>{{ t('sso.testAlgorithms') }}</dt><dd data-test="test-algorithms">{{ testReport.algorithms.join(', ') || '—' }}</dd>
            <dt>{{ t('sso.testEndpoints') }}</dt>
            <dd>
              <ul v-if="endpointEntries.length" class="endpoints">
                <li v-for="[name, value] in endpointEntries" :key="name"><code>{{ name }}</code><span class="ellipsis">{{ value }}</span></li>
              </ul>
              <span v-else>—</span>
            </dd>
          </dl>
          <el-alert v-if="testReport.error" class="notice" type="error" :closable="false" :title="testReport.error" data-test="test-error" />
        </div>
      </DataState>
      <template #footer><el-button type="primary" @click="testVisible = false">{{ t('sso.close') }}</el-button></template>
    </el-dialog>
  </section>
</template>

<script setup lang="ts">
import { computed, reactive, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { useI18n } from 'vue-i18n'
import PageHeader from '../components/PageHeader.vue'
import DataState from '../components/DataState.vue'
import StatusTag from '../components/StatusTag.vue'
import {
  createSSOProvider, deleteSSOProvider, getAuthPolicy, getSSOProviders, testSSOProvider, updateAuthPolicy, updateSSOProvider,
} from '../api/auth'
import type { AuthPolicy, RoleMapping, SSOProvider, SSOProviderInput, SSOProviderPage, SSOProviderTestReport } from '../api/auth'
import { authErrorMessage } from '../i18n/errors'
import { useFormatDateTime } from '../i18n/format'

// Mirrors the server-side provider validation: rejecting here keeps an obvious
// mistake off the wire, and the accepted algorithm set is the same closed list
// because alg=none and every HS* algorithm are rejected unconditionally.
const NAME_PATTERN = /^[a-z0-9][a-z0-9-]{1,62}$/
const ACCEPTED_ALGS = ['RS256', 'RS384', 'RS512', 'ES256', 'ES384', 'ES512', 'EdDSA']

const { t } = useI18n()
const formatDate = useFormatDateTime()

const page = ref<SSOProviderPage>({ items: [] })
const items = computed(() => page.value.items)
const loading = ref(false)
const loadingMore = ref(false)
const loadError = ref(false)

const policy = reactive<AuthPolicy>({ mfaMode: 'disabled', deviceTrustEnabled: false, deviceTrustTtlSeconds: 2592000, allowTrustedDeviceBypass: false, maxTrustedDevices: 5, sessionTokenTtlSeconds: 43200 })
const policyLoading = ref(false)
const policyLoadError = ref(false)
const policySaving = ref(false)
const policyError = ref('')

const formVisible = ref(false)
const editing = ref<SSOProvider | null>(null)
const saving = ref(false)
const formError = ref('')
const form = reactive({
  name: '', displayName: '', issuer: '', clientId: '', clientSecret: '', scopesText: 'openid, profile, email',
  redirectUri: '', algsText: 'RS256', usernameClaim: 'preferred_username', defaultRole: 'user' as 'admin' | 'user',
  roleMappings: [] as RoleMapping[], autoCreateUsers: false, publicListed: false, authoritativeRoles: false, enabled: true,
})

const deleteVisible = ref(false)
const deleteTarget = ref<SSOProvider | null>(null)
const deleting = ref(false)
const deleteError = ref('')

const testVisible = ref(false)
const testTarget = ref<SSOProvider | null>(null)
const testing = ref(false)
const testLoadError = ref(false)
const testReport = ref<SSOProviderTestReport | null>(null)
const reportOk = computed(() => Boolean(testReport.value?.discoveryOk && testReport.value?.jwksOk))
const endpointEntries = computed(() => Object.entries(testReport.value?.endpoints ?? {}))

function splitList(value: string) { return value.split(',').map(item => item.trim()).filter(Boolean) }

async function reload() {
  loading.value = true; loadError.value = false
  try { page.value = await getSSOProviders({ limit: 50 }) }
  catch { loadError.value = true }
  finally { loading.value = false }
}

async function loadMore() {
  if (!page.value.nextCursor) return
  loadingMore.value = true
  try {
    const next = await getSSOProviders({ limit: 50, cursor: page.value.nextCursor })
    page.value = { items: [...page.value.items, ...next.items], nextCursor: next.nextCursor, hasMore: next.hasMore }
  } catch { loadError.value = true }
  finally { loadingMore.value = false }
}

async function loadPolicy() {
  policyLoading.value = true; policyLoadError.value = false; policyError.value = ''
  try { Object.assign(policy, await getAuthPolicy()) }
  catch { policyLoadError.value = true }
  finally { policyLoading.value = false }
}

async function savePolicy() {
  policySaving.value = true; policyError.value = ''
  try {
    Object.assign(policy, await updateAuthPolicy({ ...policy }, crypto.randomUUID()))
    ElMessage.success(t('sso.policySaved'))
  } catch (error) { policyError.value = authErrorMessage(error, 'sso.policyUpdateFailed') }
  finally { policySaving.value = false }
}

function resetForm() {
  Object.assign(form, {
    name: '', displayName: '', issuer: '', clientId: '', clientSecret: '', scopesText: 'openid, profile, email',
    redirectUri: '', algsText: 'RS256', usernameClaim: 'preferred_username', defaultRole: 'user',
    roleMappings: [], autoCreateUsers: false, publicListed: false, authoritativeRoles: false, enabled: true,
  })
  formError.value = ''
}

function openCreate() { editing.value = null; resetForm(); formVisible.value = true }

function openEdit(row: SSOProvider) {
  editing.value = row
  resetForm()
  // The stored secret is never available, so the field starts empty and an empty
  // value on submit means "keep what the server already has".
  Object.assign(form, {
    name: row.name, displayName: row.displayName, issuer: row.issuer, clientId: row.clientId,
    scopesText: row.scopes.join(', '), redirectUri: row.redirectUri, algsText: row.idTokenAlgs.join(', '),
    usernameClaim: row.usernameClaim, defaultRole: row.defaultRole,
    roleMappings: row.roleMappings.map(mapping => ({ ...mapping })),
    autoCreateUsers: row.autoCreateUsers, publicListed: row.publicListed, authoritativeRoles: row.authoritativeRoles, enabled: row.enabled,
  })
  formVisible.value = true
}

function validate(): string {
  if (!NAME_PATTERN.test(form.name.trim())) return t('sso.invalidName')
  if (!/^https:\/\/[^\s]+$/i.test(form.issuer.trim())) return t('sso.invalidIssuer')
  if (!form.clientId.trim()) return t('sso.invalidClientId')
  if (!splitList(form.scopesText).includes('openid')) return t('sso.scopesRequired')
  if (!/^https?:\/\/[^\s]+$/i.test(form.redirectUri.trim())) return t('sso.invalidRedirectUri')
  const algs = splitList(form.algsText)
  if (!algs.length || algs.some(alg => !ACCEPTED_ALGS.includes(alg))) return t('sso.invalidAlgs')
  if (form.roleMappings.some(mapping => !mapping.claim.trim() || !mapping.value.trim())) return t('sso.invalidMapping')
  return ''
}

async function save() {
  formError.value = validate()
  if (formError.value) return
  saving.value = true
  const secret = form.clientSecret
  try {
    const input: SSOProviderInput = {
      name: form.name.trim(), displayName: form.displayName.trim(), issuer: form.issuer.trim(), clientId: form.clientId.trim(),
      scopes: splitList(form.scopesText), redirectUri: form.redirectUri.trim(), idTokenAlgs: splitList(form.algsText),
      usernameClaim: form.usernameClaim.trim(), roleMappings: form.roleMappings.map(mapping => ({ claim: mapping.claim.trim(), value: mapping.value.trim(), role: mapping.role })),
      defaultRole: form.defaultRole, autoCreateUsers: form.autoCreateUsers, publicListed: form.publicListed,
      authoritativeRoles: form.authoritativeRoles, enabled: form.enabled,
      // Omitting the field entirely is what "keep the stored secret" means; an
      // empty string would be ambiguous next to a rotation to an empty value.
      ...(secret ? { clientSecret: secret } : {}),
    }
    const wasEdit = Boolean(editing.value)
    if (editing.value) await updateSSOProvider(editing.value.id, input, crypto.randomUUID())
    else await createSSOProvider(input, crypto.randomUUID())
    formVisible.value = false
    ElMessage.success(t(wasEdit ? 'sso.updated' : 'sso.created'))
    // Cursor pages are ordered by the server, so a create can land anywhere in
    // the first page: re-reading it is simpler than splicing the row in.
    await reload()
  } catch (error) { formError.value = authErrorMessage(error, 'sso.operationFailed') }
  finally { saving.value = false; form.clientSecret = ''; editing.value = null }
}

function openDelete(row: SSOProvider) { deleteTarget.value = row; deleteError.value = ''; deleteVisible.value = true }

async function confirmDelete() {
  if (!deleteTarget.value) return
  deleting.value = true; deleteError.value = ''
  try {
    await deleteSSOProvider(deleteTarget.value.id, crypto.randomUUID())
    deleteVisible.value = false
    deleteTarget.value = null
    await reload()
    ElMessage.success(t('sso.deletedMessage'))
  } catch (error) { deleteError.value = authErrorMessage(error, 'sso.operationFailed') }
  finally { deleting.value = false }
}

async function runTest(row: SSOProvider) {
  testTarget.value = row; testReport.value = null; testLoadError.value = false; testVisible.value = true
  testing.value = true
  try { testReport.value = await testSSOProvider(row.id, crypto.randomUUID()) }
  catch (error) { testLoadError.value = true; ElMessage.error(authErrorMessage(error, 'sso.testFailedLoad')) }
  finally { testing.value = false }
}

async function retryTest() { if (testTarget.value) await runTest(testTarget.value) }

void loadPolicy()
void reload()
</script>

<style scoped>.panel { padding: 20px 24px; }
.table-panel { padding: 0; overflow: hidden; }
.panel-head h2 { margin: 0; font-size: 17px; }
.hint { margin: 4px 0 0; color: var(--tm-muted); font-size: 12px; line-height: 1.5; }
.toolbar { display: flex; justify-content: flex-end; padding: 14px 16px; border-bottom: 1px solid var(--tm-border); }
.load-more { display: flex; justify-content: center; padding: 14px; }
.policy-form { max-width: 520px; padding-top: 16px; }
.actions { display: flex; gap: 8px; align-items: center; }
.notice { margin-top: 14px; }
.mappings { width: 100%; }
.mapping-row { display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1fr) 120px auto; gap: 8px; align-items: center; margin-bottom: 8px; }
.mapping-role { width: 120px; }
.detail-list { display: grid; grid-template-columns: 140px minmax(0, 1fr); gap: 10px 16px; margin: 16px 0 0; }
.detail-list dt { color: var(--tm-muted); font-weight: 600; font-size: 13px; }
.detail-list dd { margin: 0; font-size: 13px; word-break: break-all; }
.endpoints { display: grid; grid-template-columns: minmax(0, 1fr); gap: 6px; margin: 0; padding: 0; list-style: none; }
.endpoints li { display: grid; grid-template-columns: 120px minmax(0, 1fr); gap: 8px; }
.endpoints code { color: var(--tm-muted); font-size: 12px; }
.form-error { margin-top: 12px; color: var(--el-color-danger, #d03050); font-size: 13px; }
.confirm-text { margin: 0 0 8px; }
.ellipsis { display: block; overflow: hidden; max-width: 300px; text-overflow: ellipsis; white-space: nowrap; }
:deep(.el-table) { --el-table-border-color: #edf1f7; }</style>
