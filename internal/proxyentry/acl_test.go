package proxyentry_test

import (
	"net"
	"testing"

	"github.com/tunnelmesh/tunnelmesh/internal/proxyentry"
)

func TestSourceACLMatching(t *testing.T) {
	acl, err := proxyentry.NewSourceACL([]string{"11.71.85.0/24", "10.1.2.3", "2001:db8::/32"})
	if err != nil {
		t.Fatalf("NewSourceACL: %v", err)
	}
	if acl.Broken() {
		t.Fatal("a fully valid ACL must not report broken")
	}
	allow := []string{"11.71.85.7", "10.1.2.3", "2001:db8::1"}
	deny := []string{"11.71.86.1", "10.1.2.4", "2001:db9::1", ""}
	for _, raw := range allow {
		if !acl.Allow(net.ParseIP(raw)) {
			t.Fatalf("%s should be allowed", raw)
		}
	}
	for _, raw := range deny {
		if acl.Allow(net.ParseIP(raw)) {
			t.Fatalf("%s should be denied", raw)
		}
	}
	if acl.Allow(nil) {
		t.Fatal("nil IP must be denied")
	}
	// An IPv4 address handed over in its 16-byte mapped form must still match
	// an IPv4 network: OpenResty and net/http both produce either spelling
	// depending on the listener.
	if !acl.Allow(net.ParseIP("11.71.85.7").To16()) {
		t.Fatal("v4-in-v6 mapped address must match an IPv4 network")
	}
}

func TestSourceACLDefaultsToDeny(t *testing.T) {
	empty, err := proxyentry.NewSourceACL(nil)
	if err != nil {
		t.Fatalf("empty ACL must parse: %v", err)
	}
	if empty.Broken() {
		t.Fatal("an empty ACL is a valid deny-all choice, not a parse failure")
	}
	if empty.Allow(net.ParseIP("8.8.8.8")) {
		t.Fatal("empty ACL must deny everything")
	}
	wildcard, err := proxyentry.NewSourceACL([]string{"0.0.0.0/0", "::/0"})
	if err != nil {
		t.Fatalf("wildcard ACL must parse: %v", err)
	}
	if !wildcard.Allow(net.ParseIP("8.8.8.8")) || !wildcard.Allow(net.ParseIP("2001:db8::1")) {
		t.Fatal("0.0.0.0/0 and ::/0 must allow everything")
	}
}

func TestSourceACLRejectsInvalidEntries(t *testing.T) {
	if _, err := proxyentry.NewSourceACL([]string{"10.0.0.0/33"}); err == nil {
		t.Fatal("invalid CIDR must fail at write time")
	}
	if _, err := proxyentry.NewSourceACL([]string{"not-an-ip"}); err == nil {
		t.Fatal("invalid literal must fail at write time")
	}
	// One bad entry must poison the whole list rather than being skipped: a
	// silently dropped entry would widen or narrow the ACL without the
	// administrator knowing which happened.
	if _, err := proxyentry.NewSourceACL([]string{"10.0.0.0/8", "10.0.0.0/33"}); err == nil {
		t.Fatal("a single invalid entry must fail the whole ACL")
	}
	broken := proxyentry.DenyAllSourceACL()
	if !broken.Broken() || broken.Allow(net.ParseIP("8.8.8.8")) {
		t.Fatal("deny-all ACL must report broken and deny")
	}
}

func TestParseClientIP(t *testing.T) {
	if ip, err := proxyentry.ParseClientIP(" 11.71.85.176 "); err != nil || ip.String() != "11.71.85.176" {
		t.Fatalf("got %v err %v", ip, err)
	}
	if ip, err := proxyentry.ParseClientIP("2001:db8::1"); err != nil || ip.String() != "2001:db8::1" {
		t.Fatalf("IPv6 got %v err %v", ip, err)
	}
	for _, raw := range []string{"", "   ", "11.71.85.176:4321", "abc", "11.71.85.176, 10.0.0.1", "[2001:db8::1]:443"} {
		if _, err := proxyentry.ParseClientIP(raw); err == nil {
			t.Fatalf("%q must be rejected", raw)
		}
	}
}

func TestNormalizeCIDR(t *testing.T) {
	cases := []struct {
		raw  string
		want string
		ok   bool
	}{
		{"10.1.2.3", "10.1.2.3/32", true},
		{"10.1.2.0/24", "10.1.2.0/24", true},
		{"11.71.85.7/24", "11.71.85.0/24", true},
		{"2001:db8::1", "2001:db8::1/128", true},
		{"2001:db8::/32", "2001:db8::/32", true},
		{" 10.0.0.0/8 ", "10.0.0.0/8", true},
		{"", "", false},
		{"not-an-ip", "", false},
		{"10.0.0.0/33", "", false},
		{"10.0.0.1/24/8", "", false},
	}
	for _, tc := range cases {
		got, err := proxyentry.NormalizeCIDR(tc.raw)
		if tc.ok && (err != nil || got != tc.want) {
			t.Fatalf("NormalizeCIDR(%q) = %q, %v; want %q", tc.raw, got, err, tc.want)
		}
		if !tc.ok && err == nil {
			t.Fatalf("NormalizeCIDR(%q) = %q; want error", tc.raw, got)
		}
	}
}
