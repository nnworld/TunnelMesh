// Command deps pins the spike's dependency versions, proves the module graph is
// importable by the standard Go toolchain, and prints the execution environment
// the other probes' privilege claims are made against. gVisor is built with
// Bazel upstream and does not guarantee that every pseudo-version compiles under
// `go build`, so this probe is the gate for every other task.
package main

import (
	"fmt"
	"os"
	"runtime"

	_ "golang.org/x/crypto/curve25519"
	_ "golang.org/x/net/icmp"
	_ "golang.zx2c4.com/wireguard/conn"
	_ "golang.zx2c4.com/wireguard/device"
	_ "golang.zx2c4.com/wireguard/tun"
	_ "golang.zx2c4.com/wireguard/tun/netstack"
	_ "gvisor.dev/gvisor/pkg/buffer"
	_ "gvisor.dev/gvisor/pkg/tcpip"
	_ "gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	_ "gvisor.dev/gvisor/pkg/tcpip/header"
	_ "gvisor.dev/gvisor/pkg/tcpip/link/channel"
	_ "gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	_ "gvisor.dev/gvisor/pkg/tcpip/stack"
	_ "gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	_ "gvisor.dev/gvisor/pkg/tcpip/transport/udp"
)

func main() {
	fmt.Println("PASS: gvisor + wireguard-go + x/net/icmp importable by the go tool")
	fmt.Println("      go:", runtime.Version(), "os:", runtime.GOOS, "arch:", runtime.GOARCH)
	fmt.Println("      uid:", os.Getuid(), "/dev/net/tun absent:", !exists("/dev/net/tun"))
}

func exists(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}
