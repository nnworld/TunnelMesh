# SDD ledger — plan: docs/superpowers/plans/2026-09-06-scoped-token-auth-implementation.md

## Execution constraints

- Baseline: `go test ./... -count=1` passed on main HEAD `163fe12121d2f839ea4bf4907f55a8e7dd835057` with the existing uncommitted heartbeat/metadata changes present.
- Ruling: execute sequentially in the existing workspace instead of the stale detached worktree — the uncommitted heartbeat/metadata changes are dependencies of the stability work and must not be lost or copied incompletely. Cost if wrong: task diffs need snapshot-based isolation instead of commit ranges.
- Ruling: implementers must not commit despite the stock SDD template — the repository instructions require separate explicit authorization for commit/push/merge. Cost if wrong: progress is recoverable from the working tree and ledger rather than task commits.
- Ruling: subagents inherit the current model because the execution service rejected the documented explicit model identifiers twice. Cost if wrong: per-role cost routing is unavailable, but the review gates remain unchanged.
- Review packaging: snapshot each task's declared files before implementation and create a no-index diff against the post-task files; reviewers receive that task-only package.

## Task ledger

- [x] Task 1: Record the credential-boundary ADR and schema migration
- [x] Task 2: Implement the ServiceToken repository contract
- [x] Task 3: Add CredentialService lifecycle and scope authorization
- [x] Task 4: Expose Token management API and audit events
- [x] Task 5: Enforce Agent Token and WebSocket boundary checks
- [x] Task 6: Implement authenticated Client WebSocket and per-stream authorization
- [x] Task 7: Protect Server-node relay with Token plus mTLS
- [x] Task 8: Add Token management Web UI and migration documentation
- [x] Task 9: Security integration gate

## Pre-flight consistency scan

| Tasks | Producer → consumer / shared surface | Finding |
| --- | --- | --- |
| 1 → 2 | schema v3, `storage.ServiceToken`, `TokenType` → repository contract | Consistent; Task 2 consumes the exact model and table introduced by Task 1. |
| 1 → 3 | service-token hash/scope fields → credential lifecycle | Consistent; secrets remain outside storage. |
| 1 → 4 | schema and Token model → API serialization | Consistent; API must expose derived state, never hash/secret. |
| 2 → 3 | `ServiceTokenRepository` → `CredentialService` | Consistent; lifecycle operations have create/get/list/revoke/touch/rotate primitives. |
| 2 → 4 | SQL-filtered cursor page → management API | Consistent; authorization filtering must occur before pagination. |
| 3 → 4 | lifecycle, validation, authorization → Handler/Service/API | Consistent; Handler does not execute SQL. |
| 3 → 5 | `ValidateAs(..., TokenTypeAgent)` → Agent WS authentication | Consistent; Agent binding is checked against registration identity. |
| 3 → 6 | `TokenIdentity` and `AuthorizeStream` → Client WS per-OPEN enforcement | Consistent; each OPEN repeats scope plus Policy checks. |
| 3 → 7 | `ValidateAs(..., TokenTypeServerNode)` → relay interceptors | Consistent; mTLS and application Token are both required. |
| 4 → 8 | Token API → Vue Token management UI | Consistent; one-time secret remains component-memory-only. |
| 4 → 9 | management API and audit events → security E2E | Consistent; E2E can create credentials through public API. |
| 5 ↔ 6 | exact WS router and `runtime.go` | Consistent if Task 5 reserves `/ws/client` without implementing Agent fallback; Task 6 owns the Client handler. |
| 5 ↔ 7 | `config.go` and `runtime.go` | Consistent; native/public TLS and relay mTLS are separate listeners/config domains. |
| 5 → 8 | Agent Token migration behavior → operator docs | Consistent; legacy connection Tokens stay disabled by default and Deprecated. |
| 6 ↔ 7 | `config.go` and `runtime.go` | Consistent; Client WSS and node relay authentication do not share credential types. |
| 6 → 8 | Client Token config → client/operator docs | Consistent; raw credentials are injected, not embedded in docs or assets. |
| 6 → 9 | Client TCP/UDP streams → security E2E | Consistent; denied logical streams leave the authenticated WebSocket usable. |
| 7 → 9 | server-node mTLS+Token → cluster security boundary | Consistent; Task 9 may verify through relay test doubles if a multi-node E2E is not yet available. |
| 8 → 9 | embedded Web assets and migration docs → final gate | Consistent; final gate rebuilds frontend and checks secrets. |

