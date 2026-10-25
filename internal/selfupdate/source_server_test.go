package selfupdate

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

type fakeUpdateServer struct {
	server   *httptest.Server
	lastAuth atomic.Value
	lastPath atomic.Value
	files    map[string]string
	latest   string
	status   int
}

func newFakeUpdateServer(t *testing.T, role string) *fakeUpdateServer {
	t.Helper()
	f := &fakeUpdateServer{files: map[string]string{}, latest: `{"code":200,"msg":"OK","data":{"version":"v1.4.0","server_version":"v1.4.0"}}`}
	prefix := "/api/v1/" + role + "/updates/"
	mux := http.NewServeMux()
	mux.HandleFunc(prefix+"latest", func(w http.ResponseWriter, r *http.Request) {
		f.lastAuth.Store(r.Header.Get("Authorization"))
		f.lastPath.Store(r.URL.Path)
		if f.status != 0 {
			w.WriteHeader(f.status)
			return
		}
		_, _ = io.WriteString(w, f.latest)
	})
	mux.HandleFunc(prefix+"files/", func(w http.ResponseWriter, r *http.Request) {
		f.lastAuth.Store(r.Header.Get("Authorization"))
		f.lastPath.Store(r.URL.Path)
		if f.status != 0 {
			w.WriteHeader(f.status)
			return
		}
		body, ok := f.files[strings.TrimPrefix(r.URL.Path, prefix+"files/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, body)
	})
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeUpdateServer) source(role string) *ServerSource {
	return &ServerSource{BaseURL: f.server.URL + "/", Role: role, Token: "stok_secret", HTTP: f.server.Client()}
}

func TestServerSourceLatestUsesTheTokenNamespaceOfItsRole(t *testing.T) {
	for _, role := range []string{"agent", "client"} {
		t.Run(role, func(t *testing.T) {
			f := newFakeUpdateServer(t, role)
			rel, err := f.source(role).Latest(context.Background())
			if err != nil || rel.Tag != "v1.4.0" {
				t.Fatalf("Latest() = %+v, %v", rel, err)
			}
			if got := f.lastAuth.Load(); got != "Bearer stok_secret" {
				t.Fatalf("Authorization = %v, want the service token as a bearer credential", got)
			}
			if got := f.lastPath.Load(); got != "/api/v1/"+role+"/updates/latest" {
				t.Fatalf("path = %v", got)
			}
		})
	}
}

func TestServerSourceLatestReportsWhatWentWrong(t *testing.T) {
	f := newFakeUpdateServer(t, "client")
	f.status = http.StatusNotFound
	if _, err := f.source("client").Latest(context.Background()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a 404 must be ErrNotFound (nothing published or distribution off), got %v", err)
	}
	f.status = http.StatusUnauthorized
	if _, err := f.source("client").Latest(context.Background()); err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("a 401 must be an error that is not ErrNotFound, got %v", err)
	}
	f.status = 0
	f.latest = `not json`
	if _, err := f.source("client").Latest(context.Background()); err == nil {
		t.Fatal("garbage was accepted")
	}
	f.latest = `{"data":{"version":"nightly"}}`
	if _, err := f.source("client").Latest(context.Background()); err == nil {
		t.Fatal("a version that is not a release was accepted")
	}
}

func TestServerSourceFetchStreamsAFileWithTheToken(t *testing.T) {
	f := newFakeUpdateServer(t, "agent")
	f.files["SHA256SUMS"] = "sums"
	body, err := f.source("agent").Fetch(context.Background(), Release{Tag: "v1.4.0"}, "SHA256SUMS", 100)
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	if data, _ := io.ReadAll(body); string(data) != "sums" {
		t.Fatalf("body = %q", data)
	}
	if got := f.lastAuth.Load(); got != "Bearer stok_secret" {
		t.Fatalf("Authorization = %v", got)
	}
}

func TestServerSourceFetchMapsStatusesAndEnforcesTheLimit(t *testing.T) {
	f := newFakeUpdateServer(t, "agent")
	if _, err := f.source("agent").Fetch(context.Background(), Release{}, "SHA256SUMS", 100); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing file: %v, want ErrNotFound", err)
	}
	f.files["SHA256SUMS"] = strings.Repeat("x", 500)
	body, err := f.source("agent").Fetch(context.Background(), Release{}, "SHA256SUMS", 100)
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	if _, err := io.ReadAll(body); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("ReadAll error = %v, want ErrTooLarge", err)
	}
	f.status = http.StatusServiceUnavailable
	if _, err := f.source("agent").Fetch(context.Background(), Release{}, "SHA256SUMS", 100); err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("a 503 must be a retryable error, got %v", err)
	}
}

// The service token is the credential for this Server and nowhere else.
func TestServerSourceNeverFollowsARedirect(t *testing.T) {
	f := newFakeUpdateServer(t, "client")
	// A file route that answers with a redirect to another host.
	f.server.Config.Handler.(*http.ServeMux).HandleFunc("/api/v1/client/updates/files/moved", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://127.0.0.1:1/steal", http.StatusFound)
	})
	if _, err := f.source("client").Fetch(context.Background(), Release{}, "moved", 100); err == nil {
		t.Fatal("a redirect was followed or silently accepted")
	}
}

func TestServerSourceRefusesNamesThatCouldChangeThePath(t *testing.T) {
	f := newFakeUpdateServer(t, "client")
	for _, asset := range []string{"../x", "a/b", "a?b", "a#b", "", "..", "a b"} {
		if _, err := f.source("client").Fetch(context.Background(), Release{}, asset, 10); err == nil {
			t.Errorf("Fetch(%q) was allowed", asset)
		}
	}
	bad := &ServerSource{BaseURL: f.server.URL, Role: "../admin", Token: "t", HTTP: f.server.Client()}
	if _, err := bad.Latest(context.Background()); err == nil {
		t.Error("a role that is not agent or client was allowed")
	}
}
