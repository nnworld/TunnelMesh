package vpn

import (
	"encoding/hex"
	"fmt"
	"net"
	"strconv"
	"strings"
)

// Key names of the WireGuard configuration protocol, as specified at
// https://www.wireguard.com/xplatform/ and parsed by wireguard-go's
// device.IpcSetOperation.
//
// They are constants rather than literals scattered through the renderers
// because the protocol has no schema check on the other end: an unrecognised key
// is an IPCError that names the key but not the caller, and a key that is almost
// right is silently ignored by nothing at all - it simply fails.
const (
	uapiPrivateKey        = "private_key"
	uapiListenPort        = "listen_port"
	uapiPublicKey         = "public_key"
	uapiReplaceAllowedIPs = "replace_allowed_ips"
	uapiAllowedIP         = "allowed_ip"
	uapiRemove            = "remove"
	uapiTrue              = "true"

	// uapiTerminator is the empty line that ends one set operation. Every block
	// this file renders carries its own, so a block is a complete operation on
	// its own and two blocks cannot be concatenated into an operation that stops
	// at the first of them by accident.
	uapiTerminator = ""

	// uapiSeparator joins a key to its value. It is a constant so a renderer
	// cannot drift into "key: value", which the parser reports as a protocol
	// error rather than ignoring.
	uapiSeparator = "="
)

