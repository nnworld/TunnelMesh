# WebSSH / WebSFTP browser E2E

End-to-end scenario for the admin WebSSH feature. It drives the **real** built
admin SPA in headless Chrome over the DevTools Protocol against a **real**
`tunnelmesh-server`, a **real** `tunnelmesh-agent` and a **real** SSH host with a
pty shell, so every layer is exercised: browser SSH (WASM libssh2) → WebSocket →
Server relay → Agent → pty/SFTP.

This is not part of `go test ./...` or `npm test`. Run it before releasing any
change that touches the relay, the WebSSH broker, the Agent stream dispatcher or
the terminal/SFTP frontend.

## What it asserts

| # | Check | Why it exists |
| --- | --- | --- |
| 1 | the server list marks auto-auth capable hosts | credential capability is a per-row UI contract |
| 2 | a stored credential skips the auth dialog | regression: password prompt shown despite a stored secret |
| 3 | the terminal connects with the stored password | full auth chain over the tunnel |
| 4 | keystrokes reach the pty | regression: terminal rendered but swallowed input (no cursor) |
| 5 | `sz` downloads byte-for-byte | ZMODEM receive + **relay receive-window flow control** |
| 6 | the transfer panel closes and the prompt returns | regression: bulk download killed the shell mid-transfer |
| 7 | `rz` uploads byte-for-byte | ZMODEM send + **relay send-window flow control** |
| 8 | the prompt returns after `rz` | regression: upload abort left pty echo garbage on screen |
| 9 | SFTP reuses the authenticated connection | terminal → SFTP is one SSH transport, not a second ticket |
| 9b | SFTP upload writes byte-for-byte | regression: non-blocking `libssh2_sftp_write` partial returns truncated uploads |
| 10 | disconnect returns to the server list | a closed session must not invite a reload that cannot succeed |
| 11 | no credential falls back to the manual dialog | the fallback path stays reachable |
| 12 | a second session connects | prerequisite for the refresh check |
| 13 | refresh routes back instead of dead-ending | regression: reload showed "credentials missing" with no way out |
| 14 | the browser console stayed clean | any console error or uncaught exception fails the run |

Checks 5–8 are skipped (not failed) when `lrzsz` is not installed.

## Prerequisites

- Go toolchain matching the root `go.mod` (used to build the binaries).
- Node.js **20 or newer** (the harness uses the built-in `WebSocket`; there are
  no npm dependencies to install).
- Google Chrome or Chromium, headless-capable.
- `lrzsz` (`sz`/`rz`) for the ZMODEM checks — `brew install lrzsz` /
  `apt-get install lrzsz`. Without it those four checks report `SKIP`.
- Free local ports 18199 (Server), 2222 (SSH host) and 19333 (Chrome CDP).

Nothing touches your real `~/.ssh`, your shell profile or any production data:
the SSH host is a throwaway that only accepts `tmuser`/`tmpass` on
`127.0.0.1:2222`, and its pty shell runs with a redirected `HOME`.

## Run

```bash
node test/e2e/webssh/run.mjs
```

Exit code is `0` only when every check passes. Logs, screenshots and
`results.json` are written to the work directory (default
`$TMPDIR/tunnelmesh-webssh-e2e`) and printed on the last line.

## Configuration

Everything is optional; the defaults work on a clean checkout.

| Variable | Default | Purpose |
| --- | --- | --- |
| `TM_E2E_DIR` | `$TMPDIR/tunnelmesh-webssh-e2e` | work directory for db, logs, screenshots, fixtures |
| `TM_E2E_REPO` | inferred from this file | repository root used for `go build` |
| `TM_E2E_BIN_DIR` | `$TM_E2E_DIR/bin` | where the four binaries are built |
| `TM_E2E_SKIP_BUILD` | unset | `1` reuses existing binaries instead of rebuilding |
| `TM_E2E_PORT` | `18199` | Server HTTP/WS port |
| `TM_E2E_SSH_PORT` | `2222` | throwaway SSH host port |
| `TM_E2E_CDP_PORT` | `19333` | Chrome remote debugging port |
| `TM_E2E_CHROME` | auto-detected | absolute path to Chrome/Chromium |
| `TM_E2E_DOWNLOAD_BYTES` | `4194304` | `sz` payload size |
| `TM_E2E_UPLOAD_BYTES` | `1572864` | `rz` payload size |
| `TM_E2E_SFTP_UPLOAD_BYTES` | `3145728` | SFTP upload payload size |

Keep the payload sizes large. Both must exceed the 512 KiB Server relay receive
window, and the download must exceed the 4 MiB browser transport guard, or a
flow-control regression silently passes.

Fast iteration loop:

```bash
TM_E2E_SKIP_BUILD=1 node test/e2e/webssh/run.mjs
```

## Layout

```
run.mjs          scenario script: the checks above, top to bottom
lib/stack.mjs    config, go build, Server/Agent/SSH-host lifecycle, admin API seeding
lib/cdp.mjs      minimal Chrome DevTools Protocol driver (no npm dependencies)
lib/fixtures.mjs deterministic multi-megabyte payloads, generated not committed
sshhost/         throwaway SSH+SFTP host, its own Go module (see sshhost/README.md)
```

## Debugging a failure

1. Read `$TM_E2E_DIR/results.json` and the `== summary ==` block to find the
   first failing check.
2. Look at the matching `shot-*.png` — the terminal is rendered, so screenshots
   show exactly what the user would have seen.
3. `logs/server.log`, `logs/agent.log` and `logs/sshhost.log` carry the relay and
   SSH side. A stream reset shows up in `server.log` with a stable error code.
4. `CONSOLE:` / `EXCEPTION:` entries in the run output are browser-side errors;
   they fail check 14 even when every functional check passed.

On crash the harness prints the last 60 lines of every log before exiting.

## Known constraints

- macOS and Linux are supported. On Windows the harness needs a Chrome path via
  `TM_E2E_CHROME` and a POSIX shell available to `sshhost`.
- The scenario expects the SPA default locale (`zh-CN`) or `en-US`; other
  locales need their labels added to the `UI` table in `run.mjs`.
- `sshhost` serves SFTP read-only on purpose, so the scenario can never mutate
  the host machine through the file manager.
