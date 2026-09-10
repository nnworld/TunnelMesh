# Task 2 Report: Exact-Fenced Local Connection Close

## Implementation

- Added `AgentSessionManager.CloseConnection(agentID, connectionID, connectionEpoch)`.
- Reused the existing `GOAWAY` drain-and-close flow through a shared `goAwaySession` helper.
- Rejected stale or mismatched connection epochs with `ErrEpoch`.
- Did not remove the manager map entry directly; the existing read-loop cleanup and replacement fencing remain responsible for map removal.

## TDD Evidence

RED:

```bash
go test ./internal/server -run 'TestAgentSessionManagerCloseConnection' -count=1
```

Initial result:

```text
manager.CloseConnection undefined (type *AgentSessionManager has no field or method CloseConnection)
FAIL github.com/tunnelmesh/tunnelmesh/internal/server [build failed]
```

GREEN:

```bash
go test ./internal/server -run 'TestAgentSessionManagerCloseConnection' -count=1
go test ./internal/server -count=1
```

Results:

```text
ok  github.com/tunnelmesh/tunnelmesh/internal/server
```

## Files Changed

- `internal/server/session_manager.go`
- `internal/server/session_test.go`

## Self-Review

- Correct epoch closes the selected transport and sends `GOAWAY`.
- Stale epoch leaves the live transport untouched.
- Sibling connections in the same logical Agent pool remain registered and open.
- The method intentionally accepts the session protocol epoch, not the registry lease epoch.
- No commit was created.
