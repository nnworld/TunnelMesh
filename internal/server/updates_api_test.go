package server

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/tunnelmesh/tunnelmesh/internal/build"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

const (
	updatesArchiveName = "tunnelmesh-v1.4.0-linux-amd64.tar.gz"
	updatesArchiveBody = "0123456789abcdefghijklmnopqrstuvwxyz"
)

// newUpdatesFixture publishes a release into a directory and enables the
// distribution endpoints on the fixture's API.
func newUpdatesFixture(t *testing.T) (*clientScopedFixture, string) {
	t.Helper()
	fixture := newClientScopedFixture(t)
	dir := t.TempDir()
	for name, body := range map[string]string{
		"SHA256SUMS":       strings.Repeat("a", 64) + "  " + updatesArchiveName + "\n",
		"manifest.json":    `{"version":"v1.4.0","schemaVersion":3}`,
		updatesArchiveName: updatesArchiveBody,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	fixture.api.SetUpdates(dir)
	return fixture, dir
}

func TestUpdatesLatestAnswersBothTokenNamespaces(t *testing.T) {
	fixture, _ := newUpdatesFixture(t)
	for name, tc := range map[string]struct{ path, token string }{
		"agent":  {"/api/v1/agent/updates/latest", fixture.agentToken},
		"client": {"/api/v1/client/updates/latest", fixture.ownerToken},
	} {
		t.Run(name, func(t *testing.T) {
			response := apiJSON(t, fixture.api.Handler(), http.MethodGet, tc.path, tc.token, "", nil)
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, body %s", response.Code, response.Body.String())
			}
			var envelope struct {
				Data map[string]string `json:"data"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.Data["version"] != "v1.4.0" || envelope.Data["server_version"] != build.Current().Version {
				t.Fatalf("data = %v, want the published version and the Server's own", envelope.Data)
			}
			if len(envelope.Data) != 2 {
				t.Fatalf("data leaked fields: %v", envelope.Data)
			}
		})
	}
}

// The two namespaces trust different credentials, and a tunnel token must not be
// usable on the other side's endpoints.
func TestUpdatesRejectTheWrongKindOfToken(t *testing.T) {
	fixture, _ := newUpdatesFixture(t)
	cases := map[string]struct{ path, token string }{
		"client token on the agent path":  {"/api/v1/agent/updates/latest", fixture.ownerToken},
		"agent token on the client path":  {"/api/v1/client/updates/latest", fixture.agentToken},
		"console token on the agent path": {"/api/v1/agent/updates/latest", fixture.consoleToken},
		"no token":                        {"/api/v1/client/updates/latest", ""},
		"a file, wrong token":             {"/api/v1/agent/updates/files/SHA256SUMS", fixture.ownerToken},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			response := apiJSON(t, fixture.api.Handler(), http.MethodGet, tc.path, tc.token, "", nil)
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", response.Code)
			}
		})
	}
}

func TestUpdatesServeTheReleaseFiles(t *testing.T) {
	fixture, _ := newUpdatesFixture(t)
	handler := fixture.api.Handler()
	for _, tc := range []struct{ path, token string }{
		{"/api/v1/agent/updates/files/" + updatesArchiveName, fixture.agentToken},
		{"/api/v1/client/updates/files/" + updatesArchiveName, fixture.ownerToken},
	} {
		response := apiJSON(t, handler, http.MethodGet, tc.path, tc.token, "", nil)
		if response.Code != http.StatusOK || response.Body.String() != updatesArchiveBody {
			t.Fatalf("%s: status %d, body %q", tc.path, response.Code, response.Body.String())
		}
		if got := response.Header().Get("Content-Type"); got != "application/octet-stream" {
			t.Fatalf("Content-Type = %q", got)
		}
	}
	sums := apiJSON(t, handler, http.MethodGet, "/api/v1/client/updates/files/SHA256SUMS", fixture.ownerToken, "", nil)
	if sums.Code != http.StatusOK || !strings.Contains(sums.Body.String(), updatesArchiveName) {
		t.Fatalf("SHA256SUMS: status %d, body %q", sums.Code, sums.Body.String())
	}
}

// A download that is interrupted resumes with a Range request instead of
// starting over.
func TestUpdatesSupportRangeRequests(t *testing.T) {
	fixture, _ := newUpdatesFixture(t)
	response := apiJSONWithHeaders(t, fixture.api.Handler(), http.MethodGet, "/api/v1/client/updates/files/"+updatesArchiveName,
		fixture.ownerToken, map[string]string{"Range": "bytes=10-19"}, nil)
	if response.Code != http.StatusPartialContent || response.Body.String() != updatesArchiveBody[10:20] {
		t.Fatalf("status %d, body %q, want 206 with bytes 10-19", response.Code, response.Body.String())
	}
}

// The file name comes from the request path, which the caller controls, so only
// a short allowlist of release file names is ever opened.
func TestUpdatesRefuseNamesOutsideTheReleaseLayout(t *testing.T) {
	fixture, dir := newUpdatesFixture(t)
	// A real file just outside the directory, and one inside that is not a release file.
	outside := filepath.Join(filepath.Dir(dir), "secret.txt")
	if err := os.WriteFile(outside, []byte("TOP SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("private notes"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"..%2Fsecret.txt", "%2e%2e%2fsecret.txt", "../secret.txt", "notes.txt", "..",
		"tunnelmesh-v1.4.0-linux-amd64.tar.gz%2F..%2F..%2Fsecret.txt", "tunnelmesh-v1.4.0-linux-mips.tar.gz",
		"TUNNELMESH-v1.4.0-linux-amd64.tar.gz", "sha256sums", "manifest.json.bak", "",
	} {
		response := apiJSON(t, fixture.api.Handler(), http.MethodGet, "/api/v1/client/updates/files/"+name, fixture.ownerToken, "", nil)
		if response.Code == http.StatusOK {
			t.Errorf("name %q was served: %q", name, response.Body.String())
		}
		if strings.Contains(response.Body.String(), "TOP SECRET") || strings.Contains(response.Body.String(), "private notes") {
			t.Errorf("name %q leaked file content", name)
		}
	}
}

func TestUpdatesRefuseASymlinkThatLeavesTheDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	fixture, dir := newUpdatesFixture(t)
	outside := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(outside, []byte("OUTSIDE"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "tunnelmesh-v1.5.0-linux-amd64.tar.gz")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	response := apiJSON(t, fixture.api.Handler(), http.MethodGet, "/api/v1/client/updates/files/tunnelmesh-v1.5.0-linux-amd64.tar.gz", fixture.ownerToken, "", nil)
	if response.Code == http.StatusOK || strings.Contains(response.Body.String(), "OUTSIDE") {
		t.Fatalf("a symlink out of the directory was followed: status %d", response.Code)
	}
}

func TestUpdatesReportMissingFilesAndAMissingManifest(t *testing.T) {
	fixture, dir := newUpdatesFixture(t)
	missing := apiJSON(t, fixture.api.Handler(), http.MethodGet, "/api/v1/client/updates/files/tunnelmesh-v9.9.9-linux-amd64.tar.gz", fixture.ownerToken, "", nil)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing file status = %d, want 404", missing.Code)
	}
	if err := os.Remove(filepath.Join(dir, "manifest.json")); err != nil {
		t.Fatal(err)
	}
	latest := apiJSON(t, fixture.api.Handler(), http.MethodGet, "/api/v1/client/updates/latest", fixture.ownerToken, "", nil)
	if latest.Code != http.StatusNotFound {
		t.Fatalf("latest without a manifest status = %d, want 404", latest.Code)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(`{"version":"nightly"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	bad := apiJSON(t, fixture.api.Handler(), http.MethodGet, "/api/v1/client/updates/latest", fixture.ownerToken, "", nil)
	if bad.Code != http.StatusInternalServerError {
		t.Fatalf("latest with an invalid manifest status = %d, want 500", bad.Code)
	}
}

func TestUpdatesAreOffUntilADirectoryIsConfigured(t *testing.T) {
	fixture := newClientScopedFixture(t)
	for _, path := range []string{"/api/v1/client/updates/latest", "/api/v1/client/updates/files/SHA256SUMS"} {
		response := apiJSON(t, fixture.api.Handler(), http.MethodGet, path, fixture.ownerToken, "", nil)
		if response.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d, want 404 when distribution is not configured", path, response.Code)
		}
	}
	fixture.api.SetUpdates("")
	if response := apiJSON(t, fixture.api.Handler(), http.MethodGet, "/api/v1/client/updates/latest", fixture.ownerToken, "", nil); response.Code != http.StatusNotFound {
		t.Fatalf("an empty directory must keep distribution off, status = %d", response.Code)
	}
}

func TestUpdatesOnlyAnswerGETAndHEAD(t *testing.T) {
	fixture, _ := newUpdatesFixture(t)
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		response := apiJSON(t, fixture.api.Handler(), method, "/api/v1/client/updates/latest", fixture.ownerToken, "", nil)
		if response.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s status = %d, want 405", method, response.Code)
		}
	}
	if response := apiJSON(t, fixture.api.Handler(), http.MethodHead, "/api/v1/client/updates/files/SHA256SUMS", fixture.ownerToken, "", nil); response.Code != http.StatusOK {
		t.Errorf("HEAD status = %d, want 200", response.Code)
	}
}

// Downloads are bounded so a fleet restarting together cannot exhaust the Server.
func TestUpdateServiceBoundsConcurrentDownloads(t *testing.T) {
	service := NewUpdateService(t.TempDir())
	service.maxConcurrent, service.maxPerToken = 3, 2
	var releases []func()
	grab := func(token string) bool {
		release, ok := service.acquire(token)
		if ok {
			releases = append(releases, release)
		}
		return ok
	}
	if !grab("a") || !grab("a") {
		t.Fatal("the first two downloads of one token must be allowed")
	}
	if grab("a") {
		t.Fatal("a third concurrent download for one token was allowed")
	}
	if !grab("b") {
		t.Fatal("another token must not be starved by the first")
	}
	if grab("c") {
		t.Fatal("the global limit of 3 was exceeded")
	}
	for _, release := range releases {
		release()
	}
	if !grab("c") {
		t.Fatal("capacity was not returned after the downloads finished")
	}
}

func TestUpdatesAnswer503WhenTheDownloadLimitIsReached(t *testing.T) {
	fixture, _ := newUpdatesFixture(t)
	service := fixture.api.updates
	service.maxPerToken = 1
	release, ok := service.acquire(fixtureTokenID(t, fixture))
	if !ok {
		t.Fatal("setup: could not take the only slot")
	}
	defer release()
	response := apiJSON(t, fixture.api.Handler(), http.MethodGet, "/api/v1/client/updates/files/SHA256SUMS", fixture.ownerToken, "", nil)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 while the token's slot is taken", response.Code)
	}
}

// fixtureTokenID resolves the id the Server limits downloads by.
func fixtureTokenID(t *testing.T, f *clientScopedFixture) string {
	t.Helper()
	identity, err := f.api.clientTokens.ValidateAs(context.Background(), f.ownerToken, storage.TokenTypeClient)
	if err != nil {
		t.Fatal(err)
	}
	return identity.TokenID
}
