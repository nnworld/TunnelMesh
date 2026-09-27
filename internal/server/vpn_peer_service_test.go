package server_test

import (
	"context"
	"encoding/base64"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/auth"
	"github.com/tunnelmesh/tunnelmesh/internal/server"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
	"github.com/tunnelmesh/tunnelmesh/internal/vpn"
)

const vpnTestSecretKey = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="

var (
	vpnOwner = auth.Principal{UserID: "user-owner", Username: "owner", Role: "user"}
	vpnOther = auth.Principal{UserID: "user-other", Username: "other", Role: "user"}
	vpnAdmin = auth.Principal{UserID: "user-admin", Username: "admin", Role: "admin"}
)

type vpnServiceFixture struct {
	db      *storage.DB
	service *server.VPNPeerService
	deps    server.VPNPeerServiceDeps
}

func newVPNServiceFixture(t *testing.T, mutate func(*server.VPNServiceConfig)) *vpnServiceFixture {
	t.Helper()
	db, err := storage.OpenSQLite(context.Background(), "file:"+filepath.Join(t.TempDir(), "vpn.sqlite"), true)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Agents().Create(context.Background(), storage.Agent{
		ID: "agent-1", Name: "egress", OwnerUserID: vpnOwner.UserID, Enabled: true,
	}); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	store, err := auth.NewSecretStore(vpnTestSecretKey, "test-key")
	if err != nil {
		t.Fatalf("secret store: %v", err)
	}
	// Captured once so "an hour ago" in a test case is genuinely in the past
	// relative to the service clock, while the clock itself stays fixed.
	fixtureNow := time.Now().UTC()
	cfg := server.VPNServiceConfig{
		Enabled:        true,
		IPPool:         "10.64.0.0/16",
		NodeSubnetSize: 24,
		EndpointHost:   "gw-1.mesh.example.com",
		Listen:         "0.0.0.0:51820",
		MTU:            1420,
		MaxPeers:       0,
		ICMPEnabled:    true,
	}
	if mutate != nil {
		mutate(&cfg)
	}
	deps := server.VPNPeerServiceDeps{
		Peers:         db.VPNPeers(),
		Leases:        db.VPNIPLeases(),
		Agents:        db.Agents(),
		Audits:        db.Audits(),
		Secrets:       store,
		Config:        cfg,
		NodeID:        "server-node-1",
		LeaseTTL:      time.Minute,
		NodePublicKey: "3p7bfXt9wbTTW2HC7OQ1Nz+DQ8hbeGdNrfx+FG+IK08=",
		Now:           func() time.Time { return fixtureNow },
	}
	return &vpnServiceFixture{db: db, service: server.NewVPNPeerService(deps), deps: deps}
}

func (f *vpnServiceFixture) rebuild(t *testing.T, mutate func(*server.VPNPeerServiceDeps)) {
	t.Helper()
	deps := f.deps
	if mutate != nil {
		mutate(&deps)
	}
	f.deps = deps
	f.service = server.NewVPNPeerService(deps)
}

func issueInput() server.VPNPeerInput {
	return server.VPNPeerInput{
		Name:               "laptop",
		Description:        "engineering laptop",
		AgentID:            "agent-1",
		AllowedIPs:         []string{"10.0.0.0/8"},
		AllowedPorts:       []int{443},
		MaxConcurrentFlows: 128,
	}
}

func assertVPNError(t *testing.T, err error, status int, code string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected %d/%s, got nil", status, code)
	}
	apiErr := &vpn.Error{}
	if !errors.As(err, &apiErr) {
		t.Fatalf("error %v (%T) is not a *vpn.Error", err, err)
	}
	if apiErr.Status != status || apiErr.Code != code {
		t.Fatalf("got %d/%s, want %d/%s (%v)", apiErr.Status, apiErr.Code, status, code, err)
	}
}

func TestVPNPeerServiceIssueSignsAPeer(t *testing.T) {
	f := newVPNServiceFixture(t, nil)
	ctx := context.Background()

	created, err := f.service.Issue(ctx, vpnOwner, issueInput())
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if created.ID == "" || created.OwnerID != vpnOwner.UserID {
		t.Fatalf("owner = %q, want %q", created.OwnerID, vpnOwner.UserID)
	}
	if len(created.PublicKey) != 44 {
		t.Fatalf("public key = %q, want 44 base64 characters", created.PublicKey)
	}
	if err := vpn.ValidatePublicKey(created.PublicKey); err != nil {
		t.Fatalf("the issued public key does not validate: %v", err)
	}
	if created.Status != storage.VPNPeerStatusActive {
		t.Fatalf("status = %q, want active", created.Status)
	}
	if created.NodeID != "server-node-1" || created.AgentID != "agent-1" {
		t.Fatalf("node/agent = %q/%q", created.NodeID, created.AgentID)
	}
	if created.AllowedIPs != "10.0.0.0/8" || created.AllowedPorts != "443" {
		t.Fatalf("policy text = %q / %q", created.AllowedIPs, created.AllowedPorts)
	}
	// The address must come from the node's own carved subnet, not from the pool
	// at large, otherwise two nodes could hand out the same /32.
	ip := created.VPNIP
	if !strings.HasPrefix(ip, "10.64.") {
		t.Fatalf("vpn ip %q is not inside the pool", ip)
	}
	if strings.HasSuffix(ip, ".0") || strings.HasSuffix(ip, ".255") || strings.HasSuffix(ip, ".1") {
		t.Fatalf("vpn ip %q must not be the network, broadcast or node interface address", ip)
	}
	// The private key is sealed, never stored in the clear.
	for column, value := range map[string]string{
		"ciphertext": created.PrivateKeyCiphertext,
		"nonce":      created.PrivateKeyNonce,
		"key id":     created.PrivateKeyKeyID,
	} {
		if strings.TrimSpace(value) == "" {
			t.Fatalf("%s must be sealed and stored", column)
		}
	}
	if created.PrivateKeyKeyID != "test-key" || created.PrivateKeyVersion != 1 {
		t.Fatalf("key id/version = %q/%d", created.PrivateKeyKeyID, created.PrivateKeyVersion)
	}
	if _, err := base64.RawStdEncoding.DecodeString(created.PrivateKeyCiphertext); err != nil {
		t.Fatalf("ciphertext is not base64: %v", err)
	}

	lease, err := f.db.VPNIPLeases().Get(ctx, "server-node-1", subnetOf(ip))
	if err != nil {
		t.Fatalf("read the subnet lease: %v", err)
	}
	if lease.AllocatedCount != 1 {
		t.Fatalf("allocated count = %d, want 1", lease.AllocatedCount)
	}
	if lease.Epoch < 1 {
		t.Fatalf("epoch = %d, want at least 1", lease.Epoch)
	}

	second, err := f.service.Issue(ctx, vpnOwner, issueInput())
	if err != nil {
		t.Fatalf("second Issue: %v", err)
	}
	if second.VPNIP == created.VPNIP {
		t.Fatal("two peers were given the same address")
	}
	if second.PublicKey == created.PublicKey {
		t.Fatal("two peers were given the same key pair")
	}
}

