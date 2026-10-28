import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, h } from 'vue'
import { createI18n } from 'vue-i18n'
import messages from '../i18n/messages/en-US'
import { middleTruncate } from '../utils/truncate'
import PagerBar from '../components/PagerBar.vue'
import IdCell from '../components/IdCell.vue'

function mount(component: object, props: Record<string, unknown>) {
  const host = document.createElement('div'); document.body.append(host)
  createApp({ render: () => h(component, props) }).use(createI18n({ legacy: false, locale: 'en-US', messages: { 'en-US': messages } })).mount(host)
  return host
}

describe('middleTruncate', () => {
  it('keeps short values whole and cuts the middle of long ones', () => {
    expect(middleTruncate('agent-1')).toBe('agent-1')
    expect(middleTruncate('agent-0123456789abcdef0123456789abcdef')).toBe('agent-0123…abcdef')
  })
})

describe('PagerBar', () => {
  beforeEach(() => document.body.replaceChildren())

  it('offers only previous and next, never a total or jump-to-page', () => {
    const host = mount(PagerBar, { pageIndex: 1, hasPrevious: true, hasNext: true, pageSize: 20 })
    expect(host.textContent).toContain('Page 2')
    expect(host.textContent).toContain('Previous')
    expect(host.textContent).toContain('Next')
    expect(host.querySelector('.el-pagination__jump')).toBeNull()
  })

  it('disables the ends of the walk and while loading', () => {
    const host = mount(PagerBar, { pageIndex: 0, hasPrevious: false, hasNext: true, pageSize: 20 })
    const [previous, next] = [...host.querySelectorAll<HTMLButtonElement>('button[data-test^="pager-"]')]
    expect(previous.disabled).toBe(true)
    expect(next.disabled).toBe(false)
  })

  it('emits next and previous', () => {
    const onNext = vi.fn(), onPrevious = vi.fn()
    const host = mount(PagerBar, { pageIndex: 1, hasPrevious: true, hasNext: true, pageSize: 20, onNext, onPrevious })
    host.querySelector<HTMLButtonElement>('[data-test="pager-next"]')!.click()
    host.querySelector<HTMLButtonElement>('[data-test="pager-previous"]')!.click()
    expect(onNext).toHaveBeenCalled(); expect(onPrevious).toHaveBeenCalled()
  })
})

describe('IdCell', () => {
  beforeEach(() => {
    document.body.replaceChildren()
    Object.assign(navigator, { clipboard: { writeText: vi.fn().mockResolvedValue(undefined) } })
  })

  it('shows a truncated id with the full value in the title and copies the whole id', () => {
    const id = 'agent-0123456789abcdef0123456789abcdef'
    const host = mount(IdCell, { value: id })
    const text = host.querySelector('.id-text')!
    expect(text.textContent).toBe('agent-0123…abcdef')
    expect(text.getAttribute('title')).toBe(id)
    host.querySelector<HTMLButtonElement>('button')!.click()
    expect(navigator.clipboard.writeText).toHaveBeenCalledWith(id)
  })
})
