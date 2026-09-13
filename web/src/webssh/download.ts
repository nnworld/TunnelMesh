// Shared browser-download plumbing. Both the SFTP file manager and the ZMODEM
// receiver produce bytes that the user should save, and neither view should own
// object-URL lifecycle details.
export function downloadBlob(name: string, blob: Blob): void {
  const url = URL.createObjectURL(blob)
  const anchor = document.createElement('a')
  anchor.href = url
  anchor.download = name
  anchor.click()
  // Revoking immediately is safe: the click already queued the navigation.
  URL.revokeObjectURL(url)
}

export function downloadBytes(name: string, bytes: Uint8Array): void {
  downloadBlob(name, new Blob([bytes as unknown as BlobPart]))
}
