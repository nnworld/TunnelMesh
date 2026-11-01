// Built-in host facts arrive as JSON strings under `system.*` keys. Parsing is kept
// here, away from the components, so a malformed block only ever loses itself.

export type OSBlock = { name: string; kernel?: string; arch?: string; hostname?: string }
export type CPUBlock = { model: string; logicalCpus: number }
export type DiskBlock = { mount: string; fs: string; totalBytes: number }
export type NICBlock = { name: string; mac?: string; addresses: string[] }
export type Inventory = {
  os?: OSBlock
  cpu?: CPUBlock
  memory?: { totalBytes: number }
  gpus: string[]
  disks: DiskBlock[]
  nics: NICBlock[]
  hasAny: boolean
}

function block<T>(values: Record<string, string>, key: string, read: (raw: any) => T | undefined): T | undefined {
  const text = values[key]
  if (!text) return undefined
  try {
    return read(JSON.parse(text))
  } catch {
    return undefined
  }
}

export function parseInventory(values: Record<string, string>): Inventory {
  const os = block(values, 'system.os', (raw): OSBlock | undefined => (raw && typeof raw.name === 'string' ? {
    name: raw.name, kernel: raw.kernel, arch: raw.arch, hostname: raw.hostname,
  } : undefined))
  const cpu = block(values, 'system.cpu', (raw): CPUBlock | undefined => (raw ? { model: String(raw.model ?? ''), logicalCpus: Number(raw.logical_cpus ?? 0) } : undefined))
  const memory = block(values, 'system.memory', (raw) => (raw && raw.total_bytes ? { totalBytes: Number(raw.total_bytes) } : undefined))
  const gpus = block(values, 'system.gpu', (raw) => (Array.isArray(raw) ? raw.map(String) : undefined)) ?? []
  const disks = block(values, 'system.disks', (raw) => (Array.isArray(raw) ? raw.map((d): DiskBlock => ({
    mount: String(d.mount), fs: String(d.fs), totalBytes: Number(d.total_bytes),
  })) : undefined)) ?? []
  const nics = block(values, 'system.nics', (raw) => (Array.isArray(raw) ? raw.map((n): NICBlock => ({
    name: String(n.name), mac: n.mac || undefined, addresses: Array.isArray(n.addresses) ? n.addresses.map(String) : [],
  })) : undefined)) ?? []
  const hasAny = Boolean(os || cpu || memory || gpus.length || disks.length || nics.length)
  return { os, cpu, memory, gpus, disks, nics, hasAny }
}

/** formatBytes renders a byte count in binary units, one decimal from GiB upward. */
export function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  const units = ['KiB', 'MiB', 'GiB', 'TiB', 'PiB']
  let value = bytes / 1024
  let unit = 0
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024
    unit++
  }
  return unit >= 2 ? `${value.toFixed(1)} ${units[unit]}` : `${Math.round(value)} ${units[unit]}`
}

/** isInventoryName tells built-in keys apart from operator-declared metadata. */
export function isInventoryName(name: string): boolean {
  return name.startsWith('system.')
}
