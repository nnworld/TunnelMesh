# Cross-Platform Release and Operations Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver reproducible TunnelMesh binary archives and container images for the supported platforms, together with version reporting, health probes, Nginx WSS guidance, process-manager definitions, safe upgrade/rollback procedures, and tag-gated GitHub releases.

**Architecture:** Release metadata is injected into the three existing Cobra binaries through `internal/build`; a server healthcheck command consumes the health endpoints produced by subproject B. GoReleaser cross-compiles each binary independently with `CGO_ENABLED=0`, while deployment templates remain version-controlled under `deploy/` and Docker uses a non-root, non-privileged internal listener. Local `make release` only prepares and verifies artifacts; only the explicit `v*` tag workflow may publish a GitHub release after all tests and archive checks pass.

**Tech Stack:** Go 1.23+, Cobra, GoReleaser v2.18.1, GNU/BSD Make, Vue 3/Vite, Nginx 1.27, systemd, launchd, WinSW v2.12.0, PowerShell 7/Windows PowerShell 5.1, Docker BuildKit/Buildx, Docker Compose, GitHub Actions.

**Spec:** `docs/superpowers/specs/2026-09-06-security-observability-release-design.md`

## Global Constraints

- This plan implements only subproject C: release and operations. Token persistence, scoped authorization, runtime metrics, structured logging, `/health/live`, `/health/ready`, and WebSocket lifecycle behavior belong to subprojects A and B.
- Start this plan only after subproject A has stabilized Agent and Client Bearer-token semantics and subproject B exposes `GET /health/live`, `GET /health/ready`, graceful GOAWAY drain, and systemd readiness/watchdog notifications.
- Public traffic remains HTTP/HTTPS/WebSocket on ports 80/443. Nginx terminates edge TLS and proxies to the Server on a non-privileged loopback port.
- Supported binary targets are `linux/amd64`, `linux/arm64`, `darwin/amd64`, `darwin/arm64`, `windows/amd64`, and `windows/arm64` for each of `tunnelmesh-server`, `tunnelmesh-agent`, and `tunnelmesh-client`.
- Every release build sets `CGO_ENABLED=0`, `-trimpath`, version, commit, and UTC build time. Unix archives use `.tar.gz`; Windows archives use `.zip`.
- `make release VERSION=v0.1.0` validates and builds local artifacts only. It must not create a tag, commit, push, publish, or mutate tracked embedded Web assets.
- A release archive contains its binary and non-secret release documentation. It must not contain runtime YAML/TOML/JSON configuration, `.env`, Token values, passwords, DSNs, private keys, certificates, SQLite files, MySQL dumps, or logs.
- WinSW is pinned to `v2.12.0`. The only downloaded executable is `WinSW-x64.exe`, whose SHA256 is `05b82d46ad331cc16bdc00de5c6332c1ef818df8ceefcd49c726553209b3a0da`.
- Never use `sc.exe create` to point Windows SCM directly at a TunnelMesh console executable. PowerShell installs WinSW first, then uses `sc.exe failure` only to configure recovery actions on the WinSW-created service.
- systemd units use `Type=notify` and `WatchdogSec` only after subproject B has implemented `READY=1`, `STOPPING=1`, and periodic `WATCHDOG=1` notifications.
- Docker runtime processes run as non-root and bind to port `8080` inside the container. Nginx or host port publishing owns ports 80/443.
- Nginx must preserve `Host`, explicitly forward `Authorization` and trusted `X-Forwarded-*` values, disable WebSocket buffering, and use WebSocket timeouts longer than three 30-second heartbeat periods.
- Runtime configuration and secrets remain external to binaries, archives, images, unit files, plists, and WinSW XML. Examples use paths and environment-file references, never credential values.
- Each code or script change follows RED, GREEN, refactor: add a failing focused test, record the expected failure, implement the minimum behavior, then run focused and full verification.
- Do not commit, tag, push, merge, or publish while executing this plan unless the user separately and explicitly authorizes that Git action.

---

## File Structure

### Build and release core

- `internal/build/build.go`: single source of version, commit, build time, binary names, and formatted version output.
- `internal/build/build_test.go`: defaults and ldflag-compatible metadata formatting contract.
- `internal/cli/root.go`: attaches version output to all three roots and registers the Server healthcheck command.
- `internal/cli/root_test.go`: CLI-level `--version` and command-surface tests.
- `internal/cli/healthcheck.go`: bounded HTTP liveness probe used by operators and the Server container.
- `internal/cli/healthcheck_test.go`: status, timeout, body-closing, and URL validation tests.
- `internal/config/config.go`: changes the default Server HTTP listener from `:80` to `:8080`.
- `internal/config/config_test.go`: protects the non-privileged default.
- `.goreleaser.yaml`: three builds, six target tuples, three archive families, and one checksum file.
- `scripts/verify-web-embed.sh`: proves `web/dist` and `internal/server/web_dist` contain the same production bundle without modifying tracked files.
- `scripts/verify-release.sh`: validates archive matrix, filenames, checksums, content exclusions, and version strings.
- `Makefile`: release preflight and local artifact orchestration.

### Deployment assets

- `deploy/nginx/tunnelmesh.conf`: complete HTTP-to-HTTPS redirect, TLS server, API, SPA/managed-host, Agent WS, and Client WS proxy configuration.
- `deploy/nginx/proxy-headers.conf`: one trusted upstream-header policy shared by HTTP and WebSocket locations.
- `deploy/systemd/tunnelmesh-server.service`: Server configuration validation, notify/watchdog, restart, hardening, and state directory.
- `deploy/systemd/tunnelmesh-agent.service`: Agent validation, notify/watchdog, restart, and restricted filesystem access.
- `deploy/launchd/io.tunnelmesh.server.plist`: macOS Server daemon definition.
- `deploy/launchd/io.tunnelmesh.agent.plist`: macOS Agent daemon definition.
- `deploy/windows/tunnelmesh-server.xml`: WinSW Server service definition.
- `deploy/windows/tunnelmesh-agent.xml`: WinSW Agent service definition.
- `deploy/windows/install-service.ps1`: architecture check, pinned WinSW download, SHA256 verification, installation, and recovery actions.
- `deploy/windows/uninstall-service.ps1`: stop/uninstall wrappers without deleting operator configuration or data.

### Containers, documentation, and CI

- `Dockerfile`: non-root port 8080, build metadata, three runtime targets, and Server healthcheck.
- `docker-compose.local.yml`: correct Agent WS path, external Token injection, health-gated startup, and non-privileged container port.
- `docker-compose.cluster.yml`: MySQL health, Server readiness, external secrets, and non-privileged ports.
- `docs/deployment/binary-release.md`: download, checksum, install, first start, logs, and uninstall.
- `docs/deployment/nginx.md`: TLS/WSS deployment and smoke checks.
- `docs/deployment/linux-systemd.md`: unit installation, user/directory ownership, watchdog, logs, and removal.
- `docs/deployment/macos-launchd.md`: plist installation, bootstrap/bootout, logs, and removal.
- `docs/deployment/windows-service.md`: WinSW installation, recovery, logs, architecture limitation, and removal.
- `docs/operations/upgrade-rollback.md`: preflight, backups, canary, database compatibility, rollback, and Token rotation.
- `docs/deployment/docker.md`: immutable image tags, health, backup, upgrade, and rollback commands.
- `docs/operations/configuration.md`: port 8080 and service-manager configuration paths.
- `docs/operations/troubleshooting.md`: health, proxy, service-manager, and release checksum diagnostics.
- `docs/README.md` and `README.md`: navigation to the release and operations guides.
- `.github/workflows/release.yml`: tag-only quality gate, local-style artifact generation, explicit GitHub publication, and native smoke jobs.

---

### Task 1: Version Metadata and `--version`

**Files:**
- Modify: `internal/build/build.go`
- Modify: `internal/build/build_test.go`
- Modify: `internal/cli/root.go`
- Modify: `internal/cli/root_test.go`

**Interfaces:**
- Consumes: Go linker `-X github.com/tunnelmesh/tunnelmesh/internal/build.Version=...`, `-X .../build.Commit=...`, and `-X .../build.BuildTime=...`.
- Produces: `build.Info{Version string, Commit string, BuildTime string}`, `build.Current() build.Info`, `build.String() string`, and identical Cobra `--version` output on all three binaries.

- [ ] **Step 1: Write failing build metadata tests**

Add tests that save and restore the package variables so the suite is order-independent:

