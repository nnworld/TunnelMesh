import { describe, expect, it } from 'vitest'
import { auditActorLabel, auditDetailsEntries, auditDetailsSummary, auditDetailsTitle, auditTraceId } from '../utils/audit'

// Both the overview card and the audit table label the same audit record, so the
// formatting lives in one helper instead of a second copy that can drift.
describe('audit presentation helpers', () => {
  it('renders scalars as key=value pairs and objects as JSON', () => {
    expect(auditDetailsEntries({ name: 'edge', enabled: true, scope: { protocols: ['tcp'] } })).toEqual([
      'name=edge',
      'enabled=true',
      'scope={"protocols":["tcp"]}',
    ])
  })

  it('summarizes with an overflow marker and drops empty payloads', () => {
    expect(auditDetailsSummary({ a: 1, b: 2, c: 3, d: 4 }, 3)).toBe('a=1 · b=2 · c=3 …')
    expect(auditDetailsSummary({ a: 1, b: 2 }, 3)).toBe('a=1 · b=2')
    expect(auditDetailsSummary(null, 3)).toBe('')
    expect(auditDetailsSummary({}, 3)).toBe('')
  })

  it('keeps the whole payload reachable for a hover', () => {
    expect(auditDetailsTitle({ name: 'edge' })).toContain('"name":"edge"')
    expect(auditDetailsTitle(null)).toBe('')
  })

  it('prefers the resolved username, then the id, then the system fallback', () => {
    expect(auditActorLabel({ actorUserId: 'user-7', actorUsername: 'alice' }, 'System')).toBe('alice')
    expect(auditActorLabel({ actorUserId: 'user-7' }, 'System')).toBe('user-7')
    expect(auditActorLabel({}, 'System')).toBe('System')
  })

  it('reads the trace id out of details', () => {
    expect(auditTraceId({ traceId: '4bf92f3577b34da6a3ce929d0e0e4736' })).toBe('4bf92f3577b34da6a3ce929d0e0e4736')
    expect(auditTraceId({})).toBe('')
    expect(auditTraceId(null)).toBe('')
  })
})
