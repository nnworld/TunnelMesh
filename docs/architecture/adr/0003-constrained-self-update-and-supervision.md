# ADR 0003: Constrained self-update and process supervision for client and agent

- Status: Accepted
- Date: 2026-10-25

## Context

`tunnelmesh-client run` and `tunnelmesh-agent run` have no in-process lifecycle management. Upgrades are performed by the install scripts, crashes are handled only by the operating system service manager, and a failed upgrade is rolled back only by a script that must be run by an operator. Fleets of clients and agents therefore drift across versions, and a bad release needs manual recovery on every node.

AGENTS.md states that TunnelMesh does not implement arbitrary remote command execution. A self-updater downloads and executes a binary, so the boundary between "constrained self-update" and "remote command execution" must be explicit.

Two environment facts constrain the design:

- The packaged systemd units use `ProtectSystem=strict` and the service user can write only its state directory, so a process cannot replace its own installed binary.
- On Windows a running executable cannot be overwritten.

## Decision

`run` becomes a supervisor by default (`update.supervise=true`). The installed binary starts the real service as a child worker (`TUNNELMESH_WORKER=1`), restarts it with backoff when it crashes, and runs the updater. Upgrades are enabled by default (`update.enabled=true`) and both switches can be turned off in configuration.

Upgraded binaries are stored only in the state directory (`<state>/updates/versions/<version>/`). The installed binary is never modified; rollback switches a pointer back to the previous version or to the installed baseline.

The updater is bounded:

- It fetches only the release archive for its own role and platform. The URL is derived from configuration and a version number, never from a message.
- The source is fixed by configuration: GitHub Releases (optionally through a configured mirror) or the TunnelMesh Server.
- It verifies the archive against `SHA256SUMS` from the same source and extracts only a file named `tunnelmesh-<role>[.exe]`.
- The target version is `min(latest release, Server version)`. It never downgrades, never moves a stable install to a pre-release, and never upgrades a `dev` build.
- A candidate must pass `--version` and `check-config` before activation, then must report ready within a health window and survive a stability window. Otherwise the previous version is restored and the candidate is marked bad for a period.
- Rollback applies only to failures caused by an upgrade. The updater never rewrites the user's configuration.

The worker reports readiness by atomically writing a file into a per-launch directory. The file carries the Server version learned from the handshake acknowledgement, which is extended with an optional `server_version` field.

Exit codes become a contract: 78 for configuration errors, 75 for a held lock, 77 for fatal authentication, 70 when the supervisor gives up.

This is not arbitrary remote command execution. No peer or Server message can supply a command, URL or argument; the only executable ever started is the extracted `tunnelmesh-<role>` binary from the configured source. AGENTS.md is amended to say so.

## Consequences

- Nodes recover from crashes and bad releases without operator action, within the health window plus the stability window.
- Fleet upgrades follow the Server: operators must upgrade the Server first, because the Server version is the ceiling.
- The supervisor and updater logic is frozen at the installed version, so an updater defect is fixed by reinstalling, not by self-update.
- Integrity relies on `SHA256SUMS` served from the same place as the archive. This protects against corruption and truncation but not against a compromised source. With `source=server`, a compromised Server could distribute a malicious binary to every client and agent, which widens the blast radius compared with today. Distribution from the Server is therefore opt-in through `server.updates.dir`, and clients default to GitHub.
- Containers default to both switches off, because the orchestrator already supervises them and image tags should be immutable. Users can opt in through environment variables.
- Docker, systemd, launchd and WinSW remain the outer layer: the supervisor exits non-zero when it gives up so they can act.

## Rejected alternatives

### Rely only on the operating system service manager

Using systemd, launchd and WinSW restarts, with upgrades done by a subcommand, was rejected because direct runs, container runs and embedded runs would have no supervision, and the three platforms would behave differently.

### Replace the installed binary in place

Rejected because the packaged systemd units make the installed directory read-only for the service user, Windows cannot overwrite a running executable, and an in-place swap makes rollback depend on a backup that sits in the same directory.

### Signature verification instead of checksums only

Not chosen now because it needs signing keys, release pipeline changes and key rotation. It remains a compatible later step, since the verification sits behind the `Source` abstraction.

## Related records

- Implementation plan: [2026-10-25-client-agent-auto-update](../../superpowers/plans/2026-10-25-client-agent-auto-update.md)