```go
func TestCurrentReturnsLinkerMetadata(t *testing.T) {
	originalVersion, originalCommit, originalBuildTime := build.Version, build.Commit, build.BuildTime
	t.Cleanup(func() {
		build.Version, build.Commit, build.BuildTime = originalVersion, originalCommit, originalBuildTime
	})
	build.Version = "v0.1.0"
	build.Commit = "0123456789abcdef"
	build.BuildTime = "2026-09-06T10:00:00Z"

	want := build.Info{Version: "v0.1.0", Commit: "0123456789abcdef", BuildTime: "2026-09-06T10:00:00Z"}
	if got := build.Current(); got != want {
		t.Fatalf("Current() = %#v, want %#v", got, want)
	}
}

func TestStringIsStableAndSingleLine(t *testing.T) {
	originalVersion, originalCommit, originalBuildTime := build.Version, build.Commit, build.BuildTime
	t.Cleanup(func() {
		build.Version, build.Commit, build.BuildTime = originalVersion, originalCommit, originalBuildTime
	})
	build.Version, build.Commit, build.BuildTime = "v0.1.0", "0123456789abcdef", "2026-09-06T10:00:00Z"
	if got, want := build.String(), "v0.1.0 commit=0123456789abcdef built=2026-09-06T10:00:00Z"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}
```

- [ ] **Step 2: Run the focused tests and record RED**

Run:

```bash
go test ./internal/build -run 'Test(CurrentReturnsLinkerMetadata|StringIsStableAndSingleLine)' -count=1
```

Expected: compilation fails because `build.Version`, `build.Commit`, `build.BuildTime`, `build.Info`, `build.Current`, and `build.String` do not exist.

- [ ] **Step 3: Implement the metadata contract**

Keep `BinaryNames()` unchanged and add:

```go
var (
	Version   = "dev"
	Commit    = "unknown"
	BuildTime = "unknown"
)

type Info struct {
	Version   string
	Commit    string
	BuildTime string
}

func Current() Info {
	return Info{Version: Version, Commit: Commit, BuildTime: BuildTime}
}

func String() string {
	info := Current()
	return fmt.Sprintf("%s commit=%s built=%s", info.Version, info.Commit, info.BuildTime)
}
```

Import `fmt`; do not read Git state or the wall clock at runtime.

- [ ] **Step 4: Add failing CLI version tests**

Add a table-driven test for all roots:

```go
func TestRootsExposeBuildVersion(t *testing.T) {
	for name, factory := range map[string]func() *cobra.Command{
		"tunnelmesh-server": cli.NewServerRoot,
		"tunnelmesh-agent":  cli.NewAgentRoot,
		"tunnelmesh-client": cli.NewClientRoot,
	} {
		t.Run(name, func(t *testing.T) {
			root := factory()
			root.SetArgs([]string{"--version"})
			var out bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&out)
			if err := root.ExecuteContext(context.Background()); err != nil {
				t.Fatal(err)
			}
			for _, part := range []string{name, "commit=", "built="} {
				if !strings.Contains(out.String(), part) {
					t.Fatalf("version output %q does not contain %q", out.String(), part)
				}
			}
		})
	}
}
```

- [ ] **Step 5: Run the CLI test and record RED**

Run:

```bash
go test ./internal/cli -run TestRootsExposeBuildVersion -count=1
```

Expected: FAIL because the Cobra roots have no version string and reject or ignore `--version`.

- [ ] **Step 6: Attach the version to every root**

In `newRoot`, import `internal/build` and set:

```go
Version: build.String(),
```

Set a stable template once:

```go
root.SetVersionTemplate("{{.Name}} {{.Version}}\n")
```

- [ ] **Step 7: Run focused and package tests and record GREEN**

Run:

```bash
go test ./internal/build ./internal/cli -count=1
```

Expected: PASS; each root prints one line containing the command name, version, commit, and UTC build time.

---

### Task 2: Server Healthcheck CLI and Non-Privileged Default Port

**Files:**
- Create: `internal/cli/healthcheck.go`
- Create: `internal/cli/healthcheck_test.go`
- Modify: `internal/cli/root.go`
- Modify: `internal/cli/root_test.go`
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`

**Interfaces:**
- Consumes: subproject B `GET /health/live`, which returns HTTP 200 only while the Server process is live; readiness remains available separately at `GET /health/ready`.
- Produces: `newHealthcheckCommand(defaultURL string) *cobra.Command`, `checkHealth(ctx context.Context, client *http.Client, endpoint string) error`, and `tunnelmesh-server healthcheck --url URL --timeout DURATION`.

- [ ] **Step 1: Write failing healthcheck unit tests**

Cover 200, non-200, invalid scheme, and timeout with `httptest.Server`:

```go
func TestCheckHealthAcceptsOnlyHTTP200(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusServiceUnavailable} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
			}))
			defer srv.Close()
			err := checkHealth(context.Background(), srv.Client(), srv.URL)
			if status == http.StatusOK && err != nil {
				t.Fatalf("checkHealth() error = %v", err)
			}
			if status != http.StatusOK && err == nil {
				t.Fatal("checkHealth() succeeded for non-200 response")
			}
		})
	}
}

func TestCheckHealthRejectsNonHTTPURL(t *testing.T) {
	if err := checkHealth(context.Background(), http.DefaultClient, "file:///etc/passwd"); err == nil {
		t.Fatal("checkHealth() accepted file URL")
	}
}

func TestHealthcheckCommandTimesOut(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	cmd := newHealthcheckCommand("http://127.0.0.1:8080/health/live")
	cmd.SetArgs([]string{"--url", srv.URL, "--timeout", "10ms"})
	if err := cmd.ExecuteContext(context.Background()); err == nil {
		t.Fatal("healthcheck succeeded after timeout")
	}
}
```

- [ ] **Step 2: Run healthcheck tests and record RED**

Run:

```bash
go test ./internal/cli -run 'Test(CheckHealth|HealthcheckCommand)' -count=1
```

Expected: compilation fails because the healthcheck functions do not exist.

- [ ] **Step 3: Implement the bounded probe**

Implement a GET request with no redirects and a bounded context:

```go
func checkHealth(ctx context.Context, client *http.Client, endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("healthcheck: invalid HTTP URL %q", endpoint)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return fmt.Errorf("healthcheck: create request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("healthcheck: request failed: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthcheck: unexpected status %d", resp.StatusCode)
	}
	return nil
}
```

`newHealthcheckCommand` defaults to `http://127.0.0.1:8080/health/live`, defaults timeout to `3s`, uses an `http.Client` with redirects rejected, and prints `healthy` only after a successful 200 response.

- [ ] **Step 4: Register the command and test the public surface**

Add `newHealthcheckCommand("http://127.0.0.1:8080/health/live")` to `serverCommands`. Extend `TestServerRootExposesConfigurationCommands` to require `healthcheck`.

Run:

```bash
go test ./internal/cli -run 'Test(CheckHealth|HealthcheckCommand|ServerRootExposesConfigurationCommands)' -count=1
```

Expected: PASS.

- [ ] **Step 5: Write the failing non-privileged default test**

Add:

```go
func TestDefaultServerHTTPAddressIsNonPrivileged(t *testing.T) {
	cfg, err := config.Load(context.Background(), config.ConfigOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := cfg.Server.HTTPAddr, ":8080"; got != want {
		t.Fatalf("Server.HTTPAddr = %q, want %q", got, want)
	}
}
```

- [ ] **Step 6: Run the default test and record RED**

Run:

```bash
go test ./internal/config -run TestDefaultServerHTTPAddressIsNonPrivileged -count=1
```

Expected: FAIL with `Server.HTTPAddr = ":80", want ":8080"`.

- [ ] **Step 7: Change the default and record GREEN**

Change only the default `server.http_addr` to `:8080`; keep public 80/443 in Nginx and host port mappings.

Run:

```bash
go test ./internal/config ./internal/cli -count=1
```

Expected: PASS.

---

### Task 3: Deterministic Web Embed, GoReleaser, and Local Release Verification

**Files:**
- Create: `.goreleaser.yaml`
- Create: `scripts/verify-web-embed.sh`
- Create: `scripts/verify-release.sh`
- Modify: `Makefile`
- Test: `internal/build/build_test.go`

