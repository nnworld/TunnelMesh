# GitHub growth install and templates

## Title

Improve release install experience and contributor routing

## Target branch

`main`

## Summary

Add a checksum-verifying macOS/Linux release installer, expose it from both README quick starts, and improve GitHub issue routing and PR review prompts.

## User impact

New Linux and macOS users can install a pinned TunnelMesh release into `~/.local/bin` without manually selecting an archive or checksum. Contributors receive clearer documentation, discussion, security, and security-review routing before opening a public issue or PR.

## API, schema, and configuration impact

None. This change does not modify application APIs, database schema, application configuration, service templates, or runtime behavior.

## Security impact

The installer downloads an exact tagged archive, verifies its SHA256 checksum before extraction, rejects unsupported platforms, and installs only the three release binaries. The README recommends reviewing the script before execution rather than piping remote shell input directly. Issue configuration routes vulnerability reports to the private security policy.

## Test evidence

- `bash -n scripts/install.sh`
- `go test ./scripts -count=1`
- `go test ./... -count=1`
- `go test -race ./...`
- `go vet ./...`
- `python3 scripts/gen_doc_index.py`
- `git diff --check`
- Manual `v1.1.0` release install into a temporary directory on `darwin/arm64`; all three binaries were present and executable, and `tunnelmesh-server --help` ran successfully.

Frontend commands were not run because no frontend source or embedded web asset changed.

## Release steps

1. Merge to `main`.
2. No application release is required for runtime users.
3. Include the installer in the next normal tagged release cycle; the script itself is fetched from `main` and downloads existing GitHub Release assets.

## Rollback steps

Revert the installer, installer test, README sections, GitHub templates, plan, PR record, and regenerated documentation index in one commit. Existing installed binaries and GitHub Releases are unaffected.

## Reviewer focus

Review URL construction, version validation, platform mapping, checksum comparison, temporary-directory cleanup, and the documentation warning against piping remote shell input.

## Integration status

Implementation and validation are complete on `codex/github-growth-install-templates`; integration into `main` is pending user choice.
