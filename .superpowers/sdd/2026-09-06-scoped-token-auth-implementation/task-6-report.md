# Task 6 Implementation Report

## Status

**DONE_WITH_CONCERNS**

Authenticated Client WebSocket, per-OPEN authorization, Client reconnect/dialer, shared OPEN protocol payload, and Client CLI/config wiring are implemented. No commit, push, or merge was performed.

## Design and Responsibilities

- `internal/protocol` owns the single `StreamOpenPayload` JSON wire model and bounded encode/decode helpers. The previous Agent, Client, and Server declarations are aliases, so there is no duplicate schema to drift.
- Client HTTP upgrade authentication is isolated from Agent authentication with its own context key and `ClientSessionPrincipal`. The principal contains only a random 128-bit process-unique `ConnectionID` and non-secret `auth.TokenIdentity`.
- `/ws/client` is mounted as an exact route beside `/ws/agent`. It reuses the strict Host/Origin checks and extracts credentials only from `Authorization: Bearer`; query and cookie credentials do not authenticate. The Authorization header is removed before the WebSocket handler runs.
- `CredentialStreamAuthorizer` adapts `CredentialService.AuthorizeStream`. Every OPEN invokes it, which re-reads current token lifecycle/scope and Agent Policy state from storage.
- `ServeClientSession` owns the Client-side server stream state: OPEN/DATA/HALF_CLOSE/RESET, duplicate and unknown IDs, connection PING/PONG, EOF cleanup, relay-to-client copying, and fixed bounded RESETs. Authorized streams use the injected `relay.NodeTransport.OpenStream`; a missing transport fails closed.
- Client `RunWebSocket` validates absolute `ws`/`wss` URLs, uses normal TLS verification, sends the raw token only in Authorization, starts the shared `client.Session`, answers PING with PONG, reconnects with bounded exponential backoff plus jitter, calls `onReady` for each established session, and closes promptly on context cancellation.
- `client.token` is available from file/environment/Cobra with CLI > env > file > default precedence. JSON/YAML tags omit it, and `RedactedJSON` cannot serialize it.
- TCP/UDP/HTTP forward and TCP stdio proxy subcommands now use the authenticated Client WebSocket session through `SessionOpener`. UDP continues to use `OpenDatagram`, preserving one `FrameData` per datagram.

## TDD RED Evidence

### Client WebSocket, Server WebSocket, and stream authorization

Command:

```text
go test ./internal/server ./internal/client -run 'ClientWS|StreamAuthorizer' -count=1
```

Expected RED reason:

- Client package lacked `RunWebSocket`, `RunWebSocketWithOptions`, `DialWebSocket`, URL/token errors, and reconnect options.
- Server package lacked `ClientSessionPrincipal`, `StreamAuthorizer`, `NewCredentialStreamAuthorizer`, `ServeClientSession`, runtime Client transport wiring, and `/ws/client` support.
- Protocol package lacked shared `StreamOpenPayload` encode/decode APIs.

The compile failed on those exact missing symbols; it was not a fixture or syntax failure.

### Client token configuration and CLI proxy

Command:

```text
go test ./internal/config ./internal/cli -run 'ClientToken|ClientProxy|ClientRoots' -count=1
```

Expected RED reason:

- `config.ClientConfig` had no `Token` field.
- CLI had no authenticated Client runner injection point and no real proxy session path.

### TCP/UDP/HTTP forward command wiring

Command:

```text
go test ./internal/cli -run 'ClientForwardCommands' -count=1
```

Expected RED result:

```text
authenticated Client WebSocket runner was not called
```

The assertion failed independently for TCP, UDP, and HTTP while the commands still used their output-only stubs.

### HTTP forward immediate-close race

Command:

```text
go test ./internal/client -run 'HTTPForwardCanCloseImmediately' -count=1
```

Expected RED result: stable nil-pointer panic in `HTTPForward.Start.func1`; the goroutine dereferenced `f.server` after `Close` set it to nil. The fix captures the immutable local `server` before starting the goroutine.

### Directional half-close

Command:

```text
go test ./internal/client -run 'SessionRemoteHalfCloseKeepsLocalWriteSideOpen' -count=1
```

Expected RED result:

```text
Write() after remote half-close error = EOF
```

The fix keeps the logical stream registered and treats remote EOF as read-side-only, allowing the local write side to finish.

### Relay short write

Command:

```text
go test ./internal/server -run 'ShortRelayWrite' -count=1
```

Expected RED result:

```text
RESET count = 0, want 1 after partial relay write
```

The fix converts a nil-error short write to `io.ErrShortWrite`, closes the stream, and returns the same bounded RESET used by other stream failures.

## GREEN and Verification Evidence

Focused GREEN:

```text
go test ./internal/server ./internal/client -run 'ClientWS|StreamAuthorizer' -count=1
ok github.com/tunnelmesh/tunnelmesh/internal/server 0.719s
ok github.com/tunnelmesh/tunnelmesh/internal/client 0.278s
```

Affected packages:

```text
go test ./internal/server ./internal/client ./internal/cli -count=1
ok github.com/tunnelmesh/tunnelmesh/internal/server 8.720s
ok github.com/tunnelmesh/tunnelmesh/internal/client 0.264s
ok github.com/tunnelmesh/tunnelmesh/internal/cli 0.591s
```

E2E selector:

```text
go test ./internal/e2e -run 'Client|Forward|SSH' -count=1
ok github.com/tunnelmesh/tunnelmesh/internal/e2e 0.327s
```

`go test -list` confirmed this selector ran five SSH E2E tests: byte integrity/exit status, policy denial, reconnect cleanup, bridge disconnect cleanup, and loopback-policy rejection. There are no E2E test names containing `Client` or `Forward`; this is recorded under Concerns rather than claiming dedicated Client-forward E2E coverage.

Affected race:

```text
go test -race ./internal/server ./internal/client -count=1
ok github.com/tunnelmesh/tunnelmesh/internal/server 79.216s
ok github.com/tunnelmesh/tunnelmesh/internal/client 1.393s
```

Full repository:

```text
go test ./... -count=1
PASS (all packages; server 10.737s)
```

Full repository race:

```text
go test -race ./... -count=1
PASS (all packages; server 83.433s, storage 2.570s)
```

Static verification:

```text
go vet ./...
PASS

git diff --check
PASS
```

## Changed Files

