package server

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/auth"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
	"github.com/tunnelmesh/tunnelmesh/internal/vpn"
)

const (
	// vpnPeerResourceType is the audit resource_type of every peer lifecycle
	// event. It is singular, matching "credential" and "agent" in the existing
	// audit trail, so one filter value retrieves one entity kind.
	vpnPeerResourceType = "vpn_peer"

	vpnDefaultListLimit = 50
	vpnMaxListLimit     = 500
)

// Audit actions of the peer lifecycle. They are part of the observable contract:
// the admin console filters the audit trail by these strings.
const (
	vpnAuditIssued   = "vpn_peer_issued"
	vpnAuditUpdated  = "vpn_peer_updated"
	vpnAuditRotated  = "vpn_peer_rotated"
	vpnAuditRevoked  = "vpn_peer_revoked"
	vpnAuditRevealed = "vpn_peer_config_revealed"
)

// VPNServiceConfig is the subset of config.VPNConfig the management plane needs.
//
// It is a separate type rather than a direct dependency on the loader so the
// service can be assembled by a test, by an embedder, or by a future control
// plane that reads its gateway settings from somewhere else. The data-plane
// fields (flow caps, timeouts, ICMP tuning) are deliberately absent: nothing in
// this file may consult them, which is what keeps the management plane honest
// about the fact that it does not yet forward a single packet.
type VPNServiceConfig struct {
	// Enabled mirrors server.vpn.enabled. A disabled node owns no VPN
	// resources, so writes are refused and reads report nothing.
	Enabled bool
	// IPPool and NodeSubnetSize are re-parsed on every allocation through
	// vpn.ParsePool, the same validator the configuration loader uses, so the
	// loader and the allocator can never disagree about a usable pool.
	IPPool         string
	NodeSubnetSize int
	// EndpointHost is the bare DNS name and Listen the host:port the gateway
	// binds. The peer configuration is rendered from both so the port a user
	// imports always comes from the same place the process listens on.
	EndpointHost string
	Listen       string
	MTU          int
	// MaxPeers is the node-level quota. Zero means unlimited, matching the
	// convention of max_concurrent_tunnels.
	MaxPeers int
	// ICMPEnabled is the node-level ceiling for echo handling. Phase 4 refuses
	// every per-peer ICMP request regardless of this switch, because the agent
	// capability it depends on is negotiated in a later phase; the field exists
	// so the assembly code has one place to copy the configuration into.
	ICMPEnabled bool
}

// VPNPeerServiceDeps carries everything the service is allowed to touch. The
// repository interfaces rather than a *storage.DB keep the persistence boundary
// explicit, and Now keeps every timestamp and expiry decision on one clock.
type VPNPeerServiceDeps struct {
	Peers  storage.VPNPeerRepository
	Leases storage.VPNIPLeaseRepository
	Agents storage.AgentRepository
	Audits storage.AuditRepository
	// Secrets seals peer private keys. It is injected rather than read from the
	// environment here so a missing TUNNELMESH_TOKEN_ENCRYPTION_KEY is the
	// caller's decision to make; assembly passes envSecretStore(). A nil store
	// fails closed with 503 credential_secret_unavailable and never degrades to
	// plaintext.
	Secrets *auth.SecretStore
	Config  VPNServiceConfig
	// NodeID is this server node's identity. It is both the vpn_peers.node_id
	// written into every peer and the lease holder of the node's subnet, so a
	// restarted process inherits its own lease and keeps handing out addresses
	// from the subnet its existing peers already live in.
	NodeID string
	// LeaseTTL bounds how long the subnet lease survives without a renewal.
	LeaseTTL time.Duration
	// NodePublicKey is the gateway's WireGuard identity, written into the
	// [Peer] section of every configuration handed to a user.
	NodePublicKey string
	// AgentCapabilities decides whether an egress agent can answer ICMP echo.
	// A nil probe fails closed: "not wired" is a deployment fault, and issuing a
	// peer that cannot ping is a promise the user will hold the product to.
	AgentCapabilities VPNAgentCapabilityProbe
	Now               func() time.Time
}

// VPNPeerInput is one issue request. AllowedIPs arrives as text because that is
// what a JSON body carries; parsing it through vpn.ParseAllowedIPList is what
// makes the stored column canonical.
type VPNPeerInput struct {
	Name                string
	Description         string
	AgentID             string
	AllowedIPs          []string
	AllowedPorts        []int
	AllowPrivateTargets bool
	ICMPEnabled         bool
	MaxConcurrentFlows  int
	PacketRateLimit     int
	ExpiresAt           *time.Time
}

// VPNPeerPatch is a partial update. Pointer fields distinguish "absent" from
// "set to the zero value"; the two collection fields need an explicit presence
// flag because an empty list is a meaningful value (no reachable destination,
// and no port restriction) rather than an omission.
//
// Status only accepts active and disabled. Revocation has its own endpoint, so
// that one terminal transition cannot be reached through two paths where one of
// them would skip the audit action the other writes.
type VPNPeerPatch struct {
	Name                *string
	Description         *string
	AgentID             *string
	HasAllowedIPs       bool
	AllowedIPs          []string
	HasAllowedPorts     bool
	AllowedPorts        []int
	AllowPrivateTargets *bool
	ICMPEnabled         *bool
	MaxConcurrentFlows  *int
	PacketRateLimit     *int
	HasExpiresAt        bool
	ExpiresAt           *time.Time
	Status              *storage.VPNPeerStatus
}

