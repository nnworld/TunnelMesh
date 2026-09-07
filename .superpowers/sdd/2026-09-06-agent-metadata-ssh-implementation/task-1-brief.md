### Task 1: Agent metadata configuration and collector

**Files:** Modify `internal/config/config.go`, `internal/config/config_test.go`, `internal/agent/session.go`; create `internal/agent/metadata.go` and `internal/agent/metadata_test.go`.

**Interfaces:** Produce `config.MetadataSource` and `MetadataCollector.Collect(ctx) (MetadataSnapshot, error)`.

- [ ] Write failing tests for allowlisted file/env reads, missing sources, invalid names, relative paths, sensitive names, UTF-8, field limits, aggregate limits, and deterministic ordering.
- [ ] Run `go test ./internal/agent ./internal/config -run Metadata -count=1` and verify RED.
- [ ] Add `AgentConfig.Metadata`, validate `file`/`env` entries, bound reads, trim one trailing newline, and return per-field errors without closing the session.
- [ ] Run `go test ./internal/agent ./internal/config -count=1` and verify GREEN.

