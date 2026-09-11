package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tunnelmesh/tunnelmesh/internal/auth"
	"github.com/tunnelmesh/tunnelmesh/internal/build"
	"github.com/tunnelmesh/tunnelmesh/internal/config"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

func TestDownloadsAPIReturnsCurrentReleaseAssets(t *testing.T) {
	test := newDownloadAPITest(t, "nnworld/TunnelMesh", "v1.2.3")
	response := test.requestAsAdmin(http.MethodGet, "/api/v1/downloads")
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	body := response.Body.String()
	for _, expected := range []string{"v1.2.3", "linux-amd64", "darwin-arm64", "windows-amd64", "SHA256SUMS", "manifest.json"} {
		if !strings.Contains(body, expected) {
			t.Fatalf("downloads response missing %q: %s", expected, body)
		}
	}
	if !strings.Contains(body, "https://github.com/nnworld/TunnelMesh/releases/download/v1.2.3/") || strings.Contains(body, "%2F") {
		t.Fatalf("downloads response must use a canonical GitHub path: %s", body)
	}
}

func TestDownloadsAPIRequiresAdmin(t *testing.T) {
	api, admin, user := apiTestServer(t)
	authn := auth.NewAuthService(api.DB)
	adminLogin, err := authn.Login(context.Background(), admin.Username, "admin-pass")
	if err != nil {
		t.Fatal(err)
	}
	userLogin, err := authn.Login(context.Background(), user.Username, "alice-pass")
	if err != nil {
		t.Fatal(err)
	}
	api.SetDownloads(config.DownloadsConfig{GitHubRepository: "nnworld/TunnelMesh"})
	handler := api.Handler()

	adminResponse := httptest.NewRecorder()
	adminRequest := httptest.NewRequest(http.MethodGet, "/api/v1/downloads", nil)
	adminRequest.Header.Set("Authorization", "Bearer "+adminLogin.Token)
	handler.ServeHTTP(adminResponse, adminRequest)
	if adminResponse.Code != http.StatusOK {
		t.Fatalf("admin status = %d, want 200", adminResponse.Code)
	}

	userResponse := httptest.NewRecorder()
	userRequest := httptest.NewRequest(http.MethodGet, "/api/v1/downloads", nil)
	userRequest.Header.Set("Authorization", "Bearer "+userLogin.Token)
	handler.ServeHTTP(userResponse, userRequest)
	if userResponse.Code != http.StatusForbidden {
		t.Fatalf("user status = %d, want 403", userResponse.Code)
	}
}

func TestServerRuntimeWiresDownloads(t *testing.T) {
	db, err := storage.OpenSQLite(context.Background(), "file:runtime-downloads?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := auth.NewAuthService(db).CreateUser(context.Background(), "admin", "admin-pass", "admin"); err != nil {
		t.Fatal(err)
	}
	originalVersion := build.Version
	build.Version = "v1.2.3"
	t.Cleanup(func() { build.Version = originalVersion })
	runtime, err := NewServerRuntime(db, AgentSessionConfig{}, RuntimeConfig{
		Downloads: config.DownloadsConfig{GitHubRepository: "example/release"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	if runtime.API == nil {
		t.Fatal("runtime API is missing")
	}
	response := requestFromRuntime(t, runtime, "/api/v1/downloads")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "example/release") {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestDownloadsAPIRejectsSubpaths(t *testing.T) {
	test := newDownloadAPITest(t, "nnworld/TunnelMesh", "v1.2.3")
	response := test.requestAsAdmin(http.MethodGet, "/api/v1/downloads/extra")
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.Code)
	}
}

type downloadAPITest struct {
	handler http.Handler
	token   string
}

func newDownloadAPITest(t *testing.T, repository, version string) *downloadAPITest {
	t.Helper()
	api, admin, _ := apiTestServer(t)
	authn := auth.NewAuthService(api.DB)
	login, err := authn.Login(context.Background(), admin.Username, "admin-pass")
	if err != nil {
		t.Fatal(err)
	}
	originalVersion := build.Version
	build.Version = version
	t.Cleanup(func() { build.Version = originalVersion })
	api.SetDownloads(config.DownloadsConfig{GitHubRepository: repository})
	return &downloadAPITest{handler: api.Handler(), token: login.Token}
}

func (t *downloadAPITest) requestAsAdmin(method, path string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, nil)
	request.Header.Set("Authorization", "Bearer "+t.token)
	response := httptest.NewRecorder()
	t.handler.ServeHTTP(response, request)
	return response
}

func requestFromRuntime(t *testing.T, runtime *ServerRuntime, path string) *httptest.ResponseRecorder {
	t.Helper()
	authn := auth.NewAuthService(runtime.DB)
	login, err := authn.Login(context.Background(), "admin", "admin-pass")
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, path, nil)
	request.Header.Set("Authorization", "Bearer "+login.Token)
	response := httptest.NewRecorder()
	runtime.API.Handler().ServeHTTP(response, request)
	return response
}
