package routing

import (
	"errors"
	"net"
	"strings"
	"testing"
)

func TestParseDynamicHost(t *testing.T) {
	got, err := ParseDynamicHost("agent-x-192-168-1-20-3000.apps.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if got.AgentID != "agent-x" || got.TargetHost != "192.168.1.20" || got.Port != 3000 {
		t.Fatalf("got %#v", got)
	}
	if _, err := ParseDynamicHost("bad-192-168-1-999-3000.apps.example.com"); err == nil {
		t.Fatal("expected malformed address rejection")
	}
	if _, err := ParseDynamicHost("a-127-0-0-1-80.apps.example.com"); err == nil {
		t.Fatal("expected dangerous address rejection")
	}
}

func TestResolverPrecedence(t *testing.T) {
	r := NewRouteResolver([]Route{
		{ID: "path", Domain: "example.com", PathPrefix: "/api", AgentID: "a", TargetHost: "10.0.0.1", TargetPort: 80},
		{ID: "exact", Domain: "example.com", AgentID: "b", TargetHost: "10.0.0.2", TargetPort: 81},
		{ID: "wild", Domain: "*.example.com", AgentID: "c", TargetHost: "10.0.0.3", TargetPort: 82},
	}, WithDynamicSuffix("apps.example.com"))
	got, err := r.ResolveHTTP("example.com", "/api/x")
	if err != nil || got.ID != "path" {
		t.Fatalf("path=%#v err=%v", got, err)
	}
	got, err = r.ResolveHTTP("foo.example.com", "/")
	if err != nil || got.ID != "wild" {
		t.Fatalf("wild=%#v err=%v", got, err)
	}
	got, err = r.ResolveHTTP("a-10-0-0-9-8080.apps.example.com", "/")
	if err != nil || got.AgentID != "a" || got.TargetHost != "10.0.0.9" || got.TargetPort != 8080 {
		t.Fatalf("dynamic=%#v err=%v", got, err)
	}
	if _, err := r.ResolveHTTP("none.other.test", "/"); !errors.Is(err, ErrRouteNotFound) {
		t.Fatalf("err=%v", err)
	}
}

func TestPolicyCIDRAndPorts(t *testing.T) {
	p, err := NewPolicy([]string{"10.0.0.0/8"}, []int{80, 443})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Validate(net.ParseIP("10.1.2.3"), 80); err != nil {
		t.Fatal(err)
	}
	if err := p.Validate(net.ParseIP("192.168.1.2"), 80); err == nil {
		t.Fatal("expected cidr rejection")
	}
	if err := p.Validate(net.ParseIP("10.1.2.3"), 22); err == nil {
		t.Fatal("expected port rejection")
	}
}

func TestDynamicHostValidationAndRouteIDPrecedence(t *testing.T) {
	for _, host := range []string{
		"-a-10-0-0-1-80.apps.example.com",
		"a-10-0-0-1-0.apps.example.com",
		"a-224-0-0-1-80.apps.example.com",
		"a_1-10-0-0-1-80.apps.example.com",
	} {
		if _, err := ParseDynamicHost(host); err == nil {
			t.Errorf("%s: expected rejection", host)
		}
	}
	long := "a-" + strings.Repeat("x", 64) + "-10-0-0-1-80.apps.example.com"
	if _, err := ParseDynamicHost(long); !errors.Is(err, ErrDNSLabelTooLong) {
		t.Fatalf("long label err=%v", err)
	}
	p, err := NewPolicy([]string{"10.0.0.0/8"}, "80-81")
	if err != nil {
		t.Fatal(err)
	}
	r := NewRouteResolver(RouteResolverConfig{Routes: []Route{{RouteID: "r1", Domain: "one.example.com", TargetHost: "10.1.1.1", TargetPort: 80}}, Policy: p})
	got, err := r.ResolveHTTP("r1", "ignored.example.com", "/")
	if err != nil || got.ID != "r1" {
		t.Fatalf("route id got=%#v err=%v", got, err)
	}
}
