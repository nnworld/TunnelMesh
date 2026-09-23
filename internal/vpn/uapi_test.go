package vpn_test

import (
	"errors"
	"net"
	"strings"
	"testing"

	"github.com/tunnelmesh/tunnelmesh/internal/vpn"
)

// The configuration protocol is the wire format of a hot reload. The gateway
// sends these lines to wireguard-go while other peers are already connected, so
// a misspelled key, a missing terminator or a base64 key where hex is expected
// is not a cosmetic difference: the operation is rejected, or - worse - accepted
// with a meaning nobody intended. The golden files below were written from
// https://www.wireguard.com/xplatform/ and from wireguard-go's own
// device/uapi.go parser, not captured from this implementation.

// uapiNodeIdentity is the RFC 7748 §6.1 published vector, the same one
// testdata/peer.ini already carries in base64. It is a specification example and
// not a secret; the hex renderings are the RFC's own scalars and points.
var uapiNodeIdentity = vpn.NodeIdentity{
	PrivateKey: rfcAlicePrivate,
	PublicKey:  rfcAlicePublic,
}

const (
	uapiAlicePrivateHex = "77076d0a7318a57d3c16c17251b26645df4c2f87ebc0992ab177fba51db92c2a"
	uapiAlicePublicHex  = "8520f0098930a754748b7ddcb43ef75a0dbf3a0d26381af4eba4a98eaa9b4e6a"
	uapiBobPrivateHex   = "5dab087e624a8a4b79e17f8b83800ee66f3bb1292618b6fd1c2f8b27ff88e0eb"
	uapiBobPublicHex    = "de9edb7d7b7dc1b4d35b61c2ece435373f8343c85b78674dadfc7e146f882b4f"
	uapiPeerVPNIP       = "10.64.5.7"
)

func TestEncodeKeyHexMatchesRFC7748Vectors(t *testing.T) {
	for _, tc := range []struct{ base64, hex string }{
		{rfcAlicePrivate, uapiAlicePrivateHex},
		{rfcAlicePublic, uapiAlicePublicHex},
		{rfcBobPrivate, uapiBobPrivateHex},
		{rfcBobPublic, uapiBobPublicHex},
	} {
		got, err := vpn.EncodeKeyHex(tc.base64)
		if err != nil {
			t.Fatalf("EncodeKeyHex(%q): %v", tc.base64, err)
		}
		if got != tc.hex {
			t.Errorf("EncodeKeyHex(%q) = %s, want %s", tc.base64, got, tc.hex)
		}
		if len(got) != 64 {
			t.Errorf("EncodeKeyHex(%q) rendered %d characters, want 64", tc.base64, len(got))
		}
	}
	if _, err := vpn.EncodeKeyHex("not-a-key"); err == nil {
		t.Error("EncodeKeyHex accepted a value that is not a wireguard key")
	}
}

func TestRenderNodeUAPIMatchesGolden(t *testing.T) {
	got, err := vpn.RenderNodeUAPI(uapiNodeIdentity, 51820)
	if err != nil {
		t.Fatalf("RenderNodeUAPI: %v", err)
	}
	if want := golden(t, "uapi_node.txt"); got != want {
		t.Errorf("RenderNodeUAPI =\n%q\nwant\n%q", got, want)
	}
}

func TestRenderPeerUAPIMatchesGolden(t *testing.T) {
	got, err := vpn.RenderPeerUAPI(rfcBobPublic, net.ParseIP(uapiPeerVPNIP))
	if err != nil {
		t.Fatalf("RenderPeerUAPI: %v", err)
	}
	if want := golden(t, "uapi_peer.txt"); got != want {
		t.Errorf("RenderPeerUAPI =\n%q\nwant\n%q", got, want)
	}
}

func TestRenderPeerRemoveUAPIMatchesGolden(t *testing.T) {
	got, err := vpn.RenderPeerRemoveUAPI(rfcBobPublic)
	if err != nil {
		t.Fatalf("RenderPeerRemoveUAPI: %v", err)
	}
	if want := golden(t, "uapi_peer_remove.txt"); got != want {
		t.Errorf("RenderPeerRemoveUAPI =\n%q\nwant\n%q", got, want)
	}
}

