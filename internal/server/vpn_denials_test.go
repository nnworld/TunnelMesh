package server

import (
	"context"
	"encoding/json"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/storage"
	"github.com/tunnelmesh/tunnelmesh/internal/vpn"
)

// recordingAuditRepository captures every audit entry the aggregator writes so a
// test can read the details JSON back.
type recordingAuditRepository struct {
	mu      sync.Mutex
	entries []storage.AuditLog
	err     error
}

func (r *recordingAuditRepository) Create(_ context.Context, entry storage.AuditLog) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return r.err
	}
	r.entries = append(r.entries, entry)
	return nil
}

func (r *recordingAuditRepository) List(context.Context, storage.AuditFilter, string, int) (storage.Page[storage.AuditLog], error) {
	return storage.Page[storage.AuditLog]{}, nil
}

func (r *recordingAuditRepository) recorded() []storage.AuditLog {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]storage.AuditLog(nil), r.entries...)
}

func (r *recordingAuditRepository) totalDeniedCount(t *testing.T) int {
	t.Helper()
	total := 0
	for _, entry := range r.recorded() {
		if entry.Action != vpnPacketDeniedAction {
			t.Errorf("audit action = %q, want %q", entry.Action, vpnPacketDeniedAction)
			continue
		}
		var details map[string]any
		if err := json.Unmarshal([]byte(entry.Details), &details); err != nil {
			t.Fatalf("details %q are not json: %v", entry.Details, err)
		}
		count, ok := details["count"].(float64)
		if !ok {
			t.Fatalf("details %q have no numeric count", entry.Details)
		}
		total += int(count)
	}
	return total
}

func vpnTestDenial(peerID string, class vpn.ErrorClass, dest string) vpnDenial {
	return vpnDenial{
		PeerID:   peerID,
		Class:    class,
		Reason:   "destination is outside the peer allowed_ips",
		Protocol: "tcp",
		Dest:     netip.MustParseAddr(dest),
		Port:     8080,
	}
}

func TestDenialAggregatorCollapsesOneWindowIntoOneEntry(t *testing.T) {
	audits := &recordingAuditRepository{}
	aggregator := newDenialAggregator(audits, time.Minute, 64)
	ctx := context.Background()

	for range 5 {
		aggregator.Record(ctx, vpnTestDenial("peer-a", vpn.ClassTargetDenied, "192.168.1.20"))
	}
	// The same peer, class and /24 from a different host address is still one
	// bucket: aggregating by host would let a peer scanning a range flood the
	// audit log, which is the failure the window exists to prevent.
	aggregator.Record(ctx, vpnTestDenial("peer-a", vpn.ClassTargetDenied, "192.168.1.99"))
	aggregator.Flush(ctx)

	entries := audits.recorded()
	if len(entries) != 1 {
		t.Fatalf("%d audit entries were written, want 1", len(entries))
	}
	entry := entries[0]
	if entry.ResourceType != vpnPeerResourceType {
		t.Errorf("ResourceType = %q, want %q", entry.ResourceType, vpnPeerResourceType)
	}
	if entry.ResourceID != "peer-a" {
		t.Errorf("ResourceID = %q, want %q", entry.ResourceID, "peer-a")
	}
	var details map[string]any
	if err := json.Unmarshal([]byte(entry.Details), &details); err != nil {
		t.Fatalf("details are not json: %v", err)
	}
	if details["count"].(float64) != 6 {
		t.Errorf("count = %v, want 6", details["count"])
	}
	if details["errorClass"] != string(vpn.ClassTargetDenied) {
		t.Errorf("errorClass = %v, want %q", details["errorClass"], vpn.ClassTargetDenied)
	}
	if details["protocol"] != "tcp" {
		t.Errorf("protocol = %v, want tcp", details["protocol"])
	}
	if details["subnet"] != "192.168.1.0/24" {
		t.Errorf("subnet = %v, want 192.168.1.0/24", details["subnet"])
	}
	if strings.Contains(entry.Details, "192.168.1.20") || strings.Contains(entry.Details, "192.168.1.99") {
		t.Errorf("details %q contain a full destination address, want only its /24", entry.Details)
	}
}