| Task | Internal consistency | Finding / ruling |
| --- | --- | --- |
| 1 | Tests vs schema/model/ADR | Consistent. Ruling: `service_tokens` is the only new credential table; do not ALTER `api_tokens`. |
| 2 | Repository tests vs SQL/transaction behavior | Consistent. Rotation must remain atomic on SQLite and MySQL. |
| 3 | Lifecycle tests vs authorization API | Consistent. Add repository-error tests that prove fail-closed behavior; existing authorized streams remain subject to their normal heartbeat/lease boundary. |
| 4 | API tests vs Handler → Service → Repository | Consistent. Ruling: idempotency storage may persist only non-secret metadata; replay omits `secret`. |
| 5 | Boundary/TLS tests vs runtime changes | Consistent after the plan amendment adding optional native TLS 1.2+. |
| 6 | Client transport tests vs per-stream authorization | Consistent. The existing Client CLI shell paths are replaced only where the task names them. |
| 7 | Relay tests vs mTLS+Token interceptors | Consistent. Certificate SAN, Token node binding, and registry epoch are independent checks. |
| 8 | UI tests vs one-time secret UX/docs | Consistent. Human prose is reviewed directly; frontend behavior is tested. |
| 9 | E2E scenarios vs complete security boundary | Consistent. No commit occurs at the staged-content step without separate authorization. |

