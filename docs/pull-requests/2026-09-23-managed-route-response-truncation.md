# Managed-route large response truncation

## Title

`fix(agent): size stream queue from advertised receive window`

## Target branch

`main`

## Summary

Large responses served through a managed route (browser → Server → Agent → internal service) were truncated around 256 KiB. The Server advertises a 512 KiB receive window in `OPEN_STREAM`, but the Agent built its `FairFrameWriter` from an empty `FairWriterConfig{}`, which defaults to a 256 KiB per-stream queue. `EnqueueData` is non-blocking and returns `ErrStreamQueueFull` when the queue is full, and `readBack` treated that as a fatal stream error without sending `RESET`. The stream disappeared on the Server side, `copyResponse` swallowed the `io.Copy` error, and the browser received a short body under an intact `Content-Length` — reported as `assets/mode-DouGTm1Q.js` failing to load.

The fix sizes the Agent queue from the credit the Server advertises, makes Client writes wait for credit instead of failing, and makes relay enqueue failures loud by sending `RESET`.

## User impact

Static assets, downloads, and any response larger than 256 KiB now transfer completely over managed routes. Client-side publish and port forwarding no longer fail bulk writes with `protocol: window exhausted`; the write waits for `WINDOW_UPDATE`. Truncated transfers now surface as an explicit stream reset instead of a silent short body. No configuration is required and no behavior is opt-in.

## API, schema, and configuration impact

None. No HTTP API, database schema, configuration key, or protocol constant value changes. The wire protocol is unchanged: the Agent simply stops dropping frames it was already credited for.

## Security impact

No change to authentication, authorization, or redaction. Flow-control constants are not attacker-controllable: a peer that exceeds its advertised window is still refused, and the invariant only raises the Agent queue to the credit the Server already granted. No secret, token, or DSN appears in the diff, logs, or new tests.

## Invariant

Every relay pump consumes send credit before it hands a frame to its outbound queue, and a peer returns credit only after it has taken the bytes, so:

```
queued = consumed - sent <= initialWindow + updates - sent <= initialWindow
```

Therefore "per-stream outbound queue >= the window the peer advertises" is what keeps a full queue unreachable for a compliant peer. Because these queues refuse frames instead of blocking, and every pump treats a refusal as fatal, sizing a queue below the credit does not add safety — it converts ordinary backpressure into a truncated transfer. This is now documented in `internal/protocol/window.go` so future changes cannot silently break it. The Server already mirrors the rule on its inbound side, where `receiveBudget` equals the window advertised in `OPEN_STREAM`.

## Changes

- `internal/agent/session.go`: new `streamQueueBytes = protocol.DefaultServerReceiveWindow` passed to `NewFairFrameWriter`; `readBack` now sends `RESET` when a DATA or HALF_CLOSE send fails.
- `internal/client/session.go`: `frameStream.Write` chunks to `protocol.MaxStreamFrame` (32 KiB) and blocks in `waitForSendWindow` until credit arrives, woken by the `WINDOW_UPDATE` handler through `windowSignal` with a 100 ms fallback poll for shutdown. A remote HALF_CLOSE (`io.EOF`) ends only the read direction and does not abort a write still owed credit.
- `internal/server/ws_client.go`: `relayToClient` sends `FrameReset` when the enqueue fails, so the Client learns the byte stream ended early.
- `internal/protocol/window.go`: documents the derivation and the queue-floor invariant.
- Docs: `docs/user-guide/server-admin.md` gains a Client ↔ Server row and the queue-floor paragraph; `docs/user-guide/managed-http-route.md` gains a "大响应体与流控" section with truncation triage.

## Tests run

- `go build ./...`
- `go test ./... -count=1` — all packages pass
- `go test -race ./internal/agent ./internal/client ./internal/server ./internal/session ./internal/protocol -count=1` — all pass
- `go vet ./...` — clean
- `git diff --check` — clean
- `python3 scripts/gen_doc_index.py`

New and rewritten tests:

- `TestStreamDispatcherLargeResponseSurvivesSlowWriter` (new, red first): a 1 MiB response consumed slowly must arrive in full; before the fix it failed with `relay pump abandoned the response; only 0 of 1048576 bytes`.
- `TestSessionWriterQueueCoversAdvertisedCredit` (new, red first): asserts the Agent per-stream queue is at least the window the Server advertises; before the fix 262144 < 524288.
- `TestSessionFlowControlBulkWriteIsDeliveredIntact` (new): a 1 MiB Client write is delivered byte-exact and in order.
- `TestSessionFlowControlAdvertisesWindowAndWaitsForCredit` (rewritten): the previous contract "over-send must be rejected" was itself the bug; the contract is now "over-send must wait for credit".
- `TestSessionFlowControlAppliesPeerWindowUpdate` (adapted to 32 KiB chunking).

No frontend change, so `npm test` and `npm run build` were not required.

## Release steps

1. Merge after review and CI checks.
2. Ship Server, Agent, and Client in the same release. Roll out **Agent before or together with Server**: a new Agent's larger queue is harmless to an old Server, while an old Agent against a new Server reproduces the truncation.
3. Upgrading the Agent alone already fixes managed-route truncation; the Client bulk-write fix requires upgrading the Client.
4. No schema migration and no configuration change; no downtime required.

## Rollback steps

Revert the merge commit and redeploy the previous binaries. There is no persisted state to unwind. Rolling back restores both defects: large managed-route responses truncate near 256 KiB, and Client bulk writes fail on window exhaustion.

## Reviewer focus

- The queue-floor invariant and whether `streamQueueBytes` should instead be derived at runtime from the negotiated window rather than from the default constant.
- `waitForSendWindow` termination: confirm every terminal state exits the loop, and that the 100 ms fallback cannot spin on a stream whose peer stopped returning credit.
- Head-of-line blocking: the wait is per-stream and never on the shared receive loop; verify no path holds `session.mu` while waiting.
- Whether the deliberately unchanged `io.Copy` error swallowing in `internal/server/http_proxy.go` and `internal/client/forward.go` should be a follow-up. `RESET` now makes truncation loud, but the Server access log still does not record a short body.

## Integration status

Implementation and local validation are complete on `codex/phase-a-sso-mfa-device-trust`. Plan is recorded as a retroactive plan in `docs/superpowers/plans/2026-09-23-managed-route-response-truncation.md` per the emergency-fix clause. Pull-request review and merge are pending.