// EncodeKeyHex renders a canonical base64 WireGuard key as the lowercase hex
// form the configuration protocol requires.
//
// The two spellings exist because the two audiences differ: the ini file a user
// imports and every column this product stores use base64, while the UAPI socket
// wireguard-go listens on uses hex (device.NoisePublicKey.FromHex). Converting in
// one place is what stops a caller from passing base64 to the socket and reading
// back "failed to get peer by public key" with no idea which encoding was wrong.
//
// The output is lowercase to match what IpcGetOperation writes, so a block this
// package renders can be compared byte for byte against one read back from the
// device.
func EncodeKeyHex(encoded string) (string, error) {
	raw, err := decodeKey(encoded)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

// RenderNodeUAPI renders the device half of the configuration: this gateway's own
// identity and the UDP port it answers handshakes on.
//
// It is a separate operation from the peer blocks for two reasons the protocol
// forces. Every line before the first public_key= is a device line, so identity
// and peers cannot be interleaved; and listen_port= calls BindUpdate, which
// re-creates the socket, so re-sending it with every peer would drop the handshake
// state of every other peer on the gateway to refresh one of them.
//
// The public half of the identity is not rendered: the device derives it from the
// private scalar and has no key for it. That is also why a mismatched pair in the
// supplied identity is harmless here - PublicKey is documentation for callers, and
// DerivePublicKey is what every peer configuration uses.
func RenderNodeUAPI(identity NodeIdentity, listenPort int) (string, error) {
	if err := ValidatePrivateKey(identity.PrivateKey); err != nil {
		return "", nodeKeyInvalid(err)
	}
	if listenPort < minPort || listenPort > maxPort {
		return "", fmt.Errorf("vpn: %s %d must be between %d and %d", uapiListenPort, listenPort, minPort, maxPort)
	}
	privateKey, err := EncodeKeyHex(identity.PrivateKey)
	if err != nil {
		return "", nodeKeyInvalid(err)
	}
	return uapiBlock(
		uapiLine(uapiPrivateKey, privateKey),
		uapiLine(uapiListenPort, strconv.Itoa(listenPort)),
	), nil
}

// RenderNodeIdentityUAPI renders only this gateway's own key and leaves the
// listen port to the bind.
//
// It exists for the one configuration RenderNodeUAPI cannot express, and the two
// readings of listen_port=0 are why. wg(8) documents 0 as "stop listening", while
// wireguard-go's BindUpdate passes it straight to the bind, which asks the
// operating system to choose a port. A gateway whose server.vpn.listen names port
// 0 wants the second meaning, and omitting the line is the only spelling both
// agree on: the device keeps the port it was constructed with, the bind supplies
// its own, and the caller reads the chosen port back off the bind.
//
// A deployment that serves real peers must not use it. The port a client dials is
// the port server.vpn.listen names, and a port chosen at startup cannot be written
// into a configuration file that was rendered before the process began.
func RenderNodeIdentityUAPI(identity NodeIdentity) (string, error) {
	if err := ValidatePrivateKey(identity.PrivateKey); err != nil {
		return "", nodeKeyInvalid(err)
	}
	privateKey, err := EncodeKeyHex(identity.PrivateKey)
	if err != nil {
		return "", nodeKeyInvalid(err)
	}
	return uapiBlock(uapiLine(uapiPrivateKey, privateKey)), nil
}

// RenderPeerUAPI renders one peer block that creates or updates a single peer.
//
// It is the hot-reload unit: the gateway sends it after a peer row has been
// committed, so one issue, one patch and one rotation each produce exactly one
// block and no other peer's handshake is touched. That is why replace_peers=true
// is never rendered - it would tear down every peer on the device to change one.
//
// replace_allowed_ips=true precedes allowed_ip= because the parser acts on the
// flag when it reads it, not at the end of the block. Reversing the two would
// delete the address just added and leave a peer that completes a handshake and
// then carries nothing, which is the hardest possible failure to diagnose from
// the user's side of the tunnel.
//
// The allowed set is always the peer's own /32. It is the server-side meaning of
// allowed_ip - the source addresses this peer may send from - and it is the
// opposite of the egress policy rendered into the client's own configuration file,
// which shares the key name and nothing else.
func RenderPeerUAPI(publicKey string, vpnIP net.IP) (string, error) {
	peerKey, err := peerKeyHex(publicKey)
	if err != nil {
		return "", err
	}
	address, err := peerAddress(vpnIP)
	if err != nil {
		return "", err
	}
	return uapiBlock(
		uapiLine(uapiPublicKey, peerKey),
		uapiLine(uapiReplaceAllowedIPs, uapiTrue),
		uapiLine(uapiAllowedIP, address),
	), nil
}

// RenderPeerRemoveUAPI renders one block that deletes a single peer.
//
// remove=true is the only instruction the device needs beyond the identity: the
// parser drops the peer when it reads the flag and treats every later line in the
// block as a no-op, so the block stays two lines long no matter what else is
// known about the peer. Removing by public key is also what makes revocation safe
// against reuse - the peer is identified by the key it authenticates with, not by
// an address that could later be handed to somebody else.
func RenderPeerRemoveUAPI(publicKey string) (string, error) {
	peerKey, err := peerKeyHex(publicKey)
	if err != nil {
		return "", err
	}
	return uapiBlock(
		uapiLine(uapiPublicKey, peerKey),
		uapiLine(uapiRemove, uapiTrue),
	), nil
}

// peerKeyHex validates a peer public key and renders it for the socket.
//
// Validation runs first so the caller gets the stable vpn_peer_invalid code with
// a reason that describes the shape of the problem. The message never carries the
// supplied value: these errors end up in a management-API response and in an audit
// entry, and a key is exactly the kind of text that gets pasted into a ticket.
func peerKeyHex(publicKey string) (string, error) {
	if err := ValidatePublicKey(publicKey); err != nil {
		return "", err
	}
	encoded, err := EncodeKeyHex(publicKey)
	if err != nil {
		return "", peerInvalid(reasonOf(err))
	}
	return encoded, nil
}

// peerAddress normalises the tunnel address into the CIDR text the protocol
// expects. IPv4-only is a property of the whole product - the address pool, the
// egress policy and the packet parser are all IPv4 - so an IPv6 address here is a
// bug in a caller rather than a request to reject politely.
func peerAddress(vpnIP net.IP) (string, error) {
	address := vpnIP.To4()
	if len(address) != net.IPv4len {
		return "", peerInvalid("vpn address is required and must be ipv4")
	}
	return (&net.IPNet{
		IP:   address,
		Mask: net.CIDRMask(peerAddressBits, ipv4Bits),
	}).String(), nil
}

// nodeKeyInvalid reports an unusable node identity as a startup fault naming the
// environment variable the identity comes from.
//
// It deliberately does not reuse ErrPeerInvalid, which is the management-API code
// for a field a caller supplied in a request body. The node key arrives from the
// deployment, and an operator who reads "vpn peer is invalid" while configuring a
// gateway looks in the wrong place entirely.
func nodeKeyInvalid(err error) error {
	return fmt.Errorf("%w: %s", ErrNodeKeyInvalid, reasonOf(err))
}

// uapiLine joins one key to one value.
func uapiLine(key, value string) string {
	return key + uapiSeparator + value
}

// uapiBlock renders lines into one terminated set operation.
func uapiBlock(lines ...string) string {
	return strings.Join(append(lines, uapiTerminator), "\n") + "\n"
}
