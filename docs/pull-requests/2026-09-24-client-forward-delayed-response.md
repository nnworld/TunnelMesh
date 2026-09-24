# Client local-forward delayed response

## Title

`fix(client): keep delayed responses open after half-close`

## Target branch

`main`

## Summary

Local TCP forwarding no longer closes the response direction one second after the request direction reaches EOF. The bridge now follows TCP half-close semantics and waits for the remote response to finish or an endpoint to be closed.

## User impact

Large JavaScript assets, downloads, and other responses delivered through `tunnelmesh-client forward tcp` complete even when they take longer than the request. This addresses the reported `ERR_CONTENT_LENGTH_MISMATCH` on `127.0.0.1:18080`.

## API, schema, and configuration impact

None. No API, database schema, protocol frame, or configuration option changes.

## Security and authorization impact

None. Existing stream authorization, policy validation, and endpoint ownership are unchanged.

## Test evidence

- Red: `go test ./internal/client -run '^TestBridgeWaitsForDelayedResponseAfterLocalHalfClose$' -count=1` failed with `received 0 bytes, want 524288`.
- Green after the fix: the focused test passed.
- `go test ./internal/client -count=1` passed.
- `go test -race ./internal/client -count=1` passed.
- `go test ./... -count=1` passed across all packages.
- `go test -race ./...` passed across all packages.
- `go vet ./internal/client` passed.
- `go vet ./...` passed.
- `git diff --check` passed.

## Release steps

1. Merge after review and CI.
2. Build and deploy the updated `tunnelmesh-client`.
3. Restart local forwarding sessions; no Server or Agent restart is required.

## Rollback steps

Revert the merge commit and redeploy the previous Client binary. No persisted state or configuration migration is involved.

## Reviewer focus

- Confirm the unbounded wait occurs only after a successful half-close.
- Confirm error and no-half-close cleanup paths still close promptly.
- Confirm test helpers return real write lengths so `io.CopyBuffer` does not report a short write.

## Integration status

Implementation and focused verification are complete. Merge-request creation and merge are pending user authorization.
