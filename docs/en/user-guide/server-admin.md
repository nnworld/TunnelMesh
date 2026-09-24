# Server administration

This guide summarizes the main admin-console workflows. The Chinese documentation is the authoritative deep reference.

## Dashboard navigation

The dashboard shows high-level counts and recent activity, including:

- Total and online Agents.
- Active tunnels.
- Managed routes.
- Valid service tokens.
- Recent audit events.

Use the sidebar to move between Agents, Clients, routes, tokens, remote servers, credentials, Server nodes, users, single sign-on, audit logs, downloads, WebSSH, and SFTP.

## Agent lifecycle

1. Create an Agent.
2. Bind an Agent token.
3. Configure Agent policy.
4. Deploy the Agent in the private network.
5. Monitor online state and metadata from the Agent detail page.

For connection-pool deployments, inspect per-instance health, active streams, and lease state.

## Single sign-on, MFA, and trusted devices

Administrators configure OIDC identity providers and the global authentication policy from
**Single sign-on** in the sidebar. Every user manages their own TOTP enrollment, one-time recovery
codes, trusted devices, and linked identities from **Security** in the avatar menu.

From the **Child accounts** page an administrator can also force MFA on one account (`mfaRequired`),
inspect that account's MFA status, trusted devices, and external identity links, and run `mfa/reset`
to recover a user who lost their authenticator.

Before enabling either feature, inject `TUNNELMESH_TOKEN_ENCRYPTION_KEY` (identical on every node in a
cluster) and set `security.allowed_origins` or `security.allowed_hosts` — the OIDC callback allowlist is
derived from them, and no provider can be registered while both are empty. Without the key, MFA
enrollment and provider creation fail closed with `503 secret_storage_unavailable` rather than storing
plaintext.

The full workflow, field ranges, login sequences, and a troubleshooting table keyed by `data.error` are
in [Single sign-on and multi-factor authentication](sso-and-mfa.md).

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

## Client observability

The Clients page shows client instances, their metadata, and physical WebSocket connections inside the caller's ownership scope. Two independent columns describe two different facts, so metadata freshness is no longer folded into the online state:

- `status` is presence: `online` when at least one connection lease is unexpired, otherwise `offline`. It is derived from leases only.
- `metadataState` is freshness: `fresh` when a snapshot was reported and is inside its TTL, `expired` when a reported snapshot lapsed, and `unavailable` when the Client never sent `CLIENT_HELLO` (older clients), so only basic connection information is known.

An online Client whose metadata lapsed therefore shows `online` together with `metadata expired`, while a long-dead record shows `offline` instead of "expired". The summary cards above the table come from the server-side aggregate over the whole filtered result set (`summary` in `GET /api/v1/clients`), never from the rows on the current cursor page, so they stay correct while paginating and filtering. The `status` query parameter now accepts `online` and `offline`; the legacy values `stale` and `metadata_unavailable` remain accepted as aliases of `metadataState=expired|unavailable` for one minor release.

Instance records for Clients that never reported metadata are created per physical connection and deleted when that connection closes, and the background sweep also reaps such records that hold no live lease. Records that accepted `CLIENT_HELLO` represent a real installation identity and are never removed automatically.

## Observability

Expose Prometheus metrics from the Server and monitor:

- Health and readiness.
- Agent connection state.
- Stream success and latency.
- Relay and cluster status.
- Proxy-entry requests and denials.
- Console logins, second-factor verifications, OIDC relying-party stages, trusted devices, pending challenges, and blocked login buckets.

Do not place tokens, credentials, target addresses, or client IPs in metric labels.

## Release downloads

The downloads page links to the current GitHub release, checksums, and manifest. Before upgrading, back up the database, check the schema version, and follow the documented migration and rollback steps.

