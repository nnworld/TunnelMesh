// Presentation helpers shared by every surface that lists audit events.
//
// The overview card and the audit table describe the same record, so the rules for
// reading it live here once instead of in two copies that quietly drift apart.
import type { AuditLog } from '../api/client'

/** Correlation id stamped by the server; shown as its own field, never as noise in a summary. */
export const AUDIT_TRACE_KEY = 'traceId'

/**
 * Renders each detail field as `key=value`. Objects and arrays are compacted to
 * JSON so a nested value cannot swallow the line, and the trace id is left out
 * because it is a 32-character hex string that pushes the useful facts away.
 */
export function auditDetailsEntries(details?: Record<string, unknown> | null): string[] {
  if (!details) return []
  return Object.entries(details)
    .filter(([key]) => key !== AUDIT_TRACE_KEY)
    .map(([key, value]) => {
      const rendered = typeof value === 'object' && value !== null ? JSON.stringify(value) : String(value)
      return `${key}=${rendered}`
    })
}

/** Shortens a detail payload to `limit` pairs, marking the overflow with an ellipsis. */
export function auditDetailsSummary(details?: Record<string, unknown> | null, limit = 3): string {
  const pairs = auditDetailsEntries(details)
  if (!pairs.length) return ''
  return pairs.slice(0, limit).join(' · ') + (pairs.length > limit ? ' …' : '')
}

/** Whole payload as JSON, for the `title` hover of a summarized line. */
export function auditDetailsTitle(details?: Record<string, unknown> | null): string {
  return details && Object.keys(details).length ? JSON.stringify(details) : ''
}

/**
 * Labels who acted: the username resolved by the server when the account still
 * exists, otherwise the stored identifier, otherwise the caller's wording for a
 * system event. The identifier stays the authority; this is only a label.
 */
export function auditActorLabel(actor: Pick<AuditLog, 'actorUserId' | 'actorUsername'>, systemLabel: string): string {
  return actor.actorUsername || actor.actorUserId || systemLabel
}

/** Trace id of the request that produced the event, or empty when it predates tracing. */
export function auditTraceId(details?: Record<string, unknown> | null): string {
  const value = details?.[AUDIT_TRACE_KEY]
  return typeof value === 'string' ? value : ''
}
