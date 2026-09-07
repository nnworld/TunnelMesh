# Task 1 Fix Round 2 Review

## Verdict

**PASS** — the requested round-2 fixes are present and the scoped verification checks pass.

## Review results

- `StageEvent.MarshalJSON` now replaces every metadata value with `[redacted]`, regardless of metadata key (`internal/observability/events.go:60-65`). Token, target, and payload fields remain redacted, while the declared snake_case JSON tags and `omitempty` behavior are preserved (`internal/observability/events.go:34-48`).
- Both active-state paths update the authoritative map and call the corresponding `Gauge.Set` while holding `activeMu` (`internal/observability/metrics.go:76-87` and `internal/observability/metrics.go:117-128`). This removes the previously identified stale publish race; unpaired close/reject/failure events remain floored at zero.
- Regression tests cover all-metadata redaction, snake_case JSON keys, and inactive gauge observations (`internal/observability/events_test.go:24-46`, `internal/observability/metrics_test.go:89-99`).

## Verification

- `go test ./internal/observability -count=1` — PASS
- `go vet ./...` — PASS
- `git diff --check` — PASS

No remaining findings within the requested round-2 scope.
