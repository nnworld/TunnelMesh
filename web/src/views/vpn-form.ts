import type { VpnPeerInput, VpnPeerPatch } from '../api/vpn'

// The form model of the create/edit dialog. The two policy fields are text
// because that is how an operator pastes them, and both are parsed here rather
// than in the component so the rules can be tested without a DOM.
export type VpnPeerForm = {
  name: string
  description: string
  agentId: string
  allowedIpsText: string
  allowedPortsText: string
  allowPrivateTargets: boolean
  icmpEnabled: boolean
  maxConcurrentFlows: number
  packetRateLimit: number
  expiresAt: string
}

// What the server already stores for one peer. A patch is computed against it,
// so an untouched collection is never sent: the server reads an explicit empty
// list as a policy change, and sending one on every edit would silently narrow
// a peer to "no reachable destination".
export type VpnPeerOriginal = {
  name: string
  description: string
  agentId: string
  allowedIps: string[]
  allowedPorts: number[]
  allowPrivateTargets: boolean
  icmpEnabled: boolean
  maxConcurrentFlows: number
  packetRateLimit: number
  expiresAt: string | null
}

// MaxPeerNameLength and MaxPeerDescriptionLength in internal/vpn/peerspec.go,
// counted in runes and rejecting control characters. A longer value is a 400
// server-side, so the form refuses it first and names the field.
export const maxPeerNameLength = 255
export const maxPeerDescriptionLength = 255

// The word the reveal prompt asks for, mirroring the token reveal. The server
// only requires the header to be non-empty; asking for a word is what makes the
// request deliberate rather than a stray click.
export const revealConfirmationWord = 'REVEAL'

const ipv4CidrPattern = /^(\d{1,3}\.){3}\d{1,3}\/(\d{1,2})$/
const ipv4Pattern = /^(\d{1,3}\.){3}\d{1,3}$/

// parseVpnCidrList returns null for a malformed entry and an empty array for an
// empty field: "no destination" is a valid choice, a typo is not.
//
// A bare address is normalized to a /32 because the server parses allowed_ips
// with net.ParseCIDR, which rejects an unprefixed entry; accepting one here and
// letting the server refuse it would report the failure on the wrong field.
// Only IPv4 is accepted, because the egress policy is IPv4-only: an IPv6 entry
// can never match and would look like a rule while widening nothing.
export function parseVpnCidrList(raw: string): string[] | null {
  const out: string[] = []
  for (const item of raw.split(',')) {
    const value = item.trim()
    if (!value) continue
    if (ipv4CidrPattern.test(value)) {
      const prefix = Number(value.slice(value.indexOf('/') + 1))
      if (prefix > 32 || !isValidIPv4(value.slice(0, value.indexOf('/')))) return null
      out.push(value)
      continue
    }
    if (ipv4Pattern.test(value) && isValidIPv4(value)) {
      out.push(`${value}/32`)
      continue
    }
    return null
  }
  return out
}

// parseVpnPortList keeps the flat integer form the server stores. Range syntax
// such as "80-90" is rejected on purpose: the canonical encoding is a flat
// ascending list, and accepting a shorthand here would put text into storage
// that the server cannot reproduce byte for byte.
export function parseVpnPortList(raw: string): number[] | null {
  const out: number[] = []
  for (const item of raw.split(',')) {
    const value = item.trim()
    if (!value) continue
    if (!/^\d+$/.test(value)) return null
    const port = Number(value)
    if (!Number.isInteger(port) || port < 1 || port > 65535) return null
    out.push(port)
  }
  return out
}

export function isRevealConfirmation(value: string): boolean {
  return value === revealConfirmationWord
}

// vpnPeerInputFromForm builds the create body, or null when any field is
// invalid. Keys the operator left alone are absent rather than null, because
// the server rejects unknown fields and reads an explicit null as "clear this".
export function vpnPeerInputFromForm(form: VpnPeerForm): VpnPeerInput | null {
  const name = form.name.trim()
  if (!name || !isValidLabel(name, maxPeerNameLength)) return null
  if (!isValidLabel(form.description, maxPeerDescriptionLength)) return null
  if (!form.agentId.trim()) return null
  const allowedIps = parseVpnCidrList(form.allowedIpsText)
  if (allowedIps === null) return null
  const allowedPorts = parseVpnPortList(form.allowedPortsText)
  if (allowedPorts === null) return null
  if (!isNonNegativeInteger(form.maxConcurrentFlows) || !isNonNegativeInteger(form.packetRateLimit)) return null
  const expiresAt = normalizeVpnExpiry(form.expiresAt)
  if (form.expiresAt.trim() && expiresAt === null) return null

  const input: VpnPeerInput = {
    name,
    agentId: form.agentId.trim(),
    allowedIps,
    allowedPorts,
    allowPrivateTargets: form.allowPrivateTargets,
    icmpEnabled: form.icmpEnabled,
    maxConcurrentFlows: form.maxConcurrentFlows,
    packetRateLimit: form.packetRateLimit,
  }
  if (form.description.trim()) input.description = form.description.trim()
  if (expiresAt !== null) input.expiresAt = expiresAt
  return input
}

