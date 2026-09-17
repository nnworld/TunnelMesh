# Server administration

This guide summarizes the main admin-console workflows. The Chinese documentation is the authoritative deep reference.

## Dashboard navigation

The dashboard shows high-level counts and recent activity, including:

- Total and online Agents.
- Active tunnels.
- Managed routes.
- Valid service tokens.
- Recent audit events.

Use the sidebar to move between Agents, Clients, routes, tokens, remote servers, credentials, Server nodes, users, audit logs, downloads, WebSSH, and SFTP.

## Agent lifecycle

1. Create an Agent.
2. Bind an Agent token.
3. Configure Agent policy.
4. Deploy the Agent in the private network.
5. Monitor online state and metadata from the Agent detail page.

For connection-pool deployments, inspect per-instance health, active streams, and lease state.

## Token creation and reveal

Create typed tokens for `agent`, `client`, or `server_node`.

- Agent tokens bind to one Agent.
- Client tokens can be scoped to selected Agents, protocols, CIDRs, and ports.
- Server-node tokens can be fleet-wide or bound to selected nodes.

Token plaintext is displayed only once. If token encryption is enabled, an administrator can reveal the stored secret through a dedicated audited API with explicit confirmation headers.

## Route management

Managed routes publish internal HTTP or WebSocket services through the Server. Configure:

- Domain and path prefix.
- Target Agent, host, and port.
- Upstream scheme, host header, and TLS server name.
- Authentication and source ACL for proxy-entry routes.

Use the route list and detail views to inspect status and audit changes.

## WebSSH and SFTP

The admin console includes a browser terminal and SFTP file browser.

- Authentication happens in the browser.
- The Server only relays encrypted bytes over a one-time ticket.
- Host-key fingerprints must be confirmed.
- Credentials can optionally be stored encrypted for one-click authentication.

Use these features only for authorized internal hosts and review access in the audit log.

## Audit logs

Audit logs record create, update, delete, reveal, and authorization-denied events with actor, action, resource type, and resource ID. Use them for:

- Incident reconstruction.
- Access reviews.
- Detecting mis-scoped tokens.
- Verifying who changed a route or policy.

## Observability

Expose Prometheus metrics from the Server and monitor:

- Health and readiness.
- Agent connection state.
- Stream success and latency.
- Relay and cluster status.
- Proxy-entry requests and denials.

Do not place tokens, credentials, target addresses, or client IPs in metric labels.

## Release downloads

The downloads page links to the current GitHub release, checksums, and manifest. Before upgrading, back up the database, check the schema version, and follow the documented migration and rollback steps.

