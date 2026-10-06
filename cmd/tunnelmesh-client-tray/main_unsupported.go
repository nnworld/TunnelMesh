//go:build !tray || !darwin

// Command tunnelmesh-client-tray is the macOS menu-bar client.
//
// This file is the fallback for every other build. It exists so `go build ./...` and
// `go vet ./...` keep working on Linux, Windows and in the CGO_ENABLED=0 release matrix:
// without it the package would have no buildable Go file at all outside
// "-tags tray && darwin", and a plain ./... would fail for everybody who is not building
// the tray.
package main

import (
	"fmt"
	"os"
	"runtime"
)

func main() {
	fmt.Fprintf(os.Stderr,
		"tunnelmesh-client-tray is a macOS menu-bar client and is not built for %s/%s.\n"+
			"Build it on macOS with: CGO_ENABLED=1 go build -tags tray -o tunnelmesh-client-tray ./cmd/tunnelmesh-client-tray\n"+
			"Or run scripts/package-macos-tray.sh to produce TunnelMesh Client.app.\n",
		runtime.GOOS, runtime.GOARCH)
	os.Exit(1)
}