func subnetOf(address string) string {
	parts := strings.Split(address, ".")
	return strings.Join(parts[:3], ".") + ".0/24"
}

// TestVPNPeerServiceNeverStoresThePlaintextKey reads the row straight back out of
// the database and searches every column for the revealed private key. The
// ciphertext columns are the only place key material may live.
func TestVPNPeerServiceNeverStoresThePlaintextKey(t *testing.T) {
	f := newVPNServiceFixture(t, nil)
	ctx := context.Background()
	created, err := f.service.Issue(ctx, vpnOwner, issueInput())
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	config, err := f.service.RevealConfig(ctx, vpnOwner, created.ID)
	if err != nil {
		t.Fatalf("RevealConfig: %v", err)
	}
	privateKey := valueFromINI(t, config, "PrivateKey")
	if privateKey == "" {
		t.Fatal("the revealed configuration carries no private key")
	}
	stored, err := f.db.VPNPeers().Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	columns := map[string]string{
		"id": stored.ID, "name": stored.Name, "owner_id": stored.OwnerID,
		"public_key": stored.PublicKey, "private_key_ciphertext": stored.PrivateKeyCiphertext,
		"private_key_nonce": stored.PrivateKeyNonce, "private_key_key_id": stored.PrivateKeyKeyID,
		"vpn_ip": stored.VPNIP, "node_id": stored.NodeID, "agent_id": stored.AgentID,
		"allowed_ips": stored.AllowedIPs, "allowed_ports": stored.AllowedPorts,
		"status": string(stored.Status), "description": stored.Description,
	}
	for column, value := range columns {
		if strings.Contains(value, privateKey) {
			t.Fatalf("column %s contains the plaintext private key", column)
		}
	}
	// The audit trail must not carry it either.
	page, err := f.db.Audits().List(ctx, storage.AuditFilter{}, "", 100)
	if err != nil {
		t.Fatalf("list audits: %v", err)
	}
	if len(page.Items) == 0 {
		t.Fatal("issuing a peer must write an audit entry")
	}
	for _, entry := range page.Items {
		if strings.Contains(entry.Details, privateKey) || strings.Contains(entry.Action, privateKey) {
			t.Fatalf("audit entry %s leaks the private key", entry.Action)
		}
	}
}

func valueFromINI(t *testing.T, ini, key string) string {
	t.Helper()
	for _, line := range strings.Split(ini, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, key+" = ") {
			return strings.TrimPrefix(trimmed, key+" = ")
		}
	}
	return ""
}

func TestVPNPeerServiceRejectsWritesWhileDisabled(t *testing.T) {
	f := newVPNServiceFixture(t, func(c *server.VPNServiceConfig) { c.Enabled = false })
	ctx := context.Background()

	_, err := f.service.Issue(ctx, vpnOwner, issueInput())
	assertVPNError(t, err, 409, "vpn_node_disabled")

	// Reads stay available and simply report nothing, so a console wired up
	// ahead of the gateway renders an empty table instead of an error page.
	page, err := f.service.List(ctx, vpnOwner, storage.VPNPeerFilter{}, "", 50)
	if err != nil {
		t.Fatalf("List while disabled: %v", err)
	}
	if len(page.Items) != 0 {
		t.Fatalf("a disabled node must list no peers, got %d", len(page.Items))
	}
	// A disabled node must not create VPN resources of any kind, including a
	// subnet lease.
	leases, err := f.db.VPNIPLeases().ListByHolder(ctx, "server-node-1")
	if err != nil {
		t.Fatalf("ListByHolder: %v", err)
	}
	if len(leases) != 0 {
		t.Fatalf("a disabled node created %d subnet leases", len(leases))
	}
}

// stubAgentCapabilityProbe fixes the answer the cluster would give, so the
// issuing rule can be tested without a session manager, a registry or a socket.
type stubAgentCapabilityProbe struct {
	state  server.AgentCapabilityState
	err    error
	probed []string
}

func (p *stubAgentCapabilityProbe) ProbeICMPEcho(_ context.Context, agentID string) (server.AgentCapabilityState, error) {
	p.probed = append(p.probed, agentID)
	return p.state, p.err
}

