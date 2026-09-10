# Client Proxy Remote Validation

## Summary

Add remote validation to the Client proxy protocols:

- Applies to `forward socks5`
- Applies to `forward http-proxy`
- New flag: `--auth-url`
- Supports HTTP or HTTPS remote URLs
- Sends a `POST` request with JSON context before opening an Agent stream

## Context payload

```json
{
  "protocol": "socks5",
  "agentId": "agent-devbox",
  "targetHost": "service.internal",
  "targetPort": 443,
  "username": "alice",
  "password": "secret"
}
```

`username` and `password` are included only when the local auth mode provides them.

## Decision contract

- `2xx` response: allow
- Non-`2xx` response: deny
- Timeout or network error: deny

## Behavior

- SOCKS5 denial returns reply code `0x02` (`connection not allowed`).
- HTTP proxy denial returns `403 Forbidden`.
- Remote validation runs after local authentication and before opening the Agent stream.
- Request bodies, response bodies, usernames, passwords, and target addresses are not logged.

## Compatibility

- No database schema changes.
- No OpenAPI changes.
- No new public Server ingress protocol.
- Existing local authentication modes remain unchanged.

## Verification

```bash
go test ./... -count=1
go test -race ./...
go vet ./...
git diff --check
```

All commands passed on 2026-09-09.

## Rollback

Remove `--auth-url` from the Client command. No schema migration or data cleanup is required.
