# Task 8 report: routing, managed HTTP, dynamic wildcard, and TCP bridge

## Delivered

- Added `internal/routing` with:
  - plain-text `agent-id-ip-text-port` IPv4 dynamic host parsing;
  - DNS label/host validation and support for hyphenated agent IDs;
  - exact domain + path, exact domain, explicit one-label wildcard, and dynamic wildcard matching;
  - optional route-ID precedence;
  - CIDR and port allowlists with range parsing;
  - rejection of unspecified, loopback, link-local, metadata, multicast, and broadcast targets.
- Added `internal/server/http_proxy.go`:
  - managed HTTP requests over injected Agent logical streams;
  - response header/body forwarding;
  - raw WebSocket Upgrade handling after a 101 response with bidirectional byte copying;
  - request timeout and target refusal handling.
- Added `internal/server/tcp_bridge.go`:
  - one WebSocket to one TCP logical stream;
  - binary-only messages, fragmented-message-safe forwarding, bounded 64 KiB messages;
  - half-close support through optional `CloseWrite`;
  - refusal, timeout, and backpressure-safe copying.
- Added routing, HTTP proxy, and TCP bridge tests, including precedence, malformed/dangerous hosts, allowlists, raw HTTP responses, binary bridge data, and limits.

## Verification

```text
go test ./internal/routing ./internal/server -count=20   PASS
go test -race ./internal/routing ./internal/server       PASS
go test ./...                                            PASS
go vet ./...                                             PASS
git diff --check                                         PASS
```

## Scope note

The handlers depend on the existing `relay.NodeTransport` and `server.WSConn` interfaces. A concrete WebSocket upgrader remains an application integration concern, keeping the routing and bridge packages independent of a specific WebSocket library.

## Follow-up hardening

- Agent default stream dispatch now supports `http` through an injectable raw HTTP stream dialer (with TCP fallback).
- Bridge cancellation actively closes both ends to unblock library reads; half-close remains available through `CloseWrite`.
- WebSocket Upgrade validates `101 Switching Protocols`, preserves buffered client bytes after `Hijack`, and preserves buffered upstream bytes after response parsing.
- Mixed-case exact domains are normalized before ranking.
- HTTP fallback dialing evaluates the Agent policy under the `http` protocol namespace before opening its raw TCP transport.