// vpnAuditDetails returns the details JSON of one audit action, which is where an
// operator learns whether an issued ICMP peer was actually verified.
func vpnAuditDetails(t *testing.T, f *vpnServiceFixture, action string) string {
	t.Helper()
	page, err := f.db.Audits().List(context.Background(), storage.AuditFilter{}, "", 100)
	if err != nil {
		t.Fatalf("list audits: %v", err)
	}
	for _, entry := range page.Items {
		if entry.Action == action {
			return entry.Details
		}
	}
	t.Fatalf("no %q audit entry was written", action)
	return ""
}

// ICMP is no longer refused outright: the egress agent's negotiated capability
// decides. The node switch stays a ceiling above it, and a node that cannot
// determine the answer says so in the audit trail instead of pretending.
func TestVPNPeerServiceAuthorizesICMPFromTheAgentCapability(t *testing.T) {
	ctx := context.Background()

	t.Run("a capable agent gets a pingable peer", func(t *testing.T) {
		f := newVPNServiceFixture(t, nil)
		probe := &stubAgentCapabilityProbe{state: server.CapabilitySupported}
		f.rebuild(t, func(d *server.VPNPeerServiceDeps) { d.AgentCapabilities = probe })
		input := issueInput()
		input.ICMPEnabled = true
		created, err := f.service.Issue(ctx, vpnOwner, input)
		if err != nil {
			t.Fatalf("Issue error = %v, want a peer", err)
		}
		if !created.ICMPEnabled {
			t.Fatalf("ICMPEnabled = false, want the request honoured")
		}
		if details := vpnAuditDetails(t, f, "vpn_peer_issued"); !strings.Contains(details, `"icmpCapability":"verified"`) {
			t.Fatalf("audit details = %s, want icmpCapability verified", details)
		}
		if len(probe.probed) != 1 || probe.probed[0] != "agent-1" {
			t.Fatalf("probed %v, want exactly one call for agent-1", probe.probed)
		}
	})

	t.Run("an unverifiable cluster still issues but records it", func(t *testing.T) {
		f := newVPNServiceFixture(t, nil)
		f.rebuild(t, func(d *server.VPNPeerServiceDeps) {
			d.AgentCapabilities = &stubAgentCapabilityProbe{state: server.CapabilityUnverified}
		})
		input := issueInput()
		input.ICMPEnabled = true
		if _, err := f.service.Issue(ctx, vpnOwner, input); err != nil {
			t.Fatalf("Issue error = %v, want a peer", err)
		}
		if details := vpnAuditDetails(t, f, "vpn_peer_issued"); !strings.Contains(details, `"icmpCapability":"unverified"`) {
			t.Fatalf("audit details = %s, want icmpCapability unverified", details)
		}
	})

	t.Run("an agent without the capability is refused", func(t *testing.T) {
		f := newVPNServiceFixture(t, nil)
		f.rebuild(t, func(d *server.VPNPeerServiceDeps) {
			d.AgentCapabilities = &stubAgentCapabilityProbe{state: server.CapabilityUnsupported}
		})
		input := issueInput()
		input.ICMPEnabled = true
		_, err := f.service.Issue(ctx, vpnOwner, input)
		assertVPNError(t, err, 409, "vpn_agent_capability_missing")
	})

	t.Run("a probe failure is an internal error, not a capability verdict", func(t *testing.T) {
		f := newVPNServiceFixture(t, nil)
		f.rebuild(t, func(d *server.VPNPeerServiceDeps) {
			d.AgentCapabilities = &stubAgentCapabilityProbe{err: errors.New("registry unavailable")}
		})
		input := issueInput()
		input.ICMPEnabled = true
		_, err := f.service.Issue(ctx, vpnOwner, input)
		if err == nil {
			t.Fatal("Issue error = nil, want the probe failure surfaced")
		}
		var apiError *vpn.Error
		if errors.As(err, &apiError) {
			t.Fatalf("Issue error = %v, want an internal error rather than a %s verdict", err, apiError.Code)
		}
		if !strings.Contains(err.Error(), "probe") {
			t.Fatalf("Issue error = %v, want it to name the probe", err)
		}
	})

	t.Run("the node switch stays a ceiling above the agent", func(t *testing.T) {
		f := newVPNServiceFixture(t, func(c *server.VPNServiceConfig) { c.ICMPEnabled = false })
		probe := &stubAgentCapabilityProbe{state: server.CapabilitySupported}
		f.rebuild(t, func(d *server.VPNPeerServiceDeps) { d.AgentCapabilities = probe })
		input := issueInput()
		input.ICMPEnabled = true
		_, err := f.service.Issue(ctx, vpnOwner, input)
		assertVPNError(t, err, 409, "vpn_agent_capability_missing")
		if len(probe.probed) != 0 {
			t.Fatalf("probed %v, want no probe call once the node switch is off", probe.probed)
		}
	})

	t.Run("no probe wired fails closed", func(t *testing.T) {
		f := newVPNServiceFixture(t, nil)
		input := issueInput()
		input.ICMPEnabled = true
		_, err := f.service.Issue(ctx, vpnOwner, input)
		assertVPNError(t, err, 409, "vpn_agent_capability_missing")
	})

	t.Run("a peer that does not ask for icmp is never probed", func(t *testing.T) {
		f := newVPNServiceFixture(t, nil)
		probe := &stubAgentCapabilityProbe{state: server.CapabilityUnsupported}
		f.rebuild(t, func(d *server.VPNPeerServiceDeps) { d.AgentCapabilities = probe })
		if _, err := f.service.Issue(ctx, vpnOwner, issueInput()); err != nil {
			t.Fatalf("Issue error = %v, want a peer", err)
		}
		if len(probe.probed) != 0 {
			t.Fatalf("probed %v, want no probe call for a peer that does not use icmp", probe.probed)
		}
	})
}

