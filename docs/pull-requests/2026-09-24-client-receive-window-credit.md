# Client receive-window credit timing fix

## Title

`fix(client): release receive credit after reads`

## Target branch

`main`

## Summary

The Client now returns stream receive-window credit only after the application consumes bytes. Previously, credit was returned when a frame moved from the receive queue into an internal buffer, which could over-credit a slow Server sender and fill its per-stream queue.

## User impact

Large JavaScript assets and other bulk responses delivered through `tunnelmesh-client forward tcp` no longer receive premature RESETs caused by over-credit. This addresses the intermittent `ERR_CONTENT_LENGTH_MISMATCH` observed on `127.0.0.1:18080`.

## API, schema, and configuration impact

None. No API, database schema, protocol frame, window size, or configuration option changes.

## Security and authorization impact

None. Existing stream authorization, policy validation, target restrictions, and audit behavior are unchanged.

## Test evidence

- Red: `go test ./internal/client -run '^TestSessionFlowControlWaitsForApplicationReadBeforeWindowUpdate$' -count=1` failed with a premature `WINDOW_UPDATE` of 131072 bytes.
- Green: the focused test passed after moving credit release to application-read time.
- `go test ./internal/client -run 'TestSessionFlowControl' -count=1` passed.
- `go test ./internal/client -count=1` passed.
- `go test -race ./internal/client -count=1` passed.
- `go vet ./internal/client` passed.
- `go test ./... -count=1` passed.
- `go test -race ./...` passed.
- `go vet ./...` passed.
- `git diff --check` passed.

## Release steps

1. Merge after review and CI.
2. Build and deploy the updated `tunnelmesh-client`.
3. Restart local forwarding sessions. Server and Agent restarts are not required.

## Rollback steps

Revert the merge commit and redeploy the previous Client binary. No persisted state or configuration migration is involved.

## Reviewer focus

- Confirm credit is released for exactly the bytes copied to the caller.
- Confirm DATA queued before HALF_CLOSE is still drained before EOF.
- Confirm the window size remains 262144 and no queue capacity is changed.

## Integration status

Implementation and verification are complete. This note accompanies the merge request.
