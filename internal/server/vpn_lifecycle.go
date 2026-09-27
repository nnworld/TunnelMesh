//go:build vpn

package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"strings"
	"time"

	"golang.zx2c4.com/wireguard/device"

	"github.com/tunnelmesh/tunnelmesh/internal/storage"
	"github.com/tunnelmesh/tunnelmesh/internal/vpn"
)

const (
	// vpnShutdownPollInterval is how often the teardown looks at the flow table
	// while it waits out shutdown_timeout. It is a poll rather than a signal
	// because a flow can end for four different reasons - the peer closing, the
	// agent closing, the idle reaper and a revocation - and a counter that had to
	// be told about each of them would be a fourth place to get teardown wrong.
	vpnShutdownPollInterval = 20 * time.Millisecond
	// vpnLeaseOperationTimeout bounds one lease read or renewal. The loop is
	// off the packet path, so a slow database costs a renewal rather than a flow.
	vpnLeaseOperationTimeout = 10 * time.Second
	// vpnBackgroundStopTimeout bounds how long the teardown waits for a loop that
	// was told to stop. See stopBackground for why exceeding it is logged rather
	// than returned.
	vpnBackgroundStopTimeout = 5 * time.Second
	// vpnDenialFlushTimeout bounds the final audit write.
	vpnDenialFlushTimeout = 5 * time.Second
	// vpnLeaseRenewOperation is the operation label the renewal reports under.
	// It is a constant because it is a metric label, and a label built from
	// anything a request carries is how a registry ends up unbounded.
	vpnLeaseRenewOperation = "vpn_subnet_renew"
	// vpnIdleReapFloor and vpnIdleReapCeiling bound how often the reaper runs.
	// Half the idle timeout is the interval that reaps a flow within one and a
	// half timeouts of its last packet; the floor keeps a one-second idle_timeout
	// from turning the sweep into a hot loop, and the ceiling keeps a day-long one
	// from leaving a listener claimed for twelve hours.
	vpnIdleReapFloor   = time.Second
	vpnIdleReapCeiling = time.Minute
)

// Start acquires the resources the gateway holds for the rest of the process
// lifetime and launches the two loops that maintain them.
//
// It either leaves a serving gateway or a closed one. Half-built is the state that
// cannot be reasoned about - a bound port answering no handshake, or a device with
// an identity and no peers - so every failure path closes the gateway and returns,
// and a caller only ever has to distinguish "serving" from "gone".
func (g *vpnGateway) Start(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := g.serve(ctx); err != nil {
		_ = g.Close()
		return err
	}
	g.startBackground()
	return nil
}

// serve is Start's body, split out so the failure path has one place to clean up.
func (g *vpnGateway) serve(ctx context.Context) error {
	g.mu.Lock()
	if g.started {
		g.mu.Unlock()
		return nil
	}
	if g.isClosed() {
		g.mu.Unlock()
		return errVPNGatewayClosed
	}
	bind, err := newVPNBind(g.deps.Config.Listen)
	if err != nil {
		g.mu.Unlock()
		return err
	}
	nodeConfig, err := g.renderNodeConfig(bind)
	if err != nil {
		g.mu.Unlock()
		return err
	}
	// The device is registered before it is configured so that a failure further
	// down still finds something to close. NewDevice spawns the read and encrypt
	// routines immediately, and they park on a tunnel that has not been written to
	// yet, which costs nothing until the endpoint is up.
	tunnel := device.NewDevice(g.device, bind, vpnDeviceLogger())
	g.bind = bind
	g.wg = tunnel
	g.mu.Unlock()

	if err := tunnel.IpcSet(nodeConfig); err != nil {
		return fmt.Errorf("vpn: configure the wireguard identity for server.vpn.listen %q: %w", g.deps.Config.Listen, err)
	}
	// Up is what actually binds the socket. wireguard-go only opens the bind for a
	// device that is up, so setting listen_port alone leaves the port unheld - and
	// a gateway that reports a listen address it is not listening on is worse than
	// one that refuses to start.
	if err := tunnel.Up(); err != nil {
		return fmt.Errorf("vpn: bring the wireguard endpoint up on server.vpn.listen %q: %w", g.deps.Config.Listen, err)
	}
	bound := bind.localPort()
	if bound == 0 {
		return fmt.Errorf("vpn: the wireguard endpoint came up on server.vpn.listen %q without a udp port", g.deps.Config.Listen)
	}
	if err := g.loadPeers(ctx); err != nil {
		return err
	}

	g.mu.Lock()
	g.boundPort = bound
	g.started = true
	g.mu.Unlock()
	slog.Info("vpn_gateway_started",
		"node_id", g.deps.NodeID,
		"listen", g.deps.Config.Listen,
		"port", bound,
		"mtu", g.mtu,
		"peers", g.peers.count())
	return nil
}

