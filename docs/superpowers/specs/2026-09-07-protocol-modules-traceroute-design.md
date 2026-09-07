# Protocol Modules and Secure Traceroute Design

## Scope

This increment completes the currently approved platform capabilities around protocol interoperability, logical traceroute, and administrative observability. ICMP/TUN/L2 VPN, P2P NAT traversal, and arbitrary remote command execution remain explicitly out of scope.

## Security model

Service-token authentication continues to use a one-way hash for validation. Because administrators explicitly require secret recovery, newly created and rotated Bearer secrets are also encrypted with AES-256-GCM before persistence. The encryption key is supplied by `TUNNELMESH_TOKEN_ENCRYPTION_KEY` (base64 or hex), never stored in the database or logs. Each row stores ciphertext, nonce, key id, version, and last-read time. Existing hash-only tokens cannot be recovered; rotation creates a recoverable replacement.

`POST /api/v1/tokens/{id}/reveal` is the only secret-recovery endpoint. It requires an administrator with `token:read_secret`, `X-Token-Reveal-Confirm`, and an idempotency key. Responses are `Cache-Control: no-store`; access is audited and the secret is excluded from normal token views, logs, metrics, and traceroute output.

## Logical traceroute

Traceroute follows the authenticated data path rather than emitting ICMP packets: client, public server, relay servers, serving server, and agent. Frames are `TRACE_START`, `TRACE_HOP`, and `TRACE_END`. Every hop carries trace id, sequence, role, node id, transport, timestamps, queue delay, RTT, and result. A signed hop chain prevents reordering, deletion, and injection.

Normal users receive topology-safe fields only. Administrators may request `includeSensitive=true` to receive private addresses, certificate metadata, and real peer addresses. `includeSecrets=true` is rejected unless the caller is an administrator with `token:read_secret` and a reveal confirmation; the preferred workflow is the dedicated reveal endpoint. Traceroute responses containing sensitive data are never cached and are audited.

Management endpoints are `POST /api/v1/agents/{agentId}/trace` and `GET /api/v1/traces/{traceId}`. The same trace model is used by client and server relay APIs.

## Protocol additions

The WebSocket protocol advertises version and capabilities during handshake. Stable error codes, bounded flow-control windows, UDP association lifecycle, and GOAWAY/drain semantics are required. Optional proxy modules include SOCKS5 CONNECT/UDP ASSOCIATE, HTTP CONNECT, PROXY protocol v2, TLS SNI passthrough, Unix sockets, Windows named pipes, and DNS proxy; all are subject to token scope, route policy, SSRF checks, quotas, and timeouts.

## Verification

Every new behavior is introduced with a failing Go test first. Storage contract tests cover SQLite and MySQL-compatible SQL. HTTP tests cover authorization, idempotency, no-store headers, redaction, and audit events. Integration tests cover a multi-hop trace and tamper detection. Existing Go, race, vet, frontend, and release checks remain mandatory.