// TestRenderUAPIBlocksAreTerminated pins the blank line that ends a set
// operation. wireguard-go's parser returns on the first empty line, so a block
// without one is only completed by the end of the stream; relying on that would
// make the renderings unusable as parts of a larger operation and would turn a
// truncated write into a half-applied configuration with no error.
func TestRenderUAPIBlocksAreTerminated(t *testing.T) {
	node, err := vpn.RenderNodeUAPI(uapiNodeIdentity, 51820)
	if err != nil {
		t.Fatalf("RenderNodeUAPI: %v", err)
	}
	peer, err := vpn.RenderPeerUAPI(rfcBobPublic, net.ParseIP(uapiPeerVPNIP))
	if err != nil {
		t.Fatalf("RenderPeerUAPI: %v", err)
	}
	remove, err := vpn.RenderPeerRemoveUAPI(rfcBobPublic)
	if err != nil {
		t.Fatalf("RenderPeerRemoveUAPI: %v", err)
	}
	for name, block := range map[string]string{"node": node, "peer": peer, "remove": remove} {
		if !strings.HasSuffix(block, "\n\n") {
			t.Errorf("the %s block does not end with a blank line: %q", name, block)
		}
		if strings.Contains(strings.TrimSuffix(block, "\n"), "\n\n") {
			t.Errorf("the %s block has an interior blank line, which would end the operation early: %q", name, block)
		}
		for _, line := range strings.Split(strings.TrimSuffix(block, "\n"), "\n") {
			if line == "" {
				continue
			}
			key, value, ok := strings.Cut(line, "=")
			if !ok {
				t.Errorf("the %s block has a line without a key=value shape: %q", name, line)
				continue
			}
			if strings.TrimSpace(key) != key || strings.TrimSpace(value) != value {
				t.Errorf("the %s block pads a key or a value: %q", name, line)
			}
		}
	}
}

// TestRenderPeerUAPIReplacesBeforeItAdds pins the one ordering rule the protocol
// makes load-bearing. wireguard-go removes every allowed_ip of a peer at the
// moment it reads replace_allowed_ips=true, not at the end of the block, so a
// flag rendered after the address would delete the address that was just added
// and leave a peer that can handshake and carry nothing.
func TestRenderPeerUAPIReplacesBeforeItAdds(t *testing.T) {
	block, err := vpn.RenderPeerUAPI(rfcBobPublic, net.ParseIP(uapiPeerVPNIP))
	if err != nil {
		t.Fatalf("RenderPeerUAPI: %v", err)
	}
	lines := strings.Split(strings.TrimSuffix(block, "\n"), "\n")
	indexOf := func(prefix string) int {
		for i, line := range lines {
			if strings.HasPrefix(line, prefix) {
				return i
			}
		}
		return -1
	}
	public, replace, allowed := indexOf("public_key="), indexOf("replace_allowed_ips="), indexOf("allowed_ip=")
	if public != 0 {
		t.Fatalf("public_key= is at line %d, but it is what starts the block", public)
	}
	if replace <= public || allowed <= replace {
		t.Fatalf("line order is public_key=%d replace_allowed_ips=%d allowed_ip=%d, want strictly increasing", public, replace, allowed)
	}
	if got := lines[replace]; got != "replace_allowed_ips=true" {
		t.Errorf("the replace line is %q, want the literal the parser accepts", got)
	}
	if got := lines[allowed]; got != "allowed_ip="+uapiPeerVPNIP+"/32" {
		t.Errorf("the allowed_ip line is %q, want a single /32 for the peer address", got)
	}
}

// TestRenderPeerUAPICarriesNoSecret asserts what a hot-reload block may contain.
// The gateway's own private key and any preshared key are device-wide state set
// once at startup; repeating them per peer would multiply the number of log
// lines, error messages and memory copies that hold a secret, for no benefit.
func TestRenderPeerUAPICarriesNoSecret(t *testing.T) {
	for name, block := range map[string]string{
		"peer":   mustRenderPeer(t, rfcBobPublic, net.ParseIP(uapiPeerVPNIP)),
		"remove": mustRenderRemove(t, rfcBobPublic),
	} {
		for _, forbidden := range []string{"private_key", "preshared_key", "listen_port", uapiAlicePrivateHex, rfcAlicePrivate} {
			if strings.Contains(block, forbidden) {
				t.Errorf("the %s block mentions %q:\n%s", name, forbidden, block)
			}
		}
		if !strings.Contains(block, uapiBobPublicHex) {
			t.Errorf("the %s block does not name the peer it configures:\n%s", name, block)
		}
		if strings.Contains(block, rfcBobPublic) {
			t.Errorf("the %s block carries a base64 key, which the protocol cannot parse:\n%s", name, block)
		}
	}
}

