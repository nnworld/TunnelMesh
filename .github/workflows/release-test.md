# Release Workflow Manual Checks

1. `bash -n scripts/build-release.sh` exits 0.
2. `VERSION=v0.0.0-test ./scripts/build-release.sh` creates six archives.
3. `dist/v0.0.0-test/manifest.json` contains a `schemaVersion` equal to `SchemaVersion` in `internal/storage/db.go` (currently 13); the script reads it from that file, so a mismatch means the sed extraction broke.
4. `manifest.json` lists platforms as `linux/amd64`, `linux/arm64`, `darwin/amd64`, `darwin/arm64`, `windows/amd64`, and `windows/arm64`.
5. `sha256sum -c dist/v0.0.0-test/SHA256SUMS` passes.
6. Every archive contains all three binaries and platform service templates, and no `*_test.go` file.
7. Every archive contains `README.md`, `docs/`, `LICENSE`, and `NOTICE` (Apache-2.0 4(a) and 4(d)).
8. `./scripts/build-release.sh` exits non-zero when `LICENSE` or `NOTICE` is missing from the repository root.
