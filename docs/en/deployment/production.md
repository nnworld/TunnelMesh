# Production deployment

This guide summarizes the production topology, transport requirements, storage choices, secret handling, service setup, and rollback path. For full command and deployment details, see the authoritative Chinese guides under `docs/deployment/` and `docs/operations/`.

## Topology and prerequisites

A production deployment normally has:

- One or more `tunnelmesh-server` instances at the public edge.
- One `tunnelmesh-agent` in each private network or on each target host.
- One or more `tunnelmesh-client` processes on user workstations.
- A database for management data.
- A reverse proxy or load balancer that terminates TLS and forwards HTTP and WebSocket traffic.

Agents initiate outbound TLS WebSocket connections to the Server. Clients connect to the Server as well. Neither the Server nor a Client needs inbound access to the Agent's private network.

## TLS/WSS and reverse-proxy requirements

Public ingress is HTTP, HTTPS, and WebSocket over TLS/WSS. The embedded WireGuard VPN gateway adds exactly one public UDP port and is compiled only in `-tags vpn` builds ([ADR 0002](../../architecture/adr/0002-public-ingress-and-embedded-vpn.md)); the release binaries and images do not carry that tag yet, so a default deployment of this release has no public UDP. The port never goes through the reverse proxy described below.

For a typical deployment:

1. Terminate TLS at Nginx, Caddy, or another trusted reverse proxy.
2. Configure the proxy to forward `/api/`, `/ws/`, and the embedded admin console paths to the Server.
3. Set the Server's `server.http_addr` to a loopback address such as `127.0.0.1:8080`.
4. Keep `tls.enabled: false` on the Server because the proxy owns public TLS.
5. Configure `security.allowed_hosts` and `security.allowed_origins` to the exact production domain and admin-console origin.
6. Require TLS 1.2 or newer on the public listener.

If the Server terminates TLS directly, set `tls.enabled: true`, provide a certificate and private key, and listen on port `443`.

## SQLite versus MySQL

- **SQLite** is intended for local mode and single-node deployments. It has no external database dependency and is easiest to back up.
- **MySQL** is intended for cluster mode and multi-Server deployments. Use TLS to the database, a dedicated database account, and the `database` or `etcd` registry.

For high-availability or horizontally scaled Server deployments, use MySQL and more than one Server instance. SQLite is not a shared cluster database.

## Secrets and environment variables

Keep production secrets out of the repository and out of plain configuration files. Use a secret manager or deployment platform secrets.

Common sensitive values include:

- Agent, Client, and Server-node service tokens.
- MySQL DSN and credentials.
- `TUNNELMESH_TOKEN_ENCRYPTION_KEY` for encrypted service-token recovery.
- `TUNNELMESH_TRACE_SIGNING_KEY` when traceroute signing is enabled.
- TLS private keys.

Configuration precedence is command-line arguments, then environment variables, then the configuration file, then defaults.

## systemd or Docker service setup

Both are supported:

- **systemd:** use the packaged service examples under `docs/deployment/linux-systemd.md`. Run `check-config` before `run`, keep `/etc/tunnelmesh` read-only to the service user, and keep writable state under `/var/lib/tunnelmesh`.
- **Docker:** use the provided Compose example under `docs/deployment/docker.md`. Inject secrets through environment variables or mounted secret files, and do not bake tokens into images.

Whatever process manager you choose, run the Server as an unprivileged user and restrict filesystem permissions to the config and state directories.

## Health checks and rollback

Expose the Server's health and readiness endpoints to the load balancer or orchestrator. Use readiness checks before sending production traffic and monitor metrics and logs for failures.

Before an upgrade:

1. Back up SQLite or MySQL.
2. Record the current application version and schema version.
3. Read the release notes and schema-upgrade guide.
4. Deploy to one node or a small canary group first.
5. Verify health, readiness, Agent connections, and key routes.

Rollback plan:

- Prefer rolling back the binary or container image while retaining the current database schema.
- If a release includes schema changes, follow the documented schema-specific rollback path.
- Keep a tested database backup and restore procedure.
- Do not guess reverse SQL for irreversible migrations.

