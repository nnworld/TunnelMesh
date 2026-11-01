export type ActionHue = 'brand' | 'violet' | 'lime' | 'warn' | 'sky' | 'rose' | 'neutral'

// Failure and removal words win over the family: a rejected agent connection is
// a warning-worthy event whichever resource it touched.
const ALARM = /(fail|reject|revok|delet|denied|forbid)/i

const FAMILY: Array<[prefix: RegExp, hue: ActionHue]> = [
  [/^agent/, 'brand'],
  [/^token/, 'violet'],
  [/^remote_server/, 'lime'],
  [/^credential/, 'warn'],
  [/^policy/, 'sky'],
  [/^proxy/, 'sky'],
]

/** actionHue picks the accent for an audit action so the feed groups by family. */
export function actionHue(action: string): ActionHue {
  if (!action) return 'neutral'
  if (ALARM.test(action)) return 'rose'
  return FAMILY.find(([prefix]) => prefix.test(action))?.[1] ?? 'neutral'
}
