# Distribution plan

This plan turns the repository preparation into a focused three-day launch. It is intentionally small enough to execute without a marketing team.

## Target audiences

- **Self-hosting operators** who want an alternative to managed tunnel services.
- **Platform engineers** who need private-network access with explicit policy.
- **Remote-access administrators** responsible for RBAC, audit, and revocation.
- **Go and Vue developers** interested in a production-grade control-plane codebase.

## Channel hooks

| Channel | Hook |
| --- | --- |
| GitHub | Self-hosted tunnels with a real control plane, admin console, scoped tokens, and audit |
| Reddit | Ask for feedback on a self-hosted alternative to point-to-point tunnels |
| Hacker News | Explain why tunnel tooling needs a control plane, not just a transport |
| V2EX | 中文用户视角：自建内网穿透平台的权限、路由和审计设计 |
| LinkedIn | Operator-focused angle: RBAC, scoped tokens, and audit for private-network access |
| X | Short technical pitch with the social preview and demo GIF |
| Dev.to | Publish the technical article and link the repository |
| Relevant communities | Share only where self-hosting or network access is on topic; follow each community's rules |

## Three-day launch sequence

### Day 1 — Repository readiness

- Publish a tagged release with passing CI.
- Confirm the README, screenshots, English guides, and social preview are current.
- Apply the GitHub description, topics, website, and social preview.
- Create two or three high-quality issues for early contributors.

### Day 2 — Technical article

- Publish “Why TunnelMesh needs a control plane.”
- Use the committed dashboard, WebSSH, and SFTP screenshots.
- Link the quick start, architecture overview, and security guide.
- Ask three operators for feedback before broad distribution.

### Day 3 — Community distribution

- Post to GitHub, Reddit, Hacker News, V2EX, LinkedIn, X, and Dev.to.
- Reply to every substantive comment.
- Record recurring questions and turn them into documentation updates.

## Reusable copy

**One-sentence pitch:**

> TunnelMesh is a self-hosted tunneling platform with a real control plane, scoped tokens, and a built-in admin console.

**280-character post:**

> TunnelMesh: self-hosted HTTP/TCP/UDP tunneling with an admin console, scoped tokens, Agent policy, RBAC, audit logs, and WebSSH/SFTP. Public ingress is HTTP/HTTPS/WSS only. Built with Go and Vue. https://github.com/nnworld/TunnelMesh

**Article summary:**

> Point-to-point tunnels work until you need policy, routing, and audit. TunnelMesh adds a self-hosted control plane so private-network access is explicit, scoped, and observable.

## Success metrics

- GitHub stars and forks.
- Issues and discussions opened by external users.
- README and quick-start click-through.
- Quick-start completion feedback.
- Release downloads.
- Repeat external contributors.

Treat these as learning signals, not vanity targets.

## Response rule

Answer every substantive issue within 24 hours, even if the answer is only a triage update. Record recurring questions in the documentation so the next user does not have to ask again.

