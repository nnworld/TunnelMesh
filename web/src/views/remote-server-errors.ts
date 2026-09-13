import { APIError } from '../api/client'

export type Translate = (key: string) => string

// Server 的 writeAPIError 把稳定的英文错误标识放在响应体的 data.error 中，
// envelope 的 msg 只是 HTTP 状态文本（例如 "Internal Server Error"）。
// APIError 把 data.error 暴露为 domain，因此必须按 domain 匹配，
// 否则所有创建失败都会退化成没有信息量的通用提示。
export const sshErrorMessages: Record<string, string> = {
  'remote server is unavailable': 'remoteServers.errors.serverUnavailable',
  'remote server agent is unavailable': 'remoteServers.errors.agentUnavailable',
  'remote server credential is unavailable': 'remoteServers.errors.credentialUnavailable',
  'remote server agent is offline': 'remoteServers.errors.agentOffline',
  'webssh active session limit reached': 'remoteServers.errors.limitReached',
  'webssh username is required': 'remoteServers.usernameRequired',
  'webssh session is unavailable': 'remoteServers.errors.serverUnavailable',
  'resource is forbidden': 'remoteServers.errors.forbidden',
}

// 把创建 WebSSH 会话的失败原因翻译成管理员可操作的文案；
// 未收录的标识保留原文，便于对照 Server 日志排障。
export function sshErrorMessage(error: unknown, t: Translate): string {
  if (!(error instanceof APIError)) return t('remoteServers.sshFailed')
  const identifier = error.domain || error.message
  const key = sshErrorMessages[identifier]
  if (key) return t(key)
  if (error.status === 403) return t('remoteServers.errors.forbidden')
  return identifier || t('remoteServers.sshFailed')
}
