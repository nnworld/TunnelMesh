package server

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

// The list status column rendered the enabled flag, so an agent created in the
// console showed as online before it ever connected. Connectivity must come
// from unexpired connection leases, the same fact the dashboard counts.
func TestListAgentsReportsConnectivityStatus(t *testing.T) {
	api, _, owner := apiTestServer(t)
	ctx := context.Background()
	agent := storage.Agent{ID: "agent-list-status", Name: "status-agent", OwnerUserID: owner.ID, Capabilities: `[]`, Enabled: true}
	if err := api.DB.Agents().Create(ctx, agent); err != nil {
		t.Fatal(err)
	}
	token := apiToken(t, api, owner.Username, "alice-pass")

	statusOf := func(t *testing.T) string {
		t.Helper()
		response := apiJSON(t, api.Handler(), http.MethodGet, "/api/v1/agents", token, "", nil)
		if response.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
		}
		var envelope struct {
			Data struct {
				Items []map[string]any `json:"items"`
			} `json:"data"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		for _, item := range envelope.Data.Items {
			if item["id"] == agent.ID {
				status, _ := item["status"].(string)
				return status
			}
		}
		t.Fatalf("agent %s missing from list body %s", agent.ID, response.Body.String())
		return ""
	}

	if got := statusOf(t); got != "offline" {
		t.Fatalf("never-connected agent status = %q, want offline", got)
	}

	lease := storage.AgentLease{
		AgentID: agent.ID, NodeID: "node-1", InstanceID: "instance-1", ConnectionID: "conn-1",
		ServerNodeID: "server-1", Epoch: 1, ConnectionEpoch: 1,
		ExpiresAt: time.Now().UTC().Add(time.Minute),
	}
	if _, err := api.DB.Leases().RegisterConnection(ctx, lease); err != nil {
		t.Fatal(err)
	}
	if got := statusOf(t); got != "online" {
		t.Fatalf("leased agent status = %q, want online", got)
	}

	if err := api.DB.Leases().ReleaseConnection(ctx, agent.ID, "conn-1", 1); err != nil {
		t.Fatal(err)
	}
	if got := statusOf(t); got != "offline" {
		t.Fatalf("released agent status = %q, want offline", got)
	}
}
