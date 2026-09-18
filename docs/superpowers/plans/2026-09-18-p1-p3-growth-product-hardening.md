# P1–P3 growth and product hardening implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Complete the next repository-side P1–P3 batch: workflow/security hygiene, discovery content, and first-use safety.

**Architecture:** Keep GitHub Releases authoritative and add only repository-local growth assets. Add a shared `doctor` command at the CLI boundary so Server, Agent, and Client expose the same safe diagnostics surface without bypassing configuration or storage layers. Extend the existing installer contract without changing its default behavior.

**Tech Stack:** Go 1.23, Cobra, existing SQLite/MySQL storage layer, Bash installer, GitHub Actions, GitHub Pages from `/docs`, Markdown documentation.

**Spec:** `docs/community/distribution-plan.md`, `docs/development/testing.md`, `docs/architecture/overview.md`

## Global Constraints

- Do not add `go test ./... -count=1`, `go test -race ./...`, or `go vet ./...` to CI; the user explicitly rejected that change.
- Do not fabricate benchmarks, adoption numbers, screenshots, or comparisons.
- Do not modify API behavior, database schema, service templates, or release asset layout.
- Do not print tokens, passwords, private keys, DSNs, Authorization headers, or unredacted logs.
- Keep English and Chinese README parity where the same user-facing behavior changes.
- Run full local Go validation even though CI intentionally remains lightweight.

---

### Task 1: P1 repository and workflow hygiene

**Files:**

- Modify: `.github/workflows/ci.yml`
- Modify: `.github/workflows/release.yml`
- Create: `.github/dependabot.yml`
- Modify: `README.md`
- Modify: `README.zh-CN.md`
- Create: `docs/pull-requests/2026-09-18-p1-p3-growth-product-hardening.md`

**Interfaces:**

- Produces: GitHub Actions using current Node-24-compatible action majors.
- Produces: Dependabot configuration for GitHub Actions, Go modules, and npm.
- Produces: README install examples pinned to `v1.1.1`.

**Steps:**

- [x] Update action versions:
  - `actions/checkout@v7.0.1`
  - `actions/setup-node@v7.0.0`
  - `actions/setup-go@v7.0.0`
- [x] Change release workflow Node from 20 to 22.
- [x] Add `.github/dependabot.yml` with weekly updates for:
  - `github-actions` at `/`
  - `gomod` at `/`
  - `npm` at `/web`
- [x] Replace README install examples from `v1.1.0` to `v1.1.1`.
- [x] Run `git diff --check`.
- [ ] After merge, enable repository security settings through GitHub API:
  - Dependabot security updates
  - Secret scanning
  - Secret scanning push protection

**Verification:**

```bash
git diff --check
```

Expected: no output.

---

### Task 2: P2 discovery and documentation site

**Files:**

- Create: `docs/_config.yml`
- Create: `docs/index.md`
- Create: `docs/community/comparison.md`
- Create: `docs/community/roadmap.md`
- Modify: `docs/README.md`
- Modify: `README.md`
- Modify: `README.zh-CN.md`

**Interfaces:**

- Produces: GitHub Pages source from `main` and `/docs`.
- Produces: English comparison page at `docs/community/comparison.md`.
- Produces: public roadmap at `docs/community/roadmap.md`.

**Steps:**

- [x] Add Jekyll Pages configuration:
  - title: `TunnelMesh Docs`
  - description: `Self-hosted tunneling platform documentation`
  - theme: `minima`
- [x] Add `docs/index.md` as a bilingual documentation entry page.
- [x] Add a comparison page that compares only documented TunnelMesh capabilities and explicitly labels competitor capabilities as “varies” where public behavior differs by deployment.
- [x] Add a roadmap page with three sections:
  - `Now`
  - `Next`
  - `Later`
- [x] Link the comparison and roadmap pages from both root README files and `docs/README.md`.
- [x] Run `git diff --check`.
- [ ] After merge, enable GitHub Pages from `main` and `/docs`, then verify the returned HTML status is `200`.

**Verification:**

```bash
git diff --check
```

Expected: no output.

---

### Task 3: P3 first-use safety

**Files:**

- Modify: `internal/cli/root.go`
- Modify: `internal/cli/root_test.go`
- Modify: `scripts/install.sh`
- Modify: `scripts/install_script_test.go`
- Modify: `docs/user-guide/quickstart.md`

**Interfaces:**

- Produces: `tunnelmesh-server doctor`
- Produces: `tunnelmesh-agent doctor`
- Produces: `tunnelmesh-client doctor`
- Produces: installer flags `--dry-run` and `--print-checksum`

**Doctor behavior:**

- Server:
  1. Load and validate configuration.
  2. Open configured storage, which pings the database and validates schema state.
  3. Close storage.
  4. Print `configuration: ok` and `storage: ok (<driver>)`.
- Agent and Client:
  1. Load and validate configuration.
  2. Convert the configured `ws://` or `wss://` endpoint to `http://` or `https://`.
  3. Request `/health/ready` with a five-second timeout and no Authorization header.
  4. Print `configuration: ok` and `server health: ok`.

**Installer behavior:**

- `--dry-run` resolves version and platform locally, prints planned URLs and install paths, and performs no network request or file write.
- `--print-checksum` downloads the archive and `SHA256SUMS`, verifies the checksum, prints it, and exits before extraction or installation.

**Steps:**

- [x] Write failing CLI tests that assert all three roots expose `doctor`.
- [x] Write failing CLI tests that execute Server `doctor` with a temporary SQLite config and expect configuration/storage success output.
- [x] Write failing CLI tests that execute Agent `doctor` against an `httptest.Server` and expect configuration/server-health success output.
- [x] Run:

```bash
go test ./internal/cli -count=1
```

Expected before implementation: failures because `doctor` is missing.

- [x] Implement `doctorCommand` and endpoint helpers in `internal/cli/root.go`.
- [x] Add the command to Server, Agent, and Client command factories.
- [x] Write failing installer contract tests for `--dry-run` and `--print-checksum`.
- [x] Implement both installer flags without changing default behavior.
- [x] Document `doctor`, `--dry-run`, and `--print-checksum` in the quick start.
- [x] Run:

```bash
bash -n scripts/install.sh
go test ./internal/cli -count=1
go test ./scripts -count=1
```

Expected: all pass.

---

### Task 4: Full validation and PR record

**Files:**

- Modify: `docs/pull-requests/2026-09-18-p1-p3-growth-product-hardening.md`
- Regenerate: generated documentation indexes

**Steps:**

- [x] Run:

```bash
go test ./... -count=1
go test -race ./...
go vet ./...
bash -n scripts/install.sh
go test ./scripts -count=1
python3 scripts/gen_doc_index.py
git diff --check
```

- [x] Record actual validation output and rollback steps in the PR description.
- [x] Create a feature branch and pull request after validation.
- [ ] Do not merge without the required approving review because `main` is protected.

**Rollback:**

Revert the feature branch commit. GitHub Pages, security settings, and branch protection are repository settings and must be reverted separately through GitHub if needed.
