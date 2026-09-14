package proxyentry_test

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tunnelmesh/tunnelmesh/internal/proxyentry"
)

func TestHeaderRouteIdentityNormalizesAndRejects(t *testing.T) {
	identity := proxyentry.NewHeaderRouteIdentity("X-TunnelMesh-Route", "tm.example.com")
	cases := []struct {
		values []string
		want   proxyentry.RouteKey
		ok     bool
	}{
		{[]string{"tp-demo.tm.example.com"}, "tp-demo.tm.example.com", true},
		{[]string{"TP-Demo.TM.Example.COM"}, "tp-demo.tm.example.com", true},
		{[]string{"demo"}, "tp-demo.tm.example.com", true},
		{[]string{"tp-demo"}, "tp-demo.tm.example.com", true},
		{[]string{" tp-demo "}, "tp-demo.tm.example.com", true},
		{[]string{"tp-demo.other.com"}, "", false},
		{[]string{"other.com"}, "", false},
		{[]string{""}, "", false},
		{[]string{"tp-.tm.example.com"}, "", false},
		{[]string{"tp-de mo.tm.example.com"}, "", false},
		{[]string{"tp-a.tm.example.com", "tp-b.tm.example.com"}, "", false},
		{nil, "", false},
	}
	for _, tc := range cases {
		r := httptest.NewRequest(http.MethodGet, "http://target.example/", nil)
		r.Header.Del("X-TunnelMesh-Route")
		for _, v := range tc.values {
			r.Header.Add("X-TunnelMesh-Route", v)
		}
		got, err := identity.Resolve(r)
		if tc.ok && (err != nil || got != tc.want) {
			t.Fatalf("values=%q got %q err %v want %q", tc.values, got, err, tc.want)
		}
		if !tc.ok && err == nil {
			t.Fatalf("values=%q must be rejected, got %q", tc.values, got)
		}
	}
}

func TestHeaderRouteIdentityRejectsForeignSuffix(t *testing.T) {
	identity := proxyentry.NewHeaderRouteIdentity("X-TunnelMesh-Route", "tm.example.com")
	// A host that merely contains the suffix somewhere other than the end must
	// not be accepted, otherwise "tp-x.tm.example.com.evil.test" would resolve.
	for _, raw := range []string{
		"tp-demo.tm.example.com.evil.test",
		"tp-demo.xtm.example.com",
		"tm.example.com",
		".tm.example.com",
	} {
		r := httptest.NewRequest(http.MethodGet, "http://target.example/", nil)
		r.Header.Set("X-TunnelMesh-Route", raw)
		if got, err := identity.Resolve(r); err == nil {
			t.Fatalf("raw=%q resolved to %q, want rejection", raw, got)
		}
	}
}

func TestRouteKeyName(t *testing.T) {
	if got := proxyentry.RouteKey("tp-demo.tm.example.com").Name(); got != "demo" {
		t.Fatalf("Name() = %q", got)
	}
}

func TestSNIRouteIdentityUsesTLSHandshake(t *testing.T) {
	identity := proxyentry.NewSNIRouteIdentity("tm.example.com")
	plain := httptest.NewRequest(http.MethodConnect, "https://target.example:443", nil)
	if _, err := identity.Resolve(plain); err == nil {
		t.Fatal("plaintext request must not resolve a route")
	}
	withSNI := httptest.NewRequest(http.MethodConnect, "https://target.example:443", nil)
	withSNI.TLS = &tls.ConnectionState{ServerName: "TP-Demo.tm.example.com"}
	got, err := identity.Resolve(withSNI)
	if err != nil || got != "tp-demo.tm.example.com" {
		t.Fatalf("got %q err %v", got, err)
	}
	empty := httptest.NewRequest(http.MethodConnect, "https://target.example:443", nil)
	empty.TLS = &tls.ConnectionState{}
	if _, err := identity.Resolve(empty); err == nil {
		t.Fatal("empty SNI must not resolve a route")
	}
}
