# Task 9 Fix Round 1 Scoped Review

## Result

**FAIL**

The timeout and cleanup fixes are present: `sendFrame`/`receiveFrame` use
bounded 2-second deadlines, the TCP target uses `DialTimeout` and a read
deadline, and cleanup cancels context, closes the listener, and fails on a
two-second shutdown timeout. The focused scenario is functionally stable in
most repetitions, but the test still uses a fixed shared in-memory SQLite DSN
and fixed usernames.

## Finding

### [P2] Fixed shared-memory DSN makes repeated/concurrent E2E runs collide

`internal/e2e/scoped_token_connections_test.go:25,31` opens
`file:e2e-scoped-token?mode=memory&cache=shared` and creates the fixed
`e2e-owner` username. When another invocation of this E2E test shares the same
SQLite URI (for example an overlapping normal/race gate or two package test
processes), initialization can reuse the same in-memory database and fail at
the user insert with `UNIQUE constraint failed: users.username`. This was
observed during the requested repeated verification (`go test -race ... -count=5`)
while a subsequent isolated `go test ./internal/e2e -run ScopedToken -count=20`
passed. Use a per-test unique DSN and/or unique fixture IDs to make repetition
and concurrent gates deterministic.

Confidence: 90/100.

## Verification

- `go test ./internal/e2e -run ScopedToken -count=20` — PASS on subsequent isolated run
- `go test -race ./internal/e2e -run ScopedToken -count=5` — observed intermittent UNIQUE constraint failure
- Deadlines and strict cleanup logic inspected in the current test — present

No worktree changes were made by this review.
