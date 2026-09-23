package server

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/auth"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

func TestMetadataAPI(t *testing.T) {
	api, admin, owner := apiTestServer(t)
	ctx := context.Background()
	other, err := auth.NewAuthService(api.DB).CreateUser(ctx, "bob", "bob-pass", "user")
	if err != nil {
		t.Fatal(err)
	}
	agent := storage.Agent{ID: "agent-metadata", Name: "metadata-agent", OwnerUserID: owner.ID, Capabilities: `[]`, Enabled: true}
	if err := api.DB.Agents().Create(ctx, agent); err != nil {
		t.Fatal(err)
	}

	ownerToken := apiToken(t, api, owner.Username, "alice-pass")
	adminToken := apiToken(t, api, admin.Username, "admin-pass")
	otherToken := apiToken(t, api, other.Username, "bob-pass")

	t.Run("unauthenticated", func(t *testing.T) {
		response := apiJSON(t, api.Handler(), http.MethodGet, "/api/v1/agents/agent-metadata/metadata", "", "", nil)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
		}
	})

	// An agent that exists but never reported has an empty metadata collection,
	// not a missing resource: the console renders its designed empty state
	// instead of an error banner with a retry button.
	t.Run("missing metadata returns an empty view", func(t *testing.T) {
		response := apiJSON(t, api.Handler(), http.MethodGet, "/api/v1/agents/agent-metadata/metadata?includeStale=true", ownerToken, "", nil)
		if response.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
		}
		var envelope struct {
			Data struct {
				AgentID   string           `json:"agentId"`
				Items     []map[string]any `json:"items"`
				Instances []map[string]any `json:"instances"`
				Stale     bool             `json:"stale"`
			} `json:"data"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		if envelope.Data.AgentID != agent.ID {
			t.Fatalf("agentId = %q, want %q", envelope.Data.AgentID, agent.ID)
		}
		if len(envelope.Data.Items) != 0 || len(envelope.Data.Instances) != 0 || envelope.Data.Stale {
			t.Fatalf("unexpected empty view: %s", response.Body.String())
		}
	})

	t.Run("missing agent is not found", func(t *testing.T) {
		response := apiJSON(t, api.Handler(), http.MethodGet, "/api/v1/agents/no-such-agent/metadata", ownerToken, "", nil)
		if response.Code != http.StatusNotFound {
			t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
		}
	})

	if _, err := NewAgentMetadataService(api.DB.Metadata()).Upsert(ctx, AgentMetadataInput{
		AgentID:    agent.ID,
		InstanceID: "instance-a",
		NodeID:     "node-1",
		Epoch:      12,
		Revision:   4,
		ReportedAt: time.Date(2026, 9, 6, 4, 0, 0, 0, time.UTC),
		Items: []MetadataItem{
			{Name: "device_id", Source: "file", Value: "dev-123"},
			{Name: "region", Source: "env", Value: "cn-east-1"},
			{Name: "password", Source: "env", Value: "do-not-leak"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	manager := NewAgentSessionManager(AgentSessionConfig{})
	if _, err := manager.Register(ctx, AgentRegistration{AgentID: agent.ID, NodeID: "node-1", Epoch: 12, InstanceID: "instance-a", ConnectionID: "conn-a", ConnectionEpoch: 12}, newFakeTransport()); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Register(ctx, AgentRegistration{AgentID: agent.ID, NodeID: "node-1", Epoch: 12, InstanceID: "instance-b", ConnectionID: "conn-b", ConnectionEpoch: 12}, newFakeTransport()); err != nil {
		t.Fatal(err)
	}
	api.SetAgentConnections(manager, nil)

	if _, err := NewAgentMetadataService(api.DB.Metadata()).Upsert(ctx, AgentMetadataInput{
		AgentID:    agent.ID,
		InstanceID: "instance-b",
		NodeID:     "node-2",
		Epoch:      12,
		Revision:   5,
		ReportedAt: time.Date(2026, 9, 6, 4, 1, 0, 0, time.UTC),
		Items: []MetadataItem{
			{Name: "region", Source: "env", Value: "cn-north-1"},
			{Name: "api_token", Source: "env", Value: "do-not-leak"},
		},
	}); err != nil {
		t.Fatal(err)
	}

	checkResponse := func(t *testing.T, token string) map[string]any {
		t.Helper()
		response := apiJSON(t, api.Handler(), http.MethodGet, "/api/v1/agents/agent-metadata/metadata", token, "", nil)
		if response.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
		}
		var envelope struct {
			Code int            `json:"code"`
			Msg  string         `json:"msg"`
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		if envelope.Code != http.StatusOK || envelope.Msg != http.StatusText(http.StatusOK) {
			t.Fatalf("envelope = %+v", envelope)
		}
		return envelope.Data
	}

	t.Run("owner gets bounded redacted view", func(t *testing.T) {
		data := checkResponse(t, ownerToken)
		for _, key := range []string{"agentId", "nodeId", "epoch", "revision", "stale", "reportedAt", "updatedAt", "items"} {
			if _, ok := data[key]; !ok {
				t.Fatalf("missing response field %q: %+v", key, data)
			}
		}
		connections, ok := data["connections"].([]any)
		if !ok || len(connections) != 2 {
			t.Fatalf("connections = %#v, want two live connections", data["connections"])
		}
		firstConnection, ok := connections[0].(map[string]any)
		if !ok || firstConnection["instanceId"] != "instance-a" || firstConnection["connectionId"] != "conn-a" {
			t.Fatalf("first connection = %#v", connections[0])
		}
		if data["instanceId"] != "instance-b" {
			t.Fatalf("instanceId = %#v, want instance-b", data["instanceId"])
		}
		instances, ok := data["instances"].([]any)
		if !ok || len(instances) != 2 {
			t.Fatalf("instances = %#v, want two Agent instances", data["instances"])
		}
		firstInstance, ok := instances[0].(map[string]any)
		if !ok || firstInstance["instanceId"] != "instance-a" {
			t.Fatalf("first instance = %#v", instances[0])
		}
		if firstInstance["connectionCount"] != 1.0 {
			t.Fatalf("instance-a connection count = %#v", firstInstance["connectionCount"])
		}
		for _, key := range []string{"metadata", "lastSeenAt", "expiresAt"} {
			if _, ok := data[key]; ok {
				t.Fatalf("internal field %q leaked: %+v", key, data)
			}
		}
		items, ok := data["items"].([]any)
		if !ok || len(items) != 2 {
			t.Fatalf("items = %#v", data["items"])
		}
		ordinary, ok := items[0].(map[string]any)
		if !ok || ordinary["redacted"] != false {
			t.Fatalf("ordinary item must include redacted=false: %#v", items[0])
		}
		redacted, ok := items[1].(map[string]any)
		if !ok || redacted["value"] != nil || redacted["redacted"] != true {
			t.Fatalf("sensitive item = %#v", items[2])
		}
	})

	t.Run("admin can read", func(t *testing.T) {
		data := checkResponse(t, adminToken)
		if data["agentId"] != agent.ID {
			t.Fatalf("agentId = %#v", data["agentId"])
		}
	})

	t.Run("unrelated user is forbidden", func(t *testing.T) {
		response := apiJSON(t, api.Handler(), http.MethodGet, "/api/v1/agents/agent-metadata/metadata", otherToken, "", nil)
		if response.Code != http.StatusForbidden {
			t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
		}
	})

	t.Run("metadata writes are not exposed", func(t *testing.T) {
		response := apiJSON(t, api.Handler(), http.MethodPost, "/api/v1/agents/agent-metadata/metadata", ownerToken, "", map[string]any{"items": []any{}})
		if response.Code != http.StatusMethodNotAllowed {
			t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
		}
	})

	t.Run("read is audited", func(t *testing.T) {
		page, err := api.DB.Audits().List(ctx, storage.AuditFilter{}, "", 100)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, audit := range page.Items {
			if audit.Action == "agent.metadata.read" && audit.ResourceID == agent.ID {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("metadata read audit not found: %+v", page.Items)
		}
	})

	t.Run("stale instance does not hide healthy instance", func(t *testing.T) {
		expired := time.Now().UTC().Add(-time.Minute)
		if err := api.DB.Metadata().Upsert(ctx, storage.AgentRuntimeMetadata{
			AgentID:    agent.ID,
			InstanceID: "instance-stale",
			NodeID:     "node-1",
			Epoch:      13,
			Revision:   1,
			Metadata:   `{"items":[{"name":"region","source":"env","value":"cn-east-1"}]}`,
			ReportedAt: time.Now().UTC().Add(-2 * time.Hour),
			ExpiresAt:  &expired,
		}); err != nil {
			t.Fatal(err)
		}
		response := apiJSON(t, api.Handler(), http.MethodGet, "/api/v1/agents/agent-metadata/metadata", ownerToken, "", nil)
		if response.Code != http.StatusOK {
			t.Fatalf("default status = %d, body = %s", response.Code, response.Body.String())
		}
		var envelope struct {
			Data struct {
				InstanceID string `json:"instanceId"`
				Stale      bool   `json:"stale"`
			} `json:"data"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		if envelope.Data.InstanceID != "instance-b" || envelope.Data.Stale {
			t.Fatalf("healthy fallback = %+v", envelope.Data)
		}
		response = apiJSON(t, api.Handler(), http.MethodGet, "/api/v1/agents/agent-metadata/metadata?includeStale=true", ownerToken, "", nil)
		if response.Code != http.StatusOK {
			t.Fatalf("include stale status = %d, body = %s", response.Code, response.Body.String())
		}
		if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		if envelope.Data.InstanceID != "instance-b" || envelope.Data.Stale {
			t.Fatalf("include stale response = %+v, body = %s", envelope.Data, response.Body.String())
		}
	})
}
