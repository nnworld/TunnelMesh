# P0/P1 Growth Acceleration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Repair CI, add credible visual proof and repository metadata, and expand English discovery and distribution materials.

**Architecture:** CI builds the generated web bundle before compiling Go. Visual assets live in `docs/assets/` with provenance, while both READMEs reference the same assets to keep bilingual content aligned. English guides live under `docs/en/` as concise entry points and link to the authoritative Chinese deep documentation. Community content records the exact GitHub metadata and launch plan without automating authenticated repository changes.

**Tech Stack:** GitHub Actions, Docker Compose, Go 1.23, Node.js 22, Vite, Chrome screenshots, ffmpeg, Markdown.

**Spec:** `docs/superpowers/specs/2026-09-17-p0-p1-growth-acceleration-design.md`

## Global Constraints

- Do not change runtime code, protocol behavior, or database schema.
- Do not commit secrets, tokens, private keys, production DSNs, generated web bundles, or `node_modules/`.
- English and Chinese README sections must remain semantically aligned.
- Required Go commands are exactly `go test ./... -count=1`, `go test -race ./...`, and `go vet ./...`.
- Required frontend commands are exactly `cd web && npm test -- --run` and `cd web && npm run build`.
- Required embed validation is `./scripts/verify-web-embed.sh`.
- Required Compose validation uses `TUNNELMESH_AGENT_TOKEN=ci-placeholder` and `TUNNELMESH_CLIENT_TOKEN=ci-placeholder`.
- Screenshots must come from the real TunnelMesh UI and contain no secrets or sensitive data.
- Documentation indexes must be regenerated with `python3 scripts/gen_doc_index.py`.
- Do not commit, push, merge, or create a remote PR without explicit user authorization.

---

### Task 1: Repair Clean-Checkout CI

**Files:**

- Modify: `.github/workflows/ci.yml`

**Interfaces:**

- Consumes: `web/package.json`, `web/scripts/sync-web-dist.mjs`, `internal/server/web.go`, `docker-compose.local.yml`.
- Produces: A clean-checkout CI workflow that generates `internal/server/web_dist` before Go compilation and validates Compose with non-secret placeholders.

- [x] **Step 1: Reproduce the Go failure from a clean build state**

Run:

```sh
rm -rf internal/server/web_dist
go test ./internal/server -count=1
```

Expected: FAIL with `pattern all:web_dist: no matching files found`.

- [x] **Step 2: Confirm the web build repairs the local compile**

Run:

```sh
cd web
npm ci
npm run build
cd ..
./scripts/verify-web-embed.sh
go test ./internal/server -count=1
```

Expected: the embed directories match and the focused Go test passes.

- [x] **Step 3: Replace the workflow with clean-checkout-safe jobs**

Use this workflow structure:

```yaml
name: ci

on:
  pull_request:
  push:
    branches: [main]

permissions:
  contents: read

jobs:
  build-test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-node@v4
        with:
          node-version: 22
          cache: npm
          cache-dependency-path: web/package-lock.json
      - run: cd web && npm ci
      - run: cd web && npm run build
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
          cache: true
      - run: go test ./... -count=1
      - run: go test -race ./...
      - run: go vet ./...
      - run: ./scripts/verify-web-embed.sh

  packaging:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - run: >-
          TUNNELMESH_AGENT_TOKEN=ci-placeholder
          TUNNELMESH_CLIENT_TOKEN=ci-placeholder
          docker compose -f docker-compose.local.yml config
      - run: >-
          TUNNELMESH_AGENT_TOKEN=ci-placeholder
          TUNNELMESH_CLIENT_TOKEN=ci-placeholder
          docker compose -f docker-compose.local.yml --profile agent --profile client config
```

- [x] **Step 4: Validate the workflow syntax and packaging command**

Run:

```sh
ruby -e 'require "yaml"; YAML.load_file(".github/workflows/ci.yml")'
TUNNELMESH_AGENT_TOKEN=ci-placeholder TUNNELMESH_CLIENT_TOKEN=ci-placeholder docker compose -f docker-compose.local.yml config >/dev/null
TUNNELMESH_AGENT_TOKEN=ci-placeholder TUNNELMESH_CLIENT_TOKEN=ci-placeholder docker compose -f docker-compose.local.yml --profile agent --profile client config >/dev/null
```

