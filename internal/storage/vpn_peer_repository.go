package storage

import (
	"errors"
	"strings"
)

// validateVPNPeer enforces the invariants the DDL cannot express: the identity
// fields the gateway needs to build a WireGuard configuration, a status from the
// closed set, and non-negative resource caps. It runs before every write so a
// bad record fails at the boundary instead of producing an unroutable peer.
func validateVPNPeer(peer VPNPeer) error {
	switch {
	case strings.TrimSpace(peer.OwnerID) == "":
		return errors.New("vpn peer owner is required")
	case strings.TrimSpace(peer.Name) == "":
		return errors.New("vpn peer name is required")
	case strings.TrimSpace(peer.PublicKey) == "":
		return errors.New("vpn peer public key is required")
	case strings.TrimSpace(peer.VPNIP) == "":
		return errors.New("vpn peer address is required")
	case strings.TrimSpace(peer.NodeID) == "":
		return errors.New("vpn peer node is required")
	case strings.TrimSpace(peer.AgentID) == "":
		return errors.New("vpn peer agent is required")
	}
	switch peer.Status {
	case VPNPeerStatusActive, VPNPeerStatusDisabled, VPNPeerStatusRevoked:
	case "":
		return errors.New("vpn peer status is required")
	default:
		return errors.New("unsupported vpn peer status " + string(peer.Status))
	}
	// The sealed private key is all-or-nothing. AES-GCM ciphertext without its
	// nonce can never be opened, so persisting half of it would create a peer
	// whose configuration is unrecoverable. The key id stays optional because
	// TUNNELMESH_TOKEN_ENCRYPTION_KEY_ID is optional by design.
	if (peer.PrivateKeyCiphertext == "") != (peer.PrivateKeyNonce == "") {
		return errors.New("vpn peer private key ciphertext and nonce must be set together")
	}
	if peer.MaxConcurrentFlows < 0 {
		return errors.New("vpn peer max concurrent flows must not be negative")
	}
	if peer.PacketRateLimit < 0 {
		return errors.New("vpn peer packet rate limit must not be negative")
	}
	return nil
}
