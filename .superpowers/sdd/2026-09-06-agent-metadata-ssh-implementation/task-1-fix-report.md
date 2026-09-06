# Task 1 Fix Report: Bounded Environment Metadata Reads

## Fix

`readMetadataSource` now checks the environment string’s byte length before converting it to `[]byte`. Oversized values return the existing field-limit error without allocating a value copy; normal values retain the prior UTF-8 and newline behavior.

## Regression evidence

- Added `TestOversizedEnvironmentValueIsRejectedBeforeCopy` using `testing.AllocsPerRun`.
- The test failed before the fix with 3 allocations per oversized read and passes after the fix with 0 allocations.
- `go test ./internal/agent ./internal/config -count=1`
- `go test ./internal/agent -run Metadata -count=1 -v`
- `go test ./... -count=1`
- `go test -race ./internal/agent ./internal/config`
- `go vet ./...`
- `git diff --check`

All verification commands passed after the fix. An existing duplicate-stream test was observed as timing-sensitive once during the full run and passed on immediate rerun; it is unrelated to this change.
