# Task 3 Report — runtime instrumentation

Implemented runtime instrumentation against the injected `observability.Metrics`
collector. Agent and Client WebSocket paths now record connection outcomes,
upgrade/auth stages, heartbeat PONGs, logical stream outcomes, and bounded byte
counts. Server Agent/Client handlers record registration/connection lifecycle,
Agent hello and token-auth stages. Relay stream authentication records bounded
success/failure outcomes. Storage exposes an `OpenWithMetrics` entry point and
records bounded probe/stage outcomes for `Ping`.

Added `internal/observability/integration_test.go`; its first run was RED due
to the intentionally missing instrumentation API, then passed after the
minimal wiring was added. Labels use only fixed component/mode/stage/result,
protocol, direction, and normalized error-class values; no token, target,
connection ID, stream ID, or metadata values are exported.

Verification completed:

- `go test ./... -count=1`
- `go test -race ./...` (started; package-level coverage passed, Server race suite exceeded local timeout)
- `go vet ./...`
- `git diff --check`

No commit, push, merge, or destructive cleanup was performed.

The full race command was started; the Agent/Client/Storage/Observability
packages passed. The Server package's existing race suite exceeded the local
execution window (Relay completed) and was interrupted after the timeout;
ordinary full tests and focused Server tests pass.
