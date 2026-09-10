# Client SOCKS5 CONNECT

## Summary

Add a local SOCKS5 CONNECT ingress to `tunnelmesh-client`:

- New command: `tunnelmesh-client forward socks5`
- Supports IPv4, IPv6, and domain targets
- Optional RFC 1929 username/password authentication via `--auth password`
- Credentials are read only from `TUNNELMESH_SOCKS5_USERNAME` and `TUNNELMESH_SOCKS5_PASSWORD`
- Default listener is loopback-only
- Non-loopback listening requires both `--allow-remote` and `--auth password`
- Reuses the existing authenticated Client WebSocket session and logical TCP stream
- Does not resolve domain targets locally; the Agent side remains the authorization and resolution boundary

## Security model

- SOCKS5 username/password protects only the local ingress.
- It does not replace the Client service token and does not bypass Agent policy.
- Credential comparison is constant-time and never logged.
- Username and password are each limited to 255 bytes, matching RFC 1929.
- Handshake and target values are not logged.
- `BIND`, `UDP ASSOCIATE`, and GSSAPI are not supported.
- The Server does not expose a public SOCKS5 listener.

## Compatibility and limitations

- No database schema changes.
- No OpenAPI changes; this is a Client CLI feature.
- No new public Server ingress protocol.
- SOCKS5 is command-line only and is not added to `client.tunnels` configuration in this iteration.

## Verification

```bash
go test ./... -count=1
go test -race ./...
go vet ./...
git diff --check
```

All commands passed on 2026-09-09.

## Rollback

Stop using `forward socks5` and remove the new command. No schema migration or data cleanup is required.
