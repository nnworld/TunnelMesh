<template>
  <main class="login-page">
    <div class="login-brand"><span class="mesh-dot" />TunnelMesh</div>
    <el-card class="login">
      <div class="locale"><el-button link @click="switchLocale">{{ preferences.locale === 'zh-CN' ? 'English' : '简体中文' }}</el-button></div>
      <template v-if="step === 'password'">
        <h1>{{ t('auth.welcome') }}</h1><p>{{ t('auth.description') }}</p>
        <el-form label-position="top" @submit.prevent="submit">
          <el-form-item :label="t('auth.username')"><el-input v-model="username" data-test="username" autocomplete="username" /></el-form-item>
          <el-form-item :label="t('auth.password')"><el-input v-model="password" type="password" show-password data-test="password" autocomplete="current-password" @keyup.enter="submit" /></el-form-item>
          <el-checkbox v-model="trustDevice" class="trust" data-test="trust-device">{{ t('auth.trustDevice') }}</el-checkbox>
          <el-button class="submit" type="primary" native-type="submit" :loading="loading" :disabled="throttleRemaining > 0" data-test="login-submit">{{ t('auth.signIn') }}</el-button>
          <el-alert v-if="errorText" type="error" :title="errorText" show-icon :closable="false" data-test="login-error" />
        </el-form>
        <div v-if="providers.length" class="sso">
          <el-divider>{{ t('auth.ssoTitle') }}</el-divider>
          <!-- The authorize endpoint answers with a 302 to the identity provider,
               so a plain anchor is both the simplest and the only correct control:
               a scripted navigation would drop the browser from the redirect chain. -->
          <el-button v-for="provider in providers" :key="provider.id" class="sso-button" tag="a" :href="authorizeHref(provider.name)" data-test="sso-link">{{ provider.displayName || provider.name }}</el-button>
        </div>
      </template>
      <template v-else>
        <h1>{{ t('auth.mfaTitle') }}</h1><p>{{ t('auth.mfaDescription') }}</p>
        <el-form class="mfa-step" label-position="top" data-test="mfa-step" @submit.prevent="submitMfa">
          <el-form-item :label="t('auth.mfaCode')"><el-input v-model="code" data-test="mfa-code" maxlength="64" autocomplete="one-time-code" :placeholder="t('auth.mfaPlaceholder')" @keyup.enter="submitMfa" /></el-form-item>
          <p class="hint">{{ t('auth.mfaHint') }}</p>
          <el-checkbox v-model="trustDevice" class="trust" data-test="trust-device">{{ t('auth.trustDevice') }}</el-checkbox>
          <el-button class="submit" type="primary" native-type="submit" :loading="loading" :disabled="!code.trim()" data-test="mfa-submit">{{ t('auth.verify') }}</el-button>
          <div class="back"><el-button link data-test="back-to-password" @click="backToPassword">{{ t('auth.backToPassword') }}</el-button></div>
          <el-alert v-if="errorText" type="error" :title="errorText" show-icon :closable="false" data-test="mfa-error" />
        </el-form>
      </template>
    </el-card>
  </main>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { authErrorMessage } from '../i18n/errors'
import { listOIDCProviders } from '../api/auth'
import type { OIDCProviderSummary } from '../api/auth'
import { useAuthStore } from '../stores/auth'
import type { LoginResult } from '../stores/auth'
import { usePreferencesStore } from '../stores/preferences'

const { t } = useI18n(), route = useRoute(), router = useRouter(), auth = useAuthStore(), preferences = usePreferencesStore()
const step = ref<'password' | 'mfa'>('password')
const username = ref(''), password = ref(''), code = ref('')
const trustDevice = ref(false), loading = ref(false), error = ref('')
const throttled = ref(false), throttleRemaining = ref(0)
const providers = ref<OIDCProviderSummary[]>([]), challengeId = ref('')
// A login ticket is single-use, so it is spent at most once per page load even
// if this view were mounted twice before the address bar is replaced.
let ticketSpent = false
let countdown: ReturnType<typeof setInterval> | undefined

const errorText = computed(() => {
  if (!throttled.value) return error.value
  return throttleRemaining.value > 0 ? t('auth.throttledWait', { seconds: throttleRemaining.value }) : t('auth.throttled')
})

