package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/auth"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

func apiTestServer(t *testing.T) (*API, storage.User, storage.User) {
	t.Helper()
	db, err := storage.Open(context.Background(), storage.DriverSQLite, "file:api-test-"+t.Name()+"?mode=memory&cache=shared", true)
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
	return NewAPI(db, a), admin, user
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
