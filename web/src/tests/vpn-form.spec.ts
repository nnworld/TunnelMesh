import { describe, expect, it } from 'vitest'
import {
  isRevealConfirmation, normalizeVpnExpiry, parseVpnCidrList, parseVpnPortList, vpnPatchFromForm, vpnPeerInputFromForm,
} from '../views/vpn-form'

const base = {
  name: 'laptop', description: '', agentId: 'a-1', allowedIpsText: '10.0.0.0/8, 192.168.0.0/16',
  allowedPortsText: '443, 8443', allowPrivateTargets: false, icmpEnabled: false,
  maxConcurrentFlows: 0, packetRateLimit: 0, expiresAt: '',
}

describe('vpn form parsing', () => {
  it('accepts a comma separated cidr list and rejects a single malformed entry', () => {
    // A bare address is normalized to a /32: the server parses allowed_ips
    // with net.ParseCIDR, which rejects an unprefixed entry.
    expect(parseVpnCidrList('10.0.0.0/8, 192.168.1.5')).toEqual(['10.0.0.0/8', '192.168.1.5/32'])
    expect(parseVpnCidrList('')).toEqual([])
    expect(parseVpnCidrList('10.0.0.0/33')).toBeNull()
    expect(parseVpnCidrList('office/24')).toBeNull()
    // An embedded separator would become two rules server-side, so it is a
    // malformed entry here rather than a value to be split again.
    expect(parseVpnCidrList('10.0.0.0/8 10.1.0.0/16')).toBeNull()
  })

  it('accepts integer ports inside the range and rejects everything else', () => {
    expect(parseVpnPortList('443, 8443')).toEqual([443, 8443])
    expect(parseVpnPortList('')).toEqual([])
    expect(parseVpnPortList('0')).toBeNull()
    expect(parseVpnPortList('65536')).toBeNull()
    expect(parseVpnPortList('80.5')).toBeNull()
    expect(parseVpnPortList('http')).toBeNull()
  })

  it('builds the create body only when every field validates', () => {
    const input = vpnPeerInputFromForm(base)
    expect(input).toEqual({
      name: 'laptop', agentId: 'a-1', allowedIps: ['10.0.0.0/8', '192.168.0.0/16'],
      allowedPorts: [443, 8443], allowPrivateTargets: false, icmpEnabled: false,
      maxConcurrentFlows: 0, packetRateLimit: 0,
    })
    expect(vpnPeerInputFromForm({ ...base, allowedIpsText: '192.168.1.5' })?.allowedIps).toEqual(['192.168.1.5/32'])
    // No expiry means the key is absent, not null: the server reads an explicit
    // null as "clear this" and an absent field as "leave it alone".
    expect(input && 'expiresAt' in input).toBe(false)

    expect(vpnPeerInputFromForm({ ...base, name: '' })).toBeNull()
    expect(vpnPeerInputFromForm({ ...base, name: 'x'.repeat(256) })).toBeNull()
    expect(vpnPeerInputFromForm({ ...base, name: 'x'.repeat(255) })?.name).toHaveLength(255)
    expect(vpnPeerInputFromForm({ ...base, name: 'bad\nname' })).toBeNull()
    expect(vpnPeerInputFromForm({ ...base, agentId: '' })).toBeNull()
    expect(vpnPeerInputFromForm({ ...base, allowedIpsText: 'nope' })).toBeNull()
    expect(vpnPeerInputFromForm({ ...base, allowedPortsText: '99999' })).toBeNull()
    expect(vpnPeerInputFromForm({ ...base, description: 'x'.repeat(256) })).toBeNull()
  })

  it('sends an expiry only as a future RFC3339 instant', () => {
    const future = new Date(Date.now() + 3600_000).toISOString().slice(0, 19) + 'Z'
    const input = vpnPeerInputFromForm({ ...base, expiresAt: future })
    expect(input?.expiresAt).toBe(new Date(future).toISOString())

    const past = new Date(Date.now() - 3600_000).toISOString().slice(0, 19) + 'Z'
    expect(vpnPeerInputFromForm({ ...base, expiresAt: past })).toBeNull()
    expect(vpnPeerInputFromForm({ ...base, expiresAt: 'not-a-date' })).toBeNull()
  })

  it('patches only what changed, and never sends an untouched collection', () => {
    // The stored row has a description, so the form is seeded with it: a patch
    // computed from an emptied field would be a real change and would erase it.
    const seeded = { ...base, description: 'road warrior' }
    const original = {
      name: 'laptop', description: 'road warrior', agentId: 'a-1',
      allowedIps: ['10.0.0.0/8'], allowedPorts: [443], allowPrivateTargets: false,
      icmpEnabled: false, maxConcurrentFlows: 64, packetRateLimit: 0, expiresAt: null,
    }
    const renamed = vpnPatchFromForm({ ...seeded, name: 'desktop', allowedIpsText: '10.0.0.0/8', allowedPortsText: '443', maxConcurrentFlows: 64 }, original)
    expect(renamed).toEqual({ name: 'desktop' })

    const narrowed = vpnPatchFromForm({ ...seeded, allowedIpsText: '10.0.0.0/8', allowedPortsText: '443', allowPrivateTargets: true, maxConcurrentFlows: 64 }, original)
    expect(narrowed).toEqual({ allowPrivateTargets: true })

    // Clearing the port list is a change with a meaning of its own: no port
    // restriction. It must survive as an empty array rather than be dropped.
    const ports = vpnPatchFromForm({ ...seeded, allowedPortsText: '', maxConcurrentFlows: 64 }, { ...original, allowedPorts: [443], allowedIps: ['10.0.0.0/8', '192.168.0.0/16'] })
    expect(ports).toEqual({ allowedPorts: [] })

    expect(vpnPatchFromForm({ ...seeded, allowedIpsText: 'nope' }, original)).toBeNull()
  })

  it('normalizes an expiry to an RFC3339 instant and refuses the past', () => {
    expect(normalizeVpnExpiry('')).toBeNull()
    expect(normalizeVpnExpiry('2026-01-01T00:00:00Z')).toBeNull()
    expect(normalizeVpnExpiry('nonsense')).toBeNull()
    const future = new Date(Date.now() + 60_000)
    expect(normalizeVpnExpiry(future.toISOString())).toBe(future.toISOString())
  })

  it('accepts only the typed confirmation word', () => {
    expect(isRevealConfirmation('REVEAL')).toBe(true)
    expect(isRevealConfirmation(' reveal ')).toBe(false)
    expect(isRevealConfirmation('')).toBe(false)
  })
})