// Turning ICMP on later is the same decision as turning it on at issue time, and
// it is made against the agent the peer will actually egress through.
func TestVPNPeerServiceUpdateRechecksTheAgentCapabilityForICMP(t *testing.T) {
	ctx := context.Background()
	f := newVPNServiceFixture(t, nil)
	probe := &stubAgentCapabilityProbe{state: server.CapabilityUnsupported}
	f.rebuild(t, func(d *server.VPNPeerServiceDeps) { d.AgentCapabilities = probe })
	created, err := f.service.Issue(ctx, vpnOwner, issueInput())
	if err != nil {
		t.Fatalf("Issue error = %v, want a peer", err)
	}

	enabled := true
	if _, err := f.service.Update(ctx, vpnOwner, created.ID, server.VPNPeerPatch{ICMPEnabled: &enabled}); err == nil {
		t.Fatal("Update error = nil, want a refusal from an agent without the capability")
	} else {
		assertVPNError(t, err, 409, "vpn_agent_capability_missing")
	}

	probe.state = server.CapabilityUnverified
	updated, err := f.service.Update(ctx, vpnOwner, created.ID, server.VPNPeerPatch{ICMPEnabled: &enabled})
	if err != nil {
		t.Fatalf("Update error = %v, want the peer updated", err)
	}
	if !updated.ICMPEnabled {
		t.Fatal("ICMPEnabled = false after the patch, want true")
	}
	if details := vpnAuditDetails(t, f, "vpn_peer_updated"); !strings.Contains(details, `"icmpCapability":"unverified"`) {
		t.Fatalf("audit details = %s, want icmpCapability unverified", details)
	}

	// A patch that leaves ICMP alone is not a capability question.
	before := len(probe.probed)
	name := "renamed"
	if _, err := f.service.Update(ctx, vpnOwner, created.ID, server.VPNPeerPatch{Name: &name}); err != nil {
		t.Fatalf("Update error = %v, want the rename applied", err)
	}
	if len(probe.probed) != before {
		t.Fatalf("probed %v, want no new probe call for a rename", probe.probed)
	}
}

func TestVPNPeerServiceEnforcesTheMaxPeersQuota(t *testing.T) {
	f := newVPNServiceFixture(t, func(c *server.VPNServiceConfig) { c.MaxPeers = 2 })
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if _, err := f.service.Issue(ctx, vpnOwner, issueInput()); err != nil {
			t.Fatalf("Issue %d: %v", i, err)
		}
	}
	_, err := f.service.Issue(ctx, vpnOwner, issueInput())
	assertVPNError(t, err, 503, "vpn_capacity_exhausted")

	// Revoking frees quota, because CountByNode excludes revoked peers: a retired
	// peer holds an address for audit but must not consume capacity forever.
	page, err := f.service.List(ctx, vpnOwner, storage.VPNPeerFilter{}, "", 50)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if err := f.service.Revoke(ctx, vpnOwner, page.Items[0].ID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if _, err := f.service.Issue(ctx, vpnOwner, issueInput()); err != nil {
		t.Fatalf("Issue after revoke: %v", err)
	}
}

func TestVPNPeerServiceReportsPoolExhaustion(t *testing.T) {
	// A /24 pool carved into /30s leaves exactly one allocatable address per
	// subnet after the node interface address is reserved.
	f := newVPNServiceFixture(t, func(c *server.VPNServiceConfig) {
		c.IPPool = "10.64.0.0/24"
		c.NodeSubnetSize = 30
	})
	ctx := context.Background()
	first, err := f.service.Issue(ctx, vpnOwner, issueInput())
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if !strings.HasSuffix(first.VPNIP, ".2") && !strings.HasSuffix(first.VPNIP, ".6") {
		t.Logf("allocated %s from a /30", first.VPNIP)
	}
	_, err = f.service.Issue(ctx, vpnOwner, issueInput())
	assertVPNError(t, err, 409, "vpn_ip_pool_exhausted")
}

func TestVPNPeerServiceRejectsAnUnusablePool(t *testing.T) {
	f := newVPNServiceFixture(t, func(c *server.VPNServiceConfig) {
		c.IPPool = "10.64.0.0/33"
	})
	_, err := f.service.Issue(context.Background(), vpnOwner, issueInput())
	assertVPNError(t, err, 400, "vpn_ip_pool_invalid")
}

func TestVPNPeerServiceValidatesTheAgent(t *testing.T) {
	f := newVPNServiceFixture(t, nil)
	ctx := context.Background()

	unknown := issueInput()
	unknown.AgentID = "agent-missing"
	_, err := f.service.Issue(ctx, vpnOwner, unknown)
	assertVPNError(t, err, 400, "vpn_peer_invalid")

	// Another user's agent is reported exactly like a missing one, so an
	// attacker cannot enumerate agent IDs through this endpoint.
	if err := f.db.Agents().Create(ctx, storage.Agent{
		ID: "agent-2", Name: "someone else", OwnerUserID: vpnOther.UserID, Enabled: true,
	}); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	foreign := issueInput()
	foreign.AgentID = "agent-2"
	_, err = f.service.Issue(ctx, vpnOwner, foreign)
	assertVPNError(t, err, 400, "vpn_peer_invalid")
	// An administrator may use any agent.
	if _, err := f.service.Issue(ctx, vpnAdmin, foreign); err != nil {
		t.Fatalf("an admin must be able to issue through any agent: %v", err)
	}

	if err := f.db.Agents().Update(ctx, storage.Agent{
		ID: "agent-1", Name: "egress", OwnerUserID: vpnOwner.UserID, Enabled: false,
	}); err != nil {
		t.Fatalf("disable agent: %v", err)
	}
	_, err = f.service.Issue(ctx, vpnOwner, issueInput())
	assertVPNError(t, err, 400, "vpn_peer_invalid")
}

