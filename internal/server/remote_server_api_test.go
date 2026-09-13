package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/auth"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

func remoteServerAPITest(t *testing.T) (*API, string, string) {
	t.Helper()
	api, admin, user := apiTestServer(t)
	ctx := context.Background()
	if err := api.DB.Agents().Create(ctx, storage.Agent{ID: "agent-a", Name: "agent-a", OwnerUserID: user.ID, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := api.DB.Policies().Create(ctx, storage.AgentPolicy{
		ID: "policy-a", AgentID: "agent-a", TargetHost: "10.0.0.8", TargetPort: 22, Protocol: "tcp", AllowedCIDRs: "10.0.0.0/24", AllowedPorts: "22",
	}); err != nil {
		t.Fatal(err)
	}
	credential, err := api.DB.Credentials().Create(ctx, storage.Credential{
		OwnerUserID: user.ID, Name: "key-a", Type: storage.CredentialTypeSSHPublicKey,
		PublicKey: validPublicKey, Fingerprint: "SHA256:test", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := api.DB.RemoteServers().Create(ctx, storage.RemoteServer{
		ID: "server-a", OwnerUserID: user.ID, Name: "server-a", Host: "10.0.0.8", Port: 22, CredentialID: credential.ID,
		DefaultUsername: "deploy", AgentID: "agent-a", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	return api, apiToken(t, api, user.Username, "alice-pass"), apiToken(t, api, admin.Username, "admin-pass")
}

func TestRemoteServerAPIReturnsDisplayFields(t *testing.T) {
	api, token, _ := remoteServerAPITest(t)
	ctx := context.Background()
	if _, err := api.DB.Leases().RegisterConnection(ctx, storage.AgentLease{
		AgentID: "agent-a", ConnectionID: "conn-display", ServerNodeID: "node-display",
		NodeID: "agent-node-display", InstanceID: "instance-display", ConnectionEpoch: 1, TTL: time.Minute,
	}); err != nil {
		t.Fatal(err)
	}

	response := apiJSON(t, api.Handler(), http.MethodGet, "/api/v1/remote-servers/server-a", token, "", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("get status=%d body=%s", response.Code, response.Body.String())
	}
	var payload struct {
		Data struct {
			AgentName      string `json:"agentName"`
			CredentialName string `json:"credentialName"`
			AgentOnline    *bool  `json:"agentOnline"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.AgentName != "agent-a" || payload.Data.CredentialName != "key-a" || payload.Data.AgentOnline == nil || !*payload.Data.AgentOnline {
		t.Fatalf("display fields=%+v", payload.Data)
	}
}

func TestRemoteServerAPILifecycle(t *testing.T) {
	api, token, _ := remoteServerAPITest(t)
	h := api.Handler()
	body := map[string]any{
		"name": "web-1", "host": "10.0.0.8", "port": 22, "defaultUsername": "deploy",
		"credentialId": "", "agentId": "agent-a", "enabled": true,
	}
	create := apiJSON(t, h, http.MethodPost, "/api/v1/remote-servers", token, "remote-key", body)
	if create.Code != http.StatusCreated {
		t.Fatalf("create status = %d: %s", create.Code, create.Body.String())
	}
	var created envelope
	if err := json.Unmarshal(create.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	invalid := apiJSON(t, h, http.MethodPost, "/api/v1/remote-servers", token, "invalid-remote", map[string]any{
		"name": "bad", "host": "10.0.0.8", "port": 70000, "defaultUsername": "", "agentId": "agent-a", "enabled": true,
	})
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid status = %d: %s", invalid.Code, invalid.Body.String())
	}
	update := apiJSON(t, h, http.MethodPut, "/api/v1/remote-servers/"+created.Data.ID, token, "remote-update", map[string]any{
		"name": "web-1", "host": "10.0.0.8", "port": 22, "defaultUsername": "deploy",
		"credentialId": "", "agentId": "agent-a", "enabled": false,
	})
	if update.Code != http.StatusOK {
		t.Fatalf("update status = %d: %s", update.Code, update.Body.String())
	}
	patch := apiJSON(t, h, http.MethodPatch, "/api/v1/remote-servers/"+created.Data.ID, token, "", map[string]any{"enabled": true})
	if patch.Code != http.StatusOK {
		t.Fatalf("patch status = %d: %s", patch.Code, patch.Body.String())
	}
	if r := apiJSON(t, h, http.MethodDelete, "/api/v1/remote-servers/"+created.Data.ID, token, "", nil); r.Code != http.StatusOK {
		t.Fatalf("delete status = %d", r.Code)
	}
	if r := apiJSON(t, h, http.MethodPost, "/api/v1/remote-servers/"+created.Data.ID+"/restore", token, "", nil); r.Code != http.StatusOK {
		t.Fatalf("restore status = %d: %s", r.Code, r.Body.String())
	}
}

func TestRemoteServerAPIFilterAndOwnership(t *testing.T) {
	api, token, adminToken := remoteServerAPITest(t)
	h := api.Handler()
	body := map[string]any{"name": "web-1", "host": "10.0.0.8", "port": 22, "defaultUsername": "deploy", "agentId": "agent-a", "enabled": true}
	create := apiJSON(t, h, http.MethodPost, "/api/v1/remote-servers", token, "", body)
	var created envelope
	if err := json.Unmarshal(create.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	query := url.Values{"agentId": []string{"agent-a"}}
	list := apiJSON(t, h, http.MethodGet, "/api/v1/remote-servers?"+query.Encode(), token, "", nil)
	var page pageResponse
	if err := json.Unmarshal(list.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Data.Items) != 2 {
		t.Fatalf("list items = %+v", page.Data)
	}
	detail := apiJSON(t, h, http.MethodGet, "/api/v1/remote-servers/"+created.Data.ID, adminToken, "", nil)
	if detail.Code != http.StatusOK {
		t.Fatalf("admin detail status = %d", detail.Code)
	}
}

var _ = auth.Principal{}

// TestRemoteServerAPIReportsCredentialCapability proves the list/detail views
// expose just enough information for the UI to decide whether SSH/SFTP can
// auto-authenticate, without leaking the secret itself.
func TestRemoteServerAPIReportsCredentialCapability(t *testing.T) {
	api, token, _ := remoteServerAPITest(t)
	setCredentialSecretStore(t, api)
	ctx := context.Background()
	handler := api.Handler()

	// Baseline: server-a is bound to a public-key credential with no secret.
	detail := apiJSON(t, handler, http.MethodGet, "/api/v1/remote-servers/server-a", token, "", nil)
	if detail.Code != http.StatusOK {
		t.Fatalf("detail status = %d: %s", detail.Code, detail.Body.String())
	}
	var baseline struct {
		Data struct {
			CredentialType      *string `json:"credentialType"`
			CredentialHasSecret *bool   `json:"credentialHasSecret"`
		} `json:"data"`
	}
	if err := json.Unmarshal(detail.Body.Bytes(), &baseline); err != nil {
		t.Fatal(err)
	}
	if baseline.Data.CredentialType == nil || *baseline.Data.CredentialType != "ssh_public_key" ||
		baseline.Data.CredentialHasSecret == nil || *baseline.Data.CredentialHasSecret {
		t.Fatalf("baseline capability = %+v", baseline.Data)
	}

	password, err := api.credentialService.Create(ctx, auth.Principal{UserID: credentialOwnerID(t, api), Role: "user"}, CredentialInput{
		Name: "web-password", Type: storage.CredentialTypePassword,
		Secret: &CredentialSecret{Password: "host-password"}, Enabled: true,
	})
	if err != nil {
		t.Fatalf("create password credential: %v", err)
	}
	patch := apiJSON(t, handler, http.MethodPatch, "/api/v1/remote-servers/server-a", token, "", map[string]any{"credentialId": password.ID})
	if patch.Code != http.StatusOK {
		t.Fatalf("patch status = %d: %s", patch.Code, patch.Body.String())
	}
	if strings.Contains(patch.Body.String(), "host-password") {
		t.Fatalf("patch response leaked the secret: %s", patch.Body.String())
	}
	var patched struct {
		Data struct {
			CredentialType      *string `json:"credentialType"`
			CredentialHasSecret *bool   `json:"credentialHasSecret"`
		} `json:"data"`
	}
	if err := json.Unmarshal(patch.Body.Bytes(), &patched); err != nil {
		t.Fatal(err)
	}
	if patched.Data.CredentialType == nil || *patched.Data.CredentialType != "password" ||
		patched.Data.CredentialHasSecret == nil || !*patched.Data.CredentialHasSecret {
		t.Fatalf("patched capability = %+v", patched.Data)
	}

	list := apiJSON(t, handler, http.MethodGet, "/api/v1/remote-servers", token, "", nil)
	var page pageResponse
	if err := json.Unmarshal(list.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Data.Items) != 1 {
		t.Fatalf("list items = %+v", page.Data.Items)
	}
	if page.Data.Items[0]["credentialType"] != "password" || page.Data.Items[0]["credentialHasSecret"] != true {
		t.Fatalf("list capability = %+v", page.Data.Items[0])
	}
	if strings.Contains(list.Body.String(), "host-password") {
		t.Fatal("list response leaked the secret")
	}

	// Unbinding the credential must null both capability fields. PATCH ignores
	// empty strings, so the full-replace PUT path is the way to clear it.
	unbound := apiJSON(t, handler, http.MethodPut, "/api/v1/remote-servers/server-a", token, "", map[string]any{
		"name": "server-a", "host": "10.0.0.8", "port": 22, "defaultUsername": "deploy",
		"credentialId": "", "agentId": "agent-a", "enabled": true,
	})
	if unbound.Code != http.StatusOK {
		t.Fatalf("unbind status = %d: %s", unbound.Code, unbound.Body.String())
	}
	if !strings.Contains(unbound.Body.String(), `"credentialType":null`) || !strings.Contains(unbound.Body.String(), `"credentialHasSecret":null`) {
		t.Fatalf("unbound body = %s", unbound.Body.String())
	}
}

func credentialOwnerID(t *testing.T, api *API) string {
	t.Helper()
	page, err := api.DB.Credentials().List(context.Background(), storage.CredentialFilter{Status: storage.CredentialStatusAll}, "", 10)
	if err != nil || len(page.Items) == 0 {
		t.Fatalf("credentials = %+v, err = %v", page, err)
	}
	return page.Items[0].OwnerUserID
}