Task 1: minor (deferred): live MySQL test helpers use `context.Background()` without a bounded deadline; add deadlines when the MySQL contract suite is next revised.
Task 1: fix round 1/5 (1 addressed, 0 open — ADR now derives availability from credential timestamps and bound-resource state; no commits, snapshot diff reviewed).
Task 1: complete (no commits authorized, task-only snapshot review clean).
Task 2: complete (no commits authorized, task-only snapshot review clean; live MySQL execution deferred until a test DSN is available).
Task 3: minor (deferred): add focused tests for multi-page Agent Policy evaluation, malformed/stuck cursors, concurrent Validate/Close, saturated last-used queue, and blocked last-used writes.
Task 3: minor (deferred): `credential_service.go` combines lifecycle, scope compilation, Policy evaluation, and last-used worker; split by responsibility during the next auth refactor if the file remains over 600 lines.
Task 3: Ruling: bound Token scope to 16 KiB serialized JSON, at most 128 Agent IDs, 4 protocols, 128 CIDRs, and 1024 ports; reject Agent IDs over 255 bytes. Cost if wrong: unusually broad Tokens must be split or limits raised through a reviewed configuration change.
Task 3: Ruling: malformed persisted Policies fail closed; Agent ID and target host must be non-empty and match, target host is limited to 255 bytes, and target port must be 1–65535. Cost if wrong: legacy malformed wildcard-like records stop authorizing until corrected.
Task 3: fix round 1/5 (2 addressed, 0 open — malformed Policy fail-open and unbounded/reparsed scope fixed; no commits, fix-only snapshot reviewed).
Task 3: complete (no commits authorized, task-only review clean after fix round 1).
Task 4: Ruling: Token mutation, audit record, and non-secret idempotency replay metadata must commit atomically. Add a storage transaction entry point that supplies ServiceTokenRepository, AuditRepository, and IdempotencyRepository; server Service code must not execute raw SQL. Cost if wrong: Task 4 touches `internal/storage/db.go` and its contract test beyond the original file list, but avoids duplicate Tokens or unrecoverable one-time-secret responses after partial failure.
Task 4: minor (deferred): dynamic status rendering performs per-Token owner/resource reads and may become an N+1 hotspot on large pages; add request-scoped caching or batch repository reads after measuring the management API workload.
Task 4: verification deferred: live MySQL `FOR UPDATE` and transaction behavior could not run without `TUNNELMESH_TEST_MYSQL_DSN`; execute the focused storage/server contract against a dedicated MySQL database before merge.
Task 4: fix round 1/5 (4 addressed, 0 open — transaction-bound resource authorization, CAS revoke lifecycle, observable denied-audit failures, and storage-error propagation fixed; no commits, fix-only snapshot reviewed).
Task 4: complete (no commits authorized, task-only review clean after fix round 1).
Task 5: Ruling: empty `security.allowed_hosts` and `security.allowed_origins` preserve the current internal/Nginx development path, while any configured list is an exact allowlist; production examples must configure both. Cost if wrong: an omitted production allowlist does not add Host/Origin restriction beyond exact WS routing and bearer-only authentication, but avoids making the default deployment unusable without an external public-origin setting.
Task 5: fix round 1/5 (2 addressed, 0 open — pre-upgrade credential authentication plus bounded first Hello, and strict Origin syntax under an empty allowlist; no commits, fix-only snapshot reviewed).
Task 5: complete (no commits authorized, task-only review clean after fix round 1).
Task 6: Ruling: define the OPEN payload once in `internal/protocol` and keep compatibility aliases only where existing public package names require them; Server derives identity solely from the pre-upgrade Client Token and calls `CredentialService.AuthorizeStream` for every OPEN before invoking an injected `relay.NodeTransport`. Cost if wrong: Task 6 may touch a small protocol file outside its original file list, but avoids divergent JSON contracts and prevents frame-supplied identity from becoming authoritative.
Task 6: Ruling: cap unique OPEN attempts at 65,536 per authenticated Client WebSocket and close the connection once exhausted; Agent relay wire IDs are monotonic and never reused within one Agent epoch, with exhaustion requiring a new epoch/reconnect. Cost if wrong: unusually long-lived high-churn Client connections must reconnect, but authenticated malformed OPEN floods and uint32 wraparound cannot create unbounded memory or cross-stream delivery.
Task 6: minor (deferred): exported `AgentRelayTransport` legacy wrapper methods should add nil receiver guards; normal runtime paths always use a non-nil transport and the issue is not load-bearing.
Task 6: fix round 1/5 (3 addressed, 0 open — production local relay wiring, stream-ID tombstones/half-close semantics, and reconnect jitter; no commits, fix-only snapshot reviewed).
Task 6: fix round 2/5 (5 addressed, 0 open — Agent CLI dispatcher lifecycle, per-connection OPEN/wire-ID limits, directional Agent stream errors; no commits, fix-only snapshot reviewed).
Task 6: fix round 3/5 (1 addressed, 0 open — process-local server-generation fencing for same-epoch reconnects; no commits, fix-only snapshot reviewed).
Task 6: fix round 4/5 (1 addressed, 0 open — restored legacy public int64 epoch wrappers with fail-closed ambiguity handling; no commits, fix-only snapshot reviewed).
Task 6: fix round 5/5 (1 addressed, 0 open — nil receiver guards for restored public wrappers; no commits, fix-only snapshot reviewed).
Task 6: complete (no commits authorized, task review clean after fix round 5).
Task 7: Ruling: relay authentication metadata identifies the calling Server node (`x-tunnelmesh-node-id`, `x-tunnelmesh-node-epoch`) separately from the target `StreamRequest.NodeID`; the stream server interceptor validates mTLS SAN, `server_node` Token binding, and current stored caller epoch before the handler reads the first target message. Cost if wrong: deployments must issue certificate SANs matching stable node IDs and attach a node-bound Token on every relay stream, but destination routing cannot be confused with caller identity.
Task 7: Ruling: use exact SAN entry matching (DNS/IP/URI string), never wildcard hostname matching, and require `tls.RequireAndVerifyClientCert` with an explicit client CA pool. Cost if wrong: wildcard certificates or CN-only legacy certificates are rejected and must be reissued with exact SANs.
Task 7: fix round 1/5 (2 addressed, 0 open — relay Serve failures now propagate through `ServeListener`; relay-enabled configuration now requires the listener address that runtime actually binds; no commits, scoped re-review PASS).
Task 7: complete (no commits authorized, task review clean after fix round 1; live external MySQL not required for relay boundary).
Task 8: fix round 1/5 (2 addressed, 0 open — rotate/revoke UI failures are caught and surfaced; client token injection docs no longer claim unsupported YAML interpolation; no commits, scoped re-review PASS).
Task 8: fix round 2/5 (2 addressed, 0 open — Agent/Docker docs now require bound `agent` service tokens instead of management/API bearer tokens; no commits, scoped review found one remaining legacy wording).
Task 8: fix round 3/5 (1 addressed, 0 open — remaining admin-token wording explicitly limited to `security.allow_legacy_connection_tokens=true` migration path with audit and v0.3.0 removal; scoped re-review PASS).
Task 8: complete (no commits authorized, task review clean after fix round 3).
Task 9: fix round 1/5 (2 addressed, 0 open — E2E network reads/writes now have bounded deadlines and cleanup treats shutdown as an assertion; scoped review found fixture collision).
Task 9: fix round 2/5 (1 addressed, 0 open — E2E SQLite DSN and owner fixture are unique per run; scoped re-review PASS).
Task 9: complete (no commits authorized; full Go/race/vet/frontend gates pass; focused E2E repeated and race repeated pass).
Final whole-branch review: fix round 1/5 (1 addressed, 0 open — Agent default heartbeat aligned from 60s to the specified 30s contract; scoped final-fix review PASS).
Plan complete: all scoped-token authentication tasks, UI/docs, integration gates, and final review complete. MySQL live contract remains deferred because `TUNNELMESH_TEST_MYSQL_DSN` is not configured.
