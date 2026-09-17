# P0/P1 Growth Acceleration Design

## Goal

Close the highest-impact GitHub growth gaps: make CI pass from a clean checkout, add credible visual proof, complete repository metadata, and provide enough English documentation and shareable technical content for international discovery.

## Current-state findings

- `v1.0.3` is the latest release and points to commit `b235ec4`, so the release requirement is already satisfied.
- The first `ci` run on `b235ec4` failed in 48 seconds.
- The `go` job failed because `internal/server/web_dist` is generated and absent from a clean checkout; `go:embed all:web_dist` therefore cannot compile.
- The `packaging` job failed independently; its first Compose validation command does not provide the required Agent/Client token environment values used by Compose interpolation.
- The repository currently has one English quick start, while the deeper user, deployment, operations, and security guides are Chinese.
- No real UI screenshots or demo GIF are committed.
- GitHub About, topics, website, and social preview are repository settings and cannot be encoded in Git; they require an explicit GitHub UI/API update after the assets are pushed.

## Scope

### P0

- Repair CI so a clean checkout passes.
- Capture real admin console, WebSSH, and SFTP screenshots from a local TunnelMesh instance.
- Produce a short demo GIF and a 1280×640 social preview.
- Embed the visual proof in both READMEs.
- Record and apply the exact GitHub repository metadata after user confirmation.
- Treat the existing `v1.0.3` release as the credible release; do not create another release for presentation-only changes.

### P1

- Add concise English core guides for production deployment, security, Agent, Client, and Server administration.
- Add an English technical article explaining why self-hosted tunneling needs a control plane.
- Add a distribution plan with concrete channels, hooks, and reusable copy.
- Keep Chinese documentation as the authoritative deep reference and clearly position English documents as concise entry points.

## Non-goals

- No runtime feature changes.
- No database schema changes.
- No full translation of every Chinese document.
- No fabricated screenshots, benchmarks, or adoption claims.
- No automated GitHub metadata update using credentials.
- No social-media posting; the user controls actual publication.

## Requirements

1. CI must build the web bundle before running Go commands that compile the embedded admin console.
2. CI must supply non-secret placeholder values for Compose interpolation during static configuration validation.
3. CI must continue to run the project's required Go, frontend, embed, and Compose checks.
4. Screenshots must come from a real local TunnelMesh UI and must not contain real tokens, private hosts, or sensitive logs.
5. Visual assets must be committed under `docs/assets/`, be reasonably sized for GitHub rendering, and include provenance.
6. The demo GIF must be 15–30 seconds, and the social preview must be approximately 1280×640.
7. English and Chinese READMEs must remain semantically aligned.
8. English core documentation must be concise, accurate, and linked from the English README.
9. GitHub metadata must use this exact recommendation:
   - Description: `Self-hosted HTTP/TCP/UDP tunneling platform with admin console, RBAC, scoped tokens, and observability.`
   - Topics: `self-hosted`, `tunnel`, `intranet-penetration`, `reverse-proxy`, `websocket`, `go`, `vue`, `network`, `remote-access`, `zero-trust`
   - Website: `https://github.com/nnworld/TunnelMesh#readme`
   - Social preview: `docs/assets/social-preview.png`
10. The technical article must explain the control-plane problem, architecture, security model, and honest trade-offs without disparaging alternatives.
11. The distribution plan must contain channel-specific hooks, target communities, launch sequence, and success metrics.
12. No secrets, private keys, production DSNs, real credentials, or unredacted logs may be committed.

## Acceptance criteria

- A clean CI checkout passes all jobs.
- `v1.0.3` remains the latest release.
- Both READMEs show real dashboard, Agent/token management, WebSSH, and SFTP visuals.
- `docs/assets/social-preview.png` is approximately 1280×640.
- `docs/assets/README.md` records the capture source, TunnelMesh commit, dimensions, and redaction checks.
- English readers can complete production preparation, security hardening, Agent setup, Client usage, and Server administration without reading Chinese.
- The technical article and distribution plan are complete and internally linked.
- Documentation indexes are regenerated.
- All required local validation commands pass.

## Risks and mitigations

- **Screenshot drift:** The UI may change later. `docs/assets/README.md` records the commit and capture date, making refresh auditable.
- **Large GIF size:** Use 1280-pixel width, 10–12 fps, and palette compression; target under 10 MB.
- **Translation drift:** English documents are intentionally concise entry points and link to the authoritative Chinese deep guides.
- **GitHub metadata is external:** The exact values are committed as instructions, but the live update requires user confirmation and authenticated GitHub access.
- **CI runtime:** Building the web bundle before Go tests adds time but is required by `go:embed`; packaging remains a separate job to isolate failures.
