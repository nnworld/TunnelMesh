# Client SOCKS5 CONNECT Design

## Status

Confirmed on 2026-09-09, including optional RFC 1929 username/password authentication.

## Problem

TunnelMesh Client currently supports fixed local forwards for TCP, UDP, and HTTP, plus raw TCP stdio proxy. It does not expose a standard local proxy protocol. Users therefore cannot point a browser, `curl --socks5`, or other SOCKS5-aware clients at TunnelMesh and let each request choose its Agent-side target dynamically.

## Goals

- Provide a local SOCKS5 CONNECT ingress on the Client.
- Support an optional local SOCKS5 username/password authentication mode.
- Support IPv4, IPv6, and domain target addresses.
- Reuse the existing authenticated Client WebSocket session and logical TCP stream.
- Let the Server, Agent policy, token scope, CIDR, and port checks remain the authorization boundary.
- Default to loopback-only listening for safety.
- Keep listener lifecycle, concurrent connections, close, and EOF behavior deterministic.

## Non-Goals

- SOCKS5 GSSAPI authentication is not added.
- SOCKS5 BIND is not added.
- SOCKS5 UDP ASSOCIATE is not added.
- The Server does not expose a public SOCKS5 listener.
- No new public UDP ingress is added.
- No local DNS resolution is performed; domain targets are sent to the Agent side and authorized there.
- No arbitrary remote command execution is added.

## User Interface

```bash
tunnelmesh-client forward socks5 \
  --listen 127.0.0.1:1080 \
  --agent agent-devbox
```

Authentication continues to come from the existing Client service token:

```bash
TUNNELMESH_CLIENT_TOKEN='client-service-token'
```

The command also requires `client.server_url` through the existing configuration or CLI flag.

### Local SOCKS5 Authentication

The default mode is `none` and accepts SOCKS5 method `0x00` only. Operators can enable RFC 1929 username/password authentication:

```bash
TUNNELMESH_SOCKS5_USERNAME='alice' \
TUNNELMESH_SOCKS5_PASSWORD='local-ingress-secret' \
tunnelmesh-client forward socks5 \
  --listen 127.0.0.1:1080 \
  --agent agent-devbox \
  --auth password
```

Credentials are read only from these environment variables. They are not accepted as CLI flags and should not be written to configuration files. This authentication protects the local ingress only; it does not replace the authenticated Client service token or weaken Agent-side target policy.

### Remote Listening

The default listener must be loopback. A non-loopback listen address is rejected unless all of the following are true:

```bash
--allow-remote
--auth password
```

`--allow-remote` and password mode are both deliberately required because a remote-accessible no-auth SOCKS5 endpoint lets every client on that network use the TunnelMesh token's authorized target policy.

## Protocol Behavior

### Method Negotiation

In `none` mode, the Client accepts only SOCKS5 method `0x00` (`NO AUTHENTICATION REQUIRED`).

- If the client offers `0x00`, TunnelMesh replies `05 00`.
- If it does not offer `0x00`, TunnelMesh replies `05 ff` and closes the connection.
- Method list length is bounded by the SOCKS5 protocol's one-byte `NMETHODS`.

In `password` mode, the Client accepts only SOCKS5 method `0x02` (`USERNAME/PASSWORD`, RFC 1929):

- If the client offers `0x02`, TunnelMesh replies `05 02`.
- If it does not offer `0x02`, TunnelMesh replies `05 ff` and closes the connection.
- The client then sends RFC 1929 version `1`, one-byte username length, username, one-byte password length, and password.
- Valid credentials receive `01 00`; invalid or malformed credentials receive `01 01` and the connection is closed.
- Credential comparison is constant-time and credentials are never logged.
- Username and password are each limited to 255 bytes, matching RFC 1929.

### CONNECT Request

The Client reads one SOCKS5 request:

```text
VER CMD RSV ATYP DST.ADDR DST.PORT
```

Supported:

- `VER=5`
- `CMD=1` (`CONNECT`)
- `ATYP=1` IPv4
- `ATYP=3` domain
- `ATYP=4` IPv6

Unsupported `BIND` and `UDP ASSOCIATE` commands receive SOCKS5 reply code `0x07` (`command not supported`) before the connection closes.

Malformed requests receive `0x01` (`general SOCKS server failure`) and are closed without opening an Agent stream.

### Reply

After a logical stream opens successfully, the Client returns:

```text
05 00 00 01 0.0.0.0 0 0
```

`BND.ADDR=0.0.0.0` and `BND.PORT=0` are valid placeholder values because TunnelMesh does not expose a separate upstream bind address.

If opening the logical stream fails, the Client returns `0x01` and does not bridge the connection.

## Stream Mapping

Each accepted SOCKS5 CONNECT becomes one existing logical TCP stream:

```go
client.StreamRequest{
    AgentID:    configuredAgentID,
    Protocol:   "tcp",
    TargetHost: request.Host,
    TargetPort: request.Port,
}
```

The SOCKS5 domain target is not resolved locally. This preserves the existing behavior where target authorization and resolution occur on the Server/Agent side.

## Security

- Local SOCKS5 defaults to no authentication and loopback-only listening.
- Non-loopback listening requires both explicit `--allow-remote` and password authentication.
- SOCKS5 username/password credentials are local ingress credentials only and do not replace the Client service token.
- Every CONNECT still uses the authenticated Client token.
- Every stream remains subject to token scope and Agent policy.
- Target host, CIDR, port, and protocol checks remain server/Agent-side.
- Usernames, passwords, handshake bytes, and target values are not logged.
- Handshake reads have a bounded timeout.
- Closing the listener closes active accepted connections.

## Failure Behavior

- Unsupported method: `05 ff`, close.
- Invalid username/password: RFC 1929 failure reply `01 01`, close.
- Unsupported command: SOCKS5 `0x07`, close.
- Malformed request: SOCKS5 `0x01`, close.
- Logical stream open failure: SOCKS5 `0x01`, close.
- WebSocket/session failure: existing stream bridge behavior closes the local connection.
- EOF or reset from either side closes only that SOCKS5 connection and its logical stream.

## Testing Strategy

- Parser tests for method negotiation, IPv4, IPv6, domain, malformed input, and unsupported commands.
- Listener tests proving a real SOCKS5 client handshake opens a dynamic TCP stream.
- Tests proving domain targets are passed through without local resolution.
- Tests proving unsupported methods and commands fail without opening streams.
- Concurrency and lifecycle tests for multiple accepted connections and clean close.
- CLI tests proving command flags, authenticated session wiring, loopback enforcement, and `--allow-remote`.
- Full Go test, race, and vet verification.
