# PR: Support domain-only and HTTPS upstream services for managed routes

## Problem

An explicit managed route could already dial a hostname as `targetHost`, but it could not independently control the upstream Host header or connect to an HTTPS-only service by IP. If a service used virtual-host routing or required a specific TLS SNI, the Agent connection reached the host but the service rejected or misrouted the request.

## Solution

- Add `hostHeader`, `targetScheme`, and `tlsServerName` to the managed-route API and web UI.
- Store these options in the existing `tunnels.config` JSON field; no database schema migration is required.
- Decode them into the route resolver and propagate them through relay and `OPEN_STREAM`.
- On the Agent, dial `targetHost:targetPort`; for `targetScheme=https`, wrap the connection in TLS and verify the certificate using `tlsServerName` (default `targetHost`).
- Apply `hostHeader` (default `targetHost`) to both HTTP requests and WebSocket upgrades.

Dynamic wildcard routes remain IPv4-only with their existing plain-HTTP upstream behavior.

## Compatibility

- Existing routes keep working with defaults: HTTP upstream, Host equal to `targetHost`, and no custom SNI.
- The public route protocol remains `http` or `websocket`; `targetScheme` only describes the upstream transport.
- `targetScheme` accepts `http` and `https`. TLS verification is mandatory and uses TLS 1.2 or newer.

## Verification

Verified on 2026-09-09:

- `go test ./... -count=1` — passed.
- `go test -race ./...` — passed.
- `go vet ./...` — passed.
- `git diff --check` — passed.
- `npm --prefix web test -- --run` — 8 files / 42 tests passed.
- `npm --prefix web run build` — passed.
- `./scripts/verify-web-embed.sh` — `web/dist and internal/server/web_dist match`.

Feature coverage includes API create/update/patch/validation, route resolver loading, HTTP and WebSocket Host rewrite, protocol and relay propagation, Agent TLS SNI/default SNI/untrusted-certificate behavior, and Routes page form/payload/i18n behavior.

## Rollback

Roll back the application binary and embedded assets. Because no schema migration is introduced, no database rollback is needed. Existing routes created with the new options should have those options removed before running an older release if that older release rejects unknown route response fields.
