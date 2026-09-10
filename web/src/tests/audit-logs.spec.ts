import { describe, expect, it, vi } from 'vitest'
import { readFileSync } from 'node:fs'
import { listAuditLogs } from '../api/client'

describe('audit logs', () => {
  it('sends all audit filters to the server', async () => {
    const fetchMock = vi.fn().mockResolvedValue({ ok: true, json: async () => ({ data: { items: [] } }) })
    vi.stubGlobal('fetch', fetchMock)

    await listAuditLogs({
      actorUserId: 'user-1',
      action: 'route.updated',
      resourceType: 'tunnel',
      resourceId: 'route-1',
      createdFrom: '2026-09-09T00:00:00+08:00',
      createdTo: '2026-09-10T00:00:00+08:00',
      limit: 100,
    })

    expect(vi.mocked(fetch).mock.calls[0][0]).toBe(
      '/api/v1/audit-logs?actorUserId=user-1&action=route.updated&resourceType=tunnel&resourceId=route-1&createdFrom=2026-09-09T00%3A00%3A00%2B08%3A00&createdTo=2026-09-10T00%3A00%3A00%2B08%3A00&limit=100',
    )
  })

  it('exposes a server-side filter workflow', () => {
    const source = readFileSync('src/views/AuditLogs.vue', 'utf8')
    expect(source).toContain('filter-card')
    expect(source).toContain('type="datetimerange"')
    expect(source).toContain("t('audits.timeRange')")
    expect(source).toContain('resetFilters')
    expect(source).toContain('search')
    expect(source).toContain("t('audits.reset')")
    expect(source).toContain("t('audits.query')")
    expect(source).toContain('filter-actions')
  })
})