// VPNPeerService orchestrates the peer lifecycle: issue, read, list, patch,
// rotate, revoke and reveal.
//
// It is the only place that maps between the persistence row (storage.VPNPeer,
// 22 columns, opaque policy text, sealed key material) and the domain spec
// (vpn.PeerSpec, parsed CIDRs and ports). Keeping that mapping in one file is
// what allows internal/vpn to stay a pure package with no storage import.
type VPNPeerService struct {
	peers         storage.VPNPeerRepository
	leases        storage.VPNIPLeaseRepository
	agents        storage.AgentRepository
	audits        storage.AuditRepository
	secrets       *auth.SecretStore
	config        VPNServiceConfig
	nodeID        string
	leaseTTL      time.Duration
	nodePublicKey string
	capabilities  VPNAgentCapabilityProbe
	nowFn         func() time.Time
}

// NewVPNPeerService assembles the service. It performs no I/O, so a disabled
// node costs nothing beyond the struct itself.
func NewVPNPeerService(deps VPNPeerServiceDeps) *VPNPeerService {
	return &VPNPeerService{
		peers:         deps.Peers,
		leases:        deps.Leases,
		agents:        deps.Agents,
		audits:        deps.Audits,
		secrets:       deps.Secrets,
		config:        deps.Config,
		nodeID:        strings.TrimSpace(deps.NodeID),
		leaseTTL:      deps.LeaseTTL,
		nodePublicKey: strings.TrimSpace(deps.NodePublicKey),
		capabilities:  deps.AgentCapabilities,
		nowFn:         deps.Now,
	}
}

// Issue creates one peer: it validates the request, authorizes the egress agent,
// checks the node quota, leases the node's subnet, allocates a /32, generates
// and seals a key pair, persists the row and writes the audit entry.
//
// The order is not arbitrary. The agent and the quota are checked before
// anything is allocated so a rejected request leaves no trace; the subnet lease
// precedes the address because two nodes must never believe they own the same
// /24; the key pair is generated after allocation so a request that cannot get
// an address never creates key material; and the derived counter is incremented
// immediately before the insert, which is the only step that can still fail and
// therefore the only one that needs a rollback.
func (s *VPNPeerService) Issue(ctx context.Context, actor auth.Principal, input VPNPeerInput) (storage.VPNPeer, error) {
	if !s.config.Enabled {
		return storage.VPNPeer{}, vpn.ErrNodeDisabled
	}
	if s.secrets == nil {
		// Checked before any allocation: an issue that cannot seal a key must
		// not consume an address on its way to failing.
		return storage.VPNPeer{}, vpn.ErrSecretUnavailable
	}
	if err := s.requireNodeID(); err != nil {
		return storage.VPNPeer{}, err
	}
	spec, err := s.specFromInput(input)
	if err != nil {
		return storage.VPNPeer{}, err
	}
	if err := s.authorizeAgent(ctx, actor, spec.AgentID); err != nil {
		return storage.VPNPeer{}, err
	}
	icmpState, err := s.authorizeICMP(ctx, spec.AgentID, spec.ICMPEnabled)
	if err != nil {
		return storage.VPNPeer{}, err
	}
	if err := s.checkCapacity(ctx); err != nil {
		return storage.VPNPeer{}, err
	}
	pool, err := vpn.ParsePool(s.config.IPPool, s.config.NodeSubnetSize)
	if err != nil {
		// ParsePool already reports the stable vpn_ip_pool_invalid code.
		return storage.VPNPeer{}, err
	}
	lease, subnet, err := s.leaseNodeSubnet(ctx, pool)
	if err != nil {
		return storage.VPNPeer{}, err
	}
	address, err := s.allocateAddress(ctx, pool, subnet)
	if err != nil {
		return storage.VPNPeer{}, err
	}
	keyPair, err := vpn.GenerateKeyPair()
	if err != nil {
		return storage.VPNPeer{}, fmt.Errorf("vpn: generate peer key pair: %w", err)
	}
	sealed, err := s.sealPrivateKey(keyPair.PrivateKey)
	if err != nil {
		return storage.VPNPeer{}, err
	}
	if _, err := s.leases.AddAllocated(ctx, s.nodeID, lease.Subnet, lease.LeaseHolder, lease.Epoch, 1); err != nil {
		return storage.VPNPeer{}, s.mapLeaseError(err)
	}
	peer := storage.VPNPeer{
		Name:                 spec.Name,
		OwnerID:              actor.UserID,
		PublicKey:            keyPair.PublicKey,
		PrivateKeyCiphertext: sealed.ciphertext,
		PrivateKeyNonce:      sealed.nonce,
		PrivateKeyKeyID:      sealed.keyID,
		PrivateKeyVersion:    sealed.version,
		VPNIP:                address.String(),
		NodeID:               s.nodeID,
		AgentID:              spec.AgentID,
		AllowedIPs:           spec.EncodedAllowedIPs(),
		AllowedPorts:         spec.EncodedAllowedPorts(),
		AllowPrivateTargets:  spec.AllowPrivateTargets,
		ICMPEnabled:          spec.ICMPEnabled,
		MaxConcurrentFlows:   spec.MaxConcurrentFlows,
		PacketRateLimit:      spec.PacketRateLimit,
		ExpiresAt:            spec.ExpiresAt,
		Status:               storage.VPNPeerStatusActive,
		Description:          spec.Description,
	}
	created, err := s.peers.Create(ctx, peer)
	if err != nil {
		s.giveBackAddress(ctx, lease)
		if errors.Is(err, storage.ErrVPNPeerConflict) {
			// Either the public key or (node_id, vpn_ip) collided. Both are a
			// concurrent allocation race, and the unique index is the authority
			// that decided it, so the caller may simply retry.
			return storage.VPNPeer{}, vpn.ErrPeerConflict.WithMessage("vpn peer conflicts with an existing peer: the address or key was taken concurrently, retry the request")
		}
		return storage.VPNPeer{}, fmt.Errorf("vpn: persist peer: %w", err)
	}
	issued := map[string]any{
		"nodeId": created.NodeID, "agentId": created.AgentID, "vpnIp": created.VPNIP,
	}
	if spec.ICMPEnabled {
		issued["icmpCapability"] = auditVerification(icmpState)
	}
	s.audit(ctx, actor, vpnAuditIssued, created.ID, issued)
	return created, nil
}

