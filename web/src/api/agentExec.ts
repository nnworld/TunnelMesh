import { api } from './client'

// The run starts with a one-time ticket: a browser WebSocket cannot carry the bearer
// header, so the ticket stands in for it for the lifetime of one run.
export type AgentExecTicket = { sessionId: string; ticket: string; websocketPath: string; expiresAt: string }

export function createAgentExecSession(agentId: string, idempotencyKey: string) {
  return api<AgentExecTicket>(`/agents/${encodeURIComponent(agentId)}/exec-sessions`, {
    method: 'POST',
    headers: { 'Idempotency-Key': idempotencyKey },
  })
}

export type AgentExecFrame =
  | { type: 'stdout' | 'stderr'; data: string }
  | { type: 'exit'; exitCode: number; signaled: boolean; timedOut: boolean; truncated: boolean; durationMs: number }
  | { type: 'error'; message: string }

// openAgentExecSocket connects to the broker and sends the script as the first message.
export function openAgentExecSocket(ticket: AgentExecTicket, script: string, timeoutSec: number, onFrame: (frame: AgentExecFrame) => void, onClose: () => void): WebSocket {
  const scheme = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
  const url = `${scheme}//${window.location.host}${ticket.websocketPath}?ticket=${encodeURIComponent(ticket.ticket)}`
  const socket = new WebSocket(url)
  socket.onopen = () => socket.send(JSON.stringify({ type: 'run', script, timeoutSec }))
  socket.onmessage = (event) => {
    if (typeof event.data !== 'string') return
    onFrame(JSON.parse(event.data) as AgentExecFrame)
  }
  socket.onclose = () => onClose()
  socket.onerror = () => onClose()
  return socket
}
