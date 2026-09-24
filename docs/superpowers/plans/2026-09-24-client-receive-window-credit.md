# Client receive-window credit timing fix plan

Status: retroactive emergency plan (implementation completed)

The user reported that a large JavaScript asset still intermittently failed with `ERR_CONTENT_LENGTH_MISMATCH` through a local Client forward, even though the same asset worked through a Server-managed route. The incident remained production-impacting after the earlier delayed-response fix, so this change followed the emergency path: prove the failing behavior, apply the smallest correction, and record this plan after verification.

## 1. Goal

Return Client receive-window credit only after the application consumes bytes. A slow browser or local HTTP consumer must not cause the Server to enqueue more bytes than its per-stream send queue can hold.

## 2. Root cause

`frameStream.Read` in `internal/client/session.go` moved a DATA frame from the bounded receive queue into `readBuf` and immediately returned credit for the complete frame. When the caller supplied a smaller buffer, bytes remained in `readBuf` even though their window credit had already been returned.

The Server consumes that credit before enqueuing DATA, and its per-stream `FairFrameWriter` queue is sized to the Client's advertised 256 KiB window. If the WebSocket sender is slow, the queue can still contain the original window when the early Client update grants additional credit. The Server then tries to enqueue another frame, receives `ErrStreamQueueFull`, removes the stream, and emits RESET. `forwardHTTP` currently ignores the body-copy error, so Chrome receives a 200 response with fewer bytes than `Content-Length` and reports `ERR_CONTENT_LENGTH_MISMATCH`.

This violated the invariant documented in `docs/superpowers/plans/2026-09-23-managed-route-response-truncation.md`: a peer may return receive credit only after the consumer has actually read the bytes.

## 3. Architecture decision

Keep the existing protocol, window size, update threshold, and queue capacities. Change only the credit accounting point: `Read` releases exactly the number of bytes copied into the caller's buffer. This preserves backpressure from the local application through the Client receive queue, the Server send queue, and the Agent relay.

## 4. Technology and specification references

- Go standard library `io.Reader` semantics.
- Flow-control invariant in `internal/protocol/window.go`.
- Prior incident record: `docs/superpowers/plans/2026-09-23-managed-route-response-truncation.md`.
- Prior Client fix: `docs/superpowers/plans/2026-09-24-client-forward-delayed-response.md`.

## 5. Global constraints

- No API, schema, protocol frame, or configuration change.
- No window-size or queue-capacity adjustment; the existing values must remain consistent.
- No new goroutine, lock, timer, or retry policy.
- Terminal frames and already queued DATA must still be drained before EOF is reported.

## 6. Files and interfaces

- `internal/client/session.go`: move `releaseReceiveWindow` from queue-drain time to successful application-read time and document why.
- `internal/client/session_test.go`: add `TestSessionFlowControlWaitsForApplicationReadBeforeWindowUpdate`.
- `docs/superpowers/plans/2026-09-24-client-receive-window-credit.md`: this retroactive plan.
- `docs/pull-requests/2026-09-24-client-receive-window-credit.md`: merge, verification, release, and rollback record.
- `docs/superpowers/plans/README.md` and `docs/pull-requests/README.md`: regenerated indexes.

The only internal interface change is that `releaseReceiveWindow` now receives the byte count copied to the caller, rather than the full payload length moved into `readBuf`.

## 7. TDD evidence

1. Red: `go test ./internal/client -run '^TestSessionFlowControlWaitsForApplicationReadBeforeWindowUpdate$' -count=1` failed with `WINDOW_UPDATE before application consumed bytes: {Type:5 StreamID:10 Window:131072}`.
2. Minimal fix: remove the three early release calls and release `n` bytes after copying from `readBuf` to the caller.
3. Green: the focused command passed after the fix.

## 8. Verification

- `go test ./internal/client -run 'TestSessionFlowControl' -count=1`
- `go test ./internal/client -count=1`
- `go test -race ./internal/client -count=1`
- `go vet ./internal/client`
- `go test ./... -count=1`
- `go test -race ./...`
- `go vet ./...`
- `git diff --check`

All commands passed. No frontend source or embedded assets changed, so the frontend gates were not required.

## 9. Rollback

Revert the Client commit and redeploy the previous Client binary. Server, Agent, database, protocol state, and configuration require no rollback action.
