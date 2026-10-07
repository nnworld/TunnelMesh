/**
 * The menu-bar quick panel is the same bundle as the settings window, chosen by the URL
 * fragment the shell loads.
 *
 * One document with two roots is deliberate: a second embedded directory, a second HTTP
 * surface and a second secret would each need their own review, and the panel wants the
 * same /api/stats the statistics tab already renders.
 */

/** Mirrors tray.PanelRoute in internal/tray/api.go. The guard test pins the pairing. */
export const PANEL_ROUTE = '#/panel'

/** isPanelRoute is exact on purpose. A prefix match would make "#/panel-x" a second panel
 * nobody asked for, and the fragment is the only routing signal the shell sends. */
export function isPanelRoute(hash: string): boolean {
  return hash === PANEL_ROUTE
}
