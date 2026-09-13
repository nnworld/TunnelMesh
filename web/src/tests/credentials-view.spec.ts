import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'

describe('credentials management view', () => {
  it('renders list, filter, lifecycle, detail, and state surfaces', () => {
    const source = readFileSync('src/views/Credentials.vue', 'utf8')
    for (const marker of [
      'listCredentials', 'createCredential', 'updateCredential', 'deleteCredential', 'restoreCredential', 'getCredential',
      'DataState', 'el-table', 'filterStatus', 'loadError', 'loading', 'items.length', 'detailVisible',
    ]) expect(source, marker).toContain(marker)
  })

  it('supports public-key paste and one-time server-side private-key extraction', () => {
    const source = readFileSync('src/views/Credentials.vue', 'utf8')
    expect(source).toContain("form.publicKey")
    expect(source).toContain('extractSSHPublicKey')
    expect(source).toContain('privateKey')
    expect(source).toContain('passphrase')
    expect(source).toContain("t('credentials.privateKeyWarning')")
    expect(source).toContain('clearSensitiveKeyInput')
  })

  it('clears private-key material after success and failure', () => {
    const source = readFileSync('src/views/Credentials.vue', 'utf8')
    expect(source.match(/clearSensitiveKeyInput\(\)/g)?.length ?? 0).toBeGreaterThanOrEqual(3)
    expect(source).not.toContain('localStorage')
    expect(source).not.toContain('sessionStorage')
  })

  it('supports a password credential type', () => {
    const source = readFileSync('src/views/Credentials.vue', 'utf8')
    for (const marker of [
      'form.type', "value=\"password\"", "value=\"ssh_public_key\"",
      "t('credentials.types.password')", "t('credentials.types.sshPublicKey')",
      'form.secretPassword', "type=\"password\"",
    ]) expect(source, marker).toContain(marker)
    // A password credential has no public key, so the key fields must be hidden.
    expect(source).toContain("form.type === 'ssh_public_key'")
  })

  it('offers optional encrypted private-key storage for auto-authentication', () => {
    const source = readFileSync('src/views/Credentials.vue', 'utf8')
    for (const marker of [
      'form.storePrivateKey', "t('credentials.storePrivateKey')", "t('credentials.storePrivateKeyHelp')",
      'secret:', 'privateKey: form.privateKey', 'passphrase: form.passphrase',
    ]) expect(source, marker).toContain(marker)
  })

  it('shows whether a credential can auto-authenticate without exposing the secret', () => {
    const source = readFileSync('src/views/Credentials.vue', 'utf8')
    expect(source).toContain('row.hasSecret')
    expect(source).toContain("t('credentials.secretStored')")
    expect(source).toContain("t('credentials.secretNotStored')")
    expect(source).toContain("t('credentials.secretKept')")
    expect(source).not.toContain('secretCiphertext')
  })
})
