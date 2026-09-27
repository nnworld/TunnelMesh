package server

import (
	"context"
	"errors"
	"io"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/relay"
)

// vpnFlowTestKey builds one flow key. The addresses are fixed because a flow is
// identified by its four-tuple plus the peer that owns it, and a test that varied
// them would be testing the map rather than the accounting.
func vpnFlowTestKey(peerID string, dstPort uint16, srcPort uint16) vpnFlowKey {
	return vpnFlowKey{
		PeerID:   peerID,
		Protocol: "tcp",
		Src:      netip.MustParseAddrPort("10.64.0.7:" + itoa(int(srcPort))),
		Dst:      netip.MustParseAddrPort("192.168.1.20:" + itoa(int(dstPort))),
	}
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := ""
	for value > 0 {
		digits = string(rune('0'+value%10)) + digits
		value /= 10
	}
	return digits
}

// controllableClock is the injected clock the idle reaper and the token bucket
// read. A test that slept for real would be slow and would flake under -race.
type controllableClock struct {
	mu      sync.Mutex
	current time.Time
}

func newControllableClock(start time.Time) *controllableClock {
	return &controllableClock{current: start}
}

func (c *controllableClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.current
}

func (c *controllableClock) Advance(delta time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.current = c.current.Add(delta)
}

func newVPNFlowTestFixture(t *testing.T, cfg vpnFlowTableConfig) (*vpnFlowTable, *controllableClock) {
	t.Helper()
	clock := newControllableClock(time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC))
	if cfg.Now == nil {
		cfg.Now = clock.Now
	}
	table := newVPNFlowTable(cfg)
	t.Cleanup(func() { table.close() })
	return table, clock
}

func TestVPNFlowTableRegistersLooksUpAndRemoves(t *testing.T) {
	table, _ := newVPNFlowTestFixture(t, vpnFlowTableConfig{MaxFlowsPerPeer: 4})
	key := vpnFlowTestKey("peer-a", 8080, 41000)
	closed := 0

	flow, created, err := table.register(vpnFlowRegistration{
		Key:     key,
		Target:  "192.168.1.20",
		Port:    8080,
		OnClose: func() { closed++ },
	})
	if err != nil {
		t.Fatalf("register() error = %v, want nil", err)
	}
	if !created {
		t.Error("created = false, want true for a new key")
	}
	if flow.ID() == "" {
		t.Error("flow ID is empty, want a stable identifier")
	}
	if got, ok := table.lookup(key); !ok || got != flow {
		t.Errorf("lookup() = %v, %v; want the registered flow", got, ok)
	}
	if table.count() != 1 || table.countPeer("peer-a") != 1 {
		t.Errorf("counts = %d/%d, want 1/1", table.count(), table.countPeer("peer-a"))
	}

	table.remove(key)
	if _, ok := table.lookup(key); ok {
		t.Error("lookup() still finds a removed flow")
	}
	if closed != 1 {
		t.Errorf("OnClose ran %d times, want exactly 1", closed)
	}
	if table.count() != 0 {
		t.Errorf("count() = %d after removal, want 0", table.count())
	}
	// Removing twice must be harmless: the reaper and an explicit close both call
	// it and neither can know whether the other already did.
	table.remove(key)
	if closed != 1 {
		t.Errorf("OnClose ran %d times after a second remove, want 1", closed)
	}
}

func TestVPNFlowTableRegisterIsIdempotentPerKey(t *testing.T) {
	table, _ := newVPNFlowTestFixture(t, vpnFlowTableConfig{MaxFlowsPerPeer: 4})
	key := vpnFlowTestKey("peer-a", 8080, 41000)

	first, created, err := table.register(vpnFlowRegistration{Key: key, Target: "192.168.1.20", Port: 8080})
	if err != nil || !created {
		t.Fatalf("first register = %v, %v; want created", created, err)
	}
	second, created, err := table.register(vpnFlowRegistration{Key: key, Target: "192.168.1.20", Port: 8080})
	if err != nil {
		t.Fatalf("second register error = %v, want nil", err)
	}
	if created {
		t.Error("created = true on a duplicate key, want false")
	}
	if second != first {
		t.Error("a duplicate registration produced a second flow")
	}
	if table.count() != 1 {
		t.Errorf("count() = %d, want 1", table.count())
	}
}

