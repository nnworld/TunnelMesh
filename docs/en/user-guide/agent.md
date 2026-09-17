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

By default, an Agent uses one WebSocket connection. After the Server supports connection pooling, you can raise `agent.connections.max`.

Pool scaling considers healthy connections, active streams, pending dials, and latency signals. Existing streams stay on their current connection. A stable `instance_id` distinguishes multiple physical Agent processes that share one logical Agent ID.

## Service deployment

For production:

- Run the Agent as a dedicated unprivileged user.
- Use systemd, Docker, or another supervised service.
- Keep the token in a secret manager or environment variable.
- Store the Agent instance ID in writable state storage.
- Monitor Agent online state and reconnect events.