- `internal/protocol/stream_open.go`
- `internal/protocol/stream_open_test.go`
- `internal/server/stream_authorizer.go`
- `internal/server/stream_authorizer_test.go`
- `internal/server/ws_client.go`
- `internal/server/ws_client_test.go`
- `internal/server/runtime.go`
- `internal/server/middleware.go`
- `internal/server/web.go`
- `internal/server/session_manager.go`
- `internal/client/websocket.go`
- `internal/client/websocket_test.go`
- `internal/client/session.go`
- `internal/client/forward.go`
- `internal/client/forward_test.go`
- `internal/config/config.go`
- `internal/config/config_test.go`
- `internal/cli/root.go`
- `internal/cli/root_test.go`
- `internal/cli/client_runtime_test.go`
- `internal/agent/session.go`

The protocol file and Agent alias are intentionally outside the brief's original file list to satisfy the ruling that `internal/protocol.StreamOpenPayload` is the single shared model. `middleware.go` and `web.go` are the minimal route/pre-auth integration points. `forward.go` contains only the regression fix discovered while exercising the new CLI HTTP forward lifecycle.

## Self-review

- Confirmed only one concrete `StreamOpenPayload` declaration remains; Client, Server, and Agent use aliases.
- Confirmed raw Client tokens appear only in config/dial/handshake inputs and are not copied into Client session, Server session manager, principal, logs, frames, or RESET messages.
- Confirmed Client and Agent use distinct context keys/types.
- Confirmed all OPEN branches authorize before relay open, including subsequent OPENs on an already authenticated connection.
- Confirmed storage/authorization failures collapse to the fixed `stream rejected` payload (15 bytes), with no underlying error text exposed.
- Confirmed nil authorizer/transport, invalid payload, denied scope/policy, duplicate ID, unknown stream, relay open failure, and relay short write all fail closed.
- Confirmed `WSFrameTransport` and the Client WebSocket transport serialize concurrent sends.
- Confirmed remote half-close does not incorrectly close the opposite write direction and EOF/reset cleanup closes relay streams.
- Confirmed Cobra tests exercise authenticated proxy frames and all three forward constructors without public-network dependencies.
- Confirmed no commit, push, merge, destructive Git action, or secret-bearing output was produced.

## Concerns

1. The required E2E selector currently matches five SSH tests but no test named `Client` or `Forward`. Client WebSocket and CLI forwarding are covered by package-level real frame/HTTP/WebSocket tests, but the repository still lacks a dedicated process-level Client-to-Server-to-Agent forward E2E test.
2. `ServerRuntime.ClientTransport` is deliberately nil unless the embedding runtime injects the existing local/cluster `relay.NodeTransport`. In that state `/ws/client` authenticates but every OPEN receives a bounded RESET, as required by the fail-closed ruling. Deployment wiring must supply the transport for live forwarding.

## Commits

none

---

# Fix Round 1/5

## Status

**DONE_WITH_CONCERNS**

All four controller findings are fixed with real RED-to-GREEN coverage. The production `ServerRuntime` now has a functional default local Client-to-Agent transport; the previous report concern that `ClientTransport` remained nil is resolved. No commit, push, merge, or destructive Git action was performed.

## Design Changes

- Added `AgentRelayTransport`, the runtime-owned local `relay.NodeTransport` implementation. It allocates non-zero Agent wire stream IDs independently from Client stream IDs, maps Agent frames back to the originating relay stream, bounds payload/queue admission, supports directional `CloseWrite`, and fences every stream by Agent ID plus epoch.
- `NewServerRuntime` creates one `AgentRelayTransport` and installs it as both `ClientTransport` and `LocalAgentRelay`. The Agent WebSocket receive loop routes non-metadata stream frames into this mux, and Agent session teardown fails only streams from the disconnected generation.
- Added `AgentSessionManager.SendGeneration`, which keeps the manager/session read locks through epoch validation and queue admission so a reconnect cannot replace the generation between those operations.
- Inbound Agent frame dispatch now holds the current manager/session generation stable through stream admission. This closes the replacement-registration window in which a frame already read by an old WebSocket could otherwise reach an old relay stream after a newer epoch became current.
- Agent relay EOF is directional: remote `HALF_CLOSE` ends reads while writes remain allowed. A later `RESET` or generation failure upgrades EOF to a terminal error, and closing a stream already terminated remotely no longer echoes a redundant RESET.
- Client stream IDs now have connection-lifetime tombstones. A repeated OPEN is rejected even after RESET or complete bidirectional half-close, and relay readers verify the exact stream instance before forwarding data so an old reader cannot leak late bytes.
- Client `HALF_CLOSE` requires an underlying `CloseWrite`. Unsupported or failed directional close produces the fixed bounded RESET and closes the relay stream. Relay `io.EOF` maps to `HALF_CLOSE`; every other read error maps to the fixed `stream rejected` RESET.
- `grpcStreamConn` now exposes `CloseWrite`, with `Close` delegating to it, so the gRPC relay adapter satisfies the same directional close contract.
- UDP relay reads use `protocol.MaxPayload`, preserving a large datagram as one Client DATA frame instead of splitting it at the TCP-oriented 32 KiB buffer.
- Client reconnect jitter is clamped again after jitter is applied, so the returned delay never exceeds `MaxBackoff`.
- Stabilized the existing Agent duplicate-stream test fixture by keeping the first fake stream alive until the duplicate assertion. The previous fake returned EOF immediately, allowing its background reader to delete the stream before the second OPEN and making the test scheduler-dependent; production behavior was not changed for this fixture correction.

## TDD RED Evidence

### Finding 1: production Runtime had no local Client-to-Agent transport

Command:

```text
go test ./internal/server -run 'TestClientWSRuntimeDefaultTransportBridgesRegisteredAgentBidirectionally' -count=1
```

RED result: the test used a real `NewServerRuntime` plus real `/ws/agent` and `/ws/client` WebSockets, but timed out waiting for the Agent OPEN because the production runtime had no default transport connecting authorized Client streams to the registered Agent session.

### Finding 2: stream IDs could be reused and old readers could outlive the logical stream

Commands exercised:

```text
go test ./internal/server -run 'TestServeClientSessionNeverReusesStreamIDOrForwardsLateOldData' -count=1
go test ./internal/server -run 'TestServeClientSessionNeverReusesStreamIDAfterCompleteHalfClose' -count=1
```

RED results: after RESET, the reused ID opened a second relay stream and no rejection RESET arrived; after complete bidirectional half-close, the same ID was also reusable. The first scenario additionally demonstrated that an old reader required exact-instance fencing to prevent late data from reaching a later logical stream.

### Finding 3: HALF_CLOSE support and relay read-error mapping were incomplete

Commands:

