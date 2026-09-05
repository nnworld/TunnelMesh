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

The repository-wide race suite has a known intermittent Task 8 agent-session
duplicate-stream test failure; the client package race suite is stable and
passes independently.

