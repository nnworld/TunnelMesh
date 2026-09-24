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

## VPN gateway peers

The **VPN Gateway** page (`/vpn`) issues peers for native WireGuard clients: each peer gets a VPN address plus an allowed-IP and allowed-port scope, and egress still happens from the Agent. The page is not admin-gated - ordinary users see the peers they own and administrators see all of them - and that filter runs inside the server-side paginated query, not in the browser. Lists use cursor paging, search by name or comment, and an active / disabled / revoked filter.

**The management plane ships in this release; the data plane depends on the build variant.** Peers can be issued, edited, rotated, revoked, and audited, but a tunnel only comes up when the Server is a `-tags vpn` build with `server.vpn.enabled: true`. The release binaries and images do not carry that tag yet, and the console does not detect which variant it is talking to - it only issues configuration, so confirm the build variant before importing a peer file. See [VPN gateway deployment](../../deployment/vpn-gateway.md).

Form validation mirrors the server byte for byte: names are 1-255 characters without control characters; allowed IPs are comma-separated IPv4 CIDRs, a bare address is normalised to `/32`, and an empty list means no reachable target; allowed ports are comma-separated integers from 1 to 65535, empty meaning unlimited; an expiry must be in the future, empty meaning never; `0` on a flow or packet ceiling means unlimited. Frontend checks only save a round trip - the server validates again. Edits submit changed fields only, and collections are compared as sets, so rewriting the order of `allowed_ips` is not a policy change and produces no audit entry.

"Allow ICMP echo" follows the egress Agent's real capability: the Agent must be online and have negotiated `stream_icmp_echo.v1`, otherwise issuing returns `409 vpn_agent_capability_missing`. The node-level `server.vpn.icmp_enabled` is a ceiling, not a promise - when it is off, Agents are not even asked. One cluster limitation is known and deliberate: capabilities live in the session on the node the Agent is connected to, not in the connection lease, so a request that lands elsewhere still issues successfully while the audit detail records `icmpCapability: unverified`. Refusing on "cannot check" would make issuance depend on which node answered.

Rotation and revocation are terminal and require confirmation. After a rotation the previous configuration stops working immediately while the address stays, so it must be redistributed; revocation keeps the record, and the public key and address are never reused. Issue, edit, rotate, revoke, and reveal are all audited. The private key is shown once: the client-setup drawer asks for `REVEAL`, an explicit risk acknowledgement, and an `Idempotency-Key` before calling `POST /api/v1/vpn-peers/{peerId}/config:reveal`, whose answer is `Cache-Control: no-store` and is never persisted in plaintext. Audit entries record the peer id and the action, never key material.

Active flows are a separate drawer because setup is true offline while flows are a live read that can fail. The list comes from the memory of the gateway serving that peer and is a snapshot, not history: a flow leaves it as soon as it closes, is reaped, or is revoked, nothing is persisted, and there is no cursor because one peer's flow set is bounded by `server.vpn.max_flows_per_peer`. A peer served by another node, or a server with no running gateway, returns `501` rather than an empty list - "no traffic" and "the traffic is on another node" are different facts. The IP pool overview comes from `GET /api/v1/vpn-nodes` and reports `allocated` and `capacity` from the subnet lease and the peer table rather than deriving them from the prefix, because a derived capacity would be a promise the issuing node may not keep.

## Audit logs

Audit logs record create, update, delete, reveal, and authorization-denied events with actor, action, resource type, and resource ID. Use them for:

- Incident reconstruction.
- Access reviews.
- Detecting mis-scoped tokens.
- Verifying who changed a route or policy.

Audit rows are kept forever by default: `server.audit.retention_days` is `0`, so an upgrade never destroys history. Only a positive value starts the retention sweeper, which runs hourly and deletes at most 1000 rows per batch, logging counts rather than row identifiers. Purging is an explicit compliance decision; check the required retention window and your backup path before turning it on.

## Client observability

The Clients page shows client instances, their metadata, and physical WebSocket connections inside the caller's ownership scope. Two independent columns describe two different facts, so metadata freshness is no longer folded into the online state:

- `status` is presence: `online` when at least one connection lease is unexpired, otherwise `offline`. It is derived from leases only.
- `metadataState` is freshness: `fresh` when a snapshot was reported and is inside its TTL, `expired` when a reported snapshot lapsed, and `unavailable` when the Client never sent `CLIENT_HELLO` (older clients), so only basic connection information is known.

An online Client whose metadata lapsed therefore shows `online` together with `metadata expired`, while a long-dead record shows `offline` instead of "expired". The summary cards above the table come from the server-side aggregate over the whole filtered result set (`summary` in `GET /api/v1/clients`), never from the rows on the current cursor page, so they stay correct while paginating and filtering. The `status` query parameter now accepts `online` and `offline`; the legacy values `stale` and `metadata_unavailable` remain accepted as aliases of `metadataState=expired|unavailable` for one minor release.

Instance records for Clients that never reported metadata are created per physical connection and deleted when that connection closes, and the background sweep also reaps such records that hold no live lease. Records that accepted `CLIENT_HELLO` represent a real installation identity and are never removed automatically.

Rows are ordered by most recent heartbeat first, using `last_seen_at DESC, id DESC`, so a Client that just dropped stays at the top. `updated_at` is deliberately not the sort key: the stale and expiry sweeps raise it on their own, which would rank "recently touched by a sweeper" above "recently alive".

## Observability

Expose Prometheus metrics from the Server and monitor:

- Health and readiness.
- Agent connection state.
- Stream success and latency.
- Relay and cluster status.
- Proxy-entry requests and denials.
- Console logins, second-factor verifications, OIDC relying-party stages, trusted devices, pending challenges, and blocked login buckets.

Do not place tokens, credentials, target addresses, or client IPs in metric labels.

`GET /metrics` needs no credentials by default, so keep it off the public entry point. `server.metrics.token` adds an optional `Authorization: Bearer` gate (environment variable only, at least 16 characters) that returns `401` for unauthenticated scrapes while leaving `/health/live` and `/health/ready` open for load balancers.

## Release downloads

The downloads page links to the current GitHub release, checksums, and manifest. Before upgrading, back up the database, check the schema version, and follow the documented migration and rollback steps.
