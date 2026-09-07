# Task 2 review package

Review `task-2.diff` and the health endpoint implementation. Check that live
does not require DB, ready returns 503 on unhealthy/empty dependencies without
leaking errors, metrics preserves Prometheus content type, and `/health/*` and
`/metrics` cannot be swallowed by SPA fallback. Run server focused tests, race
tests, vet, and diff-check. Do not modify the worktree; write findings to
`task-2-review.md`.
