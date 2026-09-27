import { APIError } from '../api/client'

export type Translate = (key: string) => string

// The ten stable management-API codes from internal/vpn/errors.go. The server
// puts the code in data.error, which APIError exposes as `domain`, so matching
// on anything else would degrade every failure into one generic message.
export const vpnErrorMessages: Record<string, string> = {
  vpn_peer_invalid: 'vpn.errors.peerInvalid',
  vpn_ip_pool_invalid: 'vpn.errors.ipPoolInvalid',
  vpn_peer_not_found: 'vpn.errors.peerNotFound',
  vpn_peer_conflict: 'vpn.errors.peerConflict',
  vpn_agent_capability_missing: 'vpn.errors.agentCapabilityMissing',
  vpn_ip_pool_exhausted: 'vpn.errors.ipPoolExhausted',
  vpn_node_disabled: 'vpn.errors.nodeDisabled',
  credential_secret_unavailable: 'vpn.errors.secretUnavailable',
  vpn_capacity_exhausted: 'vpn.errors.capacityExhausted',
  vpn_not_implemented: 'vpn.errors.notImplemented',
}

// Codes that mean "this node or this release cannot do that yet" rather than
// "you did something wrong". They are rendered as information and never as an
// error toast, because there is nothing for the operator to correct.
const unavailableStates = new Set(['vpn_not_implemented', 'vpn_node_disabled', 'vpn_agent_capability_missing'])

export function vpnErrorMessage(error: unknown, t: Translate, fallbackKey = 'vpn.operationFailed'): string {
  if (!(error instanceof APIError)) return t(fallbackKey)
  const identifier = error.domain || error.message
  const key = vpnErrorMessages[identifier]
  if (key) return t(key)
  return identifier || t(fallbackKey)
}

export function isVpnUnavailableState(error: unknown): boolean {
  return error instanceof APIError && Boolean(error.domain) && unavailableStates.has(error.domain as string)
}
