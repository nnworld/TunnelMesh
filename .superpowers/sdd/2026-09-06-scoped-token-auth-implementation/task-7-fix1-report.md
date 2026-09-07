# Task 7 Fix Round 1 Report

## Status

DONE_WITH_CONCERNS

Commits: none (未执行 commit/push/merge)。

## Review findings fixed

### P1 — relay Serve failure was silently discarded

Added a relay error channel in `ServerRuntime.ServeListener`. The relay gRPC
serve goroutine now reports non-shutdown errors, treats `grpc.ErrServerStopped`
as expected shutdown, reports an unexpected nil return as a runtime failure,
and shuts down HTTP before returning the relay failure. Context cancellation
and HTTP-error paths stop the relay server during runtime shutdown.

Regression test `TestServeListenerReportsRelayServeFailure` closes the relay
listener before serving and verifies `ServeListener` returns a non-nil error
instead of continuing indefinitely with management traffic only.

### P2 — endpoint-only relay config passed validation but could not start

`config.Validate` now requires `server.relay.listen` whenever relay is enabled.
The endpoint remains available for the authenticated outbound client path, but
an enabled server runtime cannot pass check-config without an inbound listener.

Regression coverage extends
`TestValidateRelayRequiresClusterIdentityTokenAndAbsoluteTLSMaterial` to assert
that endpoint-only configuration is rejected with a listen requirement.

## TDD evidence

RED tests before implementation:

- `go test ./internal/server -run TestServeListenerReportsRelayServeFailure -count=1` failed because `ServeListener` did not report relay failure.
- `go test ./internal/config -run TestValidateRelayRequiresClusterIdentityTokenAndAbsoluteTLSMaterial -count=1` failed because endpoint-only validation returned nil.

After the minimal fixes, both regression tests passed.

## Files changed

- `internal/server/runtime.go`
- `internal/server/runtime_test.go`
- `internal/config/config.go`
- `internal/config/config_test.go`

## Verification

- Focused server/config regression tests: PASS.
- `go test ./internal/server ./internal/config ./internal/relay ./internal/cli -count=1`: PASS.
- `go test ./... -count=1`: PASS.
- `go test -race ./internal/server ./internal/config ./internal/relay ./internal/cli -count=1`: PASS.
- `go vet ./...`: PASS.
- `git diff --check`: PASS.

An initial `go test -race ./...` invocation was interrupted by the parent task;
the affected-package race run above completed successfully after the fixes.

## Concerns

- Full post-fix repository race was not rerun to completion after the parent
  interrupted its first invocation; affected packages were rerun with race and
  passed. The prior full race run before this fix had passed.
- Relay `endpoint` remains an outbound dialing value; enabled server runtime
  intentionally requires `listen` to avoid a check-config/startup mismatch.