// startBackground launches the idle reaper and the lease renewal exactly once.
//
// Both loops are parented to bgCtx rather than to baseCtx on purpose. baseCtx
// bounds the streams the gateway opens and is cancelled by the forced step of the
// teardown, which is the moment traffic stops being served; a renewal that died
// with it would let the subnet lease expire during a long drain, and the node that
// took the subnet over would then be handing addresses to peers this gateway is
// still serving.
func (g *vpnGateway) startBackground() {
	g.bgOnce.Do(func() {
		g.bgMu.Lock()
		if g.bgStopped || g.isClosed() {
			// Close already ran. Launching now would leak a loop that nothing will
			// ever wait for, because the teardown that would have waited has passed.
			g.bgMu.Unlock()
			return
		}
		g.bgStarted = true
		g.bgMu.Unlock()
		go g.runIdleReaper(g.bgCtx)
		go g.runLeaseRenewal(g.bgCtx)
	})
}

// runIdleReaper gives back what a peer that vanished without closing left behind.
//
// Without it both registries grow for as long as the process runs: a flow whose
// agent side went away stays counted against the peer's ceiling, and the internal
// address its listener claimed stays claimed inside the stack.
func (g *vpnGateway) runIdleReaper(ctx context.Context) {
	defer close(g.reaperDone)
	interval := g.idleReapInterval
	if interval <= 0 {
		interval = vpnIdleReapInterval(g.deps.Config.IdleTimeout)
	}
	if interval <= 0 {
		// A deployment with no idle timeout has nothing to expire, so there is
		// nothing to sweep. The loop returns instead of ticking to no purpose, and
		// the done channel still closes so a teardown waiting on it cannot hang.
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			g.reapOnce()
		}
	}
}

// reapOnce is one sweep. It is a method rather than inline so the two registries
// it walks cannot be swept in one place and forgotten in the other.
func (g *vpnGateway) reapOnce() {
	if g.flows != nil {
		if reaped := g.flows.reapIdle(); reaped > 0 {
			slog.Info("vpn_flows_reaped", "node_id", g.deps.NodeID, "flows", reaped)
		}
	}
	if g.tcp != nil {
		if reclaimed := g.tcp.reapIdle(); reclaimed > 0 {
			slog.Info("vpn_listeners_reclaimed", "node_id", g.deps.NodeID, "listeners", reclaimed)
		}
	}
}

// vpnIdleReapInterval derives the sweep interval from the configured idle timeout.
func vpnIdleReapInterval(idle time.Duration) time.Duration {
	if idle <= 0 {
		return 0
	}
	interval := idle / 2
	if interval < vpnIdleReapFloor {
		return vpnIdleReapFloor
	}
	if interval > vpnIdleReapCeiling {
		return vpnIdleReapCeiling
	}
	return interval
}

