//go:build tray

// Package webdist carries the settings window's production bundle.
//
// It is a separate package from the admin console's internal/server/web_dist because
// the two are different applications with different lifecycles: the admin bundle is
// embedded in the Server, this one in the macOS tray client. Keeping them apart means
// rebuilding one never touches the other, and the cross-platform Server build stays
// free of anything the tray needs.
//
// The dist directory is produced by `cd web-tray && npm run build`, which mirrors its
// output here through web-tray/scripts/sync-tray-dist.mjs. It is never edited by hand,
// and it is not tracked: like internal/server/web_dist it is a build output.
//
// Both halves of the package sit behind a build tag. `//go:embed` fails the compile when
// its pattern matches nothing, so an untagged embed would make every `go build ./...`
// depend on a front-end toolchain nobody asked for - including a Linux checkout that can
// never run the tray. Tagging it keeps the default build independent and moves the
// requirement to `scripts/package-macos-tray.sh`, which refuses to package without it.
package webdist

import (
	"embed"
	"io/fs"
)

// all: is required: go:embed skips files whose name starts with "." or "_" by default,
// and a bundler is free to emit such a chunk. A missing asset would fall through to a
// 404 in the webview and render a blank window.
//
//go:embed all:dist
var dist embed.FS

// FS returns the bundle rooted at the document root, so a caller can serve
// "index.html" without knowing that the files live under dist/ in the module.
func FS() (fs.FS, error) {
	return fs.Sub(dist, "dist")
}

// Index reports whether a real bundle was embedded.
//
// A tray built without running the front-end build still has to start; reporting the
// missing bundle lets it serve an explanatory page instead of an empty window that
// looks like a rendering bug.
func Index() bool {
	entries, err := fs.ReadDir(dist, "dist")
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if entry.Name() == "index.html" {
			return true
		}
	}
	return false
}
