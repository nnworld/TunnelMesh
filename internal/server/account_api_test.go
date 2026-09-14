package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/auth"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

func TestAccountAPIRequiresAdminAndManagesLogicalDeletion(t *testing.T) {
	api, admin, user := apiTestServer(t)
	adminToken := apiToken(t, api, admin.Username, "admin-pass")
	userToken := apiToken(t, api, user.Username, "alice-pass")

	if response := apiJSON(t, api, http.MethodGet, "/api/v1/users", "", "", nil); response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d", response.Code)
	}
	if response := apiJSON(t, api, http.MethodGet, "/api/v1/users", userToken, "", nil); response.Code != http.StatusForbidden {
		t.Fatalf("non-admin status = %d", response.Code)
	}
	if response := apiJSON(t, api, http.MethodGet, "/api/v1/users?status=unknown", adminToken, "", nil); response.Code != http.StatusBadRequest {
		t.Fatalf("invalid status = %d: %s", response.Code, response.Body.String())
	}

	created := apiJSON(t, api, http.MethodPost, "/api/v1/users", adminToken, "", map[string]any{"username": "operator.one"})
	if created.Code != http.StatusCreated || created.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("create status/header = %d/%q: %s", created.Code, created.Header().Get("Cache-Control"), created.Body.String())
	}
	var creation struct {
		Data struct {
			User struct {
				ID       string `json:"id"`
				Username string `json:"username"`
				Role     string `json:"role"`
			} `json:"user"`
			TemporaryPassword string `json:"temporaryPassword"`
		} `json:"data"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &creation); err != nil {
		t.Fatal(err)
	}
	if creation.Data.User.ID == "" || creation.Data.User.Role != "user" || len(creation.Data.TemporaryPassword) < 32 {
		t.Fatalf("create response = %s", created.Body.String())
	}
	if strings.Contains(strings.ToLower(created.Body.String()), "passwordhash") {
		t.Fatalf("response leaked password hash: %s", created.Body.String())
	}

	deleted := apiJSON(t, api, http.MethodDelete, "/api/v1/users/"+creation.Data.User.ID, adminToken, "", nil)
	if deleted.Code != http.StatusOK || !strings.Contains(deleted.Body.String(), `"deletedAt"`) {
		t.Fatalf("delete response = %d: %s", deleted.Code, deleted.Body.String())
	}
	deletedList := apiJSON(t, api, http.MethodGet, "/api/v1/users?status=deleted", adminToken, "", nil)
	if deletedList.Code != http.StatusOK || !strings.Contains(deletedList.Body.String(), "operator.one") {
		t.Fatalf("deleted list = %d: %s", deletedList.Code, deletedList.Body.String())
	}
	restored := apiJSON(t, api, http.MethodPost, "/api/v1/users/"+creation.Data.User.ID+"/restore", adminToken, "", nil)
	if restored.Code != http.StatusOK || strings.Contains(restored.Body.String(), `"disabled":true`) {
		t.Fatalf("restore response = %d: %s", restored.Code, restored.Body.String())
	}

	reset := apiJSON(t, api, http.MethodPost, "/api/v1/users/"+creation.Data.User.ID+"/reset-password", adminToken, "", nil)
	if reset.Code != http.StatusOK || reset.Header().Get("Cache-Control") != "no-store" || !strings.Contains(reset.Body.String(), "temporaryPassword") {
		t.Fatalf("reset response = %d/%q: %s", reset.Code, reset.Header().Get("Cache-Control"), reset.Body.String())
	}
	protected := apiJSON(t, api, http.MethodDelete, "/api/v1/users/"+admin.ID, adminToken, "", nil)
	if protected.Code != http.StatusForbidden {
		t.Fatalf("admin delete status = %d: %s", protected.Code, protected.Body.String())
	}
}

func TestDashboardSummaryUsesPrincipalResourceScope(t *testing.T) {
	api, admin, user := apiTestServer(t)
	authService := auth.NewAuthService(api.DB)
	other, err := authService.CreateUser(context.Background(), "dashboard.other", "other-password-12", "user")
	if err != nil {
		t.Fatal(err)
	}
	disabledOwner, err := authService.CreateUser(context.Background(), "dashboard.disabled", "disabled-password-12", "user")
	if err != nil {
		t.Fatal(err)
	}
	disabledOwner.Disabled = true
	if err := api.DB.Users().Update(context.Background(), disabledOwner); err != nil {
		t.Fatal(err)
	}
	for _, agent := range []storage.Agent{
		{ID: "dashboard-agent-user", Name: "user", OwnerUserID: user.ID, Enabled: true},
		{ID: "dashboard-agent-disabled", Name: "disabled", OwnerUserID: user.ID, Enabled: false},
		{ID: "dashboard-agent-other", Name: "other", OwnerUserID: other.ID, Enabled: true},
	} {
		if err := api.DB.Agents().Create(context.Background(), agent); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := api.DB.Leases().Acquire(context.Background(), storage.AgentLease{AgentID: "dashboard-agent-user", NodeID: "node-user", TTL: time.Hour}); err != nil {
		t.Fatal(err)
	}
	if _, err := api.DB.Leases().Acquire(context.Background(), storage.AgentLease{AgentID: "dashboard-agent-disabled", NodeID: "node-disabled", TTL: time.Hour}); err != nil {
		t.Fatal(err)
	}
	for _, tunnel := range []storage.Tunnel{
		{ID: "dashboard-route-user", AgentID: "dashboard-agent-user", Protocol: "http", Domain: "app.example.com", TargetHost: "127.0.0.1", TargetPort: 80, Status: "active", Config: "{}"},
		{ID: "dashboard-route-other", AgentID: "dashboard-agent-other", Protocol: "http", Domain: "other.example.com", TargetHost: "127.0.0.1", TargetPort: 81, Status: "active", Config: "{}"},
	} {
		if err := api.DB.Tunnels().Create(context.Background(), tunnel); err != nil {
			t.Fatal(err)
		}
	}
	for _, token := range []storage.ServiceToken{
		{ID: "dashboard-token-user", OwnerUserID: user.ID, Type: storage.TokenTypeClient, Prefix: "user", TokenHash: "dashboard-hash-user", Scope: "{}"},
		{ID: "dashboard-token-other", OwnerUserID: other.ID, Type: storage.TokenTypeClient, Prefix: "other", TokenHash: "dashboard-hash-other", Scope: "{}"},
		{ID: "dashboard-token-disabled", OwnerUserID: disabledOwner.ID, Type: storage.TokenTypeClient, Prefix: "disabled", TokenHash: "dashboard-hash-disabled", Scope: "{}"},
	} {
		if err := api.DB.ServiceTokens().Create(context.Background(), token); err != nil {
			t.Fatal(err)
		}
	}

	userSummary := apiJSON(t, api, http.MethodGet, "/api/v1/dashboard/summary", apiToken(t, api, user.Username, "alice-pass"), "", nil)
	if userSummary.Code != http.StatusOK || !strings.Contains(userSummary.Body.String(), `"agentsTotal":2`) || !strings.Contains(userSummary.Body.String(), `"agentsOnline":1`) || !strings.Contains(userSummary.Body.String(), `"activeTunnels":1`) || !strings.Contains(userSummary.Body.String(), `"validServiceTokens":1`) {
		t.Fatalf("user summary = %d: %s", userSummary.Code, userSummary.Body.String())
	}
	adminSummary := apiJSON(t, api, http.MethodGet, "/api/v1/dashboard/summary", apiToken(t, api, admin.Username, "admin-pass"), "", nil)
	if adminSummary.Code != http.StatusOK || !strings.Contains(adminSummary.Body.String(), `"agentsTotal":3`) || !strings.Contains(adminSummary.Body.String(), `"agentsOnline":1`) || !strings.Contains(adminSummary.Body.String(), `"activeTunnels":2`) || !strings.Contains(adminSummary.Body.String(), `"validServiceTokens":2`) {
		t.Fatalf("admin summary = %d: %s", adminSummary.Code, adminSummary.Body.String())
	}
}

func TestDashboardSummaryToleratesActorlessAuditEvents(t *testing.T) {
	api, admin, _ := apiTestServer(t)
	// Proxy entry and other system audits have no acting user; the repository
	// persists an empty actor as NULL, so the summary scan must tolerate NULLs.
	if err := api.DB.Audits().Create(context.Background(), storage.AuditLog{
		Action:       "proxy_auth_failed",
		ResourceType: "proxy_route",
		ResourceID:   "route-1",
		Details:      `{"reason":"bad_password"}`,
	}); err != nil {
		t.Fatal(err)
	}
	summary := apiJSON(t, api, http.MethodGet, "/api/v1/dashboard/summary", apiToken(t, api, admin.Username, "admin-pass"), "", nil)
	if summary.Code != http.StatusOK {
		t.Fatalf("summary = %d: %s", summary.Code, summary.Body.String())
	}
	if !strings.Contains(summary.Body.String(), `"action":"proxy_auth_failed"`) {
		t.Fatalf("summary missing actorless event: %s", summary.Body.String())
	}
}

func TestAccountAPIChangesOwnPasswordWithoutRevokingToken(t *testing.T) {
	api, _, user := apiTestServer(t)
	token := apiToken(t, api, user.Username, "alice-pass")
	response := apiJSON(t, api, http.MethodPut, "/api/v1/auth/password", token, "", map[string]any{
		"currentPassword": "alice-pass",
		"newPassword":     "new-alice-password",
	})
	if response.Code != http.StatusOK {
		t.Fatalf("change password = %d: %s", response.Code, response.Body.String())
	}
	me := apiJSON(t, api, http.MethodGet, "/api/v1/auth/me", token, "", nil)
	if me.Code != http.StatusOK {
		t.Fatalf("existing token status = %d: %s", me.Code, me.Body.String())
	}
	if !strings.Contains(me.Body.String(), `"role":"user"`) {
		t.Fatalf("current user role was not serialized with the public field name: %s", me.Body.String())
	}
	if _, err := auth.NewAuthService(api.DB).Login(context.Background(), user.Username, "new-alice-password"); err != nil {
		t.Fatalf("new password login: %v", err)
	}
}