// runLeaseRenewal keeps this node's claim on its subnet alive for as long as the
// gateway serves it.
//
// The interval is a third of the lease TTL, which leaves two missed renewals of
// headroom before another node may take the subnet over. That is the answer to the
// question the peer API's one-minute TTL raised: the TTL can be short because the
// renewal is periodic and never runs on the packet path.
func (g *vpnGateway) runLeaseRenewal(ctx context.Context) {
	defer close(g.renewalDone)
	if g.deps.Leases == nil {
		return
	}
	interval := g.leaseRenewInterval
	if interval <= 0 {
		interval = vpnLeaseRenewInterval(vpnSubnetLeaseTTL)
	}
	// Subnets this node has been fenced out of. The set only grows, and it is
	// owned by this loop alone, so it needs no lock.
	lost := make(map[string]struct{})
	// One pass before the first tick: a gateway that starts and then never issues
	// a peer still has to hold its lease, and the pass is also where the node's own
	// tunnel address is learned.
	g.renewHeldSubnets(ctx, lost)
	if interval <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			g.renewHeldSubnets(ctx, lost)
		}
	}
}

// vpnLeaseRenewInterval derives the renewal period from the lease TTL.
func vpnLeaseRenewInterval(ttl time.Duration) time.Duration {
	if ttl <= 0 {
		return 0
	}
	return ttl / 3
}

// renewHeldSubnets renews every subnet this node holds.
//
// A repository it cannot read is a degraded gateway rather than a stopped one: the
// peers already loaded keep being served, the node's own address stays as it was,
// and the next pass may well succeed. Only a fence - an epoch that no longer
// matches - is acted on, and even then only against the renewal itself.
func (g *vpnGateway) renewHeldSubnets(ctx context.Context, lost map[string]struct{}) {
	listCtx, cancelList := context.WithTimeout(ctx, vpnLeaseOperationTimeout)
	held, err := g.deps.Leases.ListByHolder(listCtx, g.deps.NodeID)
	cancelList()
	if err != nil {
		g.metrics.Lease(vpnLeaseRenewOperation, "failed", "list_failed")
		slog.Error("vpn_subnet_list_failed", "node_id", g.deps.NodeID, "error", err)
		return
	}
	for _, lease := range held {
		g.learnNodeAddress(lease.Subnet)
		if _, gone := lost[lease.Subnet]; gone {
			continue
		}
		g.renewSubnet(ctx, lease, lost)
	}
}

// renewSubnet extends one lease, carrying the fencing fields back unchanged.
//
// The holder and the epoch come from the row rather than from this node's
// configuration: Renew is a compare-and-set, and an epoch the loop invented would
// fence the node out of its own subnet on the first pass.
func (g *vpnGateway) renewSubnet(ctx context.Context, lease storage.VPNIPLease, lost map[string]struct{}) {
	renewCtx, cancel := context.WithTimeout(ctx, vpnLeaseOperationTimeout)
	err := g.deps.Leases.Renew(renewCtx, g.deps.NodeID, lease.Subnet, lease.LeaseHolder, lease.Epoch, vpnSubnetLeaseTTL)
	cancel()
	switch {
	case err == nil:
		g.metrics.Lease(vpnLeaseRenewOperation, "success", "")
	case errors.Is(err, storage.ErrVPNIPLeaseStaleEpoch):
		// Another node holds this subnet now. Renewing again can only fail the same
		// way, so the loop gives up on it - but the peers and flows already being
		// served are left alone. One database disagreement is not a reason to
		// black-hole traffic that is working, and new issues are fenced by the
		// repository rather than by this loop.
		lost[lease.Subnet] = struct{}{}
		g.metrics.Lease(vpnLeaseRenewOperation, "failed", "stale_epoch")
		slog.Error("vpn_subnet_lease_lost",
			"node_id", g.deps.NodeID,
			"subnet", lease.Subnet,
			"epoch", lease.Epoch,
			"action", "renewal stopped; in-flight peers and flows were left alone",
			"error", err)
	default:
		g.metrics.Lease(vpnLeaseRenewOperation, "failed", "error")
		slog.Error("vpn_subnet_renew_failed",
			"node_id", g.deps.NodeID,
			"subnet", lease.Subnet,
			"error", err)
	}
}

