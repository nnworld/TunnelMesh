//go:build !tray

// This is the half of package webdist that compiles without the "tray" build tag. The
// package doc lives in embed.go.
//
// The API is identical so that callers - and the tests that exercise them - do not have
// to fork on the build tag. Without the tag there is nothing to embed, so FS reports no
// bundle and Index reports false, which is exactly the state the tray's local server
// already handles by rendering its "run npm run build" placeholder page.
package webdist

import "io/fs"

// FS reports that no bundle is compiled into this binary.
func FS() (fs.FS, error) { return nil, nil }

// Index reports false: an untagged build embeds no settings interface.
func Index() bool { return false }