**Interfaces:**
- Consumes: Task 1 linker variables and the tracked `internal/server/web_dist` bundle.
- Produces: `make release VERSION=vX.Y.Z`, the `dist/tunnelmesh-server_vX.Y.Z_<os>_<arch>.<ext>`, `dist/tunnelmesh-agent_vX.Y.Z_<os>_<arch>.<ext>`, and `dist/tunnelmesh-client_vX.Y.Z_<os>_<arch>.<ext>` archive families, plus `dist/tunnelmesh_vX.Y.Z_checksums.txt`.

- [ ] **Step 1: Add a failing configuration contract test**

Extend `internal/build/build_test.go` with a repository-root test that reads `.goreleaser.yaml` and checks all commands and metadata variables:

```go
func TestGoReleaserConfigCoversEveryBinaryAndMetadataField(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", ".goreleaser.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range append(build.BinaryNames(),
		"internal/build.Version", "internal/build.Commit", "internal/build.BuildTime",
		"linux", "darwin", "windows", "amd64", "arm64", "CGO_ENABLED=0") {
		if !strings.Contains(text, want) {
			t.Fatalf(".goreleaser.yaml does not contain %q", want)
		}
	}
}
```

- [ ] **Step 2: Run the contract test and record RED**

Run:

```bash
go test ./internal/build -run TestGoReleaserConfigCoversEveryBinaryAndMetadataField -count=1
```

Expected: FAIL because `.goreleaser.yaml` does not exist.

- [ ] **Step 3: Create the GoReleaser v2 configuration**

Use three explicit build IDs. Each build contains:

```yaml
version: 2

builds:
  - id: tunnelmesh-server
    main: ./cmd/tunnelmesh-server
    binary: tunnelmesh-server
    env: [CGO_ENABLED=0]
    goos: [linux, darwin, windows]
    goarch: [amd64, arm64]
    flags: [-trimpath]
    ldflags:
      - >-
        -s -w
        -X github.com/tunnelmesh/tunnelmesh/internal/build.Version={{ .Env.RELEASE_VERSION }}
        -X github.com/tunnelmesh/tunnelmesh/internal/build.Commit={{ .Env.RELEASE_COMMIT }}
        -X github.com/tunnelmesh/tunnelmesh/internal/build.BuildTime={{ .Env.RELEASE_BUILD_TIME }}
```

Repeat the complete build block for Agent and Client with their own `id`, `main`, and `binary`. Define three archive stanzas, each restricted to one build ID, with this naming and format policy:

```yaml
archives:
  - id: tunnelmesh-server
    ids: [tunnelmesh-server]
    name_template: >-
      tunnelmesh-server_{{ .Env.RELEASE_VERSION }}_{{ .Os }}_{{ .Arch }}
    formats: [tar.gz]
    format_overrides:
      - goos: windows
        formats: [zip]
```

Repeat for Agent and Client. Add:

```yaml
checksum:
  name_template: tunnelmesh_{{ .Env.RELEASE_VERSION }}_checksums.txt

changelog:
  disable: true
```

Do not configure GitHub, Docker, Homebrew, Scoop, signing, or publishing in `.goreleaser.yaml`; the tag workflow publishes verified files explicitly.

- [ ] **Step 4: Run the Go contract test and GoReleaser schema check**

Run:

```bash
go test ./internal/build -run TestGoReleaserConfigCoversEveryBinaryAndMetadataField -count=1
goreleaser check
```

Expected: both commands PASS with GoReleaser v2.18.1.

- [ ] **Step 5: Write the Web embed verification script**

`scripts/verify-web-embed.sh` must use `set -eu`, require `web/dist/index.html`, require `internal/server/web_dist/index.html`, and compare file lists and bytes without copying:

```sh
#!/bin/sh
set -eu

test -f web/dist/index.html
test -f internal/server/web_dist/index.html

web_list=$(mktemp)
embed_list=$(mktemp)
trap 'rm -f "$web_list" "$embed_list"' EXIT HUP INT TERM
(cd web/dist && find . -type f -print | LC_ALL=C sort) >"$web_list"
(cd internal/server/web_dist && find . -type f -print | LC_ALL=C sort) >"$embed_list"
diff -u "$web_list" "$embed_list"
while IFS= read -r file; do
  cmp "web/dist/$file" "internal/server/web_dist/$file"
done <"$web_list"
```

- [ ] **Step 6: Prove stale Web assets fail**

Run in a temporary worktree or disposable copy:

```bash
cd web
npm ci
npm run build
cd ..
./scripts/verify-web-embed.sh
```

Expected before synchronizing a changed frontend: FAIL with a `diff` or `cmp` mismatch. After the normal frontend task updates the tracked embed directory: PASS. The release task itself must never copy into `internal/server/web_dist`.

- [ ] **Step 7: Implement the release archive verifier**

`scripts/verify-release.sh DIST VERSION` must:

1. Reject a version that does not match `^v[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?$`.
2. Require exactly 18 archives: 3 binaries × 6 platform tuples.
3. Require `.zip` for Windows and `.tar.gz` for Linux/Darwin.
4. Run `sha256sum -c` when available, otherwise `shasum -a 256 -c`.
5. List every archive and reject names matching `(^|/)(\.env|.*\.(db|sqlite|sqlite3|pem|key|crt|p12|pfx|yaml|yml|toml|json|log))$`.
6. Reject archive paths containing `token`, `password`, `secret`, `private-key`, or `mysql-dump`, case-insensitively.
7. Extract the current host archive to `mktemp -d`, run its binary with `--version`, and require VERSION, `commit=`, and `built=`.
8. On a host whose OS/architecture has no matching archive, print the structural verification result and exit zero after checksum/content checks.

Use `unzip -Z1` and `tar -tzf` for content listing. Cleanup all temporary directories with a trap.

- [ ] **Step 8: Add Make release targets**

Add `.PHONY` entries for `release-check`, `release`, and `release-verify`. Implement these exact semantics:

```make
GORELEASER ?= goreleaser

release-check:
	@test -n "$(VERSION)" || (echo "VERSION is required, for example VERSION=v0.1.0" >&2; exit 1)
	@printf '%s\n' "$(VERSION)" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?$$'
	@test -z "$$(git status --porcelain --untracked-files=all)" || (echo "release requires a clean worktree" >&2; exit 1)
	cd web && npm ci && npm run build
	./scripts/verify-web-embed.sh
	$(GORELEASER) check

release: release-check
	RELEASE_VERSION="$(VERSION)" \
	RELEASE_COMMIT="$$(git rev-parse HEAD)" \
	RELEASE_BUILD_TIME="$$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
	GORELEASER_CURRENT_TAG="$(VERSION)" \
	$(GORELEASER) release --clean --skip=publish
	./scripts/verify-release.sh dist "$(VERSION)"

release-verify:
	@test -n "$(VERSION)"
	./scripts/verify-release.sh dist "$(VERSION)"
```

- [ ] **Step 9: Run local release RED/GREEN checks**

Run first without VERSION:

```bash
make release
```

Expected: FAIL before building with `VERSION is required`.

Run from a clean worktree with GoReleaser v2.18.1:

```bash
make release VERSION=v0.1.0
```

Expected: PASS, 18 archives plus one checksum file in `dist/`, no tag, no Git push, and no GitHub release.

---

### Task 4: Nginx HTTPS/WSS Reference Configuration

**Files:**
- Create: `deploy/nginx/tunnelmesh.conf`
- Create: `deploy/nginx/proxy-headers.conf`
- Create: `scripts/verify-nginx.sh`
- Create: `docs/deployment/nginx.md`

**Interfaces:**
- Consumes: Server HTTP listener `127.0.0.1:8080`; subproject A Bearer authentication and Host/Origin validation; subproject B `/health/live`, `/health/ready`, `/ws/agent`, and `/ws/client` routes.
- Produces: a complete Nginx configuration and `scripts/verify-nginx.sh CONFIG_PATH` syntax/semantic validator.

- [ ] **Step 1: Write the failing semantic validator**

Create a POSIX script that fails unless the config contains all required controls:

```sh
#!/bin/sh
set -eu
config=${1:-deploy/nginx/tunnelmesh.conf}
test -f "$config"

for required in \
  'listen 80' \
  'return 301 https://$host$request_uri' \
  'listen 443 ssl' \
  'ssl_protocols TLSv1.2 TLSv1.3' \
  'location /api/' \
  'location = /ws/agent' \
  'location = /ws/client' \
  'proxy_http_version 1.1' \
  'proxy_set_header Upgrade $http_upgrade' \
  'proxy_set_header Authorization $http_authorization' \
  'proxy_set_header Host $host' \
  'proxy_buffering off' \
  'proxy_read_timeout 120s' \
  'proxy_send_timeout 120s' \
  'client_max_body_size 2m' \
  'limit_req_zone $binary_remote_addr zone=tunnelmesh_api:10m rate=20r/s' \
  'limit_req_zone $binary_remote_addr zone=tunnelmesh_ws:10m rate=5r/s'
do
  grep -F "$required" "$config" >/dev/null
done
```

