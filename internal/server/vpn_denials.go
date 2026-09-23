package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/storage"
	"github.com/tunnelmesh/tunnelmesh/internal/vpn"
)

const (
	// vpnPacketDeniedAction is the audit action for a packet the gateway refused.
	// It is published in the design spec §12.2 and is what an administrator
	// filters the audit log on, so renaming it would orphan every saved query.
	vpnPacketDeniedAction = "vpn_packet_denied"
	// vpnDenialSubnetBits is the prefix a destination address is reduced to before
	// it is aggregated or written to the audit log. A /24 is the unit an operator
	// thinks in when reading "which range is this peer scanning", and it is also
	// the point at which the address stops being a per-host fact about one
	// internal machine.
	vpnDenialSubnetBits = 24
	// vpnDenialIPv6SubnetBits is the equivalent reduction for a destination that
	// is not IPv4. The tunnel does not carry IPv6, so this only ever applies to a
	// datagram that was already refused, and it exists so the aggregator cannot be
	// coaxed into writing a full address of any family.
	vpnDenialIPv6SubnetBits = 48
	// vpnDenialDefaultWindow and vpnDenialDefaultMaxKeys bound the aggregator when
	// a caller does not choose. One minute matches the granularity an operator
	// reads an audit log at; four thousand keys is far more than a peer set can
	// produce in a minute and small enough that the map cannot matter to memory.
	vpnDenialDefaultWindow  = time.Minute
	vpnDenialDefaultMaxKeys = 4096
)

// vpnDenial is one refused packet, reduced to what may be persisted.
//
// It deliberately has no field for payload bytes, key material or the peer's
// public key. The aggregator writes what it is given, so the only way to keep a
// secret out of the audit log is for the type to be unable to carry one.
type vpnDenial struct {
	PeerID   string
	Class    vpn.ErrorClass
	Reason   string
	Protocol string
	Dest     netip.Addr
	Port     int
}

// denialKey is the aggregation bucket identity: one peer, one published class,
// one destination range and one protocol.
type denialKey struct {
	peerID   string
	class    vpn.ErrorClass
	subnet   string
	protocol string
}

// denialBucket is one window's worth of identical denials.
type denialBucket struct {
	key         denialKey
	reason      string
	port        int
	count       int
	peers       map[string]struct{}
	windowStart time.Time
}

// denialEntry is a fully rendered audit write, prepared under the lock and
// executed outside it.
type denialEntry struct {
	resourceID string
	details    string
}

// denialAggregator folds packet denials into windowed audit entries.
//
// The packet path calls Record once per refused packet. Without aggregation a
// peer scanning a /16 would write 65536 audit rows a second and take the
// management database with it, which is why the design spec requires the window.
// The number of live buckets is bounded twice over: maxKeys distinct buckets, and
// then one overflow bucket per published error_class, so a scan can grow neither
// the map nor the audit log.
type denialAggregator struct {
	audits  storage.AuditRepository
	window  time.Duration
	maxKeys int
	nowFn   func() time.Time

	mu       sync.Mutex
	buckets  map[denialKey]*denialBucket
	overflow map[vpn.ErrorClass]*denialBucket
	closed   bool

	// failureLogged keeps one unusable audit repository from producing a log line
	// per denied packet, which would replace the flood it was meant to prevent
	// with a different one.
	failureLogged atomic.Bool
}

// newDenialAggregator builds an aggregator. A nil repository is accepted and
// turns the aggregator into a no-op: the metric still counts every denial, and a
// deployment without an audit sink must not lose its packet path over it.
func newDenialAggregator(audits storage.AuditRepository, window time.Duration, maxKeys int) *denialAggregator {
	if window <= 0 {
		window = vpnDenialDefaultWindow
	}
	if maxKeys <= 0 {
		maxKeys = vpnDenialDefaultMaxKeys
	}
	return &denialAggregator{
		audits:   audits,
		window:   window,
		maxKeys:  maxKeys,
		nowFn:    func() time.Time { return time.Now().UTC() },
		buckets:  make(map[denialKey]*denialBucket),
		overflow: make(map[vpn.ErrorClass]*denialBucket),
	}
}