// TestRenderUAPIRefusesUnusableInput covers the shapes that must never reach the
// socket. Sending them would produce an IPCError whose text names a protocol
// field rather than the deployment setting that is wrong.
func TestRenderUAPIRefusesUnusableInput(t *testing.T) {
	t.Run("node identity", func(t *testing.T) {
		for name, identity := range map[string]vpn.NodeIdentity{
			"empty":          {},
			"all-zero":       {PrivateKey: strings.Repeat("A", 43) + "="},
			"not-a-key":      {PrivateKey: "obviously-not-a-key"},
			"public-only":    {PublicKey: rfcAlicePublic},
			"mismatched-hex": {PrivateKey: "7" + uapiAlicePrivateHex[1:]},
		} {
			if _, err := vpn.RenderNodeUAPI(identity, 51820); err == nil {
				t.Errorf("%s: RenderNodeUAPI accepted an unusable identity", name)
			} else if !errors.Is(err, vpn.ErrNodeKeyInvalid) {
				// The remedy for a bad node identity is "inject the secret
				// again", not "this peer is invalid", so the error must name the
				// environment variable the identity comes from.
				t.Errorf("%s: error = %v, want it to wrap ErrNodeKeyInvalid", name, err)
			} else if !strings.Contains(err.Error(), vpn.NodePrivateKeyEnv) {
				t.Errorf("%s: error %q does not name %s", name, err, vpn.NodePrivateKeyEnv)
			}
		}
		// The public half is derived, never trusted: an operator who pasted a
		// mismatched pair must still get a configuration that works.
		block, err := vpn.RenderNodeUAPI(vpn.NodeIdentity{PrivateKey: rfcAlicePrivate, PublicKey: rfcBobPublic}, 51820)
		if err != nil {
			t.Fatalf("RenderNodeUAPI with a mismatched public key: %v", err)
		}
		if !strings.Contains(block, uapiAlicePrivateHex) {
			t.Errorf("the node block does not carry the identity's own key:\n%s", block)
		}
		if strings.Contains(block, uapiBobPublicHex) {
			t.Errorf("the node block rendered a supplied public key, which the device line has no use for:\n%s", block)
		}
	})

	t.Run("listen port", func(t *testing.T) {
		for _, port := range []int{0, -1, 65536, 1 << 20} {
			if _, err := vpn.RenderNodeUAPI(uapiNodeIdentity, port); err == nil {
				t.Errorf("listen_port %d was accepted", port)
			}
		}
		for _, port := range []int{1, 51820, 65535} {
			if _, err := vpn.RenderNodeUAPI(uapiNodeIdentity, port); err != nil {
				t.Errorf("listen_port %d was refused: %v", port, err)
			}
		}
	})

	t.Run("peer public key", func(t *testing.T) {
		for name, key := range map[string]string{
			"empty":     "",
			"not-a-key": "obviously-not-a-wireguard-public-key",
			"all-zero":  strings.Repeat("A", 43) + "=",
			"hex":       uapiBobPublicHex,
		} {
			_, err := vpn.RenderPeerUAPI(key, net.ParseIP(uapiPeerVPNIP))
			if err == nil {
				t.Errorf("%s: RenderPeerUAPI accepted %q", name, key)
				continue
			}
			var apiErr *vpn.Error
			if !errors.As(err, &apiErr) || apiErr.Code != vpn.ErrPeerInvalid.Code {
				t.Errorf("%s: error = %v, want the stable %s code", name, err, vpn.ErrPeerInvalid.Code)
			}
			if strings.Contains(err.Error(), key) && key != "" {
				t.Errorf("%s: the error echoes the supplied key: %v", name, err)
			}
			if _, err := vpn.RenderPeerRemoveUAPI(key); err == nil {
				t.Errorf("%s: RenderPeerRemoveUAPI accepted %q", name, key)
			}
		}
	})

	t.Run("peer address", func(t *testing.T) {
		for name, address := range map[string]net.IP{
			"nil":   nil,
			"ipv6":  net.ParseIP("2001:db8::1"),
			"empty": net.IP{},
		} {
			if _, err := vpn.RenderPeerUAPI(rfcBobPublic, address); err == nil {
				t.Errorf("%s: RenderPeerUAPI accepted the address %v", name, address)
			}
		}
		// An IPv4-mapped IPv6 address is the same host, and net.IP renders it
		// both ways depending on how it was parsed, so it must be accepted and
		// normalised rather than refused.
		block, err := vpn.RenderPeerUAPI(rfcBobPublic, net.ParseIP("::ffff:"+uapiPeerVPNIP))
		if err != nil {
			t.Fatalf("RenderPeerUAPI with an ipv4-mapped address: %v", err)
		}
		if !strings.Contains(block, "allowed_ip="+uapiPeerVPNIP+"/32") {
			t.Errorf("the ipv4-mapped address was not normalised:\n%s", block)
		}
	})
}

func mustRenderPeer(t *testing.T, publicKey string, address net.IP) string {
	t.Helper()
	block, err := vpn.RenderPeerUAPI(publicKey, address)
	if err != nil {
		t.Fatalf("RenderPeerUAPI: %v", err)
	}
	return block
}

func mustRenderRemove(t *testing.T, publicKey string) string {
	t.Helper()
	block, err := vpn.RenderPeerRemoveUAPI(publicKey)
	if err != nil {
		t.Fatalf("RenderPeerRemoveUAPI: %v", err)
	}
	return block
}
