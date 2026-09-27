package vpn

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"unicode"
)

const (
	// DefaultPersistentKeepalive is the interval WireGuard clients use to keep a
	// NAT mapping alive. 25 seconds is the conventional value: it is shorter than
	// the timeout of almost every consumer and carrier NAT, and infrequent
	// enough not to matter for battery or bandwidth.
	DefaultPersistentKeepalive = 25

	// MinTunnelMTU and MaxTunnelMTU bound server.vpn.mtu. 576 is the minimum
	// IPv4 MTU every host must accept (RFC 791); 1500 is standard Ethernet, and
	// a WireGuard tunnel above that cannot be carried by the underlying link
	// without fragmentation, which the packet policy drops anyway.
	MinTunnelMTU = 576
	MaxTunnelMTU = 1500

	// peerAddressBits is the prefix length written into a client Address line.
	// A peer owns exactly one /32; the route to the rest of the tunnel comes from
	// the AllowedIPs line instead.
	peerAddressBits = 32
)

// PeerConfig is everything needed to render one peer's client configuration.
//
// AllowedIPs here is the peer's egress policy: the set of destinations traffic
// should be routed into the tunnel for. It is the same set PacketPolicy enforces
// on the server, and it is rendered through EncodeAllowedIPs so the downloaded
// file and the stored column are byte-identical rather than two spellings of one
// policy that must be mentally translated when debugging.
//
// This is the client-side meaning of AllowedIPs. The server-side WireGuard
// configuration uses the same key for the opposite direction, where it lists the
// source addresses a peer may send from, which is always that peer's single /32.
// The two are not interchangeable and only this one is rendered here.
type PeerConfig struct {
	PrivateKey          string
	Address             net.IP
	MTU                 int
	NodePublicKey       string
	AllowedIPs          []*net.IPNet
	Endpoint            string
	PersistentKeepalive int
}

// RenderPeerConfig renders a complete wg-quick compatible configuration file.
//
// It refuses to render a configuration that cannot work, rather than emitting a
// file the user would import and then debug: an empty egress policy routes
// nothing into the tunnel, and a malformed key or endpoint produces a client
// error message that does not point back at the field that is wrong.
func RenderPeerConfig(cfg PeerConfig) (string, error) {
	iface, err := RenderInterface(cfg)
	if err != nil {
		return "", err
	}
	peer, err := RenderPeerSection(cfg)
	if err != nil {
		return "", err
	}
	// The blank line between sections is what makes the file readable in a
	// terminal and what wg-quick's own output looks like.
	return iface + "\n" + peer, nil
}

// RenderInterface renders the [Interface] half: the identity and address this
// peer uses on the tunnel.
func RenderInterface(cfg PeerConfig) (string, error) {
	if err := cfg.validate(); err != nil {
		return "", err
	}
	address := (&net.IPNet{
		IP:   cfg.Address.To4(),
		Mask: net.CIDRMask(peerAddressBits, ipv4Bits),
	}).String()
	lines := []string{"[Interface]"}
	for _, field := range []struct{ key, value string }{
		{"PrivateKey", cfg.PrivateKey},
		{"Address", address},
		{"MTU", strconv.Itoa(cfg.MTU)},
	} {
		line, err := iniLine(field.key, field.value)
		if err != nil {
			return "", err
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n") + "\n", nil
}

// RenderPeerSection renders the [Peer] half: the gateway this peer connects to
// and the traffic it should send there.
func RenderPeerSection(cfg PeerConfig) (string, error) {
	if err := cfg.validate(); err != nil {
		return "", err
	}
	allowedIPs := EncodeAllowedIPs(cfg.AllowedIPs)
	if allowedIPs == "" {
		return "", peerInvalid("a peer configuration with no allowed destination would route nothing into the tunnel")
	}
	lines := []string{"[Peer]"}
	for _, field := range []struct{ key, value string }{
		{"PublicKey", cfg.NodePublicKey},
		{"AllowedIPs", allowedIPs},
		{"Endpoint", cfg.Endpoint},
		{"PersistentKeepalive", strconv.Itoa(cfg.PersistentKeepalive)},
	} {
		line, err := iniLine(field.key, field.value)
		if err != nil {
			return "", err
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n") + "\n", nil
}

// validate checks the fields the renderers interpolate. It runs for both halves
// so neither can be used on its own to produce a partial file that skips a
// check.
func (c PeerConfig) validate() error {
	if err := ValidatePrivateKey(c.PrivateKey); err != nil {
		return err
	}
	if err := ValidatePublicKey(c.NodePublicKey); err != nil {
		return err
	}
	if c.Address == nil || c.Address.To4() == nil {
		return peerInvalid("peer address is required and must be ipv4")
	}
	if c.MTU < MinTunnelMTU || c.MTU > MaxTunnelMTU {
		return peerInvalid(fmt.Sprintf("mtu %d must be between %d and %d", c.MTU, MinTunnelMTU, MaxTunnelMTU))
	}
	if c.PersistentKeepalive < 0 || c.PersistentKeepalive > maxPort {
		return peerInvalid(fmt.Sprintf("persistent keepalive %d must be between 0 and %d", c.PersistentKeepalive, maxPort))
	}
	trimmed := strings.TrimSpace(c.Endpoint)
	if trimmed == "" {
		return peerInvalid("endpoint is required")
	}
	host, port, err := net.SplitHostPort(trimmed)
	if err != nil {
		return peerInvalid(fmt.Sprintf("endpoint %q must be a host:port pair: %v", c.Endpoint, err))
	}
	if strings.TrimSpace(host) == "" {
		return peerInvalid("endpoint host must not be empty")
	}
	if value, err := strconv.Atoi(port); err != nil || value < minPort || value > maxPort {
		return peerInvalid(fmt.Sprintf("endpoint port %q must be between %d and %d", port, minPort, maxPort))
	}
	for _, network := range c.AllowedIPs {
		if network == nil {
			return peerInvalid("allowed_ips contains an empty entry")
		}
		if _, bits := network.Mask.Size(); bits != ipv4Bits || network.IP.To4() == nil {
			return peerInvalid(fmt.Sprintf("allowed_ips entry %s must be an ipv4 cidr", network))
		}
	}
	return nil
}

// iniLine renders one "key = value" line after checking the value cannot escape
// its own line.
//
// The check is defence in depth. Every field is already validated above, but the
// ini format terminates a value at a newline and starts a comment at ";" or "#",
// so any future field rendered without this guard would let one crafted value
// append a second "[Peer]" section or comment out the AllowedIPs line. Rejecting
// is cheap and the alternative is a configuration-injection hole.
//
// Base64 padding is deliberately allowed: every 32-byte key ends in "=" and
// keys legitimately contain "+" and "/".
func iniLine(key, value string) (string, error) {
	if value == "" {
		return "", peerInvalid(fmt.Sprintf("%s must not be empty", key))
	}
	for _, r := range value {
		if unicode.IsControl(r) || r == ';' || r == '#' || r == ' ' {
			return "", peerInvalid(fmt.Sprintf("%s contains a character that cannot appear in a wireguard configuration", key))
		}
	}
	return key + " = " + value, nil
}
