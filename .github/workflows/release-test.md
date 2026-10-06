# Release Workflow Manual Checks

自动化守卫见 `scripts/merge_tray_dist_test.go`、`scripts/release_workflow_test.go`、
`deploy/macos/tray_bundle_test.go` 与 `scripts/tray_build_tag_test.go`；下面这些需要一个真实
runner（Linux 或 macOS）才能确认，因此在改发行链路时手工走一遍。

## 跨平台矩阵（`release` 作业，ubuntu）

1. `bash -n scripts/build-release.sh` exits 0.
2. `VERSION=v0.0.0-test ./scripts/build-release.sh` creates six archives.
3. `dist/v0.0.0-test/manifest.json` contains a `schemaVersion` equal to `SchemaVersion` in `internal/storage/db.go` (currently 16); the script reads it from that file, so a mismatch means the sed extraction broke.
4. `manifest.json` lists platforms as `linux/amd64`, `linux/arm64`, `darwin/amd64`, `darwin/arm64`, `windows/amd64`, and `windows/arm64`.
5. `sha256sum -c dist/v0.0.0-test/SHA256SUMS` passes.
6. Every archive contains all three binaries and platform service templates, and no `*_test.go` file.
7. Every archive contains `README.md`, `docs/`, `LICENSE`, and `NOTICE` (Apache-2.0 4(a) and 4(d)).
8. `./scripts/build-release.sh` exits non-zero when `LICENSE` or `NOTICE` is missing from the repository root.

## macOS 托盘（`macos-tray` 作业，macOS runner）

9. `bash -n scripts/package-macos-tray.sh` and `bash -n scripts/merge-tray-dist.sh` both exit 0.
10. `VERSION=v0.0.0-test ./scripts/package-macos-tray.sh` creates one `.dmg` per architecture plus `SHA256SUMS` and `manifest.json`, and both darwin architectures come off one arm64 host.
11. Each image mounts with `TunnelMesh Client.app` beside an `Applications` symlink, and `codesign --verify --verbose=2` passes on the app inside the mounted volume.
12. `hdiutil verify` passes on every image the script writes.
13. The app inside carries `LICENSE` and `NOTICE` in `Contents/Resources`, and `CFBundleShortVersionString` is the version without the leading `v`.
14. The script exits non-zero off macOS, without `internal/tray/webdist/dist/index.html`, and when that directory is stale relative to `web-tray/dist`.
15. Each image opens as an installer window, not a folder dump: mounting it and asking Finder for the icon positions answers `170210|470210|icon view` with a 640x420 centred window, and `manifest.json` records `installerLayout: true` for both assets. The read-back is scripted in `docs/deployment/macos-client-tray.md`.
16. `DMG_BACKGROUND=<png>` puts the picture at `.background/background.png` inside the volume with the `hidden` flag set, and the image's `.DS_Store` carries a `backgroundImageAlias`.
17. When the layout cannot be applied - replace `deploy/macos/tray-dmg-layout.applescript` with a script that always fails - the build still succeeds, reports three failed attempts plus one warning on stderr, and records `installerLayout: false`. A silent downgrade to a plain folder window is the failure this guards.

## 合并（`TRAY_DIST_DIR`）

18. `VERSION=v0.0.0-test TRAY_DIST_DIR=dist/v0.0.0-test/macos-tray ./scripts/build-release.sh` publishes eight assets, and `sha256sum -c dist/v0.0.0-test/SHA256SUMS` passes for all of them.
19. `dist/v0.0.0-test/manifest-tray.json` is the tray manifest, and `manifest.json` keeps the six-asset cross-platform schema unchanged.
20. `scripts/merge-tray-dist.sh --check <dir>` exits non-zero for a directory missing `SHA256SUMS`, `manifest.json` or any `.dmg`, and for an image whose bytes do not match the source checksums.
21. A wrong `TRAY_DIST_DIR` fails before any cross-compilation starts, not after the whole matrix.
22. Merging refuses to overwrite an asset name that already exists in the release directory.

## workflow 结构

23. `version` resolves the tag or the dispatch input once; both build jobs read `VERSION` from its output.
24. Only `macos-tray` runs `scripts/package-macos-tray.sh`, and only `release` runs `scripts/build-release.sh` and `gh release create`.
25. `macos-tray` uploads an artifact named `macos-tray` with `if-no-files-found: error`; `release` downloads it into `tray-dist` and passes `TRAY_DIST_DIR=tray-dist`.
26. `permissions` stays `contents: write` at the workflow level and no job other than `release` calls `gh`.