// Get returns one peer the caller may see. A peer owned by somebody else is
// reported exactly like a missing one, because 403 would confirm the ID exists
// and let an outsider enumerate peer IDs one request at a time.
func (s *VPNPeerService) Get(ctx context.Context, actor auth.Principal, peerID string) (storage.VPNPeer, error) {
	if !s.config.Enabled {
		// A node that serves no VPN has nothing to show, and saying so with the
		// same code as "no such peer" keeps the disabled state from leaking
		// which IDs exist.
		return storage.VPNPeer{}, vpn.ErrPeerNotFound
	}
	return s.load(ctx, actor, peerID)
}

// List returns one page of peers. The owner restriction is injected into the
// filter, which the repository applies in the WHERE clause, so permission
// filtering happens inside pagination semantics rather than after it: a row the
// caller may not see can never appear on a later page, and a page is never
// silently truncated to hide one.
func (s *VPNPeerService) List(ctx context.Context, actor auth.Principal, filter storage.VPNPeerFilter, cursor string, limit int) (storage.Page[storage.VPNPeer], error) {
	if !s.config.Enabled {
		return storage.Page[storage.VPNPeer]{Items: []storage.VPNPeer{}}, nil
	}
	if !isAdmin(actor) {
		filter.OwnerUserID = actor.UserID
	}
	page, err := s.peers.List(ctx, filter, cursor, clampVPNListLimit(limit))
	if err != nil {
		return storage.Page[storage.VPNPeer]{}, fmt.Errorf("vpn: list peers: %w", err)
	}
	if page.Items == nil {
		page.Items = []storage.VPNPeer{}
	}
	return page, nil
}

// Update applies a partial patch. The stored row is loaded into a domain spec
// first, so a partial update travels the same validation path as creation and
// cannot bypass a rule the issue request enforced.
//
// The expiry rule only applies when the patch itself touches expires_at: a peer
// whose expiry has passed must still be renamable and must still be able to
// have its policy narrowed, because refusing those would leave an operator no
// way to shorten the lifetime of a peer that is already overdue.
func (s *VPNPeerService) Update(ctx context.Context, actor auth.Principal, peerID string, patch VPNPeerPatch) (storage.VPNPeer, error) {
	if !s.config.Enabled {
		return storage.VPNPeer{}, vpn.ErrNodeDisabled
	}
	stored, err := s.load(ctx, actor, peerID)
	if err != nil {
		return storage.VPNPeer{}, err
	}
	if stored.Status == storage.VPNPeerStatusRevoked {
		return storage.VPNPeer{}, revokedPeerConflict("modified")
	}
	spec, err := specFromStored(stored)
	if err != nil {
		return storage.VPNPeer{}, err
	}
	applyVPNPeerPatch(&spec, patch)
	if patch.HasExpiresAt {
		err = spec.ValidateRequest(s.now())
	} else {
		err = spec.ValidateFields()
	}
	if err != nil {
		return storage.VPNPeer{}, err
	}
	if patch.AgentID != nil {
		if err := s.authorizeAgent(ctx, actor, spec.AgentID); err != nil {
			return storage.VPNPeer{}, err
		}
	}
	// After the agent authorization, not before: probing first would answer a
	// question about an agent the caller may not be entitled to use, and the
	// difference between 409 and the agent's own refusal is information.
	icmpRequested := spec.ICMPEnabled && patch.ICMPEnabled != nil
	icmpState, err := s.authorizeICMP(ctx, spec.AgentID, icmpRequested)
	if err != nil {
		return storage.VPNPeer{}, err
	}
	status := stored.Status
	if patch.Status != nil {
		switch *patch.Status {
		case storage.VPNPeerStatusActive, storage.VPNPeerStatusDisabled:
			status = *patch.Status
		case storage.VPNPeerStatusRevoked:
			return storage.VPNPeer{}, vpn.ErrPeerInvalid.WithMessage("vpn peer is invalid: revocation has its own endpoint and cannot be set through a patch")
		default:
			return storage.VPNPeer{}, vpn.ErrPeerInvalid.WithMessage(fmt.Sprintf("vpn peer is invalid: unsupported status %q", string(*patch.Status)))
		}
	}
	updated := stored
	updated.Name = spec.Name
	updated.Description = spec.Description
	updated.AgentID = spec.AgentID
	updated.AllowedIPs = spec.EncodedAllowedIPs()
	updated.AllowedPorts = spec.EncodedAllowedPorts()
	updated.AllowPrivateTargets = spec.AllowPrivateTargets
	updated.ICMPEnabled = spec.ICMPEnabled
	updated.MaxConcurrentFlows = spec.MaxConcurrentFlows
	updated.PacketRateLimit = spec.PacketRateLimit
	updated.ExpiresAt = spec.ExpiresAt
	updated.Status = status
	updated.UpdatedAt = s.now()
	if err := s.peers.Update(ctx, updated); err != nil {
		if errors.Is(err, storage.ErrVPNPeerConflict) {
			return storage.VPNPeer{}, vpn.ErrPeerConflict
		}
		return storage.VPNPeer{}, fmt.Errorf("vpn: persist peer update: %w", err)
	}
	changes := map[string]any{
		"nodeId": updated.NodeID, "agentId": updated.AgentID, "status": string(updated.Status),
	}
	if icmpRequested {
		changes["icmpCapability"] = auditVerification(icmpState)
	}
	s.audit(ctx, actor, vpnAuditUpdated, updated.ID, changes)
	return updated, nil
}

