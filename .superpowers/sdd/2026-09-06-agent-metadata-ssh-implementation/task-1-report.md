# Task 1 Report: Agent Metadata Configuration and Collector

## Status

Implemented with TDD. The Agent now supports explicit, allowlisted metadata sources from files and environment variables, with bounded UTF-8 collection and field-scoped failures that do not close the active session.

## Delivered

- Added `config.MetadataSource` and `AgentConfig.Metadata` decoding through Viper.
- Added configuration validation for source type, metadata name syntax/length, duplicate names, absolute file paths, required environment keys, wildcard rejection, sensitive names, and the 32-field limit.
- Added `MetadataCollector.Collect(ctx)` with:
  - explicit `file` and `env` reads only;
  - 4 KiB per-field bound;
  - 32 KiB aggregate JSON payload bound;
  - one trailing newline trim, UTF-8 validation, context cancellation, deterministic field/error ordering;
  - field-scoped errors for missing/unreadable/invalid sources.
- Added `NewSessionWithMetadata` and `Session.CollectMetadata`; metadata failures remain separate from frame transport lifecycle.
- Added tests for allowlisted reads, missing sources, invalid and sensitive names, relative paths, UTF-8, field and aggregate limits, deterministic ordering, config decoding, cancellation, and session non-closure.

## Verification

- RED observed with `go test ./internal/agent ./internal/config -run Metadata -count=1` before implementation (missing metadata types/collector).
- `go test ./internal/agent ./internal/config -count=1`
- `go test ./... -count=1`
- `go test -race ./internal/agent ./internal/config`
- `go vet ./...`
- `git diff --check`

All verification commands passed after implementation.

## Concerns / follow-ups

- Metadata protocol framing, persistence, server APIs, and UI remain intentionally deferred to Tasks 2–5.
- Collector errors avoid returning source paths, environment keys, or values to keep failure reporting safe for logs and ACKs.