func TestVPNPeerServiceValidatesTheSpec(t *testing.T) {
	f := newVPNServiceFixture(t, nil)
	cases := map[string]func(*server.VPNPeerInput){
		"empty name":     func(i *server.VPNPeerInput) { i.Name = "" },
		"empty agent":    func(i *server.VPNPeerInput) { i.AgentID = "" },
		"bad cidr":       func(i *server.VPNPeerInput) { i.AllowedIPs = []string{"10.0.0.1"} },
		"bad port":       func(i *server.VPNPeerInput) { i.AllowedPorts = []int{0} },
		"negative flows": func(i *server.VPNPeerInput) { i.MaxConcurrentFlows = -1 },
		"negative rate":  func(i *server.VPNPeerInput) { i.PacketRateLimit = -1 },
		"expired":        func(i *server.VPNPeerInput) { past := time.Now().Add(-time.Hour); i.ExpiresAt = &past },
	}
	for name, mutate := range cases {
		input := issueInput()
		mutate(&input)
		_, err := f.service.Issue(context.Background(), vpnOwner, input)
		assertVPNError(t, err, 400, "vpn_peer_invalid")
		_ = name
	}
}

// TestVPNPeerServiceRollsBackTheCounterOnPersistFailure is the invariant that
// keeps exhaustion detection honest. allocated_count is a derived counter, and a
// counter that only ever grows would eventually report a subnet full while it
// still has addresses, so a failed insert must give the address back.
func TestVPNPeerServiceRollsBackTheCounterOnPersistFailure(t *testing.T) {
	f := newVPNServiceFixture(t, nil)
	ctx := context.Background()
	f.rebuild(t, func(d *server.VPNPeerServiceDeps) {
		d.Peers = failingVPNPeerRepository{VPNPeerRepository: d.Peers, failCreate: true}
	})
	_, err := f.service.Issue(ctx, vpnOwner, issueInput())
	if err == nil {
		t.Fatal("expected the failing repository to surface an error")
	}

	// Restore a working repository and issue again: the address handed back by
	// the rollback must be reusable, and the counter must not have drifted.
	f.rebuild(t, func(d *server.VPNPeerServiceDeps) {
		d.Peers = f.db.VPNPeers()
	})
	created, err := f.service.Issue(ctx, vpnOwner, issueInput())
	if err != nil {
		t.Fatalf("Issue after rollback: %v", err)
	}
	lease, err := f.db.VPNIPLeases().Get(ctx, "server-node-1", subnetOf(created.VPNIP))
	if err != nil {
		t.Fatalf("read lease: %v", err)
	}
	if lease.AllocatedCount != 1 {
		t.Fatalf("allocated count = %d after one successful issue, want 1", lease.AllocatedCount)
	}
}

func TestVPNPeerServiceReportsSecretStoreUnavailability(t *testing.T) {
	f := newVPNServiceFixture(t, nil)
	ctx := context.Background()
	f.rebuild(t, func(d *server.VPNPeerServiceDeps) { d.Secrets = nil })
	_, err := f.service.Issue(ctx, vpnOwner, issueInput())
	// Reusing the existing credential code rather than inventing a second name
	// for one root cause: the process has no TUNNELMESH_TOKEN_ENCRYPTION_KEY.
	assertVPNError(t, err, 503, "credential_secret_unavailable")

	// Issuing must not have consumed an address.
	f.rebuild(t, func(d *server.VPNPeerServiceDeps) {
		store, storeErr := auth.NewSecretStore(vpnTestSecretKey, "test-key")
		if storeErr != nil {
			t.Fatalf("secret store: %v", storeErr)
		}
		d.Secrets = store
	})
	created, err := f.service.Issue(ctx, vpnOwner, issueInput())
	if err != nil {
		t.Fatalf("Issue once the store is available: %v", err)
	}
	lease, err := f.db.VPNIPLeases().Get(ctx, "server-node-1", subnetOf(created.VPNIP))
	if err != nil {
		t.Fatalf("read lease: %v", err)
	}
	if lease.AllocatedCount != 1 {
		t.Fatalf("allocated count = %d, want 1", lease.AllocatedCount)
	}
}