- [ ] **Step 2: Run the validator and record RED**

Run:

```bash
chmod +x scripts/verify-nginx.sh
./scripts/verify-nginx.sh deploy/nginx/tunnelmesh.conf
```

Expected: FAIL because the Nginx configuration does not exist.

- [ ] **Step 3: Create the Nginx configuration**

The file is a standalone Nginx configuration. It must define `events`, place `map $http_upgrade $connection_upgrade` in `http` scope, redirect port 80, and terminate TLS on 443. Use:

```nginx
events {}

http {
    limit_req_zone $binary_remote_addr zone=tunnelmesh_api:10m rate=20r/s;
    limit_req_zone $binary_remote_addr zone=tunnelmesh_ws:10m rate=5r/s;

    upstream tunnelmesh_server {
        server 127.0.0.1:8080;
        keepalive 64;
    }

    map $http_upgrade $connection_upgrade {
        default upgrade;
        ''      close;
    }

    server {
        listen 80;
        listen [::]:80;
        server_name tunnel.example.com *.tunnel.example.com;
        return 301 https://$host$request_uri;
    }

    server {
        listen 443 ssl;
        listen [::]:443 ssl;
        server_name tunnel.example.com *.tunnel.example.com;

        ssl_certificate     /etc/nginx/tunnelmesh/fullchain.pem;
        ssl_certificate_key /etc/nginx/tunnelmesh/privkey.pem;
        ssl_protocols TLSv1.2 TLSv1.3;
        client_max_body_size 2m;

        location /api/ {
            limit_req zone=tunnelmesh_api burst=40 nodelay;
            proxy_pass http://tunnelmesh_server;
            include /etc/nginx/tunnelmesh/proxy-headers.conf;
        }

        location = /ws/agent {
            limit_req zone=tunnelmesh_ws burst=10 nodelay;
            proxy_pass http://tunnelmesh_server;
            include /etc/nginx/tunnelmesh/proxy-headers.conf;
            proxy_http_version 1.1;
            proxy_set_header Upgrade $http_upgrade;
            proxy_set_header Connection $connection_upgrade;
            proxy_set_header Authorization $http_authorization;
            proxy_buffering off;
            proxy_request_buffering off;
            proxy_read_timeout 120s;
            proxy_send_timeout 120s;
        }

        location = /ws/client {
            limit_req zone=tunnelmesh_ws burst=10 nodelay;
            proxy_pass http://tunnelmesh_server;
            include /etc/nginx/tunnelmesh/proxy-headers.conf;
            proxy_http_version 1.1;
            proxy_set_header Upgrade $http_upgrade;
            proxy_set_header Connection $connection_upgrade;
            proxy_set_header Authorization $http_authorization;
            proxy_buffering off;
            proxy_request_buffering off;
            proxy_read_timeout 120s;
            proxy_send_timeout 120s;
        }

        location / {
            proxy_pass http://tunnelmesh_server;
            include /etc/nginx/tunnelmesh/proxy-headers.conf;
        }
    }
}
```

Define the included header file as a second `http`-context file:

- Create: `deploy/nginx/proxy-headers.conf`

```nginx
proxy_set_header Host $host;
proxy_set_header X-Forwarded-Proto $scheme;
proxy_set_header X-Forwarded-Host $host;
proxy_set_header X-Forwarded-Port $server_port;
proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
proxy_set_header X-Real-IP $remote_addr;
proxy_set_header Authorization $http_authorization;
```

The Server must trust forwarded headers only from configured reverse-proxy addresses; this trust enforcement is supplied by subproject A/B, not by this file.

- [ ] **Step 4: Run semantic and Nginx syntax checks**

Run:

```bash
./scripts/verify-nginx.sh deploy/nginx/tunnelmesh.conf
nginx_test_dir="$(mktemp -d)"
trap 'rm -rf "$nginx_test_dir"' EXIT
openssl req -x509 -newkey rsa:2048 -nodes -days 1 \
  -subj '/CN=tunnel.example.com' \
  -keyout "$nginx_test_dir/privkey.pem" \
  -out "$nginx_test_dir/fullchain.pem"
docker run --rm \
  -v "$PWD/deploy/nginx:/etc/nginx/tunnelmesh:ro" \
  -v "$nginx_test_dir/fullchain.pem:/etc/nginx/tunnelmesh/fullchain.pem:ro" \
  -v "$nginx_test_dir/privkey.pem:/etc/nginx/tunnelmesh/privkey.pem:ro" \
  nginx:1.27.5-alpine nginx -t -c /etc/nginx/tunnelmesh/tunnelmesh.conf
```

Expected: semantic script PASS and `nginx: configuration file /etc/nginx/tunnelmesh/tunnelmesh.conf test is successful`. The disposable certificate directory is outside the repository and is removed by the trap.

- [ ] **Step 5: Document and execute WSS smoke checks**

Document these commands with actual Agent and Client scoped Tokens created by subproject A:

```bash
curl -fsS https://tunnel.example.com/health/live
curl -fsS https://tunnel.example.com/health/ready
websocat --binary -H='Authorization: Bearer <agent-token>' wss://tunnel.example.com/ws/agent
websocat --binary -H='Authorization: Bearer <client-token>' wss://tunnel.example.com/ws/client
```

Expected: health endpoints return 200; each WebSocket reaches the correct backend handler. A missing Bearer header returns 401/403 and never upgrades. The documentation explicitly states that angle-bracket values are shell substitutions supplied by the operator and must not be committed or stored in shell history.

---

### Task 5: Linux systemd Units

**Files:**
- Create: `deploy/systemd/tunnelmesh-server.service`
- Create: `deploy/systemd/tunnelmesh-agent.service`
- Create: `scripts/verify-systemd.sh`
- Create: `docs/deployment/linux-systemd.md`

**Interfaces:**
- Consumes: `tunnelmesh-{server,agent} --config PATH check-config`; subproject B systemd `READY=1`, `WATCHDOG=1`, and `STOPPING=1`; Server state at `/var/lib/tunnelmesh`; external configs under `/etc/tunnelmesh`.
- Produces: notify/watchdog service units validated by `systemd-analyze verify`.

- [ ] **Step 1: Write the failing unit validator**

Create `scripts/verify-systemd.sh` to require both files and these directives:

```sh
#!/bin/sh
set -eu
for unit in deploy/systemd/tunnelmesh-server.service deploy/systemd/tunnelmesh-agent.service; do
  test -f "$unit"
  grep -F 'Type=notify' "$unit" >/dev/null
  grep -F 'Restart=on-failure' "$unit" >/dev/null
  grep -F 'RestartSec=5s' "$unit" >/dev/null
  grep -F 'TimeoutStopSec=30s' "$unit" >/dev/null
  grep -F 'NoNewPrivileges=true' "$unit" >/dev/null
  grep -F 'PrivateTmp=true' "$unit" >/dev/null
  grep -F 'ProtectSystem=strict' "$unit" >/dev/null
done
grep -F 'WatchdogSec=45s' deploy/systemd/tunnelmesh-server.service >/dev/null
grep -F 'WatchdogSec=45s' deploy/systemd/tunnelmesh-agent.service >/dev/null
systemd-analyze verify deploy/systemd/tunnelmesh-server.service deploy/systemd/tunnelmesh-agent.service
```

- [ ] **Step 2: Run the validator on Linux and record RED**

Run:

```bash
./scripts/verify-systemd.sh
```

Expected: FAIL because the units do not exist.

- [ ] **Step 3: Create the Server unit**

Use:

```ini
[Unit]
Description=TunnelMesh Server
After=network-online.target
Wants=network-online.target

[Service]
Type=notify
NotifyAccess=main
User=tunnelmesh
Group=tunnelmesh
StateDirectory=tunnelmesh
RuntimeDirectory=tunnelmesh
ExecStartPre=/usr/local/bin/tunnelmesh-server --config /etc/tunnelmesh/server.yaml check-config
ExecStart=/usr/local/bin/tunnelmesh-server --config /etc/tunnelmesh/server.yaml run
Restart=on-failure
RestartSec=5s
TimeoutStartSec=30s
TimeoutStopSec=30s
WatchdogSec=45s
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/var/lib/tunnelmesh /run/tunnelmesh
UMask=0077

[Install]
WantedBy=multi-user.target
```

