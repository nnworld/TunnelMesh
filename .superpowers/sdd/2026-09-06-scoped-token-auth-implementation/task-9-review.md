# Task 9 Independent Review

## Result

**FAIL**

The E2E scenario covers the requested happy/negative flows: distinct Agent
and Client credentials, UDP denial while the Client socket remains alive,
allowed TCP data delivery, rotation rejection on the existing Client
connection, and audit-detail secret scanning. The implementation passed the
available Go/frontend gates, but the test itself has two reliability problems
that can turn a security regression into a hung or leaking CI job.

## Findings

### [P2] WebSocket and target I/O assertions have no deadlines

`internal/e2e/scoped_token_connections_test.go:65,74,76,80,86,89,93,101`
calls `websocket.Message.Receive` and `io.ReadFull` without setting read
deadlines or using a timeout-aware helper. If the server drops a frame, sends
the wrong frame, or the relay stalls, the test blocks indefinitely rather than
failing with a bounded diagnostic. This makes the E2E gate vulnerable to hangs
under exactly the timing/resource failures it is intended to catch.

Confidence: 98/100.

### [P2] ServeListener cleanup timeout is silently ignored

`internal/e2e/scoped_token_connections_test.go:55` closes the HTTP listener
and waits one second for `serveErr`, but does nothing when the timeout fires.
The test can therefore return while the runtime serving goroutine is still
alive (and while its resources remain active), masking a shutdown regression
and allowing cross-test interference. The cleanup should fail the test on a
timeout and/or use a bounded context plus an explicit diagnostic.

Confidence: 92/100.

## Verification

- `go test ./... -count=1` — PASS
- `go vet ./...` — PASS
- `go test ./internal/e2e -run TestScopedTokenConnectionsAndRotation -count=1 -v` — PASS
- `go test -race ./internal/e2e ./internal/server ./internal/auth ./internal/storage -count=1` — PASS
- `cd web && npm test -- --run` — PASS (6 tests)
- `cd web && npm run build` — PASS (Vite build; chunk-size warning only)

No raw service-token secret was embedded in the test fixture or found in the
audited details/built frontend during this review.