```text
go test ./internal/server -run 'TestServeClientSessionResetsHalfCloseWithoutDirectionalRelaySupport' -count=1
go test ./internal/server -run 'TestServeClientSessionMapsNonEOFRelayReadErrorToReset' -count=1
go test ./internal/relay -run 'TestGRPCStreamConnExposesDirectionalCloseWrite' -count=1
```

RED results:

- A Client HALF_CLOSE against a relay without `CloseWrite` timed out waiting for the required RESET.
- A non-EOF relay read error incorrectly produced HALF_CLOSE instead of RESET.
- The gRPC test failed with `grpcStreamConn does not expose CloseWrite`.

### Finding 4: reconnect jitter exceeded the configured maximum

Command:

```text
go test ./internal/client -run 'TestClientWSReconnectJitterNeverExceedsMaxBackoff' -count=1
```

RED result: reconnect delay was `110ms` with `MaxBackoff=100ms` because the pre-jitter value was clamped but the jittered result was not.

### Additional UDP boundary regression

Command:

```text
go test ./internal/server -run 'TestServeClientSessionPreservesLargeUDPFrameBoundary' -count=1
```

RED result: a 64 KiB relay datagram reached the Client as a 32 KiB DATA frame, violating UDP message-boundary preservation.

### Additional local relay state/fencing self-review

Commands and RED results:

```text
go test ./internal/server -run 'AgentRelayTransportResetOverridesPriorRemoteHalfClose' -count=1
Write() after RESET error = <nil>, want ErrStreamReset

go test ./internal/server -run 'AgentRelayTransportRejectsOldGenerationDataAfterReplacementRegistration' -count=1
Read() after replacement = (10, <nil>), payload "stale-data"; want (0, ErrNodeDisconnected)

go test ./internal/server -run 'AgentRelayTransportCloseDoesNotEchoRemoteReset' -count=1
Close() echoed terminal Agent frame: Type=RESET
```

After adding the inbound generation fence, the existing real runtime integration test exposed a lock re-entry deadlock during completed bidirectional half-close. A Go SIGQUIT stack showed `HandleAgentFrame -> remoteHalfClose -> detach` trying to reacquire `AgentRelayTransport.mu`. The minimal fix made `remoteHalfClose` return completion to its already-locked caller, which removes the mapping directly. The same runtime test then passed under an explicit 30-second timeout.

### Existing Agent test-fixture race found by the full gate

Command:

```text
go test ./... -count=1
```

RED result:

```text
TestStreamDispatcherRejectsDuplicateStreamID: err=<nil>
```

Root cause: the first fake connection returned EOF immediately, so its background reader could remove the stream before the duplicate OPEN. A focused `-count=20` run demonstrated the scheduler dependence. The fixture now blocks the first read until after the duplicate assertion; a `-count=100` focused run passed. No Agent production behavior changed for this stabilization.

## GREEN and Verification Evidence

Focused Client/authorization regression:

```text
go test ./internal/server ./internal/client -run 'ClientWS|StreamAuthorizer' -count=1
ok github.com/tunnelmesh/tunnelmesh/internal/server 0.764s
ok github.com/tunnelmesh/tunnelmesh/internal/client 0.275s
```

Local relay focused regression, including the real runtime bridge and self-review cases:

```text
go test ./internal/server -run 'ClientWSRuntimeDefaultTransportBridgesRegisteredAgentBidirectionally|AgentRelayTransport' -count=1 -timeout=30s
ok github.com/tunnelmesh/tunnelmesh/internal/server 0.627s
```

Affected packages:

```text
go test ./internal/server ./internal/client ./internal/cli ./internal/relay -count=1
ok github.com/tunnelmesh/tunnelmesh/internal/server 8.898s
ok github.com/tunnelmesh/tunnelmesh/internal/client 0.259s
ok github.com/tunnelmesh/tunnelmesh/internal/cli 0.924s
ok github.com/tunnelmesh/tunnelmesh/internal/relay 0.639s
```

E2E selector:

```text
go test ./internal/e2e -run 'Client|Forward|SSH' -count=1
ok github.com/tunnelmesh/tunnelmesh/internal/e2e 0.316s
```

Affected race:

```text
go test -race ./internal/server ./internal/client ./internal/relay -count=1
ok github.com/tunnelmesh/tunnelmesh/internal/server 79.537s
ok github.com/tunnelmesh/tunnelmesh/internal/client 1.691s
ok github.com/tunnelmesh/tunnelmesh/internal/relay 1.348s
```

Agent flaky-fixture stability:

```text
go test ./internal/agent -run 'TestStreamDispatcherRejectsDuplicateStreamID' -count=100
ok github.com/tunnelmesh/tunnelmesh/internal/agent 0.480s
```

Full repository:

```text
go test ./... -count=1
PASS (all packages; server 9.627s, storage 1.379s)
```

Full repository race:

```text
go test -race ./... -count=1
PASS (all packages; server 79.614s, storage 2.490s)
```

Static verification:

```text
go vet ./...
PASS

git diff --check
PASS
```

## Fix-round Changed Files

- `internal/server/agent_relay_transport.go` (new)
- `internal/server/agent_relay_transport_test.go` (new)
- `internal/server/runtime.go`
- `internal/server/session_manager.go`
- `internal/server/ws_client.go`
- `internal/server/ws_client_test.go`
- `internal/client/websocket.go`
- `internal/client/websocket_test.go`
- `internal/relay/transport.go`
- `internal/relay/transport_closewrite_test.go` (new)
- `internal/agent/session_test.go` (test-fixture stabilization only)
- `.superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-report.md`

## Self-review

- Confirmed `NewServerRuntime` owns exactly one local Agent relay and installs it into the production Client path; the real WebSocket integration verifies OPEN, DATA in both directions, and HALF_CLOSE in both directions.
- Confirmed Agent wire IDs are non-zero and independent from Client IDs, and map entries are removed only when the exact stream instance/generation matches.
- Confirmed outbound admission is fenced by `SendGeneration`; inbound delivery is fenced while the current manager/session generation is held stable. Old generation data is ignored after replacement registration even before deferred old-session cleanup runs.
- Confirmed lock ordering is manager -> Agent session -> local relay -> relay stream where multiple locks are needed. The runtime half-close test specifically guards the lock re-entry deadlock found during this review.
- Confirmed remote EOF remains directional, RESET/generation failure upgrades EOF to a terminal error, and closing an already terminal stream does not emit a redundant RESET.
- Confirmed Agent DATA payloads are copied before queueing, outbound payloads are bounded by `protocol.MaxPayload`, and read-queue saturation fails the stream with bounded backpressure behavior.
- Confirmed Client stream IDs remain tombstoned for the authenticated connection lifetime, and late readers cannot forward unless their exact stream object is still current.
- Confirmed unsupported/failed `CloseWrite`, non-EOF relay read errors, relay send failures, and short writes close/detach the stream and expose only the fixed bounded `stream rejected` RESET.
- Confirmed UDP relay reads preserve one large datagram per DATA frame and TCP retains the smaller streaming buffer.
- Confirmed reconnect jitter is clamped after jitter and gRPC streams implement directional close.
- Confirmed no raw credentials, internal relay errors, or sensitive details are placed into frames/logs by these changes.
- Confirmed no commit, push, merge, or destructive Git action was performed.

