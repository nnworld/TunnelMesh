# Task 8 review package — Token management UI and operator docs

Review only `task-8.diff` and the current Task 8 files. The page adds an
authenticated `/tokens` route for Element Plus, supports list/create/rotate/
revoke for service tokens, and displays a newly created secret once in
component memory. Documentation covers token roles, injection, rotation,
legacy migration, Docker, configuration, and troubleshooting.

Check:

- route/auth behavior and role-aware `server_node` controls;
- redacted list/detail and one-time secret clearing on close/unmount/reload;
- API paths, idempotency headers, cursor loading, and error handling;
- no raw secrets in localStorage, logs, docs, generated assets, or tests;
- responsive/accessibility basics and Element Plus consistency;
- docs do not instruct operators to use management tokens for Agent/Client
  WebSockets.

Run `cd web && npm test -- --run && npm run build` as needed. Do not modify
the worktree; record findings in `task-8-review.md` with severity and exact
file/line references, then give PASS/FAIL.
