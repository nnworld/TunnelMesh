### Task 5: Web后台 Agent metadata and server function guide

**Files:** Modify `web/src/api/client.ts`, `web/src/views/Agents.vue`, `web/src/router.ts`, `web/src/tests/routes.spec.ts`, `docs/README.md`, and `README.md`; create `web/src/views/AgentDetail.vue` and `docs/user-guide/server-admin.md`.

**Interfaces:** Produce an Agent detail route with metadata card and a complete server后台 function guide.

- [ ] Write failing frontend tests for the detail route, metadata endpoint call, field/source/value rendering, stale badge, redaction, and no edit control.
- [ ] Run `cd web && npm test -- --run src/tests/routes.spec.ts` and verify RED.
- [ ] Add typed API models, `/agents/:id`, loading/error/empty states, metadata table, stale state, timestamps, and masked values using Element Plus.
- [ ] Document login/bootstrap, dashboard, Agent list/detail/metadata, policy, explicit route, wildcard route, tunnel status, audit log, roles, idempotency, and credential recovery in `server-admin.md`.
- [ ] Run `cd web && npm test -- --run && npm run build`.

