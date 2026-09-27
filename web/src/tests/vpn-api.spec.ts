import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { callsTo, stubApi } from './api-stub'
import {
  createVpnPeer, getVpnPeer, listVpnNodes, listVpnPeerFlows, listVpnPeers, patchVpnPeer,
  revealVpnPeerConfig, revokeVpnPeer, rotateVpnPeer,
} from '../api/vpn'

// The projection mirrors the server's VPNPeer schema, which is
// additionalProperties:false and carries no sealed private-key column. A field
// added here that the server does not send would silently render as undefined.
const peer = {
  id: 'p-1', ownerUserId: 'u-1', name: 'laptop', description: 'road warrior',
  publicKey: 'AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=', vpnIp: '10.64.0.2',
  nodeId: 'n-1', agentId: 'a-1', allowedIps: ['10.0.0.0/8'], allowedPorts: [443],
  allowPrivateTargets: false, icmpEnabled: false, maxConcurrentFlows: 64,
  packetRateLimit: 0, expiresAt: null, status: 'active',
  createdAt: '2026-09-22T00:00:00Z', updatedAt: '2026-09-22T00:00:00Z',
}

describe('vpn api client', () => {
  it('sends only the filters that are set', async () => {
    const calls = stubApi([{ path: '/vpn-peers', data: { items: [peer], nextCursor: 'c2', hasMore: true } }])

    const page = await listVpnPeers({ status: 'active', keyword: 'laptop', cursor: 'c1', limit: 20 })

    expect(page.items).toHaveLength(1)
    expect(page.hasMore).toBe(true)
    expect(callsTo(calls, 'GET', '/vpn-peers')[0]?.path).toBe('/vpn-peers?status=active&keyword=laptop&cursor=c1&limit=20')
  })

  it('omits the query string when nothing is filtered, so an empty list is not a filtered list', async () => {
    const calls = stubApi([{ path: '/vpn-peers', data: { items: [] } }])

    await listVpnPeers()

    expect(callsTo(calls, 'GET', '/vpn-peers')[0]?.path).toBe('/vpn-peers')
  })

  it('issues a peer with an idempotency key and the create body verbatim', async () => {
    const calls = stubApi([{ method: 'POST', path: '/vpn-peers', status: 201, data: { ...peer, configRevealPath: '/api/v1/vpn-peers/p-1/config:reveal' } }])

    const created = await createVpnPeer({
      name: 'laptop', agentId: 'a-1', allowedIps: ['10.0.0.0/8'], allowedPorts: [443],
      allowPrivateTargets: false, icmpEnabled: false, maxConcurrentFlows: 64, packetRateLimit: 0,
    }, 'idem-1')

    expect(created.configRevealPath).toContain('config:reveal')
    const call = callsTo(calls, 'POST', '/vpn-peers')[0]
    expect(call?.headers.get('Idempotency-Key')).toBe('idem-1')
    expect(call?.body).toEqual({
      name: 'laptop', agentId: 'a-1', allowedIps: ['10.0.0.0/8'], allowedPorts: [443],
      allowPrivateTargets: false, icmpEnabled: false, maxConcurrentFlows: 64, packetRateLimit: 0,
    })
  })

  it('reads and patches one peer, sending only the fields the caller supplied', async () => {
    const calls = stubApi([
      { path: '/vpn-peers/p-1', data: peer },
      { method: 'PATCH', path: '/vpn-peers/p-1', data: { ...peer, name: 'renamed' } },
    ])

    expect((await getVpnPeer('p-1')).vpnIp).toBe('10.64.0.2')
    await patchVpnPeer('p-1', { name: 'renamed' }, 'idem-2')

    const patch = callsTo(calls, 'PATCH', '/vpn-peers/p-1')[0]
    expect(patch?.headers.get('Idempotency-Key')).toBe('idem-2')
    expect(patch?.body).toEqual({ name: 'renamed' })
  })

  it('revokes with DELETE and rotates with POST, both under an idempotency key', async () => {
    const calls = stubApi([
      { method: 'DELETE', path: '/vpn-peers/p-1', data: { ...peer, status: 'revoked' } },
      { method: 'POST', path: '/vpn-peers/p-1/rotate', data: { ...peer, publicKey: 'BBBB' } },
    ])

    await revokeVpnPeer('p-1', 'idem-3')
    await rotateVpnPeer('p-1', 'idem-4')

    expect(callsTo(calls, 'DELETE', '/vpn-peers/p-1')[0]?.headers.get('Idempotency-Key')).toBe('idem-3')
    expect(callsTo(calls, 'POST', '/vpn-peers/p-1/rotate')[0]?.headers.get('Idempotency-Key')).toBe('idem-4')
  })

  it('reveals only with the typed confirmation, an idempotency key and the risk acknowledgement', async () => {
    const calls = stubApi([{
      method: 'POST', path: '/vpn-peers/p-1/config:reveal',
      data: { id: 'p-1', name: 'laptop', vpnIp: '10.64.0.2', format: 'wg-quick', config: '[Interface]\nPrivateKey = secret\n' },
    }])

    const revealed = await revealVpnPeerConfig('p-1', 'REVEAL', 'idem-5')

    expect(revealed.format).toBe('wg-quick')
    const call = callsTo(calls, 'POST', '/vpn-peers/p-1/config:reveal')[0]
    expect(call?.headers.get('X-VPN-Config-Reveal-Confirm')).toBe('REVEAL')
    expect(call?.headers.get('Idempotency-Key')).toBe('idem-5')
    expect(call?.body).toEqual({ acknowledgeRisk: true })
  })

  it('reads the node status the gateway runtime reports', async () => {
    const calls = stubApi([
      { path: '/vpn-nodes', data: { items: [{ nodeId: 'n-1', enabled: true, listen: '0.0.0.0:51820', subnet: '10.64.0.0/24', allocated: 3, capacity: 253, peers: 3, icmpCapable: true }] } },
    ])

    const nodes = await listVpnNodes()

    expect(nodes.items[0]?.subnet).toBe('10.64.0.0/24')
    expect(nodes.items[0]?.icmpCapable).toBe(true)
    expect(callsTo(calls, 'GET', '/vpn-nodes')).toHaveLength(1)
  })

  it('reads the active flows of one peer field by field', async () => {
    const calls = stubApi([
      { path: '/vpn-peers/p-1/flows', data: { items: [{ id: 'f-1', protocol: 'tcp', target: '10.0.0.7', port: 443, startedAt: '2026-09-23T08:00:00Z', bytesSent: 2048, bytesReceived: 4096 }] } },
    ])

    const flows = await listVpnPeerFlows('p-1')

    expect(flows.items[0]).toMatchObject({ protocol: 'tcp', target: '10.0.0.7', port: 443, bytesSent: 2048, bytesReceived: 4096 })
    expect(callsTo(calls, 'GET', '/vpn-peers/p-1/flows')[0]?.path).toBe('/vpn-peers/p-1/flows')
  })

  it('maps a peer this node cannot observe onto its stable domain code', async () => {
    const calls = stubApi([
      { path: '/vpn-peers/p-1/flows', status: 501, data: { error: 'vpn_not_implemented' } },
    ])

    // Not an empty list: "the peer is idle" and "the peer is served elsewhere"
    // are different facts, and only the second tells an operator where to look.
    await expect(listVpnPeerFlows('p-1')).rejects.toMatchObject({ status: 501, domain: 'vpn_not_implemented' })
    expect(callsTo(calls, 'GET', '/vpn-peers/p-1/flows')).toHaveLength(1)
  })

  it('never declares a sealed private-key column on the peer projection', () => {
    const source = readFileSync('src/api/vpn.ts', 'utf8')
    for (const forbidden of ['Ciphertext', 'privateKeyNonce', 'privateKeyKeyId', 'privateKeyVersion']) {
      expect(source, forbidden).not.toContain(forbidden)
    }
    // The one response that carries key material is the reveal body, and it is
    // a rendered configuration rather than a column of the peer row.
    expect(source).toContain('config: string')
  })
})