- [ ] **Step 4: Create the Agent unit**

Use the same lifecycle controls, with:

```ini
ExecStartPre=/usr/local/bin/tunnelmesh-agent --config /etc/tunnelmesh/agent.yaml check-config
ExecStart=/usr/local/bin/tunnelmesh-agent --config /etc/tunnelmesh/agent.yaml run
```

Give the Agent `ReadOnlyPaths=/etc/tunnelmesh` and only the exact metadata file paths configured by the operator through additional systemd drop-ins. Do not grant blanket home-directory access.

- [ ] **Step 5: Run static and live unit checks**

Run on Linux with systemd:

```bash
./scripts/verify-systemd.sh
sudo install -m 0644 deploy/systemd/tunnelmesh-server.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl start tunnelmesh-server
systemctl show tunnelmesh-server -p ActiveState -p SubState -p WatchdogUSec -p MainPID
curl -fsS http://127.0.0.1:8080/health/ready
sudo systemctl stop tunnelmesh-server
```

Expected: `ActiveState=active`, `SubState=running`, non-zero `WatchdogUSec`, readiness 200, and a clean stop within 30 seconds after GOAWAY/drain. If subproject B has not implemented watchdog notifications, the task is not eligible for GREEN and the unit must not be installed with `WatchdogSec`.

---

### Task 6: macOS launchd Definitions

**Files:**
- Create: `deploy/launchd/io.tunnelmesh.server.plist`
- Create: `deploy/launchd/io.tunnelmesh.agent.plist`
- Create: `scripts/verify-launchd.sh`
- Create: `docs/deployment/macos-launchd.md`

**Interfaces:**
- Consumes: foreground `run` commands, SIGTERM-aware shutdown from subproject B, configs under `/usr/local/etc/tunnelmesh`, and log directory `/usr/local/var/log/tunnelmesh`.
- Produces: two system LaunchDaemon plists validated by `plutil -lint`.

- [ ] **Step 1: Write the failing plist validator**

```sh
#!/bin/sh
set -eu
for plist in deploy/launchd/io.tunnelmesh.server.plist deploy/launchd/io.tunnelmesh.agent.plist; do
  test -f "$plist"
  plutil -lint "$plist"
  grep -F '<key>RunAtLoad</key>' "$plist" >/dev/null
  grep -F '<key>KeepAlive</key>' "$plist" >/dev/null
  grep -F '<key>ThrottleInterval</key>' "$plist" >/dev/null
  grep -F '<key>StandardOutPath</key>' "$plist" >/dev/null
  grep -F '<key>StandardErrorPath</key>' "$plist" >/dev/null
done
```

- [ ] **Step 2: Run the validator and record RED**

Run:

```bash
./scripts/verify-launchd.sh
```

Expected: FAIL because the plists do not exist.

- [ ] **Step 3: Create both plists**

Use system paths independent of Homebrew architecture:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>io.tunnelmesh.server</string>
  <key>ProgramArguments</key>
  <array>
    <string>/usr/local/bin/tunnelmesh-server</string>
    <string>--config</string><string>/usr/local/etc/tunnelmesh/server.yaml</string>
    <string>run</string>
  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key>
  <dict><key>SuccessfulExit</key><false/></dict>
  <key>ThrottleInterval</key><integer>5</integer>
  <key>ProcessType</key><string>Background</string>
  <key>StandardOutPath</key><string>/usr/local/var/log/tunnelmesh/server.log</string>
  <key>StandardErrorPath</key><string>/usr/local/var/log/tunnelmesh/server-error.log</string>
</dict>
</plist>
```

Create the Agent plist with label `io.tunnelmesh.agent`, `tunnelmesh-agent`, `agent.yaml`, `agent.log`, and `agent-error.log`.

- [ ] **Step 4: Run static and live launchd verification**

Run on macOS:

```bash
./scripts/verify-launchd.sh
sudo install -d -m 0750 /usr/local/etc/tunnelmesh /usr/local/var/log/tunnelmesh
sudo cp deploy/launchd/io.tunnelmesh.server.plist /Library/LaunchDaemons/
sudo chown root:wheel /Library/LaunchDaemons/io.tunnelmesh.server.plist
sudo chmod 0644 /Library/LaunchDaemons/io.tunnelmesh.server.plist
sudo launchctl bootstrap system /Library/LaunchDaemons/io.tunnelmesh.server.plist
sudo launchctl print system/io.tunnelmesh.server
curl -fsS http://127.0.0.1:8080/health/ready
sudo launchctl bootout system/io.tunnelmesh.server
```

Expected: plist lint PASS, launchd reports a running PID, readiness returns 200, and bootout stops the process cleanly.

---

### Task 7: Windows Services Through Pinned WinSW

**Files:**
- Create: `deploy/windows/tunnelmesh-server.xml`
- Create: `deploy/windows/tunnelmesh-agent.xml`
- Create: `deploy/windows/install-service.ps1`
- Create: `deploy/windows/uninstall-service.ps1`
- Create: `scripts/verify-windows-service.ps1`
- Create: `docs/deployment/windows-service.md`

**Interfaces:**
- Consumes: Windows console binaries that remain foreground processes and handle service-wrapper termination; configuration under `%ProgramData%\TunnelMesh`; WinSW v2.12.0 x64 wrapper.
- Produces: `Install-TunnelMeshService -Component server|agent` and `Uninstall-TunnelMeshService -Component server|agent`.

- [ ] **Step 1: Write the failing PowerShell validator**

```powershell
$ErrorActionPreference = 'Stop'
$files = @(
  'deploy/windows/tunnelmesh-server.xml',
  'deploy/windows/tunnelmesh-agent.xml',
  'deploy/windows/install-service.ps1',
  'deploy/windows/uninstall-service.ps1'
)
foreach ($file in $files) {
  if (-not (Test-Path $file -PathType Leaf)) { throw "missing $file" }
}
[xml](Get-Content 'deploy/windows/tunnelmesh-server.xml' -Raw) | Out-Null
[xml](Get-Content 'deploy/windows/tunnelmesh-agent.xml' -Raw) | Out-Null
$installer = Get-Content 'deploy/windows/install-service.ps1' -Raw
foreach ($required in @(
  'v2.12.0',
  'WinSW-x64.exe',
  '05b82d46ad331cc16bdc00de5c6332c1ef818df8ceefcd49c726553209b3a0da',
  'Get-FileHash',
  'sc.exe failure'
)) {
  if (-not $installer.Contains($required)) { throw "installer missing $required" }
}
if ($installer -match 'sc\.exe\s+create') { throw 'installer must not use sc.exe create' }
```

- [ ] **Step 2: Run the validator and record RED**

Run on Windows:

```powershell
pwsh -NoProfile -File scripts/verify-windows-service.ps1
```

Expected: FAIL because the service assets do not exist.

- [ ] **Step 3: Create the WinSW XML files**

Server XML:

```xml
<service>
  <id>TunnelMeshServer</id>
  <name>TunnelMesh Server</name>
  <description>TunnelMesh control plane and proxy server</description>
  <executable>%BASE%\tunnelmesh-server.exe</executable>
  <arguments>--config "%ProgramData%\TunnelMesh\server.yaml" run</arguments>
  <logpath>%ProgramData%\TunnelMesh\logs</logpath>
  <log mode="roll-by-size-time">
    <sizeThreshold>10485760</sizeThreshold>
    <pattern>yyyyMMdd</pattern>
    <autoRollAtTime>00:00:00</autoRollAtTime>
    <zipOlderThanNumDays>7</zipOlderThanNumDays>
    <zipDateFormat>yyyyMM</zipDateFormat>
  </log>
  <stoptimeout>30sec</stoptimeout>
  <onfailure action="restart" delay="5 sec" />
  <onfailure action="restart" delay="30 sec" />
  <onfailure action="restart" delay="60 sec" />