func TestDenialAggregatorKeepsDistinctClassesApart(t *testing.T) {
	audits := &recordingAuditRepository{}
	aggregator := newDenialAggregator(audits, time.Minute, 64)
	ctx := context.Background()

	aggregator.Record(ctx, vpnTestDenial("peer-a", vpn.ClassTargetDenied, "192.168.1.20"))
	aggregator.Record(ctx, vpnTestDenial("peer-a", vpn.ClassPortDenied, "192.168.1.20"))
	aggregator.Record(ctx, vpnTestDenial("peer-b", vpn.ClassTargetDenied, "192.168.1.20"))
	aggregator.Flush(ctx)

	if got := len(audits.recorded()); got != 3 {
		t.Fatalf("%d audit entries were written, want 3: class and peer both separate a bucket", got)
	}
	if total := audits.totalDeniedCount(t); total != 3 {
		t.Errorf("the recorded counts sum to %d, want 3", total)
	}
}

func TestDenialAggregatorRollsTheWindowOver(t *testing.T) {
	audits := &recordingAuditRepository{}
	aggregator := newDenialAggregator(audits, time.Minute, 64)
	clock := newControllableClock(time.Unix(1_800_000_000, 0))
	aggregator.nowFn = clock.Now
	ctx := context.Background()

	aggregator.Record(ctx, vpnTestDenial("peer-a", vpn.ClassTargetDenied, "192.168.1.20"))
	aggregator.Record(ctx, vpnTestDenial("peer-a", vpn.ClassTargetDenied, "192.168.1.20"))
	clock.Advance(90 * time.Second) // past the one minute window
	aggregator.Record(ctx, vpnTestDenial("peer-a", vpn.ClassTargetDenied, "192.168.1.20"))
	aggregator.Flush(ctx)

	entries := audits.recorded()
	if len(entries) != 2 {
		t.Fatalf("%d audit entries were written, want 2: one per window", len(entries))
	}
	counts := map[float64]int{}
	for _, entry := range entries {
		var details map[string]any
		if err := json.Unmarshal([]byte(entry.Details), &details); err != nil {
			t.Fatalf("details are not json: %v", err)
		}
		counts[details["count"].(float64)]++
	}
	if counts[2] != 1 || counts[1] != 1 {
		t.Errorf("window counts = %v, want one entry of 2 and one of 1", counts)
	}
}

func TestDenialAggregatorBoundsItsKeySet(t *testing.T) {
	audits := &recordingAuditRepository{}
	const maxKeys = 4
	aggregator := newDenialAggregator(audits, time.Minute, maxKeys)
	ctx := context.Background()

	// Twenty distinct peers, each with a distinct destination range: twenty keys
	// against a cap of four. Without an overflow bucket the map would grow to
	// twenty and a peer that scans a /16 would grow it to 256.
	for peer := range 20 {
		peerID := "peer-" + itoa(peer)
		aggregator.Record(ctx, vpnTestDenial(peerID, vpn.ClassTargetDenied, "10.0."+itoa(peer)+".5"))
	}
	if pending := aggregator.pending(); pending > maxKeys+len(vpn.AllErrorClasses()) {
		t.Errorf("pending() = %d, want at most %d buckets", pending, maxKeys+len(vpn.AllErrorClasses()))
	}
	aggregator.Flush(ctx)

	if total := audits.totalDeniedCount(t); total != 20 {
		t.Errorf("the recorded counts sum to %d, want 20: overflow must count, not discard", total)
	}
	// Nothing in an overflowed entry may name a peer it is aggregating across,
	// because the count covers more than one.
	for _, entry := range audits.recorded() {
		var details map[string]any
		if err := json.Unmarshal([]byte(entry.Details), &details); err != nil {
			t.Fatalf("details are not json: %v", err)
		}
		if aggregated, _ := details["aggregated"].(bool); aggregated {
			if _, hasPeer := details["peerId"]; hasPeer {
				t.Errorf("an aggregated entry %q names a single peer", entry.Details)
			}
		}
	}
}

