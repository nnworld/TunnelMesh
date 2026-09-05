# Task 5 report — Versioned WebSocket protocol and stream state machines

## Delivered

- Added a versioned binary `Frame` protocol with a fixed 16-byte big-endian header:
  `version(1)`, `type(1)`, `flags(2)`, `stream_id(4)`, `payload_length(4)`, and `window(4)`.
- Added bounded `Encoder`/`Decoder` APIs and package-level `Encode`/`Decode` helpers. Decoding validates version and frame type before allocating payload memory and enforces the 1 MiB payload ceiling.
- Added stream lifecycle and flow control in `StreamState`: open, data receive accounting, local/remote half-close, full close, reset, window update, send-window consumption, and overflow checks. State access and transitions are mutex protected.
- Added `UDPAssociation`, preserving one datagram per binary message and enforcing a configurable maximum (capped at 64 KiB).
- Added unit and fuzz tests for valid/invalid frames, payload limits, unknown versions/types, truncation, stream transitions, half-close/reset, flow-control windows, UDP boundaries, and malformed-input panic safety.

## TDD evidence

The protocol tests were written before implementation and initially failed to compile because the package APIs did not exist. The implementation was then added until all focused tests passed.

## Verification

```text
go test ./internal/protocol -v -count=1       PASS
go test -race ./internal/protocol -count=1   PASS
go vet ./internal/protocol                    PASS
go test ./internal/protocol -run '^$' -fuzz FuzzDecoderRejectsMalformedInput -fuzztime=2s  PASS
go test ./... -count=1                        PASS
git diff --check                              PASS
```

## Commit

Pending local commit after parent review handoff.
