# Client guide

`tunnelmesh-client` runs on the user workstation. It creates local listeners or proxy entries and connects to the Server over TLS WebSocket.

## Local forwarding

Minimal TCP forward:

```bash
tunnelmesh-client forward tcp \
  --listen 127.0.0.1:15432 \
  --agent agent-devbox \
  --target-host 10.0.0.10 \
  --target-port 5432
```

Then connect to `127.0.0.1:15432` as if it were the internal service.

Supported local forward types:

- `tcp`
- `udp`
- `http`
- `socks5`
- `http-proxy`

UDP forwarding preserves datagram boundaries and source associations. It starts on the user's machine, and the Server carries it to the Agent - there is no public UDP listener behind it. The Server has a separate WireGuard VPN gateway ingress ([ADR 0002](../../architecture/adr/0002-public-ingress-and-embedded-vpn.md), built only with `-tags vpn`) that does not go through the Client; see the [VPN gateway guide](../../user-guide/vpn.md).

## SOCKS5 and HTTP proxy modes

Start a local SOCKS5 proxy:

```bash
tunnelmesh-client forward socks5 \
  --listen 127.0.0.1:1080 \
  --agent agent-devbox
```

Start a standard HTTP proxy:

```bash
tunnelmesh-client forward http-proxy \
  --listen 127.0.0.1:8080 \
  --agent agent-devbox
```

These modes reuse the same service-token scope and Agent policy controls as explicit forwarding.

## Managed HTTP publishing

Use `publish http` or the admin console to expose an internal HTTP or WebSocket service through a managed route.

Managed routes support:

- Explicit domains and path prefixes.
- Wildcard routes.
- HTTPS and WebSocket upgrade.
- Upstream host and TLS options.

For production, create long-lived routes in the admin console rather than storing them in shell history.

## SSH over WebSocket

Use the Server's TCP-over-WebSocket bridge for tools such as `ssh -o ProxyCommand` or `websocat`.

Example:

```bash
ssh -o ProxyCommand='tunnelmesh-client proxy tcp --agent agent-devbox --target-host 10.0.0.10 --target-port 22' \
  user@10.0.0.10
```

SSH authentication and host-key verification still happen in the SSH client. The Agent only forwards TCP bytes.

## Multi-entry run mode

Use `run` mode to start every configured local listener, proxy, and publish entry in one Client process.

```yaml
mode: local
client:
  token: <token-from-secret-manager>
  entries:
    - type: forward
      protocol: tcp
      listen: 127.0.0.1:15432
      agent: agent-devbox
      target_host: 10.0.0.10
      target_port: 5432
```

This is useful for workstations that need several local mappings at once.

## Non-loopback listeners and process exit

A `tcp`, `udp`, or `http` entry whose `listen` address is not loopback requires `allow_remote: true` (`--allow-remote` for the single-command form); validation and startup both refuse it otherwise, because those three tunnel types have no local credential check, so binding a non-loopback address publishes the target unauthenticated on that segment. `socks5` and `http-proxy` additionally require a `password` or `basic` auth entry.

When any local listener's accept or read loop fails permanently - exhausted descriptors, a destroyed listening socket - `run` and single-command `forward` exit immediately with a non-zero status and write the error to stderr. A port that still looks healthy while serving nothing is the hardest failure to notice from outside, so letting systemd or Docker restart the process beats staying "online" on a broken listener. `Close` and an ordinary configured stop are not failures and never trigger an exit.

