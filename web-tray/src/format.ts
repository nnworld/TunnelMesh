/** Display helpers shared by the tabs. They are pure so tests can pin the format. */

export function formatUptime(totalSeconds: number): string {
  const seconds = Math.max(0, Math.floor(totalSeconds || 0))
  const days = Math.floor(seconds / 86400)
  const hours = Math.floor((seconds % 86400) / 3600)
  const minutes = Math.floor((seconds % 3600) / 60)
  const rest = seconds % 60
  if (days > 0) return `${days}d ${hours}h ${minutes}m`
  if (hours > 0) return `${hours}h ${minutes}m ${rest}s`
  if (minutes > 0) return `${minutes}m ${rest}s`
  return `${rest}s`
}

const BYTE_UNITS = ['B', 'KB', 'MB', 'GB', 'TB', 'PB']

export function formatBytes(value: number): string {
  let size = Math.max(0, Number(value) || 0)
  let unit = 0
  while (size >= 1024 && unit < BYTE_UNITS.length - 1) {
    size /= 1024
    unit += 1
  }
  const rounded = unit === 0 ? size.toFixed(0) : size.toFixed(size >= 100 ? 0 : size >= 10 ? 1 : 2)
  return `${rounded} ${BYTE_UNITS[unit]}`
}

export function formatTimestamp(value?: string | null): string {
  if (!value) return ''
  const parsed = new Date(value)
  if (Number.isNaN(parsed.getTime())) return value
  return parsed.toLocaleString()
}

export function formatTimeOfDay(value?: string | null): string {
  if (!value) return ''
  const parsed = new Date(value)
  if (Number.isNaN(parsed.getTime())) return value
  return parsed.toLocaleTimeString()
}

/** Masks a secret for display. The tray never sends one, but a locally typed value is
 * echoed in the summary and must not be readable over somebody's shoulder. */
export function maskSecret(value: string): string {
  if (!value) return ''
  return '•'.repeat(Math.min(Math.max(value.length, 8), 24))
}
