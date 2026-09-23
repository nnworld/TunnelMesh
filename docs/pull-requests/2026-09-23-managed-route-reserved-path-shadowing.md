# Managed-route hosts shadowed by control-plane path prefixes

## Title

`fix(server): dispatch managed routes by host, not path prefix`

## Target branch

`main`

## Summary

Managed routes whose upstream paths begin with `/api/` returned TunnelMesh's own management API 404 envelope instead of the upstream response:

```json
{"code":404,"msg":"Not Found","data":{"error":"not found"}}
```

Reported against a live route, where `https://tm-6000d.claw.qihoo.net/skills` worked but `https://tm-6000d.claw.qihoo.net/api/skill/claw/cate` did not.

`NewWebHandlerWithManagedRoutes` decided **path first, Host second**: `/api/`, `/health/`, `/metrics`, `/ws/agent`, `/ws/client`, `/ws/webssh/` and then any remaining `/ws/` prefix were all matched before the Host-based managed-route dispatcher was ever consulted. The management API answers every path that does not start with `/api/v1/` with a 404 envelope (`internal/server/api.go:316`), so an upstream's `/api/*` never reached the Agent. The same root cause broke upstream `/ws/*` endpoints (a bare 404, which also breaks `protocol: websocket` routes) and upstream `/health/*` and `/metrics`.

The reserved prefixes exist to stop the SPA history fallback from swallowing the console's own API and WebSocket calls. That is a control-plane need, but it was implemented as a Host-independent global rule, so it claimed paths on other origins.

## User impact

An upstream service behind a managed route can now use `/api/...` and `/ws/...` paths, which is how most REST backends and SPA APIs are laid out. Before this change any such route silently returned the management API's 404 envelope, or a bare 404 for `/ws/*`, with nothing in the Agent or route configuration to explain it.

Dispatch is now Host-scoped:

- **`tm-*` namespace hosts** (the `tm-*.<suffix>` wildcard and dynamic agent/ip/port hostnames that TunnelMesh itself allocates) hand **every** path to the route. Nothing is reserved, so an upstream may serve its own `/health`, `/metrics`, `/ws/agent`, or `/ws/client`.
- **Explicit-domain routes** (for example `git.example.com`) hand every path to the route except four control-plane endpoints: `/ws/agent`, `/ws/client`, the `/health/` prefix, and `/metrics`.
- **Hosts that match no route** are unchanged: reserved prefixes first, then the SPA history fallback.

The asymmetry is deliberate. `tm-*` names are allocated by TunnelMesh and can never be the console origin, the Agent dial-in origin, or a load-balancer health target, so reserving anything there only blocks legitimate upstream paths. An explicit domain is typed in by an operator and can collide with either origin; losing `/ws/agent` would stop every Agent behind that origin from reconnecting and take down all of their routes at once, and a health probe that had to resolve routes first would let a route-table outage pull healthy nodes out of the load balancer. Those four paths skip route resolution entirely rather than merely losing to it, so control-plane health never depends on the route table or the database.

## API, schema, and configuration impact

None. No HTTP API, database schema, configuration key, or protocol change. No new configuration is introduced: the existing design already treats a Host that matches no route as the control plane, and `internal/config` has no authoritative control-plane origin to key on. Adding one would create a second source of truth for a distinction the `tm-` namespace predicate already makes without I/O.

## Security impact

No change to authentication, authorization, or redaction. The management API keeps its own bearer authentication; it is simply no longer reached for Hosts that belong to a route. Route resolution still runs the same policy validation (`validateRoutePolicy`, dynamic-host policy checks), and the new predicate cannot widen what a route may serve: it only decides whether the control plane or the route table is consulted first, and an unmatched `tm-*` name falls through to exactly the previous behaviour. The predicate is a pure string test on the Host label, so it introduces no lookup that an attacker could influence beyond the Host header they already control.

One operational caveat is documented rather than enforced: pointing an explicit-domain route at the console origin or the Agent dial-in origin now hands that Host's console surface to the route. That configuration was already broken before this change (its static assets were proxied upstream), and it is called out in `docs/user-guide/managed-http-route.md` and `docs/operations/troubleshooting.md`.

## Changes

