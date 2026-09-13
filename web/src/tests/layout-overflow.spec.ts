import { describe, expect, it } from 'vitest'
import { readFileSync, readdirSync } from 'node:fs'

const viewFiles = readdirSync('src/views').filter(name => name.endsWith('.vue'))
const styleDirs = ['src/views', 'src/components', 'src/layouts']

function read(path: string) {
  return readFileSync(path, 'utf8')
}

function styleBlocks(source: string) {
  return [...source.matchAll(/<style[^>]*>([\s\S]*?)<\/style>/g)].map(match => match[1]).join('\n')
}

function templateOf(source: string) {
  return /<template>([\s\S]*)<\/template>/.exec(source)?.[1] ?? ''
}

function rootClassOf(source: string) {
  const first = /^\s*<[a-z-]+[^>]*?class="([^"]*)"/m.exec(templateOf(source))
  return first?.[1] ?? ''
}

// display:grid 而没有显式列定义时，隐式列按 auto（min-content..max-content）计算，
// 宽表格会把轨道撑开并把滚动条推到页面级，操作列的 fixed="right" 也会失效。
function unboundedGridSelectors(css: string) {
  const offenders: string[] = []
  const rule = /([^{}]+)\{([^{}]*)\}/g
  let match: RegExpExecArray | null
  while ((match = rule.exec(css))) {
    const body = match[2].replace(/\s+/g, '')
    if (!/display:grid/.test(body)) continue
    if (/grid-template-columns|grid-template-areas|grid-auto-flow|place-items/.test(body)) continue
    offenders.push(match[1].trim().replace(/\s+/g, ' '))
  }
  return offenders
}

describe('admin pages cannot overflow horizontally', () => {
  it('roots every console page in the bounded shared page grid', () => {
    for (const name of viewFiles) {
      if (name === 'Login.vue') continue
      expect(rootClassOf(read(`src/views/${name}`)), name).toMatch(/tm-page/)
    }
  })

  it('declares explicit columns for every grid container', () => {
    for (const dir of styleDirs) {
      for (const name of readdirSync(dir).filter(file => file.endsWith('.vue'))) {
        const offenders = unboundedGridSelectors(styleBlocks(read(`${dir}/${name}`)))
        expect(offenders, `${dir}/${name} -> ${offenders.join(', ')}`).toEqual([])
      }
    }
  })

  it('keeps the shared page grid bounded in tokens.css', () => {
    expect(read('src/styles/tokens.css'))
      .toContain('.tm-page { display: grid; grid-template-columns: minmax(0, 1fr); gap: 16px; }')
  })

  it('pins the action column on tables that need internal scrolling', () => {
    // 列宽合计超过约 1270px（1521 视口减去侧栏和内边距）时必须有 fixed="right"，
    // 否则表内横向滚动后管理员看不到操作按钮。
    for (const name of viewFiles) {
      const template = templateOf(read(`src/views/${name}`))
      for (const block of template.match(/<el-table[\s>][\s\S]*?<\/el-table>/g) ?? []) {
        let total = 0
        for (const tag of block.match(/<el-table-column[^>]*>/g) ?? []) {
          const width = /\bwidth="(\d+)"/.exec(tag)
          const minWidth = /\bmin-width="(\d+)"/.exec(tag)
          total += width && !minWidth ? Number(width[1]) : minWidth ? Number(minWidth[1]) : 100
        }
        if (total > 1270) expect(block, `${name} table min=${total}px`).toContain('fixed="right"')
      }
    }
  })
})
