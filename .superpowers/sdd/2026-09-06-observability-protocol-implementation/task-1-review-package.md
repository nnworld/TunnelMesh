# Task 1 review package

Review the task-only diff in `task-1.diff`. Check Prometheus metric names,
injected registry isolation, bounded labels, active gauge semantics, negative
durations/counters, and JSON redaction of Token/target/payload/authorization
values. Run `go test ./internal/observability -count=1`, `go vet ./...`, and
`git diff --check`. Do not modify the worktree; write findings to
`task-1-review.md`.
