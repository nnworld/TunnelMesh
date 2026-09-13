import { execFileSync } from 'node:child_process'
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

describe('production bundle configuration', () => {
  it('loads Element Plus components on demand', () => {
    const main = readFileSync('src/main.ts', 'utf8')
    const config = readFileSync('vite.config.ts', 'utf8')
    expect(main).not.toContain("from 'element-plus'")
    expect(main).not.toContain('element-plus/dist/index.css')
    expect(config).toContain('unplugin-vue-components/vite')
    expect(config).toContain('ElementPlusResolver')
  })

  it('splits stable vendor dependencies from application chunks', () => {
    const config = readFileSync('vite.config.ts', 'utf8')
    expect(config).toContain('manualChunks')
    expect(config).toContain("'vue-vendor'")
  })

  it('loads styles for menu components imported in render functions', () => {
    const shell = readFileSync('src/layouts/AppShell.vue', 'utf8')
    expect(shell).toContain("element-plus/es/components/menu/style/css")
  })

  it('loads styles for breadcrumb components imported in render functions', () => {
    const header = readFileSync('src/components/PageHeader.vue', 'utf8')
    expect(header).toContain("element-plus/es/components/breadcrumb/style/css")
    expect(header).toContain("element-plus/es/components/breadcrumb-item/style/css")
  })

  // unplugin-vue-components only injects styles for components it resolves from
  // templates. Components imported explicitly in <script setup> therefore need
  // their style entry imported by hand, otherwise they render unstyled on every
  // route whose chunk does not happen to use the same tag in a template: the
  // empty-state SVG showed up as an oversized black shape on list pages.
  it('loads styles for components imported explicitly in script setup', () => {
    const dataState = readFileSync('src/components/DataState.vue', 'utf8')
    for (const style of ['alert', 'button', 'empty', 'skeleton']) {
      expect(dataState, style).toContain(`element-plus/es/components/${style}/style/css`)
    }
    const statusTag = readFileSync('src/components/StatusTag.vue', 'utf8')
    expect(statusTag).toContain('element-plus/es/components/tag/style/css')
  })

  // The admin UI ships inside the Server binary through `//go:embed
  // all:web_dist`, so a build that forgets the sync step silently ships the
  // previous bundle: the binary rebuilds, the embedded assets do not change,
  // and every frontend fix looks "not released". The sync therefore has to be
  // part of `npm run build` itself, not a remembered manual step.
  it('syncs the build output into the Go embed directory as part of build', () => {
    const pkg = JSON.parse(readFileSync('package.json', 'utf8')) as { scripts: Record<string, string> }
    expect(pkg.scripts.build).toContain('vite build')
    expect(pkg.scripts.build).toContain('scripts/sync-web-dist.mjs')
  })

  // Hashed chunk names change on every build, so the embed directory must be
  // mirrored (stale files deleted), not merged into: leftovers would be
  // embedded forever and bloat every Server binary.
  it('mirrors the source tree into the embed directory and deletes stale files', () => {
    const root = mkdtempSync(join(tmpdir(), 'tm-sync-web-dist-'))
    const source = join(root, 'dist')
    const target = join(root, 'web_dist')
    try {
      mkdirSync(join(source, 'assets'), { recursive: true })
      mkdirSync(target, { recursive: true })
      writeFileSync(join(source, 'index.html'), '<html>new</html>')
      writeFileSync(join(source, 'assets', 'app-new.js'), 'console.log(1)')
      writeFileSync(join(target, 'index.html'), '<html>old</html>')
      writeFileSync(join(target, 'app-old.js'), 'stale')

      execFileSync(process.execPath, [resolve('scripts/sync-web-dist.mjs'), source, target], { stdio: 'pipe' })

      expect(readFileSync(join(target, 'index.html'), 'utf8')).toBe('<html>new</html>')
      expect(readFileSync(join(target, 'assets', 'app-new.js'), 'utf8')).toBe('console.log(1)')
      expect(existsSync(join(target, 'app-old.js'))).toBe(false)
    } finally {
      rmSync(root, { recursive: true, force: true })
    }
  })

  // A missing build output must fail loudly instead of "syncing" an empty or
  // half-written tree into the embed directory.
  it('fails when the build output is missing', () => {
    const root = mkdtempSync(join(tmpdir(), 'tm-sync-web-dist-'))
    try {
      let message = ''
      try {
        execFileSync(process.execPath, [resolve('scripts/sync-web-dist.mjs'), join(root, 'nope'), join(root, 'web_dist')], { stdio: 'pipe' })
      } catch (error) {
        message = String(error)
      }
      expect(message).toContain('sync-web-dist: source build output missing')
    } finally {
      rmSync(root, { recursive: true, force: true })
    }
  })
  // The release script cross-compiles the Server, and the Server embeds
  // whatever internal/server/web_dist holds at that moment. A clean checkout
  // has no such directory at all (it is gitignored), and a release host may
  // still hold the previous bundle, so the script must refuse to build instead
  // of shipping a binary with no UI or with last release's UI.
  it('refuses to release when the embed directory is missing or stale', () => {
    const script = readFileSync('../scripts/build-release.sh', 'utf8')
    expect(script).toContain('internal/server/web_dist/index.html')
    expect(script).toContain('cd web && npm run build')
  })

})
