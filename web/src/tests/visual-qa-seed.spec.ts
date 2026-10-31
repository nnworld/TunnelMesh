import { describe, expect, it } from 'vitest'
// @ts-expect-error - plain ESM script shipped without type declarations
import { agentNames, parseArgs, planSeed, SCREENSHOT_MATRIX } from '../../scripts/visual-qa/seed.mjs'

// The visual QA harness is how this redesign is judged, so the data it screenshots
// has to be deterministic: two runs must show the same panel.
describe('visual QA seed plan', () => {
  it('generates unique, deterministic agent names', () => {
    const names: string[] = agentNames(40)
    expect(names).toHaveLength(40)
    expect(new Set(names).size).toBe(40)
    expect(agentNames(40)).toEqual(names)
    for (const name of names) expect(name).toMatch(/^[a-z][a-z0-9-]*$/)
  })

  it('plans a mix of online, offline and disabled agents', () => {
    const plan = planSeed({ agents: 40, online: 8 })
    expect(plan.agents).toHaveLength(40)
    expect(plan.agents.filter((a: { online: boolean }) => a.online)).toHaveLength(8)
    expect(plan.agents.some((a: { enabled: boolean }) => !a.enabled)).toBe(true)
    // A disabled agent cannot be online: the panel's three LED states must all appear.
    expect(plan.agents.filter((a: { online: boolean; enabled: boolean }) => a.online && !a.enabled)).toEqual([])
    expect(plan.agents.some((a: { online: boolean; enabled: boolean }) => !a.online && a.enabled)).toBe(true)
  })

  it('never plans more online agents than there are enabled ones', () => {
    const plan = planSeed({ agents: 5, online: 50 })
    expect(plan.agents.filter((a: { online: boolean }) => a.online).length).toBeLessThanOrEqual(5)
  })

  it('covers every page in both themes and both viewports', () => {
    const shots = SCREENSHOT_MATRIX as { page: string; theme: string; viewport: string }[]
    const pages = new Set(shots.map((s) => s.page))
    expect([...pages].sort()).toEqual(['agents', 'audit-logs', 'dashboard', 'login', 'security', 'tokens'])
    for (const page of pages) {
      const forPage = shots.filter((s) => s.page === page)
      expect(forPage).toHaveLength(4)
      expect(new Set(forPage.map((s) => s.theme))).toEqual(new Set(['light', 'dark']))
      expect(new Set(forPage.map((s) => s.viewport))).toEqual(new Set(['desktop', 'mobile']))
    }
  })

  it('parses arguments with sensible defaults', () => {
    expect(parseArgs([])).toMatchObject({ locale: 'zh-CN', agents: 40, online: 8, keep: false, skipBuild: false })
    expect(parseArgs(['--locale', 'en-US', '--agents', '12', '--online', '3', '--keep', '--skip-build'])).toMatchObject({
      locale: 'en-US', agents: 12, online: 3, keep: true, skipBuild: true,
    })
    expect(() => parseArgs(['--agents', 'many'])).toThrow(/--agents/)
    expect(() => parseArgs(['--locale', 'fr-FR'])).toThrow(/--locale/)
    expect(() => parseArgs(['--nope'])).toThrow(/--nope/)
  })
})
