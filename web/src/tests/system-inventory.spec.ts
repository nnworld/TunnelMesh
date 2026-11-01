import { describe, expect, it } from 'vitest'
import { formatBytes, parseInventory } from '../utils/inventory'

const values = {
  'system.os': JSON.stringify({ name: 'Ubuntu 24.04 LTS', kernel: '6.8.0', arch: 'amd64', hostname: 'edge-1' }),
  'system.cpu': JSON.stringify({ model: 'Xeon 8375C', logical_cpus: 8 }),
  'system.memory': JSON.stringify({ total_bytes: 17179869184 }),
  'system.gpu': JSON.stringify(['Intel UHD Graphics 630']),
  'system.disks': JSON.stringify([{ mount: '/', fs: 'ext4', total_bytes: 107374182400 }]),
  'system.nics': JSON.stringify([{ name: 'eth0', mac: 'aa:bb:cc:dd:ee:ff', addresses: ['10.0.0.5'] }]),
  'region': 'cn-north',
}

describe('parseInventory', () => {
  it('reads every built-in block', () => {
    const inventory = parseInventory(values)
    expect(inventory.os?.name).toBe('Ubuntu 24.04 LTS')
    expect(inventory.cpu?.logicalCpus).toBe(8)
    expect(inventory.memory?.totalBytes).toBe(17179869184)
    expect(inventory.gpus).toEqual(['Intel UHD Graphics 630'])
    expect(inventory.disks[0]).toMatchObject({ mount: '/', fs: 'ext4' })
    expect(inventory.nics[0]).toMatchObject({ name: 'eth0', mac: 'aa:bb:cc:dd:ee:ff' })
  })

  it('ignores operator fields and absent blocks rather than inventing them', () => {
    const inventory = parseInventory({ region: 'cn-north' })
    expect(inventory.os).toBeUndefined()
    expect(inventory.disks).toEqual([])
    expect(inventory.nics).toEqual([])
    expect(inventory.hasAny).toBe(false)
  })

  it('survives a malformed block by dropping only that block', () => {
    const inventory = parseInventory({ ...values, 'system.cpu': '{not json' })
    expect(inventory.cpu).toBeUndefined()
    expect(inventory.os?.name).toBe('Ubuntu 24.04 LTS')
  })
})

describe('formatBytes', () => {
  it('uses binary units with one decimal above a gibibyte', () => {
    expect(formatBytes(17179869184)).toBe('16.0 GiB')
    expect(formatBytes(512)).toBe('512 B')
    expect(formatBytes(0)).toBe('0 B')
  })
})
