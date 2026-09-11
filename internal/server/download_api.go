package server

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/tunnelmesh/tunnelmesh/internal/auth"
	"github.com/tunnelmesh/tunnelmesh/internal/build"
	"github.com/tunnelmesh/tunnelmesh/internal/config"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

// DownloadAsset describes one immutable release archive and its download URL.
type DownloadAsset struct {
	Platform string `json:"platform"`
	Archive  string `json:"archive"`
	URL      string `json:"url"`
}

// DownloadInfo is the admin-facing release manifest. It deliberately contains
// only public build identity and download URLs, never credentials or DSNs.
type DownloadInfo struct {
	Version       string          `json:"version"`
	Commit        string          `json:"commit"`
	BuildTime     string          `json:"buildTime"`
	Repository    string          `json:"repository"`
	ReleaseURL    string          `json:"releaseUrl"`
	ChecksumURL   string          `json:"checksumUrl"`
	ManifestURL   string          `json:"manifestUrl"`
	SchemaVersion int             `json:"schemaVersion"`
	Assets        []DownloadAsset `json:"assets"`
}

// SetDownloads injects the release repository configured for this deployment.
func (a *API) SetDownloads(cfg config.DownloadsConfig) {
	if a == nil {
		return
	}
	repository := strings.TrimSpace(cfg.GitHubRepository)
	if repository == "" {
		repository = config.DefaultGitHubRepository
	}
	a.downloads = config.DownloadsConfig{GitHubRepository: repository}
}

func (a *API) handleDownloads(w http.ResponseWriter, r *http.Request, principal auth.Principal) {
	if !isAdmin(principal) {
		writeAPIError(w, http.StatusForbidden, "admin role required")
		return
	}
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, newDownloadInfo(a.downloads))
}

func newDownloadInfo(cfg config.DownloadsConfig) DownloadInfo {
	repository := strings.TrimSpace(cfg.GitHubRepository)
	if repository == "" {
		repository = config.DefaultGitHubRepository
	}
	info := build.Current()
	owner, name, _ := strings.Cut(repository, "/")
	releaseBase := "https://github.com/" + url.PathEscape(owner) + "/" + url.PathEscape(name) + "/releases"
	version := url.PathEscape(info.Version)
	downloadBase := releaseBase + "/download/" + version
	assets := []DownloadAsset{
		{Platform: "linux-amd64", Archive: "tunnelmesh-" + info.Version + "-linux-amd64.tar.gz"},
		{Platform: "linux-arm64", Archive: "tunnelmesh-" + info.Version + "-linux-arm64.tar.gz"},
		{Platform: "darwin-amd64", Archive: "tunnelmesh-" + info.Version + "-darwin-amd64.tar.gz"},
		{Platform: "darwin-arm64", Archive: "tunnelmesh-" + info.Version + "-darwin-arm64.tar.gz"},
		{Platform: "windows-amd64", Archive: "tunnelmesh-" + info.Version + "-windows-amd64.zip"},
		{Platform: "windows-arm64", Archive: "tunnelmesh-" + info.Version + "-windows-arm64.zip"},
	}
	for i := range assets {
		assets[i].URL = downloadBase + "/" + url.PathEscape(assets[i].Archive)
	}
	return DownloadInfo{
		Version: info.Version, Commit: info.Commit, BuildTime: info.BuildTime,
		Repository: repository, ReleaseURL: releaseBase + "/tag/" + version,
		ChecksumURL: downloadBase + "/SHA256SUMS", ManifestURL: downloadBase + "/manifest.json",
		SchemaVersion: storage.SchemaVersion, Assets: assets,
	}
}
