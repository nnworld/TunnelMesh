import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'

describe('localized management views', () => {
  it('loads dashboard values from the summary endpoint', () => {
    const source = readFileSync('src/views/Dashboard.vue', 'utf8')
    expect(source).toContain('getDashboardSummary')
    expect(source).toContain('retry')
  })

  it('uses i18n in every management view', () => {
    for (const name of ['Dashboard','Agents','AgentDetail','Routes','Tunnels','Tokens','AuditLogs','Login']) {
      expect(readFileSync(`src/views/${name}.vue`, 'utf8'), name).toContain('useI18n')
    }
  })
})
