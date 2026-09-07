### Task 4: Metadata API, OpenAPI, and audit behavior

**Files:** Modify `internal/server/api.go`, `internal/server/api_test.go`, `docs/api/openapi.yaml`; create `internal/server/metadata_api_test.go`.

**Interfaces:** Produce `GET /api/v1/agents/{agentId}/metadata` with optional `includeStale=true` and the unified `{code,msg,data}` envelope.

- [ ] Write failing tests for 401, owner/admin 200, unrelated user 403, missing Agent 404, stale response shape, redaction, and the absence of a metadata write endpoint.
- [ ] Run `go test ./internal/server -run MetadataAPI -count=1` and verify RED.
- [ ] Implement Handler → Service → Repository routing, owner/admin authorization, bounded response serialization, and audit read events.
- [ ] Update OpenAPI with metadata schemas, `includeStale`, stale semantics, and 401/403/404 responses.
- [ ] Run `go test ./internal/server ./internal/storage -count=1` and `go test ./... -count=1`.

