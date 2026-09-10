import { readFileSync } from 'node:fs'
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
})
