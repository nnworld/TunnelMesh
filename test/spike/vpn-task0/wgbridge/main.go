// Probe: can a WireGuard tunnel run entirely in memory, with NO /dev/net/tun
// and NO CAP_NET_ADMIN? Two in-process wireguard-go devices are attached to
// gVisor-netstack-backed tun.Device implementations (upstream
// golang.zx2c4.com/wireguard/tun/netstack), complete a real Noise handshake
// over a UDP loopback bind, and carry a real TCP connection end to end.
//
// This is the load-bearing claim of the embedded VPN gateway design: the VPN
// data plane can live inside tunnelmesh-server without granting it any kernel
// networking privilege.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/netip"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/curve25519"
	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun"
	"golang.zx2c4.com/wireguard/tun/netstack"
)

const (
	mtu           = 1420
	serverVPNIP   = "10.64.0.1"
	clientVPNIP   = "10.64.0.2"
	serverTCPPort = uint16(8080)
	wantBody      = "hello-over-wireguard-memory-tun"
)

func main() {
	// CreateNetTUN returns a tun.Device backed purely by a gVisor netstack and a
	// channel link endpoint -- there is no file descriptor behind it, so
	// tun.Device.File() returns nil and nothing ever opens /dev/net/tun.
	serverTUN, serverNet, err := netstack.CreateNetTUN(
		[]netip.Addr{netip.MustParseAddr(serverVPNIP)}, nil, mtu)
	if err != nil {
		fail("CreateNetTUN(server)", err)
	}
	clientTUN, clientNet, err := netstack.CreateNetTUN(
		[]netip.Addr{netip.MustParseAddr(clientVPNIP)}, nil, mtu)
	if err != nil {
		fail("CreateNetTUN(client)", err)
	}
	var _ tun.Device = serverTUN // compile-time proof of the interface

	logger := &device.Logger{Verbosef: device.DiscardLogf, Errorf: device.DiscardLogf}
	serverDev := device.NewDevice(serverTUN, conn.NewDefaultBind(), logger)
	clientDev := device.NewDevice(clientTUN, conn.NewDefaultBind(), logger)
	defer serverDev.Close()
	defer clientDev.Close()

	serverPriv := newKey()
	clientPriv := newKey()
	serverPub, err := curve25519.X25519(serverPriv, curve25519.Basepoint)
	if err != nil {
		fail("server public key", err)
	}
	clientPub, err := curve25519.X25519(clientPriv, curve25519.Basepoint)
	if err != nil {
		fail("client public key", err)
	}

	if err := serverDev.IpcSet(fmt.Sprintf("private_key=%s\nlisten_port=0\nreplace_peers=true\n", hex.EncodeToString(serverPriv))); err != nil {
		fail("server IpcSet identity", err)
	}
	listenPort := currentListenPort(serverDev)
	if listenPort == 0 {
		fail("could not read the server's WireGuard listen port", nil)
	}
	// conn.NewDefaultBind() opens ListenPacket(ctx, network, ":"+port), i.e. a
	// wildcard bind on every interface -- not a loopback bind. 127.0.0.1 is only
	// the endpoint the client is told to dial. Phase 1 must carry this into the
	// deployment docs: the VPN data plane holds a wildcard UDP port.
	fmt.Printf("OK: wireguard-go accepted an in-memory tun.Device; server bound to UDP :%d (wildcard), client endpoint 127.0.0.1:%d\n", listenPort, listenPort)

	if err := serverDev.IpcSet(fmt.Sprintf("public_key=%s\nallowed_ip=%s/32\n", hex.EncodeToString(clientPub), clientVPNIP)); err != nil {
		fail("server IpcSet peer", err)
	}
	if err := clientDev.IpcSet(fmt.Sprintf(
		"private_key=%s\nlisten_port=0\nreplace_peers=true\npublic_key=%s\nendpoint=127.0.0.1:%d\nallowed_ip=%s/32\npersistent_keepalive_interval=0\n",
		hex.EncodeToString(clientPriv), hex.EncodeToString(serverPub), listenPort, serverVPNIP)); err != nil {
		fail("client IpcSet", err)
	}

	// Server side: terminate the VPN-borne TCP connection inside its own
	// netstack, exactly as the gateway will before splicing to a TunnelMesh stream.
	ln, err := serverNet.ListenTCPAddrPort(netip.AddrPortFrom(netip.MustParseAddr(serverVPNIP), serverTCPPort))
	if err != nil {
		fail("server ListenTCPAddrPort", err)
	}
	defer ln.Close()

	type accepted struct {
		remote string
		body   string
	}
	got := make(chan accepted, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		b, _ := io.ReadAll(c)
		got <- accepted{remote: c.RemoteAddr().String(), body: string(b)}
	}()

	// Client side: dial through its own netstack; the packet leaves via the
	// memory TUN, is encrypted by wireguard-go, and crosses a real UDP socket.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cc, err := clientNet.DialContextTCPAddrPort(ctx, netip.AddrPortFrom(netip.MustParseAddr(serverVPNIP), serverTCPPort))
	if err != nil {
		fail("client DialContextTCPAddrPort (handshake did not complete)", err)
	}
	if _, err := cc.Write([]byte(wantBody)); err != nil {
		fail("client write", err)
	}
	cc.CloseWrite()

	select {
	case a := <-got:
		if a.body != wantBody {
			fail(fmt.Sprintf("payload %q, want %q", a.body, wantBody), nil)
		}
		fmt.Printf("PASS: WireGuard handshake + TCP round trip over memory TUN; server saw %s -> %q\n", a.remote, a.body)
		fmt.Printf("      env: %s/%s uid=%d, /dev/net/tun absent=%v, CAP_NET_ADMIN=%s\n",
			runtime.GOOS, runtime.GOARCH, os.Getuid(), !fileExists("/dev/net/tun"), capNetAdmin())
		return
	case <-time.After(15 * time.Second):
		fail("no data accepted on the server side within 15s", nil)
	}
}

