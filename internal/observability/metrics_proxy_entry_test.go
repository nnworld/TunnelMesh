package observability

import (
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// The proxy entry Grafana row groups every panel by these metric names and by
// the label sets asserted below, so a rename or a dropped label has to fail a
// test rather than silently break the dashboard.
func TestProxyEntryMetricsAreRegistered(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics := NewMetrics(reg)
	metrics.ObserveProxyEntryRequest("tp-demo.tm.example.com", "connect", "success", "")
	metrics.ObserveProxyEntryTunnel("tp-demo.tm.example.com", true)
	metrics.ObserveProxyEntryTunnel("tp-demo.tm.example.com", false)
	metrics.ObserveProxyEntryTunnelDuration("tp-demo.tm.example.com", "success", 1500*time.Millisecond)
	metrics.ObserveProxyEntryAuthFailure("tp-demo.tm.example.com", "bad_password")
	metrics.ObserveProxyEntryACLDenied("tp-demo.tm.example.com")

	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{
		"tunnelmesh_proxy_entry_requests_total":          {"route", "mode", "result", "error_class"},
		"tunnelmesh_proxy_entry_tunnels_active":          {"route"},
		"tunnelmesh_proxy_entry_tunnel_duration_seconds": {"route", "result"},
		"tunnelmesh_proxy_entry_auth_failures_total":     {"route", "reason"},
		"tunnelmesh_proxy_entry_acl_denied_total":        {"route"},
	}
	found := 0
	for _, family := range families {
		labels, ok := want[family.GetName()]
		if !ok {
			continue
		}
		found++
		for _, metric := range family.GetMetric() {
			got := make([]string, 0, len(metric.GetLabel()))
			for _, pair := range metric.GetLabel() {
				got = append(got, pair.GetName())
			}
			// Gather sorts label pairs alphabetically, so compare as sets.
			sortedGot := append([]string(nil), got...)
			sortedWant := append([]string(nil), labels...)
			sort.Strings(sortedGot)
			sort.Strings(sortedWant)
			if strings.Join(sortedGot, ",") != strings.Join(sortedWant, ",") {
				t.Fatalf("%s labels = %v, want %v", family.GetName(), got, labels)
			}
		}
	}
	if found != len(want) {
		t.Fatalf("registered %d of %d proxy entry metrics", found, len(want))
	}
}

// Empty label values are normalized by label() rather than rejected, so a
// denial that never resolved a route still lands in a finite series set instead
// of exploding cardinality with attacker-chosen hostnames.
func TestProxyEntryMetricsNormalizeEmptyLabels(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics := NewMetrics(reg)
	metrics.ObserveProxyEntryRequest("", "connect", "denied", "identity")
	metrics.ObserveProxyEntryAuthFailure("", "backoff")
	metrics.ObserveProxyEntryACLDenied("")
	metrics.ObserveProxyEntryTunnelDuration("", "timeout", -time.Second)

	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, family := range families {
		for _, metric := range family.GetMetric() {
			for _, pair := range metric.GetLabel() {
				checked++
				// route may legitimately be empty before identity resolution;
				// every other label has a fixed enum and must never be blank.
				if pair.GetValue() == "" && pair.GetName() != "route" {
					t.Fatalf("empty %s label in %s", pair.GetName(), family.GetName())
				}
			}
			if family.GetName() == "tunnelmesh_proxy_entry_tunnel_duration_seconds" {
				if got := metric.GetHistogram().GetSampleCount(); got != 1 {
					t.Fatalf("duration sample count = %d, want 1", got)
				}
				// A negative duration is clamped to zero rather than recorded.
				if metric.GetHistogram().GetSampleSum() != 0 {
					t.Fatalf("duration sample sum = %v, want 0", metric.GetHistogram().GetSampleSum())
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("no proxy entry labels were gathered")
	}
}
