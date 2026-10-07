# Release Workflow Manual Checks

自动化守卫见 `scripts/merge_tray_dist_test.go`、`scripts/release_workflow_test.go`、
`scripts/ci_workflow_test.go`、`deploy/macos/tray_bundle_test.go`、
`deploy/windows/tray_bundle_test.go` 与 `scripts/tray_build_tag_test.go`；下面这些需要一个真实
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

## Windows 托盘（`windows-tray` 作业，ubuntu）

18. `bash -n scripts/package-windows-tray.sh` exits 0.
19. `VERSION=v0.0.0-test ./scripts/package-windows-tray.sh` creates one `.zip` per architecture plus one `-amd64-setup.exe`, `SHA256SUMS` and `manifest.json` - from Linux or macOS, with no Windows host involved.
20. `go version -m` reports `GOOS=windows`, the matching `GOARCH`, `tags=tray` and `CGO_ENABLED=0` for each exe, and the PE `Subsystem` field reads 2 (GUI), so starting the tray from Explorer opens no console window.
21. Each archive holds `TunnelMeshClient.exe`, `README.txt`, `LICENSE` and `NOTICE` flat at the root (`unzip -l` shows no wrapping directory), and `shasum -a 256 -c SHA256SUMS` passes inside the dist directory.
22. `go run github.com/tc-hib/go-winres extract <exe>` finds exactly one `RT_MANIFEST` (per-monitor-v2 DPI, `asInvoker`, common controls v6), one `RT_GROUP_ICON/APP` and a `RT_VERSION` block whose `OriginalFilename` is `TunnelMeshClient.exe`; Explorer's Properties sheet and the taskbar icon agree with it.
23. The script exits non-zero without `makensis`, without `internal/tray/webdist/dist/index.html`, when that directory is stale relative to `web-tray/dist`, for an `ARCHES`/`INSTALLER_ARCH` combination it cannot build, and when `makensis` produced no installer file.
24. `makensis -DVERSION=... -DARCH=amd64 -DBUILD_DIR=<staged exe dir> -DICON_FILE=deploy/windows/TunnelMeshClient.ico -DOUTFILE=<path> deploy/windows/installer.nsi` compiles; the resulting installer needs no UAC prompt, installs under `%LOCALAPPDATA%\Programs\TunnelMesh Client`, and its uninstaller deletes the `HKCU\Software\Microsoft\Windows\CurrentVersion\Run` value.
25. On a machine without the WebView2 runtime the tray still starts, the notification-area tooltip says the runtime is required, "Open main window" opens the same loopback URL in the default browser, and the About tab shows the fallback banner.
26. `client.yaml`, `tray.json`, `client.lock` and `tray.log` live under `%USERPROFILE%\.config\tunnelmesh\`; running `tunnelmesh-client run -c` on the same file while the tray holds it reports the mutex error and exits non-zero, and killing the tray releases the lock without deleting anything by hand.

## 合并（`TRAY_DIST_DIR` 与 `WINDOWS_TRAY_DIST_DIR`）

27. `VERSION=v0.0.0-test TRAY_DIST_DIR=dist/v0.0.0-test/macos-tray WINDOWS_TRAY_DIST_DIR=dist/v0.0.0-test/windows-tray ./scripts/build-release.sh` publishes eleven assets, and `sha256sum -c dist/v0.0.0-test/SHA256SUMS` passes for all of them.
28. `dist/v0.0.0-test/manifest-tray.json` and `manifest-windows-tray.json` are the two tray manifests, neither overwrites the other, and `manifest.json` keeps the six-asset cross-platform schema unchanged.
29. `scripts/merge-tray-dist.sh --check <dir>` exits non-zero for a directory missing `SHA256SUMS` or `manifest.json`, for one holding nothing but those two files, and for an artifact whose bytes do not match the source checksums.
30. A wrong `TRAY_DIST_DIR` or `WINDOWS_TRAY_DIST_DIR` fails before any cross-compilation starts, not after the whole matrix. Only the hand-offs that are set get validated: a macOS-only run still works.
31. Merging refuses to overwrite an asset name that already exists in the release directory.

## workflow 结构

32. `version` resolves the tag or the dispatch input once; all three build jobs read `VERSION` from its output.
33. Only `macos-tray` runs `scripts/package-macos-tray.sh`, only `windows-tray` runs `scripts/package-windows-tray.sh`, and only `release` runs `scripts/build-release.sh` and `gh release create`.
34. Each tray job uploads its artifact with `if-no-files-found: error`; `release` downloads `macos-tray` into `tray-dist` and `windows-tray` into `windows-tray-dist` and passes both. `release` needs both tray jobs, so it cannot publish before the Windows installer exists.
35. `permissions` stays `contents: write` at the workflow level and no job other than `release` calls `gh`.