func (a *denialAggregator) now() time.Time { return a.nowFn() }

// Record folds one denial into its window's bucket.
//
// It never returns an error and never blocks on the database: the packet has
// already been dropped by the time this runs, so the only thing left to do is
// account for it, and an accounting failure must not become a packet-path
// failure.
func (a *denialAggregator) Record(ctx context.Context, denial vpnDenial) {
	if a == nil || a.audits == nil {
		return
	}
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return
	}
	due := a.recordLocked(denial, a.now())
	a.mu.Unlock()
	a.write(ctx, due)
}

func (a *denialAggregator) recordLocked(denial vpnDenial, now time.Time) []denialEntry {
	var due []denialEntry
	if len(a.buckets) >= a.maxKeys {
		// Reclaim finished windows before falling back to overflow: a bucket whose
		// window has passed is a write waiting to happen, and writing it is both
		// the honest thing to do and the way the map shrinks again.
		due = append(due, a.sweepExpiredLocked(now)...)
	}
	key := denialKey{
		peerID:   denial.PeerID,
		class:    denial.Class,
		subnet:   vpnDenialSubnet(denial.Dest),
		protocol: denial.Protocol,
	}
	if bucket, ok := a.buckets[key]; ok {
		if now.Sub(bucket.windowStart) < a.window {
			bucket.count++
			return due
		}
		// The window closed while the bucket was still held, so the count it
		// carries belongs to the past and this denial starts a new one.
		due = append(due, a.renderLocked(bucket))
		delete(a.buckets, key)
	}
	if len(a.buckets) >= a.maxKeys {
		due = append(due, a.overflowLocked(denial, now)...)
		return due
	}
	a.buckets[key] = &denialBucket{key: key, reason: denial.Reason, port: denial.Port, count: 1, windowStart: now}
	return due
}

// overflowLocked folds a denial into the per-class overflow bucket.
//
// The peer and the destination range are dropped here on purpose. The bucket is
// shared by every peer that was denied for the same class while the map was full,
// so naming one of them in the audit entry would attribute another peer's packets
// to it, and an attribution that may be wrong is worse than one that says
// "aggregated". The distinct peer count is kept, because "one peer scanning" and
// "forty peers affected" are different incidents.
func (a *denialAggregator) overflowLocked(denial vpnDenial, now time.Time) []denialEntry {
	bucket, ok := a.overflow[denial.Class]
	if !ok {
		a.overflow[denial.Class] = &denialBucket{
			key:         denialKey{class: denial.Class},
			reason:      denial.Reason,
			count:       1,
			peers:       map[string]struct{}{denial.PeerID: {}},
			windowStart: now,
		}
		return nil
	}
	if now.Sub(bucket.windowStart) >= a.window {
		due := []denialEntry{a.renderLocked(bucket)}
		delete(a.overflow, denial.Class)
		a.overflow[denial.Class] = &denialBucket{
			key:         denialKey{class: denial.Class},
			reason:      denial.Reason,
			count:       1,
			peers:       map[string]struct{}{denial.PeerID: {}},
			windowStart: now,
		}
		return due
	}
	bucket.count++
	if denial.PeerID != "" {
		bucket.peers[denial.PeerID] = struct{}{}
	}
	return nil
}

// sweepExpiredLocked renders and removes every bucket whose window has passed.
func (a *denialAggregator) sweepExpiredLocked(now time.Time) []denialEntry {
	var due []denialEntry
	for key, bucket := range a.buckets {
		if now.Sub(bucket.windowStart) >= a.window {
			due = append(due, a.renderLocked(bucket))
			delete(a.buckets, key)
		}
	}
	for class, bucket := range a.overflow {
		if now.Sub(bucket.windowStart) >= a.window {
			due = append(due, a.renderLocked(bucket))
			delete(a.overflow, class)
		}
	}
	return due
}

