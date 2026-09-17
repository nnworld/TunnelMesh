# Security hardening

This guide summarizes TunnelMesh's transport, token, policy, RBAC, and logging controls. The Chinese documentation remains the authoritative deep reference.

## TLS and allowed hosts/origins

- Use HTTPS and WSS for all public traffic.
- Terminate TLS at a reverse proxy or directly on the Server.
- Allow only TLS 1.2 or newer.
- Restrict `security.allowed_hosts` to the production hostname.
- When WebSSH or SFTP is enabled, restrict `security.allowed_origins` to the exact admin-console origin.
- Public ingress is HTTP/HTTPS/WSS only. The Server does not expose public UDP.

## Scoped Agent and Client tokens

Service tokens are typed as `agent`, `client`, or `server_node`.

- Bind an Agent token to a specific Agent.
- Bind a Client token to one or more explicitly allowed Agents when practical.
- Limit token scope by protocol, target CIDR, and target port.
- Set an expiration time for short-lived or operator-specific tokens.
- Rotate tokens instead of reusing long-lived shared credentials.

Token plaintext is shown once at creation or rotation. Store it in a secret manager, not in a repository or ticket.

## Agent CIDR and port policy

Agent policy is enforced on the Server and revalidated by the Agent before dialing a target.

- Permit only required protocols.
- Allow only necessary CIDRs.
- Restrict target ports to the minimum set.
- Keep private, loopback, and link-local ranges blocked unless a specific route needs them.
- Review policies when an internal network changes.

This defense in depth prevents a compromised token from becoming unrestricted access to every internal service.

## Admin RBAC and audit logs

- Assign the `admin` role only to operators who need global control.
- Regular users should see only their own Agents, routes, and tokens.
- Use structured audit logs for create, update, delete, reveal, and authorization-denied events.
- Review audit logs during incident response and periodic access reviews.

The admin console and API enforce authorization server-side. Do not trust client-supplied ownership or role fields.

## Token encryption and reveal risk

With `TUNNELMESH_TOKEN_ENCRYPTION_KEY` configured, newly created or rotated service tokens are stored as AES-256-GCM ciphertext, not plaintext. Token validation still uses a hash.

Reveal is a privileged, audited operation:

1. Only administrators may call the reveal API.
2. The request must include the explicit confirmation header and a unique idempotency key.
3. The response is marked `Cache-Control: no-store`.
4. The action is written to the audit log.

Legacy tokens created before encryption cannot be recovered; rotate them first.

## Safe logging and secret handling

Do not log or expose:

- Bearer tokens or complete `Authorization` headers.
- Passwords, private keys, or database DSNs.
- WebSSH terminal bytes or SFTP file contents.
- Unredacted Agent metadata values.
- Full target addresses when a summarized or redacted form is enough.

Restrict access to Server logs, audit logs, metrics, and database backups. Use short log retention where allowed, and forward security-relevant events to a controlled audit system.

