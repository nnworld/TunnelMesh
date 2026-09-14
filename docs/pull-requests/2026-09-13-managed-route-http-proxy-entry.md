# Managed-route HTTP proxy entry (tp-*)

## Title

`feat(observability): add proxy entry dashboard row, alerts and docs`

The feature landed as sixteen commits (Task 0 through Task 15); this record covers the whole
range, not only the final commit.

## Target Branch

`main`

## Summary

Users can now point a browser or an operating system at `https://tp-<name>.<domain_suffix>` as a
standard HTTPS proxy and reach the internet — or an Agent's internal network — through a chosen
egress Agent, without installing `tunnelmesh-client`. The egress Agent, the authentication mode,
the source ACL and the target restrictions are all configured in the admin console and take effect
within five seconds.

Architecturally the public edge stays exactly as it was: one 443 listener, selected by SNI. An
OpenResty server block built on a `ngx_http_proxy_connect_module`-patched kernel intercepts
`CONNECT` in the server-level access phase, injects three trusted headers
(`X-TunnelMesh-Route` from SNI, `X-TunnelMesh-Client-IP`, `X-TunnelMesh-Client-Port`) and splices
bytes to a plaintext loopback listener inside `tunnelmesh-server`. OpenResty carries no policy at
all. The Server is the single authorization point: route identity, source ACL, Basic auth with
exponential backoff, target validation, per-route and global concurrency caps, audit events and
metrics all live in `internal/proxyentry` and `internal/server/proxy_entry*.go`. Egress reuses the
existing relay path, so cluster mode works without changes.

Implementation-wise the feature adds a policy kernel (`internal/proxyentry`), a third in-process
listener (`server.proxy_entry.listen`, default `127.0.0.1:8089`) guarded by `trusted_proxies`,
a `proxy_basic` credential type, an `http-proxy` route protocol stored in the existing `tunnels`
table with the `*` / `0` sentinel target, admin API extensions, an Element Plus form and list rewrite,
shipped OpenResty artifacts and an observability row. **Schema stays at v13: this feature is zero
DDL.**

## User Impact

New:

- tp-* managed HTTP proxy routes, created in 路由管理 with the `HTTP 代理入口 (tp-)` protocol.
- Two authentication modes: `none` and `basic` (a `proxy_basic` credential holding username and
  password, created under 密钥管理).
- Source IP/network ACL, defaulting to deny-all, with a one-click `0.0.0.0/0` "allow everything".
- Optional target CIDR and target port restrictions, plus an "allow private targets" switch. Cloud
  metadata addresses are refused regardless of that switch.
- Per-route concurrent tunnel cap and a 256-character operator note.
- A usage drawer in the admin console with the full proxy URL, macOS / Windows / PAC / curl
  examples and the stable error-code table.
- Absolute-form (non-CONNECT) proxy requests are supported as well as `CONNECT` tunnels.

Unchanged:

- Existing explicit and `tm-*` wildcard reverse-proxy routes.
- `tunnelmesh-client forward socks5` and `forward http-proxy` local endpoints.
- WebSSH / WebSFTP, agents, tokens, accounts and the release page.
- Public ingress: still HTTP/HTTPS/WebSocket only. No public UDP listener was added, and no new
  public port is opened.

## API / Schema / Configuration Impact

`POST /api/v1/routes` and `PATCH /api/v1/routes/{id}` accept `protocol: "http-proxy"` plus
`authMode`, `credentialId`, `sourceCIDRs`, `targetCIDRs`, `targetPorts`, `allowPrivateTargets`,
`maxConcurrentTunnels` and `description`. `domain` must be the full `tp-<name>.<suffix>`;
`targetHost` / `targetPort` / `config` / `hostHeader` / `targetScheme` / `tlsServerName` must be
omitted (create) or left at the `*` / `0` sentinel (update). The protocol cannot be switched to or
from `http-proxy`. An `http-proxy` route claims its whole hostname, so the duplicate check ignores
`path_prefix`. `GET` responses gained the read-only `proxyUrl`.

`POST /api/v1/credentials` and `PATCH /api/v1/credentials/{id}` accept `type: "proxy_basic"` with a
`username`; the username is surfaced through the existing `publicKey` field so no list column had to
change. `docs/api/openapi.yaml` was updated in the same change.

