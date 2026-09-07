# Task 4 independent review

Scope: runtime stats/probe persistence, `migrations/ddl.sql`, and
`internal/server/probe_service.go`. Focused unit and race tests pass, but the
following issues remain against the Task 4 requirements.

## Findings

### [P1] Task 4 migration adds MySQL-invalid indexes on `TEXT` columns

- `migrations/ddl.sql:138-152` defines `window_start TEXT` and then indexes it in `idx_agent_runtime_stats_range`.
- `migrations/ddl.sql:163-165` defines `observed_at TEXT` and then indexes it in `idx_agent_probe_results_range`.

MySQL does not permit a `TEXT`/BLOB column in an index without a prefix length
(error 1170). The new DDL therefore cannot be auto-initialized on MySQL as
claimed in the Task 4 report. The existing DDL has the same historical risk
for some timestamp indexes, but these two indexes are introduced by Task 4
and must be fixed as part of its SQLite/MySQL portability requirement (for
example, use a bounded `VARCHAR` timestamp representation or a driver-specific
portable schema strategy).

### [P1] Probe result writes bypass epoch fencing and persist no lease identity

- `internal/server/probe_service.go:70-88` constructs the persisted result
  without `NodeID` or `Epoch`.
- `internal/storage/repository.go:1230-1244` accepts any `agent_id`, node, and
  epoch combination and performs a plain insert; unlike runtime stats, it does
  not verify the current `agent_runtime_leases` row or reject stale epochs.

The model/schema explicitly carry `node_id` and `epoch`, but the service always
stores zero values and stale writers can append probe summaries after lease
takeover. This defeats epoch fencing for probe persistence and makes the
record's origin unauditable.

### [P2] Runtime stats are described as bounded but accept negative/unbounded values

- `internal/storage/repository.go:1145-1157` validates only identity and
  timestamps before inserting counters, byte totals, stream counts, and RTT
  durations.
- No checks reject negative counters/durations, reversed windows, or an
  excessive window length.

Corrupt or hostile callers can persist negative aggregates and arbitrarily long
windows, undermining the bounded-history contract and producing misleading
reports. Validation should enforce non-negative counters/durations and the
one-minute aggregate bounds before the fencing insert.

### [P2] Probe execution has no default deadline

- `internal/server/probe_service.go:71-73` passes the caller context directly
  to `ProbeExecutor.Execute`.
- `validateDiagnoseRequest` (`:102-104`) permits `Timeout == 0`, but no default
  timeout is applied.

A caller can submit a valid request whose executor blocks indefinitely, tying up
server resources. The service should derive a bounded context (with a documented
default) and cap it at the existing 30-second maximum.

### [P2] Cursor lookup is not scoped to the requested resource/range

- `internal/storage/repository.go:1179-1185` and `:1251-1257` resolve a cursor
  by ID globally, then apply the current agent/range filter.

A cursor copied between agents or outside the requested time range is accepted
and can skip records or return an unexpected page boundary. Cursors should carry
or be checked against the agent and query scope, returning a clear invalid-cursor
error for mismatches.

## Positive checks

- `go test ./internal/storage -run 'RuntimeStats|ProbeResult' -count=1` — PASS.
- `go test ./internal/server -run Probe -count=1` — PASS.
- `go test -race ./internal/storage -run 'RuntimeStats|ProbeResult' -count=1` — PASS.
- `go test -race ./internal/server -run Probe -count=1` — PASS.
- Probe responses and persisted models do not include target response bodies or
  arbitrary error text; repository error classes are allowlisted.
- Owner/admin authorization is enforced before probe execution in
  `ProbeService.Diagnose`.

No files other than this review report were modified; no commit was created.
