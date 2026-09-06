# Task 6 Report: SSH Documentation and Integration Coverage

## Scope

- Added `internal/e2e/agent_metadata_ssh_test.go` covering raw TCP byte integrity for SSH-like handshake and command/exit-status bytes, policy denial, reconnect replacement and stale cleanup, and bridge disconnect cleanup.
- Expanded Agent documentation with explicit file/env metadata allowlists, limits, redaction, stale behavior, and the no-command-exec boundary.
- Expanded Client and SSH-over-WebSocket documentation with TCP/UDP/HTTP/publish/proxy usage, SSH public-key/ssh-agent setup, `tunnelmesh-client` and `websocat` ProxyCommand examples, remote command exit-code behavior, and policy/security guidance.
- Updated README capability summary and security boundary.

## Test notes

The first focused E2E run exposed a cleanup hang in the test double when a stream ignored half-close. The fixture was corrected to model a target that observes `CloseWrite`; the existing bridge then cleanly closes both ends on disconnect without adding a second SSH protocol.

## Verification

- `go test ./internal/e2e -run AgentMetadataSSH -count=1`
- `go test ./... -count=1`
- `go test -race ./...`
- `go vet ./...`
- `git diff --check`
- `cd web && npm test -- --run`
- `cd web && npm run build`

Docker verification was unavailable because the `docker` executable is not installed in the environment; no Docker build or Compose validation was attempted.