Schema: **no change**. `tunnels` stores the sentinel target and the proxy policy in the existing
`config` JSON; `credentials` reuses the existing secret columns. `schema_meta.version` stays 13 and
there is no migration in either direction.

Configuration: thirteen new keys under `server.proxy_entry` — `enabled` (default `false`), `listen`
(`127.0.0.1:8089`), `trusted_proxies` (`["127.0.0.1/32","::1/128"]`), `domain_suffix` (required
when enabled), `route_header`, `client_ip_header`, `client_port_header`, `connect_timeout` (10s),
`idle_timeout` (300s), `shutdown_timeout` (30s), `max_concurrent_tunnels` (512),
`max_header_bytes` (16384) and `auth_backoff_threshold` (5). Environment variables
(`TUNNELMESH_SERVER_PROXY_ENTRY_*`) and flags (`--server.proxy_entry.*`) are equivalent;
`check-config` rejects a non-loopback `listen` combined with `0.0.0.0/0` or `::/0` in
`trusted_proxies`.

## Security and Authorization Impact

- The three trusted headers are written only by OpenResty. Client-supplied copies are dropped by the
  Lua mover, which sends a fixed header whitelist; the E2E smoke test forges all three and asserts
  the forged values never arrive.
- Route identity comes from TLS SNI only. There is no fallback to the `Host` header, so a client
  cannot impersonate another route.
- The internal listener binds loopback by default and closes connections from peers outside
  `trusted_proxies` **before reading the request**. Startup validation refuses a non-loopback
  `listen` with an all-zeroes trusted range.
- Basic passwords reuse the existing AES-256-GCM secret store (`TUNNELMESH_TOKEN_ENCRYPTION_KEY`).
  They are never returned by the list API, never logged, and a missing key yields 503
  `credential_secret_unavailable` rather than an open proxy.
- Targets are validated twice: once in the Server policy kernel and again on the Agent side
  (SSRF / loopback / private / link-local / CIDR / port). Cloud metadata addresses are always
  refused.
- Authentication failures back off exponentially per route after `auth_backoff_threshold`
  (30s doubling to a 15m cap); during backoff no password comparison happens at all.
- Unknown routes, disabled routes and ACL denials render the same 403 text so the entry cannot be
  used as a `tp-*` name oracle; only logs, audit and the `error_class` metric label distinguish them.
- Every rejection carries a stable error code. 407 always carries
  `Proxy-Authenticate: Basic realm="TunnelMesh", charset="UTF-8"`; 503 always carries
  `Retry-After: 5`.
- `Proxy-Authorization` is never written to logs, metrics, audit details or traces. The E2E asserts
  the base64 secret does not appear in the OpenResty error log.

## Test Evidence

Executed on `Darwin arm64`, Go `go1.27.1`, Node `v24.15.0`. Development baseline `main` @
`b3acd34`; the seventeen commits were rebased onto `origin/main` @ `50b4130` before pushing (see
Integration Status). Gate results below are from the pre-rebase run except where noted.

```text
go test ./... -count=1                      all packages ok (20 packages, 0 FAIL)
go test -race -timeout 40m ./...            RACE_EXIT=0, 20 packages ok; slowest internal/server 291.594s
go vet ./...                                VET_EXIT=0, no output
gofmt -l internal/ cmd/ deploy/ test/       no output
git diff --check                            no output
go test ./deploy/... -count=1               grafana / install / openresty all ok
cd web && npm test -- --run                 30 files, 243 tests passed (244 after the rebase)
cd web && npm run build                     built, mirrored to internal/server/web_dist
./scripts/verify-web-embed.sh               "web/dist and internal/server/web_dist match"
node test/e2e/proxy-entry/run.mjs           SKIP (TM_PROXY_E2E_NGINX is not 1), exit 0
TM_PROXY_E2E_NGINX=1 node test/e2e/proxy-entry/run.mjs
                                            SKIP (missing docker CLI with a reachable daemon), exit 0
```

Feature-scoped runs used as acceptance evidence:

```text
go test ./internal/proxyentry/ -count=1 -v                              23 top-level tests pass
go test ./internal/server/ -run 'ManagedRoute|APIRouteProxy|ProxyEntry|Credential' -v
                                                                        45 top-level tests pass
go test ./internal/observability/ -run 'ProxyEntry|Metrics' -v          10 top-level tests pass
go test ./internal/storage/ -run 'Credential' -v                        6 top-level tests pass
go test ./internal/config/ -run 'ProxyEntry|Defaults' -v                8 top-level tests pass
```

