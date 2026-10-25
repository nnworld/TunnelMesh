package selfupdate

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/tunnelmesh/tunnelmesh/internal/build"
)

// ServerSource reads releases from the TunnelMesh Server the node is already
// connected to, authenticating with the node's own service token. It suits
// networks that cannot reach GitHub: the node needs no route beyond its Server.
type ServerSource struct {
	// BaseURL is the Server's HTTP(S) address, for example https://server.example/.
	BaseURL string
	// Role is "agent" or "client" and picks the token namespace.
	Role string
	// Token is the node's service token. It is sent only to BaseURL: redirects are
	// not followed, so it can never be forwarded to another host.
	Token string
	HTTP  *http.Client
}

// Name implements Source.
func (s *ServerSource) Name() string { return "server" }

func (s *ServerSource) validate() error {
	if s.Role != "agent" && s.Role != "client" {
		return fmt.Errorf("selfupdate: role %q is not agent or client", s.Role)
	}
	if strings.TrimSpace(s.BaseURL) == "" {
		return fmt.Errorf("selfupdate: no Server address")
	}
	return nil
}

func (s *ServerSource) client() *http.Client {
	c := http.DefaultClient
	if s.HTTP != nil {
		c = s.HTTP
	}
	copied := *c
	copied.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &copied
}

func (s *ServerSource) get(ctx context.Context, rest string) (*http.Response, error) {
	url := strings.TrimRight(strings.TrimSpace(s.BaseURL), "/") + "/api/v1/" + s.Role + "/updates/" + rest
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+s.Token)
	req.Header.Set("User-Agent", "tunnelmesh/"+build.Current().Version+" (self-update)")
	return s.client().Do(req)
}

// Latest asks the Server which release it publishes.
func (s *ServerSource) Latest(ctx context.Context) (Release, error) {
	if err := s.validate(); err != nil {
		return Release{}, err
	}
	resp, err := s.get(ctx, "latest")
	if err != nil {
		return Release{}, err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return Release{}, fmt.Errorf("%w: the Server publishes no release", ErrNotFound)
	case resp.StatusCode != http.StatusOK:
		return Release{}, fmt.Errorf("selfupdate: the Server answered %d to the release query", resp.StatusCode)
	}
	var body struct {
		Data struct {
			Version string `json:"version"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxAPIResponse)).Decode(&body); err != nil {
		return Release{}, fmt.Errorf("selfupdate: unreadable release answer: %w", err)
	}
	return releaseFromTag(body.Data.Version)
}

// Fetch streams one release file. The asset name already carries the version, so
// the release is not part of the URL.
func (s *ServerSource) Fetch(ctx context.Context, _ Release, asset string, maxBytes int64) (io.ReadCloser, error) {
	if err := s.validate(); err != nil {
		return nil, err
	}
	if !assetPattern.MatchString(asset) || asset == "." || asset == ".." {
		return nil, fmt.Errorf("selfupdate: %q is not an acceptable asset name", asset)
	}
	resp, err := s.get(ctx, "files/"+asset)
	if err != nil {
		return nil, err
	}
	switch {
	case resp.StatusCode == http.StatusNotFound:
		_ = resp.Body.Close()
		return nil, fmt.Errorf("%w: %s", ErrNotFound, asset)
	case resp.StatusCode != http.StatusOK:
		_ = resp.Body.Close()
		return nil, fmt.Errorf("selfupdate: fetching %s from the Server: status %d", asset, resp.StatusCode)
	}
	return LimitBody(resp.Body, maxBytes), nil
}