// learnNodeAddress records this node's own tunnel address, the first usable
// address of the subnet it holds.
//
// It is learned from the lease rather than derived from the configuration because
// which subnet this node carved out is decided when the first peer is issued, not
// when the process starts. The packet pipeline refuses traffic to this address: it
// is the gateway's identity on the tunnel and not an internal service, so handing
// it to an agent would buy one guaranteed connection failure per packet.
//
// Only the first successful learning counts. A node can hold more than one subnet
// over its lifetime, and every peer already issued lives in the first one, so
// moving the reserved address later would start refusing the wrong destination.
func (g *vpnGateway) learnNodeAddress(subnet string) {
	if g.hasSelfAddress() {
		return
	}
	pool, err := vpn.ParsePool(g.deps.Config.IPPool, g.deps.Config.NodeSubnetSize)
	if err != nil {
		slog.Error("vpn_node_address_pool_invalid",
			"node_id", g.deps.NodeID,
			"ip_pool", g.deps.Config.IPPool,
			"error", err)
		return
	}
	nodeSubnet, err := pool.NodeSubnet(subnet)
	if err != nil {
		// A lease written under an older ip_pool. The row is still renewed, because
		// the peers on it keep their addresses, and the node address stays unknown
		// until this node holds a subnet the current pool carves.
		return
	}
	address, err := pool.NodeAddress(nodeSubnet)
	if err != nil {
		slog.Error("vpn_node_address_unavailable",
			"node_id", g.deps.NodeID,
			"subnet", subnet,
			"error", err)
		return
	}
	parsed, ok := netip.AddrFromSlice(address.To4())
	if !ok {
		return
	}
	g.setSelfAddress(parsed.Unmap())
	slog.Info("vpn_node_address_learned",
		"node_id", g.deps.NodeID,
		"subnet", subnet,
		"address", parsed.Unmap().String())
}

func (g *vpnGateway) hasSelfAddress() bool {
	g.selfMu.RLock()
	defer g.selfMu.RUnlock()
	return g.selfAddress.IsValid()
}

// Close drains and releases the gateway exactly once.
func (g *vpnGateway) Close() error {
	g.closeOnce.Do(g.release)
	return nil
}

// release is the teardown body, and it is also what a failed construction calls,
// so an error halfway through assembly cannot leave a device, a stack or a denial
// aggregator behind.
//
// The order is D14's and every step makes the next one safe. New flows are refused
// before the listeners go, otherwise a SYN accepted after them would be answered
// with a registration error that reads as a capacity problem. The listeners go
// before the wait, so the wait is bounded by the peers' own timeouts instead of by
// connections still being accepted. The device is closed after the flows, because
// a splice still writing into a tunnel that is gone is indistinguishable from a
// network fault in the agent's logs. The stack goes after the device, which is what
// makes RemoveNIC safe against a link endpoint whose queue is still being read.
func (g *vpnGateway) release() {
	select {
	case <-g.closed:
	default:
		close(g.closed)
	}
	port := g.localPort()

	g.recordShutdown("drain")
	g.beginDrain()

	g.recordShutdown("listeners")
	if g.tcp != nil {
		g.tcp.close()
	}

	g.recordShutdown("await_flows")
	remaining, waited := g.awaitInFlightFlows()

	g.recordShutdown("flows")
	forced := g.forceCloseFlows()

	g.recordShutdown("endpoint")
	g.stopEndpoint()

	g.recordShutdown("peers")
	if g.peers != nil {
		g.peers.close()
	}

	g.recordShutdown("stack")
	if g.stack != nil {
		_ = g.stack.close()
	}

	g.recordShutdown("background")
	g.stopBackground()

	g.recordShutdown("denials")
	g.flushDenials()

	// The trace is logged rather than only recorded: a shutdown that took the whole
	// budget, or that had to force flows, is the thing an operator needs to see
	// next to the restart that follows it.
	slog.Info("vpn_gateway_stopped",
		"node_id", g.deps.NodeID,
		"port", port,
		"waited", waited.Round(time.Millisecond).String(),
		"in_flight_at_timeout", remaining,
		"flows_forced", forced,
		"steps", strings.Join(g.shutdownSteps(), ">"))
}

// beginDrain refuses new flows while leaving the ones already registered running.
//
// A draining gateway reports capacity rather than "closed" for a refused flow,
// which is deliberate: from the peer's side both look like a connection that did
// not open, and capacity_exhausted is the class the published error taxonomy
// already reserves for "this gateway is holding less than you asked it to".
func (g *vpnGateway) beginDrain() {
	if g.flows != nil {
		g.flows.drain()
	}
}