func TestVPNFlowTableConcurrentRegistrationOfOneKeyYieldsOneFlow(t *testing.T) {
	table, _ := newVPNFlowTestFixture(t, vpnFlowTableConfig{MaxFlowsPerPeer: 64})
	key := vpnFlowTestKey("peer-a", 8080, 41000)

	const racers = 32
	var start sync.WaitGroup
	var done sync.WaitGroup
	start.Add(1)
	created := make(chan bool, racers)
	flows := make(chan *vpnFlow, racers)
	for range racers {
		done.Add(1)
		go func() {
			defer done.Done()
			start.Wait()
			flow, wasCreated, err := table.register(vpnFlowRegistration{Key: key, Target: "192.168.1.20", Port: 8080})
			if err != nil {
				created <- false
				flows <- nil
				return
			}
			created <- wasCreated
			flows <- flow
		}()
	}
	start.Done()
	done.Wait()
	close(created)
	close(flows)

	winners := 0
	var winner *vpnFlow
	for wasCreated := range created {
		if wasCreated {
			winners++
		}
	}
	for flow := range flows {
		if flow == nil {
			t.Fatal("a concurrent registration returned an error")
		}
		if winner == nil {
			winner = flow
		} else if flow != winner {
			t.Fatal("concurrent registrations produced two different flows for one key")
		}
	}
	if winners != 1 {
		t.Errorf("%d racers reported creating the flow, want exactly 1", winners)
	}
	if table.count() != 1 {
		t.Errorf("count() = %d, want 1", table.count())
	}
}

func TestVPNFlowTableEnforcesThePerPeerCeiling(t *testing.T) {
	table, _ := newVPNFlowTestFixture(t, vpnFlowTableConfig{MaxFlowsPerPeer: 2})

	for port := uint16(1); port <= 2; port++ {
		if _, _, err := table.register(vpnFlowRegistration{Key: vpnFlowTestKey("peer-a", 8080, port), Target: "192.168.1.20", Port: 8080}); err != nil {
			t.Fatalf("register flow %d: %v", port, err)
		}
	}
	_, _, err := table.register(vpnFlowRegistration{Key: vpnFlowTestKey("peer-a", 8080, 3), Target: "192.168.1.20", Port: 8080})
	if !errors.Is(err, errVPNFlowCapacity) {
		t.Fatalf("third flow error = %v, want %v", err, errVPNFlowCapacity)
	}
	// Another peer is unaffected: the ceiling is per peer, not per gateway.
	if _, _, err := table.register(vpnFlowRegistration{Key: vpnFlowTestKey("peer-b", 8080, 1), Target: "192.168.1.20", Port: 8080}); err != nil {
		t.Errorf("a different peer was refused: %v", err)
	}
}

func TestVPNFlowTableHonoursAPeerSpecificCeiling(t *testing.T) {
	table, _ := newVPNFlowTestFixture(t, vpnFlowTableConfig{MaxFlowsPerPeer: 8})

	if _, _, err := table.register(vpnFlowRegistration{Key: vpnFlowTestKey("peer-a", 8080, 1), Target: "192.168.1.20", Port: 8080, PeerLimit: 1}); err != nil {
		t.Fatalf("first flow error = %v, want nil", err)
	}
	_, _, err := table.register(vpnFlowRegistration{Key: vpnFlowTestKey("peer-a", 8080, 2), Target: "192.168.1.20", Port: 8080, PeerLimit: 1})
	if !errors.Is(err, errVPNFlowCapacity) {
		t.Fatalf("second flow error = %v, want %v", err, errVPNFlowCapacity)
	}
}