// newKey returns 32 random bytes suitable as a WireGuard X25519 private key.
// wgctrl/wgtypes is a separate module and is deliberately not added to the
// spike; crypto/rand + curve25519 is all the IPC protocol needs.
func newKey() []byte {
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		fail("generate key", err)
	}
	return k
}

// currentListenPort reads back the port the kernel assigned for listen_port=0.
func currentListenPort(d *device.Device) int {
	out, err := d.IpcGet()
	if err != nil {
		return 0
	}
	port := 0
	for _, line := range strings.Split(out, "\n") {
		if n, ok := strings.CutPrefix(line, "listen_port="); ok {
			if v, err := strconv.Atoi(strings.TrimSpace(n)); err == nil {
				port = v
			}
		}
	}
	return port
}

// capNetAdmin reports whether this process actually holds CAP_NET_ADMIN, read
// from the kernel rather than asserted. It is the difference between "the probe
// claims it needs no privilege" and "the probe proves it had none": on Linux the
// effective capability mask is decoded and bit 12 tested. Outside Linux there is
// no equivalent mask, so the result is reported as unavailable instead of being
// silently reported as absent.
func capNetAdmin() string {
	if runtime.GOOS != "linux" {
		return "n/a (only Linux exposes a capability mask)"
	}
	data, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return "unknown (/proc/self/status unreadable)"
	}
	for _, line := range strings.Split(string(data), "\n") {
		v, ok := strings.CutPrefix(line, "CapEff:")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		mask, err := strconv.ParseUint(v, 16, 64)
		if err != nil {
			return "unknown (CapEff=" + v + " unparsable)"
		}
		const capNetAdminBit = 12
		if mask&(1<<capNetAdminBit) != 0 {
			return "PRESENT (CapEff=" + v + ") -- the claim is NOT evidenced"
		}
		return "absent (CapEff=" + v + ")"
	}
	return "unknown (no CapEff line in /proc/self/status)"
}

func fileExists(p string) bool {
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}

func fail(stage string, err error) {
	if err != nil {
		fmt.Printf("FAIL %s: %v\n", stage, err)
	} else {
		fmt.Printf("FAIL %s\n", stage)
	}
	os.Exit(1)
}
