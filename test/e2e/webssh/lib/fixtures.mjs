// Deterministic payloads for the WebSSH end-to-end run.
//
// They are generated on demand instead of committed: a multi-megabyte binary in
// git would bloat every clone forever, and the assertions only need
// byte-for-byte equality between what was sent and what arrived.
//
// The default sizes are deliberate. Both exceed the 512 KiB server relay receive
// window, and the download exceeds the 4 MiB browser guard that used to kill the
// session outright, so a regression in either flow-control layer fails the run.
import crypto from 'node:crypto'
import fs from 'node:fs'
import path from 'node:path'

export const DEFAULT_DOWNLOAD_BYTES = 4 * 1024 * 1024
export const DEFAULT_UPLOAD_BYTES = Math.round(1.5 * 1024 * 1024)

const BLOCK = 32

// deterministicBytes derives content from HMAC-SHA256 in counter mode. Node has
// no seeded randomBytes, and a hash chain is reproducible across platforms and
// Node versions, which a PRNG is not.
function deterministicBytes(seedText, length) {
  const key = crypto.createHash('sha256').update(seedText).digest()
  const out = Buffer.alloc(length)
  const counter = Buffer.alloc(4)
  for (let written = 0; written < length; ) {
    counter.writeUInt32BE(Math.floor(written / BLOCK), 0)
    const block = crypto.createHmac('sha256', key).update(counter).digest()
    block.copy(out, written, 0, Math.min(BLOCK, length - written))
    written += BLOCK
  }
  return out
}

// hostsLikeText builds an ASCII file that compresses badly enough to be a
// realistic upload while still being diffable by a human debugging a failure.
function hostsLikeText(seedText, length) {
  const chunks = []
  let written = 0
  let index = 0
  const key = crypto.createHash('sha256').update(seedText).digest()
  while (written < length) {
    const counter = Buffer.alloc(4)
    counter.writeUInt32BE(index++, 0)
    const digest = crypto.createHmac('sha256', key).update(counter).digest()
    const octet = (offset) => digest[offset % digest.length]
    const line = `10.${octet(0)}.${octet(1)}.${octet(2)}\thost-${digest.subarray(0, 4).toString('hex')}.e2e.local\n`
    chunks.push(line)
    written += line.length
  }
  return Buffer.from(chunks.join(''), 'utf8').subarray(0, length)
}

/**
 * writeFixtures materialises the download payload (lives on the fake SSH host)
 * and the upload payload (lives on the browser side) and returns their paths.
 * Existing files with the right size are kept so repeated runs stay fast.
 */
export function writeFixtures({ remoteFsDir, uploadDir, downloadBytes, uploadBytes, sftpUploadBytes }) {
  fs.mkdirSync(remoteFsDir, { recursive: true })
  fs.mkdirSync(uploadDir, { recursive: true })

  const downloadPath = path.join(remoteFsDir, 'payload.bin')
  const uploadPath = path.join(uploadDir, 'upload.bin')
  // Kept distinct from the ZMODEM upload so the SFTP check can assert the file
  // *appears* in the listing, not merely that some upload.bin is present.
  const sftpUploadPath = path.join(uploadDir, 'sftp-upload.bin')
  const targets = [
    [downloadPath, downloadBytes, () => deterministicBytes('tunnelmesh-e2e-download', downloadBytes)],
    [uploadPath, uploadBytes, () => hostsLikeText('tunnelmesh-e2e-upload', uploadBytes)],
    [sftpUploadPath, sftpUploadBytes, () => deterministicBytes('tunnelmesh-e2e-sftp-upload', sftpUploadBytes)],
  ]
  for (const [file, size, build] of targets) {
    if (fs.existsSync(file) && fs.statSync(file).size === size) continue
    fs.writeFileSync(file, build())
  }
  return { downloadPath, uploadPath, sftpUploadPath }
}

export function sha256(file) {
  return crypto.createHash('sha256').update(fs.readFileSync(file)).digest('hex')
}