## Concerns

1. The required E2E selector still matches the existing five SSH E2E tests and no test named `Client` or `Forward`. The new real in-process Runtime WebSocket integration covers Client-to-Server-to-Agent transport, but a dedicated process-level Client/forward E2E remains Task 9.
2. This round implements the local/default Agent relay only. Cross-node relay selection and mTLS credentials remain Task 7 and were intentionally not pulled into Task 6.
3. Connection-lifetime stream-ID tombstones consume memory proportional to unique OPEN IDs on a single long-lived authenticated Client connection. This is required to prevent ID reuse; a future connection-level stream-attempt budget or forced reconnect policy may be appropriate if abuse testing shows the set can grow materially.

## Commits

none
---

# Fix Round 2/5

## Status

**DONE_WITH_CONCERNS**

All controller findings for this round are implemented with real RED-to-GREEN coverage. Production `tunnelmesh-agent run` now creates a per-WebSocket stream dispatcher, forwards TCP/UDP/HTTP frames, closes the dispatcher before reconnecting, and serializes dispatcher sends through `Session.Send`. Client OPEN attempts and Agent relay wire IDs are bounded without ID reuse. No commit, push, merge, or destructive Git action was performed.

## Design Changes

- Added a connection-lifetime Client OPEN-attempt cap of 65,536 unique non-zero stream IDs. Duplicate IDs do not consume the budget again; the 65,537th unique ID is not inserted, receives one fixed bounded `GOAWAY` (`open limit reached`), and terminates the connection.
- Replaced the Agent relay's process-wide wrapping `uint32` allocator with an Agent-ID-plus-epoch allocator backed by `uint64`. IDs 1 through `math.MaxUint32` are admitted once per epoch, exhaustion fails closed without reuse, and a new authenticated epoch starts again at ID 1.
- Keyed Agent relay streams by Agent ID, epoch, and wire ID so stale frames cannot collide with replacement-generation streams. Generation teardown removes both streams and that generation's allocator.
- Agent relay DATA after remote `HALF_CLOSE` now fails the stream with `protocol.ErrInvalidFrame`, detaches it, and sends a terminal RESET to the current Agent generation.
- Reworked `StreamDispatcher` into a directional per-stream state machine. Inbound HALF_CLOSE uses the target's `CloseWrite`, keeps the read side alive, and closes fully only after both directions complete. Unsupported/failed `CloseWrite`, non-EOF target reads, target write errors, target short writes, and DATA after inbound HALF_CLOSE all close only that stream and send the fixed bounded RESET.
- UDP target reads use `protocol.MaxPayload`, retaining one logical datagram per DATA frame; TCP/HTTP keep the 32 KiB streaming buffer.
- Dispatcher shutdown rejects new OPENs, cancels terminal sends, closes all target connections, and waits for every target reader goroutine to exit before reconnect can create the next handler.
- Added `Session.Send`, which checks shutdown before and after acquiring the existing send mutex and serializes dispatcher DATA/HALF_CLOSE/RESET with metadata, heartbeat, and PONG frames.
- Added `RunWebSocketWithHandlerFactory`. The factory runs only after a successful dial, creates one handler for that connection, and synchronously closes it after `Session.Run` before any reconnect attempt.
- Wired production `tunnelmesh-agent run` to create `NewStreamDispatcherWithSender(agent.Dialer{}, nil, session.Send)` for every successful Agent WebSocket connection.
- Added a real in-process CLI integration using SQLite, credentials, policies, `ServerRuntime`, the Cobra Agent command, authenticated Agent/Client WebSockets, and real loopback TCP, UDP, and HTTP targets.

## TDD RED Evidence

### Finding 1: Client unique OPEN attempts were unbounded

Command:

```text
go test ./internal/server -run 'CapsUniqueOpenAttemptsBeforePayloadDecode|NeverReusesStreamID' -count=1
```

Initial RED failed to compile with `undefined: serveClientSessionWithOpenLimit`. The test independently verifies that unique IDs are counted before payload decode/authorization, duplicates do not recount, the over-limit ID is not inserted, and the terminal payload is fixed and bounded.

### Finding 2: Agent relay wire IDs wrapped and stale generations could collide

Command:

```text
go test ./internal/server -run 'RejectsWireIDWrapAndResetsForNewEpoch' -count=1
```

RED result:

```text
OpenStream reused a retired wire ID after MaxUint32
```

The minimal implementation exhausts the epoch allocator instead of wrapping and verifies that a replacement epoch receives wire ID 1 without sharing the old mapping.

### Finding 3: DATA after remote HALF_CLOSE did not terminate deterministically

Command:

```text
go test ./internal/server -run 'RejectsDataAfterRemoteHalfClose' -count=1
```

RED result:

```text
Write returned relay: backpressure, wanted protocol.ErrInvalidFrame
```

The queue previously admitted the invalid DATA path until incidental saturation. The stream now checks `remoteHalf` under its state lock before queue admission.

### Finding 4: Agent dispatcher lacked directional close, RESET mapping, UDP sizing, and joined shutdown

Focused tests and their observed RED results:

```text
TestStreamDispatcherHalfCloseKeepsTargetReadSideUntilResponseEOF
undefined: NewStreamDispatcherWithSender

TestStreamDispatcherResetsHalfCloseWhenTargetLacksCloseWrite
returned "agent: target stream does not support half-close" and sent no RESET

TestStreamDispatcherMapsNonEOFReadErrorToBoundedReset
received HALF_CLOSE instead of RESET

TestStreamDispatcherRejectsDataAfterInboundHalfClose
target received "after-half-close"

TestStreamDispatcherPreservesLargeUDPDatagramBoundary
61,440-byte logical datagram was split at 32,768 bytes

TestStreamDispatcherCloseWaitsForTargetReaderExit
Close returned before the target reader exited
```

Each test exercises observable dispatcher behavior through a real `io.ReadWriteCloser` fake; no assertion depends on private implementation fields.

### Finding 5: Agent reconnects had no per-connection handler lifecycle

Command:

