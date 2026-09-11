# PR: Publish platform release downloads

## Target Branch

`main`

## Summary

This change adds an immutable cross-platform release pipeline and admin download surface:

- Version, commit, and UTC build-time injection into all three binaries.
- Six-platform release archives with `CGO_ENABLED=0`.
- Platform-specific Linux systemd, macOS launchd, and Windows service templates.
- Portable `SHA256SUMS` and `manifest.json`.
- GitHub Release workflow for exact `vMAJOR.MINOR.PATCH` tags and manual dispatch.
- `downloads.github_repository` configuration with a safe default.
- Administrator-only `GET /api/v1/downloads`.
- `/downloads` page with build identity, Schema version, platform cards, asset links, checksum commands, manifest link, and upgrade warning.
- Release documentation, rollback guidance, and private-repository permission notes.
- Canonical GitHub URL construction and strict `owner/name` repository validation.

## User Impact

Administrators can download the exact package matching the running Server version, verify SHA256 locally, and read the release manifest before upgrade. GitHub remains the download origin; TunnelMesh does not proxy credentials or binary content.

## API / Schema / Configuration Impact

- Adds `GET /api/v1/downloads`; administrator only.
- Adds `downloads.github_repository`, default `nnworld/TunnelMesh`, environment variable `TUNNELMESH_DOWNLOADS_GITHUB_REPOSITORY`.
- Adds public `DownloadInfo` and `DownloadAsset` OpenAPI schemas.
- No database schema change is introduced by this PR beyond the shared v11 client observability migration.
- Release manifest reports `schemaVersion: 11`.

## Security and Authorization Impact

- Download API is restricted to administrators.
- Generated URLs contain only public GitHub Release metadata.
- Release archives contain binaries, documentation, install scripts, and service templates; they do not contain configuration, databases, Tokens, passwords, private keys, or logs.
- Workflow permissions are limited to `contents: write`.
- No mutable major/minor tags are created.

## Test Evidence

Fresh verification on the final working tree:

```text
bash -n scripts/build-release.sh
PASS

VERSION=v0.0.0-test ./scripts/build-release.sh
PASS

cd dist/v0.0.0-test && sha256sum -c SHA256SUMS
6 archives OK

test "$(jq -r .schemaVersion dist/v0.0.0-test/manifest.json)" = "11"
PASS

manifest platforms
["linux/amd64","linux/arm64","darwin/amd64","darwin/arm64","windows/amd64","windows/arm64"]

archive inspection
all six archives contain all three binaries and the expected platform service templates

ruby -e 'require "yaml"; YAML.load_file(".github/workflows/release.yml")'
PASS

ruby -e 'require "yaml"; YAML.load_file("docs/api/openapi.yaml")'
PASS

cd web && npm test -- --run
13 files / 85 tests passed

cd web && npm run build
PASS

./scripts/verify-web-embed.sh
PASS

go test ./... -count=1
all packages with tests passed

go test -race ./...
all packages with tests passed; internal/server completed in 237.523s

go vet ./...
PASS

git diff --check
PASS
```

## Release Steps

1. Merge the Server and release changes to `main`.
2. Create and push an exact `vMAJOR.MINOR.PATCH` tag, or run the release workflow with that exact version.
3. Confirm workflow frontend tests/build, embed verification, Go tests, race, vet, packaging, checksum, and manifest checks pass.
4. Confirm the GitHub Release contains six archives, `SHA256SUMS`, and `manifest.json`.
5. Verify the admin `/downloads` page points to the same immutable tag.

## Rollback Steps

1. Select the previous immutable GitHub Release.
2. Verify its `SHA256SUMS`.
3. Stop the current service, replace binaries and service templates, and preserve configuration, data, and secrets.
4. Start the previous version and verify health, logs, Client connections, and core proxy paths.
5. If the database was migrated, follow the Schema v11 rollback guidance; do not issue guessed reverse DDL.

## Reviewer Focus

- Workflow accepts only exact `vMAJOR.MINOR.PATCH` releases.
- All release binaries receive the same version, commit, and UTC build time.
- `SHA256SUMS` uses archive filenames so verification works after download.
- Manifest `platforms` and asset names match the documented contract.
- Each archive includes only the service template appropriate for its platform.
- Download API and page expose no secrets and do not proxy GitHub credentials.
- The downloads route and page are administrator-only; unknown `/api/v1/downloads` subpaths return 404.
- GitHub asset URLs remain canonical and do not over-encode repository path separators.

## Integration Status

- Build script, workflow, OpenAPI, admin UI, and documentation are implemented and verified locally.
- GitHub Actions execution and a real GitHub Release creation remain pending until the authorized tag or manual workflow run is created.
