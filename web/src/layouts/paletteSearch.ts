import type { PaletteEntry } from './navigation'

type Translate = (key: string) => string

// `alternate` is the other locale's translator: operators who switch between
// languages keep typing the label they learned first.

/**
 * Rank palette entries for a query. Label prefix beats word prefix beats
 * substring; ties keep navigation order (sort is stable).
 */
export function rankEntries(entries: PaletteEntry[], query: string, t: Translate, alternate?: Translate): PaletteEntry[] {
  const q = query.trim().toLowerCase()
  if (!q) return entries
  const score = (entry: PaletteEntry): number => {
    let best = 0
    for (const translate of alternate ? [t, alternate] : [t]) {
      const label = translate(entry.labelKey).toLowerCase()
      if (label.startsWith(q)) best = Math.max(best, 3)
      else if (label.split(/[\s/-]+/).some(word => word.startsWith(q))) best = Math.max(best, 2)
      else if (label.includes(q)) best = Math.max(best, 1)
    }
    return best
  }
  return entries
    .map((entry, index) => ({ entry, index, score: score(entry) }))
    .filter(row => row.score > 0)
    .sort((a, b) => b.score - a.score || a.index - b.index)
    .map(row => row.entry)
}
