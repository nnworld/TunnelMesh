# Why TunnelMesh needs a control plane

Most tunnel tools optimize for the happy path: one user, one machine, one temporary connection. Operations teams eventually need something different. They need to know who may connect, which internal service is reachable, what changed, and what to audit when something breaks.

That is why TunnelMesh is built around a real control plane rather than a single point-to-point tunnel.

## The operational problem with point-to-point tunnels

A point-to-point tunnel is simple until you multiply it:

- Every user needs their own long-lived process.
- Credentials often live in shell histories or ad-hoc config files.
- Access policy is implicit in the tunnel configuration.
- There is no central place to see active connections or revoke access.
- Debugging becomes terminal archaeology instead of structured inspection.

These problems are manageable for one person. They become expensive when dozens of operators need temporary access to internal services.

## What a control plane changes

A control plane separates access policy from the transport path.

In TunnelMesh:

- The Server owns management, routing, authorization, and audit.
- The Agent holds only the credentials needed to connect outward and dial approved targets.
- The Client exposes local forwards or proxy endpoints.
- The admin console gives operators a shared view of Agents, routes, tokens, sessions, and events.

This separation makes authorization, routing, and auditability first-class instead of afterthoughts.

## Architecture

![TunnelMesh admin dashboard](../assets/admin-dashboard.png)

TunnelMesh has four main pieces:

1. **Server** — public edge, management API, embedded admin console, route resolution, and inter-node relay.
2. **Agent** — outbound TLS WebSocket connection from a private network.
3. **Client** — local TCP, UDP, HTTP, SOCKS5, or HTTP-proxy listener on a user workstation.
4. **Relay** — mTLS-authenticated forwarding between Server nodes when the Client and Agent land on different nodes.

The Agent never exposes a public listener; public ingress is HTTP/HTTPS/WSS. An embedded WireGuard gateway that adds exactly one public UDP port on the Server is approved by [ADR 0002](../architecture/adr/0002-public-ingress-and-embedded-vpn.md) and in progress, and it changes nothing here: the Agent still dials out.

## Security model

![WebSSH terminal](../assets/webssh-terminal.png)

TunnelMesh's security model is based on explicit boundaries:

- TLS/WSS for public traffic.
- Scoped service tokens.
- Agent CIDR and port policy.
- Server-side RBAC.
- Structured audit logs.
- Optional encrypted-at-rest credentials for WebSSH.

A token is not a blanket grant. It can be constrained by Agent, protocol, target CIDR, and target port. The Agent revalidates policy before dialing, which adds a second enforcement point.

## Browser SSH and SFTP

![SFTP browser](../assets/sftp-browser.png)

The admin console includes WebSSH and SFTP. Authentication happens in the browser, and the Server only relays encrypted bytes through a one-time ticket. This is useful when an operator needs a terminal or file transfer without installing a local client, while still keeping access inside the audited control plane.

## Honest trade-offs

Choose a different tool if:

- You only need one temporary SSH session and have no compliance or audit requirement.
- You want a full layer-2 or layer-3 VPN.
- You need peer-to-peer NAT traversal.
- You want arbitrary remote command execution rather than constrained tunneling.

Choose TunnelMesh if you need self-hosted access control, scoped tokens, managed HTTP routes, and operational visibility across multiple Agents and users.

## Five-minute quick start

See [Quick start](../user-guide/quickstart.md) for the fastest path from Server to Agent to first local forward.

