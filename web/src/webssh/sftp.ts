export type SFTPEntryType = 'file' | 'directory' | 'symlink'

export interface SFTPEntry {
  name: string
  path: string
  type: SFTPEntryType
  size: number
  modifiedAt?: string
  permissions?: string
}

export interface SFTPProgress {
  path: string
  transferred: number
  total: number
}

export type SFTPErrorKind = 'not-found' | 'permission-denied' | 'closed' | 'failure'

export class SFTPError extends Error {
  constructor(readonly kind: SFTPErrorKind, message?: string) {
    super(message || kind)
    this.name = 'SFTPError'
  }
}

export interface SFTPFileHandle {
  read(buffer: Uint8Array): Promise<number>
  write(chunk: Uint8Array): Promise<number>
  close(): Promise<void>
}

export type SFTPOpenMode = 'read' | 'write'

export type RawSFTPEntry = Omit<SFTPEntry, 'path'>

/**
 * Low-level protocol adapter implemented by the browser SSH module. Helpers in
 * this module own chunking, limits, progress, and path composition so the UI
 * does not depend on libssh2's memory layout.
 */
export interface SFTPClient {
  readDirectory(path: string): Promise<RawSFTPEntry[]>
  openFile(path: string, mode: SFTPOpenMode): Promise<SFTPFileHandle>
  unlink(path: string): Promise<void>
  rmdir(path: string): Promise<void>
  rename(path: string, nextPath: string): Promise<void>
  realpath(path: string): Promise<string>
  close(): Promise<void>
}

export const SFTP_CHUNK_SIZE = 256 * 1024
export const SFTP_MAX_TRANSFER_BYTES = 1024 * 1024 * 1024

function assertPath(path: string, operation: string) {
  if (!path || path.includes('\0')) throw new SFTPError('failure', `${operation} path is invalid`)
}

function assertTransferLimit(size: number) {
  if (size > SFTP_MAX_TRANSFER_BYTES) {
    throw new SFTPError('failure', 'SFTP transfer exceeds 1 GiB limit')
  }
}

function joinPath(directory: string, name: string) {
  return directory === '/' ? `/${name}` : `${directory}/${name}`
}

async function readBlob(blob: Blob): Promise<ArrayBuffer> {
  if (typeof blob.arrayBuffer === 'function') return blob.arrayBuffer()
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.onload = () => resolve(reader.result as ArrayBuffer)
    reader.onerror = () => reject(reader.error ?? new Error('File read failed'))
    reader.readAsArrayBuffer(blob)
  })
}

export function isSFTPError(error: unknown): error is SFTPError {
  return error instanceof SFTPError
}

export async function listDirectory(client: SFTPClient, path: string): Promise<SFTPEntry[]> {
  assertPath(path, 'Directory')
  const entries = await client.readDirectory(path)
  return entries.map((entry) => ({ ...entry, path: joinPath(path, entry.name) }))
}

export async function uploadFile(
  client: SFTPClient,
  file: File,
  remotePath: string,
  onProgress?: (event: SFTPProgress) => void,
): Promise<void> {
  assertPath(remotePath, 'Upload')
  assertTransferLimit(file.size)
  onProgress?.({ path: remotePath, transferred: 0, total: file.size })

  const handle = await client.openFile(remotePath, 'write')
  try {
    let transferred = 0
    while (transferred < file.size) {
      const chunk = new Uint8Array(
        await readBlob(file.slice(transferred, Math.min(transferred + SFTP_CHUNK_SIZE, file.size))),
      )
      const written = await handle.write(chunk)
      if (written !== chunk.length) throw new SFTPError('failure', 'SFTP upload ended early')
      transferred += written
      onProgress?.({ path: remotePath, transferred, total: file.size })
    }
  } finally {
    await handle.close()
  }
}

export async function downloadFile(
  client: SFTPClient,
  remotePath: string,
  size: number,
  onProgress?: (event: SFTPProgress) => void,
): Promise<Blob> {
  assertPath(remotePath, 'Download')
  assertTransferLimit(size)
  onProgress?.({ path: remotePath, transferred: 0, total: size })

  const handle = await client.openFile(remotePath, 'read')
  try {
    const chunks: BlobPart[] = []
    let transferred = 0
    while (transferred < size) {
      const buffer = new Uint8Array(Math.min(SFTP_CHUNK_SIZE, size - transferred))
      const count = await handle.read(buffer)
      if (count === 0) throw new SFTPError('failure', 'SFTP download ended early')
      if (count < 0 || count > buffer.length) throw new SFTPError('failure', 'SFTP read failed')
      chunks.push(buffer.slice(0, count))
      transferred += count
      onProgress?.({ path: remotePath, transferred, total: size })
    }
    return new Blob(chunks, { type: 'application/octet-stream' })
  } finally {
    await handle.close()
  }
}

export async function deleteEntry(client: SFTPClient, entry: SFTPEntry): Promise<void> {
  if (entry.type === 'directory') await client.rmdir(entry.path)
  else await client.unlink(entry.path)
}

export async function renameEntry(client: SFTPClient, from: SFTPEntry, toPath: string): Promise<void> {
  assertPath(toPath, 'Rename destination')
  await client.rename(from.path, toPath)
}
