import { h } from 'vue'
import { useI18n } from 'vue-i18n'
import { ElMessageBox } from 'element-plus'
// This module imports MessageBox programmatically, so unplugin-vue-components
// never injects its style entry. Without it the confirmation dialog renders
// unstyled and the fingerprint is unreadable.
import 'element-plus/es/components/message-box/style/css'

export type HostKeyVerifier = (fingerprint: string) => Promise<boolean>

/**
 * Accepting an unknown host key is a security decision, and there are now two
 * entry points that can perform the SSH handshake on their own: the terminal
 * and the SFTP-only view opened straight from the server list. Both must show
 * exactly the same prompt, so the dialog lives here instead of being copied
 * per view where the two could silently drift.
 *
 * Must be called from a component `setup` scope because it binds `useI18n`.
 */
export function useHostKeyVerifier(): HostKeyVerifier {
  const { t } = useI18n()

  return async function verifyHostKey(fingerprint: string): Promise<boolean> {
    try {
      // The fingerprint is rendered in its own wrapping code block: a long
      // SHA256 value inside an interpolated sentence overflowed the dialog and
      // pushed the action buttons out of view.
      const message = h('div', { class: 'webssh-host-key-confirm' }, [
        h('p', t('webssh.hostKeyPrompt')),
        h('code', {
          style: {
            display: 'block',
            marginTop: '8px',
            padding: '8px',
            wordBreak: 'break-all',
            whiteSpace: 'normal',
            fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace',
          },
        }, fingerprint),
      ])
      await ElMessageBox.confirm(
        message,
        t('webssh.hostKeyTitle'),
        {
          type: 'warning',
          confirmButtonText: t('webssh.hostKeyTrust'),
          cancelButtonText: t('webssh.hostKeyReject'),
          autofocus: false,
        },
      )
      return true
    } catch {
      // Rejecting the key and dismissing the dialog are the same decision: do
      // not complete the handshake against an unverified host.
      return false
    }
  }
}
