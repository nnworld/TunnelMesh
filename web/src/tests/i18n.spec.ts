import { describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { normalizeLocale, resolveInitialLocale } from '../i18n'
import zhCN from '../i18n/messages/zh-CN'
import enUS from '../i18n/messages/en-US'

describe('locale preferences', () => {
  it('normalizes every Chinese browser locale and falls back to English', () => {
    expect(normalizeLocale('zh-Hant')).toBe('zh-CN')
    expect(normalizeLocale('zh-CN')).toBe('zh-CN')
    expect(normalizeLocale('fr-FR')).toBe('en-US')
  })

  it('prefers a stored supported locale over browser languages', () => {
    expect(resolveInitialLocale('en-US', ['zh-CN'])).toBe('en-US')
    expect(resolveInitialLocale(null, ['fr-FR', 'zh-Hans'])).toBe('zh-CN')
  })

  it('does not persist an automatically detected locale', async () => {
    localStorage.removeItem('tunnelmesh_locale')
    vi.resetModules()

    await import('../i18n')

    expect(localStorage.getItem('tunnelmesh_locale')).toBeNull()
  })

  it('persists only a manually selected locale and keeps UI state synchronized', async () => {
    const { i18n, setAppLocale } = await import('../i18n')
    setAppLocale('zh-CN')
    setActivePinia(createPinia())
    const { usePreferencesStore } = await import('../stores/preferences')
    const preferences = usePreferencesStore()

    preferences.setLocale('en-US')

    expect(localStorage.getItem('tunnelmesh_locale')).toBe('en-US')
    expect(preferences.locale).toBe('en-US')
    expect(i18n.global.locale.value).toBe('en-US')
    expect(document.documentElement.lang).toBe('en-US')
  })

  it('keeps locale message keys structurally identical', () => {
    expect(messageKeys(zhCN)).toEqual(messageKeys(enUS))
  })

  it('defines complete credentials and remote-server message trees', () => {
    for (const locale of [zhCN, enUS]) {
      expect(Object.keys(locale.credentials).sort()).toContainEqual('privateKeyWarning')
      for (const key of [
        'title', 'description', 'refresh', 'query', 'keyword', 'agent', 'status', 'all', 'enabled', 'disabled', 'deleted',
        'name', 'host', 'port', 'username', 'credential', 'actions', 'detail', 'edit', 'ssh', 'delete', 'restore',
        'empty', 'loadFailed', 'operationFailed', 'sshTitle', 'password', 'connect', 'cancel', 'agentOffline',
      ]) expect(Object.keys(locale.remoteServers).sort(), key).toContainEqual(key)
    }
  })

  it('translates every proxy entry key in both locales', () => {
    for (const locale of [zhCN, enUS]) {
      for (const key of [
        'protocolHttpProxy', 'proxyName', 'proxyNameHelp', 'proxyDomainPreview', 'proxyAuthMode',
        'proxyAuthNone', 'proxyAuthBasic', 'proxyCredential', 'proxyCreateCredential',
        'proxySourceCIDRs', 'proxySourceCIDRsHelp', 'proxyAllowAll', 'proxyTargetCIDRs',
        'proxyTargetPorts', 'proxyAllowPrivateTargets', 'proxyMaxConcurrentTunnels',
        'proxyDescription', 'proxyUrl', 'proxyCopy', 'proxyCopied', 'proxyUsage', 'proxyUsageTitle',
        'proxyActiveTunnelsHint', 'proxyTargetDynamic',
        'proxyNameInvalid', 'proxyCredentialRequired', 'proxyCIDRInvalid', 'proxyPortInvalid',
      ]) expect(Object.keys(locale.routes).sort(), key).toContainEqual(key)
      for (const key of ['proxyBasic', 'username', 'usernameRequired']) {
        expect(Object.keys(locale.credentials).sort(), key).toContainEqual(key)
      }
      expect(Object.keys(locale.credentials.types).sort()).toContainEqual('proxyBasic')
    }
  })

  // The existing equality check reports two large arrays on failure; this one
  // names the drift, so a key added to only one locale is obvious in CI output.
  it('reports no missing or extra key path between the two locales', () => {
    const zh = messageKeys(zhCN)
    const en = messageKeys(enUS)
    expect(zh.filter(key => !en.includes(key))).toEqual([])
    expect(en.filter(key => !zh.includes(key))).toEqual([])
    expect(zh).toHaveLength(en.length)
  })

  it('translates every SSO, MFA, device, and identity key in both locales', () => {
    for (const locale of [zhCN, enUS]) {
      for (const key of [
        'trustDevice', 'ssoTitle', 'mfaTitle', 'mfaDescription', 'mfaCode', 'mfaHint', 'verify',
        'backToPassword', 'throttled', 'throttledWait', 'recoveryExhausted', 'ticketInvalid',
        'mfaErrorInvalid', 'mfaErrorExceeded', 'mfaErrorChallenge', 'mfaErrorNotEnrolled', 'mfaErrorReused',
      ]) expect(Object.keys(locale.auth), key).toContainEqual(key)

      for (const group of ['mfa', 'devices', 'identities']) expect(Object.keys(locale.security), group).toContainEqual(group)
      for (const key of [
        'statusNone', 'statusPending', 'statusEnabled', 'policyRequiredHint', 'remainingCodes', 'enroll', 'enable',
        'disable', 'regenerate', 'enrollOtpauth', 'enrollSecret', 'copy', 'recoveryAck', 'recoveryTitle',
        'requiredByPolicy', 'currentPasswordRequired', 'codeInvalid',
      ]) expect(Object.keys(locale.security.mfa), key).toContainEqual(key)
      for (const key of ['name', 'ip', 'userAgent', 'current', 'rename', 'revoke', 'notFound', 'empty']) {
        expect(Object.keys(locale.security.devices), key).toContainEqual(key)
      }
      for (const key of ['provider', 'subject', 'account', 'unlink', 'empty', 'requiredForLogin', 'notFound']) {
        expect(Object.keys(locale.security.identities), key).toContainEqual(key)
      }

      for (const key of [
        'title', 'description', 'create', 'edit', 'name', 'issuer', 'clientId', 'clientSecret', 'secretStored',
        'secretNotStored', 'secretKept', 'secretPlaceholderKeep', 'scopes', 'scopesRequired', 'redirectUri',
        'idTokenAlgs', 'invalidAlgs', 'invalidName', 'invalidIssuer', 'invalidRedirectUri', 'invalidClientId',
        'invalidMapping', 'usernameClaim', 'roleMappings', 'defaultRole', 'autoCreateUsers', 'publicListed',
        'authoritativeRoles', 'enabled', 'test', 'testTitle', 'testDiscoveryOk', 'testJwksOk', 'testAlgorithms',
        'testEndpoints', 'testOk', 'testFailed', 'delete', 'deleteConfirm', 'policyTitle', 'policyMfaMode',
        'policyDeviceTrust', 'policyAllowBypass', 'policyDeviceTtl', 'policyMaxDevices', 'policySessionTtl',
        'policySave', 'policyLoadFailed', 'policyUpdateFailed',
      ]) expect(Object.keys(locale.sso), key).toContainEqual(key)

      for (const key of ['mfaRequired', 'resetMFA', 'confirmResetMFA', 'mfaReset', 'mfaRequiredUpdated']) {
        expect(Object.keys(locale.users), key).toContainEqual(key)
      }
      expect(Object.keys(locale.navigation)).toContainEqual('sso')
      expect(Object.keys(locale.errors)).toContainEqual('secretStorageUnavailable')
    }
  })

  it('interpolates the throttle countdown in both locales', () => {
    expect(zhCN.auth.throttledWait).toContain('{seconds}')
    expect(enUS.auth.throttledWait).toContain('{seconds}')
  })
})

function messageKeys(value: unknown, prefix = ''): string[] {
  if (typeof value !== 'object' || value === null) return [prefix]
  return Object.entries(value).flatMap(([key, nested]) => messageKeys(nested, prefix ? `${prefix}.${key}` : key)).sort()
}
