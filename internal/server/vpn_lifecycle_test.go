//go:build vpn

package server

import (
	"context"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/config"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
	"github.com/tunnelmesh/tunnelmesh/internal/vpn"
)

// This file asserts the two properties of the gateway that no other test can
// see: the order its teardown runs in, and the lifetime of the two loops it owns.
//
// Neither is observable from timings. A teardown that shut the stack down before
// the device would still finish inside any deadline a test could choose, and a
// reaper that never ran is indistinguishable from one that found nothing to do.
// So the gateway records the steps it took and the tests read the record, which
// is also what makes D14's sequence a claim about the code rather than a comment
// next to it.

const (
	vpnLifecycleNodeID = "server-node-1"
	vpnLifecycleSubnet = "10.64.5.0/24"
	// vpnLifecycleNodeIP is Pool.NodeAddress of the subnet above: the address the
	// gateway must refuse traffic to, learned from the lease rather than derived
	// from the configuration because the subnet is chosen at issue time.
	vpnLifecycleNodeIP = "10.64.5.1"
	// vpnLifecycleLoopInterval shortens both background loops so a test observes
	// several passes instead of waiting out a production interval.
	vpnLifecycleLoopInterval = 5 * time.Millisecond
	// vpnLifecycleWait bounds every poll below. It is generous on purpose: these
	// assertions are about something happening at all, so a failure has to mean
	// the loop is broken rather than that the machine was busy.
	vpnLifecycleWait = 5 * time.Second
)

// vpnShutdownStepOrder is D14's teardown sequence, spelled once so the tests that
// assert it and the implementation that records it cannot drift apart silently.
var vpnShutdownStepOrder = []string{
	"drain",
	"listeners",
	"await_flows",
	"flows",
	"endpoint",
	"peers",
	"stack",
	"background",
	"denials",
}

// recordingLeaseRepository answers the two calls the renewal loop makes and
// records their arguments verbatim.
//
// The embedded interface is the same trick stubVPNPeerRepository uses: a
// lifecycle test that reached another repository method would rather panic than
// receive a zero value that could be mistaken for an empty lease set.
type recordingLeaseRepository struct {
	storage.VPNIPLeaseRepository

	mu       sync.Mutex
	held     []storage.VPNIPLease
	listErr  error
	renewErr error
	lists    int
	renews   []string
}

func (r *recordingLeaseRepository) ListByHolder(_ context.Context, _ string) ([]storage.VPNIPLease, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lists++
	if r.listErr != nil {
		return nil, r.listErr
	}
	return append([]storage.VPNIPLease(nil), r.held...), nil
}

func (r *recordingLeaseRepository) Renew(_ context.Context, nodeID, subnet, holder string, epoch int64, ttl time.Duration) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.renews = append(r.renews, strings.Join([]string{
		nodeID, subnet, holder, strconv.FormatInt(epoch, 10), ttl.String(),
	}, "|"))
	return r.renewErr
}

func (r *recordingLeaseRepository) renewCalls() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.renews...)
}

func (r *recordingLeaseRepository) renewCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.renews)
}

func (r *recordingLeaseRepository) listCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lists
}

// vpnLeaseRow is one subnet this node holds. The epoch is carried through to
// Renew unchanged: an epoch the loop invented would fence the node out of its own
// lease, which is the failure D15 exists to describe.
func vpnLeaseRow(subnet string, epoch int64) storage.VPNIPLease {
	return storage.VPNIPLease{
		ID:          "lease-" + subnet,
		NodeID:      vpnLifecycleNodeID,
		Subnet:      subnet,
		LeaseHolder: vpnLifecycleNodeID,
		Epoch:       epoch,
	}
}

