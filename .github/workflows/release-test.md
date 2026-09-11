# Release Workflow Manual Checks

1. `bash -n scripts/build-release.sh` exits 0.
2. `VERSION=v0.0.0-test ./scripts/build-release.sh` creates six archives.
3. `dist/v0.0.0-test/manifest.json` contains `schemaVersion` 11.
4. `manifest.json` lists platforms as `linux/amd64`, `linux/arm64`, `darwin/amd64`, `darwin/arm64`, `windows/amd64`, and `windows/arm64`.
5. `sha256sum -c dist/v0.0.0-test/SHA256SUMS` passes.
6. Every archive contains all three binaries and platform service templates.