Expected: all commands exit 0.

Workflow YAML was validated locally. Docker is unavailable locally, so both Compose `config` commands are delegated to the `packaging` CI job.

### Task 2: Capture Real UI Assets

**Files:**

- Create: `docs/assets/admin-dashboard.png`
- Create: `docs/assets/agent-management.png`
- Create: `docs/assets/token-management.png`
- Create: `docs/assets/webssh-terminal.png`
- Create: `docs/assets/sftp-browser.png`
- Create: `docs/assets/demo.gif`
- Create: `docs/assets/social-preview.png`
- Create: `docs/assets/README.md`

**Interfaces:**

- Consumes: The locally built web bundle and a fresh SQLite Server started from commit `HEAD`.
- Produces: Real UI images used by both READMEs and a social preview for GitHub repository settings.

- [x] **Step 1: Prepare an isolated local demo**

Create a temporary YAML config outside Git with:

```yaml
mode: local
storage:
  driver: sqlite
  auto_init: true
  sqlite:
    path: /tmp/tunnelmesh-demo/tunnelmesh.db
server:
  http_addr: 127.0.0.1:18080
  webssh:
    enabled: true
```

Run:

```sh
cd web && npm run build && cd ..
go run ./cmd/tunnelmesh-server run --config /tmp/tunnelmesh-demo/server.yaml
go run ./cmd/tunnelmesh-server admin bootstrap --config /tmp/tunnelmesh-demo/server.yaml
```

Expected: the Server listens on `http://127.0.0.1:18080` and bootstrap prints one-time credentials.

- [x] **Step 2: Populate representative demo data**

Use the admin console or its API to create:

- One Agent named `demo-agent`.
- One Agent token and one Client token; copy secrets only into the temporary local process environment and never into files.
- One local target service suitable for the demo.
- One WebSSH session and one SFTP session against a local test SSH server.

Expected: the UI contains representative rows and views but no real credentials.

- [x] **Step 3: Capture screenshots with Chrome**

Use the local Chrome UI at 1440×900 or 1536×960 and save:

- `admin-dashboard.png` from the dashboard view.
- `agent-management.png` from the Agents view.
- `token-management.png` from the Tokens view.
- `webssh-terminal.png` from an active WebSSH terminal.
- `sftp-browser.png` from the SFTP file browser.

Expected: each image shows the real UI, has a stable viewport, and contains no token values or sensitive logs.

- [x] **Step 4: Produce the demo GIF**

Run:

```sh
ffmpeg -y -framerate 2 -pattern_type glob -i '/tmp/tunnelmesh-demo/frames/*.png' \
  -vf "scale=1280:-1:flags=lanczos,split[a][b];[a]palettegen[p];[b][p]paletteuse" \
  -loop 0 docs/assets/demo.gif
```

Expected: a 15–30 second GIF, preferably under 10 MB, cycling through dashboard, Agents, tokens, WebSSH, and SFTP.

- [x] **Step 5: Create the social preview**

Generate a 1280×640 PNG from the dashboard screenshot or a composed brand card. It must include:

- `TunnelMesh`
- `Self-hosted tunnels with a real control plane`
- A visual cue for the admin console.

Expected: `docs/assets/social-preview.png` is approximately 1280×640 and remains readable at GitHub card size.

- [x] **Step 6: Record asset provenance**

Write `docs/assets/README.md` with:

```markdown
# Visual assets

All screenshots come from the TunnelMesh admin console running locally from commit `<short-sha>` on `<YYYY-MM-DD>`.

| Asset | View | Dimensions | Redaction check |
| --- | --- | --- | --- |
| `admin-dashboard.png` | Dashboard | `<width>x<height>` | No token, credential, or private host |
| `agent-management.png` | Agent management | `<width>x<height>` | No token, credential, or private host |
| `token-management.png` | Token management | `<width>x<height>` | Secret values redacted or not displayed |
| `webssh-terminal.png` | WebSSH terminal | `<width>x<height>` | No credential or sensitive command output |
| `sftp-browser.png` | SFTP browser | `<width>x<height>` | No credential or private path |
| `demo.gif` | Product demo | `<width>x<height>` | Same checks as source frames |
| `social-preview.png` | GitHub social card | `1280x640` | Text only or redacted UI |
```

