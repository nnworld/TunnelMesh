### Task 2: Exact-Fenced Local Connection Close

**Files:**

- Modify: `internal/server/session_manager.go`
- Test: `internal/server/session_test.go`

**Interfaces:**

- Produces:

```go
func (m *AgentSessionManager) CloseConnection(
    agentID, connectionID string, connectionEpoch int64,
) error
```

- [ ] **Step 1: Write failing tests**

Add:

```go
func TestAgentSessionManagerCloseConnectionSendsGoAway(t *testing.T)
func TestAgentSessionManagerCloseConnectionRejectsStaleEpoch(t *testing.T)
func TestAgentSessionManagerCloseConnectionDoesNotAffectSiblingConnection(t *testing.T)
```

Required assertions:

```go
err := manager.CloseConnection("agent-a", "conn-a", 7)
errors.Is(err, ErrEpoch) == false
transport.closed == true
lastFrame.Type == protocol.FrameGoAway

err = manager.CloseConnection("agent-a", "conn-a", 6)
errors.Is(err, ErrEpoch) == true
replacementTransport.closed == false
```

- [ ] **Step 2: Run failing tests**

Run:

```bash
go test ./internal/server -run 'TestAgentSessionManagerCloseConnection' -count=1
```

Expected result: compile failure because `CloseConnection` does not exist.

- [ ] **Step 3: Implement exact close**

Implementation requirements:

- Resolve the session by Agent ID and connection ID.
- Reject a mismatched `connectionEpoch` with `ErrEpoch`.
- Mark the session closing, drain queued frames, send `GOAWAY`, close the transport, and return the transport error.
- Do not remove the map entry directly; let the existing read-loop cleanup call `RemoveSession` so replacement fencing remains intact.

- [ ] **Step 4: Verify task**

Run:

```bash
go test ./internal/server -run 'TestAgentSessionManagerCloseConnection' -count=1
go test ./internal/server -count=1
```

Expected result: all tests pass.
