# Roadmap

The roadmap is a communication tool, not a delivery promise. Priorities can change based on security, compatibility, and user feedback.

## Shipped

- **OIDC single sign-on.** Authorization-code flow with PKCE (`S256`), discovery and JWKS with caching, per-provider role and group mappings, just-in-time provisioning, and administrator-managed providers. `alg=none` and every `HS*` algorithm are rejected unconditionally. See [SSO and MFA](../en/user-guide/sso-and-mfa.md).
- **TOTP multi-factor authentication with recovery codes.** Three global modes (`disabled`, `optional`, `required`) plus a per-account override, one-time `tmrc-` recovery codes stored as SHA-256 digests, and a TOTP replay guard.
- **Revocable trusted devices.** "Remember this browser" with a bounded lifetime, a per-account cap, one-way token digests, self-service and administrator revocation, and a policy kill switch that invalidates every bypass on the next login.
- **Operator-editable authentication policy.** A single authoritative database row, changed through an audited admin API, with a feature-level rollback path that needs no deploy.
- **Identity observability.** Six Prometheus families covering console logins, second-factor verifications, OIDC relying-party stages, trusted-device population, pending challenges, and blocked login buckets, with bounded low-cardinality labels.
- Self-hosted control plane with a built-in admin console, RBAC, scoped service tokens, Agent policy, and a structured audit log.
- Browser SSH/SFTP with host-key fingerprint confirmation and optional encrypted-at-rest credentials.
- Cluster relay over mTLS, MySQL lease or etcd registration, epoch fencing, and a bundled Grafana dashboard with alert and recording rules.

## Now

- Improve first-install diagnostics and failure messages.
- Keep the bilingual quick start aligned with the current release.
- Expand English documentation for operators and contributors.

## Next (planned)

- **Policy templates.** Reusable presets for common internal-access scenarios, so a route or Agent policy can be applied from a named template instead of field by field.
- **Rate limiting and concurrency policy.** Principal- and token-scoped limits with a documented per-node token bucket for soft limits, evaluated inside the same policy layer as templates.
- **Session observability.** Live console and tunnel sessions with actor, age, and origin, so an operator can see who is connected right now rather than only what the audit log recorded.
- **Audit export.** Bounded, filtered export of `audit_logs` for compliance retention and forwarding to a controlled audit system.
- Add package-manager distribution channels for macOS and Windows.
- Publish benchmark methodology and repeatable results.
- Improve one-command demo deployment.

## Later (planned)

- **Access diagnostics enhancement.** A workbench that answers "why was this request denied" from one place, correlating route resolution, source ACL, credential state, Agent policy, and capacity.
- **Configuration as code.** Declarative desired-state bundles covering Agents, routes, policies, templates, SSO providers, and webhook endpoints, with plan/apply semantics and revision history.
- **Webhook and API automation.** Signed outbound notifications for lifecycle events, with database-backed delivery state, retries, and a dead-letter queue so cluster nodes never double-deliver.
- Expand deployment recipes for managed Kubernetes environments.
- Explore additional observability integrations.

The identity foundation is deliberately first: rate limiting keys on the principal and token identity that SSO and MFA harden, and webhook delivery must authenticate with the same secret-storage model that already seals OIDC client secrets and TOTP shared secrets.

Explicitly not planned: ICMP, TUN/L2 VPN, P2P NAT traversal, and arbitrary remote command execution. SSH support stays limited to the existing stdio/WebSocket proxy path and is not extended into a general command-execution API.

## Contribution path

Good first issues are labeled [`good first issue`](https://github.com/nnworld/TunnelMesh/issues?q=is%3Aissue+is%3Aopen+label%3A%22good+first+issue%22). Read [CONTRIBUTING.md](https://github.com/nnworld/TunnelMesh/blob/main/CONTRIBUTING.md) before opening a pull request.
