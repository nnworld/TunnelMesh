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
})

function messageKeys(value: unknown, prefix = ''): string[] {
  if (typeof value !== 'object' || value === null) return [prefix]
  return Object.entries(value).flatMap(([key, nested]) => messageKeys(nested, prefix ? `${prefix}.${key}` : key)).sort()
}
