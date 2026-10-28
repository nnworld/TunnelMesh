import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, h, nextTick, ref } from 'vue'
import { createPinia, setActivePinia } from 'pinia'
import { createI18n } from 'vue-i18n'
import { createMemoryHistory, createRouter } from 'vue-router'
import messages from '../i18n/messages/en-US'
import zh from '../i18n/messages/zh-CN'
import CommandPalette from '../components/CommandPalette.vue'
import { rankEntries } from '../layouts/paletteSearch'
import { paletteEntries } from '../layouts/navigation'
import { useAuthStore } from '../stores/auth'
import { usePreferencesStore } from '../stores/preferences'

const tr = (locale: 'en-US' | 'zh-CN') => {
  const tree = locale === 'en-US' ? messages : zh
  return (key: string) => key.split('.').reduce<any>((n, p) => n?.[p], tree) as string
}

describe('rankEntries', () => {
  const entries = paletteEntries(true)

  it('returns everything in navigation order for an empty query', () => {
    expect(rankEntries(entries, '', tr('en-US'))).toHaveLength(entries.length)
  })

  it('ranks a label prefix above a mid-word match', () => {
    const labels = rankEntries(entries, 'a', tr('en-US')).map(entry => tr('en-US')(entry.labelKey))
    expect(labels[0].toLowerCase().startsWith('a')).toBe(true)
  })

  it('matches labels in either language so a muscle-memory query still works', () => {
    const hit = rankEntries(entries, 'audit', tr('zh-CN'), tr('en-US'))
    expect(hit.some(entry => entry.kind === 'page' && entry.path === '/audit-logs')).toBe(true)
    const zhHit = rankEntries(entries, '审计', tr('en-US'), tr('zh-CN'))
    expect(zhHit.some(entry => entry.kind === 'page' && entry.path === '/audit-logs')).toBe(true)
  })

  it('returns nothing when nothing matches', () => {
    expect(rankEntries(entries, 'zzzz-nope', tr('en-US'))).toEqual([])
  })
})

async function mountPalette(admin = true) {
  const pinia = createPinia()
  setActivePinia(pinia)
  const router = createRouter({ history: createMemoryHistory(), routes: ['/', '/agents', '/audit-logs', '/account/security'].map(path => ({ path, component: { render: () => null } })) })
  await router.push('/')
  const i18n = createI18n({ legacy: false, locale: 'en-US', fallbackLocale: 'en-US', messages: { 'en-US': messages, 'zh-CN': zh } })
  const auth = useAuthStore(pinia)
  auth.user = { id: 'u', username: 'root', role: admin ? 'admin' : 'member' } as never
  const open = ref(true)
  const host = document.createElement('div')
  document.body.append(host)
  createApp({ render: () => h(CommandPalette, { modelValue: open.value, 'onUpdate:modelValue': (v: boolean) => (open.value = v) }) }).use(pinia).use(router).use(i18n).mount(host)
  await nextTick(); await nextTick()
  return { router, open, preferences: usePreferencesStore(pinia) }
}

const input = () => document.body.querySelector<HTMLInputElement>('input[role="combobox"]')!
const press = (key: string) => { input().dispatchEvent(new KeyboardEvent('keydown', { key, bubbles: true })); return nextTick() }
const options = () => [...document.body.querySelectorAll('[role="option"]')]

describe('CommandPalette', () => {
  beforeEach(() => document.body.replaceChildren())

  it('exposes combobox/listbox semantics with an active descendant', async () => {
    await mountPalette()
    expect(input().getAttribute('aria-expanded')).toBe('true')
    const list = document.body.querySelector('[role="listbox"]')!
    expect(input().getAttribute('aria-controls')).toBe(list.id)
    expect(input().getAttribute('aria-activedescendant')).toBe(options()[0].id)
    expect(options()[0].getAttribute('aria-selected')).toBe('true')
  })

  it('moves the active option with the arrow keys and wraps', async () => {
    await mountPalette()
    await press('ArrowDown')
    expect(input().getAttribute('aria-activedescendant')).toBe(options()[1].id)
    await press('ArrowUp'); await press('ArrowUp')
    expect(input().getAttribute('aria-activedescendant')).toBe(options()[options().length - 1].id)
  })

  it('filters as you type and announces an empty result', async () => {
    await mountPalette()
    input().value = 'audit'
    input().dispatchEvent(new Event('input', { bubbles: true }))
    await nextTick()
    expect(options().map(option => option.textContent)).toEqual([expect.stringContaining('Audit logs')])
    input().value = 'zzzz'
    input().dispatchEvent(new Event('input', { bubbles: true }))
    await nextTick()
    expect(options()).toHaveLength(0)
    expect(document.body.querySelector('[role="status"]')!.textContent).toContain('zzzz')
  })

  it('opens the highlighted page on Enter and closes', async () => {
    const { router, open } = await mountPalette()
    input().value = 'agents'
    input().dispatchEvent(new Event('input', { bubbles: true }))
    await nextTick()
    await press('Enter')
    await vi.waitFor(() => expect(router.currentRoute.value.path).toBe('/agents'))
    expect(open.value).toBe(false)
  })

  it('runs an action entry', async () => {
    const { preferences } = await mountPalette()
    input().value = 'dark'
    input().dispatchEvent(new Event('input', { bubbles: true }))
    await nextTick()
    await press('Enter')
    expect(preferences.theme).toBe('dark')
  })

  it('hides admin pages from members', async () => {
    await mountPalette(false)
    expect(options().some(option => option.textContent?.includes('Audit logs'))).toBe(false)
  })
})
