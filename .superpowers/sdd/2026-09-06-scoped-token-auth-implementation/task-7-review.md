# Task 7 Independent Review

## Result

**FAIL**

The mTLS + `server_node` Token authentication path itself is well structured: the server interceptor runs before the relay handler, requires a verified client certificate, performs exact SAN matching without wildcard/CN fallback, validates the Token type and node binding, checks the authoritative node epoch, and keeps caller identity separate from `StreamRequest.NodeID`. Raw node Tokens are omitted from config JSON and are not included in authentication errors.

## Findings

### [P1] Relay runtime failures are silently discarded

`internal/server/runtime.go:312-314` launches `grpc.Server.Serve` in a goroutine and ignores its returned error. If the relay listener fails after HTTP startup (accept failure, listener failure, or another runtime error), the process continues serving management/WS traffic and appears healthy while all inter-node relay traffic is unavailable. This is a fail-open availability/degradation path: the relay failure must be surfaced through the runtime error channel/readiness state (while treating the expected `grpc.ErrServerStopped` shutdown case separately).

Confidence: 92/100.

### [P2] Validation accepts endpoint-only relay config that runtime cannot start

`internal/config/config.go:526-528` declares either `server.relay.listen` or `server.relay.endpoint` sufficient, but `internal/server/runtime.go:79-92` unconditionally creates the inbound listener from `Relay.Listen` whenever relay is enabled. Therefore an endpoint-only configuration passes `check-config` and then fails during `server run` with an empty listen address. Either require `listen` for the current runtime model, or make listener creation conditional and explicitly support an outbound-only mode.

Confidence: 100/100.

## Verification

- `go test ./internal/relay -run 'AuthenticatedGRPCRelay|ServerNode' -count=1` — PASS
- `go test ./internal/server ./internal/config ./internal/cli -count=1` — PASS
- `go test -race ./internal/relay -count=1` — PASS
- `go vet ./internal/relay ./internal/server ./internal/config ./internal/cli` — PASS

Residual coverage gap: the focused tests do not exercise relay `Serve` failure propagation or the endpoint-only configuration accepted by `check-config`.