// Rotate replaces the key pair and keeps everything else, above all the
// address: the /32 is part of the user's own configuration, so changing it
// during a rotation would silently break connectivity that the user asked to
// protect. The old public key stops working immediately, because a WireGuard
// handshake only ever consults public_key.
//
// Rotation does not touch the derived counter. The peer holds the same single
// address before and after, so incrementing again would drift the counter
// upwards on every rotation until the subnet looked full while it was not.
func (s *VPNPeerService) Rotate(ctx context.Context, actor auth.Principal, peerID string) (storage.VPNPeer, error) {
	if !s.config.Enabled {
		return storage.VPNPeer{}, vpn.ErrNodeDisabled
	}
	if s.secrets == nil {
		return storage.VPNPeer{}, vpn.ErrSecretUnavailable
	}
	stored, err := s.load(ctx, actor, peerID)
	if err != nil {
		return storage.VPNPeer{}, err
	}
	if stored.Status == storage.VPNPeerStatusRevoked {
		// Revocation is terminal. Rotating a revoked peer would revive a
		// retired identity, which is exactly what the terminal state forbids.
		return storage.VPNPeer{}, revokedPeerConflict("rotated")
	}
	keyPair, err := vpn.GenerateKeyPair()
	if err != nil {
		return storage.VPNPeer{}, fmt.Errorf("vpn: generate peer key pair: %w", err)
	}
	sealed, err := s.sealPrivateKey(keyPair.PrivateKey)
	if err != nil {
		return storage.VPNPeer{}, err
	}
	updated := stored
	updated.PublicKey = keyPair.PublicKey
	updated.PrivateKeyCiphertext = sealed.ciphertext
	updated.PrivateKeyNonce = sealed.nonce
	updated.PrivateKeyKeyID = sealed.keyID
	updated.PrivateKeyVersion = sealed.version
	updated.UpdatedAt = s.now()
	if err := s.peers.Update(ctx, updated); err != nil {
		if errors.Is(err, storage.ErrVPNPeerConflict) {
			return storage.VPNPeer{}, vpn.ErrPeerConflict
		}
		return storage.VPNPeer{}, fmt.Errorf("vpn: persist rotated peer: %w", err)
	}
	s.audit(ctx, actor, vpnAuditRotated, updated.ID, map[string]any{"nodeId": updated.NodeID})
	return updated, nil
}

// Revoke moves a peer to its terminal state. The row is kept: it is the audit
// trail for an address that was handed out, and the data plane stops seeing it
// because ListByNode excludes revoked peers.
//
// The address is deliberately not returned to the pool and the counter is
// deliberately not decremented. Reusing a retired address would let traffic
// intended for one peer arrive at another, and a counter that went down on
// revocation would eventually report capacity the subnet does not have.
//
// Revoking twice is not an error: the caller cannot act on "already revoked",
// and a retry after a lost response must not fail.
func (s *VPNPeerService) Revoke(ctx context.Context, actor auth.Principal, peerID string) error {
	if !s.config.Enabled {
		return vpn.ErrNodeDisabled
	}
	stored, err := s.load(ctx, actor, peerID)
	if err != nil {
		return err
	}
	if stored.Status == storage.VPNPeerStatusRevoked {
		return nil
	}
	if err := s.peers.SetStatus(ctx, stored.ID, storage.VPNPeerStatusRevoked, s.now()); err != nil {
		if errors.Is(err, storage.ErrVPNPeerRevoked) {
			// A concurrent revoke won the race. The end state is the one this
			// call wanted, so it succeeded.
			return nil
		}
		return fmt.Errorf("vpn: revoke peer: %w", err)
	}
	s.audit(ctx, actor, vpnAuditRevoked, stored.ID, map[string]any{
		"nodeId": stored.NodeID, "vpnIp": stored.VPNIP,
	})
	return nil
}

// RevealConfig renders the wg-quick file a user imports, private key included.
//
// This is the only code path in the product where a peer private key exists in
// plaintext outside the AEAD, so the plaintext never reaches a log line, a
// metric label or an audit detail: the audit entry records that a reveal
// happened and for which peer, nothing else. The HTTP layer pairs it with an
// explicit confirmation, an idempotency key and Cache-Control: no-store.
//
// A node that cannot produce a configuration which would actually work refuses
// instead of rendering a partial file: a blank gateway public key or an
// out-of-range MTU yields an import that fails on the user's machine with no
// pointer back to the server setting that is wrong.
func (s *VPNPeerService) RevealConfig(ctx context.Context, actor auth.Principal, peerID string) (string, error) {
	if !s.config.Enabled {
		return "", vpn.ErrNodeDisabled
	}
	stored, err := s.load(ctx, actor, peerID)
	if err != nil {
		return "", err
	}
	if err := s.nodeIdentity(); err != nil {
		return "", err
	}
	endpoint, err := s.peerEndpoint()
	if err != nil {
		return "", err
	}
	privateKey, err := s.openPrivateKey(stored)
	if err != nil {
		return "", err
	}
	allowedIPs, err := vpn.ParseAllowedIPs(stored.AllowedIPs)
	if err != nil {
		return "", err
	}
	rendered, err := vpn.RenderPeerConfig(vpn.PeerConfig{
		PrivateKey:          privateKey,
		Address:             net.ParseIP(stored.VPNIP),
		MTU:                 s.config.MTU,
		NodePublicKey:       s.nodePublicKey,
		AllowedIPs:          allowedIPs,
		Endpoint:            endpoint,
		PersistentKeepalive: vpn.DefaultPersistentKeepalive,
	})
	if err != nil {
		return "", err
	}
	s.audit(ctx, actor, vpnAuditRevealed, stored.ID, map[string]any{"nodeId": stored.NodeID})
	return rendered, nil
}

