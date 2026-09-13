import { afterEach, describe, expect, it, vi } from 'vitest'
import { downloadBlob, downloadBytes } from './download'

function stubObjectURL() {
  const createObjectURL = vi.fn(() => 'blob:one')
  const revokeObjectURL = vi.fn()
  Object.defineProperty(URL, 'createObjectURL', { value: createObjectURL, configurable: true })
  Object.defineProperty(URL, 'revokeObjectURL', { value: revokeObjectURL, configurable: true })
  return { createObjectURL, revokeObjectURL }
}

function stubAnchor() {
  const anchor = { click: vi.fn(), href: '', download: '' }
  vi.spyOn(document, 'createElement').mockReturnValue(anchor as unknown as HTMLElement)
  return anchor
}

describe('browser download helpers', () => {
  afterEach(() => { vi.restoreAllMocks() })

  it('saves bytes under the given name and releases the object URL', () => {
    const { createObjectURL, revokeObjectURL } = stubObjectURL()
    const anchor = stubAnchor()

    downloadBytes('report.txt', new TextEncoder().encode('payload'))

    expect(anchor.download).toBe('report.txt')
    expect(anchor.href).toBe('blob:one')
    expect(anchor.click).toHaveBeenCalledTimes(1)
    expect(revokeObjectURL).toHaveBeenCalledWith('blob:one')
    const blob = createObjectURL.mock.calls[0][0] as Blob
    expect(blob.size).toBe('payload'.length)
  })

  it('accepts a ready-made blob so SFTP and ZMODEM share one path', () => {
    stubObjectURL()
    const anchor = stubAnchor()
    const blob = new Blob(['content'])

    downloadBlob('file.bin', blob)

    expect(anchor.download).toBe('file.bin')
    expect(anchor.click).toHaveBeenCalledTimes(1)
  })
})
