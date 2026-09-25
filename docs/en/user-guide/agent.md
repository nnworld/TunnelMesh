# Agent guide

`tunnelmesh-agent` runs in the private network or on the target host. It initiates an outbound TLS WebSocket connection to the Server, so the Server and Client do not need inbound access to the Agent.

## Registration and connection

1. Create the Agent in the admin console or through `/api/v1/agents`.
2. Create an Agent token bound to that Agent.
3. Store the token in a secret manager or process environment.
4. Run `check-config`.
5. Start the Agent with `run`.

The Agent maintains its online state through heartbeat and lease updates. If the connection drops, the Agent reconnects and re-establishes its session.

## Minimal configuration

```yaml
mode: local
agent:
  server_url: wss://tunnel.example.com/ws/agent
  id: agent-devbox
```

Inject the token separately:

```bash
export TUNNELMESH_AGENT_TOKEN='<token-from-secret-manager>'
```

Do not commit a real token.

## Policy validation

Agent policy is defined on the Server and enforced again by the Agent before dialing an internal target. A policy can restrict:

- Protocol: TCP, UDP, HTTP, or WebSocket.
- Target CIDR.
- Target port.
- Whether private, loopback, or link-local ranges are permitted.

Keep policies narrow. For example, allow only `10.0.0.0/8` and port `22` for an SSH-only Agent.

## Metadata allowlist

The Agent only reports metadata items explicitly configured in its YAML file.

```yaml
agent:
  metadata:
    - name: region
      source: env
      key: TUNNELMESH_REGION
    - name: firmware_version
      source: file
      path: /etc/tunnelmesh/firmware-version
```

Rules:

- File sources must use absolute paths.
- Environment sources must name a single variable.
- Sensitive-looking names such as `password`, `token`, `secret`, `private_key`, or `dsn` are redacted.
- The Agent does not execute commands or scan arbitrary paths.

## Connection pool behavior

By default, an Agent uses one WebSocket connection. After the Server supports connection pooling, you can raise `agent.connections.max`, which is accepted in the range 1-512.

The Agent pool size and the Server's per-Agent ceiling (`server.agents.max_connections_per_agent`, default 64) are two settings on two machines and are never reconciled automatically: once the pool exceeds the Server ceiling, the extra connections are refused and back off while established ones keep working. Raise both together and confirm the enforced value with `tunnelmesh_agent_connection_capacity`.

Pool scaling considers healthy connections, active streams, pending dials, and latency signals. Existing streams stay on their current connection. A stable `instance_id` distinguishes multiple physical Agent processes that share one logical Agent ID.

## ICMP echo for the VPN gateway

The Agent answers a single ICMP echo on behalf of a VPN peer. The capability is off by default and takes two things on that host:

```yaml
agent:
  streams:
    icmp_enabled: true
```

- `net.ipv4.ping_group_range` must include the group the Agent runs as. Without it the Agent cannot open an unprivileged ping socket and logs `agent icmp echo is unavailable` on stderr at start.
- Only echo is handled. Every other ICMP type is dropped and counted by the gateway policy chain, and the Agent never constructs one.
- Echo targets pass the same Agent policy, CIDR, and port checks as TCP and UDP targets.

When a peer that should answer a `ping` does not, check in this order: the Agent's `icmp_enabled` and that startup error line; whether the issuing request could verify the capability (`icmpCapability: unverified` in the audit detail is a cluster placement fact, not an Agent failure); and the node ceiling `server.vpn.icmp_enabled`. Then check the Server side, which is where this usually ends: the binary must be a `-tags vpn` build and `server.vpn.enabled` must be `true`. A Server without the tag refuses to start rather than failing silently, and the release binaries and images do not carry the tag yet.

## Service deployment

For production:

- Run the Agent as a dedicated unprivileged user.
- Use systemd, Docker, or another supervised service.
- Keep the token in a secret manager or environment variable.
- Store the Agent instance ID in writable state storage.
- Monitor Agent online state and reconnect events.

