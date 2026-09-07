# Task 1 Review — Observability primitives and redacted events

## Verdict

**FAIL** — the focused tests and static checks pass, but the implementation has two high-confidence correctness issues in the event JSON contract and active gauge accounting.

## Findings

### [P1] Preserve the StageEvent JSON field contract

`StageEvent.MarshalJSON` copies data into a local `safeEvent` whose exported fields have no JSON tags (`internal/observability/events.go:34-40`). `encoding/json` therefore emits keys such as `TraceID`, `ConnectionID`, `TokenID`, and `StartedAt`, and it also emits zero-value fields, instead of the declared `snake_case`/`omitempty` contract (`trace_id`, `connection_id`, `token_id`, `started_at`, etc.). Any structured-log or API consumer using the documented field names will silently stop finding the event fields even though redaction still occurs.

### [P1] Do not decrement active gauges for non-active outcomes

`ObserveConnection` decrements `connections_active` for `failed` and `error` (`internal/observability/metrics.go:66-74`), and `ObserveStream` decrements `streams_active` for `rejected` and `failed` (`internal/observability/metrics.go:97-108`). Those outcomes can occur before a connection/stream has ever become active; a first observation of either result therefore exports `-1` for a metric that represents a current active count. The same public method has no identity/state tracking to prevent this underflow. Active gauges should only decrement after a previously recorded active/open state (or clamp at zero).

## Checks

- `go test ./internal/observability -count=1` — PASS
- `go vet ./...` — PASS
- `git diff --check` — PASS

## Reviewed behavior that passed

- Metric families use the required `tunnelmesh_` names and fixed low-cardinality label vectors.
- Injected registries remain isolated; a nil registry creates a private registry.
- Negative stage/probe durations are clamped/skipped, and non-positive byte deltas are ignored.
- Token, target, payload, and authorization metadata values are redacted from serialized values.

## Residual test gap

The existing tests only cover paired active increment/decrement and do not assert the JSON key names, `omitempty` behavior, or first-event failure/rejection gauge values. Add regression tests for those cases after fixing the findings.