// renderLocked turns one bucket into an audit write. The caller holds mu.
func (a *denialAggregator) renderLocked(bucket *denialBucket) denialEntry {
	aggregated := bucket.key.peerID == ""
	details := map[string]any{
		"errorClass": string(bucket.key.class),
		"count":      bucket.count,
		"windowMs":   a.window.Milliseconds(),
		"aggregated": aggregated,
	}
	if bucket.reason != "" {
		details["reason"] = bucket.reason
	}
	if aggregated {
		details["peers"] = len(bucket.peers)
	} else {
		details["peerId"] = bucket.key.peerID
		details["protocol"] = bucket.key.protocol
		details["subnet"] = bucket.key.subnet
		details["port"] = bucket.port
	}
	encoded, err := json.Marshal(details)
	if err != nil {
		// json.Marshal cannot fail on a map of strings and integers, so this is a
		// defensive branch; an entry with the class and the count is still worth
		// more than no entry at all.
		encoded = []byte(`{"errorClass":"` + string(bucket.key.class) + `","count":1}`)
	}
	return denialEntry{resourceID: bucket.key.peerID, details: string(encoded)}
}

// Flush writes every outstanding bucket.
//
// It is called on shutdown and by nothing else in the steady state: a bucket is
// written when its window rolls over or when the map needs the room. Flushing on
// a timer as well would double the audit volume for no additional information.
func (a *denialAggregator) Flush(ctx context.Context) {
	if a == nil || a.audits == nil {
		return
	}
	a.mu.Lock()
	due := a.sweepAllLocked()
	a.mu.Unlock()
	a.write(ctx, due)
}

func (a *denialAggregator) sweepAllLocked() []denialEntry {
	due := make([]denialEntry, 0, len(a.buckets)+len(a.overflow))
	for key, bucket := range a.buckets {
		due = append(due, a.renderLocked(bucket))
		delete(a.buckets, key)
	}
	for class, bucket := range a.overflow {
		due = append(due, a.renderLocked(bucket))
		delete(a.overflow, class)
	}
	return due
}

// Close flushes and refuses later records. It is idempotent, so the shutdown path
// and the runtime's safety net can both call it.
func (a *denialAggregator) Close(ctx context.Context) error {
	if a == nil {
		return nil
	}
	if a.audits == nil {
		a.mu.Lock()
		a.closed = true
		a.mu.Unlock()
		return nil
	}
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return nil
	}
	due := a.sweepAllLocked()
	a.closed = true
	a.mu.Unlock()
	a.write(ctx, due)
	return nil
}

// pending reports how many buckets are being held. It exists so the bound the
// aggregator promises is observable rather than inferred.
func (a *denialAggregator) pending() int {
	if a == nil {
		return 0
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.buckets) + len(a.overflow)
}

// write executes prepared audit entries outside the lock.
func (a *denialAggregator) write(ctx context.Context, due []denialEntry) {
	for _, entry := range due {
		if err := a.audits.Create(ctx, storage.AuditLog{
			Action:       vpnPacketDeniedAction,
			ResourceType: vpnPeerResourceType,
			ResourceID:   entry.resourceID,
			Details:      entry.details,
		}); err != nil {
			a.reportFailure(err)
		}
	}
}

func (a *denialAggregator) reportFailure(err error) {
	if !a.failureLogged.CompareAndSwap(false, true) {
		return
	}
	slog.Warn("vpn_packet_denied_audit_failed",
		"error", err,
		"action", "denials are still counted by the metric; only the audit entry was lost")
}

// vpnDenialSubnet reduces a destination address to the range that may be
// persisted.
//
// The full address never reaches the audit log. An internal host address is a
// fact about somebody's network, the denial event is about a peer's behaviour,
// and the range is enough to tell "one host is being probed" from "a whole
// segment is being walked".
func vpnDenialSubnet(dest netip.Addr) string {
	if !dest.IsValid() {
		return "unknown"
	}
	bits := vpnDenialSubnetBits
	if !dest.Is4() {
		bits = vpnDenialIPv6SubnetBits
	}
	prefix, err := dest.Prefix(bits)
	if err != nil {
		return "unknown"
	}
	return prefix.Masked().String()
}