// awaitInFlightFlows waits out server.vpn.shutdown_timeout for traffic that is
// still being served, and reports what was left when the budget ran out.
//
// A zero timeout means "do not wait", which is what a supervisor with its own stop
// timeout needs: the gateway must not add a second, hidden budget on top of the one
// that is already counting down.
func (g *vpnGateway) awaitInFlightFlows() (int, time.Duration) {
	if g.flows == nil {
		return 0, 0
	}
	started := time.Now()
	remaining := g.flows.count()
	timeout := g.deps.Config.ShutdownTimeout
	if timeout <= 0 {
		return remaining, time.Since(started)
	}
	deadline := started.Add(timeout)
	for remaining > 0 {
		wait := time.Until(deadline)
		if wait <= 0 {
			break
		}
		if wait > vpnShutdownPollInterval {
			wait = vpnShutdownPollInterval
		}
		time.Sleep(wait)
		remaining = g.flows.count()
	}
	return remaining, time.Since(started)
}

// forceCloseFlows ends everything the wait did not.
//
// Cancelling baseCtx is what makes this step forced rather than polite: it is the
// parent of every stream the gateway opened, so a splice whose agent side stopped
// answering still loses its context and returns instead of holding a flow open
// until the process is killed.
func (g *vpnGateway) forceCloseFlows() int {
	if g.cancelBase != nil {
		g.cancelBase()
	}
	forced := 0
	if g.flows != nil {
		forced = g.flows.close()
	}
	if g.udp != nil {
		// Associations whose flow was already detached still hold an agent stream,
		// and Close on the relay is the only place that knows about them.
		g.udp.close()
	}
	return forced
}

// stopBackground cancels the two loops and waits for them to return.
//
// Exceeding the bound is logged rather than returned. Close's job is to release the
// gateway, and a loop that ignores its own context is a defect an operator has to
// see; failing the shutdown over it would leave the endpoint bound, which is the
// worse outcome by a wide margin.
func (g *vpnGateway) stopBackground() {
	g.bgMu.Lock()
	g.bgStopped = true
	started := g.bgStarted
	g.bgMu.Unlock()
	if g.cancelBackground != nil {
		g.cancelBackground()
	}
	if !started {
		return
	}
	waitForLoop := func(done chan struct{}, name string) {
		if done == nil {
			return
		}
		select {
		case <-done:
		case <-time.After(vpnBackgroundStopTimeout):
			slog.Error("vpn_background_loop_stop_timeout",
				"node_id", g.deps.NodeID,
				"loop", name,
				"timeout", vpnBackgroundStopTimeout.String())
		}
	}
	waitForLoop(g.reaperDone, "idle_reaper")
	waitForLoop(g.renewalDone, "lease_renewal")
}

// flushDenials writes the last window of aggregated denials.
//
// It runs last and with a context of its own: baseCtx was cancelled by the forced
// step, and the denials of the seconds before a shutdown are exactly the ones an
// operator investigating it wants to find in the audit log.
func (g *vpnGateway) flushDenials() {
	if g.denials == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), vpnDenialFlushTimeout)
	defer cancel()
	if err := g.denials.Close(ctx); err != nil {
		slog.Error("vpn_denial_flush_failed", "node_id", g.deps.NodeID, "error", err)
	}
}

// recordShutdown appends one teardown step to the trace.
func (g *vpnGateway) recordShutdown(step string) {
	g.shutdownMu.Lock()
	g.shutdownTrace = append(g.shutdownTrace, step)
	g.shutdownMu.Unlock()
}

// shutdownSteps returns the teardown steps in the order they ran.
func (g *vpnGateway) shutdownSteps() []string {
	g.shutdownMu.Lock()
	defer g.shutdownMu.Unlock()
	return append([]string(nil), g.shutdownTrace...)
}

// isClosed reports whether Close has run.
func (g *vpnGateway) isClosed() bool {
	select {
	case <-g.closed:
		return true
	default:
		return false
	}
}
