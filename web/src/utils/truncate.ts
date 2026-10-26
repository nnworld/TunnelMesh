// Identifiers are compared by their ends (prefix says what, suffix says which),
// so the middle is what gets dropped when space runs out.
export function middleTruncate(value: string, head = 10, tail = 6): string {
  if (value.length <= head + tail + 1) return value
  return `${value.slice(0, head)}…${value.slice(-tail)}`
}
