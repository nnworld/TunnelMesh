package proxyentry_test

import (
	"errors"
	"net"
	"testing"

	"github.com/tunnelmesh/tunnelmesh/internal/proxyentry"
)

func TestTargetPolicyIPLiterals(t *testing.T) {
	open, err := proxyentry.NewTargetPolicy(nil, nil, true)
	if err != nil {
		t.Fatalf("NewTargetPolicy: %v", err)
	}
	if err := open.Validate("93.184.216.34", 443); err != nil {
		t.Fatalf("public target rejected: %v", err)
	}
	if err := open.Validate("10.0.0.5", 8080); err != nil {
		t.Fatalf("private target must be allowed by default: %v", err)
	}
	if err := open.Validate("169.254.169.254", 80); !errors.Is(err, proxyentry.ErrTargetDenied) {
		t.Fatalf("metadata address = %v", err)
	}
	// routing.IsDangerousAddress deliberately keeps loopback reachable: the
	// target is dialed from the Agent host, so 127.0.0.1 is a valid
	// agent-local service address. Only multicast/reserved ranges are denied.
	if err := open.Validate("127.0.0.1", 22); err != nil {
		t.Fatalf("loopback must follow routing.Policy and stay allowed: %v", err)
	}
	if err := open.Validate("224.0.0.1", 80); !errors.Is(err, proxyentry.ErrTargetDenied) {
		t.Fatalf("multicast = %v", err)
	}
	if err := open.Validate("2001:db8::1", 443); !errors.Is(err, proxyentry.ErrTargetDenied) {
		t.Fatalf("IPv6 literal follows routing.Policy and is denied = %v", err)
	}
	if err := open.Validate("93.184.216.34", 0); !errors.Is(err, proxyentry.ErrTargetInvalid) {
		t.Fatalf("port 0 = %v", err)
	}
	if err := open.Validate("93.184.216.34", 70000); !errors.Is(err, proxyentry.ErrTargetInvalid) {
		t.Fatalf("port 70000 = %v", err)
	}
	if err := open.Validate("", 80); !errors.Is(err, proxyentry.ErrTargetInvalid) {
		t.Fatalf("empty host = %v", err)
	}
	if err := open.ValidateIP(nil, 80); !errors.Is(err, proxyentry.ErrTargetInvalid) {
		t.Fatalf("nil IP = %v", err)
	}
}

func TestTargetPolicyPrivateSwitchAndAllowlist(t *testing.T) {
	strict, err := proxyentry.NewTargetPolicy(nil, nil, false)
	if err != nil {
		t.Fatalf("NewTargetPolicy: %v", err)
	}
	if err := strict.Validate("10.0.0.5", 80); !errors.Is(err, proxyentry.ErrTargetDenied) {
		t.Fatalf("private target with allowPrivateTargets=false = %v", err)
	}
	if err := strict.Validate("127.0.0.1", 22); !errors.Is(err, proxyentry.ErrTargetDenied) {
		t.Fatalf("loopback with allowPrivateTargets=false = %v", err)
	}
	// A public address must stay reachable when only private targets are off.
	if err := strict.Validate("93.184.216.34", 443); err != nil {
		t.Fatalf("public target wrongly denied by allowPrivateTargets=false: %v", err)
	}
	allowlisted, err := proxyentry.NewTargetPolicy([]string{"10.10.0.0/16"}, []int{443, 8443}, true)
	if err != nil {
		t.Fatalf("NewTargetPolicy: %v", err)
	}
	if err := allowlisted.Validate("10.10.1.1", 443); err != nil {
		t.Fatalf("allowlisted target rejected: %v", err)
	}
	if err := allowlisted.Validate("10.10.1.1", 80); !errors.Is(err, proxyentry.ErrTargetDenied) {
		t.Fatalf("port outside allowlist = %v", err)
	}
	if err := allowlisted.Validate("10.20.1.1", 443); !errors.Is(err, proxyentry.ErrTargetDenied) {
		t.Fatalf("CIDR outside allowlist = %v", err)
	}
	if _, err := proxyentry.NewTargetPolicy([]string{"10.0.0.0/33"}, nil, true); err == nil {
		t.Fatal("invalid CIDR must fail at construction time")
	}
	if _, err := proxyentry.NewTargetPolicy(nil, []int{0}, true); err == nil {
		t.Fatal("invalid port must fail at construction time")
	}
}

func TestTargetPolicyDefersDomainsToAgent(t *testing.T) {
	policy, err := proxyentry.NewTargetPolicy([]string{"10.10.0.0/16"}, nil, true)
	if err != nil {
		t.Fatalf("NewTargetPolicy: %v", err)
	}
	// A CIDR allowlist cannot be evaluated before resolution, so domains are
	// rejected here exactly like routing.Policy does for agent policies.
	if err := policy.Validate("intranet.example.com", 443); !errors.Is(err, proxyentry.ErrTargetDenied) {
		t.Fatalf("domain with CIDR allowlist = %v", err)
	}
	open, err := proxyentry.NewTargetPolicy(nil, nil, true)
	if err != nil {
		t.Fatalf("NewTargetPolicy: %v", err)
	}
	if err := open.Validate("intranet.example.com", 443); err != nil {
		t.Fatalf("domain without allowlist must be deferred to the agent: %v", err)
	}
	if err := open.Validate("  intranet.example.com  ", 443); err != nil {
		t.Fatalf("surrounding whitespace must be trimmed: %v", err)
	}
	for _, host := range []string{"bad host", "host/path", "a..b.example.com", "trailing.example.com.", "a.b.c." + string(make([]byte, 250))} {
		if err := open.Validate(host, 443); !errors.Is(err, proxyentry.ErrTargetInvalid) {
			t.Fatalf("%q = %v", host, err)
		}
	}
	// A port allowlist still applies to domains even though the CIDR part has to
	// be deferred: the port is known before resolution.
	ports, err := proxyentry.NewTargetPolicy(nil, []int{8443}, true)
	if err != nil {
		t.Fatalf("NewTargetPolicy: %v", err)
	}
	if err := ports.Validate("intranet.example.com", 443); !errors.Is(err, proxyentry.ErrTargetDenied) {
		t.Fatalf("domain on a port outside the allowlist = %v", err)
	}
	if err := ports.Validate("intranet.example.com", 8443); err != nil {
		t.Fatalf("domain on an allowlisted port rejected: %v", err)
	}
}

func TestIsPrivateTarget(t *testing.T) {
	for _, raw := range []string{"10.0.0.1", "192.168.1.1", "172.16.0.1", "127.0.0.1", "169.254.1.1", "fd00::1", "::1"} {
		if !proxyentry.IsPrivateTarget(net.ParseIP(raw)) {
			t.Fatalf("%s should count as private", raw)
		}
	}
	if proxyentry.IsPrivateTarget(net.ParseIP("93.184.216.34")) {
		t.Fatal("public address must not count as private")
	}
	if proxyentry.IsPrivateTarget(nil) {
		t.Fatal("nil must not count as private")
	}
}
