package selfupdate

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"

	"github.com/tunnelmesh/tunnelmesh/internal/build"
	"github.com/tunnelmesh/tunnelmesh/internal/semver"
)

const (
	defaultAPIBase = "https://api.github.com"
	defaultWebBase = "https://github.com"
	// maxAPIResponse bounds the "latest release" JSON; the real one is a few KiB
	// but carries every asset, so leave room.
	maxAPIResponse = 4 << 20
)

var (
	repoPattern  = regexp.MustCompile(`^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$`)
	assetPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
)

// GitHubSource reads releases from GitHub, or from a GitHub-compatible mirror.
type GitHubSource struct {
	// Repo is "owner/name".
	Repo string
	// MirrorBase, when set, replaces github.com for both the "latest" lookup and
	// the downloads, and the API is not used: a mirror rarely proxies it.
	MirrorBase string
	// HTTP carries the requests. It is where proxy support comes from: the caller
	// builds it on the same transport.Dialer the connection to the Server uses.
	HTTP *http.Client
	// Token is optional and only raises GitHub's API rate limit.
	Token string
	// APIBase and WebBase are overridden by tests.
	APIBase, WebBase string
}

// Name implements Source.
func (g *GitHubSource) Name() string { return "github:" + g.Repo }

func (g *GitHubSource) validate() error {
	if !repoPattern.MatchString(g.Repo) || strings.Contains(g.Repo, "..") {
		return fmt.Errorf("selfupdate: %q is not an owner/name repository", g.Repo)
	}
	return nil
}

func (g *GitHubSource) webBase() string {
	switch {
	case strings.TrimSpace(g.MirrorBase) != "":
		return strings.TrimRight(strings.TrimSpace(g.MirrorBase), "/")
	case g.WebBase != "":
		return strings.TrimRight(g.WebBase, "/")
	default:
		return defaultWebBase
	}
}

func (g *GitHubSource) client() *http.Client {
	if g.HTTP != nil {
		return g.HTTP
	}
	return http.DefaultClient
}

func (g *GitHubSource) newRequest(ctx context.Context, rawURL string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "tunnelmesh/"+build.Current().Version+" (self-update)")
	return req, nil
}

// Latest finds the newest release. Unless a mirror is configured it asks the API
// first, and falls back to the redirect on /releases/latest when the API is
// unreachable, rate limited or answers something unreadable. The redirect has no
// rate limit, which matters because a fleet behind one NAT shares the API's.
func (g *GitHubSource) Latest(ctx context.Context) (Release, error) {
	if err := g.validate(); err != nil {
		return Release{}, err
	}
	if strings.TrimSpace(g.MirrorBase) == "" {
		if tag, err := g.latestFromAPI(ctx); err == nil {
			return releaseFromTag(tag)
		}
	}
	tag, err := g.latestFromRedirect(ctx)
	if err != nil {
		return Release{}, err
	}
	return releaseFromTag(tag)
}

func releaseFromTag(tag string) (Release, error) {
	version, err := semver.Parse(tag)
	if err != nil {
		return Release{}, fmt.Errorf("selfupdate: latest release tag %q is not a release version", tag)
	}
	return Release{Version: version, Tag: version.String()}, nil
}

func (g *GitHubSource) latestFromAPI(ctx context.Context) (string, error) {
	apiBase := g.APIBase
	if apiBase == "" {
		apiBase = defaultAPIBase
	}
	req, err := g.newRequest(ctx, strings.TrimRight(apiBase, "/")+"/repos/"+g.Repo+"/releases/latest")
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if g.Token != "" {
		req.Header.Set("Authorization", "Bearer "+g.Token)
	}
	resp, err := g.client().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("github api: status %d", resp.StatusCode)
	}
	var body struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxAPIResponse)).Decode(&body); err != nil {
		return "", err
	}
	if body.TagName == "" {
		return "", fmt.Errorf("github api: no tag_name")
	}
	return body.TagName, nil
}

// latestFromRedirect reads the tag out of where /releases/latest redirects to
// (.../releases/tag/<tag>), without following it.
func (g *GitHubSource) latestFromRedirect(ctx context.Context) (string, error) {
	req, err := g.newRequest(ctx, g.webBase()+"/"+g.Repo+"/releases/latest")
	if err != nil {
		return "", err
	}
	client := *g.client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 300 || resp.StatusCode > 399 {
		return "", fmt.Errorf("selfupdate: %s/releases/latest answered %d, want a redirect", g.Repo, resp.StatusCode)
	}
	location, err := resp.Location()
	if err != nil {
		return "", err
	}
	const marker = "/releases/tag/"
	i := strings.LastIndex(location.Path, marker)
	if i < 0 {
		return "", fmt.Errorf("selfupdate: unexpected redirect target %q", location.Path)
	}
	tag := location.Path[i+len(marker):]
	if tag == "" || strings.Contains(tag, "/") {
		return "", fmt.Errorf("selfupdate: unexpected redirect target %q", location.Path)
	}
	return tag, nil
}

// Fetch streams one asset of a release. The URL is assembled from the configured
// repository, the release tag and an asset name checked against a strict pattern,
// so nothing a message or a response says can redirect the request elsewhere.
func (g *GitHubSource) Fetch(ctx context.Context, rel Release, asset string, maxBytes int64) (io.ReadCloser, error) {
	if err := g.validate(); err != nil {
		return nil, err
	}
	if !assetPattern.MatchString(asset) || asset == "." || asset == ".." {
		return nil, fmt.Errorf("selfupdate: %q is not an acceptable asset name", asset)
	}
	if _, err := semver.Parse(rel.Tag); err != nil || path.Base(rel.Tag) != rel.Tag {
		return nil, fmt.Errorf("selfupdate: %q is not a release tag", rel.Tag)
	}
	rawURL := g.webBase() + "/" + g.Repo + "/releases/download/" + url.PathEscape(rel.Tag) + "/" + asset
	req, err := g.newRequest(ctx, rawURL)
	if err != nil {
		return nil, err
	}
	resp, err := g.client().Do(req)
	if err != nil {
		return nil, err
	}
	switch {
	case resp.StatusCode == http.StatusNotFound:
		_ = resp.Body.Close()
		return nil, fmt.Errorf("%w: %s", ErrNotFound, asset)
	case resp.StatusCode != http.StatusOK:
		_ = resp.Body.Close()
		return nil, fmt.Errorf("selfupdate: fetching %s: status %d", asset, resp.StatusCode)
	}
	return LimitBody(resp.Body, maxBytes), nil
}