// newVPNLifecycleFixture builds a gateway whose two loops run fast enough to
// observe. Both intervals are fields rather than derived values so this is the
// only place a test has to reach into.
func newVPNLifecycleFixture(t *testing.T, cfg config.VPNConfig, leases storage.VPNIPLeaseRepository) *vpnGatewayFixture {
	t.Helper()
	fixture := newVPNGatewayFixture(t, cfg, func(deps *VPNGatewayDeps) { deps.Leases = leases })
	fixture.gateway.leaseRenewInterval = vpnLifecycleLoopInterval
	fixture.gateway.idleReapInterval = vpnLifecycleLoopInterval
	return fixture
}

// vpnLifecycleFlow registers one flow directly in the table.
//
// The lifecycle under test is what happens to a flow it did not create, so
// building one through the packet path would only add a handshake to assert
// something about teardown.
func vpnLifecycleFlow(t *testing.T, gateway *vpnGateway, peerID string, onClose func()) *vpnFlow {
	t.Helper()
	flow, created, err := gateway.flows.register(vpnFlowRegistration{
		Key: vpnFlowKey{
			PeerID:   peerID,
			Protocol: "tcp",
			Src:      netip.MustParseAddrPort(vpnPipePeerIP + ":51000"),
			Dst:      netip.MustParseAddrPort(vpnPipeServiceIP + ":443"),
		},
		Target:  vpnPipeServiceIP,
		Port:    443,
		OnClose: onClose,
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if !created {
		t.Fatal("the flow key was already registered")
	}
	return flow
}

func waitForVPN(t *testing.T, description string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(vpnLifecycleWait)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s", vpnLifecycleWait, description)
}

func vpnShutdownStepsOf(gateway *vpnGateway) string {
	return strings.Join(gateway.shutdownSteps(), " > ")
}

// The two loops are the gateway's only background work, and both must stop when
// Close returns: a reaper that outlived the gateway would touch a closed stack,
// and a renewal that outlived it would keep a subnet leased that the node no
// longer serves, which is precisely the lease a replacement node needs.
func TestVPNLifecycleStartsAndStopsTheBackgroundLoops(t *testing.T) {
	leases := &recordingLeaseRepository{held: []storage.VPNIPLease{vpnLeaseRow(vpnLifecycleSubnet, 3)}}
	fixture := newVPNLifecycleFixture(t, enabledVPNTestConfig(t), leases)
	gateway := fixture.gateway

	if err := gateway.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	for _, loop := range []struct {
		name string
		done chan struct{}
	}{{"the idle reaper", gateway.reaperDone}, {"the lease renewal", gateway.renewalDone}} {
		select {
		case <-loop.done:
			t.Fatalf("%s exited as soon as it started", loop.name)
		default:
		}
	}
	waitForVPN(t, "the renewal loop's first pass", func() bool { return leases.listCount() > 0 })

	// A second Start must not install a second pair. The channels are created once
	// at construction, so a loop launched twice would either replace them or close
	// one of them twice, and the identity check below plus the Close that follows
	// catch both.
	renewalDone := gateway.renewalDone
	reaperDone := gateway.reaperDone
	if err := gateway.Start(context.Background()); err != nil {
		t.Fatalf("a second Start returned %v, want nil", err)
	}
	if gateway.renewalDone != renewalDone || gateway.reaperDone != reaperDone {
		t.Error("a second Start replaced the background loops' done channels")
	}

	if err := gateway.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	waitForVPN(t, "the idle reaper to stop", func() bool { return channelClosed(gateway.reaperDone) })
	waitForVPN(t, "the lease renewal to stop", func() bool { return channelClosed(gateway.renewalDone) })
	renewals := leases.renewCount()
	lists := leases.listCount()
	time.Sleep(50 * time.Millisecond)
	if leases.renewCount() != renewals || leases.listCount() != lists {
		t.Error("a background loop kept running after Close returned")
	}
}

func channelClosed(done chan struct{}) bool {
	select {
	case <-done:
		return true
	default:
		return false
	}
}

// D14 fixes the teardown order because each step makes the next one safe. Closing
// the device before the flows would leave a splice writing into a tunnel that no
// longer exists; closing the flow table before the listeners would answer a SYN
// with a registration error that reads as a capacity problem.
func TestVPNLifecycleCloseFollowsTheDocumentedOrder(t *testing.T) {
	fixture := newVPNGatewayFixture(t, enabledVPNTestConfig(t), nil)
	gateway := fixture.gateway
	if err := gateway.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	if err := gateway.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if got, want := vpnShutdownStepsOf(gateway), strings.Join(vpnShutdownStepOrder, " > "); got != want {
		t.Errorf("teardown ran\n%s\nwant\n%s", got, want)
	}
}

// A gateway closed before it ever served must run the same teardown. The failure
// paths of construction and Start both call it, and a step that assumed a bound
// socket would panic on the one path an operator cannot retry.
func TestVPNLifecycleCloseWithoutStartRunsTheSameTeardown(t *testing.T) {
	fixture := newVPNGatewayFixture(t, enabledVPNTestConfig(t), nil)
	gateway := fixture.gateway

	if err := gateway.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got, want := vpnShutdownStepsOf(gateway), strings.Join(vpnShutdownStepOrder, " > "); got != want {
		t.Errorf("teardown ran\n%s\nwant\n%s", got, want)
	}
	// Close is reached from the serve shutdown chain and from the runtime's own
	// Close, and the two can overlap. The second call has to be a no-op rather
	// than a second teardown: the record proves it ran exactly once.
	if err := gateway.Close(); err != nil {
		t.Fatalf("a second Close returned %v, want nil", err)
	}
	if got := len(gateway.shutdownSteps()); got != len(vpnShutdownStepOrder) {
		t.Errorf("a second Close recorded %d steps, want %d", got, len(vpnShutdownStepOrder))
	}
}

// Draining is what makes the wait in step three meaningful: without it a new flow
// could be registered after the listeners were closed and before the timeout
// expired, and the gateway would then force-close a connection it had just
// accepted.
func TestVPNLifecycleDrainRefusesNewFlowsAsCapacity(t *testing.T) {
	fixture := newVPNGatewayFixture(t, enabledVPNTestConfig(t), nil)
	fixture.applyPeer(t, nil)
	fixture.gateway.beginDrain()

	if !fixture.gateway.flows.isDraining() {
		t.Fatal("the flow table did not enter the draining state")
	}
	fixture.gateway.handlePacket(vpnPipePacket(vpnTestProtocolUDP, vpnPipeServiceIP, 53, []byte("query")))

	if got := fixture.metrics.countDroppedWith("udp", vpn.ClassCapacityExhausted); got != 1 {
		t.Errorf("capacity_exhausted was counted %d times for udp, want 1 (dropped: %v)", got, fixture.metrics.droppedRecords())
	}
	if got := fixture.opener.openCount(); got != 0 {
		t.Errorf("a draining gateway opened %d agent streams, want none", got)
	}
}

// The shutdown timeout is a promise to in-flight traffic, not a delay: a flow
// that is still carrying bytes gets the whole window to finish on its own.
func TestVPNLifecycleKeepsInFlightFlowsUntilTheShutdownTimeout(t *testing.T) {
	cfg := enabledVPNTestConfig(t)
	cfg.ShutdownTimeout = 150 * time.Millisecond
	fixture := newVPNGatewayFixture(t, cfg, nil)
	gateway := fixture.gateway
	if err := gateway.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	closed := make(chan struct{})
	flow := vpnLifecycleFlow(t, gateway, "peer-a", func() { close(closed) })

	started := time.Now()
	if err := gateway.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	elapsed := time.Since(started)

	if elapsed < cfg.ShutdownTimeout {
		t.Errorf("Close returned after %s, before the %s shutdown timeout elapsed", elapsed, cfg.ShutdownTimeout)
	}
	if elapsed > cfg.ShutdownTimeout+10*time.Second {
		t.Errorf("Close took %s for a %s timeout", elapsed, cfg.ShutdownTimeout)
	}
	select {
	case <-closed:
	default:
		t.Error("the in-flight flow was not closed by the forced step")
	}
	if !flow.Closed() {
		t.Error("the flow is still registered after Close")
	}
	if got := gateway.flows.count(); got != 0 {
		t.Errorf("the gateway holds %d flows after Close, want 0", got)
	}
}

// Zero means "do not wait", which is what a supervisor with its own stop timeout
// needs: the gateway must not add a second, hidden budget on top of it.
func TestVPNLifecycleClosesImmediatelyWhenTheShutdownTimeoutIsZero(t *testing.T) {
	cfg := enabledVPNTestConfig(t)
	cfg.ShutdownTimeout = 0
	fixture := newVPNGatewayFixture(t, cfg, nil)
	gateway := fixture.gateway
	closed := make(chan struct{})
	flow := vpnLifecycleFlow(t, gateway, "peer-a", func() { close(closed) })

	started := time.Now()
	if err := gateway.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Errorf("Close waited %s with shutdown_timeout=0", elapsed)
	}
	select {
	case <-closed:
	default:
		t.Error("the in-flight flow survived a Close that was not allowed to wait")
	}
	if !flow.Closed() {
		t.Error("the flow is still registered after Close")
	}
}

// The reaper is the only thing that gives a listener and a flow back after a peer
// disappears without closing either, so it has to be proven to run rather than
// merely to exist.
func TestVPNLifecycleReapsIdleFlowsInTheBackground(t *testing.T) {
	cfg := enabledVPNTestConfig(t)
	cfg.IdleTimeout = 20 * time.Millisecond
	fixture := newVPNLifecycleFixture(t, cfg, nil)
	gateway := fixture.gateway
	reaped := make(chan struct{})
	vpnLifecycleFlow(t, gateway, "peer-a", func() { close(reaped) })

	if err := gateway.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// The fixture's clock only moves when a test moves it, so the flow is idle the
	// moment it is registered until this advance says otherwise.
	fixture.clock.Advance(time.Second)

	select {
	case <-reaped:
	case <-time.After(vpnLifecycleWait):
		t.Fatalf("the idle reaper did not close a flow idle for %s within %s", cfg.IdleTimeout, vpnLifecycleWait)
	}
	if got := gateway.flows.count(); got != 0 {
		t.Errorf("the table holds %d flows after the reaper ran, want 0", got)
	}
}

// D15: renewal is periodic, off the packet path, and carries the fencing fields
// the repository compares against. A renewal that invented an epoch or dropped the
// holder would lose the subnet on the first pass.
func TestVPNLifecycleRenewsTheHeldSubnetLease(t *testing.T) {
	leases := &recordingLeaseRepository{held: []storage.VPNIPLease{vpnLeaseRow(vpnLifecycleSubnet, 7)}}
	fixture := newVPNLifecycleFixture(t, enabledVPNTestConfig(t), leases)
	gateway := fixture.gateway

	if err := gateway.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForVPN(t, "the first renewal", func() bool { return leases.renewCount() > 0 })

	want := strings.Join([]string{vpnLifecycleNodeID, vpnLifecycleSubnet, vpnLifecycleNodeID, "7", vpnSubnetLeaseTTL.String()}, "|")
	if got := leases.renewCalls()[0]; got != want {
		t.Errorf("Renew was called with %q, want %q", got, want)
	}
	waitForVPN(t, "a second renewal pass", func() bool { return leases.renewCount() > 1 })
	if records := fixture.metrics.leaseRecords(); !vpnLeaseRecorded(records, "vpn_subnet_renew/success/") {
		t.Errorf("no successful renewal was recorded: %v", records)
	}
}

// D15's second half: a lost fence stops the renewal of that subnet and nothing
// else. Closing the peers or the flows would turn one database disagreement into
// an outage for traffic that is already being served.
func TestVPNLifecycleStopsRenewingALostSubnetLease(t *testing.T) {
	leases := &recordingLeaseRepository{
		held:     []storage.VPNIPLease{vpnLeaseRow(vpnLifecycleSubnet, 2)},
		renewErr: storage.ErrVPNIPLeaseStaleEpoch,
	}
	cfg := enabledVPNTestConfig(t)
	cfg.ShutdownTimeout = 0
	fixture := newVPNLifecycleFixture(t, cfg, leases)
	gateway := fixture.gateway
	fixture.applyPeer(t, nil)
	flow := vpnLifecycleFlow(t, gateway, "peer-a", nil)

	if err := gateway.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForVPN(t, "the fenced renewal", func() bool { return leases.renewCount() > 0 })
	waitForVPN(t, "the stale epoch metric", func() bool {
		return vpnLeaseRecorded(fixture.metrics.leaseRecords(), "vpn_subnet_renew/failed/stale_epoch")
	})

	// Several intervals pass with the fence still closed. The loop must give up on
	// this subnet rather than retry a compare-and-set that can never succeed.
	time.Sleep(20 * vpnLifecycleLoopInterval)
	if got := leases.renewCount(); got != 1 {
		t.Errorf("Renew was called %d times after the epoch went stale, want 1", got)
	}
	if flow.Closed() {
		t.Error("a lost lease fence closed an in-flight flow")
	}
	if got := gateway.flows.count(); got != 1 {
		t.Errorf("the gateway holds %d flows after losing the fence, want 1", got)
	}
	if got := gateway.peers.count(); got != 1 {
		t.Errorf("the gateway holds %d peers after losing the fence, want 1", got)
	}
}

// The gateway's own tunnel address is the one address it must refuse, and it is
// not derivable from the configuration: which subnet this node holds is decided
// by the lease it acquired at issue time.
func TestVPNLifecycleLearnsTheNodeAddressFromTheSubnetLease(t *testing.T) {
	leases := &recordingLeaseRepository{held: []storage.VPNIPLease{vpnLeaseRow(vpnLifecycleSubnet, 1)}}
	fixture := newVPNLifecycleFixture(t, enabledVPNTestConfig(t), leases)
	gateway := fixture.gateway
	// The fixture installs the packet-pipeline test's own address. A fresh process
	// starts without one, which is the state this assertion is about.
	gateway.setSelfAddress(netip.Addr{})

	if err := gateway.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	nodeIP := netip.MustParseAddr(vpnLifecycleNodeIP)
	waitForVPN(t, "the node address to be learned from the lease", func() bool { return gateway.isSelfAddress(nodeIP) })

	// Learning is idempotent: a second pass must not move the address to another
	// subnet this node may hold later, because every peer already issued lives in
	// the first one.
	gateway.setSelfAddress(nodeIP)
	time.Sleep(10 * vpnLifecycleLoopInterval)
	if !gateway.isSelfAddress(nodeIP) {
		t.Error("a later renewal pass replaced the learned node address")
	}
}

// A repository the loop cannot read is a degraded gateway, not a stopped one: the
// peers it already loaded keep being served, and the next pass may well succeed.
func TestVPNLifecycleSurvivesALeaseRepositoryItCannotRead(t *testing.T) {
	leases := &recordingLeaseRepository{listErr: context.DeadlineExceeded}
	fixture := newVPNLifecycleFixture(t, enabledVPNTestConfig(t), leases)
	gateway := fixture.gateway
	fixture.applyPeer(t, nil)

	if err := gateway.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForVPN(t, "the renewal loop to attempt a read", func() bool { return leases.listCount() > 1 })
	if got := gateway.peers.count(); got != 1 {
		t.Errorf("the gateway dropped to %d peers after a failed lease read, want 1", got)
	}
	if err := gateway.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

func vpnLeaseRecorded(records []string, want string) bool {
	for _, record := range records {
		if record == want {
			return true
		}
	}
	return false
}
