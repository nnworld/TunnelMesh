import { describe, expect, it } from 'vitest'
import { formatBytes, formatUptime, maskSecret } from '../format'

describe('display helpers', () => {
  it('renders uptime in the largest useful units', () => {
    expect(formatUptime(0)).toBe('0s')
    expect(formatUptime(45)).toBe('45s')
    expect(formatUptime(125)).toBe('2m 5s')
    expect(formatUptime(3725)).toBe('1h 2m 5s')
    expect(formatUptime(90061)).toBe('1d 1h 1m')
    expect(formatUptime(-5)).toBe('0s')
    expect(formatUptime(Number.NaN)).toBe('0s')
  })

  it('renders byte counters with binary units', () => {
    expect(formatBytes(0)).toBe('0 B')
    expect(formatBytes(512)).toBe('512 B')
    expect(formatBytes(2048)).toBe('2.00 KB')
    expect(formatBytes(5 * 1024 * 1024)).toBe('5.00 MB')
    expect(formatBytes(1536 * 1024 * 1024)).toBe('1.50 GB')
    expect(formatBytes(-10)).toBe('0 B')
  })

  it('masks a secret without revealing its length beyond a bound', () => {
    expect(maskSecret('')).toBe('')
    expect(maskSecret('abc')).toBe('•'.repeat(8))
    expect(maskSecret('x'.repeat(64))).toBe('•'.repeat(24))
    expect(maskSecret('hunter2')).not.toContain('hunter2')
  })
})
