# GitHub Growth Optimization Design

## Goal

Improve the repository's first-screen conversion and trust signals without changing TunnelMesh runtime behavior.

## Scope

This batch optimizes the repository presentation only:

- Rewrite the English and Chinese README first screens around the user outcome.
- Add a rendered architecture diagram and a concise comparison table.
- Add pull-request CI that runs the project's required Go, frontend, embed, and Docker checks.
- Add security, contribution, issue, and pull-request templates.
- Add an English five-minute quick start and a Docker-first demo entry.

## Non-goals

- No runtime feature changes.
- No database schema changes.
- No marketing campaign execution.
- No GitHub repository metadata automation; About, Topics, and social preview must be set in the GitHub UI.

## Requirements

1. The README must communicate the project's value within the first screen.
2. English and Chinese README content must stay semantically aligned.
3. The architecture diagram must render on GitHub without external image hosting.
4. The comparison table must be factual and avoid disparaging competing projects.
5. CI must run on every pull request and `main` push.
6. CI must run `go test ./... -count=1`, `go test -race ./...`, `go vet ./...`, `cd web && npm test -- --run`, `cd web && npm run build`, `./scripts/verify-web-embed.sh`, and `docker compose -f docker-compose.local.yml config`.
7. Security documentation must state supported versions, reporting expectations, and safe disclosure expectations.
8. Contribution documentation must state local validation commands and PR requirements.
9. Issue and pull-request templates must collect reproducible, non-secret information.
10. The quick start must get a user from clone to a local Server and Agent in five minutes.

## Acceptance criteria

- README first screen contains badges, outcome summary, quick links, and a visual architecture diagram.
- README contains a factual comparison table and a Docker-first quick start.
- CI workflow passes the required commands locally.
- Governance files and templates exist and contain no secrets.
- The English quick start is complete and does not require the user to read Chinese documentation.
- Documentation indexes are regenerated.

## Risks

- README rewrite may accidentally weaken existing technical detail; mitigation is to preserve the full Quick Start and link to deeper docs.
- CI may be slower than desired; mitigation is to run Go and frontend jobs in parallel.
- Docker checks may require Docker in the execution environment; mitigation is to use `docker compose config` for static validation and keep runtime E2E separate.