</service>
```

Agent XML uses IDs/names/descriptions for Agent, `tunnelmesh-agent.exe`, and `agent.yaml`.

- [ ] **Step 4: Implement pinned download and installation**

`install-service.ps1` accepts `-Component server|agent`, `-InstallDir` defaulting to `$env:ProgramFiles\TunnelMesh`, and `-DataDir` defaulting to `$env:ProgramData\TunnelMesh`. It must:

1. Require an elevated session.
2. Require the matching TunnelMesh `.exe` in `InstallDir`.
3. Create `DataDir\logs` without overwriting existing YAML.
4. Download `https://github.com/winsw/winsw/releases/download/v2.12.0/WinSW-x64.exe` to a temporary file.
5. Compare `(Get-FileHash -Algorithm SHA256).Hash.ToLowerInvariant()` with `05b82d46ad331cc16bdc00de5c6332c1ef818df8ceefcd49c726553209b3a0da` using `-ceq`.
6. Copy the verified wrapper to `tunnelmesh-$Component-service.exe` and the matching XML beside it with the same basename.
7. Execute `& $wrapper install`; never call `sc.exe create`.
8. Configure SCM recovery:

```powershell
sc.exe failure $serviceId reset= 86400 actions= restart/5000/restart/30000/restart/60000
if ($LASTEXITCODE -ne 0) { throw "sc.exe failure exited $LASTEXITCODE" }
```

9. Start via `& $wrapper start` and query via `sc.exe query $serviceId`.
10. Delete the temporary WinSW download in `finally`.

On Windows ARM64, print that the Go executable is native ARM64 while WinSW v2.12.0 is x64 and requires Windows x64 emulation. Abort with a clear error if the wrapper fails to execute; do not silently replace it with an unpinned build.

- [ ] **Step 5: Implement safe uninstall**

`uninstall-service.ps1` resolves the existing wrapper, runs `stop` then `uninstall`, and removes only the wrapper and adjacent generated XML. It explicitly preserves:

```text
%ProgramData%\TunnelMesh\server.yaml
%ProgramData%\TunnelMesh\agent.yaml
%ProgramData%\TunnelMesh\logs
%ProgramData%\TunnelMesh\tunnelmesh.db
```

- [ ] **Step 6: Run static and live Windows checks**

Run:

```powershell
pwsh -NoProfile -File scripts/verify-windows-service.ps1
pwsh -NoProfile -File deploy/windows/install-service.ps1 -Component server
sc.exe query TunnelMeshServer
sc.exe qfailure TunnelMeshServer
Invoke-WebRequest http://127.0.0.1:8080/health/ready -UseBasicParsing
pwsh -NoProfile -File deploy/windows/uninstall-service.ps1 -Component server
```

Expected: XML parsing PASS, hash verification PASS, SCM reports `RUNNING`, recovery lists 5/30/60-second restarts, readiness returns 200, uninstall removes the service but preserves config, logs, and data.

---

### Task 8: Dockerfile and Compose Runtime Safety

**Files:**
- Modify: `Dockerfile`
- Modify: `docker-compose.local.yml`
- Modify: `docker-compose.cluster.yml`
- Modify: `.dockerignore`
- Create: `scripts/verify-containers.sh`
- Modify: `docs/deployment/docker.md`

**Interfaces:**
- Consumes: Task 1 build metadata, Task 2 `tunnelmesh-server healthcheck`, subproject A Agent Token environment variable, and subproject B health endpoints.
- Produces: three non-root image targets; Server image health status; health-gated local and cluster Compose deployments.

- [ ] **Step 1: Write the failing container semantic validator**

Create a POSIX script requiring:

```sh
#!/bin/sh
set -eu
grep -F 'USER nonroot:nonroot' Dockerfile >/dev/null
grep -F 'TUNNELMESH_SERVER_HTTP_ADDR=:8080' Dockerfile >/dev/null
grep -F 'HEALTHCHECK' Dockerfile >/dev/null
grep -F 'healthcheck --url http://127.0.0.1:8080/health/live' Dockerfile >/dev/null
grep -F 'org.opencontainers.image.version' Dockerfile >/dev/null
grep -F 'org.opencontainers.image.revision' Dockerfile >/dev/null
grep -F 'target: runtime-server' docker-compose.local.yml >/dev/null
grep -F 'TUNNELMESH_AGENT_TOKEN' docker-compose.local.yml >/dev/null
grep -F 'ws://server:8080/ws/agent' docker-compose.local.yml >/dev/null
grep -F 'condition: service_healthy' docker-compose.local.yml >/dev/null
grep -F '127.0.0.1:8080:8080' docker-compose.local.yml >/dev/null
grep -F 'condition: service_healthy' docker-compose.cluster.yml >/dev/null
docker compose -f docker-compose.local.yml config >/dev/null
```

- [ ] **Step 2: Run the validator and record RED**

Run with a disposable Agent Token value:

```bash
TUNNELMESH_AGENT_TOKEN=release-smoke-token ./scripts/verify-containers.sh
```

Expected: FAIL because the Dockerfile has no healthcheck/runtime targets and Compose still uses the incorrect URL and ports.

- [ ] **Step 3: Refactor the Docker runtime stages**

Pass metadata as build arguments:

```dockerfile
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_TIME=unknown
```

Use the same three linker variables as GoReleaser. Create a shared non-root base and three named runtime stages:

```dockerfile
FROM gcr.io/distroless/static-debian12:nonroot AS runtime-base
ARG VERSION
ARG COMMIT
ARG BUILD_TIME
LABEL org.opencontainers.image.version=$VERSION \
      org.opencontainers.image.revision=$COMMIT \
      org.opencontainers.image.created=$BUILD_TIME
WORKDIR /var/lib/tunnelmesh
USER nonroot:nonroot

FROM runtime-base AS runtime-server
COPY --from=go-build /out/tunnelmesh /usr/local/bin/tunnelmesh
ENV TUNNELMESH_SERVER_HTTP_ADDR=:8080
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD ["/usr/local/bin/tunnelmesh", "healthcheck", "--url", "http://127.0.0.1:8080/health/live", "--timeout", "3s"]
ENTRYPOINT ["/usr/local/bin/tunnelmesh"]
CMD ["run"]
```

Agent and Client stages copy their binaries and have no fake HTTP healthcheck. Container process exit remains their liveness signal. Select the final stage with a validated `APP` argument or use explicit Compose `target` values.

Keep `ARG APP=server` in global scope and finish the Dockerfile with:

```dockerfile
FROM runtime-${APP} AS runtime
```

This preserves the existing `docker build --build-arg APP=server|agent|client` interface while Compose may address `runtime-server`, `runtime-agent`, and `runtime-client` directly.

- [ ] **Step 4: Correct local Compose**

Required behavior:

- Server build target `runtime-server`, internal port 8080, host mapping `127.0.0.1:8080:8080` for direct local access. Production public 80/443 belongs to Nginx.
- Server healthcheck invokes the binary command.
- Agent build target `runtime-agent`, URL `ws://server:8080/ws/agent` only for the isolated local Compose network, Agent ID, and `TUNNELMESH_AGENT_TOKEN: ${TUNNELMESH_AGENT_TOKEN:?set TUNNELMESH_AGENT_TOKEN}`.
- Agent `depends_on.server.condition: service_healthy`.
- Client build target `runtime-client`; no fabricated service healthcheck.
- All long-running services retain `restart: unless-stopped`.

- [ ] **Step 5: Correct cluster Compose**

Add MySQL health:

```yaml
healthcheck:
  test: ["CMD-SHELL", "mysqladmin ping -h 127.0.0.1 -u root -p$$MYSQL_ROOT_PASSWORD --silent"]
  interval: 10s
  timeout: 5s
  retries: 12
```

Both Servers use `runtime-server`, bind 8080 internally, expose only loopback test ports, wait for `mysql: condition: service_healthy`, and expose Server readiness. Keep MySQL DSN and CA external. Do not add real passwords, DSNs, certificates, or Tokens to Compose defaults.

- [ ] **Step 6: Run semantic, build, and runtime verification**

Run:

```bash
TUNNELMESH_AGENT_TOKEN=release-smoke-token ./scripts/verify-containers.sh
docker buildx build --load --platform linux/amd64 --target runtime-server \
  --build-arg APP=server --build-arg VERSION=v0.1.0 \
  --build-arg COMMIT="$(git rev-parse HEAD)" \
  --build-arg BUILD_TIME="$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  -t tunnelmesh:server-smoke .
docker inspect --format '{{.Config.User}}' tunnelmesh:server-smoke
TUNNELMESH_AGENT_TOKEN=release-smoke-token docker compose -f docker-compose.local.yml up -d --build server
docker compose -f docker-compose.local.yml ps
curl -fsS http://127.0.0.1:8080/health/ready
docker compose -f docker-compose.local.yml down
```

