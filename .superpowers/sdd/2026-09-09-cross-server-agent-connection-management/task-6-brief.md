### Task 6: Documentation, Embed Sync, and Full Verification

**Files:**

- Modify: `docs/user-guide/server-admin.md`
- Modify: `docs/operations/connection-pool.md`
- Modify: `docs/operations/troubleshooting.md`
- Modify: `docs/api/openapi.yaml`
- Regenerate: `internal/server/web_dist`

- [ ] **Step 1: Update documentation**

Document:

- Cross-node prerequisites: unique `node.id`, enabled relay mTLS, valid server-node token, and reachable relay address.
- Query and close API examples.
- The fact that closing one physical connection does not disable the Agent or prevent reconnect.
- Why an unreachable owner returns `503` and the lease is retained.
- Rollout note: all participating Server nodes must run the new version for complete cluster visibility.

- [ ] **Step 2: Sync embedded frontend**

Run:

```bash
rm -rf internal/server/web_dist
cp -R web/dist internal/server/web_dist
```

Before running deletion, confirm that `web/dist` exists and `internal/server/web_dist` contains only generated assets.

- [ ] **Step 3: Run full verification**

Run:

```bash
go test ./... -count=1
go test -race ./...
go vet ./...
git diff --check
cd web
npm test -- --run
npm run build
```

Expected result: every command exits successfully.

### Final Review Checklist

- [ ] Registry registration, renewal, statistics, and release are wired into the real Agent WebSocket lifecycle.
- [ ] Query merges local and remote state without leaking another owner's Agent.
- [ ] Close is exact-fenced by connection ID and connection epoch.
- [ ] Remote close uses authenticated mTLS and does not expose a public unauthenticated control endpoint.
- [ ] Unreachable owner returns `503` and preserves the lease.
- [ ] UI supports query, refresh, and close in Chinese and English.
- [ ] OpenAPI, user guide, operations guide, troubleshooting guide, and embedded assets are updated.
- [ ] All required Go and frontend validation commands pass.
