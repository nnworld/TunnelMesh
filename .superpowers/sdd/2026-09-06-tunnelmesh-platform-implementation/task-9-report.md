# Task 9 report — tunnelmesh-client forwards

## Delivered

- Local TCP listener lifecycle with bounded bidirectional byte copying.
- Local UDP forwarding with per-source association reuse, datagram stream
  adapters, idle expiry, and graceful shutdown.
- Local HTTP forwarding over logical `http` streams, preserving raw request
  headers and response bodies.
- Full-duplex stdio proxy suitable for SSH `ProxyCommand` and binary
  `websocat` usage; no line buffering or base64 conversion.
- Retrying stream opener with bounded exponential backoff for transient
  reconnects.
- Client session frame multiplexer for OPEN/DATA/HALF_CLOSE/RESET streams.
- CLI shells for `forward tcp|udp|http`, `publish http`, `proxy tcp`, and
  `tunnel stop|status`, including `client.tunnels` config-file loading.

## Verification

- `go test ./internal/client -count=1` — pass
- `go test -race ./internal/client -count=20` — pass
- `go test ./... -count=1` — pass
- `go vet ./...` — pass
- `git diff --check` — pass

The repository-wide tests and race suite pass on the reviewed revision.

## Review fixes

- UDP associations now use message-oriented datagram openers and never add a
  byte-stream length prefix; concurrent source creation uses open-outside-lock
  plus compare-and-swap insertion.
- HTTP 101 upgrades hijack the local connection and bridge raw bytes in both
  directions, including bytes buffered while parsing the upstream headers.
- TCP forwarders close tracked active connections during shutdown; raw bridges
  handle half-close, errors, bounded join, and cancellation without hanging.
- Session frame streams preserve DATA queued before HALF_CLOSE, expose AgentID
  in OPEN payloads, and provide a native DatagramStream implementation.
- stdio proxy handles nil contexts and joins both directions while closing on
  hard errors.
- Raw stream endpoints expose directional `CloseWrite` half-close semantics;
  queued DATA is drained before terminal EOF and proxy joins are bounded.
