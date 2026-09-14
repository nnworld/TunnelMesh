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

  it('renders release information with only the GitHub Releases link', () => {
    const source = readFileSync('src/views/Downloads.vue', 'utf8')
    expect(source).toContain('getDownloads')
    expect(source).toContain('release.version')
    expect(source).toContain('https://github.com/nnworld/TunnelMesh/releases')
    expect(source).toContain('downloads.releaseLink')
    expect(source).toContain('downloads.upgradeNote')
    expect(source).not.toContain('v-for="asset in release.assets"')
    expect(source).not.toContain('asset.url')
    expect(source).not.toContain('checksumCommand')
  })
})
