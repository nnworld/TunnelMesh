import { describe, expect, it, vi } from 'vitest'
import {
  deleteEntry,
  downloadFile,
  listDirectory,
  renameEntry,
  SFTP_MAX_TRANSFER_BYTES,
  SFTP_CHUNK_SIZE,
  uploadFile,
  type SFTPClient,
  type SFTPEntry,
} from './sftp'

function fakeClient(overrides: Partial<SFTPClient> = {}): SFTPClient {
  return {
    readDirectory: vi.fn(async () => [{
      name: 'logs',
      type: 'directory' as const,
      size: 4096,
      modifiedAt: '2026-09-12T08:00:00.000Z',
      permissions: 'drwxr-xr-x',
    }, {
      name: 'app.log',
      type: 'file' as const,
      size: 123,
      modifiedAt: '2026-09-12T08:01:00.000Z',
      permissions: '-rw-r--r--',
    }, {
      name: 'current',
      type: 'symlink' as const,
      size: 7,
      modifiedAt: '2026-09-12T08:02:00.000Z',
      permissions: 'lrwxrwxrwx',
    }] satisfies Omit<SFTPEntry, 'path'>[]),
    openFile: vi.fn(async () => ({
      read: vi.fn(async () => 0),
      write: vi.fn(async () => 0),
      close: vi.fn(async () => undefined),
    })),
    unlink: vi.fn(async () => undefined),
    rmdir: vi.fn(async () => undefined),
    rename: vi.fn(async () => undefined),
    realpath: vi.fn(async (path: string) => path),
    close: vi.fn(async () => undefined),
    ...overrides,
  }
}

describe('SFTP operations', () => {
  it('maps raw directory entries to absolute paths', async () => {
    const client = fakeClient()

    const entries = await listDirectory(client, '/var/www')

    expect(entries).toEqual([
      expect.objectContaining({ name: 'logs', path: '/var/www/logs', type: 'directory' }),
      expect.objectContaining({ name: 'app.log', path: '/var/www/app.log', type: 'file', size: 123 }),
      expect.objectContaining({ name: 'current', path: '/var/www/current', type: 'symlink' }),
    ])
  })

  it('uploads a file in bounded chunks and reports progress', async () => {
    const client = fakeClient()
    const content = new Uint8Array(SFTP_CHUNK_SIZE * 2 + 1024)
    const file = new File([content], 'bundle.tar', { type: 'application/octet-stream' })
    const written: number[] = []
    client.openFile = vi.fn(async () => ({
      read: vi.fn(async () => 0),
      write: vi.fn(async (chunk: Uint8Array) => {
        written.push(chunk.length)
        return chunk.length
      }),
      close: vi.fn(async () => undefined),
    }))
    const progress: number[] = []

    await uploadFile(client, file, '/tmp/bundle.tar', (event) => progress.push(event.transferred))

    expect(client.openFile).toHaveBeenCalledWith('/tmp/bundle.tar', 'write')
    expect(written).toEqual([SFTP_CHUNK_SIZE, SFTP_CHUNK_SIZE, 1024])
    expect(progress).toEqual([0, SFTP_CHUNK_SIZE, SFTP_CHUNK_SIZE * 2, content.length])
  })

  it('rejects an upload above the one-gibibyte limit before opening a file', async () => {
    const client = fakeClient()
    const file = { name: 'too-large.bin', size: SFTP_MAX_TRANSFER_BYTES + 1 } as File

    await expect(uploadFile(client, file, '/tmp/too-large.bin')).rejects.toThrow('SFTP transfer exceeds 1 GiB limit')
    expect(client.openFile).not.toHaveBeenCalled()
  })

  it('downloads in bounded chunks and reports progress', async () => {
    const client = fakeClient()
    const size = SFTP_CHUNK_SIZE * 2 + 512
    let transferred = 0
    client.openFile = vi.fn(async () => ({
      read: vi.fn(async (buffer: Uint8Array) => {
        const remaining = size - transferred
        const count = Math.min(remaining, SFTP_CHUNK_SIZE)
        transferred += count
        buffer.fill(65, 0, count)
        return count
      }),
      write: vi.fn(async () => 0),
      close: vi.fn(async () => undefined),
    }))
    const progress: number[] = []

    const blob = await downloadFile(client, '/tmp/data.bin', size, (event) => progress.push(event.transferred))

    expect(blob.size).toBe(size)
    expect(client.openFile).toHaveBeenCalledWith('/tmp/data.bin', 'read')
    expect(progress).toEqual([0, SFTP_CHUNK_SIZE, SFTP_CHUNK_SIZE * 2, size])
  })

  it('uses unlink for files and symlinks and rmdir for directories', async () => {
    const client = fakeClient()

    await deleteEntry(client, { name: 'a', path: '/tmp/a', type: 'file', size: 1 })
    await deleteEntry(client, { name: 'b', path: '/tmp/b', type: 'symlink', size: 1 })
    await deleteEntry(client, { name: 'c', path: '/tmp/c', type: 'directory', size: 1 })

    expect(client.unlink).toHaveBeenCalledWith('/tmp/a')
    expect(client.unlink).toHaveBeenCalledWith('/tmp/b')
    expect(client.rmdir).toHaveBeenCalledWith('/tmp/c')
  })

  it('renames by source and destination paths', async () => {
    const client = fakeClient()

    await renameEntry(client, { name: 'old', path: '/tmp/old', type: 'file', size: 1 }, '/tmp/new')

    expect(client.rename).toHaveBeenCalledWith('/tmp/old', '/tmp/new')
  })
})
