package server

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

func TestServerNodeAPIRequiresAdmin(t *testing.T) {
	api, _, user := apiTestServer(t)
	token := apiToken(t, api, "alice", "alice-pass")
	_ = user
	for _, tc := range []struct {
		method string
		path   string
		body   any
	}{
		{method: http.MethodGet, path: "/api/v1/server-nodes"},
		{method: http.MethodGet, path: "/api/v1/server-nodes/server-a"},
		{method: http.MethodPatch, path: "/api/v1/server-nodes/server-a", body: map[string]any{"name": "edge"}},
		{method: http.MethodDelete, path: "/api/v1/server-nodes/server-a"},
		{method: http.MethodPost, path: "/api/v1/server-nodes/server-a/restore"},
	} {
		response := apiJSON(t, api.Handler(), tc.method, tc.path, token, "", tc.body)
		if response.Code != http.StatusForbidden {
			t.Fatalf("%s %s status = %d, want 403: %s", tc.method, tc.path, response.Code, response.Body.String())
		}
	}
}

func TestServerNodeAPIListDetailLifecycleAndAudit(t *testing.T) {
	api, _, _ := apiTestServer(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	expires := now.Add(time.Minute)
	for _, node := range []storage.ServerNode{
		{ID: "server-a", Name: "edge-a", Address: "10.0.0.1:9443", Epoch: 7, Metadata: "{}", LastSeenAt: &now, ExpiresAt: &expires},
		{ID: "server-b", Name: "edge-b", Address: "10.0.0.2:9443", Epoch: 8, Metadata: "{}", LastSeenAt: &now, ExpiresAt: &expires},
	} {
		if err := api.DB.Nodes().Create(ctx, node); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := api.DB.Leases().RegisterConnection(ctx, storage.AgentLease{
		AgentID: "agent-a", NodeID: "server-a", ServerNodeID: "server-a", ConnectionID: "conn-a",
		ActiveStreams: 4, HealthScore: 87, TTL: time.Minute,
	}); err != nil {
		t.Fatal(err)
	}
	token := apiToken(t, api, "admin", "admin-pass")
	handler := api.Handler()

	page := apiJSON(t, handler, http.MethodGet, "/api/v1/server-nodes?limit=1", token, "", nil)
	if page.Code != http.StatusOK {
		t.Fatalf("list status = %d: %s", page.Code, page.Body.String())
	}
	var list struct {
		Data struct {
			Items      []map[string]any `json:"items"`
			NextCursor string           `json:"nextCursor"`
			HasMore    bool             `json:"hasMore"`
		} `json:"data"`
	}
	if err := json.Unmarshal(page.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Data.Items) != 1 || list.Data.Items[0]["id"] != "server-a" || !list.Data.HasMore || list.Data.NextCursor == "" {
		t.Fatalf("first page = %s", page.Body.String())
	}

	detail := apiJSON(t, handler, http.MethodGet, "/api/v1/server-nodes/server-a", token, "", nil)
	if detail.Code != http.StatusOK {
		t.Fatalf("detail status = %d: %s", detail.Code, detail.Body.String())
	}
	var view struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(detail.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]any{
		"name": "edge-a", "address": "10.0.0.1:9443", "epoch": float64(7),
		"enabled": true, "status": "online", "activeConnections": float64(1),
		"activeStreams": float64(4), "healthScore": float64(87),
	} {
		if view.Data[key] != want {
			t.Fatalf("detail %s = %v, want %v; response=%s", key, view.Data[key], want, detail.Body.String())
		}
	}

	invalid := apiJSON(t, handler, http.MethodPatch, "/api/v1/server-nodes/server-a", token, "", map[string]any{"name": " "})
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid patch status = %d: %s", invalid.Code, invalid.Body.String())
	}
	updated := apiJSON(t, handler, http.MethodPatch, "/api/v1/server-nodes/server-a", token, "", map[string]any{"name": "edge-renamed", "enabled": false})
	if updated.Code != http.StatusOK {
		t.Fatalf("patch status = %d: %s", updated.Code, updated.Body.String())
	}
	var updatedView struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(updated.Body.Bytes(), &updatedView); err != nil {
		t.Fatal(err)
	}
	if updatedView.Data["name"] != "edge-renamed" || updatedView.Data["enabled"] != false || updatedView.Data["status"] != "disabled" {
		t.Fatalf("patched node = %s", updated.Body.String())
	}

	deleted := apiJSON(t, handler, http.MethodDelete, "/api/v1/server-nodes/server-a", token, "", nil)
	if deleted.Code != http.StatusOK {
		t.Fatalf("delete status = %d: %s", deleted.Code, deleted.Body.String())
	}
	deletedNode, err := api.DB.Nodes().Get(ctx, "server-a")
	if err != nil {
		t.Fatal(err)
	}
	if deletedNode.Enabled || deletedNode.DeletedAt == nil {
		t.Fatalf("logical delete result = %+v", deletedNode)
	}

	restored := apiJSON(t, handler, http.MethodPost, "/api/v1/server-nodes/server-a/restore", token, "", nil)
	if restored.Code != http.StatusOK {
		t.Fatalf("restore status = %d: %s", restored.Code, restored.Body.String())
	}
	restoredNode, err := api.DB.Nodes().Get(ctx, "server-a")
	if err != nil {
		t.Fatal(err)
	}
	if !restoredNode.Enabled || restoredNode.DeletedAt != nil {
		t.Fatalf("restore result = %+v", restoredNode)
	}

	audits, err := api.DB.Audits().List(ctx, storage.AuditFilter{}, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	actions := make(map[string]bool)
	for _, audit := range audits.Items {
		actions[audit.Action] = true
	}
	for _, action := range []string{"server_node.updated", "server_node.deleted", "server_node.restored"} {
		if !actions[action] {
			t.Fatalf("audit action %q missing; actions=%v", action, actions)
		}
	}
}

func TestServerNodeAPIDerivesOfflineStatus(t *testing.T) {
	api, _, _ := apiTestServer(t)
	ctx := context.Background()
	expired := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	if err := api.DB.Nodes().Create(ctx, storage.ServerNode{ID: "server-offline", Name: "offline", Address: "10.0.0.3:9443", Epoch: 1, Metadata: "{}", ExpiresAt: &expired}); err != nil {
		t.Fatal(err)
	}
	token := apiToken(t, api, "admin", "admin-pass")
	response := apiJSON(t, api.Handler(), http.MethodGet, "/api/v1/server-nodes/server-offline", token, "", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	var view struct {
		Data struct {
			Status string `json:"status"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.Data.Status != "offline" {
		t.Fatalf("status = %q, want offline; response=%s", view.Data.Status, response.Body.String())
	}
}
