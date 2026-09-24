# Client local-forward delayed response fix plan

Status: retroactive emergency plan (implementation completed)

The user reported that a browser loading a large JavaScript asset through `tunnelmesh-client forward tcp` received `ERR_CONTENT_LENGTH_MISMATCH` even after the Client update from the managed-route flow-control fix. The incident was production-impacting, so the fix followed the emergency path: reproduce first, implement the minimal correction, then record this plan and rerun verification.

## 1. Goal

Keep a local TCP forward open after the browser request reaches EOF until the remote response reaches EOF or an endpoint is explicitly closed. A large response that takes longer than the request must arrive byte-for-byte.

## 2. Non-goals

- Do not change protocol frames, window sizes, or configuration.
- Do not alter Server or Agent behavior; PR #19 already fixed their flow-control queues and credit waits.
- Do not add a new idle timeout. Endpoint closure remains the cancellation mechanism.

## 3. Root cause

`bridge` in `internal/client/forward.go` runs two independent copy loops. For an HTTP asset request, the local-to-remote direction reaches EOF almost immediately and sends a directional half-close. The old bridge then waited at most one second for the remote-to-remote response direction. A several-hundred-KiB asset can take longer than one second under normal flow-control and network backpressure. When the timer fired, the bridge closed the local connection before the response EOF, so Chrome reported a 200 response with a mismatched `Content-Length`.

## 4. Architecture decision

Preserve TCP half-close semantics. A successful half-close ends only one direction; the remaining direction belongs to the peer and has no wall-clock deadline. The bridge waits for that direction's natural completion. Callers can still cancel by closing either endpoint, and error/no-half-close paths retain their existing bounded cleanup.

## 5. Technology and specification references

- Go standard library `io.CopyBuffer` and directional `CloseWrite`.
- User path: local TCP forwarding in `docs/user-guide/client.md`.
- Prior related fix: `docs/superpowers/plans/2026-09-23-managed-route-response-truncation.md`.

## 6. Global constraints

- No schema, API, protocol, or configuration change.
- No new goroutine, lock, timer, or policy layer.
- The fix must not turn a peer that never closes into an unbounded resource leak; external endpoint closure must remain the cancellation boundary.

## 7. Files and interfaces

- `internal/client/forward_test.go`: add a delayed remote response and a local endpoint that exposes request EOF and response delivery.
- `internal/client/forward.go`: remove only the one-second wait after a successful first-direction half-close.
- `docs/user-guide/client.md`: document that the request half-close does not terminate the response direction.
- `docs/pull-requests/2026-09-24-client-forward-delayed-response.md`: record merge and rollout evidence.

## 8. TDD evidence

1. Red: `go test ./internal/client -run '^TestBridgeWaitsForDelayedResponseAfterLocalHalfClose$' -count=1` failed with `received 0 bytes, want 524288`.
2. Minimal fix: replace the second-result timer with an unbounded channel receive.
3. Green: the same command passed after the fix.

## 9. Verification

- `go test ./internal/client -count=1`
- `go test -race ./internal/client -count=1`
- `go vet ./internal/client`
- `git diff --check`

Full-repository gates required before the merge request are recorded in the PR note.

## 10. Rollback

Revert the Client commit and redeploy the previous Client binary. Server, Agent, database, configuration, and protocol state are unchanged.
