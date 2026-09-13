# sshhost — throwaway SSH/SFTP host for the WebSSH E2E

A minimal SSH server used **only** by `test/e2e/webssh`. It exists because the
WebSSH feature has to be verified against a real SSH peer with a real pty: an
echo loop cannot run `lrzsz`, and `lrzsz` refuses to start without a tty.

## Why a separate Go module

This directory has its own `go.mod`, so `go build ./...` and `go vet ./...` at
the repository root skip it entirely. That is deliberate:

- TunnelMesh is a tunnel, not an SSH server. The product must never ship an SSH
  server, and `golang.org/x/crypto/ssh` + `github.com/creack/pty` +
  `github.com/pkg/sftp` must never land in the root `go.mod`.
- Test-only dependencies stay out of every release binary and out of the
  product's dependency audit surface.

Build and run it directly only when debugging the harness:

```bash
cd test/e2e/webssh/sshhost
go build -o /tmp/sshhost .
TM_SSHHOST_CWD=/tmp/ssh-fs TM_SSHHOST_LISTEN=127.0.0.1:2222 /tmp/sshhost
```

The harness builds it into its own bin directory automatically.

## Behaviour

- Accepts exactly one password login: `tmuser` / `tmpass`. There is no key auth
  and no other user.
- Host key is a freshly generated Ed25519 key, so every run has a new
  fingerprint. That is what makes the host-key confirmation check meaningful.
- `shell` runs a real pty with the caller's shell (`TM_SSHHOST_SHELL`, else
  `$SHELL`, else `/bin/bash`, else `/bin/sh`) and forces the prompt to
  `tmhost$ ` so terminal assertions are deterministic.
- `subsystem sftp` serves the working directory **read-only**.
- Everything happens inside `TM_SSHHOST_CWD`, so an `rz` upload can be verified
  from the harness without touching the rest of the machine.

## Environment

| Variable | Default | Purpose |
| --- | --- | --- |
| `TM_SSHHOST_LISTEN` | `127.0.0.1:2222` | listen address |
| `TM_SSHHOST_CWD` | a fresh temp dir | shell and SFTP root |
| `TM_SSHHOST_SHELL` | `$SHELL`, `/bin/bash`, `/bin/sh` | pty shell binary |
| `TM_SSHHOST_EXTRA_PATH` | unset | appended to `PATH` so `sz`/`rz` resolve |

`HOME` and `ZDOTDIR` are pointed at a scratch directory by the harness so the
pty shell reads a generated rc file instead of the developer's own profile.

## Security note

This binary listens on a local port and accepts a hardcoded password. It is test
code with no hardening, no rate limiting and no audit logging. It must never be
installed, packaged, referenced from production deployment manifests, or exposed
beyond loopback.