function switchLocale() { preferences.setLocale(preferences.locale === 'zh-CN' ? 'en-US' : 'zh-CN') }
function authorizeHref(name: string) { return `/api/v1/auth/oidc/${encodeURIComponent(name)}/authorize` }
function queryValue(value: unknown) { return typeof value === 'string' ? value : Array.isArray(value) && typeof value[0] === 'string' ? value[0] : '' }
// Every stable identity domain is translated in one shared map so the login
// page and account security cannot drift apart.
function errorMessage(cause: unknown) { return authErrorMessage(cause, 'auth.failed') }

onMounted(async () => {
  const challenge = queryValue(route.query.mfa)
  if (challenge) { challengeId.value = challenge; step.value = 'mfa' }
  const ticket = queryValue(route.query.ticket)
  if (ticket) { if (!ticketSpent) { ticketSpent = true; await exchangeTicket(ticket) }; return }
  await loadProviders()
})
onBeforeUnmount(() => { if (countdown) clearInterval(countdown) })

// A 404 here means the deployment does not publish its providers, which is a
// configuration choice rather than an error worth showing on the sign-in page.
async function loadProviders() { try { providers.value = await listOIDCProviders() } catch { providers.value = [] } }

async function submit() {
  if (loading.value || throttleRemaining.value > 0) return
  loading.value = true; error.value = ''; throttled.value = false
  try { handleResult(await auth.login(username.value, password.value, trustDevice.value)) }
  catch (cause) { error.value = errorMessage(cause) }
  finally { loading.value = false }
}

async function submitMfa() {
  if (loading.value || !code.value.trim()) return
  loading.value = true; error.value = ''
  try {
    const result = await auth.verifyMfa(challengeId.value, code.value.trim(), trustDevice.value)
    if (result.recoveryCodesExhausted) ElMessage.warning(t('auth.recoveryExhausted'))
    router.push('/')
  } catch (cause) { error.value = errorMessage(cause); code.value = '' }
  finally { loading.value = false }
}

async function exchangeTicket(ticket: string) {
  loading.value = true; error.value = ''
  try {
    const result = await auth.exchangeTicket(ticket, trustDevice.value)
    // The spent ticket must leave the address bar before anything else can read
    // or resubmit it; only the challenge id survives into the MFA step.
    if (result.state === 'mfa_required') { challengeId.value = result.challengeId; step.value = 'mfa'; router.replace({ path: '/login', query: { mfa: result.challengeId } }); return }
    router.replace('/')
  } catch (cause) {
    error.value = errorMessage(cause)
    router.replace('/login')
    await loadProviders()
  } finally { loading.value = false }
}

function handleResult(result: LoginResult) {
  if (result.state === 'authenticated') { router.push('/'); return }
  // The password already proved itself, so it is dropped instead of being kept
  // in a ref for the rest of the second-factor step.
  if (result.state === 'mfa_required') { challengeId.value = result.challengeId; step.value = 'mfa'; password.value = ''; return }
  startCountdown(result.retryAfter)
}

function startCountdown(seconds: number) {
  throttled.value = true; throttleRemaining.value = seconds
  if (countdown) clearInterval(countdown)
  if (seconds <= 0) { throttled.value = false; return }
  countdown = setInterval(() => {
    throttleRemaining.value -= 1
    if (throttleRemaining.value > 0) return
    throttleRemaining.value = 0; throttled.value = false; error.value = ''
    if (countdown) clearInterval(countdown)
  }, 1000)
}

function backToPassword() { step.value = 'password'; code.value = ''; challengeId.value = ''; error.value = ''; throttled.value = false; throttleRemaining.value = 0 }
</script>

<style scoped>.login-page{min-height:100vh;display:grid;place-items:center;padding:24px;background:radial-gradient(circle at 20% 10%,#e8f3ff 0,transparent 35%),var(--tm-bg)}.login-brand{position:fixed;top:28px;left:32px;font-weight:750}.mesh-dot{display:inline-block;width:10px;height:10px;margin-right:8px;border-radius:50%;background:var(--tm-primary);box-shadow:16px 0 0 #8dc5ff}.login{width:min(420px,100%);border:1px solid var(--tm-border);border-radius:10px}.login h1{margin:0;font-size:26px}.login p{margin:6px 0 24px;color:var(--tm-muted)}.locale{text-align:right}.submit{width:100%;margin:14px 0}.trust{margin-bottom:4px}.hint{margin:-8px 0 14px;color:var(--tm-muted);font-size:12px}.mfa-step .hint{margin-bottom:14px}.back{text-align:center}.sso{margin-top:4px}.sso-button{display:flex;width:100%;margin-bottom:10px;text-decoration:none}:deep(.el-divider__text){color:var(--tm-muted);font-size:12px}</style>
