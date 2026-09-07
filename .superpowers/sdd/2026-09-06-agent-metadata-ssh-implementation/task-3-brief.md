### Task 3: Runtime metadata storage and Service layer

**Files:** Modify `migrations/ddl.sql`, `internal/storage/models.go`, `internal/storage/repository.go`, and `internal/storage/db.go`; create `internal/storage/metadata_test.go`, `internal/server/metadata_service.go`, and `internal/server/metadata_service_test.go`.

**Interfaces:** Produce `AgentRuntimeMetadata`, `AgentMetadataRepository`, and `AgentMetadataService` with `Upsert`, `Get`, `List`, and `MarkStale`.

- [ ] Write failing SQLite contract tests for insert/update, equal-revision replay, stale epoch/revision rejection, stale marking, and cursor pagination.
- [ ] Run `go test ./internal/storage -run RuntimeMetadata -count=1` and verify RED.
- [ ] Add portable `agent_runtime_metadata` DDL with Agent ID primary key, node/epoch/revision, JSON/TEXT metadata, timestamps, expiry, stale flag, and indexes.
- [ ] Implement explicit-column repository queries, epoch/revision fencing, idempotent replay, stale marking, and cursor pagination.
- [ ] Implement Service validation, JSON conversion, stale computation, and sensitive-value redaction.
- [ ] Run SQLite tests and, when `TUNNELMESH_TEST_MYSQL_DSN` exists, the same contract against MySQL.