func TestVPNFlowTableEnforcesTheGlobalCeiling(t *testing.T) {
	table, _ := newVPNFlowTestFixture(t, vpnFlowTableConfig{MaxFlowsPerPeer: 8, MaxFlowsTotal: 2})

	for port := uint16(1); port <= 2; port++ {
		peer := "peer-" + itoa(int(port))
		if _, _, err := table.register(vpnFlowRegistration{Key: vpnFlowTestKey(peer, 8080, port), Target: "192.168.1.20", Port: 8080}); err != nil {
			t.Fatalf("register flow %d: %v", port, err)
		}
	}
	_, _, err := table.register(vpnFlowRegistration{Key: vpnFlowTestKey("peer-3", 8080, 3), Target: "192.168.1.20", Port: 8080})
	if !errors.Is(err, errVPNFlowCapacity) {
		t.Fatalf("third flow error = %v, want %v", err, errVPNFlowCapacity)
	}
}

func TestVPNFlowTableZeroCeilingMeansUnlimited(t *testing.T) {
	table, _ := newVPNFlowTestFixture(t, vpnFlowTableConfig{})

	for port := uint16(1); port <= 64; port++ {
		if _, _, err := table.register(vpnFlowRegistration{Key: vpnFlowTestKey("peer-a", 8080, port), Target: "192.168.1.20", Port: 8080}); err != nil {
			t.Fatalf("register flow %d: %v", port, err)
		}
	}
	if table.count() != 64 {
		t.Errorf("count() = %d, want 64", table.count())
	}
}

func TestVPNFlowTableReapsOnlyIdleFlows(t *testing.T) {
	table, clock := newVPNFlowTestFixture(t, vpnFlowTableConfig{MaxFlowsPerPeer: 8, IdleTimeout: 30 * time.Second})

	stale, _, err := table.register(vpnFlowRegistration{Key: vpnFlowTestKey("peer-a", 8080, 1), Target: "192.168.1.20", Port: 8080})
	if err != nil {
		t.Fatalf("register stale flow: %v", err)
	}
	live, _, err := table.register(vpnFlowRegistration{Key: vpnFlowTestKey("peer-a", 8080, 2), Target: "192.168.1.20", Port: 8080})
	if err != nil {
		t.Fatalf("register live flow: %v", err)
	}

	clock.Advance(20 * time.Second)
	live.Touch()
	clock.Advance(15 * time.Second) // stale is 35s idle, live is 15s

	if reaped := table.reapIdle(); reaped != 1 {
		t.Errorf("reapIdle() = %d, want 1", reaped)
	}
	if !stale.Closed() {
		t.Error("the idle flow was not closed")
	}
	if live.Closed() {
		t.Error("a flow that was touched inside the window was closed")
	}
	if _, ok := table.lookup(vpnFlowTestKey("peer-a", 8080, 1)); ok {
		t.Error("the reaped flow is still registered")
	}
}

func TestVPNFlowTableSnapshotMatchesTheAPIContract(t *testing.T) {
	table, clock := newVPNFlowTestFixture(t, vpnFlowTableConfig{MaxFlowsPerPeer: 8})
	startedAt := clock.Now()

	flow, _, err := table.register(vpnFlowRegistration{Key: vpnFlowTestKey("peer-a", 8080, 41000), Target: "192.168.1.20", Port: 8080})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	flow.AddSent(120)
	flow.AddReceived(3400)
	// A different peer must not appear in the snapshot: the API answers for one
	// peer and leaking another peer's targets would be a cross-tenant read.
	if _, _, err := table.register(vpnFlowRegistration{Key: vpnFlowTestKey("peer-b", 22, 41001), Target: "10.0.0.5", Port: 22}); err != nil {
		t.Fatalf("register other peer: %v", err)
	}

	items := table.snapshot("peer-a")
	if len(items) != 1 {
		t.Fatalf("snapshot() returned %d items, want 1", len(items))
	}
	got := items[0]
	if got.ID != flow.ID() {
		t.Errorf("ID = %q, want %q", got.ID, flow.ID())
	}
	if got.Protocol != "tcp" {
		t.Errorf("Protocol = %q, want %q", got.Protocol, "tcp")
	}
	if got.Target != "192.168.1.20" {
		t.Errorf("Target = %q, want %q", got.Target, "192.168.1.20")
	}
	if got.Port != 8080 {
		t.Errorf("Port = %d, want 8080", got.Port)
	}
	if !got.StartedAt.Equal(startedAt) {
		t.Errorf("StartedAt = %v, want %v", got.StartedAt, startedAt)
	}
	if got.BytesSent != 120 || got.BytesReceived != 3400 {
		t.Errorf("bytes = %d/%d, want 120/3400", got.BytesSent, got.BytesReceived)
	}
	if len(table.snapshot("peer-missing")) != 0 {
		t.Error("a peer with no flows must produce an empty snapshot, not nil-vs-empty confusion")
	}
}

