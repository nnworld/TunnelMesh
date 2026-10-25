package selfupdate

import (
	"context"
	"errors"
	"io"

	"github.com/tunnelmesh/tunnelmesh/internal/semver"
)

// ErrNotFound means the source has no such release or file. It is permanent for
// the purposes of one upgrade attempt, so it is not retried.
var ErrNotFound = errors.New("selfupdate: not found at the release source")

// Release identifies one published version.
type Release struct {
	Version semver.Version
	// Tag is the version as the source names it, for example "v1.4.0".
	Tag string
}

// Source is where releases come from. The archive and its SHA256SUMS are always
// fetched from the same Source, so a checksum can only vouch for an archive the
// same place served.
type Source interface {
	Name() string
	// Latest returns the newest published release.
	Latest(ctx context.Context) (Release, error)
	// Fetch streams one release file. maxBytes bounds it; reading past the bound
	// fails with ErrTooLarge, so a hostile or broken source cannot fill the disk.
	Fetch(ctx context.Context, r Release, asset string, maxBytes int64) (io.ReadCloser, error)
}

// AssetName is the release archive for a version and platform, matching what
// scripts/build-release.sh publishes.
func AssetName(v semver.Version, goos, goarch string) string {
	ext := ".tar.gz"
	if goos == "windows" {
		ext = ".zip"
	}
	return "tunnelmesh-" + v.String() + "-" + goos + "-" + goarch + ext
}

// ChecksumsAsset is the checksum file published beside the archives.
const ChecksumsAsset = "SHA256SUMS"

// LimitBody wraps body so that reading more than max bytes fails with ErrTooLarge.
// A body of exactly max bytes is allowed.
func LimitBody(body io.ReadCloser, max int64) io.ReadCloser {
	return &limitedBody{ReadCloser: body, remaining: max}
}

type limitedBody struct {
	io.ReadCloser
	remaining int64
}

func (b *limitedBody) Read(p []byte) (int, error) {
	if b.remaining < 0 {
		return 0, ErrTooLarge
	}
	// Reading one byte past the limit is what distinguishes "exactly max" from "more".
	if int64(len(p)) > b.remaining+1 {
		p = p[:b.remaining+1]
	}
	n, err := b.ReadCloser.Read(p)
	if int64(n) > b.remaining {
		b.remaining = -1
		return n - 1, ErrTooLarge
	}
	b.remaining -= int64(n)
	return n, err
}