// specFromInput parses and validates a request body into a domain spec. The
// server-assigned fields stay empty here; Issue fills them before persisting.
func (s *VPNPeerService) specFromInput(input VPNPeerInput) (vpn.PeerSpec, error) {
	allowedIPs, err := vpn.ParseAllowedIPList(input.AllowedIPs)
	if err != nil {
		return vpn.PeerSpec{}, err
	}
	spec := vpn.PeerSpec{
		Name:                strings.TrimSpace(input.Name),
		Description:         input.Description,
		AgentID:             strings.TrimSpace(input.AgentID),
		AllowedIPs:          allowedIPs,
		AllowedPorts:        input.AllowedPorts,
		AllowPrivateTargets: input.AllowPrivateTargets,
		ICMPEnabled:         input.ICMPEnabled,
		MaxConcurrentFlows:  input.MaxConcurrentFlows,
		PacketRateLimit:     input.PacketRateLimit,
		ExpiresAt:           input.ExpiresAt,
	}
	if err := spec.ValidateRequest(s.now()); err != nil {
		return vpn.PeerSpec{}, err
	}
	return spec, nil
}

// specFromStored rebuilds the domain spec of a persisted peer. The policy text
// is parsed rather than trusted: the encoder is the only writer, so a parse
// failure means the row was hand-edited or corrupted, and refusing to operate on
// it is better than re-encoding a guess over the damage.
func specFromStored(peer storage.VPNPeer) (vpn.PeerSpec, error) {
	allowedIPs, err := vpn.ParseAllowedIPs(peer.AllowedIPs)
	if err != nil {
		return vpn.PeerSpec{}, err
	}
	allowedPorts, err := vpn.ParseAllowedPorts(peer.AllowedPorts)
	if err != nil {
		return vpn.PeerSpec{}, err
	}
	return vpn.PeerSpec{
		Name:                peer.Name,
		Description:         peer.Description,
		PublicKey:           peer.PublicKey,
		VPNIP:               net.ParseIP(peer.VPNIP),
		NodeID:              peer.NodeID,
		AgentID:             peer.AgentID,
		AllowedIPs:          allowedIPs,
		AllowedPorts:        allowedPorts,
		AllowPrivateTargets: peer.AllowPrivateTargets,
		ICMPEnabled:         peer.ICMPEnabled,
		MaxConcurrentFlows:  peer.MaxConcurrentFlows,
		PacketRateLimit:     peer.PacketRateLimit,
		ExpiresAt:           peer.ExpiresAt,
	}, nil
}

// applyVPNPeerPatch merges the present fields of a patch into a spec. Absent
// fields keep the stored value, which is what makes the call a patch.
func applyVPNPeerPatch(spec *vpn.PeerSpec, patch VPNPeerPatch) {
	if patch.Name != nil {
		spec.Name = strings.TrimSpace(*patch.Name)
	}
	if patch.Description != nil {
		spec.Description = *patch.Description
	}
	if patch.AgentID != nil {
		spec.AgentID = strings.TrimSpace(*patch.AgentID)
	}
	if patch.HasAllowedIPs {
		// Parse errors are reported by the validation that follows, which keeps
		// one code path for "this CIDR is not acceptable".
		parsed, err := vpn.ParseAllowedIPList(patch.AllowedIPs)
		if err != nil {
			spec.AllowedIPs = []*net.IPNet{nil}
		} else {
			spec.AllowedIPs = parsed
		}
	}
	if patch.HasAllowedPorts {
		spec.AllowedPorts = patch.AllowedPorts
	}
	if patch.AllowPrivateTargets != nil {
		spec.AllowPrivateTargets = *patch.AllowPrivateTargets
	}
	if patch.ICMPEnabled != nil {
		spec.ICMPEnabled = *patch.ICMPEnabled
	}
	if patch.MaxConcurrentFlows != nil {
		spec.MaxConcurrentFlows = *patch.MaxConcurrentFlows
	}
	if patch.PacketRateLimit != nil {
		spec.PacketRateLimit = *patch.PacketRateLimit
	}
	if patch.HasExpiresAt {
		spec.ExpiresAt = patch.ExpiresAt
	}
}

// authorizeAgent checks that the egress agent exists, is enabled and belongs to
// the caller.
//
// All three failures report the same message. They are indistinguishable on
// purpose: an agent ID is a guessable identifier, and a differing message would
// turn this endpoint into an oracle for "which agents exist and who owns them".
// The owner check is what stops a user from attaching a peer to somebody else's
// egress host, which would both route their traffic through it and let them
// probe the network behind it. An administrator may use any agent, because
// operating the gateway fleet is the administrator's job.
func (s *VPNPeerService) authorizeAgent(ctx context.Context, actor auth.Principal, agentID string) error {
	unavailable := vpn.ErrPeerInvalid.WithMessage("vpn peer is invalid: the egress agent is not available")
	if s.agents == nil {
		return errors.New("vpn: agent repository is not configured")
	}
	agent, err := s.agents.Get(ctx, agentID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return unavailable
		}
		return fmt.Errorf("vpn: load egress agent: %w", err)
	}
	if !agent.Enabled || (!isAdmin(actor) && agent.OwnerUserID != actor.UserID) {
		return unavailable
	}
	return nil
}

