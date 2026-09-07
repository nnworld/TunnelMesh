# Task 1 Metrics/Events Review

## Result

**FAIL**

The Prometheus collectors use an injected registry, avoid obvious secret and
high-cardinality identity labels, clamp active gauges at zero, and the focused
tests/vet/diff check pass. One event-redaction issue remains.

## Finding

### [P1] Arbitrary metadata values are serialized unless their key looks sensitive

`internal/observability/events.go:59-66` copies metadata values verbatim when
the metadata key does not match the `sensitiveKey` substring list. Agent
metadata values can contain credentials or other host secrets under benign
allowlisted names such as `region`, `account`, or `device`; the project design
explicitly requires metadata values not to enter structured logs. The existing
test even asserts that `region=cn-east-1` remains visible. Redact all metadata
values (or omit the metadata map and retain only bounded field names/counts),
rather than inferring sensitivity from keys.

Confidence: 96/100.

## Verification

- `go test ./internal/observability -count=1` — PASS
- `go vet ./internal/observability` — PASS
- `git diff --check` — PASS

No worktree changes were made by this review.