Expected: image user is `nonroot:nonroot`; Server binds 8080 without permission errors; Compose reports Server healthy; readiness returns 200.

- [ ] **Step 7: Verify both Linux architectures and archive no secrets**

Run:

```bash
docker buildx build --platform linux/amd64,linux/arm64 --target runtime-server --build-arg APP=server .
if docker image inspect tunnelmesh:server-smoke | rg -i 'bearer |password=|private[_-]?key=|mysql.*dsn=|token='; then
  echo 'secret-bearing image metadata detected' >&2
  exit 1
fi
```

Expected: both platform builds complete and image inspection finds no secret-bearing `Env` or labels. Review `docker history --no-trunc tunnelmesh:server-smoke` separately to ensure no secret value was passed through a build instruction.

---

### Task 9: Installation, Upgrade, Rollback, and Uninstall Documentation

**Files:**
- Create: `docs/deployment/binary-release.md`
- Modify: `docs/deployment/nginx.md`
- Modify: `docs/deployment/linux-systemd.md`
- Modify: `docs/deployment/macos-launchd.md`
- Modify: `docs/deployment/windows-service.md`
- Create: `docs/operations/upgrade-rollback.md`
- Modify: `docs/deployment/docker.md`
- Modify: `docs/operations/configuration.md`
- Modify: `docs/operations/troubleshooting.md`
- Modify: `docs/README.md`
- Modify: `README.md`
- Create: `scripts/verify-docs.sh`

**Interfaces:**
- Consumes: release filenames from Task 3, platform assets from Tasks 4-8, subproject A Token rotation commands/API, and subproject B health/readiness endpoints.
- Produces: complete operator paths from download through uninstall, with a rollback decision point that fits the five-minute recovery objective.

- [ ] **Step 1: Write the failing documentation validator**

The script checks every file exists and requires concrete command terms:

```sh
#!/bin/sh
set -eu
files='docs/deployment/binary-release.md
docs/deployment/nginx.md
docs/deployment/linux-systemd.md
docs/deployment/macos-launchd.md
docs/deployment/windows-service.md
docs/operations/upgrade-rollback.md'
for file in $files; do test -s "$file"; done

grep -F 'checksums.txt' docs/deployment/binary-release.md >/dev/null
grep -F 'shasum -a 256 -c' docs/deployment/binary-release.md >/dev/null
grep -F 'systemctl' docs/deployment/linux-systemd.md >/dev/null
grep -F 'launchctl bootstrap' docs/deployment/macos-launchd.md >/dev/null
grep -F 'WinSW v2.12.0' docs/deployment/windows-service.md >/dev/null
grep -F '05b82d46ad331cc16bdc00de5c6332c1ef818df8ceefcd49c726553209b3a0da' docs/deployment/windows-service.md >/dev/null
grep -F '/health/ready' docs/operations/upgrade-rollback.md >/dev/null
grep -F 'migrations/ddl.sql' docs/operations/upgrade-rollback.md >/dev/null
grep -F 'Token rotation' docs/operations/upgrade-rollback.md >/dev/null
grep -F 'five minutes' docs/operations/upgrade-rollback.md >/dev/null
```

- [ ] **Step 2: Run the validator and record RED**

Run:

```bash
./scripts/verify-docs.sh
```

Expected: FAIL because the release and platform guides do not exist.

- [ ] **Step 3: Write binary installation and uninstall procedures**

`binary-release.md` must include:

- Mapping from `uname -s`, `uname -m`, and Windows architecture to exact archive names.
- Download of the selected archive and `tunnelmesh_vX.Y.Z_checksums.txt`.
- Linux `sha256sum -c`; macOS `shasum -a 256 -c`; Windows `Get-FileHash` comparison.
- Extraction into a temporary directory and atomic installation using a new filename followed by `mv`/`Move-Item` on the same filesystem.
- `--version` verification before service restart.
- Config files under `/etc/tunnelmesh`, `/usr/local/etc/tunnelmesh`, or `%ProgramData%\TunnelMesh`, never beside release binaries.
- Logs through journald, configured launchd paths, WinSW logs, or container logs.
- Uninstall that removes binaries/service definitions but preserves DB/config by default and lists a separate, explicit data deletion command.

- [ ] **Step 4: Write the upgrade/rollback runbook**

The runbook must prescribe this sequence:

1. Record current binary/image digest and `--version` output.
2. Back up SQLite with the Server stopped or MySQL with a transaction-consistent backup.
3. Inspect `migrations/ddl.sql`; schema changes must use expand/contract order and remain readable by the immediately previous release.
4. Run new binary `check-config` before replacing the active executable.
5. Canary one Server/Agent; require `/health/live` and `/health/ready` 200, successful authenticated Agent and Client WSS connections, and stable error/reconnect metrics.
6. Promote remaining nodes one at a time.
7. Roll back within five minutes by stopping the failed version, restoring the previous immutable binary/image, restoring DB only when the schema is not backward compatible, starting the service, and rechecking readiness.
8. Perform Token rotation through subproject A after binary stability is confirmed; never rotate all Agent/Client Tokens at the same instant as the binary rollout.

- [ ] **Step 5: Update existing docs and navigation**

Update examples from Server `:80` to `:8080` where they refer to the direct process listener. Keep public URLs on HTTPS 443. Add all new guides to `docs/README.md` and the root README. Add troubleshooting commands for:

```bash
tunnelmesh-server --version
tunnelmesh-server --config /etc/tunnelmesh/server.yaml check-config
tunnelmesh-server healthcheck --url http://127.0.0.1:8080/health/live
curl -fsS http://127.0.0.1:8080/health/ready
systemctl status tunnelmesh-server
launchctl print system/io.tunnelmesh.server
sc.exe query TunnelMeshServer
docker inspect --format '{{json .State.Health}}' tunnelmesh-server
```

- [ ] **Step 6: Run documentation verification**

Run:

```bash
./scripts/verify-docs.sh
rg -n 'server.*:80|127\.0\.0\.1:80([^0-9]|$)' README.md docs deploy docker-compose*.yml
```

Expected: docs validator PASS. Any remaining direct-process `:80` match is corrected; matches describing Nginx public port 80 redirects remain documented and are reviewed manually.

---

### Task 10: Tag-Gated GitHub Release With Pre-Publication Native Smoke Tests

**Files:**
- Create: `.github/workflows/release.yml`
- Create: `scripts/smoke-release-artifact.sh`
- Create: `scripts/smoke-release-artifact.ps1`
- Modify: `scripts/verify-release.sh`
- Modify: `README.md`

**Interfaces:**
- Consumes: `make release VERSION=TAG`, verified `dist/` artifacts, GitHub Actions artifacts, and the protected `GITHUB_TOKEN`.
- Produces: a GitHub release for an existing pushed `vX.Y.Z` tag only after Linux amd64, macOS arm64, and Windows amd64 artifacts pass native smoke tests. Linux arm64 and Windows arm64 remain structural cross-build checks until trusted native runners are configured.

- [ ] **Step 1: Write a failing workflow policy test**

Extend `scripts/verify-release.sh` with a workflow check that requires packaging, pre-publication smoke, and a final publish job:

```sh
workflow=.github/workflows/release.yml
test -f "$workflow"
grep -F 'tags:' "$workflow" >/dev/null
grep -F "'v*.*.*'" "$workflow" >/dev/null
grep -F 'actions/upload-artifact@v4' "$workflow" >/dev/null
grep -F 'actions/download-artifact@v4' "$workflow" >/dev/null
grep -F 'native-smoke:' "$workflow" >/dev/null
grep -F 'publish:' "$workflow" >/dev/null
grep -F 'needs: [package, native-smoke]' "$workflow" >/dev/null
grep -F 'contents: write' "$workflow" >/dev/null
grep -F 'gh release create' "$workflow" >/dev/null
if grep -F 'goreleaser release --clean' "$workflow" | grep -v -- '--skip=publish' >/dev/null; then
  echo 'workflow must not let GoReleaser publish before verification' >&2
  exit 1
fi
```

- [ ] **Step 2: Run the policy test and record RED**

Run after producing a structurally valid Task 3 `dist/` fixture:

```bash
./scripts/verify-release.sh dist v0.1.0
```

Expected: FAIL because `.github/workflows/release.yml` does not exist.