func TestVPNFlowTableClosePeerStopsEveryFlowOfThatPeer(t *testing.T) {
	table, _ := newVPNFlowTestFixture(t, vpnFlowTableConfig{MaxFlowsPerPeer: 8})
	closed := map[string]int{}
	var mu sync.Mutex
	record := func(peerID string) func() {
		return func() {
			mu.Lock()
			closed[peerID]++
			mu.Unlock()
		}
	}
	for port := uint16(1); port <= 3; port++ {
		if _, _, err := table.register(vpnFlowRegistration{Key: vpnFlowTestKey("peer-a", 8080, port), Target: "192.168.1.20", Port: 8080, OnClose: record("peer-a")}); err != nil {
			t.Fatalf("register: %v", err)
		}
	}
	if _, _, err := table.register(vpnFlowRegistration{Key: vpnFlowTestKey("peer-b", 8080, 9), Target: "192.168.1.20", Port: 8080, OnClose: record("peer-b")}); err != nil {
		t.Fatalf("register other peer: %v", err)
	}

	if stopped := table.closePeer("peer-a"); stopped != 3 {
		t.Errorf("closePeer() = %d, want 3", stopped)
	}
	mu.Lock()
	defer mu.Unlock()
	if closed["peer-a"] != 3 {
		t.Errorf("peer-a OnClose ran %d times, want 3", closed["peer-a"])
	}
	if closed["peer-b"] != 0 {
		t.Error("closing one peer stopped another peer's flow")
	}
	if table.count() != 1 {
		t.Errorf("count() = %d, want 1 for the surviving peer", table.count())
	}
}

func TestVPNFlowTableRefusesRegistrationAfterClose(t *testing.T) {
	table, _ := newVPNFlowTestFixture(t, vpnFlowTableConfig{MaxFlowsPerPeer: 4})
	flow, _, err := table.register(vpnFlowRegistration{Key: vpnFlowTestKey("peer-a", 8080, 1), Target: "192.168.1.20", Port: 8080})
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	if stopped := table.close(); stopped != 1 {
		t.Errorf("close() = %d, want 1", stopped)
	}
	if !flow.Closed() {
		t.Error("close() left a flow open")
	}
	if _, _, err := table.register(vpnFlowRegistration{Key: vpnFlowTestKey("peer-a", 8080, 2), Target: "192.168.1.20", Port: 8080}); !errors.Is(err, errVPNFlowTableClosed) {
		t.Errorf("register after close = %v, want %v", err, errVPNFlowTableClosed)
	}
	// Close is idempotent: the shutdown path and the runtime both call it.
	if stopped := table.close(); stopped != 0 {
		t.Errorf("second close() = %d, want 0", stopped)
	}
}

// blockingOpener is a relay transport whose OpenStream waits for a test to say
// so, which is how the connect-timeout path is exercised without sleeping.
type blockingOpener struct {
	release chan struct{}
	request relay.StreamRequest
	closed  chan struct{}
	err     error
}

func newBlockingOpener(err error) *blockingOpener {
	return &blockingOpener{release: make(chan struct{}), closed: make(chan struct{}, 1), err: err}
}

func (o *blockingOpener) OpenStream(_ context.Context, request relay.StreamRequest) (io.ReadWriteCloser, error) {
	o.request = request
	<-o.release
	if o.err != nil {
		return nil, o.err
	}
	return &trackingStream{closed: o.closed}, nil
}

func (o *blockingOpener) Close() error { return nil }

// trackingStream reports whether it was closed, which is what makes a leaked
// late-arriving stream visible to a test.
type trackingStream struct {
	closed chan struct{}
	once   sync.Once
}

