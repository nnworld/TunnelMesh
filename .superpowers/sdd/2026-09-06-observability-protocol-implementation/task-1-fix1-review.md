# Task 1 Fix Round 1 Review

## Verdict

**FAIL** — snake_case JSON serialization and zero-floor behavior are fixed, but active gauges can still diverge from the protected active counts under concurrent observations.

## Finding

### [P1] Publish the gauge value while holding the active-state lock

Both active observers compute the protected map value, unlock, and only then call `Gauge.Set` (`internal/observability/metrics.go:75-87` and `internal/observability/metrics.go:116-128`). Two calls can therefore publish in reverse order: call A updates the map to 1 and unlocks, call B updates it to 2 and publishes 2, then call A publishes the stale value 1. The inverse close sequence can similarly leave a positive gauge after the map reached zero. Prometheus collectors are thread-safe, but that does not preserve the ordering between the separate map update and `Set`. Keep `Set` inside the same critical section (or otherwise publish using an ordered/atomic state transition) so the exported active gauge matches the authoritative count.

## Confirmed fixes

- `StageEvent.MarshalJSON` now preserves the declared snake_case JSON names and `omitempty` tags (`internal/observability/events.go:34-48`).
- Unpaired close/failure/rejection observations are floored at zero and no longer make either active gauge negative in sequential execution.
- Regression tests cover snake_case keys and first-event inactive outcomes.

## Verification

- `go test ./internal/observability -count=1` — PASS
- `go vet ./...` — PASS
- `git diff --check` — PASS

