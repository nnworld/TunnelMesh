# GitHub growth install and template plan

## Goal

Reduce first-use friction and improve contributor routing without duplicating existing project assets. The change adds a checksum-verifying macOS/Linux release installer, exposes it from both README quick starts, and makes GitHub issue routing explicit.

## Architecture decisions

- Keep release distribution authoritative on GitHub Releases. The new installer downloads an exact tagged archive and its `SHA256SUMS`, verifies the checksum, and only then copies the three binaries.
- Keep service installation separate. The existing platform scripts remain responsible for systemd, launchd, and WinSW setup because they already have repository consistency tests and avoid generating duplicate templates.
- Keep the installer user-local by default (`~/.local/bin`) to avoid accidental privileged writes. Operators can pass `--install-dir /usr/local/bin` when that is intended.
- Do not execute remote shell directly. `curl ... | sh` would hide the script before execution; users download and review the installer, or pass it through `bash` after download.

## Technical stack

- Bash for the macOS/Linux installer because the required operations are download, checksum, extraction, and copy.
- Go tests in `scripts/` to protect installer contract and security-relevant behavior.
- GitHub issue template configuration using `.github/ISSUE_TEMPLATE/config.yml`.

## Specification references

- `docs/deployment/binary-release.md` defines release archive naming, supported platforms, and `SHA256SUMS`.
- `deploy/README.md` defines the boundary between release bootstrap and service installation.
- `README.md` and `README.zh-CN.md` define bilingual quick-start expectations.

## Global constraints

- Do not add secrets, tokens, private keys, production DSNs, or unredacted logging.
- Do not modify release artifacts, service templates, API behavior, or schema.
- Keep comments focused on why behavior is required rather than restating implementation.
- Preserve English and Chinese documentation parity.

## Exact files

- Add `scripts/install.sh`.
- Add `scripts/install_script_test.go`.
- Add `.github/ISSUE_TEMPLATE/config.yml`.
- Update `.github/PULL_REQUEST_TEMPLATE.md`.
- Update `README.md`.
- Update `README.zh-CN.md`.
- Add `docs/pull-requests/2026-09-17-github-growth-install-and-templates.md`.
- Regenerate generated documentation indexes.

## Task interfaces

- Installer CLI: `scripts/install.sh [--version vMAJOR.MINOR.PATCH] [--install-dir DIR]`.
- Default version resolution uses the GitHub Releases `latest` endpoint and then downloads only that exact tag.
- Supported platforms are `linux/amd64`, `linux/arm64`, `darwin/amd64`, and `darwin/arm64`.
- The installer exits non-zero for unsupported platforms, invalid version syntax, missing tools, download failure, checksum mismatch, missing binaries, or install failure.

## TDD steps

1. Add `scripts/install_script_test.go` with failing assertions for exact-version support, checksum verification, user-local default, unsupported-platform rejection, and binary copying.
2. Run `go test ./scripts -count=1` and confirm the installer-contract tests fail because `scripts/install.sh` does not exist.
3. Implement the minimal installer and issue routing configuration.
4. Extend both README quick starts with the reviewed installer flow and service-install next step.
5. Strengthen the PR template with plan and documentation prompts while retaining current test evidence and security warning.

## Expected failure result

Before implementation, `go test ./scripts -count=1` reports missing `scripts/install.sh` or missing required contract strings, proving the new tests are effective.

## Minimal implementation

- A Bash script with strict mode, argument parsing, platform detection, exact release URL construction, checksum verification, temporary extraction, and binary installation.
- A small contract test suite that reads and validates the script without requiring network access.
- GitHub issue configuration that disables blank issues and links documentation, security policy, and Discussions.
- README quick-start install sections that use the new installer and keep source builds as the alternative.

## Expected pass result

- `bash -n scripts/install.sh` succeeds.
- `go test ./scripts -count=1` succeeds.
- Full required Go validation succeeds.
- Documentation indexes include the new plan with no orphan records.
- `git diff --check` succeeds.

## Validation commands

```bash
bash -n scripts/install.sh
go test ./scripts -count=1
go test ./... -count=1
go test -race ./...
go vet ./...
python3 scripts/gen_doc_index.py
git diff --check
```

## Rollback notes

The change is additive and has no runtime impact on shipped binaries. Revert the installer, test, README/template changes, and regenerated index in one commit to restore the previous state.
