import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, h } from 'vue'
import { APIError, api } from '../api/client'
import { accountErrorMessage } from '../i18n/errors'
import { formatDateTime } from '../i18n/format'
import { breadcrumbsFor } from '../layouts/breadcrumbs'
import StatusTag from '../components/StatusTag.vue'
import DataState from '../components/DataState.vue'
import { readFileSync } from 'node:fs'

describe('frontend design-system contracts', () => {
  beforeEach(() => {
    localStorage.clear()
    document.body.replaceChildren()
  })

  it('formats dates with the active locale and an explicit timezone', () => {
    const value = '2026-09-08T02:03:04Z'
    expect(formatDateTime(value, 'zh-CN', 'UTC')).toBe('2026/9/8 02:03:04')
    expect(formatDateTime(value, 'en-US', 'UTC')).toBe('9/8/2026, 2:03:04 AM')
    expect(formatDateTime(undefined, 'zh-CN')).toBe('—')
  })

  it('derives stable breadcrumbs from management routes', () => {
    expect(breadcrumbsFor('/agents/agent-1')).toEqual([
      { key: 'shell.console' },
      { key: 'agents.title' },
      { key: 'agentDetail.title' },
    ])
    expect(breadcrumbsFor('/account/security')).toEqual([
      { key: 'shell.console' },
      { key: 'navigation.security' },
    ])
  })

  it('imports programmatic element-plus styles that templates never reference', () => {
    const source = readFileSync('src/main.ts', 'utf8')
    expect(source).toContain('element-plus/es/components/message-box/style/css')
    expect(source).toContain('element-plus/es/components/message/style/css')
  })

  it('constrains page grids so wide tables scroll inside their card', () => {
    const css = readFileSync('src/styles/tokens.css', 'utf8')
    expect(css).toContain('.tm-page { display: grid; grid-template-columns: minmax(0, 1fr); gap: 16px; }')
  })

  it('preserves HTTP and domain error identifiers in API errors', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({
      ok: false, status: 400,
      json: async () => ({ code: 400, msg: 'Bad Request', data: { error: 'username_invalid' } }),
    }))

    const error = await api('/users').catch((cause: unknown) => cause)

    expect(error).toBeInstanceOf(APIError)
    expect(error).toMatchObject({ status: 400, code: 400, domain: 'username_invalid' })
  })

  it('localizes stable account domain errors', async () => {
    const { setAppLocale } = await import('../i18n')
    setAppLocale('zh-CN')
    expect(accountErrorMessage(new APIError('Bad Request', 400, 400, 'username_invalid'))).toBe('用户名只能包含 3-64 位字母、数字、点、下划线或连字符')
    setAppLocale('en-US')
    expect(accountErrorMessage(new APIError('Forbidden', 403, 403, 'current_password_invalid'))).toBe('Current password is incorrect')
    expect(accountErrorMessage(new Error('network down'))).toBe('Operation failed. Please try again.')
  })

  it('renders a normalized status tag', () => {
    const container = document.createElement('div')
    document.body.appendChild(container)
    createApp(StatusTag, { kind: 'success', label: 'Active' }).mount(container)

    expect(container.textContent).toContain('Active')
  })

  it('renders normalized loading, error, and empty states', () => {
    const mount = (props: Record<string, unknown>, content = '') => {
      const container = document.createElement('div')
      document.body.appendChild(container)
      createApp({ render: () => h(DataState, props, { default: () => content }) }).mount(container)
      return container
    }

    expect(mount({ loading: true }).querySelector('.el-skeleton')).not.toBeNull()
    expect(mount({ error: true, retryLabel: 'Retry' }).textContent).toContain('Retry')
    expect(mount({ empty: true, emptyLabel: 'No data' }).textContent).toContain('No data')
    expect(mount({}, 'Loaded content').textContent).toContain('Loaded content')
  })

  it('applies shared primitives across management pages', () => {
    const read = (path: string) => readFileSync(path, 'utf8')
    for (const path of ['src/views/Agents.vue', 'src/views/Routes.vue', 'src/views/Tunnels.vue', 'src/views/AuditLogs.vue', 'src/views/Users.vue']) {
      expect(read(path), path).toContain('<DataState')
    }
    expect(read('src/views/Dashboard.vue')).toContain('<DataState')
    for (const path of ['src/views/Agents.vue', 'src/views/Users.vue', 'src/views/AgentDetail.vue']) {
      expect(read(path), path).toContain('<StatusTag')
    }
    for (const path of ['src/views/Users.vue', 'src/views/AuditLogs.vue', 'src/views/AgentDetail.vue', 'src/views/Dashboard.vue', 'src/views/Tokens.vue']) {
      expect(read(path), path).toContain('useFormatDateTime')
    }
    expect(read('src/components/PageHeader.vue')).toContain('<el-breadcrumb')
    expect(read('src/views/Tokens.vue')).toContain('<div class="tm-card')
    expect(read('src/views/Tokens.vue')).not.toContain('<el-card')
    expect(read('src/views/AccountSecurity.vue')).toContain('accountErrorMessage')
    expect(read('src/views/Users.vue')).toContain('accountErrorMessage')
  })
})