func TestVPNPeerServiceRevokeIsTerminal(t *testing.T) {
	f := newVPNServiceFixture(t, nil)
	ctx := context.Background()
	created, err := f.service.Issue(ctx, vpnOwner, issueInput())
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if err := f.service.Revoke(ctx, vpnOwner, created.ID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	stored, err := f.service.Get(ctx, vpnOwner, created.ID)
	if err != nil {
		t.Fatalf("a revoked peer must stay readable for audit: %v", err)
	}
	if stored.Status != storage.VPNPeerStatusRevoked {
		t.Fatalf("status = %q, want revoked", stored.Status)
	}
	// The data plane loads peers through ListByNode, so a revoked peer must
	// disappear from it while the row itself is kept.
	byNode, err := f.db.VPNPeers().ListByNode(ctx, "server-node-1")
	if err != nil {
		t.Fatalf("ListByNode: %v", err)
	}
	for _, peer := range byNode {
		if peer.ID == created.ID {
			t.Fatal("ListByNode still returns a revoked peer")
		}
	}
	// Revocation is terminal: rotating would revive a retired key.
	_, err = f.service.Rotate(ctx, vpnOwner, created.ID)
	assertVPNError(t, err, 409, "vpn_peer_conflict")
	// Revoking twice is not an error the caller can act on, but it must not
	// resurrect anything either.
	if err := f.service.Revoke(ctx, vpnOwner, created.ID); err != nil {
		t.Fatalf("a second revoke must be idempotent, got %v", err)
	}
	// Revocation is terminal for modification too, not only for rotation.
	name := "renamed"
	_, err = f.service.Update(ctx, vpnOwner, created.ID, server.VPNPeerPatch{Name: &name})
	assertVPNError(t, err, 409, "vpn_peer_conflict")
	// The address is never reused, so the counter keeps counting it.
	if _, err := f.service.Issue(ctx, vpnOwner, issueInput()); err != nil {
		t.Fatalf("Issue after revoke: %v", err)
	}
}

func TestVPNPeerServiceRotateKeepsTheAddress(t *testing.T) {
	f := newVPNServiceFixture(t, nil)
	ctx := context.Background()
	created, err := f.service.Issue(ctx, vpnOwner, issueInput())
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	before, err := f.service.RevealConfig(ctx, vpnOwner, created.ID)
	if err != nil {
		t.Fatalf("RevealConfig: %v", err)
	}
	rotated, err := f.service.Rotate(ctx, vpnOwner, created.ID)
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	// The key changes, because that is the entire point of a rotation: the old
	// public key must stop working immediately.
	if rotated.PublicKey == created.PublicKey {
		t.Fatal("rotation did not replace the public key")
	}
	if err := vpn.ValidatePublicKey(rotated.PublicKey); err != nil {
		t.Fatalf("the rotated public key does not validate: %v", err)
	}
	// The address does not change. It is part of the user's own configuration,
	// so replacing it would silently break their connectivity.
	if rotated.VPNIP != created.VPNIP {
		t.Fatalf("rotation changed the address from %s to %s", created.VPNIP, rotated.VPNIP)
	}
	if rotated.PrivateKeyCiphertext == created.PrivateKeyCiphertext {
		t.Fatal("rotation did not reseal the private key")
	}
	after, err := f.service.RevealConfig(ctx, vpnOwner, rotated.ID)
	if err != nil {
		t.Fatalf("RevealConfig after rotate: %v", err)
	}
	if valueFromINI(t, before, "PrivateKey") == valueFromINI(t, after, "PrivateKey") {
		t.Fatal("rotation returned the same private key")
	}
	if valueFromINI(t, after, "Address") != valueFromINI(t, before, "Address") {
		t.Fatal("rotation changed the rendered address")
	}
	// Rotating must not consume another address.
	lease, err := f.db.VPNIPLeases().Get(ctx, "server-node-1", subnetOf(rotated.VPNIP))
	if err != nil {
		t.Fatalf("read lease: %v", err)
	}
	if lease.AllocatedCount != 1 {
		t.Fatalf("allocated count = %d after a rotation, want 1", lease.AllocatedCount)
	}
}

func TestVPNPeerServiceVisibilityIsNotFoundRatherThanForbidden(t *testing.T) {
	f := newVPNServiceFixture(t, nil)
	ctx := context.Background()
	created, err := f.service.Issue(ctx, vpnOwner, issueInput())
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	// 403 would confirm the ID exists, which lets an outsider enumerate peer IDs
	// one request at a time. 404 for both "missing" and "yours, not mine".
	for _, call := range map[string]func() error{
		"Get": func() error { _, err := f.service.Get(ctx, vpnOther, created.ID); return err },
		"Update": func() error {
			name := "stolen"
			_, err := f.service.Update(ctx, vpnOther, created.ID, server.VPNPeerPatch{Name: &name})
			return err
		},
		"Revoke": func() error { return f.service.Revoke(ctx, vpnOther, created.ID) },
		"Rotate": func() error { _, err := f.service.Rotate(ctx, vpnOther, created.ID); return err },
		"Reveal": func() error { _, err := f.service.RevealConfig(ctx, vpnOther, created.ID); return err },
	} {
		assertVPNError(t, call(), 404, "vpn_peer_not_found")
	}
	assertVPNError(t, func() error {
		_, err := f.service.Get(ctx, vpnOwner, "vpn-peer-does-not-exist")
		return err
	}(), 404, "vpn_peer_not_found")
	// An administrator can see and reveal anyone's peer.
	if _, err := f.service.Get(ctx, vpnAdmin, created.ID); err != nil {
		t.Fatalf("an admin must read any peer: %v", err)
	}
	if _, err := f.service.RevealConfig(ctx, vpnAdmin, created.ID); err != nil {
		t.Fatalf("an admin must reveal any peer: %v", err)
	}
}

// TestVPNPeerServiceListFiltersInsidePagination is D11 verified below the HTTP
// layer: the owner filter is a WHERE condition, so a leaked row cannot appear on
// a later page either.
func TestVPNPeerServiceListFiltersInsidePagination(t *testing.T) {
	f := newVPNServiceFixture(t, nil)
	ctx := context.Background()
	// The second owner issues through an agent of their own. Sharing somebody
	// else's egress agent is refused by design, as
	// TestVPNPeerServiceValidatesTheAgent asserts, so this test needs a second
	// agent rather than a second user on the first one.
	if err := f.db.Agents().Create(ctx, storage.Agent{
		ID: "agent-2", Name: "other egress", OwnerUserID: vpnOther.UserID, Enabled: true,
	}); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, err := f.service.Issue(ctx, vpnOwner, issueInput()); err != nil {
			t.Fatalf("owner Issue %d: %v", i, err)
		}
		other := issueInput()
		other.AgentID = "agent-2"
		if _, err := f.service.Issue(ctx, vpnOther, other); err != nil {
			t.Fatalf("other Issue %d: %v", i, err)
		}
	}
	cursor := ""
	pages := 0
	seen := 0
	for {
		page, err := f.service.List(ctx, vpnOwner, storage.VPNPeerFilter{}, cursor, 2)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		for _, peer := range page.Items {
			if peer.OwnerID != vpnOwner.UserID {
				t.Fatalf("page %d leaked peer %s owned by %s", pages, peer.ID, peer.OwnerID)
			}
		}
		seen += len(page.Items)
		pages++
		if !page.HasMore || pages > 10 {
			break
		}
		cursor = page.NextCursor
	}
	if seen != 3 {
		t.Fatalf("the owner saw %d peers across %d pages, want 3", seen, pages)
	}
	// An administrator sees both owners' peers.
	adminPage, err := f.service.List(ctx, vpnAdmin, storage.VPNPeerFilter{}, "", 50)
	if err != nil {
		t.Fatalf("admin List: %v", err)
	}
	if len(adminPage.Items) != 6 {
		t.Fatalf("an admin saw %d peers, want 6", len(adminPage.Items))
	}
}

