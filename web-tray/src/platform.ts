// The words the interface uses for the shell it is running inside.
//
// This window is served by two native shells: a macOS menu-bar item and a Windows
// notification-area icon. Both are the same Vue application, so a sentence that says
// "menu bar" or "macOS" is wrong for half of the users, and a value that came from the
// operating system is the only honest source for it.
//
// Unknown platforms fall back to `other`, whose wording is deliberately neutral: a tray
// built for a platform nobody has tested must still tell the operator something true.
export type PlatformKey = 'macos' | 'windows' | 'other'

const names: Record<string, PlatformKey> = {
  macos: 'macos',
  darwin: 'macos',
  windows: 'windows',
  win32: 'windows',
}

export function platformKey(platform?: string): PlatformKey {
  if (!platform) return 'other'
  return names[platform.toLowerCase()] ?? 'other'
}

// platformMessage builds an i18n key for the platform-specific variant of a message, so a
// call site reads `t(platformMessage('general.theme.hint', platform))` and every language
// file keeps one group of three strings rather than a conditional of its own.
export function platformMessage(key: string, platform?: string): string {
  return `${key}.${platformKey(platform)}`
}