```text
go test ./internal/agent -run 'CreatesAndClosesHandlerForEveryConnection' -count=1
```

Initial RED failed to compile because `RunWebSocketWithHandlerFactory` and `SessionFrameHandler` did not exist. The GREEN test verifies that the first handler is already closed when the replacement connection's factory runs and that cancellation closes the final handler.

### Finding 6: production Agent CLI ignored stream frames

Command:

```text
go test ./internal/cli -run 'AgentRunCommandForwardsTCPUDPAndHTTPThroughRealDispatcher' -count=1 -v -timeout=30s
```

RED result before CLI wiring:

```text
timed out reading stream response
```

The real Agent authenticated and registered, but `tunnelmesh-agent run` passed `onFrame=nil`, so TCP OPEN/DATA never reached the target. Wiring the per-connection dispatcher made the TCP path pass and enabled the same production handler for UDP and HTTP.

### Additional target write-error and short-write regression

During the real UDP integration, a 60 KiB loopback datagram exposed a host constraint and a dispatcher error boundary. Boundary diagnostics showed server relay OPEN succeeded, the full 61,440-byte DATA frame was queued, then the relay observed `relay: node disconnected`; the UDP target received nothing. A direct host UDP send of the same payload returned `EMSGSIZE` (`Message too long`), proving the integration fixture exceeded this host's kernel UDP datagram limit.

That environment finding exposed a real production bug: target `Write` errors escaped `Handle`, disconnecting the entire Agent session. Focused RED tests were added before the production change:

```text
go test ./internal/agent -run 'MapsTargetWriteErrorToBoundedReset' -count=1 -v
Handle(DATA) error = target datagram too large, want per-stream RESET

go test ./internal/agent -run 'MapsTargetShortWriteToBoundedReset' -count=1 -v
timed out waiting for target-short-write RESET
```

The minimal fix maps both cases to per-stream close plus the fixed RESET. The real socket integration now uses an 8 KiB datagram that is portable on the verification host, while `TestStreamDispatcherPreservesLargeUDPDatagramBoundary` retains explicit 60 KiB logical-frame coverage.

### Full-gate test-fixture corrections

The affected ordinary gate found:

```text
TestStaleReadBackCannotDeleteReusedStreamID
panic: sync: negative WaitGroup counter
```

Root cause: the old test directly invoked private `readBack` without performing the reader registration that production `Handle(OPEN)` now guarantees. The fixture now calls `readers.Add(1)` before the direct invocation; production code was unchanged.

The first affected race gate then found a race in `TestStreamDispatcherRejectsDuplicateStreamID`: its fake connection's deferred `Close` and reader goroutine both wrote the fake `closed` flag. The fake's release and underlying close are now protected by its existing `sync.Once`, matching real connection idempotent-close expectations. Focused race passed before rerunning the complete gate.

## GREEN and Verification Evidence

Focused dispatcher and handler lifecycle:

```text
go test ./internal/agent -run 'StreamDispatcher|CreatesAndClosesHandlerForEveryConnection' -count=1 -v -timeout=20s
ok github.com/tunnelmesh/tunnelmesh/internal/agent 0.306s
```

Focused OPEN cap and Agent relay lifecycle:

```text
go test ./internal/server -run 'CapsUniqueOpenAttemptsBeforePayloadDecode|RejectsWireIDWrapAndResetsForNewEpoch|RejectsDataAfterRemoteHalfClose|AgentRelayTransport' -count=1 -v -timeout=30s
ok github.com/tunnelmesh/tunnelmesh/internal/server 0.474s
```

Real production Agent CLI TCP/UDP/HTTP integration:

```text
go test ./internal/cli -run 'AgentRunCommandForwardsTCPUDPAndHTTPThroughRealDispatcher' -count=1 -v -timeout=30s
ok github.com/tunnelmesh/tunnelmesh/internal/cli 0.427s
```

Affected packages:

```text
go test ./internal/server ./internal/client ./internal/agent ./internal/cli ./internal/relay -count=1
ok github.com/tunnelmesh/tunnelmesh/internal/server 9.800s
ok github.com/tunnelmesh/tunnelmesh/internal/client 0.717s
ok github.com/tunnelmesh/tunnelmesh/internal/agent 1.055s
ok github.com/tunnelmesh/tunnelmesh/internal/cli 0.379s
ok github.com/tunnelmesh/tunnelmesh/internal/relay 0.484s
```

E2E selector:

```text
go test ./internal/e2e -run 'Client|Forward|SSH' -count=1
ok github.com/tunnelmesh/tunnelmesh/internal/e2e 0.319s
```

Fresh `go test -list` confirms the selector currently runs the same five SSH tests and no test named Client or Forward. The new real CLI Runtime test supplies dedicated Client-to-Server-to-Agent TCP/UDP/HTTP coverage at package integration level.

Affected race:

```text
go test -race ./internal/server ./internal/client ./internal/agent ./internal/cli ./internal/relay -count=1
ok github.com/tunnelmesh/tunnelmesh/internal/server 80.368s
ok github.com/tunnelmesh/tunnelmesh/internal/client 1.326s
ok github.com/tunnelmesh/tunnelmesh/internal/agent 2.047s
ok github.com/tunnelmesh/tunnelmesh/internal/cli 3.103s
ok github.com/tunnelmesh/tunnelmesh/internal/relay 1.552s
```

Full repository:

```text
go test ./... -count=1
PASS (all packages; server 10.369s, storage 2.009s)
```

Full repository race:

```text
go test -race ./... -count=1
PASS (all packages; server 81.011s, storage 2.239s)
```

Static verification:

```text
go vet ./...
PASS

git diff --check
PASS
```

## Fix-round Changed Files

- `internal/server/ws_client.go`
- `internal/server/ws_client_test.go`
- `internal/server/agent_relay_transport.go`
- `internal/server/agent_relay_transport_test.go`
- `internal/agent/session.go`
- `internal/agent/session_test.go`
- `internal/agent/websocket.go`
- `internal/agent/websocket_test.go` (new)
- `internal/cli/root.go`
- `internal/cli/agent_runtime_test.go` (new)
- `.superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-report.md`

## Self-review

