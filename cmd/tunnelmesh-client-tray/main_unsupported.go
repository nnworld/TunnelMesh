//go:build !tray || (!darwin && !windows)

// Command tunnelmesh-client-tray is the desktop tray client; this is its fallback build.
//
// It exists so `go build ./...` and `go vet ./...` keep working on Linux and inside the
// CGO_ENABLED=0 release matrix: without it the package would have no buildable Go file at
// all outside "-tags tray" on macOS or Windows, and a plain ./... would fail for everybody
// who is not building the tray.
package main

import (
	"fmt"
	"os"
	"runtime"
)

func main() {
	fmt.Fprintf(os.Stderr,
		"tunnelmesh-client-tray is a macOS and Windows tray client and is not built for %s/%s.\n"+
			"On macOS: CGO_ENABLED=1 go build -tags tray -o tunnelmesh-client-tray ./cmd/tunnelmesh-client-tray\n"+
			"On Windows: CGO_ENABLED=0 go build -tags tray -o TunnelMeshClient.exe ./cmd/tunnelmesh-client-tray\n"+
			"Or run scripts/package-macos-tray.sh and scripts/package-windows-tray.sh for installers.\n",
		runtime.GOOS, runtime.GOARCH)
	os.Exit(1)
}
