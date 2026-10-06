package webdist

import (
	"io/fs"
	"strings"
	"testing"
)

// TestFSAndIndexAgree pins the contract the tray's static handler depends on, in both
// build modes.
//
// With the "tray" tag the bundle is embedded and FS is rooted at the document root - not
// at dist/ - so serving "index.html" works without the caller knowing the embed layout.
// Without the tag there is nothing to embed, and FS must then report no bundle rather
// than a filesystem whose every read fails: the server picks its placeholder page on
// exactly that nil, and a non-nil empty FS would render a blank window instead.
func TestFSAndIndexAgree(t *testing.T) {
	assets, err := FS()
	if err != nil {
		t.Fatalf("FS: %v", err)
	}
	if assets == nil {
		if Index() {
			t.Fatal("Index() = true although FS returned no bundle")
		}
		return
	}

	data, err := fs.ReadFile(assets, "index.html")
	if err != nil {
		t.Fatalf("index.html is not reachable from the returned filesystem: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("index.html is empty")
	}
	if !Index() {
		t.Fatal("Index() = false although index.html was just read")
	}
	if !strings.Contains(string(data), "<div id=\"app\"") {
		t.Error("index.html does not look like the built settings interface; run: cd web-tray && npm run build")
	}
}
