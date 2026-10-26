// Icon geometry on a 24 grid, drawn with a 1.5px square-capped stroke.
// Coordinates sit on half pixels so strokes stay crisp at 16/20/24px.
// Markup is trusted constants rendered via v-html; it must never take input.
export const iconPaths = {
  'overview': '<rect x="3.5" y="3.5" width="7" height="7"/><rect x="13.5" y="3.5" width="7" height="7"/><rect x="3.5" y="13.5" width="7" height="7"/><rect x="13.5" y="13.5" width="7" height="7"/>',
  'agents': '<rect x="3.5" y="4.5" width="17" height="6"/><rect x="3.5" y="13.5" width="17" height="6"/><path d="M7 7.5h.01M7 16.5h.01"/>',
  'clients': '<rect x="3.5" y="4.5" width="17" height="11"/><path d="M9 20h6M12 15.5V20"/>',
  'routes': '<circle cx="6" cy="6" r="2.5"/><circle cx="18" cy="18" r="2.5"/><path d="M8.5 6H15a3 3 0 0 1 0 6H9a3 3 0 0 0 0 6h6.5"/>',
  'tokens': '<circle cx="8" cy="15" r="4.5"/><path d="M11.2 11.8 20 3m-3 3 2.5 2.5M14 9l2 2"/>',
  'servers-remote': '<rect x="3.5" y="4.5" width="17" height="6"/><rect x="3.5" y="13.5" width="17" height="6"/><path d="M7 7.5h.01M7 16.5h.01M12 10.5v3"/>',
  'credentials': '<rect x="4.5" y="10.5" width="15" height="10"/><path d="M8 10.5V7a4 4 0 0 1 8 0v3.5M12 14.5v2"/>',
  'vpn': '<path d="M12 3 4.5 6v5.5c0 4.5 3 8 7.5 9.5 4.5-1.5 7.5-5 7.5-9.5V6z"/><path d="m9 12 2.2 2.2L15.5 10"/>',
  'servers': '<rect x="3.5" y="3.5" width="17" height="5"/><rect x="3.5" y="9.5" width="17" height="5"/><rect x="3.5" y="15.5" width="17" height="5"/><path d="M7 6h.01M7 12h.01M7 18h.01"/>',
  'users': '<circle cx="9" cy="8" r="3.5"/><path d="M2.5 20c0-3.6 2.9-6 6.5-6s6.5 2.4 6.5 6M16 4.8a3.5 3.5 0 0 1 0 6.4M18 14.3c2 .7 3.5 2.6 3.5 5.7"/>',
  'sso': '<path d="M14 4.5h5.5v5.5M19.5 4.5 11 13"/><path d="M18 14v5.5H4.5V6H10"/>',
  'audit': '<path d="M6 3.5h9l4 4v13H6z"/><path d="M14.5 3.5v4.5H19M9 12.5h7M9 16h7"/>',
  'releases': '<path d="M12 3.5v11M7.5 10.5l4.5 4.5 4.5-4.5M4.5 19.5h15"/>',
  'search': '<circle cx="10.5" cy="10.5" r="6"/><path d="m15 15 5.5 5.5"/>',
  'menu': '<path d="M4 7h16M4 12h16M4 17h16"/>',
  'close': '<path d="m5.5 5.5 13 13m0-13-13 13"/>',
  'chevron-left': '<path d="m14.5 5.5-6.5 6.5 6.5 6.5"/>',
  'chevron-right': '<path d="m9.5 5.5 6.5 6.5-6.5 6.5"/>',
  'chevron-down': '<path d="m5.5 9.5 6.5 6.5 6.5-6.5"/>',
  'copy': '<rect x="8.5" y="8.5" width="11" height="11"/><path d="M15.5 8.5v-5h-12v12h5"/>',
  'check': '<path d="m4.5 12.5 5 5 10-11"/>',
  'eye': '<path d="M2.5 12S6 5.5 12 5.5 21.5 12 21.5 12 18 18.5 12 18.5 2.5 12 2.5 12z"/><circle cx="12" cy="12" r="3"/>',
  'eye-off': '<path d="M2.5 12S6 5.5 12 5.5c1.4 0 2.7.4 3.8.9M21.5 12S18 18.5 12 18.5c-1.4 0-2.7-.4-3.8-.9"/><path d="M4 4l16 16"/>',
  'plus': '<path d="M12 4.5v15M4.5 12h15"/>',
  'refresh': '<path d="M19.5 12a7.5 7.5 0 1 1-2.2-5.3M19.5 4.5v4.5H15"/>',
  'sun': '<circle cx="12" cy="12" r="4"/><path d="M12 2.5v2.5M12 19v2.5M2.5 12H5M19 12h2.5M5.3 5.3l1.8 1.8M16.9 16.9l1.8 1.8M5.3 18.7l1.8-1.8M16.9 7.1l1.8-1.8"/>',
  'moon': '<path d="M19.5 14.5A8 8 0 0 1 9.5 4.5a8 8 0 1 0 10 10z"/>',
  'monitor': '<rect x="3.5" y="4.5" width="17" height="11"/><path d="M8 20h8M12 15.5V20"/>',
  'globe': '<circle cx="12" cy="12" r="8.5"/><path d="M3.5 12h17M12 3.5c2.4 2.4 3.5 5.2 3.5 8.5s-1.1 6.1-3.5 8.5c-2.4-2.4-3.5-5.2-3.5-8.5S9.6 5.9 12 3.5z"/>',
  'logout': '<path d="M10 4.5H4.5v15H10M9.5 12h11M16.5 8l4 4-4 4"/>',
  'shield': '<path d="M12 3 4.5 6v5.5c0 4.5 3 8 7.5 9.5 4.5-1.5 7.5-5 7.5-9.5V6z"/>',
  'terminal': '<rect x="3.5" y="4.5" width="17" height="15"/><path d="m7.5 9.5 3 2.5-3 2.5M13 15h4"/>',
  'alert': '<path d="M12 3.5 21.5 20h-19z"/><path d="M12 10v4.5M12 17.2h.01"/>',
} as const

export type IconName = keyof typeof iconPaths

export const ICON_NAMES = Object.keys(iconPaths) as IconName[]
