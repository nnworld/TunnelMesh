# Task 2 Report: Configuration and CLI Framework

## Status

Implemented and locally verified. Configuration loading now follows CLI > environment > file > defaults precedence and returns typed configuration snapshots. The three binaries now execute Cobra roots with the command shells required by the implementation brief.

## Delivered

- Added `internal/config` with typed local/cluster, SQLite/MySQL, database/etcd registry, node, server, agent, and client settings.
- Added Viper-backed loading with YAML/JSON/TOML file support, `TUNNELMESH_*` environment binding, CLI/override injection, context cancellation, and explicit validation.
- Local defaults are SQLite, `auto_init=true`, database registry, public `:80`/`:443` listeners, and enabled `/ws/tcp` TCP Bridge with a 64 KiB limit.
- Cluster validation requires MySQL storage, MySQL DSN, MySQL TLS, and node identity. Registry values are constrained to `database` or `etcd`; etcd requires endpoints.
- Added redacted JSON rendering for `print-config`; DSNs and private key paths are never emitted in clear text.
- Added Cobra roots and command shells:
  - Server: `run`, `check-config`, `init-db`, `print-config`
  - Agent: `run`, `register`, `check-config`, `id`
  - Client: `login`, `agent`, `tunnel`, `forward`, `publish`, `proxy`, `stop`, `status`
- Wired all three `cmd/*/main.go` entrypoints to their roots.
- Added Cobra and Viper dependencies.

## Verification

- `go test ./internal/config ./internal/cli -v`
- `go test ./...`
- `go test -race ./internal/config ./internal/cli`
- `go vet ./...`
- `go build ./cmd/...`
- `git diff --check`

All commands passed.

## Concerns / follow-ups

- Command shells intentionally stop at configuration/orchestration boundaries. Database initialization, server loops, agent registration, and client forwarding are service work for later tasks.
- `ConfigOptions` accepts map aliases (`CLI`, `Overrides`, `Set`, and test/embedder `Env`) to keep later integrations decoupled from Cobra internals.
