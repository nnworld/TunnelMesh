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

	"github.com/tunnelmesh/tunnelmesh/internal/semver"
)

type fakeGitHub struct {
	server     *httptest.Server
	apiHits    atomic.Int32
	apiStatus  int
	apiBody    string
	tag        string
	lastAuth   atomic.Value
	lastAgent  atomic.Value
	assets     map[string]string
	downloadOK atomic.Int32
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	t.Helper()
	g := &fakeGitHub{apiStatus: http.StatusOK, tag: "v1.4.0", assets: map[string]string{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/repos/acme/tunnelmesh/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		g.apiHits.Add(1)
		g.lastAuth.Store(r.Header.Get("Authorization"))
		g.lastAgent.Store(r.Header.Get("User-Agent"))
		if g.apiStatus != http.StatusOK {
			w.WriteHeader(g.apiStatus)
			return
		}
		body := g.apiBody
		if body == "" {
			body = `{"tag_name":"` + g.tag + `"}`
		}
		_, _ = io.WriteString(w, body)
	})
	mux.HandleFunc("/acme/tunnelmesh/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/acme/tunnelmesh/releases/tag/"+g.tag, http.StatusFound)
	})
	mux.HandleFunc("/acme/tunnelmesh/releases/download/", func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/acme/tunnelmesh/releases/download/")
		tag, asset, _ := strings.Cut(rest, "/")
		body, ok := g.assets[tag+"/"+asset]
		if !ok {
			http.NotFound(w, r)
			return
		}
		g.downloadOK.Add(1)
		// Real GitHub redirects downloads to a CDN; follow-the-redirect must work.
		if r.URL.Query().Get("direct") == "" && strings.HasSuffix(asset, ".tar.gz") {
			http.Redirect(w, r, r.URL.Path+"?direct=1", http.StatusFound)
			return
		}
		_, _ = io.WriteString(w, body)
	})
	g.server = httptest.NewServer(mux)
	t.Cleanup(g.server.Close)
	return g
}

func (g *fakeGitHub) source() *GitHubSource {
	return &GitHubSource{Repo: "acme/tunnelmesh", HTTP: g.server.Client(), APIBase: g.server.URL + "/api", WebBase: g.server.URL}
}

func TestGitHubLatestUsesTheAPI(t *testing.T) {
	g := newFakeGitHub(t)
	src := g.source()
	src.Token = "ghp_secret"
	rel, err := src.Latest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rel.Tag != "v1.4.0" || rel.Version != (semver.Version{Major: 1, Minor: 4}) {
		t.Fatalf("Latest() = %+v", rel)
	}
	if got := g.lastAuth.Load(); got != "Bearer ghp_secret" {
		t.Fatalf("Authorization = %v, want the token as a bearer credential", got)
	}
	if ua, _ := g.lastAgent.Load().(string); !strings.HasPrefix(ua, "tunnelmesh") {
		t.Fatalf("User-Agent = %q, want one that names the product", ua)
	}
}

// api.github.com rate-limits unauthenticated callers hard, and a fleet behind one
// NAT shares that budget; the redirect on github.com has no such limit.
func TestGitHubLatestFallsBackToTheRedirectWhenTheAPIIsRateLimited(t *testing.T) {
	g := newFakeGitHub(t)
	g.apiStatus = http.StatusForbidden
	rel, err := g.source().Latest(context.Background())
	if err != nil || rel.Tag != "v1.4.0" {
		t.Fatalf("Latest() = %+v, %v, want the version from the redirect", rel, err)
	}
	if g.apiHits.Load() != 1 {
		t.Fatalf("API hits = %d, want 1", g.apiHits.Load())
	}
}

func TestGitHubLatestFallsBackWhenTheAPIAnswersGarbage(t *testing.T) {
	g := newFakeGitHub(t)
	g.apiBody = "<html>proxy login page</html>"
	rel, err := g.source().Latest(context.Background())
	if err != nil || rel.Tag != "v1.4.0" {
		t.Fatalf("Latest() = %+v, %v", rel, err)
	}
}

// A mirror rarely proxies the API, so with one configured only the redirect is used.
func TestGitHubLatestWithAMirrorNeverCallsTheAPI(t *testing.T) {
	g := newFakeGitHub(t)
	src := &GitHubSource{Repo: "acme/tunnelmesh", HTTP: g.server.Client(), MirrorBase: g.server.URL + "/", APIBase: g.server.URL + "/api"}
	rel, err := src.Latest(context.Background())
	if err != nil || rel.Tag != "v1.4.0" {
		t.Fatalf("Latest() = %+v, %v", rel, err)
	}
	if g.apiHits.Load() != 0 {
		t.Fatalf("API hits = %d, want 0 when a mirror is configured", g.apiHits.Load())
	}
}

func TestGitHubLatestRejectsATagThatIsNotAReleaseVersion(t *testing.T) {
	g := newFakeGitHub(t)
	g.tag = "nightly"
	g.apiBody = `{"tag_name":"nightly"}`
	if _, err := g.source().Latest(context.Background()); err == nil {
		t.Fatal("Latest() accepted a non-release tag")
	}
}

func TestGitHubFetchStreamsAnAssetAndFollowsRedirects(t *testing.T) {
	g := newFakeGitHub(t)
	g.assets["v1.4.0/tunnelmesh-v1.4.0-linux-amd64.tar.gz"] = "ARCHIVE-BYTES"
	body, err := g.source().Fetch(context.Background(), Release{Tag: "v1.4.0"}, "tunnelmesh-v1.4.0-linux-amd64.tar.gz", 1024)
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	if data, _ := io.ReadAll(body); string(data) != "ARCHIVE-BYTES" {
		t.Fatalf("body = %q", data)
	}
}

func TestGitHubFetchMapsAMissingAssetToErrNotFound(t *testing.T) {
	g := newFakeGitHub(t)
	if _, err := g.source().Fetch(context.Background(), Release{Tag: "v1.4.0"}, "SHA256SUMS", 1024); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestGitHubFetchEnforcesTheSizeLimit(t *testing.T) {
	g := newFakeGitHub(t)
	g.assets["v1.4.0/SHA256SUMS"] = strings.Repeat("x", 500)
	body, err := g.source().Fetch(context.Background(), Release{Tag: "v1.4.0"}, "SHA256SUMS", 100)
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	if _, err := io.ReadAll(body); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("ReadAll error = %v, want ErrTooLarge", err)
	}
}

// The URL is built from configuration and a version, never from message content;
// even so, an asset name that could change the path is refused outright.
func TestGitHubFetchRefusesAssetNamesThatCouldChangeTheURLPath(t *testing.T) {
	g := newFakeGitHub(t)
	for _, asset := range []string{"../x", "a/b", "a?b=c", "a#b", "", "..", "a b"} {
		if _, err := g.source().Fetch(context.Background(), Release{Tag: "v1.4.0"}, asset, 10); err == nil {
			t.Errorf("Fetch(%q) was allowed", asset)
		}
	}
	if _, err := g.source().Fetch(context.Background(), Release{Tag: "v1.4.0/../../x"}, "SHA256SUMS", 10); err == nil {
		t.Error("a tag with path segments was allowed")
	}
}

func TestGitHubRepositoryMustLookLikeOwnerSlashName(t *testing.T) {
	for _, repo := range []string{"", "noslash", "a/b/c", "../x", "a/..", "a b/c"} {
		if _, err := (&GitHubSource{Repo: repo}).Latest(context.Background()); err == nil {
			t.Errorf("Repo %q was accepted", repo)
		}
	}
}
