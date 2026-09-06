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
make web-build
```

The project rules are maintained in [AGENTS.md](AGENTS.md); `CLAUDE.md` is a
compatibility pointer to that single source of truth.

## User and deployment help

- [Documentation index](docs/README.md)
- [Client usage](docs/user-guide/client.md)
- [Agent usage](docs/user-guide/agent.md)
- [Server admin guide](docs/user-guide/server-admin.md)
- [Managed HTTP routes](docs/user-guide/managed-http-route.md)
- [SSH over WebSocket](docs/user-guide/tcp-over-websocket-ssh.md)
- [Docker deployment](docs/deployment/docker.md)
- [Configuration](docs/operations/configuration.md)
- [Troubleshooting](docs/operations/troubleshooting.md)

Build the three container variants with:

```sh
docker build --build-arg APP=server -t tunnelmesh:server .
docker build --build-arg APP=agent -t tunnelmesh:agent .
docker build --build-arg APP=client -t tunnelmesh:client .
```