- Confirmed unique Client OPEN accounting occurs before decode/authorization, excludes invalid zero IDs through frame validation, does not recount duplicates, and never inserts the over-limit ID.
- Confirmed Agent wire IDs never wrap or reuse within one authenticated epoch and that allocator/map keys include Agent identity plus epoch. A new epoch receives an independent allocator only after generation replacement.
- Confirmed stale generation teardown cannot delete a replacement allocator or deliver old frames into a new epoch's stream.
- Confirmed DATA after Agent remote HALF_CLOSE checks state before queue admission and deterministically terminates the stream rather than depending on backpressure.
- Confirmed dispatcher outbound frames use `Session.Send`; no dispatcher goroutine writes the underlying WebSocket transport directly.
- Confirmed HALF_CLOSE is directional, both halves trigger full cleanup, target read EOF maps to HALF_CLOSE, and all non-EOF read/write failures expose only the fixed `stream rejected` payload.
- Confirmed target write errors and short writes terminate one logical stream without returning the target error into `Session.Run`; this prevents a local UDP `EMSGSIZE` or target failure from forcing an Agent reconnect.
- Confirmed DATA after inbound HALF_CLOSE never reaches the target and produces RESET/cleanup.
- Confirmed per-connection handler creation occurs only after successful dial, handler closure waits for target readers, and the next reconnect factory cannot run while the prior dispatcher remains open.
- Confirmed UDP logical reads use `protocol.MaxPayload`, while the real host integration stays below kernel datagram limits and verifies exactly one request datagram and one response datagram.
- Confirmed the real CLI test exercises the production Cobra Agent command and the shared authenticated Client/Server Runtime path for TCP half-close/reverse response, UDP boundary preservation, and transparent HTTP bytes.
- Confirmed no raw token, target error, internal RESET reason, or other credential-bearing detail is emitted in protocol terminal frames or logs.
- Confirmed no commit, push, merge, destructive Git action, or unrelated production refactor was performed.

## Concerns

1. The required E2E selector still matches only five SSH-named tests. This round adds a real package-level production CLI TCP/UDP/HTTP integration, but the dedicated process-level Client/Forward E2E remains scheduled for Task 9.
2. Actual UDP datagram limits are host/kernel dependent and may be lower than the protocol's 64 KiB logical maximum. TunnelMesh preserves logical frame boundaries up to the protocol bound, and target `EMSGSIZE` now resets only the affected stream, but it cannot make an operating system transmit a datagram larger than that host permits.
3. Existing malformed OPEN, dial failure, duplicate OPEN, and unknown stream transitions in `StreamDispatcher` still return errors and therefore may reconnect the Agent session. They were not controller findings for this round and were intentionally not expanded here.
4. Independent subagent code review was not run because this fix round explicitly prohibited subagents. The controller review remains the independent review checkpoint.

## Commits

none

---

# Fix Round 3/5

## Status

**DONE_WITH_CONCERNS**

The remaining controller finding is closed with process-local exact connection fencing. Agent protocol/metadata `Epoch` retains its existing wire and persistence meaning; a separate opaque `uint64` server connection generation now isolates relay allocators, streams, callbacks, sends, and teardown across same-epoch reconnects. No commit, push, merge, or destructive Git action was performed.

## Design Changes

- `AgentSessionManager.Register` allocates a non-zero, process-local, monotonically increasing `serverGeneration` under the manager lock only for a successful registration. The value is immutable for the lifetime of the returned `AgentSession`.
- `serverGeneration` is not added to Agent frames, metadata payloads, configuration, API responses, or database state. A Server process restart can safely restart the counter because no callback/goroutine from the prior process remains.
- The last valid `uint64` generation may be allocated once. Any later registration fails closed with `ErrServerGenerationExhausted`; the counter never wraps and no live session is installed.
- `ErrServerGeneration` is distinct from `ErrEpoch`, preventing the process-local connection incarnation from being conflated with the protocol/persistent Agent epoch.
- `AgentRelayTransport` allocator and stream keys now use `(AgentID, serverGeneration)`. Numeric wire IDs remain monotonic and non-reusable within that connection namespace; a replacement connection has an independent allocator and may safely begin at wire ID 1.
- `AgentSessionManager.SendGeneration`, `AgentRelayTransport.HandleAgentFrame`, and `FailAgentGeneration` all require the exact process-local generation. Old same-epoch sends/callbacks are rejected or ignored, and old teardown deletes only its own allocator/streams.
- Runtime stream callbacks and teardown receive the exact registered `AgentSession` through the package-internal `serveAgentSession` lifecycle callbacks. Public `ServeAgentSession` and `ServeAgentSessionWithInitialFrame` signatures remain compatible for existing callers.
- Runtime cleanup deliberately performs `RemoveSession` and then invokes the exact-generation relay cleanup callback. A same-epoch replacement can register in that interval without being affected by the old cleanup.
- The session writer goroutine now starts only after registration and generation allocation succeed; rejected stale/overflow registrations close their transport without publishing a writer or session.

## TDD RED Evidence

### Same-epoch replacement and delayed old callback/teardown

New tests:

- `TestAgentRelayTransportSameEpochReplacementFencesDelayedOldCallbacksAndTeardown`
- `TestAgentRelayTransportConcurrentOldTeardownAndSameEpochRegistration`
- `TestAgentSessionManagerAllocatesMonotonicServerGenerationsAndFailsClosedAtOverflow`

Initial command:

```text
go test ./internal/server -run 'ServerGenerations|SameEpochReplacementFences|ConcurrentOldTeardown' -count=1 -v
```

Expected RED result:

```text
oldSession.serverGeneration undefined
newSession.serverGeneration undefined
manager.nextServerGeneration undefined
```

The tests model the controller's failure case: register Agent A epoch 1 and open a stream; remove it; register Agent A epoch 1 again and open a new stream whose numeric wire ID is independently 1; then deliver the old callback and delayed old teardown. The new same-epoch stream must continue receiving data.

The concurrency test starts old-generation teardown and a legal same-epoch registration from the same barrier, then verifies the replacement can open and exchange stream data. It is exercised repeatedly under the race detector.

### Keep server connection generation semantically separate from Epoch

After the initial implementation, a second RED explicitly required a distinct stale-incarnation error:

```text
go test ./internal/server -run 'ServerGenerations' -count=1 -v
undefined: ErrServerGeneration
```

The GREEN implementation returns `ErrServerGeneration` when an exact process-local incarnation no longer matches. `ErrEpoch` remains limited to protocol/business epoch validation.

### Overflow behavior

The manager test sets the next counter to `math.MaxUint64` and verifies that registration returns `(nil, ErrServerGenerationExhausted)`, does not install a session, and never wraps to zero. The existing Agent relay MaxUint32 test remains active and verifies that stream wire IDs still exhaust without reuse inside one connection namespace.

## GREEN and Verification Evidence

Focused Agent relay, SessionManager, same/different epoch, MaxUint32, half-close, OPEN cap-adjacent runtime bridge:

