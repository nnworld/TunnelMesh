# TunnelMesh vs frp vs ngrok: tunneling model comparison

This page compares deployment models, not every feature of every product. Product capabilities change frequently and can depend on plan, configuration, and deployment. Check each project's official documentation before making an architecture decision.

## Model comparison

| Concern | TunnelMesh | Self-hosted point-to-point tunnel | Mesh VPN | Managed edge tunnel |
| --- | --- | --- | --- | --- |
| Control plane | Built in | Usually minimal or external | Usually distributed | Managed by vendor |
| Admin console | Built in | Rare | Rare | Vendor console |
| Access policy | Server-side RBAC, scoped service tokens, Agent CIDR/port policy | Usually per-tunnel config | Usually network-level policy | Vendor plan and policy features |
| Audit | Structured audit logs | Varies | Varies | Vendor plan and logs |
| Public ingress | HTTP/HTTPS/WSS only | Protocol-specific | Network-level | Vendor edge |
| Private-network component | Agent | Server or client | Mesh node | Edge connector |
| Local user component | Client or browser SSH/SFTP | Client | VPN client | Usually none |
| Deployment model | Server, Agent, optional Client | Usually one server and one client | Full mesh or partial mesh | Managed service |

## How frp and ngrok map to this page

- **frp** is closest to the self-hosted point-to-point tunnel column. It is a widely used
  open-source reverse proxy for exposing local services. TunnelMesh is a better fit when
  those connections need a shared admin console, scoped tokens, RBAC, audit, and Agent
  policy as first-class control-plane features.
- **ngrok** is closest to the managed edge tunnel column. It provides a vendor-operated
  edge and management experience. TunnelMesh is a better fit when the edge, policy data,
  audit logs, and release artifacts must remain self-hosted.

Both products have features that can overlap with TunnelMesh depending on plan and
configuration. This page does not replace their official documentation or a deployment
specific evaluation.

## When TunnelMesh fits

Choose TunnelMesh when you need:

- a self-hosted control plane rather than only a transport process;
- scoped service tokens with protocol, Agent, CIDR, and port constraints;
- a built-in admin console for routes, tokens, sessions, and audit events;
- browser SSH/SFTP with host-key confirmation and one-time tickets;
- cluster relay, observability, and health checks across multiple Server nodes.

## When another model fits better

Choose a different model when you need:

- one temporary SSH session and no audit or policy requirement;
- a full layer-2 or layer-3 VPN;
- peer-to-peer NAT traversal;
- arbitrary remote command execution rather than constrained tunneling;
- a vendor-managed edge with no operational ownership.

## Honest trade-offs

TunnelMesh intentionally does not implement ICMP, TUN/L2 VPN, P2P NAT traversal, or arbitrary remote command execution. Its public Server ingress is HTTP/HTTPS/WSS only; UDP is supported for internal forwarding and local client listeners, not as a public Server listener.