Red-green evidence recorded per task; two examples:

- `go test ./deploy/grafana/` failed with `dashboard must contain exactly six row panels` before the
  `HTTP Proxy Entry` row was added, and passed after.
- `go test ./deploy/openresty/` failed with
  `read tunnelmesh_proxy_entry.lua: open tunnelmesh_proxy_entry.lua: no such file or directory`
  before the artifacts were written, and passed after.

**Not executed on this machine** (must be run before release, and are recorded here as unverified
rather than passed):

| Item | Reason | What is needed |
| --- | --- | --- |
| `TM_PROXY_E2E_NGINX=1 node test/e2e/proxy-entry/run.mjs` (13 checks) | no `docker` binary and no reachable daemon | a host with docker plus access to `openresty.org` and `github.com` for the first image build |
| `docker build -f deploy/openresty/Dockerfile.proxy-connect` and the `nginx -V \| grep proxy_connect` check | same | same |
| `promtool check rules deploy/prometheus/alert-rules.yaml` | `promtool` not installed | Prometheus tooling. Fallback used: `ruby -ryaml` parsed the file and reported `groups=1 rules=9` (7 existing + 2 new) |
| Real MySQL contract tests | `TUNNELMESH_TEST_MYSQL_DSN` not set | a MySQL instance; only the SQLite dialect ran |
| "Egress IP belongs to the Agent network", cross-node cluster trace, and the 5-second propagation on a live deployment | needs a real Agent, a real OpenResty and DNS | commands are written in `docs/deployment/openresty-proxy-entry.md` and `docs/user-guide/http-proxy-entry.md` |

Acceptance criteria from spec §18, mapped to evidence:

| spec §18 | Verified here | Evidence / caveat |
| --- | --- | --- |
| 1. Route created in the console takes effect within 5s, no Server restart, no nginx change | Partially | `internal/server` ManagedRoute/APIRouteProxy tests prove `http-proxy` rows stay out of the reverse-proxy table and are readable from the proxy snapshot immediately. The 5s figure is the pre-existing `loadManagedRoutes` TTL and was not re-measured end to end |
| 2. In-ACL client works and egresses via the Agent; out-of-ACL gets 403; wrong password gets 407 and backs off on the 6th attempt | Partially | ACL, auth, backoff and the 403/407 handler paths are unit/integration tested. "Egress IP belongs to the Agent network" needs a real Agent and was **not** verified |
| 3. `authMode=none` accepts requests with and without wrong credentials, ACL still enforced | Yes | `internal/server` proxy entry tests |
| 4. `allowPrivateTargets=true` reaches internal targets; `169.254.169.254` always 403 | Yes | `internal/proxyentry` target policy tests |
| 5. Cluster mode: tunnel works when the Agent is on another node, trace hops complete | Partially | The tunnel path reuses the existing relay fixtures and is covered; `POST /api/v1/agents/{agentId}/trace` is unchanged by this feature, so hop completeness was **not** re-verified |
| 6. All gates pass, spec §16 docs complete, OpenAPI matches the implementation | Partially | Go/frontend gates pass; `promtool` and docker-dependent items are listed as not executed above. `docs/api/openapi.yaml` was compared field by field against the Task 12 implementation |

## Release Steps

1. Upgrade `tunnelmesh-server`. With `server.proxy_entry.enabled=false` (the default) behaviour is
   identical to the previous release, so this step is safe on its own.
2. Deploy the OpenResty artifacts: run the two-level kernel check, install
   `tunnelmesh_proxy_entry.lua`, render `tunnelmesh-proxy.conf.example`, merge the `upstream` and
   `server` blocks into `http{}`, add `worker_shutdown_timeout 300s;` to the main context,
   `nginx -t`, then reload.
3. Publish wildcard DNS for `tp-*.<domain_suffix>` and make sure the certificate covers
   `*.<domain_suffix>`.
4. Turn the feature on: set `server.proxy_entry.enabled=true` and `domain_suffix`, run
   `tunnelmesh-server check-config`, restart.