```text
go test ./internal/server -run 'ServerGenerations|SameEpochReplacementFences|ConcurrentOldTeardown|AgentRelayTransport|RemoveSessionDoesNotStaleSameEpochReplacement|ClientWSRuntimeDefaultTransportBridgesRegisteredAgentBidirectionally' -count=1 -v -timeout=40s
ok github.com/tunnelmesh/tunnelmesh/internal/server 0.669s
```

Focused generation/teardown race stability:

```text
go test -race ./internal/server -run 'ServerGenerations|SameEpochReplacementFences|ConcurrentOldTeardown|AgentRelayTransport|RemoveSessionDoesNotStaleSameEpochReplacement|ClientWSRuntimeDefaultTransportBridgesRegisteredAgentBidirectionally' -count=20 -timeout=60s
ok github.com/tunnelmesh/tunnelmesh/internal/server 16.862s
```

Affected packages:

```text
go test ./internal/server ./internal/agent ./internal/cli ./internal/client ./internal/relay -count=1
ok github.com/tunnelmesh/tunnelmesh/internal/server 8.840s
ok github.com/tunnelmesh/tunnelmesh/internal/agent 0.325s
ok github.com/tunnelmesh/tunnelmesh/internal/cli 1.073s
ok github.com/tunnelmesh/tunnelmesh/internal/client 0.761s
ok github.com/tunnelmesh/tunnelmesh/internal/relay 1.263s
```

E2E selector:

```text
go test ./internal/e2e -run 'Client|Forward|SSH' -count=1
ok github.com/tunnelmesh/tunnelmesh/internal/e2e 0.318s
```

Affected race:

```text
go test -race ./internal/server ./internal/agent ./internal/cli ./internal/client ./internal/relay -count=1
ok github.com/tunnelmesh/tunnelmesh/internal/server 79.535s
ok github.com/tunnelmesh/tunnelmesh/internal/agent 1.347s
ok github.com/tunnelmesh/tunnelmesh/internal/cli 2.976s
ok github.com/tunnelmesh/tunnelmesh/internal/client 1.683s
ok github.com/tunnelmesh/tunnelmesh/internal/relay 2.497s
```

Full repository:

```text
go test ./... -count=1
PASS (all packages; server 10.457s, storage 2.302s)
```

Full repository race:

```text
go test -race ./... -count=1
PASS (all packages; server 79.431s, storage 2.215s)
```

Static verification:

```text
go vet ./...
PASS

git diff --check
PASS
```

## Fix-round Changed Files

- `internal/server/session_manager.go`
- `internal/server/session_test.go`
- `internal/server/agent_relay_transport.go`
- `internal/server/agent_relay_transport_test.go`
- `internal/server/ws_agent.go`
- `internal/server/runtime.go`
- `.superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-report.md`

## Self-review

- Confirmed `Epoch` continues to fence metadata, heartbeat, persistence, and stale-epoch registration exactly as before; no protocol or DDL field was added.
- Confirmed `serverGeneration` is allocated and published under `AgentSessionManager.mu`, remains immutable after publication, and is read only from the registered session.
- Confirmed stale and same-epoch rejected registrations do not consume/publish a generation, while every successful same-epoch-after-removal or higher-epoch registration receives a strictly newer value.
- Confirmed `math.MaxUint64` can be used once as the final non-zero generation and the following attempt fails closed without installing a session or wrapping.
- Confirmed Agent relay allocator keys, stream keys, stream-owned send state, inbound callback admission, RESET sends, and generation cleanup contain no remaining Epoch-based connection fencing.
- Confirmed old same-epoch Handle calls cannot target a replacement stream even when both connections use numeric wire ID 1.
- Confirmed delayed `FailAgentGeneration(oldGeneration)` removes/fails only old streams and its allocator, leaving the new same-epoch allocator and stream live.
- Confirmed different-epoch replacement tests now use the actual immutable session generations rather than assuming those values equal protocol epochs.
- Confirmed the existing MaxUint32 wire-ID exhaustion test still passes and a replacement connection starts with an independent ID 1 allocator.
- Confirmed package-internal runtime lifecycle callbacks capture the exact registered session for both inbound frames and cleanup; public session-serving APIs remain source-compatible.
- Confirmed the existing manager-to-session-to-relay lock order is preserved. Generation allocation adds no new lock inversion, and concurrent teardown/registration passes repeated race testing.
- Confirmed Client OPEN cap, Agent directional half-close/RESET behavior, per-connection handler factory, real CLI TCP/UDP/HTTP forwarding, and relay backpressure tests remain green through affected and full gates.
- Confirmed no secret-bearing data is stored or emitted in `serverGeneration`, errors, frames, logs, or persistence.
- Confirmed no commit, push, merge, destructive Git action, or unrelated production refactor was performed.

## Concerns

1. GitNexus did not have TunnelMesh indexed, so graph-based impact output was unavailable. The impact fallback used complete `rg` call-site enumeration plus focused/full/race verification.
2. `serverGeneration` is intentionally process-local and unexported. Operational logs and APIs continue to identify Agent sessions by the existing non-secret Agent ID/node ID/epoch; no new externally visible connection identifier was introduced.
3. Independent subagent code review was not run because this fix round explicitly prohibited subagents. The controller review remains the independent checkpoint.

## Commits

none

---

# Fix Round 4/5

## Status

**DONE_WITH_CONCERNS**

The public callback API compatibility regression introduced by the exact connection-generation fencing is fixed without weakening the internal fencing. Legacy epoch-based callbacks remain source-compatible, while runtime paths continue to use the opaque process-local server generation. No commit, push, merge, or destructive Git action was performed.

## TDD RED Evidence

The first compatibility test run intentionally compiled callers using the historical exported `int64` signatures. It failed because the implementation exposed `uint64 serverGeneration` instead and the test fixture could not access the private session generation/counter:

```text
oldSession.serverGeneration undefined
newSession.serverGeneration undefined
manager.nextServerGeneration undefined
```

A second RED test required stale-incarnation failures to remain distinct from protocol epoch failures:

```text
undefined: ErrServerGeneration
```

These failures established that the fix needed an adapter rather than changing the public API to expose the internal generation type.

## Design Changes

- Restored the legacy exported signatures:
  - `AgentSessionManager.SendGeneration(id string, epoch int64, frame)`
  - `AgentRelayTransport.HandleAgentFrame(agentID string, epoch int64, frame)`
  - `AgentRelayTransport.FailAgentGeneration(agentID string, epoch int64)`
