# Task 4 Report: Bounded runtime stats and probe persistence

## Implemented

- Added schema v4 tables `agent_runtime_stats` and `agent_probe_results` to the sole portable DDL, with explicit ordering indexes.
- Added bounded `AgentRuntimeStats` and `AgentProbeResult` models and repository contracts wired through `storage.DB`.
- Implemented minute-range cursor pagination, retention deletion, microsecond duration persistence, and `(agent_id,node_id,epoch)` lease fencing for runtime stats.
- Implemented probe summary persistence with an allowlisted error-class set; no target response body or arbitrary error text is stored.
- Added `ProbeService` with owner/admin authorization, request bounds, executor correlation, and persisted result summaries.
- Added SQLite contract tests for append/list/cursor/retention, stale epoch rejection, probe pagination and bounded classes, plus service authorization tests.

## Verification

- `go test ./internal/storage -run 'RuntimeStats|ProbeResult' -count=1` — passed.
- `go test ./internal/server -run Probe -count=1` — passed.
- `go test ./... -count=1` — passed.
- `go test -race ./internal/storage ./internal/server` — passed.
- `go vet ./...` — passed.
- `git diff --check` — passed.

## Deferred

Live MySQL contract execution was deferred because `TUNNELMESH_TEST_MYSQL_DSN` is not configured in this environment. The DDL and SQL use only the existing SQLite/MySQL portable subset; run the storage contract against a dedicated MySQL database before merge.

No commit or push was performed.

## Fix round 1

After independent review, the DDL timestamps were changed to bounded VARCHAR
columns, probe persistence gained lease identity and epoch fencing, bounded
aggregate validation and cursor scope checks were added, and probe execution
now has a default deadline. Focused tests remain green.
