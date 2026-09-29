import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, nextTick } from 'vue'
import Downloads from '../views/Downloads.vue'
import { i18n } from '../i18n'
import { getDownloads, type DownloadInfo } from '../api/client'

vi.mock('vue-router', () => ({ useRoute: () => ({ path: '/downloads', params: {} }) }))
vi.mock('../api/client', async () => {
  const actual = await vi.importActual<typeof import('../api/client')>('../api/client')
  return { ...actual, getDownloads: vi.fn() }
})

async function flush() {
  await nextTick()
  await new Promise((resolve) => setTimeout(resolve, 0))
  await nextTick()
}

async function mountDownloads() {
  const container = document.createElement('div')
  document.body.appendChild(container)
  const app = createApp(Downloads)
  app.use(i18n)
  app.mount(container)
  await flush()
  return { container, unmount: () => { app.unmount(); container.remove() } }
}

function release(version = 'v1.3.0'): DownloadInfo {
  const platform = (name: string, archive: string) => ({
    platform: name,
    archive,
    url: `https://github.com/acme/mirror/releases/download/${version}/${archive}`,
  })
  return {
    version,
    commit: 'deadbeef',
    buildTime: '2026-09-27T14:23:41Z',
    repository: 'acme/mirror',
    releaseUrl: `https://github.com/acme/mirror/releases/tag/${version}`,
    checksumUrl: `https://github.com/acme/mirror/releases/download/${version}/SHA256SUMS`,
    manifestUrl: `https://github.com/acme/mirror/releases/download/${version}/manifest.json`,
    schemaVersion: 15,
    assets: [
      platform('linux-amd64', `tunnelmesh-${version}-linux-amd64.tar.gz`),
      platform('darwin-arm64', `tunnelmesh-${version}-darwin-arm64.tar.gz`),
      platform('windows-amd64', `tunnelmesh-${version}-windows-amd64.zip`),
    ],
  }
}

function buttonByText(scope: ParentNode, label: string) {
  return Array.from(scope.querySelectorAll('button')).find((node) => (node.textContent || '').trim() === label)
}

// The page used to hand operators a per-platform archive, its download link and a
// copyable checksum command. The release assets exist on GitHub again, and the
// management API still returns them, so the surface has to come back.
describe('release download surface', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
    vi.mocked(getDownloads).mockReset()
  })

  it('lists every platform archive with the download URL from the API', async () => {
    const info = release()
    vi.mocked(getDownloads).mockResolvedValue(info)
    const { container, unmount } = await mountDownloads()
    try {
      const cards = container.querySelectorAll('.asset')
      expect(cards).toHaveLength(info.assets.length)
      info.assets.forEach((asset, index) => {
        const card = cards[index]
        expect(card.textContent).toContain(asset.platform)
        expect(card.textContent).toContain(asset.archive)
        const link = card.querySelector('a.download') as HTMLAnchorElement
        expect(link.getAttribute('href')).toBe(asset.url)
      })
      // The archive type is shown, because zip and tar.gz are verified differently.
      expect(cards[2].textContent).toContain('zip')
      expect(cards[0].textContent).toContain('tar.gz')
    } finally {
      unmount()
    }
  })

  // The repository is configuration (downloads.github_repository), so the links
  // must come from the API instead of a host baked into the bundle.
  it('opens the configured release rather than a hard-coded repository', async () => {
    vi.mocked(getDownloads).mockResolvedValue(release())
    const { container, unmount } = await mountDownloads()
    try {
      const links = Array.from(container.querySelectorAll('a'))
      const releaseLink = links.find((node) => (node.textContent || '').trim() === i18n.global.t('downloads.releaseLink'))!
      expect(releaseLink, 'release link').toBeTruthy()
      expect(releaseLink.getAttribute('href')).toBe('https://github.com/acme/mirror/releases/tag/v1.3.0')
      expect(container.innerHTML).not.toContain('nnworld/TunnelMesh')
    } finally {
      unmount()
    }
  })

  it('copies the checksum verification command for one archive', async () => {
    const info = release()
    vi.mocked(getDownloads).mockResolvedValue(info)
    const writeText = vi.fn().mockResolvedValue(undefined)
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText } })
    const { container, unmount } = await mountDownloads()
    try {
      const card = container.querySelectorAll('.asset')[1]
      buttonByText(card, i18n.global.t('downloads.checksumCommand'))!.click()
      await flush()
      expect(writeText).toHaveBeenCalledWith(`curl -fsSL ${info.checksumUrl} | grep '${info.assets[1].archive}' | sha256sum -c -`)
    } finally {
      unmount()
    }
  })

  // A binary built from source reports version "dev", whose release assets were
  // never published. Showing dead links without saying why wastes an operator's
  // time during an upgrade, so the page states it instead.
  it('warns when the running build has no published release assets', async () => {
    vi.mocked(getDownloads).mockResolvedValue(release('dev'))
    const { container, unmount } = await mountDownloads()
    try {
      expect(container.textContent).toContain(i18n.global.t('downloads.notPublished'))
    } finally {
      unmount()
    }
  })

  it('stays quiet for a published release', async () => {
    vi.mocked(getDownloads).mockResolvedValue(release())
    const { container, unmount } = await mountDownloads()
    try {
      expect(container.textContent).not.toContain(i18n.global.t('downloads.notPublished'))
    } finally {
      unmount()
    }
  })
})
