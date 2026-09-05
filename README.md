# TunnelMesh

TunnelMesh is a Go platform for authenticated Agent tunnels, local client
forwarding, managed HTTP routes, and TCP-over-WebSocket access.

The repository ships three commands:

- `tunnelmesh-server` — public API, routing, and tunnel coordination server.
- `tunnelmesh-agent` — outbound Agent connection and target dialer.
- `tunnelmesh-client` — local forwarding and user-facing tunnel client.

## Development

The default local mode uses SQLite and keeps runtime state external to the
process so commands can be scaled horizontally. Configuration and storage
features are added incrementally by the implementation plan in
`docs/superpowers/plans/`.

Common checks:

```sh
make test
make race
make lint
make build
```

The project rules are maintained in [AGENTS.md](AGENTS.md); `CLAUDE.md` is a
compatibility pointer to that single source of truth.
