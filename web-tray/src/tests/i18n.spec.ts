import { describe, expect, it } from 'vitest'
import { applyLanguagePreference, enUS, i18n, normalizeLocale, resolveLocale, setLocale, zhCN } from '../i18n'

type Tree = Record<string, unknown>

/** Flattens a message tree into dotted keys so the two locales can be compared exactly. */
function messageKeys(tree: Tree, prefix = ''): string[] {
  const keys: string[] = []
  for (const [key, value] of Object.entries(tree)) {
    const path = prefix ? `${prefix}.${key}` : key
    if (value && typeof value === 'object' && !Array.isArray(value)) {
      keys.push(...messageKeys(value as Tree, path))
    } else {
      keys.push(path)
    }
  }
  return keys.sort()
}

describe('locale resolution', () => {
  it('normalizes every Chinese browser locale and falls back to English', () => {
    expect(normalizeLocale('zh-Hant')).toBe('zh-CN')
    expect(normalizeLocale('zh_CN')).toBe('zh-CN')
    expect(normalizeLocale('zh')).toBe('zh-CN')
    expect(normalizeLocale('fr-FR')).toBe('en-US')
    expect(normalizeLocale(undefined)).toBe('en-US')
  })

  it('follows the system languages only for the system preference', () => {
    expect(resolveLocale('system', ['zh-CN'])).toBe('zh-CN')
    expect(resolveLocale('system', ['fr-FR', 'zh-Hans'])).toBe('zh-CN')
    expect(resolveLocale('system', ['fr-FR'])).toBe('en-US')
    expect(resolveLocale('system', [])).toBe('en-US')
    // An explicit choice wins over the operating system.
    expect(resolveLocale('en-US', ['zh-CN'])).toBe('en-US')
    expect(resolveLocale('zh-CN', ['en-US'])).toBe('zh-CN')
  })

  it('treats an unknown stored preference as system', () => {
    expect(resolveLocale('klingon' as never, ['zh-CN'])).toBe('zh-CN')
    expect(resolveLocale(null, ['en-GB'])).toBe('en-US')
  })

  it('applies the preference to the i18n instance and the document', () => {
    expect(applyLanguagePreference('system', ['zh-TW'])).toBe('zh-CN')
    expect(i18n.global.locale.value).toBe('zh-CN')
    expect(document.documentElement.lang).toBe('zh-CN')

    applyLanguagePreference('en-US')
    expect(i18n.global.locale.value).toBe('en-US')
    expect(document.documentElement.lang).toBe('en-US')

    setLocale('zh-CN')
    expect(i18n.global.locale.value).toBe('zh-CN')
  })

  it('keeps the two locales structurally identical', () => {
    expect(messageKeys(zhCN as Tree)).toEqual(messageKeys(enUS as Tree))
  })

  it('labels every validation check the tray can report', () => {
    const ids = [
      'serverUrl.format',
      'serverUrl.reachable',
      'token.present',
      'token.valid',
      'agents.available',
      'tunnels.present',
      'tunnel.protocol',
      'tunnel.listen',
      'tunnel.agent',
      'tunnel.target',
      'tunnel.credentials',
      'config.valid',
    ]
    const keys = messageKeys(zhCN as Tree)
    for (const id of ids) {
      expect(keys, id).toContain(`checks.${id}.label`)
      expect(keys, id).toContain(`checks.${id}.passed`)
    }
  })

  it('names all four tabs and every tunnel protocol', () => {
    const keys = messageKeys(enUS as Tree)
    for (const tab of ['general', 'stats', 'routing', 'about']) {
      expect(keys, tab).toContain(`app.tabs.${tab}`)
    }
    for (const protocol of ['tcp', 'udp', 'http', 'socks5', 'http-proxy']) {
      expect(keys, protocol).toContain(`routing.protocols.${protocol}`)
    }
  })
})
