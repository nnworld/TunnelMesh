# Client Standard HTTP Proxy

## Summary

Add a local standard HTTP proxy ingress to `tunnelmesh-client`:

- New command: `tunnelmesh-client forward http-proxy`
- Supports absolute-form HTTP requests
- Supports HTTPS and arbitrary TCP through `CONNECT`
- Supports WebSocket upgrade over HTTP/HTTPS
- Optional standard HTTP proxy Basic authentication via `--auth basic`
- Credentials are read only from `TUNNELMESH_HTTP_PROXY_USERNAME` and `TUNNELMESH_HTTP_PROXY_PASSWORD`
- Default listener is loopback-only
- Non-loopback listening requires both `--allow-remote` and `--auth basic`
- Reuses the existing authenticated Client WebSocket session and logical stream
- Does not resolve target domains locally; the Agent side remains the authorization and resolution boundary

## Security model

- HTTP proxy Basic authentication protects only the local ingress.
- It does not replace the Client service token and does not bypass Agent policy.
- Credential comparison is constant-time and never logged.
- `Proxy-Authorization` and `Proxy-Connection` are stripped before forwarding upstream.
- Basic is plaintext encoding and is intended for loopback or trusted networks only.
- Handshake and target values are not logged.

## Compatibility and limitations

- No database schema changes.
- No OpenAPI changes; this is a Client CLI feature.
- No new public Server ingress protocol.
- HTTP proxy is command-line only and is not added to `client.tunnels` configuration in this iteration.
- Only HTTP/1.1 is supported.
- UDP, FTP, SMTP, DNS, ICMP, transparent proxy, proxy chaining, and HTTP/2 proxy mode are not supported.

## Verification

```bash
go test ./... -count=1
go test -race ./...
go vet ./...
git diff --check
```

All commands passed on 2026-09-09.

## Rollback

Stop using `forward http-proxy` and remove the new command. No schema migration or data cleanup is required.