// authorizeICMP decides whether a peer may ask for echo, and returns the state it
// decided so the caller can record whether the cluster was able to confirm it.
//
// Three refusals share the one stable code because they are the same thing from
// the caller's point of view — this peer cannot have ICMP — while their messages
// stay distinct so an operator can tell a node switch from a missing probe from
// an agent that never negotiated the capability.
//
// The node switch is a ceiling above the agent: an operator who turns ICMP off on
// this node is not overruled by an agent that happens to support it, and the
// probe is not even asked.
func (s *VPNPeerService) authorizeICMP(ctx context.Context, agentID string, requested bool) (AgentCapabilityState, error) {
	if !requested {
		return "", nil
	}
	if !s.config.ICMPEnabled {
		return CapabilityUnsupported, vpn.ErrAgentCapabilityMissing.WithMessage("egress agent lacks the required vpn capability: icmp is disabled on this server node, set server.vpn.icmp_enabled=true to allow peers to request it")
	}
	if s.capabilities == nil {
		return CapabilityUnsupported, vpn.ErrAgentCapabilityMissing.WithMessage("egress agent lacks the required vpn capability: this server node has no agent capability probe configured, so it cannot verify stream_icmp_echo.v1")
	}
	state, err := s.capabilities.ProbeICMPEcho(ctx, agentID)
	if err != nil {
		// An infrastructure failure is reported as one. Folding it into a
		// capability verdict would tell the user their agent is broken when the
		// registry is.
		return CapabilityUnsupported, fmt.Errorf("vpn: probe the egress agent icmp capability: %w", err)
	}
	switch state {
	case CapabilitySupported, CapabilityUnverified:
		// Unverified passes on purpose; see probeAgentICMPEcho for why a refusal
		// here would fail at random across a cluster.
		return state, nil
	case CapabilityUnsupported:
		return state, vpn.ErrAgentCapabilityMissing.WithMessage("egress agent lacks the required vpn capability: icmp needs stream_icmp_echo.v1, which the agent has not negotiated or it is not connected to any server node")
	default:
		return CapabilityUnsupported, fmt.Errorf("vpn: unknown agent capability state %q", state)
	}
}

// auditVerification records whether the cluster could confirm the capability when
// the peer was issued. The state itself is not what an operator needs afterwards;
// whether it was verified is, because that is what separates "the agent is
// broken" from "this peer was issued without anybody being able to check".
func auditVerification(state AgentCapabilityState) string {
	if state == CapabilityUnverified {
		return "unverified"
	}
	return "verified"
}

// checkCapacity enforces server.vpn.max_peers against the peers this node
// actually serves. Revoked peers are excluded by the repository, so a retired
// peer stops consuming capacity: it keeps its row and its address for audit, but
// holding a gateway slot forever would make the quota a leak.
func (s *VPNPeerService) checkCapacity(ctx context.Context) error {
	if s.config.MaxPeers <= 0 {
		return nil
	}
	if s.peers == nil {
		return errors.New("vpn: peer repository is not configured")
	}
	count, err := s.peers.CountByNode(ctx, s.nodeID)
	if err != nil {
		return fmt.Errorf("vpn: count peers on node: %w", err)
	}
	if count >= s.config.MaxPeers {
		return vpn.ErrCapacityExhausted.WithMessage(fmt.Sprintf("vpn peer capacity exhausted: node %s serves %d of %d peers", s.nodeID, count, s.config.MaxPeers))
	}
	return nil
}

// leaseNodeSubnet returns the one subnet this node hands addresses from, plus
// the lease that fences it.
//
// A node holds exactly one subnet, which is what makes its own tunnel interface
// address well defined: the first usable address of that subnet. Reusing the
// subnet it already holds is not an optimisation but a correctness requirement.
// A restart that moved to a different /24 would leave every existing peer
// configured with an address outside the range the gateway answers for, and
// those peers would go unreachable the moment the process came back.
//
// Subnets are scanned in ascending order and a subnet with a live foreign lease
// is skipped, so two nodes converging on the same pool settle on different
// subnets without coordinating.
func (s *VPNPeerService) leaseNodeSubnet(ctx context.Context, pool vpn.Pool) (storage.VPNIPLease, *net.IPNet, error) {
	if s.leases == nil {
		return storage.VPNIPLease{}, nil, errors.New("vpn: ip lease repository is not configured")
	}
	held, err := s.leases.ListByHolder(ctx, s.nodeID)
	if err != nil {
		return storage.VPNIPLease{}, nil, fmt.Errorf("vpn: list leased subnets: %w", err)
	}
	for _, lease := range held {
		subnet, err := pool.NodeSubnet(lease.Subnet)
		if err != nil {
			// The pool changed under a running cluster, or the row was written
			// by an older configuration. Neither is a reason to fail the
			// request: the node can still lease a subnet the current pool
			// carves. Existing peers on the stale subnet keep their addresses,
			// because a peer row is never renumbered.
			continue
		}
		acquired, err := s.renewOrAcquire(ctx, lease, subnet)
		if err == nil {
			return acquired, subnet, nil
		}
		if !errors.Is(err, storage.ErrVPNIPLeaseHeld) {
			return storage.VPNIPLease{}, nil, s.mapLeaseError(err)
		}
	}
	for _, subnet := range pool.Subnets() {
		acquired, err := s.acquireSubnet(ctx, subnet.String())
		if errors.Is(err, storage.ErrVPNIPLeaseHeld) {
			continue
		}
		if err != nil {
			return storage.VPNIPLease{}, nil, s.mapLeaseError(err)
		}
		return acquired, subnet, nil
	}
	return storage.VPNIPLease{}, nil, vpn.ErrIPPoolExhausted.WithMessage(fmt.Sprintf("every subnet of ip_pool %s is leased by another node", s.config.IPPool))
}

