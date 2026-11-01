import { describe, expect, it } from 'vitest'
import { actionHue } from '../utils/actionHue'

describe('actionHue', () => {
  it('colours each resource family differently so a feed reads at a glance', () => {
    expect(actionHue('agent.created')).toBe('brand')
    expect(actionHue('token.created')).toBe('violet')
    expect(actionHue('remote_server.created')).toBe('lime')
    expect(actionHue('credential.secret-set')).toBe('warn')
    expect(actionHue('policy.created')).toBe('sky')
  })

  it('marks refusals and removals rose whatever the family', () => {
    expect(actionHue('agent.connection.rejected')).toBe('rose')
    expect(actionHue('proxy_auth_failed')).toBe('rose')
    expect(actionHue('token.revoked')).toBe('rose')
    expect(actionHue('agent.deleted')).toBe('rose')
  })

  it('falls back to neutral for unknown actions', () => {
    expect(actionHue('something.else')).toBe('neutral')
    expect(actionHue('')).toBe('neutral')
  })
})