func TestVPNPeerServiceUpdateAppliesAPartialPatch(t *testing.T) {
	f := newVPNServiceFixture(t, nil)
	ctx := context.Background()
	created, err := f.service.Issue(ctx, vpnOwner, issueInput())
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	name := "renamed"
	ips := []string{"192.0.2.0/24", "10.10.0.0/16"}
	ports := []int{8443, 443}
	private := false
	updated, err := f.service.Update(ctx, vpnOwner, created.ID, server.VPNPeerPatch{
		Name: &name, HasAllowedIPs: true, AllowedIPs: ips,
		HasAllowedPorts: true, AllowedPorts: ports, AllowPrivateTargets: &private,
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Name != "renamed" {
		t.Fatalf("name = %q", updated.Name)
	}
	if updated.AllowedIPs != "10.10.0.0/16,192.0.2.0/24" {
		t.Fatalf("allowed_ips = %q, want the canonical encoding", updated.AllowedIPs)
	}
	if updated.AllowedPorts != "443,8443" {
		t.Fatalf("allowed_ports = %q, want 443,8443", updated.AllowedPorts)
	}
	if updated.AllowPrivateTargets {
		t.Fatal("allow_private_targets was not applied")
	}
	// Untouched fields keep their value, which is what makes this a patch.
	if updated.VPNIP != created.VPNIP || updated.PublicKey != created.PublicKey || updated.AgentID != created.AgentID {
		t.Fatal("the patch changed a field it was not given")
	}
	if updated.Description != created.Description {
		t.Fatalf("description changed from %q to %q", created.Description, updated.Description)
	}

	// Revocation has its own endpoint; letting PATCH set the terminal state
	// would give two ways to do one thing and one of them would skip the audit
	// action the other writes.
	revoked := storage.VPNPeerStatusRevoked
	_, err = f.service.Update(ctx, vpnOwner, created.ID, server.VPNPeerPatch{Status: &revoked})
	assertVPNError(t, err, 400, "vpn_peer_invalid")

	disabled := storage.VPNPeerStatusDisabled
	if _, err := f.service.Update(ctx, vpnOwner, created.ID, server.VPNPeerPatch{Status: &disabled}); err != nil {
		t.Fatalf("disabling a peer through PATCH must work: %v", err)
	}
	bogus := storage.VPNPeerStatus("exploded")
	_, err = f.service.Update(ctx, vpnOwner, created.ID, server.VPNPeerPatch{Status: &bogus})
	assertVPNError(t, err, 400, "vpn_peer_invalid")
}

// TestVPNPeerServicePatchesAnExpiredPeer pins why validation is split in two. An
// overdue peer must still be renamable and must still be able to have its policy
// narrowed: refusing that would leave an operator no way to shorten the lifetime
// of a peer that is already past it. The clock rule still guards the one field it
// exists for, so writing an expiry that is already in the past stays a 400.
func TestVPNPeerServicePatchesAnExpiredPeer(t *testing.T) {
	f := newVPNServiceFixture(t, nil)
	ctx := context.Background()
	expires := time.Now().Add(time.Hour)
	input := issueInput()
	input.ExpiresAt = &expires
	created, err := f.service.Issue(ctx, vpnOwner, input)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	// Move the service clock past the expiry, as a peer left alone for a day.
	f.rebuild(t, func(d *server.VPNPeerServiceDeps) {
		later := expires.Add(time.Hour)
		d.Now = func() time.Time { return later }
	})
	name := "renamed after expiry"
	updated, err := f.service.Update(ctx, vpnOwner, created.ID, server.VPNPeerPatch{Name: &name})
	if err != nil {
		t.Fatalf("renaming an expired peer must stay possible: %v", err)
	}
	if updated.Name != name {
		t.Fatalf("name = %q, want %q", updated.Name, name)
	}
	past := expires.Add(-2 * time.Hour)
	_, err = f.service.Update(ctx, vpnOwner, created.ID, server.VPNPeerPatch{HasExpiresAt: true, ExpiresAt: &past})
	assertVPNError(t, err, 400, "vpn_peer_invalid")
	// Extending the lifetime is how an overdue peer is brought back, so it must
	// be accepted even though the stored expiry has already passed.
	future := expires.Add(48 * time.Hour)
	if _, err := f.service.Update(ctx, vpnOwner, created.ID, server.VPNPeerPatch{HasExpiresAt: true, ExpiresAt: &future}); err != nil {
		t.Fatalf("extending an expired peer must work: %v", err)
	}
}

func TestVPNPeerServiceRevealRendersAnImportableConfiguration(t *testing.T) {
	f := newVPNServiceFixture(t, nil)
	ctx := context.Background()
	created, err := f.service.Issue(ctx, vpnOwner, issueInput())
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	rendered, err := f.service.RevealConfig(ctx, vpnOwner, created.ID)
	if err != nil {
		t.Fatalf("RevealConfig: %v", err)
	}
	for _, want := range []string{
		"[Interface]", "[Peer]",
		"Address = " + created.VPNIP + "/32",
		"MTU = 1420",
		"PublicKey = 3p7bfXt9wbTTW2HC7OQ1Nz+DQ8hbeGdNrfx+FG+IK08=",
		"AllowedIPs = 10.0.0.0/8",
		"Endpoint = gw-1.mesh.example.com:51820",
		"PersistentKeepalive = 25",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("the rendered configuration is missing %q:\n%s", want, rendered)
		}
	}
	if valueFromINI(t, rendered, "PrivateKey") == "" {
		t.Fatal("the revealed configuration must carry the peer private key exactly once")
	}

	// A node with no WireGuard identity cannot hand out a configuration that
	// would work, so reveal is refused rather than rendering a file whose
	// [Peer] PublicKey is blank.
	f.rebuild(t, func(d *server.VPNPeerServiceDeps) { d.NodePublicKey = "" })
	_, err = f.service.RevealConfig(ctx, vpnOwner, created.ID)
	assertVPNError(t, err, 409, "vpn_node_disabled")

	// With no encryption key the sealed private key cannot be opened, and the
	// service must not fall back to any plaintext path.
	f.rebuild(t, func(d *server.VPNPeerServiceDeps) {
		d.NodePublicKey = "3p7bfXt9wbTTW2HC7OQ1Nz+DQ8hbeGdNrfx+FG+IK08="
		d.Secrets = nil
	})
	_, err = f.service.RevealConfig(ctx, vpnOwner, created.ID)
	assertVPNError(t, err, 503, "credential_secret_unavailable")
}

func TestVPNPeerServiceReusesOneSubnetLease(t *testing.T) {
	f := newVPNServiceFixture(t, nil)
	ctx := context.Background()
	for i := 0; i < 4; i++ {
		if _, err := f.service.Issue(ctx, vpnOwner, issueInput()); err != nil {
			t.Fatalf("Issue %d: %v", i, err)
		}
	}
	leases, err := f.db.VPNIPLeases().ListByHolder(ctx, "server-node-1")
	if err != nil {
		t.Fatalf("ListByHolder: %v", err)
	}
	if len(leases) != 1 {
		t.Fatalf("the node holds %d subnet leases, want 1", len(leases))
	}
	if leases[0].AllocatedCount != 4 {
		t.Fatalf("allocated count = %d, want 4", leases[0].AllocatedCount)
	}
	// Every address came from the one subnet the node carved.
	page, err := f.service.List(ctx, vpnOwner, storage.VPNPeerFilter{}, "", 50)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, peer := range page.Items {
		if subnetOf(peer.VPNIP) != leases[0].Subnet {
			t.Fatalf("peer %s holds %s outside the leased subnet %s", peer.ID, peer.VPNIP, leases[0].Subnet)
		}
	}
}

// TestVPNPeerServiceSubnetIsStableAcrossRestarts covers the operational property
// that matters most: a restarted node must keep handing out addresses from the
// subnet its existing peers already live in, otherwise those peers stop being
// reachable the moment the process comes back.
func TestVPNPeerServiceSubnetIsStableAcrossRestarts(t *testing.T) {
	f := newVPNServiceFixture(t, nil)
	ctx := context.Background()
	first, err := f.service.Issue(ctx, vpnOwner, issueInput())
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	// A brand new service instance over the same database and node ID, as after
	// a process restart.
	f.rebuild(t, nil)
	second, err := f.service.Issue(ctx, vpnOwner, issueInput())
	if err != nil {
		t.Fatalf("Issue after restart: %v", err)
	}
	if subnetOf(first.VPNIP) != subnetOf(second.VPNIP) {
		t.Fatalf("the node moved subnet across a restart: %s then %s", first.VPNIP, second.VPNIP)
	}
	leases, err := f.db.VPNIPLeases().ListByHolder(ctx, "server-node-1")
	if err != nil {
		t.Fatalf("ListByHolder: %v", err)
	}
	if len(leases) != 1 {
		t.Fatalf("a restart created a second lease: %d rows", len(leases))
	}
}

func TestVPNPeerServiceAuditsLifecycleEvents(t *testing.T) {
	f := newVPNServiceFixture(t, nil)
	ctx := context.Background()
	created, err := f.service.Issue(ctx, vpnOwner, issueInput())
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if _, err := f.service.Rotate(ctx, vpnOwner, created.ID); err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if _, err := f.service.RevealConfig(ctx, vpnOwner, created.ID); err != nil {
		t.Fatalf("RevealConfig: %v", err)
	}
	if err := f.service.Revoke(ctx, vpnOwner, created.ID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	page, err := f.db.Audits().List(ctx, storage.AuditFilter{}, "", 100)
	if err != nil {
		t.Fatalf("list audits: %v", err)
	}
	actions := make(map[string]bool, len(page.Items))
	for _, entry := range page.Items {
		actions[entry.Action] = true
		if entry.ResourceID != created.ID {
			t.Errorf("audit action %s points at resource %q", entry.Action, entry.ResourceID)
		}
		if entry.ActorUserID != vpnOwner.UserID {
			t.Errorf("audit action %s records actor %q", entry.Action, entry.ActorUserID)
		}
	}
	for _, want := range []string{"vpn_peer_issued", "vpn_peer_rotated", "vpn_peer_config_revealed", "vpn_peer_revoked"} {
		if !actions[want] {
			t.Errorf("no audit entry for %s (have %v)", want, actions)
		}
	}
}

// failingVPNPeerRepository fails one method and delegates the rest, so a test can
// force the persist step to fail without reimplementing the interface.
type failingVPNPeerRepository struct {
	storage.VPNPeerRepository
	failCreate bool
	failUpdate bool
}

func (r failingVPNPeerRepository) Create(ctx context.Context, peer storage.VPNPeer) (storage.VPNPeer, error) {
	if r.failCreate {
		return storage.VPNPeer{}, errors.New("injected create failure")
	}
	return r.VPNPeerRepository.Create(ctx, peer)
}

func (r failingVPNPeerRepository) Update(ctx context.Context, peer storage.VPNPeer) error {
	if r.failUpdate {
		return errors.New("injected update failure")
	}
	return r.VPNPeerRepository.Update(ctx, peer)
}
