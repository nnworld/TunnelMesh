package vpn_test

import (
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/tunnelmesh/tunnelmesh/internal/vpn"
)

func splitTunnelConfig() vpn.PeerConfig {
	return vpn.PeerConfig{
		PrivateKey:          rfcAlicePrivate,
		Address:             net.ParseIP("10.64.5.7"),
		MTU:                 1420,
		NodePublicKey:       rfcBobPublic,
		AllowedIPs:          cidrs("192.0.2.0/24", "10.0.0.0/8"),
		Endpoint:            "gw-1.mesh.example.com:51820",
		PersistentKeepalive: vpn.DefaultPersistentKeepalive,
	}
}

func fullTunnelConfig() vpn.PeerConfig {
	return vpn.PeerConfig{
		PrivateKey:          rfcBobPrivate,
		Address:             net.ParseIP("10.64.9.254"),
		MTU:                 1280,
		NodePublicKey:       rfcAlicePublic,
		AllowedIPs:          cidrs("0.0.0.0/0"),
		Endpoint:            "gw-2.mesh.example.com:51820",
		PersistentKeepalive: vpn.DefaultPersistentKeepalive,
	}
}

func golden(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read golden %s: %v", name, err)
	}
	return string(raw)
}

// TestRenderPeerConfigMatchesGolden is the contract with every WireGuard client.
// The golden files were written from the wg-quick format, not captured from the
// implementation, so a change to key order, separators or spacing fails here
// rather than silently producing a configuration a client cannot import.
func TestRenderPeerConfigMatchesGolden(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  vpn.PeerConfig
	}{
		{"peer.ini", splitTunnelConfig()},
		{"peer-full-tunnel.ini", fullTunnelConfig()},
	} {
		got, err := vpn.RenderPeerConfig(tc.cfg)
		if err != nil {
			t.Fatalf("%s: RenderPeerConfig: %v", tc.name, err)
		}
		want := golden(t, tc.name)
		if got != want {
			t.Errorf("%s: rendered configuration differs\n--- got ---\n%s\n--- want ---\n%s", tc.name, got, want)
		}
		if !strings.HasSuffix(got, "\n") {
			t.Errorf("%s: the configuration must end with a newline", tc.name)
		}
	}
}

// TestRenderedAllowedIPsMatchTheStoredEncoding ties the client configuration to
// the database. The AllowedIPs line is the canonical encoding verbatim, so an
// operator comparing a peer's row with the file they downloaded sees one text
// form rather than two that must be mentally translated.
func TestRenderedAllowedIPsMatchTheStoredEncoding(t *testing.T) {
	cfg := splitTunnelConfig()
	rendered, err := vpn.RenderPeerConfig(cfg)
	if err != nil {
		t.Fatalf("RenderPeerConfig: %v", err)
	}
	want := "AllowedIPs = " + vpn.EncodeAllowedIPs(cfg.AllowedIPs)
	if !strings.Contains(rendered, want+"\n") {
		t.Fatalf("rendered config does not contain %q:\n%s", want, rendered)
	}
}

// TestRenderInterfaceAndPeerSectionCompose pins the relationship between the
// whole-file renderer and its two halves, so a caller that needs only the
// interface block (for example to update an existing tunnel) cannot drift from
// the full render.
func TestRenderInterfaceAndPeerSectionCompose(t *testing.T) {
	cfg := splitTunnelConfig()
	whole, err := vpn.RenderPeerConfig(cfg)
	if err != nil {
		t.Fatalf("RenderPeerConfig: %v", err)
	}
	iface, err := vpn.RenderInterface(cfg)
	if err != nil {
		t.Fatalf("RenderInterface: %v", err)
	}
	peer, err := vpn.RenderPeerSection(cfg)
	if err != nil {
		t.Fatalf("RenderPeerSection: %v", err)
	}
	if !strings.HasPrefix(iface, "[Interface]\n") {
		t.Errorf("RenderInterface must start with the section header, got %q", iface)
	}
	if !strings.HasPrefix(peer, "[Peer]\n") {
		t.Errorf("RenderPeerSection must start with the section header, got %q", peer)
	}
	if want := iface + "\n" + peer; whole != want {
		t.Errorf("the two halves do not compose to the whole:\n--- got ---\n%s\n--- want ---\n%s", whole, want)
	}
	if strings.Contains(iface, "[Peer]") || strings.Contains(peer, "[Interface]") {
		t.Error("each half must render only its own section")
	}
}

func TestRenderPeerConfigIsDeterministic(t *testing.T) {
	cfg := splitTunnelConfig()
	first, err := vpn.RenderPeerConfig(cfg)
	if err != nil {
		t.Fatalf("RenderPeerConfig: %v", err)
	}
	for i := 0; i < 8; i++ {
		again, err := vpn.RenderPeerConfig(cfg)
		if err != nil {
			t.Fatalf("RenderPeerConfig: %v", err)
		}
		if again != first {
			t.Fatal("rendering the same configuration twice produced different bytes")
		}
	}
}

