# Task 3 review package

Review the task-only instrumentation diff. Check that Agent dial failures and
successful upgrades emit bounded connection/stage metrics, heartbeat metrics
are wired through Session, storage Ping instrumentation does not alter
transaction behavior, and no high-cardinality or secret labels are introduced.
Run focused observability/agent/storage tests, race tests, vet, and diff-check.
Do not modify the worktree; write findings to `task-3-review.md`.
