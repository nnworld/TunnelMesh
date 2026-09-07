# Task 9 review package — scoped-token security integration gate

Review `internal/e2e/scoped_token_connections_test.go` and the complete
security implementation. The E2E test must prove an Agent and Client can
connect with their distinct service tokens, a UDP OPEN is denied while the
Client WebSocket remains alive, an allowed TCP stream reaches the Agent-side
service, rotation denies new streams on the old connection, and audit details
do not contain raw secrets.

Run the full Go and frontend gates from the plan. Check for flaky timing,
resource leaks, secret-bearing fixtures, and regressions outside the E2E
test. Do not modify the worktree; write findings to `task-9-review.md`.