func TestRenderPeerConfigRejectsInjectionAttempts(t *testing.T) {
	// An ini value is terminated by a newline and a comment starts at ";" or
	// "#", so any of those characters in a rendered value would let one field
	// inject a second section. A malicious peer name or a crafted endpoint must
	// not be able to add "[Peer]" lines or comment out the AllowedIPs line.
	payloads := map[string]string{
		"newline":   "x\nPrivateKey = injected",
		"carriage":  "x\r\n[Peer]",
		"semicolon": "x; comment",
		"hash":      "x# comment",
		// Space-free variants, so a payload is rejected by the ";" and "#"
		// rules rather than incidentally by the space rule.
		"bare semicolon": "x;",
		"bare hash":      "x#",
		"mid semicolon":  "x;y",
		"mid hash":       "x#y",
		"space":          "x y",
		"tab":            "x\ty",
		"nul":            "x\x00y",
		"section header": "[Peer]",
	}
	for name, payload := range payloads {
		for field, mutate := range map[string]func(*vpn.PeerConfig, string){
			"PrivateKey":    func(c *vpn.PeerConfig, v string) { c.PrivateKey = v },
			"NodePublicKey": func(c *vpn.PeerConfig, v string) { c.NodePublicKey = v },
			"Endpoint":      func(c *vpn.PeerConfig, v string) { c.Endpoint = v },
		} {
			cfg := splitTunnelConfig()
			mutate(&cfg, payload)
			if _, err := vpn.RenderPeerConfig(cfg); err == nil {
				t.Errorf("%s in %s: rendering accepted an injectable value", name, field)
			}
		}
	}
	// The base64 padding character must stay legal: every 32-byte key ends in
	// "=" and rejecting it would make the renderer unusable.
	if _, err := vpn.RenderPeerConfig(splitTunnelConfig()); err != nil {
		t.Fatalf("a valid key with base64 padding was rejected: %v", err)
	}
}

func TestRenderPeerConfigRejectsInvalidInput(t *testing.T) {
	cases := map[string]func(*vpn.PeerConfig){
		"empty private key":       func(c *vpn.PeerConfig) { c.PrivateKey = "" },
		"malformed private key":   func(c *vpn.PeerConfig) { c.PrivateKey = "not-a-key" },
		"all zero private key":    func(c *vpn.PeerConfig) { c.PrivateKey = vpn.EncodeKey(make([]byte, 32)) },
		"empty node public key":   func(c *vpn.PeerConfig) { c.NodePublicKey = "" },
		"malformed node key":      func(c *vpn.PeerConfig) { c.NodePublicKey = "not-a-key" },
		"all zero node key":       func(c *vpn.PeerConfig) { c.NodePublicKey = vpn.EncodeKey(make([]byte, 32)) },
		"nil address":             func(c *vpn.PeerConfig) { c.Address = nil },
		"ipv6 address":            func(c *vpn.PeerConfig) { c.Address = net.ParseIP("fd00::1") },
		"mtu zero":                func(c *vpn.PeerConfig) { c.MTU = 0 },
		"mtu below the floor":     func(c *vpn.PeerConfig) { c.MTU = 575 },
		"mtu above the ceiling":   func(c *vpn.PeerConfig) { c.MTU = 1501 },
		"empty endpoint":          func(c *vpn.PeerConfig) { c.Endpoint = "" },
		"endpoint without a port": func(c *vpn.PeerConfig) { c.Endpoint = "gw-1.mesh.example.com" },
		"endpoint bad port":       func(c *vpn.PeerConfig) { c.Endpoint = "gw-1.mesh.example.com:http" },
		// A ";" or "#" survives net.SplitHostPort, so these two are the only
		// inputs where the ini value guard is the sole thing standing between a
		// crafted endpoint and a comment that swallows the rest of the file.
		"endpoint with semicolon": func(c *vpn.PeerConfig) { c.Endpoint = "gw;1.mesh.example.com:51820" },
		"endpoint with hash":      func(c *vpn.PeerConfig) { c.Endpoint = "gw#1.mesh.example.com:51820" },
		"endpoint with space":     func(c *vpn.PeerConfig) { c.Endpoint = "gw 1.mesh.example.com:51820" },
		"keepalive negative":      func(c *vpn.PeerConfig) { c.PersistentKeepalive = -1 },
		"keepalive too large":     func(c *vpn.PeerConfig) { c.PersistentKeepalive = 65536 },
		// A configuration with no allowed destination routes nothing into the
		// tunnel, so handing it to a user would produce a peer that looks
		// configured and cannot reach anything. Refusing to render is louder
		// than emitting a dead file.
		"empty allowed ips": func(c *vpn.PeerConfig) { c.AllowedIPs = nil },
	}
	for name, mutate := range cases {
		cfg := splitTunnelConfig()
		mutate(&cfg)
		_, err := vpn.RenderPeerConfig(cfg)
		if err == nil {
			t.Errorf("%s: RenderPeerConfig accepted invalid input", name)
			continue
		}
		if apiErr, ok := err.(*vpn.Error); ok && apiErr.Code != "vpn_peer_invalid" {
			t.Errorf("%s: got code %q, want vpn_peer_invalid", name, apiErr.Code)
		}
	}
}

func TestRenderPeerConfigAcceptsTheMTUBoundaries(t *testing.T) {
	for _, mtu := range []int{576, 1420, 1500} {
		cfg := splitTunnelConfig()
		cfg.MTU = mtu
		rendered, err := vpn.RenderPeerConfig(cfg)
		if err != nil {
			t.Fatalf("mtu %d: %v", mtu, err)
		}
		if !strings.Contains(rendered, "MTU = "+strconv.Itoa(mtu)+"\n") {
			t.Errorf("mtu %d was not rendered", mtu)
		}
	}
}

func TestDefaultPersistentKeepalive(t *testing.T) {
	if vpn.DefaultPersistentKeepalive != 25 {
		t.Fatalf("DefaultPersistentKeepalive = %d, want 25", vpn.DefaultPersistentKeepalive)
	}
	rendered, err := vpn.RenderPeerConfig(splitTunnelConfig())
	if err != nil {
		t.Fatalf("RenderPeerConfig: %v", err)
	}
	if !strings.Contains(rendered, "PersistentKeepalive = 25\n") {
		t.Errorf("the keepalive interval was not rendered:\n%s", rendered)
	}
}