// vpnPatchFromForm sends only what changed, or null when a field is invalid.
//
// Collections are compared as sets: the server canonicalizes allowed_ips by
// sorting and deduplicating, so a reordered list is not a policy change and
// must not be reported as one in the audit log.
export function vpnPatchFromForm(form: VpnPeerForm, original: VpnPeerOriginal): VpnPeerPatch | null {
  const allowedIps = parseVpnCidrList(form.allowedIpsText)
  if (allowedIps === null) return null
  const allowedPorts = parseVpnPortList(form.allowedPortsText)
  if (allowedPorts === null) return null
  if (!isNonNegativeInteger(form.maxConcurrentFlows) || !isNonNegativeInteger(form.packetRateLimit)) return null

  const name = form.name.trim()
  if (!name || !isValidLabel(name, maxPeerNameLength)) return null
  if (!isValidLabel(form.description, maxPeerDescriptionLength)) return null

  const patch: VpnPeerPatch = {}
  if (name !== original.name) patch.name = name
  const description = form.description.trim()
  if (description !== (original.description ?? '')) patch.description = description
  if (form.agentId.trim() !== original.agentId) patch.agentId = form.agentId.trim()
  if (sameSet(allowedIps, original.allowedIps ?? [])) {
    // unchanged
  } else {
    patch.allowedIps = allowedIps
  }
  if (!sameNumbers(allowedPorts, original.allowedPorts ?? [])) patch.allowedPorts = allowedPorts
  if (form.allowPrivateTargets !== original.allowPrivateTargets) patch.allowPrivateTargets = form.allowPrivateTargets
  if (form.icmpEnabled !== original.icmpEnabled) patch.icmpEnabled = form.icmpEnabled
  if (form.maxConcurrentFlows !== original.maxConcurrentFlows) patch.maxConcurrentFlows = form.maxConcurrentFlows
  if (form.packetRateLimit !== original.packetRateLimit) patch.packetRateLimit = form.packetRateLimit

  if (form.expiresAt.trim()) {
    const expiresAt = normalizeVpnExpiry(form.expiresAt)
    if (expiresAt === null) return null
    // Compared as an instant, not as text: the server may have stored
    // "...:23Z" where the picker produced "...:23.000Z", and a string
    // comparison would send a patch that changes nothing.
    if (!sameInstant(expiresAt, original.expiresAt)) patch.expiresAt = expiresAt
  } else if (original.expiresAt) {
    patch.expiresAt = null
  }
  return patch
}

function isValidIPv4(value: string) {
  return value.split('.').every(part => /^\d{1,3}$/.test(part) && Number(part) <= 255)
}

function isValidLabel(value: string, max: number) {
  if ([...value].length > max) return false
  for (const character of value) if (character.charCodeAt(0) < 32 || character.charCodeAt(0) === 127) return false
  return true
}

function isNonNegativeInteger(value: number) {
  return Number.isInteger(value) && value >= 0
}

// normalizeVpnExpiry renders the picker's value as an RFC3339 instant the server
// can unmarshal into time.Time, and refuses a past expiry: the server applies
// the same future-only rule to an issue and to a patch that writes expires_at.
export function normalizeVpnExpiry(raw: string): string | null {
  const value = raw.trim()
  if (!value) return null
  const parsed = new Date(value)
  if (Number.isNaN(parsed.getTime())) return null
  if (parsed.getTime() <= Date.now()) return null
  return parsed.toISOString()
}

function sameInstant(next: string, previous: string | null) {
  if (!previous) return false
  const left = new Date(next).getTime()
  const right = new Date(previous).getTime()
  return !Number.isNaN(left) && left === right
}

function sameSet(next: string[], previous: string[]) {
  const left = [...next].sort()
  const right = [...previous].sort()
  return left.length === right.length && left.every((value, index) => value === right[index])
}

function sameNumbers(next: number[], previous: number[]) {
  const left = [...next].sort((a, b) => a - b)
  const right = [...previous].sort((a, b) => a - b)
  return left.length === right.length && left.every((value, index) => value === right[index])
}
