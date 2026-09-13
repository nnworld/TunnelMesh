import { describe, expect, it } from 'vitest'
import { APIError } from '../api/client'
import { sshErrorMessage, sshErrorMessages } from '../views/remote-server-errors'
import zhCN from '../i18n/messages/zh-CN'
import enUS from '../i18n/messages/en-US'

const translate = (key: string) => `t:${key}`

function resolve(messages: Record<string, unknown>, path: string) {
  return path.split('.').reduce<unknown>((acc, part) => (
    acc && typeof acc === 'object' ? (acc as Record<string, unknown>)[part] : undefined
  ), messages)
}

describe('sshErrorMessage', () => {
  // Server 的 writeAPIError 把稳定标识放在 data.error（映射为 APIError.domain），
  // envelope 的 msg 只是 HTTP 状态文本，所以必须按 domain 匹配。
  it('translates the domain identifier carried in data.error', () => {
    const error = new APIError('Internal Server Error', 500, 500, 'remote server agent is offline')
    expect(sshErrorMessage(error, translate)).toBe('t:remoteServers.errors.agentOffline')
  })

  it('translates the active session quota identifier', () => {
    const error = new APIError('Internal Server Error', 500, 500, 'webssh active session limit reached')
    expect(sshErrorMessage(error, translate)).toBe('t:remoteServers.errors.limitReached')
  })

  it('maps a forbidden response to the permission message', () => {
    const error = new APIError('Forbidden', 403, 403, 'forbidden')
    expect(sshErrorMessage(error, translate)).toBe('t:remoteServers.errors.forbidden')
  })

  it('keeps an unknown server identifier readable instead of hiding it', () => {
    const error = new APIError('Internal Server Error', 500, 500, 'webssh local node identity is required')
    expect(sshErrorMessage(error, translate)).toBe('webssh local node identity is required')
  })

  it('falls back to the generic message for non-API failures', () => {
    expect(sshErrorMessage(new TypeError('network down'), translate)).toBe('t:remoteServers.sshFailed')
    expect(sshErrorMessage('cancel', translate)).toBe('t:remoteServers.sshFailed')
  })

  it('defines every mapped key in both locales', () => {
    const keys = new Set(Object.values(sshErrorMessages))
    expect(keys.size).toBeGreaterThan(3)
    for (const key of keys) {
      expect(resolve(zhCN as unknown as Record<string, unknown>, key), key).toBeTruthy()
      expect(resolve(enUS as unknown as Record<string, unknown>, key), key).toBeTruthy()
    }
  })
})
