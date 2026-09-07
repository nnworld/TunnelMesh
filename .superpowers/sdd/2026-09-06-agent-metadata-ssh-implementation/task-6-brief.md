### Task 6: SSH documentation, integration coverage, and final verification

**Files:** Modify `docs/user-guide/client.md`, `docs/user-guide/agent.md`, `docs/user-guide/tcp-over-websocket-ssh.md`, and `README.md`; create `internal/e2e/agent_metadata_ssh_test.go`.

**Interfaces:** Produce SSH public-key proxy acceptance coverage and complete Agent/Client usage documentation.

- [ ] Write failing integration tests for TCP byte integrity, SSH-like handshake bytes, policy denial, reconnect cleanup, and SSH remote-command exit-code propagation.
- [ ] Run `go test ./internal/e2e -run AgentMetadataSSH -count=1` and verify RED.
- [ ] Ensure the existing TCP proxy carries SSH bytes, supports half-close/disconnect cleanup, and emits Agent/target/user audit context; do not add a second SSH protocol.
- [ ] Document Agent metadata configuration, TCP/UDP simultaneous forwarding, SSH public-key setup, ProxyCommand, remote commands, and the distinction from future command-exec.
- [ ] Run `go test ./... -count=1`, `go test -race ./...`, `go vet ./...`, `git diff --check`, `cd web && npm test -- --run && npm run build`.
- [ ] If Docker is available, run all three Docker builds and validate both Compose files; report unavailable external dependencies explicitly.

## Execution Notes

Implement tasks in order because each task produces interfaces consumed by later tasks. Keep metadata reporting independent from data forwarding so malformed or stale metadata cannot interrupt TCP/UDP/HTTP streams. If arbitrary command execution is requested later, stop and create a separate design.