// renewOrAcquire extends a lease this node still holds and re-acquires it when
// the extension is fenced out.
//
// Renew is tried first because it leaves the epoch alone. Acquiring on every
// issue would advance the epoch each time, and an epoch bump is how the
// repository fences a node out of its own counter update, so a second concurrent
// issue on the same node would fail for a reason nobody can act on.
func (s *VPNPeerService) renewOrAcquire(ctx context.Context, lease storage.VPNIPLease, subnet *net.IPNet) (storage.VPNIPLease, error) {
	err := s.leases.Renew(ctx, s.nodeID, lease.Subnet, s.nodeID, lease.Epoch, s.leaseTTL)
	if err == nil {
		// Only the fencing fields are read back from the returned lease, so the
		// stale in-memory expiry does not matter; the database owns it.
		return lease, nil
	}
	acquired, acquireErr := s.acquireSubnet(ctx, subnet.String())
	if acquireErr != nil {
		// Report the acquisition failure: it is the more recent fact, and a
		// lost renewal on its own does not tell the caller what to do.
		return storage.VPNIPLease{}, acquireErr
	}
	return acquired, nil
}

func (s *VPNPeerService) acquireSubnet(ctx context.Context, subnet string) (storage.VPNIPLease, error) {
	return s.leases.AcquireSubnet(ctx, storage.VPNIPLease{
		NodeID:      s.nodeID,
		Subnet:      subnet,
		LeaseHolder: s.nodeID,
		TTL:         s.leaseTTL,
	})
}

// allocateAddress picks the lowest free /32 in the node's subnet.
//
// Occupancy is read from the peer table rather than from the derived counter,
// because vpn_peers.UNIQUE(node_id, vpn_ip) is the authoritative fact that an
// address is taken. Revoked rows count as occupied, which is how a retired
// address stays retired.
func (s *VPNPeerService) allocateAddress(ctx context.Context, pool vpn.Pool, subnet *net.IPNet) (net.IP, error) {
	if s.peers == nil {
		return nil, errors.New("vpn: peer repository is not configured")
	}
	var lookupErr error
	address, err := pool.AllocateAddress(subnet, func(candidate net.IP) bool {
		if lookupErr != nil {
			// Stop handing out addresses on a database fault: guessing would
			// allocate from a set nobody can see.
			return true
		}
		_, getErr := s.peers.GetByNodeAndIP(ctx, s.nodeID, candidate.String())
		switch {
		case getErr == nil:
			return true
		case errors.Is(getErr, sql.ErrNoRows):
			return false
		default:
			lookupErr = getErr
			return true
		}
	})
	if lookupErr != nil {
		return nil, fmt.Errorf("vpn: probe allocated addresses: %w", lookupErr)
	}
	if err != nil {
		// AllocateAddress reports the stable vpn_ip_pool_exhausted code.
		return nil, err
	}
	return address, nil
}

// giveBackAddress undoes the counter increment after a failed insert.
//
// The rollback is best effort and its failure is not surfaced, because the
// original error is the one the caller can act on and the counter is a derived
// value: the unique index still prevents a duplicated address, so a counter that
// drifts high costs capacity, never correctness.
func (s *VPNPeerService) giveBackAddress(ctx context.Context, lease storage.VPNIPLease) {
	if s.leases == nil {
		return
	}
	_, _ = s.leases.AddAllocated(ctx, s.nodeID, lease.Subnet, lease.LeaseHolder, lease.Epoch, -1)
}

// load reads one peer and enforces visibility. Missing and not-yours are the
// same answer; see Get.
func (s *VPNPeerService) load(ctx context.Context, actor auth.Principal, peerID string) (storage.VPNPeer, error) {
	if s.peers == nil {
		return storage.VPNPeer{}, errors.New("vpn: peer repository is not configured")
	}
	peer, err := s.peers.Get(ctx, strings.TrimSpace(peerID))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return storage.VPNPeer{}, vpn.ErrPeerNotFound
		}
		return storage.VPNPeer{}, fmt.Errorf("vpn: load peer: %w", err)
	}
	if !isAdmin(actor) && peer.OwnerID != actor.UserID {
		return storage.VPNPeer{}, vpn.ErrPeerNotFound
	}
	return peer, nil
}

// sealedKey is the at-rest form of a peer private key: an AES-GCM ciphertext
// plus everything needed to open it again, in the same four columns the
// credential secrets use.
type sealedKey struct {
	ciphertext string
	nonce      string
	keyID      string
	version    int
}

func (s *VPNPeerService) sealPrivateKey(privateKey string) (sealedKey, error) {
	if s.secrets == nil {
		return sealedKey{}, vpn.ErrSecretUnavailable
	}
	ciphertext, nonce, keyID, version, err := s.secrets.Encrypt(privateKey)
	if err != nil {
		return sealedKey{}, fmt.Errorf("vpn: seal peer private key: %w", err)
	}
	return sealedKey{
		ciphertext: base64.RawStdEncoding.EncodeToString(ciphertext),
		nonce:      base64.RawStdEncoding.EncodeToString(nonce),
		keyID:      keyID,
		version:    version,
	}, nil
}