func TestDenialAggregatorCloseFlushesOutstandingEntries(t *testing.T) {
	audits := &recordingAuditRepository{}
	aggregator := newDenialAggregator(audits, time.Hour, 64)
	ctx := context.Background()

	aggregator.Record(ctx, vpnTestDenial("peer-a", vpn.ClassRateLimited, "192.168.1.20"))
	if err := aggregator.Close(ctx); err != nil {
		t.Fatalf("Close() error = %v, want nil", err)
	}
	if got := len(audits.recorded()); got != 1 {
		t.Fatalf("%d audit entries were written by Close, want 1", got)
	}
	if pending := aggregator.pending(); pending != 0 {
		t.Errorf("pending() = %d after Close, want 0", pending)
	}
	// A second Close must not write the same denials again.
	if err := aggregator.Close(ctx); err != nil {
		t.Fatalf("second Close() error = %v, want nil", err)
	}
	if got := len(audits.recorded()); got != 1 {
		t.Errorf("%d audit entries exist after two Closes, want 1", got)
	}
	// Recording after Close is dropped rather than resurrecting a bucket, because
	// the gateway is shutting down and a later Flush is not guaranteed to run.
	aggregator.Record(ctx, vpnTestDenial("peer-a", vpn.ClassRateLimited, "192.168.1.20"))
	aggregator.Flush(ctx)
	if got := len(audits.recorded()); got != 1 {
		t.Errorf("%d audit entries exist after a post-Close record, want 1", got)
	}
}

func TestDenialAggregatorSurvivesAnAuditFailure(t *testing.T) {
	audits := &recordingAuditRepository{err: context.DeadlineExceeded}
	aggregator := newDenialAggregator(audits, time.Minute, 64)
	ctx := context.Background()

	// A denied packet must never fail the packet path: the audit write is best
	// effort and the packet has already been dropped by the time this runs.
	aggregator.Record(ctx, vpnTestDenial("peer-a", vpn.ClassTargetDenied, "192.168.1.20"))
	aggregator.Record(ctx, vpnTestDenial("peer-a", vpn.ClassTargetDenied, "192.168.1.20"))
	aggregator.Flush(ctx)
	if err := aggregator.Close(ctx); err != nil {
		t.Errorf("Close() error = %v, want nil even when the audit repository fails", err)
	}
}

func TestDenialAggregatorIgnoresANilRepository(t *testing.T) {
	aggregator := newDenialAggregator(nil, time.Minute, 64)
	ctx := context.Background()

	aggregator.Record(ctx, vpnTestDenial("peer-a", vpn.ClassTargetDenied, "192.168.1.20"))
	aggregator.Flush(ctx)
	if err := aggregator.Close(ctx); err != nil {
		t.Errorf("Close() error = %v, want nil", err)
	}
	if pending := aggregator.pending(); pending != 0 {
		t.Errorf("pending() = %d, want 0: with nowhere to write there is nothing to hold", pending)
	}
}

func TestDenialAggregatorConcurrentRecords(t *testing.T) {
	audits := &recordingAuditRepository{}
	aggregator := newDenialAggregator(audits, time.Minute, 64)
	ctx := context.Background()

	var start sync.WaitGroup
	var done sync.WaitGroup
	start.Add(1)
	for range 64 {
		done.Add(1)
		go func() {
			defer done.Done()
			start.Wait()
			aggregator.Record(ctx, vpnTestDenial("peer-a", vpn.ClassRateLimited, "192.168.1.20"))
		}()
	}
	start.Done()
	done.Wait()
	aggregator.Flush(ctx)

	if total := audits.totalDeniedCount(t); total != 64 {
		t.Errorf("the recorded counts sum to %d, want 64", total)
	}
}
