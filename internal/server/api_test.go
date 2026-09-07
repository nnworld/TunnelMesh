package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

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