func (s *trackingStream) Read([]byte) (int, error)  { return 0, io.EOF }
func (s *trackingStream) Write([]byte) (int, error) { return 0, io.EOF }
func (s *trackingStream) Close() error {
	s.once.Do(func() {
		if s.closed != nil {
			select {
			case s.closed <- struct{}{}:
			default:
			}
		}
	})
	return nil
}

func TestOpenVPNEgressRefusesWithoutATransport(t *testing.T) {
	stream, cancel, err := openVPNEgress(context.Background(), nil, relay.StreamRequest{Protocol: "tcp"}, time.Second)
	if !errors.Is(err, errVPNEgressUnavailable) {
		t.Fatalf("error = %v, want %v", err, errVPNEgressUnavailable)
	}
	if stream != nil {
		t.Error("a stream was returned alongside an error")
	}
	if cancel == nil {
		t.Fatal("cancel = nil, want a callable no-op so a caller can defer it unconditionally")
	}
	cancel()
}

func TestOpenVPNEgressPropagatesAnOpenFailure(t *testing.T) {
	opener := newBlockingOpener(errors.New("agent is gone"))
	close(opener.release)

	_, cancel, err := openVPNEgress(context.Background(), opener, relay.StreamRequest{Protocol: "tcp", AgentID: "agent-a"}, time.Second)
	defer cancel()
	if !errors.Is(err, errVPNEgressUnavailable) {
		t.Fatalf("error = %v, want %v", err, errVPNEgressUnavailable)
	}
}

func TestOpenVPNEgressTimesOutAndClosesTheLateStream(t *testing.T) {
	opener := newBlockingOpener(nil)

	stream, cancel, err := openVPNEgress(context.Background(), opener, relay.StreamRequest{Protocol: "tcp", AgentID: "agent-a"}, 20*time.Millisecond)
	if !errors.Is(err, errVPNEgressTimeout) {
		t.Fatalf("error = %v, want %v", err, errVPNEgressTimeout)
	}
	if stream != nil {
		t.Error("a stream was returned alongside a timeout")
	}
	if cancel == nil {
		t.Fatal("cancel = nil, want a callable func")
	}
	cancel()

	// The dial is still in flight. When it finally succeeds the gateway must not
	// leave an agent-side connection open that nobody will ever read from.
	close(opener.release)
	select {
	case <-opener.closed:
	case <-time.After(2 * time.Second):
		t.Fatal("a stream that arrived after the timeout was not closed")
	}
}

func TestOpenVPNEgressReturnsAUsableStream(t *testing.T) {
	opener := newBlockingOpener(nil)
	close(opener.release)

	request := relay.StreamRequest{Protocol: "udp", AgentID: "agent-a", TargetHost: "192.168.1.20", TargetPort: 53}
	stream, cancel, err := openVPNEgress(context.Background(), opener, request, time.Second)
	if err != nil {
		t.Fatalf("openVPNEgress() error = %v, want nil", err)
	}
	defer cancel()
	if stream == nil {
		t.Fatal("stream = nil, want a usable stream")
	}
	if opener.request.Protocol != "udp" || opener.request.TargetPort != 53 {
		t.Errorf("the request was not forwarded verbatim: %+v", opener.request)
	}
	if err := stream.Close(); err != nil {
		t.Errorf("Close() error = %v, want nil", err)
	}
}

func TestOpenVPNEgressHonoursCallerCancellation(t *testing.T) {
	opener := newBlockingOpener(nil)
	ctx, cancelCtx := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		_, cancel, err := openVPNEgress(ctx, opener, relay.StreamRequest{Protocol: "tcp"}, time.Minute)
		if cancel != nil {
			cancel()
		}
		done <- err
	}()
	cancelCtx()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("openVPNEgress() returned no error after the caller cancelled")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("openVPNEgress did not return after the caller cancelled")
	}
	close(opener.release)
	select {
	case <-opener.closed:
	case <-time.After(2 * time.Second):
		t.Fatal("a stream that arrived after cancellation was not closed")
	}
}