Expected: every asset has provenance and an explicit redaction result.

### Task 3: Apply GitHub Repository Metadata

**Files:**

- Create: `docs/community/github-metadata.md`

**Interfaces:**

- Consumes: `docs/assets/social-preview.png`.
- Produces: Exact live GitHub settings and an auditable instruction record.

- [x] **Step 1: Commit the metadata instruction**

Create `docs/community/github-metadata.md` with:

```markdown
# GitHub repository metadata

Apply these values in GitHub → Repository → Settings → General:

- **Description:** Self-hosted HTTP/TCP/UDP tunneling platform with admin console, RBAC, scoped tokens, and observability.
- **Website:** https://github.com/nnworld/TunnelMesh#readme
- **Topics:** self-hosted, tunnel, intranet-penetration, reverse-proxy, websocket, go, vue, network, remote-access, zero-trust
- **Social preview:** `docs/assets/social-preview.png`

The description is under GitHub's 350-character limit and names the primary protocols and control-plane differentiators. Topics mix product categories with implementation keywords so repository search and topic feeds can both discover TunnelMesh. No dedicated marketing site exists yet, so the README is the most accurate website target.
```

- [ ] **Step 2: Apply the settings after user confirmation**

Open GitHub repository settings, paste the exact values, upload `docs/assets/social-preview.png`, and save.

Expected: the repository About panel, topics, website link, and social preview match the committed instruction.

### Task 4: Add English Core Documentation

**Files:**

- Create: `docs/en/README.md`
- Create: `docs/en/deployment/production.md`
- Create: `docs/en/operations/security.md`
- Create: `docs/en/user-guide/agent.md`
- Create: `docs/en/user-guide/client.md`
- Create: `docs/en/user-guide/server-admin.md`

**Interfaces:**

- Consumes: Existing Chinese deep documentation under `docs/deployment/`, `docs/operations/`, and `docs/user-guide/`.
- Produces: Concise English entry points for deployment, security, and the three binaries.

- [x] **Step 1: Create the English documentation index**

`docs/en/README.md` must contain:

```markdown
# English documentation

These guides are concise English entry points. The Chinese documentation remains the authoritative deep reference.

- [Production deployment](deployment/production.md)
- [Security hardening](operations/security.md)
- [Agent guide](user-guide/agent.md)
- [Client guide](user-guide/client.md)
- [Server administration](user-guide/server-admin.md)
```

- [x] **Step 2: Write the production deployment guide**

Cover, in order:

1. Topology and prerequisites.
2. TLS/WSS and reverse-proxy requirements.
3. SQLite versus MySQL selection.
4. Secrets and environment variables.
5. systemd or Docker service setup.
6. Health checks and rollback.

Every command must use placeholders such as `<domain>`, `<token from the console>`, and `<database DSN from the secret manager>`.

- [x] **Step 3: Write the security guide**

Cover:

1. TLS and allowed hosts/origins.
2. Scoped Agent and Client tokens.
3. Agent CIDR and port policy.
4. Admin RBAC and audit logs.
5. Token encryption and reveal risk.
6. Safe logging and secret handling.

The guide must state that public ingress is HTTP/HTTPS/WSS only and that public UDP is not exposed.

- [x] **Step 4: Write the Agent guide**

Cover Agent registration, outbound WebSocket connection, policy validation, metadata allowlist, connection pool behavior, and service deployment. Include a safe example config with no real token.

- [x] **Step 5: Write the Client guide**

Cover local TCP/UDP/HTTP forwarding, SOCKS5 and HTTP proxy modes, managed HTTP publishing, SSH over WebSocket, and multi-entry `run` mode. Include one minimal forward example.

- [x] **Step 6: Write the Server administration guide**

Cover dashboard navigation, Agent lifecycle, token creation and reveal, route management, WebSSH/SFTP, audit logs, observability, and release downloads. Do not include a real token or private host.

### Task 5: Add Technical Article and Distribution Plan

**Files:**

- Create: `docs/community/why-tunnelmesh-needs-a-control-plane.md`
- Create: `docs/community/distribution-plan.md`

