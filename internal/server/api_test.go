package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/auth"
	"github.com/tunnelmesh/tunnelmesh/internal/config"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

// apiTestDSNCounter keeps two fixtures built inside one test from attaching to the
// same shared-cache in-memory database, which would surface as a unique constraint
// violation on the seeded accounts.
var apiTestDSNCounter atomic.Int64

func apiTestServer(t *testing.T) (*API, storage.User, storage.User) {
	t.Helper()
	dsn := fmt.Sprintf("file:api-test-%s-%d?mode=memory&cache=shared", t.Name(), apiTestDSNCounter.Add(1))
	db, err := storage.Open(context.Background(), storage.DriverSQLite, dsn, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	a := auth.NewAuthService(db)
	admin, err := a.CreateUser(context.Background(), "admin", "admin-pass", "admin")
	if err != nil {
		t.Fatal(err)
	}
	user, err := a.CreateUser(context.Background(), "alice", "alice-pass", "user")
	if err != nil {
		t.Fatal(err)
	}
	api := NewAPI(db, a)
	installTestIdentity(t, api, a)
	return api, admin, user
}

// installTestIdentity wires the Phase A identity services the same way the server
// runtime does, so a handler test exercises the production construction path
// instead of a hand-assembled subset. The encryption key is injected through the
// environment because that is the only supported production source.
func installTestIdentity(t *testing.T, api *API, tokens *auth.AuthService) *IdentityServices {
	t.Helper()
	return installTestIdentityWith(t, api, tokens, config.DefaultAuthConfig(), IdentityRuntimeConfig{})
}

// installTestIdentityWith builds the container from an explicit configuration so a
// test can tighten the throttle budget or point the relying party at a local
// identity provider without changing the shared default.
func installTestIdentityWith(t *testing.T, api *API, tokens *auth.AuthService, authConfig config.AuthConfig, extra IdentityRuntimeConfig) *IdentityServices {
	t.Helper()
	t.Setenv("TUNNELMESH_TOKEN_ENCRYPTION_KEY", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	t.Setenv("TUNNELMESH_TOKEN_ENCRYPTION_KEY_ID", "test-key")
	secrets := auth.SecretProviderFromEnv()
	if !secrets.Available() {
		t.Fatal("test secret provider is unavailable")
	}
	extra.Auth = authConfig
	extra.Secrets = secrets
	if len(extra.AllowedRedirectBases) == 0 {
		extra.AllowedRedirectBases = []string{"https://tm.example.com"}
	}
	services := NewIdentityServices(api.DB, tokens, extra)
	if services == nil {
		t.Fatal("identity services could not be built")
	}
	api.SetIdentityServices(services)
	return services
}

func apiJSON(t *testing.T, h http.Handler, method, path, token, idem string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var b bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&b).Encode(body)
	}
	r := httptest.NewRequest(method, path, &b)
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if idem != "" {
		r.Header.Set("Idempotency-Key", idem)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func apiJSONWithHeaders(t *testing.T, h http.Handler, method, path, token string, headers map[string]string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var b bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&b).Encode(body)
	}
	r := httptest.NewRequest(method, path, &b)
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	for key, value := range headers {
		r.Header.Set(key, value)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func apiToken(t *testing.T, api *API, username, password string) string {
	t.Helper()
	result, err := auth.NewAuthService(api.DB).Login(context.Background(), username, password)
	if err != nil {
		t.Fatal(err)
	}
	return result.Token
}

func TestAPIAuthAndRBAC(t *testing.T) {
	api, _, user := apiTestServer(t)
	h := api.Handler()
	r := apiJSON(t, h, http.MethodGet, "/api/v1/agents", "", "", nil)
	if r.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d", r.Code)
	}
	login := apiJSON(t, h, http.MethodPost, "/api/v1/auth/login", "", "", map[string]string{"username": "alice", "password": "alice-pass"})
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d: %s", login.Code, login.Body.String())
	}
	var lr struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(login.Body.Bytes(), &lr); err != nil || lr.Data.Token == "" {
		t.Fatalf("login response: %s", login.Body.String())
	}
	create := apiJSON(t, h, http.MethodPost, "/api/v1/agents", lr.Data.Token, "agent-1", map[string]any{"name": "devbox", "capabilities": []string{"tcp"}})
	if create.Code != http.StatusForbidden {
		t.Fatalf("user agent create status = %d", create.Code)
	}
	_ = user
}

