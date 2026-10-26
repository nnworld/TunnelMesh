import type { ITheme } from '@xterm/xterm'
import type { ResolvedTheme } from '../utils/theme'

// xterm paints to a canvas and cannot read CSS variables, so the palette is
// duplicated here. Backgrounds must equal --tm-terminal-bg in tokens.css
// (terminal-theme.spec.ts enforces it); the ANSI colours are tuned for 4.5:1
// against that background.
const LIGHT: ITheme = {
  background: '#F4F6F2', foreground: '#101B1A', cursor: '#0A6C74', cursorAccent: '#F4F6F2', selectionBackground: '#BFDCDD',
  black: '#101B1A', red: '#B02F26', green: '#13734B', yellow: '#85560A', blue: '#0A5E8A', magenta: '#8A3F8F', cyan: '#0A6C74', white: '#4E5D5B',
  brightBlack: '#5F6D6A', brightRed: '#C8372D', brightGreen: '#178A5C', brightYellow: '#9A6408', brightBlue: '#0B6FA3', brightMagenta: '#A24FA8', brightCyan: '#0D8089', brightWhite: '#101B1A',
}
const DARK: ITheme = {
  background: '#0F1716', foreground: '#E6EBE8', cursor: '#4FB3B8', cursorAccent: '#0F1716', selectionBackground: '#25474A',
  black: '#1A2322', red: '#EF6A5E', green: '#3DBB86', yellow: '#E0A83A', blue: '#6AB0E8', magenta: '#C792D0', cyan: '#4FB3B8', white: '#A5B2AF',
  brightBlack: '#7D8B88', brightRed: '#F58A80', brightGreen: '#5FD0A0', brightYellow: '#EDBE62', brightBlue: '#8BC5F0', brightMagenta: '#D8AEE0', brightCyan: '#74C9CD', brightWhite: '#E6EBE8',
}

export function terminalTheme(theme: ResolvedTheme): ITheme {
  return { ...(theme === 'dark' ? DARK : LIGHT) }
}
