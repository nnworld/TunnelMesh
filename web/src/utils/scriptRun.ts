// The script-run console only applies to administrators and to agents that are online
// and currently serve agent_exec.v1. Both facts come from the server: the role from the
// session, and execCapable from the live handshake.
export type ScriptRunAgent = { status?: string; enabled?: boolean; execCapable?: boolean }

export function canRunScript(agent: ScriptRunAgent, isAdmin: boolean): boolean {
  return isAdmin && agent.status === 'online' && agent.enabled !== false && agent.execCapable === true
}

// Output arrives as base64 chunks. A character split across two chunks must not turn
// into a replacement glyph, so each stream keeps its own incremental decoder.
export function createOutputDecoder(): (base64: string) => string {
  const decoder = new TextDecoder('utf-8')
  return (base64: string) => {
    const binary = atob(base64)
    const bytes = Uint8Array.from(binary, (char) => char.charCodeAt(0))
    return decoder.decode(bytes, { stream: true })
  }
}
