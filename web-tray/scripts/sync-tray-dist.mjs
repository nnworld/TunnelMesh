#!/usr/bin/env node
// Mirror the Vite build output into the Go embed directory that
// `//go:embed all:dist` in internal/tray/webdist/embed.go compiles into the tray
// binary. The settings window only changes when this directory is refreshed and the
// tray is rebuilt, so the sync is wired into `npm run build` instead of being a
// remembered manual step.
//
// Usage: node scripts/sync-tray-dist.mjs [source] [target]
// Both arguments are optional and default to the repository layout; tests pass
// temporary directories.
import { cpSync, existsSync, statSync, rmSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const here = dirname(fileURLToPath(import.meta.url))
const source = resolve(process.argv[2] ?? resolve(here, '../dist'))
const target = resolve(process.argv[3] ?? resolve(here, '../../internal/tray/webdist/dist'))

if (!existsSync(source) || !statSync(source).isDirectory()) {
  console.error(`sync-tray-dist: source build output missing: ${source} (run vite build first)`)
  process.exit(1)
}
if (!existsSync(resolve(target, '..'))) {
  console.error(`sync-tray-dist: embed package missing: ${resolve(target, '..')}`)
  process.exit(1)
}

// Mirror, not merge: hashed chunk names change on every build, so files left behind by
// an older build would be embedded forever and bloat every tray binary.
rmSync(target, { recursive: true, force: true })
cpSync(source, target, { recursive: true })
console.log(`sync-tray-dist: mirrored ${source} -> ${target}`)
