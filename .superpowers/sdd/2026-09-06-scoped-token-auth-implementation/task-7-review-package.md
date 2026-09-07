# Task 7 review package — Server-node relay Token plus mTLS

## Scope

Review only the Task 7 implementation delta captured in `task-7.diff` and
the current files under `task-7-after/`. The task protects inter-node relay
streams with both TLS client-certificate verification and a bound
`server_node` service Token.

## Required behavior

- Client and server stream interceptors authenticate relay calls before the
  handler receives the first target message.
- Metadata uses `x-tunnelmesh-node-id`, `x-tunnelmesh-node-epoch`, and
  `authorization: Bearer ...`.
- The caller node identity is separate from `StreamRequest.NodeID`.
- Certificate SAN matching is exact and verified against an explicit client
  CA; wildcard/CN-only fallback is rejected.
- Token type, node binding, node existence/expiry, and stored epoch are
  checked; stale epochs fail closed.
- Runtime/config wiring must keep relay mTLS separate from public TLS and
  must not serialize raw node Tokens.

## Verification requested

Run focused relay/config/server tests, race tests for relay, and inspect for
credential leakage, downgrade paths, handler-before-auth races, and changes
outside the declared Task 7 scope. Report findings with severity and exact
file/line references. Do not modify the worktree.

## Artifacts

- Implementer report: `task-7-report.md`
- Isolated before snapshot: `task-7-before/`
- Isolated after snapshot: `task-7-after/`
- Task-only diff: `task-7.diff`