func (s *VPNPeerService) openPrivateKey(peer storage.VPNPeer) (string, error) {
	if s.secrets == nil {
		return "", vpn.ErrSecretUnavailable
	}
	ciphertext, err := base64.RawStdEncoding.DecodeString(peer.PrivateKeyCiphertext)
	if err != nil {
		return "", vpn.ErrSecretUnavailable.WithMessage("the sealed peer private key is not readable")
	}
	nonce, err := base64.RawStdEncoding.DecodeString(peer.PrivateKeyNonce)
	if err != nil {
		return "", vpn.ErrSecretUnavailable.WithMessage("the sealed peer private key nonce is not readable")
	}
	plaintext, err := s.secrets.Decrypt(ciphertext, nonce, peer.PrivateKeyKeyID, peer.PrivateKeyVersion)
	if err != nil {
		// A corrupt blob or a rotated encryption key is an operator-side fault.
		// Reporting it as unavailable, rather than as a peer error, keeps a
		// server-side key problem from looking like something the caller did.
		return "", vpn.ErrSecretUnavailable.WithMessage("the sealed peer private key cannot be opened with the configured encryption key")
	}
	return plaintext, nil
}

// nodeIdentity checks the gateway-side prerequisites of a rendered
// configuration: this node's own WireGuard public key and the MTU it advertises.
//
// Both come from configuration, so a failure means the node cannot hand out a
// file that would work. That is reported as the node not serving VPN rather than
// as a fault in the peer, which sends the operator to server.vpn instead of
// leaving them to debug a client import error that names no server setting.
func (s *VPNPeerService) nodeIdentity() error {
	if err := vpn.ValidatePublicKey(s.nodePublicKey); err != nil {
		return vpnNodeMisconfigured("this node has no usable wireguard public key, so a peer configuration would name no gateway")
	}
	if s.config.MTU < vpn.MinTunnelMTU || s.config.MTU > vpn.MaxTunnelMTU {
		return vpnNodeMisconfigured(fmt.Sprintf("server.vpn.mtu %d is outside %d-%d", s.config.MTU, vpn.MinTunnelMTU, vpn.MaxTunnelMTU))
	}
	return nil
}

// peerEndpoint renders the host:port a client must dial. The port always comes
// from server.vpn.listen and the host from server.vpn.endpoint_host, so the two
// cannot disagree about where the gateway is.
func (s *VPNPeerService) peerEndpoint() (string, error) {
	host := strings.TrimSpace(s.config.EndpointHost)
	if host == "" {
		return "", vpnNodeMisconfigured("server.vpn.endpoint_host is empty, so no peer configuration can name this gateway")
	}
	_, port, err := net.SplitHostPort(strings.TrimSpace(s.config.Listen))
	if err != nil || port == "" {
		return "", vpnNodeMisconfigured(fmt.Sprintf("server.vpn.listen %q is not a host:port pair", s.config.Listen))
	}
	return net.JoinHostPort(host, port), nil
}

// requireNodeID refuses to allocate on a node with no identity. Without a node
// id there is no lease holder to fence and no node_id column to write, and the
// repository would reject the lease with an error that does not name the cause.
func (s *VPNPeerService) requireNodeID() error {
	if s.nodeID == "" {
		return vpnNodeMisconfigured("this server node has no node id, so it cannot own vpn resources")
	}
	return nil
}

// mapLeaseError translates a lease fencing failure.
//
// A lost fence is a cluster fault, not something the caller did, and none of the
// stable vpn codes describes it: vpn_node_disabled means the configuration
// switch is off, and vpn_capacity_exhausted means the quota is full. Returning
// the wrapped error lets the HTTP layer answer 500 and the operator find the
// real cause, which is a node that took this node's subnet over.
func (s *VPNPeerService) mapLeaseError(err error) error {
	switch {
	case errors.Is(err, storage.ErrVPNIPLeaseStaleEpoch), errors.Is(err, storage.ErrVPNIPLeaseHeld):
		return fmt.Errorf("vpn: lost the subnet lease fence on node %s: %w", s.nodeID, err)
	case err == nil:
		return nil
	default:
		return fmt.Errorf("vpn: subnet lease: %w", err)
	}
}

// audit writes one lifecycle entry. Details carry only identifiers an operator
// needs to correlate the event with a peer; key material, packet content and
// credentials never appear, whatever the action is.
func (s *VPNPeerService) audit(ctx context.Context, actor auth.Principal, action, peerID string, details map[string]any) {
	if s.audits == nil {
		return
	}
	encoded, err := json.Marshal(details)
	if err != nil {
		encoded = []byte("{}")
	}
	_ = s.audits.Create(ctx, storage.AuditLog{
		ActorUserID:  actor.UserID,
		Action:       action,
		ResourceType: vpnPeerResourceType,
		ResourceID:   peerID,
		Details:      string(encoded),
	})
}

func (s *VPNPeerService) now() time.Time {
	if s.nowFn != nil {
		return s.nowFn()
	}
	return time.Now().UTC()
}

func revokedPeerConflict(verb string) *vpn.Error {
	return vpn.ErrPeerConflict.WithMessage(fmt.Sprintf("vpn peer is revoked and cannot be %s", verb))
}

func vpnNodeMisconfigured(reason string) *vpn.Error {
	return vpn.ErrNodeDisabled.WithMessage("vpn is not usable on this node: " + reason)
}

func clampVPNListLimit(limit int) int {
	switch {
	case limit <= 0:
		return vpnDefaultListLimit
	case limit > vpnMaxListLimit:
		return vpnMaxListLimit
	default:
		return limit
	}
}