func TestAPICRUDConflictPaginationAndIdempotency(t *testing.T) {
	api, admin, _ := apiTestServer(t)
	authn := auth.NewAuthService(api.DB)
	lr, err := authn.Login(context.Background(), admin.Username, "admin-pass")
	if err != nil {
		t.Fatal(err)
	}
	h := api.Handler()
	body := map[string]any{"name": "devbox", "capabilities": []string{"tcp", "http"}}
	first := apiJSON(t, h, http.MethodPost, "/api/v1/agents", lr.Token, "same-key", body)
	if first.Code != http.StatusCreated {
		t.Fatalf("create agent status = %d: %s", first.Code, first.Body.String())
	}
	second := apiJSON(t, h, http.MethodPost, "/api/v1/agents", lr.Token, "same-key", body)
	if second.Code != first.Code || second.Body.String() != first.Body.String() {
		t.Fatalf("idempotent replay differs: %d/%d %s/%s", first.Code, second.Code, first.Body.String(), second.Body.String())
	}
	var created struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	route := map[string]any{"agentId": created.Data.ID, "domain": "app.example.com", "pathPrefix": "/", "protocol": "http", "targetHost": "10.0.0.1", "targetPort": 8080}
	createdRoute := apiJSON(t, h, http.MethodPost, "/api/v1/routes", lr.Token, "route-1", route)
	if createdRoute.Code != http.StatusCreated {
		t.Fatalf("route create status = %d: %s", createdRoute.Code, createdRoute.Body.String())
	}
	conflict := apiJSON(t, h, http.MethodPost, "/api/v1/routes", lr.Token, "route-2", route)
	if conflict.Code != http.StatusConflict {
		t.Fatalf("route conflict status = %d: %s", conflict.Code, conflict.Body.String())
	}
	secondAgent := apiJSON(t, h, http.MethodPost, "/api/v1/agents", lr.Token, "agent-2", map[string]any{"name": "second"})
	if secondAgent.Code != http.StatusCreated {
		t.Fatalf("second agent status = %d", secondAgent.Code)
	}
	page := apiJSON(t, h, http.MethodGet, "/api/v1/agents?limit=1", lr.Token, "", nil)
	if page.Code != http.StatusOK {
		t.Fatalf("list status = %d", page.Code)
	}
	var p struct {
		Data struct {
			Items      []map[string]any `json:"items"`
			NextCursor string           `json:"nextCursor"`
			HasMore    bool             `json:"hasMore"`
		} `json:"data"`
	}
	if err := json.Unmarshal(page.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if len(p.Data.Items) != 1 || !p.Data.HasMore || p.Data.NextCursor == "" {
		t.Fatalf("unexpected page: %s", page.Body.String())
	}
}

func TestAPIRouteAcceptsTMWildcardAndRejectsGenericWildcard(t *testing.T) {
	api, admin, _ := apiTestServer(t)
	authn := auth.NewAuthService(api.DB)
	lr, err := authn.Login(context.Background(), admin.Username, "admin-pass")
	if err != nil {
		t.Fatal(err)
	}
	createdAgent := apiJSON(t, api.Handler(), http.MethodPost, "/api/v1/agents", lr.Token, "tm-agent", map[string]any{"name": "tm-agent"})
	if createdAgent.Code != http.StatusCreated {
		t.Fatalf("create agent status=%d: %s", createdAgent.Code, createdAgent.Body.String())
	}
	var agent struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(createdAgent.Body.Bytes(), &agent); err != nil {
		t.Fatal(err)
	}
	base := map[string]any{"agentId": agent.Data.ID, "pathPrefix": "/", "protocol": "http", "targetHost": "10.0.0.1", "targetPort": 8080}
	tmRoute := cloneMap(base)
	tmRoute["domain"] = "tm-*.example.com"
	if response := apiJSON(t, api.Handler(), http.MethodPost, "/api/v1/routes", lr.Token, "tm-route", tmRoute); response.Code != http.StatusCreated {
		t.Fatalf("tm route status=%d: %s", response.Code, response.Body.String())
	}
	generic := cloneMap(base)
	generic["domain"] = "*.example.com"
	if response := apiJSON(t, api.Handler(), http.MethodPost, "/api/v1/routes", lr.Token, "generic-route", generic); response.Code != http.StatusBadRequest {
		t.Fatalf("generic wildcard status=%d: %s", response.Code, response.Body.String())
	}
}

func TestAPIAgentPolicyAcceptsWildcardTarget(t *testing.T) {
	api, admin, _ := apiTestServer(t)
	h := api.Handler()
	token := apiToken(t, api, admin.Username, "admin-pass")

	createdAgent := apiJSON(t, h, http.MethodPost, "/api/v1/agents", token, "wildcard-policy-agent", map[string]any{"name": "wildcard-policy-agent"})
	if createdAgent.Code != http.StatusCreated {
		t.Fatalf("create agent status=%d: %s", createdAgent.Code, createdAgent.Body.String())
	}
	var agent struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(createdAgent.Body.Bytes(), &agent); err != nil {
		t.Fatal(err)
	}

	body := map[string]any{
		"protocol":     "tcp",
		"targetHost":   "*",
		"targetPort":   0,
		"allowedCIDRs": []string{},
		"allowedPorts": []int{},
	}
	response := apiJSON(t, h, http.MethodPost, "/api/v1/agents/"+agent.Data.ID+"/policies", token, "wildcard-policy", body)
	if response.Code != http.StatusCreated {
		t.Fatalf("create wildcard policy status=%d: %s", response.Code, response.Body.String())
	}
	var created struct {
		Data struct {
			TargetHost string `json:"targetHost"`
			TargetPort int    `json:"targetPort"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Data.TargetHost != "*" || created.Data.TargetPort != 0 {
		t.Fatalf("created policy = %+v, want targetHost=* and targetPort=0", created.Data)
	}
}

func TestAPIAgentPolicyPatchCanSetWildcardPort(t *testing.T) {
	api, admin, _ := apiTestServer(t)
	h := api.Handler()
	token := apiToken(t, api, admin.Username, "admin-pass")

	createdAgent := apiJSON(t, h, http.MethodPost, "/api/v1/agents", token, "wildcard-patch-agent", map[string]any{"name": "wildcard-patch-agent"})
	if createdAgent.Code != http.StatusCreated {
		t.Fatalf("create agent status=%d: %s", createdAgent.Code, createdAgent.Body.String())
	}
	var agent struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(createdAgent.Body.Bytes(), &agent); err != nil {
		t.Fatal(err)
	}
	policyPath := "/api/v1/agents/" + agent.Data.ID + "/policies"
	created := apiJSON(t, h, http.MethodPost, policyPath, token, "wildcard-patch-policy", map[string]any{
		"protocol": "tcp", "targetHost": "service.internal", "targetPort": 22,
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("create policy status=%d: %s", created.Code, created.Body.String())
	}
	var createdPolicy struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &createdPolicy); err != nil {
		t.Fatal(err)
	}

	patched := apiJSON(t, h, http.MethodPatch, policyPath+"/"+createdPolicy.Data.ID, token, "", map[string]any{
		"targetHost": "*", "targetPort": 0, "allowedCIDRs": []string{}, "allowedPorts": []int{},
	})
	if patched.Code != http.StatusOK {
		t.Fatalf("patch policy status=%d: %s", patched.Code, patched.Body.String())
	}
	var updated struct {
		Data struct {
			TargetHost string `json:"targetHost"`
			TargetPort int    `json:"targetPort"`
		} `json:"data"`
	}
	if err := json.Unmarshal(patched.Body.Bytes(), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Data.TargetHost != "*" || updated.Data.TargetPort != 0 {
		t.Fatalf("updated policy = %+v, want targetHost=* and targetPort=0", updated.Data)
	}
}

func TestAPIAgentPolicyLogicalLifecycle(t *testing.T) {
	api, admin, user := apiTestServer(t)
	h := api.Handler()
	adminToken := apiToken(t, api, admin.Username, "admin-pass")
	userToken := apiToken(t, api, user.Username, "alice-pass")

	createdAgent := apiJSON(t, h, http.MethodPost, "/api/v1/agents", adminToken, "lifecycle-agent", map[string]any{"name": "lifecycle-agent"})
	if createdAgent.Code != http.StatusCreated {
		t.Fatalf("create agent status=%d: %s", createdAgent.Code, createdAgent.Body.String())
	}
	var agent struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(createdAgent.Body.Bytes(), &agent); err != nil {
		t.Fatal(err)
	}
	policyPath := "/api/v1/agents/" + agent.Data.ID + "/policies"
	created := apiJSON(t, h, http.MethodPost, policyPath, adminToken, "lifecycle-policy", map[string]any{
		"protocol": "tcp", "targetHost": "service.internal", "targetPort": 443,
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("create policy status=%d: %s", created.Code, created.Body.String())
	}
	var createdPolicy struct {
		Data struct {
			ID        string     `json:"id"`
			DeletedAt *time.Time `json:"deletedAt"`
		} `json:"data"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &createdPolicy); err != nil {
		t.Fatal(err)
	}
	policyResource := policyPath + "/" + createdPolicy.Data.ID
	if createdPolicy.Data.ID == "" || createdPolicy.Data.DeletedAt != nil {
		t.Fatalf("created policy = %+v, want active with id", createdPolicy.Data)
	}

	type policyPage struct {
		Data struct {
			Items []struct {
				ID        string     `json:"id"`
				DeletedAt *time.Time `json:"deletedAt"`
			} `json:"items"`
		} `json:"data"`
	}
	assertItems := func(response *httptest.ResponseRecorder, wantIDs ...string) []struct {
		ID        string     `json:"id"`
		DeletedAt *time.Time `json:"deletedAt"`
	} {
		t.Helper()
		if response.Code != http.StatusOK {
			t.Fatalf("list status=%d: %s", response.Code, response.Body.String())
		}
		var page policyPage
		if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		if len(page.Data.Items) != len(wantIDs) {
			t.Fatalf("items = %+v, want IDs %v", page.Data.Items, wantIDs)
		}
		for i, id := range wantIDs {
			if page.Data.Items[i].ID != id {
				t.Fatalf("items = %+v, want IDs %v", page.Data.Items, wantIDs)
			}
		}
		return page.Data.Items
	}

	if userResponse := apiJSON(t, h, http.MethodGet, policyPath+"?status=deleted", userToken, "", nil); userResponse.Code != http.StatusForbidden {
		t.Fatalf("user deleted status = %d, want 403", userResponse.Code)
	}
	if userResponse := apiJSON(t, h, http.MethodDelete, policyResource, userToken, "", nil); userResponse.Code != http.StatusForbidden {
		t.Fatalf("user delete status = %d, want 403", userResponse.Code)
	}
	if userResponse := apiJSON(t, h, http.MethodPost, policyResource+"/restore", userToken, "", nil); userResponse.Code != http.StatusForbidden {
		t.Fatalf("user restore status = %d, want 403", userResponse.Code)
	}

	assertItems(apiJSON(t, h, http.MethodGet, policyPath, adminToken, "", nil), createdPolicy.Data.ID)
	deletedResponse := apiJSON(t, h, http.MethodDelete, policyResource, adminToken, "", nil)
	if deletedResponse.Code != http.StatusOK {
		t.Fatalf("delete status=%d: %s", deletedResponse.Code, deletedResponse.Body.String())
	}
	assertItems(apiJSON(t, h, http.MethodGet, policyPath, adminToken, "", nil))
	deletedItems := assertItems(apiJSON(t, h, http.MethodGet, policyPath+"?status=deleted", adminToken, "", nil), createdPolicy.Data.ID)
	if deletedItems[0].DeletedAt == nil {
		t.Fatal("deleted policy response does not expose deletedAt")
	}
	assertItems(apiJSON(t, h, http.MethodGet, policyPath+"?status=all", adminToken, "", nil), createdPolicy.Data.ID)

	conflict := apiJSON(t, h, http.MethodPatch, policyResource, adminToken, "", map[string]any{"targetHost": "updated.internal"})
	if conflict.Code != http.StatusConflict {
		t.Fatalf("patch deleted status=%d: %s", conflict.Code, conflict.Body.String())
	}
	restored := apiJSON(t, h, http.MethodPost, policyResource+"/restore", adminToken, "", nil)
	if restored.Code != http.StatusOK {
		t.Fatalf("restore status=%d: %s", restored.Code, restored.Body.String())
	}
	assertItems(apiJSON(t, h, http.MethodGet, policyPath, adminToken, "", nil), createdPolicy.Data.ID)
	if invalid := apiJSON(t, h, http.MethodGet, policyPath+"?status=invalid", adminToken, "", nil); invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid status filter = %d, want 400", invalid.Code)
	}

	audits, err := api.DB.Audits().List(context.Background(), storage.AuditFilter{}, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	actions := make(map[string]bool)
	for _, audit := range audits.Items {
		if audit.ResourceID == createdPolicy.Data.ID {
			actions[audit.Action] = true
		}
	}
	if !actions["policy.deleted"] || !actions["policy.restored"] {
		t.Fatalf("policy lifecycle audits = %v", actions)
	}
}

func TestAPIAgentPolicyRejectsPartialWildcardTarget(t *testing.T) {
	api, admin, _ := apiTestServer(t)
	h := api.Handler()
	token := apiToken(t, api, admin.Username, "admin-pass")

	createdAgent := apiJSON(t, h, http.MethodPost, "/api/v1/agents", token, "partial-wildcard-agent", map[string]any{"name": "partial-wildcard-agent"})
	if createdAgent.Code != http.StatusCreated {
		t.Fatalf("create agent status=%d: %s", createdAgent.Code, createdAgent.Body.String())
	}
	var agent struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(createdAgent.Body.Bytes(), &agent); err != nil {
		t.Fatal(err)
	}
	policyPath := "/api/v1/agents/" + agent.Data.ID + "/policies"

	created := apiJSON(t, h, http.MethodPost, policyPath, token, "partial-wildcard-create", map[string]any{
		"protocol": "tcp", "targetHost": "service-*.internal", "targetPort": 443,
	})
	if created.Code != http.StatusBadRequest {
		t.Fatalf("create partial wildcard status=%d: %s", created.Code, created.Body.String())
	}

	valid := apiJSON(t, h, http.MethodPost, policyPath, token, "partial-wildcard-seed", map[string]any{
		"protocol": "tcp", "targetHost": "service.internal", "targetPort": 443,
	})
	if valid.Code != http.StatusCreated {
		t.Fatalf("create valid policy status=%d: %s", valid.Code, valid.Body.String())
	}
	var seed struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(valid.Body.Bytes(), &seed); err != nil {
		t.Fatal(err)
	}
	patched := apiJSON(t, h, http.MethodPatch, policyPath+"/"+seed.Data.ID, token, "", map[string]any{
		"targetHost": "*.internal",
	})
	if patched.Code != http.StatusBadRequest {
		t.Fatalf("patch partial wildcard status=%d: %s", patched.Code, patched.Body.String())
	}
}

func TestAPIAgentPolicyPatchOmittedAllowedFields(t *testing.T) {
	api, admin, _ := apiTestServer(t)
	h := api.Handler()
	token := apiToken(t, api, admin.Username, "admin-pass")

	createdAgent := apiJSON(t, h, http.MethodPost, "/api/v1/agents", token, "policy-patch-allowed-agent", map[string]any{"name": "policy-patch-allowed-agent"})
	if createdAgent.Code != http.StatusCreated {
		t.Fatalf("create agent status=%d: %s", createdAgent.Code, createdAgent.Body.String())
	}
	var agent struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(createdAgent.Body.Bytes(), &agent); err != nil {
		t.Fatal(err)
	}
	policyPath := "/api/v1/agents/" + agent.Data.ID + "/policies"
	created := apiJSON(t, h, http.MethodPost, policyPath, token, "policy-patch-allowed-seed", map[string]any{
		"protocol": "tcp", "targetHost": "service.internal", "targetPort": 443,
		"allowedCIDRs": []string{}, "allowedPorts": []int{},
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("create policy status=%d: %s", created.Code, created.Body.String())
	}
	var seed struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &seed); err != nil {
		t.Fatal(err)
	}

	patched := apiJSON(t, h, http.MethodPatch, policyPath+"/"+seed.Data.ID, token, "", map[string]any{
		"targetHost": "*",
	})
	if patched.Code != http.StatusOK {
		t.Fatalf("patch policy status=%d: %s", patched.Code, patched.Body.String())
	}
	var updated struct {
		Data struct {
			AllowedCIDRs []string `json:"allowedCIDRs"`
			AllowedPorts []int    `json:"allowedPorts"`
		} `json:"data"`
	}
	if err := json.Unmarshal(patched.Body.Bytes(), &updated); err != nil {
		t.Fatal(err)
	}
	if len(updated.Data.AllowedCIDRs) != 0 || len(updated.Data.AllowedPorts) != 0 {
		t.Fatalf("updated allowlists = %+v, want empty values preserved", updated.Data)
	}
}

func TestAPIRouteUpstreamDomainAndTLSConfig(t *testing.T) {
	api, admin, _ := apiTestServer(t)
	h := api.Handler()
	token := apiToken(t, api, admin.Username, "admin-pass")

	createdAgent := apiJSON(t, h, http.MethodPost, "/api/v1/agents", token, "domain-agent", map[string]any{"name": "domain-agent"})
	if createdAgent.Code != http.StatusCreated {
		t.Fatalf("agent create status=%d: %s", createdAgent.Code, createdAgent.Body.String())
	}
	var agent struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(createdAgent.Body.Bytes(), &agent); err != nil {
		t.Fatal(err)
	}

	created := apiJSON(t, h, http.MethodPost, "/api/v1/routes", token, "domain-route", map[string]any{
		"agentId": agent.Data.ID, "domain": "app.example.com", "pathPrefix": "/", "protocol": "http",
		"targetHost": "10.0.0.1", "targetPort": 443,
		"hostHeader": "service.internal.example.com", "targetScheme": "https",
		"tlsServerName": "service.internal.example.com",
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("route create status=%d: %s", created.Code, created.Body.String())
	}
	var createdRoute struct {
		Data struct {
			ID            string `json:"id"`
			HostHeader    string `json:"hostHeader"`
			TargetScheme  string `json:"targetScheme"`
			TLSServerName string `json:"tlsServerName"`
		} `json:"data"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &createdRoute); err != nil {
		t.Fatal(err)
	}
	if createdRoute.Data.ID == "" || createdRoute.Data.HostHeader != "service.internal.example.com" ||
		createdRoute.Data.TargetScheme != "https" || createdRoute.Data.TLSServerName != "service.internal.example.com" {
		t.Fatalf("created route data = %+v", createdRoute.Data)
	}

	stored, err := api.service.GetTunnel(context.Background(), createdRoute.Data.ID)
	if err != nil {
		t.Fatal(err)
	}
	var storedConfig struct {
		HostHeader    string `json:"hostHeader"`
		TargetScheme  string `json:"targetScheme"`
		TLSServerName string `json:"tlsServerName"`
	}
	if err := json.Unmarshal([]byte(stored.Config), &storedConfig); err != nil {
		t.Fatalf("stored config %q: %v", stored.Config, err)
	}
	if storedConfig != (struct {
		HostHeader    string `json:"hostHeader"`
		TargetScheme  string `json:"targetScheme"`
		TLSServerName string `json:"tlsServerName"`
	}{"service.internal.example.com", "https", "service.internal.example.com"}) {
		t.Fatalf("stored config = %+v", storedConfig)
	}

	patched := apiJSON(t, h, http.MethodPatch, "/api/v1/routes/"+createdRoute.Data.ID, token, "", map[string]any{
		"targetPort": 8443,
	})
	if patched.Code != http.StatusOK {
		t.Fatalf("route patch status=%d: %s", patched.Code, patched.Body.String())
	}
	var patchedRoute struct {
		Data struct {
			TargetPort    int    `json:"targetPort"`
			HostHeader    string `json:"hostHeader"`
			TargetScheme  string `json:"targetScheme"`
			TLSServerName string `json:"tlsServerName"`
		} `json:"data"`
	}
	if err := json.Unmarshal(patched.Body.Bytes(), &patchedRoute); err != nil {
		t.Fatal(err)
	}
	if patchedRoute.Data.TargetPort != 8443 || patchedRoute.Data.HostHeader != "service.internal.example.com" ||
		patchedRoute.Data.TargetScheme != "https" || patchedRoute.Data.TLSServerName != "service.internal.example.com" {
		t.Fatalf("patched route = %+v", patchedRoute.Data)
	}

	invalidScheme := apiJSON(t, h, http.MethodPatch, "/api/v1/routes/"+createdRoute.Data.ID, token, "", map[string]any{"targetScheme": "ftp"})
	if invalidScheme.Code != http.StatusBadRequest {
		t.Fatalf("invalid targetScheme status=%d: %s", invalidScheme.Code, invalidScheme.Body.String())
	}
	httpWithSNI := apiJSON(t, h, http.MethodPatch, "/api/v1/routes/"+createdRoute.Data.ID, token, "", map[string]any{
		"targetScheme": "http", "tlsServerName": "service.internal.example.com",
	})
	if httpWithSNI.Code != http.StatusBadRequest {
		t.Fatalf("http with SNI status=%d: %s", httpWithSNI.Code, httpWithSNI.Body.String())
	}
}

func TestAPIRouteUpdateAndAuditView(t *testing.T) {
	api, admin, _ := apiTestServer(t)
	h := api.Handler()
	token := apiToken(t, api, admin.Username, "admin-pass")

	firstAgent := apiJSON(t, h, http.MethodPost, "/api/v1/agents", token, "route-agent-1", map[string]any{"name": "route-agent-1"})
	secondAgent := apiJSON(t, h, http.MethodPost, "/api/v1/agents", token, "route-agent-2", map[string]any{"name": "route-agent-2"})
	if firstAgent.Code != http.StatusCreated || secondAgent.Code != http.StatusCreated {
		t.Fatalf("agent creation failed: %d %s; %d %s", firstAgent.Code, firstAgent.Body.String(), secondAgent.Code, secondAgent.Body.String())
	}
	var agentOne, agentTwo struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	_ = json.Unmarshal(firstAgent.Body.Bytes(), &agentOne)
	_ = json.Unmarshal(secondAgent.Body.Bytes(), &agentTwo)

	created := apiJSON(t, h, http.MethodPost, "/api/v1/routes", token, "route-update-create", map[string]any{
		"agentId": agentOne.Data.ID, "domain": "before.example.com", "pathPrefix": "/", "protocol": "http",
		"targetHost": "10.0.0.1", "targetPort": 8080,
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("route create status = %d: %s", created.Code, created.Body.String())
	}
	var createdRoute struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &createdRoute); err != nil {
		t.Fatal(err)
	}

	updated := apiJSON(t, h, http.MethodPatch, "/api/v1/routes/"+createdRoute.Data.ID, token, "", map[string]any{
		"agentId": agentTwo.Data.ID, "domain": "after.example.com", "pathPrefix": "/git", "protocol": "websocket",
		"targetHost": "127.0.0.1", "targetPort": 3001, "status": "disabled",
	})
	if updated.Code != http.StatusOK {
		t.Fatalf("route update status = %d: %s", updated.Code, updated.Body.String())
	}
	var route struct {
		Data struct {
			AgentID    string `json:"agentId"`
			Domain     string `json:"domain"`
			PathPrefix string `json:"pathPrefix"`
			Protocol   string `json:"protocol"`
			TargetHost string `json:"targetHost"`
			TargetPort int    `json:"targetPort"`
			Status     string `json:"status"`
		} `json:"data"`
	}
	if err := json.Unmarshal(updated.Body.Bytes(), &route); err != nil {
		t.Fatal(err)
	}
	if route.Data.AgentID != agentTwo.Data.ID || route.Data.Domain != "after.example.com" || route.Data.PathPrefix != "/git" ||
		route.Data.Protocol != "websocket" || route.Data.TargetHost != "127.0.0.1" || route.Data.TargetPort != 3001 || route.Data.Status != "disabled" {
		t.Fatalf("updated route = %+v", route.Data)
	}

	invalid := apiJSON(t, h, http.MethodPatch, "/api/v1/routes/"+createdRoute.Data.ID, token, "", map[string]any{"targetPort": 0})
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid route update status = %d: %s", invalid.Code, invalid.Body.String())
	}

	auditResponse := apiJSON(t, h, http.MethodGet, "/api/v1/audit-logs", token, "", nil)
	if auditResponse.Code != http.StatusOK {
		t.Fatalf("audit list status = %d: %s", auditResponse.Code, auditResponse.Body.String())
	}
	var auditPage struct {
		Data struct {
			Items []struct {
				ID           string          `json:"id"`
				ActorUserID  string          `json:"actorUserId"`
				Action       string          `json:"action"`
				ResourceType string          `json:"resourceType"`
				ResourceID   string          `json:"resourceId"`
				Details      json.RawMessage `json:"details"`
				CreatedAt    string          `json:"createdAt"`
			} `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(auditResponse.Body.Bytes(), &auditPage); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range auditPage.Data.Items {
		if event.Action == "route.updated" && event.ResourceID == createdRoute.Data.ID {
			found = true
			if event.ID == "" || event.ActorUserID != admin.ID || event.ResourceType != "tunnel" || event.CreatedAt == "" || len(event.Details) == 0 {
				t.Fatalf("route update audit is incomplete: %+v", event)
			}
			var details map[string]any
			if err := json.Unmarshal(event.Details, &details); err != nil {
				t.Fatalf("route update audit details are invalid: %v", err)
			}
			if details["agentId"] != agentTwo.Data.ID || details["domain"] != "after.example.com" || details["targetPort"] != float64(3001) {
				t.Fatalf("route update audit details = %+v", details)
			}
		}
	}
	if !found {
		t.Fatalf("route.updated audit missing from %s", auditResponse.Body.String())
	}
}

func TestAuditAPIFiltersAndValidatesTimeRange(t *testing.T) {
	api, admin, _ := apiTestServer(t)
	h := api.Handler()
	token := apiToken(t, api, admin.Username, "admin-pass")
	base := time.Date(2026, 9, 9, 13, 0, 0, 0, time.UTC)
	records := []storage.AuditLog{
		{ID: "api-audit-old", ActorUserID: admin.ID, Action: "route.updated", ResourceType: "tunnel", ResourceID: "route-1", CreatedAt: base},
		{ID: "api-audit-new", ActorUserID: admin.ID, Action: "route.updated", ResourceType: "tunnel", ResourceID: "route-1", CreatedAt: base.Add(time.Minute)},
		{ID: "api-audit-noise", ActorUserID: "actor-other", Action: "agent.created", ResourceType: "agent", ResourceID: "agent-1", CreatedAt: base.Add(2 * time.Minute)},
	}
	for _, record := range records {
		if err := api.DB.Audits().Create(context.Background(), record); err != nil {
			t.Fatalf("create audit %s: %v", record.ID, err)
		}
	}

	query := url.Values{
		"actorUserId":  []string{admin.ID},
		"action":       []string{"route.updated"},
		"resourceType": []string{"tunnel"},
		"resourceId":   []string{"route-1"},
		"createdFrom":  []string{base.Add(30 * time.Second).Format(time.RFC3339Nano)},
		"createdTo":    []string{base.Add(2 * time.Minute).Format(time.RFC3339Nano)},
		"limit":        []string{"10"},
	}
	response := apiJSON(t, h, http.MethodGet, "/api/v1/audit-logs?"+query.Encode(), token, "", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("filtered audit status = %d: %s", response.Code, response.Body.String())
	}
	var page struct {
		Data struct {
			Items []struct {
				ID string `json:"id"`
			} `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Data.Items) != 1 || page.Data.Items[0].ID != "api-audit-new" {
		t.Fatalf("filtered audit items = %+v", page.Data.Items)
	}

	invalidTime := apiJSON(t, h, http.MethodGet, "/api/v1/audit-logs?createdFrom=not-a-time", token, "", nil)
	if invalidTime.Code != http.StatusBadRequest {
		t.Fatalf("invalid createdFrom status = %d: %s", invalidTime.Code, invalidTime.Body.String())
	}
	reversed := apiJSON(t, h, http.MethodGet, "/api/v1/audit-logs?createdFrom="+base.Add(time.Hour).Format(time.RFC3339Nano)+"&createdTo="+base.Format(time.RFC3339Nano), token, "", nil)
	if reversed.Code != http.StatusBadRequest {
		t.Fatalf("reversed time range status = %d: %s", reversed.Code, reversed.Body.String())
	}
}

func cloneMap(input map[string]any) map[string]any {
	output := make(map[string]any, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}
