import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'

describe('localized management views', () => {
  it('loads dashboard values from the summary endpoint', () => {
    const source = readFileSync('src/views/Dashboard.vue', 'utf8')
    expect(source).toContain('getDashboardSummary')
    expect(source).toContain('retry')
  })

  it('uses i18n in every management view', () => {
    for (const name of ['Dashboard','Agents','AgentDetail','Routes','Tunnels','Tokens','Servers','Clients','Downloads','AuditLogs','Login']) {
      expect(readFileSync(`src/views/${name}.vue`, 'utf8'), name).toContain('useI18n')
    }
  })

  it('renders the immutable release download surface', () => {
    const source = readFileSync('src/views/Downloads.vue', 'utf8')
    expect(source).toContain('getDownloads')
    expect(source).toContain('release.version')
    expect(source).toContain('downloads.releaseLink')
    expect(source).toContain('downloads.platform')
    expect(source).toContain('downloads.checksumCommand')
    expect(source).toContain('downloads.upgradeNote')
    expect(source).toContain('v-for="asset in release.assets"')
    expect(source).toContain('class="checksum-command"')
    // Every address comes from the management API, so downloads.github_repository is
    // honored; a repository host baked into the bundle would break forks.
    expect(source).not.toContain('https://github.com/')
  })
})

  it('counts client observability cards from the server summary, not the loaded page', () => {
    const source = readFileSync('src/views/Clients.vue', 'utf8')
    expect(source).toContain('summary')
    expect(source).toContain('page.summary')
    // A counter derived from the current page is what made the cards disagree
    // with the table, so the derivation must not come back.
    expect(source).not.toContain("items.value.filter(item => item.status === 'stale')")
    expect(source).not.toContain('items.value.reduce((total, item) => total + item.activeConnections, 0)')
  })

  it('renders client presence and metadata freshness as two separate columns', () => {
    const source = readFileSync('src/views/Clients.vue', 'utf8')
    expect(source).toContain("t('clients.metadataState')")
    expect(source).toContain('metadataStateLabel')
    expect(source).toContain('metadataStateOptions')
    // Presence must stop encoding metadata freshness.
    expect(source).not.toContain("status === 'stale'")
    expect(source).not.toContain("status === 'metadata_unavailable'")
  })