- [ ] **Step 3: Create the tag-only quality and package jobs**

Start the workflow with read-only default permissions. Run tests on all three native runner families, package once on Linux, and upload the complete verified `dist/` directory without publishing:

```yaml
name: release

on:
  push:
    tags:
      - 'v*.*.*'

permissions:
  contents: read

jobs:
  quality:
    strategy:
      matrix:
        os: [ubuntu-24.04, macos-14, windows-2022]
    runs-on: ${{ matrix.os }}
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: {go-version-file: go.mod, cache: true}
      - uses: actions/setup-node@v4
        with: {node-version: '22', cache: npm, cache-dependency-path: web/package-lock.json}
      - run: go test ./... -count=1
      - run: go test -race ./...
        if: runner.os == 'Linux'
      - run: go vet ./...
      - run: npm ci
        working-directory: web
      - run: npm test -- --run
        working-directory: web
      - run: npm run build
        working-directory: web

  package:
    needs: quality
    runs-on: ubuntu-24.04
    steps:
      - uses: actions/checkout@v4
        with: {fetch-depth: 0}
      - uses: actions/setup-go@v5
        with: {go-version-file: go.mod, cache: true}
      - uses: actions/setup-node@v4
        with: {node-version: '22', cache: npm, cache-dependency-path: web/package-lock.json}
      - uses: goreleaser/goreleaser-action@v6
        with: {install-only: true, version: v2.18.1}
      - name: Validate semantic version tag
        shell: bash
        run: |
          [[ "${GITHUB_REF_NAME}" =~ ^v[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?$ ]]
      - run: make release VERSION=${{ github.ref_name }}
      - uses: actions/upload-artifact@v4
        with:
          name: release-dist
          path: dist/
          if-no-files-found: error
          retention-days: 7
```

- [ ] **Step 4: Add the POSIX native artifact smoke script**

`scripts/smoke-release-artifact.sh DIST VERSION BINARY OS ARCH` must locate the exact archive under `DIST`, verify its checksum entry, extract it to `mktemp -d`, execute `./BINARY --version`, and require VERSION, `commit=`, and `built=`. For `tunnelmesh-server`, run:

```bash
if "$binary_path" healthcheck --url http://127.0.0.1:1/health/live --timeout 100ms; then
  echo 'healthcheck unexpectedly accepted an unreachable endpoint' >&2
  exit 1
fi
```

The script must clean temporary files with a trap and never download from a public release.

- [ ] **Step 5: Add the Windows native artifact smoke script**

`scripts/smoke-release-artifact.ps1 -Dist DIR -Version VERSION -Binary BINARY -OS windows -Arch amd64` must select the exact zip, extract the matching line from the checksum file, compare it with `Get-FileHash -Algorithm SHA256`, expand into a temporary directory, run `& $binaryPath --version`, and require VERSION, `commit=`, and `built=`. Delete the temporary directory in `finally`; do not use `gh release download` because publication has not occurred yet.

- [ ] **Step 6: Add native smoke jobs before publication**

Add a matrix job that downloads `release-dist` and runs the correct script:

```yaml
  native-smoke:
    needs: package
    strategy:
      matrix:
        include:
          - runner: ubuntu-24.04
            binary: tunnelmesh-server
            os: linux
            arch: amd64
            shell: bash
          - runner: macos-14
            binary: tunnelmesh-agent
            os: darwin
            arch: arm64
            shell: bash
          - runner: windows-2022
            binary: tunnelmesh-client
            os: windows
            arch: amd64
            shell: pwsh
    runs-on: ${{ matrix.runner }}
    steps:
      - uses: actions/checkout@v4
      - uses: actions/download-artifact@v4
        with: {name: release-dist, path: dist}
      - if: runner.os != 'Windows'
        run: ./scripts/smoke-release-artifact.sh dist "${GITHUB_REF_NAME}" "${{ matrix.binary }}" "${{ matrix.os }}" "${{ matrix.arch }}"
      - if: runner.os == 'Windows'
        shell: pwsh
        run: ./scripts/smoke-release-artifact.ps1 -Dist dist -Version "$env:GITHUB_REF_NAME" -Binary "${{ matrix.binary }}" -OS "${{ matrix.os }}" -Arch "${{ matrix.arch }}"
```

Task 3's archive verifier proves the other target archives exist and have safe contents. Add Linux arm64 and Windows arm64 native jobs when trusted runner capacity is available.

- [ ] **Step 7: Publish only after every smoke job passes**

Add the sole write-capable job:

```yaml
  publish:
    needs: [package, native-smoke]
    runs-on: ubuntu-24.04
    permissions:
      contents: write
    steps:
      - uses: actions/download-artifact@v4
        with: {name: release-dist, path: dist}
      - run: gh release create "${GITHUB_REF_NAME}" dist/*.tar.gz dist/*.zip dist/*_checksums.txt --verify-tag --generate-notes
        env:
          GH_TOKEN: ${{ github.token }}
```

`gh release create` is the only publishing action. A failed quality, package, checksum, or native smoke job leaves no GitHub release.

- [ ] **Step 8: Validate workflow syntax and release policy**

Run locally:

```bash
git diff --check
rg -n 'pull_request|branches:' .github/workflows/release.yml
rg -n 'gh release create|contents: write|needs: \[package, native-smoke\]' .github/workflows/release.yml
rg -n 'goreleaser.*publish|sc\.exe create|gh release download' .github deploy scripts Makefile
```

Expected: no whitespace errors; no branch or pull-request publishing trigger; exactly one publication command under the final job; no direct GoReleaser publishing, direct SCM creation, or smoke download from an already-public release.

- [ ] **Step 9: Perform a controlled release-candidate smoke after explicit Git authorization**

Only after a human authorizes tag creation and push, use an agreed release-candidate version:

```bash
git tag -s v0.1.0-rc.1 -m 'TunnelMesh v0.1.0-rc.1'
git push origin v0.1.0-rc.1
gh run watch --exit-status
gh release view v0.1.0-rc.1
```

Expected: quality passes on Linux/macOS/Windows, packaging creates exactly 18 archives and one checksum file, native smoke jobs pass before the publish job starts, and only then does the GitHub release appear. These commands are operator documentation and are not executed while writing or implementing this plan without separate Git authorization.

---

## Final Verification Gate

- [ ] Run the mandatory repository checks:

```bash
go test ./... -count=1
go test -race ./...
go vet ./...
git diff --check
cd web && npm test -- --run && npm run build
```

Expected: all PASS.

- [ ] Run release and deployment static checks from a clean worktree:

```bash
make release VERSION=v0.1.0
./scripts/verify-release.sh dist v0.1.0
./scripts/verify-web-embed.sh
./scripts/verify-nginx.sh deploy/nginx/tunnelmesh.conf
./scripts/verify-docs.sh
```

Expected: all PASS; `git status --short` remains empty after `make release`.

- [ ] Run platform-native checks on their actual operating systems:

```text
Linux:   systemd-analyze verify, systemctl watchdog/start/stop, Docker Buildx amd64/arm64
macOS:   plutil -lint, launchctl bootstrap/print/bootout, darwin arm64 --version
Windows: PowerShell XML/hash checks, WinSW install/query/recovery/uninstall, windows amd64 --version
```

Expected: all service managers report the process running, readiness returns 200, and controlled stop completes within the configured timeout.

- [ ] Inspect release content and secret exposure:

```bash
git status --short --untracked-files=all
git diff --cached
find dist -maxdepth 1 -type f -print | LC_ALL=C sort
rg -n -i 'bearer |password|private[_-]?key|mysql.*dsn|token=' dist deploy .github docs Makefile scripts
```

Expected: no staged secret or runtime credential; matches in documentation are explanatory field names or redacted examples only; archives contain no runtime configuration or data.

- [ ] Record platform limitations in the release evidence:

```text
- Race tests are native tests and are not replaced by cross-compilation.
- systemd watchdog verification requires a Linux system running systemd as PID 1.
- launchd verification requires macOS and root for LaunchDaemon installation.
- WinSW v2.12.0 provides x64 only; Windows ARM64 service use depends on OS x64 emulation, while the TunnelMesh executable itself is native ARM64.
- macOS notarization, Apple code signing, Windows Authenticode signing, SBOM, and artifact signatures are not part of this design; SHA256 verifies integrity but not publisher identity.
```

Expected: the release report names which native checks ran and which target artifacts received structural cross-build verification only.