- `internal/server/web.go`: add `managedNamespaceLabelPrefix`, `isManagedNamespaceHost`, and `isControlPlaneReservedPath`; move managed-route dispatch ahead of the control-plane path prefixes with the two-branch rule above; remove the now-redundant late dispatcher call so `TryServeHTTP` runs at most once per request; update the `ManagedRouteDispatcher` and file-header comments.
- `internal/server/web_dispatch_test.go`: six dispatch-order tests using a recording dispatcher stub.
- Docs: `docs/user-guide/managed-http-route.md` gains "上游路径与控制面保留端点" with both ownership tables and the origin-collision warning; `docs/operations/troubleshooting.md` gains a triage entry for the 404 envelope; retroactive plan recorded and indexes regenerated.

## Tests run

- `go build ./...`
- `go test ./internal/server -count=1` - full package passes (46s)
- `go test ./... -count=1` - all packages pass
- `go test -race ./internal/server ./internal/routing -count=1`
- `go vet ./...` - clean
- `git diff --check` - clean
- `python3 scripts/gen_doc_index.py`

New tests:

- `TestManagedNamespaceHostServesEveryPath` (red first): `/api/skill/claw/cate`, `/api/v1/tokens`, `/ws/chat`, `/ws/agent`, `/ws/client`, `/health/live`, `/metrics`, `/skills` on `tm-6000d.claw.qihoo.net` must all be served upstream. Before the fix: `/api/skill/claw/cate on a managed-route host was served by "api", want the upstream route`.
- `TestExplicitDomainRouteServesUpstreamAPIAndWSPaths` (red first): `/api/v1/repos`, `/ws/git`, `/health`, `/readyz` on `git.example.com`. Before the fix: `served by "api"`.
- `TestControlPlaneEndpointsReservedOnExplicitDomainRoute`: the four reserved endpoints stay on the control plane and the dispatcher is never called for them.
- `TestControlPlaneHostKeepsReservedPathOrder`: an unmatched Host keeps `/api/` -> API, `/health/` and `/metrics` -> health, `/ws/agent` and `/ws/client` -> their handlers, `/ws/webssh/tick` -> broker, unknown `/ws/unknown` -> 404, `/some/spa/route` -> SPA fallback.
- `TestUnmatchedManagedNamespaceHostFallsThrough`: a `tm-*` name no route claims still reaches the management API and the health endpoint.
- `TestControlPlaneHealthSurvivesRouteTableOutage`: with the dispatcher writing 503, the control-plane origin's health, metrics, and both WebSocket handshakes still return 200 and the route table is consulted zero times.

Tests 3-6 passed before the change and are regression guards for the reorder. No frontend or embedded-asset change, so `npm test`, `npm run build`, and `scripts/verify-web-embed.sh` were not required.

## Release steps

1. Merge after review and CI checks.
2. Deploy the Server. **The Server alone is sufficient**; Agent and Client do not need upgrading and there is no lockstep requirement.
3. Canary on one Server node first: verify a `tm-*` route with `/api/` prefixed paths, a `protocol: websocket` route whose endpoint is under `/ws/`, console login, Agent reconnection, and load-balancer health checks.
4. Notify operators of the ownership tables above, in particular that upstream `/health` and `/metrics` on an explicit-domain route still belong to the control plane.

## Rollback steps

Revert the merge commit and redeploy the previous Server binary. No persisted state to unwind. Rolling back restores the defect: `/api/*` and `/ws/*` upstream paths on managed routes return the management API 404 envelope or a bare 404.

## Reviewer focus

- Whether the `tm-` label predicate is the right discriminator, or whether an explicit control-plane origin configuration should be introduced instead (deferred here as a second source of truth).
- Whether the four endpoints reserved on explicit-domain routes should also be released to the route, making the two Host classes symmetric.
- Whether an unmatched `tm-*` name should fall through to the control plane (current behaviour, unchanged from before) or answer 404.
- Whether route creation should reject a domain that collides with the console or Agent origin, which would need a configured control-plane origin to detect.
- Dispatch cost: management API requests on a non-`tm-` Host now consult the cached route table first. The resolver snapshot is in-memory with a 5s TTL; the only added failure mode is a 503 when the table has never loaded, which requires the database to be unreachable at cold start.

## Integration status

Implementation and local validation are complete on `codex/managed-route-reserved-path-shadowing`. The plan was confirmed by the user with the amendment that `tm-*` hosts reserve no paths at all, and is recorded in `docs/superpowers/plans/2026-09-23-managed-route-reserved-path-shadowing.md`. Pull-request review and merge are pending.