5. Create one tp-* route in the admin console (Basic credential plus a narrow ACL) and verify with
   `curl -x https://tp-<name>.<suffix> --proxy-user 'u:p' https://ifconfig.me`.
6. Re-import `deploy/grafana/dashboards/tunnelmesh.json`; the new `HTTP Proxy Entry` row is empty
   until step 4.

## Rollback Steps

Five-minute stop-loss, in order of preference:

1. Remove the rendered `upstream tunnelmesh_proxy_entry` and tp-* `server` blocks and
   `nginx -s reload`. The entry disappears immediately; admin and reverse-proxy routes are untouched.
2. Or set `server.proxy_entry.enabled=false` and restart the Server. The internal listener goes
   away and OpenResty answers 502.
3. Individual routes can simply be disabled in the console.

No data rollback is involved: the feature is zero-DDL and Schema stays at v13. `http-proxy` rows in
`tunnels` are inert once the entry is disabled, and can be deleted at leisure.

## Reviewer Focus

- `internal/proxyentry` fail-closed semantics: an empty ACL denies everything, an unparsable stored
  CIDR denies everything, and a disabled or soft-deleted credential never authenticates.
- `internal/server/proxy_entry.go`: the hijack path and `spliceWithIdleTimeout` half-close / EOF
  handling, and that every rejection records its metric before the response is written.
- The `*` / `0` sentinel must be skipped by `loadManagedRoutes`; otherwise `location /` wildcard
  reverse-proxy matching would swallow tp-* hostnames.
- `deploy/openresty/tunnelmesh_proxy_entry.lua`: no policy logic of any kind, every `return` path
  closes the upstream socket, `ngx.on_abort` registered, `ngx.exit(444)` used so nginx does not
  append its own error page, and no `ngx.print` / `ngx.say` / `ngx.header` after the raw socket is
  taken.
- Logs, metrics and audit details contain no credential material; `Proxy-Authorization` appears
  nowhere but the forwarded request.
- `deploy/openresty/openresty_artifacts_test.go` is the only executable guard tying the Lua and
  nginx artifacts to the Go configuration defaults — check that its markers still match after any
  config default change.

## Integration Status

Development started from `b3acd34` (`docs(superpowers): complete plan file inventory`) and the
feature spans seventeen commits, ending with this one.

Four commits from another session landed on `origin/main` while this feature was in flight:
`91553a1` (drop the Go test/vet steps from `release.yml`), `da994b3` (simplify the release
management page), `6227c17` (hide the tunnels menu) and `50b4130` (record the package license).
The repository history is linear, so the seventeen feature commits were **rebased** onto
`origin/main` @ `50b4130`; the rebase completed with **no conflicts**.

| Overlapping file | Their change | Our change | Resolution |
| --- | --- | --- | --- |
| `web/src/i18n/messages/zh-CN.ts` | rewrote the `downloads` entry (dropped `platform`, `archive`, `download`, `checksumCommand`, `copied`, `copyFailed`) | added `credentials.proxyBasic` / `username*` and the `routes.proxy*` keys | different lines of the same file; git merged both, no manual edit needed |
| `web/src/i18n/messages/en-US.ts` | same `downloads` rewrite | same `credentials` / `routes` additions | same |

No other file was touched by both sides: their commits also changed
`.github/workflows/release.yml`, `docs/user-guide/server-admin.md`, `web/package-lock.json`,
`web/src/layouts/AppShell.vue`, `web/src/views/Downloads.vue`, `web/src/tests/shell.spec.ts` and
`web/src/tests/views.spec.ts`, none of which this feature modifies.

Because the rebase pulled in frontend changes, the frontend gates were re-run afterwards:
`npm test -- --run` reports 30 files and **244** tests passing (one more than pre-rebase, added by
their `shell.spec.ts` case), `npm run build` succeeds and mirrors into `internal/server/web_dist`,
and `./scripts/verify-web-embed.sh` reports `web/dist and internal/server/web_dist match`. The Go
side is unaffected by their commits, but `go vet ./...`, `gofmt -l internal/ cmd/ deploy/ test/`,
`git diff --check` and `go test ./deploy/... -count=1` were re-run after the rebase and are clean.

Generated `web/dist` and `internal/server/web_dist` are gitignored (`internal/server/web_dist/*`)
and are therefore not part of any commit; they are rebuilt by `npm run build` and checked by
`scripts/verify-web-embed.sh`.