**Interfaces:**

- Consumes: English documentation, architecture overview, visual assets, and comparison table.
- Produces: A publishable technical article and a launch execution plan.

- [x] **Step 1: Write the technical article**

Use this outline:

1. The operational problem with point-to-point tunnels.
2. Why a control plane changes authorization, routing, and auditability.
3. TunnelMesh architecture: Server, Agent, Client, relay, and admin console.
4. Security model: TLS/WSS, scoped tokens, Agent policy, RBAC, and audit.
5. Honest trade-offs and when to choose another tool.
6. Five-minute quick-start link.

The article must be publishable in English, use the committed screenshots, and make no unverifiable performance or adoption claims.

- [x] **Step 2: Write the distribution plan**

Include:

- Target audiences: self-hosting operators, platform engineers, remote-access administrators, and Go/Vue developers.
- Channel hooks for GitHub, Reddit, Hacker News, V2EX, LinkedIn, X, Dev.to, and relevant communities.
- A three-day launch sequence: repository readiness, technical article, community distribution.
- Reusable one-sentence pitch, 280-character post, and article summary.
- Success metrics: stars, forks, issues, README click-through, quick-start completion, and release downloads.
- A rule to answer every substantive issue within 24 hours and record recurring questions in documentation.

### Task 6: Integrate Presentation, Records, and Validation

**Files:**

- Modify: `README.md`
- Modify: `README.zh-CN.md`
- Modify: `docs/README.md`
- Modify: `docs/development/documentation.md`
- Create: `docs/pull-requests/2026-09-17-p0-p1-growth-acceleration.md`

**Interfaces:**

- Consumes: All assets and English/community documents from earlier tasks.
- Produces: Bilingual README integration, documentation indexes, PR record, and validation evidence.

- [x] **Step 1: Add visual proof to both READMEs**

Insert the same asset order after the comparison table in both READMEs:

1. `docs/assets/demo.gif`
2. `docs/assets/admin-dashboard.png`
3. `docs/assets/webssh-terminal.png`
4. `docs/assets/sftp-browser.png`

Use equivalent English and Chinese captions. Keep image links relative.

- [x] **Step 2: Link English documentation**

Update the English README documentation section to link `docs/en/README.md` and the five English guides. Update the Chinese README to mention that concise English guides are available at `docs/en/README.md`.

- [x] **Step 3: Update documentation indexes and language policy**

- Add `docs/en/` and `docs/community/` to the directory responsibilities in `docs/development/documentation.md`.
- Add English and community links to `docs/README.md`.
- Run:

```sh
python3 scripts/gen_doc_index.py
```

Expected: generated plan/spec/PR indexes include this design and plan, with no orphan links.

- [x] **Step 4: Write the PR record**

Create `docs/pull-requests/2026-09-17-p0-p1-growth-acceleration.md` with title, target branch `main`, summary, user impact, API/schema/config impact, security impact, test evidence, release steps, rollback steps, reviewer focus, and integration status. State explicitly that API, schema, and runtime configuration are unchanged.

- [x] **Step 5: Run full validation**

Run:

```sh
go test ./... -count=1
go test -race ./...
go vet ./...
cd web && npm test -- --run
cd web && npm run build
./scripts/verify-web-embed.sh
TUNNELMESH_AGENT_TOKEN=ci-placeholder TUNNELMESH_CLIENT_TOKEN=ci-placeholder docker compose -f docker-compose.local.yml config >/dev/null
TUNNELMESH_AGENT_TOKEN=ci-placeholder TUNNELMESH_CLIENT_TOKEN=ci-placeholder docker compose -f docker-compose.local.yml --profile agent --profile client config >/dev/null
python3 scripts/gen_doc_index.py
git diff --check
```

Expected: every command exits 0. Record exact results in the PR document.

All local commands passed. The two Compose `config` commands were not run because Docker is unavailable locally; CI must complete them before merge.

## Rollback

- Revert the presentation, documentation, asset, and CI commit together.
- No database or runtime rollback is needed because runtime behavior and schema are unchanged.
- GitHub metadata can be manually restored from the previous values visible in repository settings.
- Remove uploaded social preview only through GitHub repository settings; Git revert does not alter GitHub metadata.
