# P1–P3 growth and product hardening

## Title

Improve repository hygiene, discovery content, and first-use safety

## Target branch

`main`

## Summary

Upgrade GitHub Actions, add dependency update policies, publish a documentation-site entry and growth content, and add safe first-use diagnostics to the three binaries and release installer.

## User impact

New users get a current install example, a public documentation site, a factual comparison page, a public roadmap, a `doctor` command for Server, Agent, and Client, and installer safety flags. Maintainers get current workflow actions and automated dependency update requests.

## API, schema, and configuration impact

No HTTP API, database schema, or configuration format changes. The change adds CLI commands and installer flags only.

## Security impact

The `doctor` command sends unauthenticated health requests only to the configured Server origin, never includes tokens in output or requests, and validates storage without modifying schema. The installer's `--dry-run` mode performs no network or filesystem writes; `--print-checksum` verifies the release checksum before printing it and does not install files.

## Tests run

- `go test ./... -count=1`
- `go test -race ./...`
- `go vet ./...`
- `go test ./internal/cli -count=1`
- `go test ./scripts -count=1`
- `bash -n scripts/install.sh`
- `./scripts/install.sh --dry-run --version v1.1.1 --install-dir /tmp/tunnelmesh-doctor-check`
- `./scripts/install.sh --print-checksum --version v1.1.1`
- YAML parsing for `.github/dependabot.yml`, `.github/workflows/ci.yml`, `.github/workflows/release.yml`, and `docs/_config.yml`
- `python3 scripts/gen_doc_index.py`
- `git diff --check`

The `--dry-run` check confirmed no network request or install write. The `--print-checksum` check verified the `v1.1.1` archive checksum `bb8be2a3100be0ee9c25ac34ae4a2f393ea3787f42188fe77a6b66be0c9b8bf6` without installing files.

## Release steps

1. Merge the pull request after required review and CI checks.
2. Enable GitHub Pages from `main` and `/docs`.
3. Enable Dependabot security updates, secret scanning, and secret scanning push protection.
4. Include the changes in the next normal release; no schema migration is required.

## Rollback steps

Revert the pull request. Disable GitHub Pages and repository security features separately if rollback of the operational settings is also required.

## Reviewer focus

Review workflow action versions, doctor health URL conversion, storage lifecycle, timeout behavior, checksum handling, and comparison-page claims.

## Integration status

Implementation and local validation are complete on `codex/p1-p3-growth-hardening`. Pull-request review and merge are pending.