- Added package-private exact-generation methods (`sendServerGeneration`, `handleAgentFrameGeneration`, and `failAgentGeneration`). Runtime registration, Agent WebSocket dispatch, stream callbacks, and teardown use these methods with the immutable `serverGeneration` captured from the registered session.
- Added `retiredEpochs` tracking to `AgentSessionManager`. A legacy epoch callback can still route a unique current session, preserving existing integrations. Once a session is removed and the same protocol epoch is replaced, epoch-only callbacks fail closed with `ErrServerGeneration`; they cannot send to or tear down the replacement.
- `Remove` and `RemoveSession` record retired epochs before releasing the manager state. Exact-generation cleanup remains unaffected and continues to target only the originating connection namespace.
- Kept `serverGeneration` process-local and unexported. It is not added to protocol frames, metadata, API responses, configuration, or persistence, and `Epoch` retains its existing protocol/business semantics.

## GREEN and Verification Evidence

Focused compatibility and generation behavior:

```text
go test ./internal/server ./internal/agent ./internal/cli ./internal/client ./internal/relay -count=1
PASS
```

The focused suite covers legacy typed `int64` calls routing a unique current session, same-epoch replacement fail-closed behavior for epoch-only callbacks, exact internal callback continuity, generation overflow, and prior generation-fencing behavior.

E2E selector:

```text
go test ./internal/e2e -run 'Client|Forward|SSH' -count=1
PASS
```

Affected race:

```text
go test -race ./internal/server ./internal/agent ./internal/cli ./internal/client ./internal/relay -count=1
PASS
```

Full repository:

```text
go test ./... -count=1
PASS
```

Full repository race:

```text
go test -race ./... -count=1
PASS (server 79.069s, storage 1.900s; migrations has no test files)
```

Static verification:

```text
go vet ./...
PASS

git diff --check
PASS
```

## Fix-round Changed Files

- `internal/server/session_manager.go`
- `internal/server/session_test.go`
- `internal/server/agent_relay_transport.go`
- `internal/server/agent_relay_transport_test.go`
- `internal/server/ws_agent.go`
- `internal/server/runtime.go`
- `internal/agent/session.go`
- `internal/agent/session_test.go`
- `.superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-report.md`

## Self-review

- Confirmed legacy APIs accept the historical `int64` epoch type and preserve unique-current-session routing.
- Confirmed exact runtime callbacks still use `serverGeneration`; no internal path regressed to epoch-only matching.
- Confirmed same-epoch replacement rejects delayed old epoch-only sends, frame callbacks, and teardown with `ErrServerGeneration`, while exact old-generation teardown remains isolated.
- Confirmed a retired epoch cannot be confused with a currently unique session, and different-epoch replacement behavior remains unchanged.
- Confirmed overflow and MaxUint32 stream-ID fencing tests remain green.
- Confirmed no `serverGeneration` field was added to protocol payloads, Agent metadata, API responses, configuration, or database schema.
- Confirmed no commit, push, merge, destructive Git action, or secret-bearing output was produced.

## Concerns

1. Epoch-only integrations cannot disambiguate two simultaneous same-epoch incarnations; after replacement, the compatibility adapter intentionally fails closed rather than guessing. New runtime code must use the exact-generation internal path.
2. `serverGeneration` remains process-local and unexported by design; operational identifiers and protocol `Epoch` semantics are unchanged.
3. Independent subagent code review was not run because this fix round explicitly prohibited subagents. The controller review remains the independent checkpoint.

## Commits

none

---

# Fix Round 5/5

## Status

**DONE_WITH_CONCERNS**

Legacy `AgentRelayTransport` wrappers now handle nil receivers safely. `HandleAgentFrame` fails closed with `errAgentRelayClosed`, and `FailAgentGeneration` remains a best-effort no-op. Exact-generation methods, legacy `int64` adapters, and retired-epoch fencing semantics are unchanged. No commit, push, merge, or destructive Git action was performed.

## TDD RED Evidence

Added `TestAgentRelayTransportLegacyWrappersNilReceiverFailClosed` and ran:

```text
go test ./internal/server -run '^TestAgentRelayTransportLegacyWrappersNilReceiverFailClosed$' -count=1 -v
```

The test initially failed with a nil-pointer panic in `AgentRelayTransport.HandleAgentFrame` while dereferencing `t.manager`. The test also exercises `FailAgentGeneration` on a nil receiver and requires it not to panic.

## Design Changes

- Added an early `t == nil` guard to `HandleAgentFrame`, returning the existing `errAgentRelayClosed` fail-closed error before accessing `t.manager`.
- Added an early `t == nil` guard to `FailAgentGeneration`, preserving teardown's best-effort no-op behavior.
- Left `handleAgentFrameGeneration`, `failAgentGeneration`, exact generation fencing, public epoch adapters, and `retiredEpochs` behavior unchanged.

## GREEN and Verification Evidence

Focused nil-wrapper regression:

```text
go test ./internal/server -run '^TestAgentRelayTransportLegacyWrappersNilReceiverFailClosed$' -count=1 -v
PASS
```

Focused relay/server generation suite:

```text
go test ./internal/server ./internal/relay -run 'AgentRelayTransport|ServerGenerations|SameEpochReplacementFences|ConcurrentOldTeardown' -count=1
PASS
```

Affected race:

```text
go test -race ./internal/server ./internal/agent ./internal/cli ./internal/client ./internal/relay -count=1
PASS (server 82.368s)
```

Full repository:

```text
go test ./... -count=1
PASS (server 10.670s, storage 2.067s)
```

Full repository race:

```text
go test -race ./... -count=1
PASS (server 80.340s, storage 2.149s; migrations has no test files)
```

Static verification:

```text
go vet ./...
PASS

git diff --check
PASS
```

## Fix-round Changed Files

- `internal/server/agent_relay_transport.go`
- `internal/server/agent_relay_transport_test.go`
- `.superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-6-report.md`

## Self-review

- Confirmed both exported legacy wrappers check `t == nil` before dereferencing `t.manager`.
- Confirmed nil `HandleAgentFrame` returns the established relay-closed error and nil `FailAgentGeneration` does not panic or mutate state.
- Confirmed non-nil legacy callbacks still route/fail closed through `resolveServerGeneration`, including same-epoch replacement behavior.
- Confirmed exact-generation/private runtime paths and all prior generation-fencing tests remain unchanged and green.
- Confirmed no protocol, metadata, API, configuration, or database changes were introduced.
- Confirmed no commit, push, merge, destructive Git action, or secret-bearing output was produced.

## Concerns

1. Nil legacy wrappers indicate a closed/uninitialized relay and intentionally do not attempt recovery; callers should replace the transport before opening streams.
2. Independent subagent code review was not run because this fix round explicitly prohibited subagents. The controller review remains the independent checkpoint.

## Commits

none
