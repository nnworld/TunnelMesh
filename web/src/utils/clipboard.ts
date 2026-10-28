// Clipboard access can be refused (insecure origin, permissions policy), so
// callers get a boolean instead of an unhandled rejection.
export async function copyText(text: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(text)
    return true
  } catch {
    return false
  }
}
