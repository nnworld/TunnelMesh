# Task 3 report — runtime metadata storage and service

Implemented the runtime Agent metadata persistence and service layer.

- Added portable `agent_runtime_metadata` DDL and indexes.
- Added `AgentRuntimeMetadata` model and `AgentMetadataRepository` with
  explicit-column Upsert/Get/List/MarkStale operations.
- Added epoch/revision fencing, equal-revision idempotent replay, stale
  marking, and cursor pagination for SQLite and MySQL-aware transactions.
- Added `AgentMetadataService` validation, field/source limits, sensitive-name
  redaction, JSON conversion, expiry-based stale computation, and view helper.
- Added SQLite repository contract tests and service validation/redaction tests.

Verification:

- `go test ./... -count=1` — pass
- `go test ./internal/storage ./internal/server -run 'RuntimeMetadata|AgentMetadata'` — pass
- `go vet ./...` — pass
- `git diff --check` — pass

