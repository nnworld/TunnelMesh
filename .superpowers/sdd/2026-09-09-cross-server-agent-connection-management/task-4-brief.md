### Task 4: Cluster-Wide Query and Close API

**Files:**

- Create: `internal/server/agent_connection_api.go`
- Create: `internal/server/agent_connection_api_test.go`
- Modify: `internal/server/api.go`
- Modify: `docs/api/openapi.yaml`

**Interfaces:**

- Produces:

```go
type AgentConnectionClusterView struct {
    AgentID           string    `json:"agentId"`
    InstanceID        string    `json:"instanceId"`
    ConnectionID      string    `json:"connectionId"`
    ConnectionEpoch   int64     `json:"connectionEpoch"`
    ServerNodeID      string    `json:"serverNodeId"`
    ServerNodeEpoch   int64     `json:"serverNodeEpoch"`
    ServerNodeAddress string    `json:"serverNodeAddress"`
    Healthy           bool      `json:"healthy"`
    ActiveStreams     int       `json:"activeStreams"`
    HealthScore       int64     `json:"healthScore"`
    LastHeartbeatAt   time.Time `json:"lastHeartbeatAt"`
    LeaseExpiresAt    time.Time `json:"leaseExpiresAt"`
    Local             bool      `json:"local"`
}

type AgentConnectionCloseService interface {
    Close(ctx context.Context, agentID, connectionID string, epoch int64) error
}
```

- Routes:
  - `GET /api/v1/agents/{agentId}/connections`
  - `DELETE /api/v1/agents/{agentId}/connections/{connectionId}?connectionEpoch={epoch}`

- [ ] **Step 1: Write failing API tests**

Add:

```go
func TestAgentConnectionsListMergesLocalAndRemoteLeases(t *testing.T)
func TestAgentConnectionsListEnforcesOwnership(t *testing.T)
func TestAgentConnectionsCloseLocalExactConnection(t *testing.T)
func TestAgentConnectionsCloseRemoteThroughControlService(t *testing.T)
func TestAgentConnectionsCloseRejectsStaleEpoch(t *testing.T)
func TestAgentConnectionsCloseIsIdempotent(t *testing.T)
func TestAgentConnectionsCloseWritesAudit(t *testing.T)
```

Required assertions:

```go
response.Code == http.StatusOK
len(data.Connections) == 2
data.Connections[0].Local == true
data.Connections[1].ServerNodeID == "server-b"

remoteService.closeRequests[0].ConnectionID == "conn-remote"
remoteService.closeRequests[0].ConnectionEpoch == 11

audit.Action == "agent.connection.closed_by_admin"
```

- [ ] **Step 2: Run failing tests**

Run:

```bash
go test ./internal/server -run 'TestAgentConnections' -count=1
```

Expected result: compile or 404 failure because the routes do not exist.

- [ ] **Step 3: Implement the API**

Implementation requirements:

- Resolve and authorize the Agent before reading leases.
- Query `registry.ListAgentConnections`.
- Overlay local manager state when `ServerNodeID` equals the local node ID.
- For remote views, use lease `ActiveStreams`, `HealthScore`, and expiry; mark `Healthy` as true only when the lease is active.
- Require a positive `connectionEpoch`.
- Close locally when the owner is this node.
- For remote owners, dial the lease address using the existing relay client configuration and invoke `CloseAgentConnection`.
- Return `503` when the remote node is unreachable; do not delete the lease.
- Treat a lease that disappeared before close as successful idempotency.
- Write an audit record for both successful and failed close attempts.
- Update OpenAPI schemas, parameters, responses, and authorization descriptions.

- [ ] **Step 4: Verify task**

Run:

```bash
go test ./internal/server -run 'TestAgentConnections' -count=1
go test ./internal/server -count=1
```

Expected result: all API and server tests pass.
