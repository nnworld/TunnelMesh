#!/usr/bin/env node
// Mirror the Vite build output into the Go embed directory that
// `//go:embed all:web_dist` in internal/server/web.go compiles into the Server
// binary. The admin UI only reaches users when this directory is refreshed and
// the Server is rebuilt, so the sync is wired into `npm run build` instead of
// being a remembered manual step.
//
// Usage: node scripts/sync-web-dist.mjs [source] [target]
// Both arguments are optional and default to the repository layout; tests pass
// temporary directories.
import { cpSync, existsSync, rmSync, statSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const here = dirname(fileURLToPath(import.meta.url))
const source = resolve(process.argv[2] ?? resolve(here, '../dist'))
const target = resolve(process.argv[3] ?? resolve(here, '../../internal/server/web_dist'))

if (!existsSync(source) || !statSync(source).isDirectory()) {
  console.error(`sync-web-dist: source build output missing: ${source} (run vite build first)`)
  process.exit(1)
}

// Mirror, not merge: hashed chunk names change on every build, so files left
// behind by an older build would be embedded forever and bloat every Server
// binary. rmSync+cpSync also keeps the step portable to hosts without rsync.
rmSync(target, { recursive: true, force: true })
cpSync(source, target, { recursive: true })
console.log(`sync-web-dist: mirrored ${source} -> ${target}`)
