package main

import (
	"go/build"
	"testing"
)

// TestPinnedPackagesAreImportable guards the exact failure found at gvisor
// v0.0.0-20260919054224-26f3455a4cb9: pkg/tcpip/stack/bridge_test.go declares
// `package bridge_test` in a directory whose package is `stack`, so the go tool
// cannot load the directory at all. If a future `go get -u` re-breaks the pin,
// this test fails here instead of in a downstream task.
func TestPinnedPackagesAreImportable(t *testing.T) {
	for _, path := range []string{
		"gvisor.dev/gvisor/pkg/buffer",
		"gvisor.dev/gvisor/pkg/tcpip",
		"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet",
		"gvisor.dev/gvisor/pkg/tcpip/header",
		"gvisor.dev/gvisor/pkg/tcpip/link/channel",
		"gvisor.dev/gvisor/pkg/tcpip/network/ipv4",
		"gvisor.dev/gvisor/pkg/tcpip/stack",
		"gvisor.dev/gvisor/pkg/tcpip/transport/tcp",
		"gvisor.dev/gvisor/pkg/tcpip/transport/udp",
		"golang.zx2c4.com/wireguard/conn",
		"golang.zx2c4.com/wireguard/device",
		"golang.zx2c4.com/wireguard/tun",
		"golang.zx2c4.com/wireguard/tun/netstack",
		"golang.org/x/crypto/curve25519",
		"golang.org/x/net/icmp",
	} {
		if _, err := build.Import(path, ".", 0); err != nil {
			t.Errorf("cannot import %s: %v", path, err)
		}
	}
}
