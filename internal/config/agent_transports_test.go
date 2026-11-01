package config

import (
	"strings"
	"testing"
)

func TestAgentServerURLSchemes(t *testing.T) {
	cases := map[string]bool{
		"wss://edge.example.com/ws/agent":  true,
		"ws://127.0.0.1:8080/ws/agent":     true,
		"tls://edge.example.com:7443":      true,
		"tcp://10.0.0.5:7000":              true,
		"quic://edge.example.com:7443":     true,
		"tls://edge.example.com":           false, // raw transports need a port
		"quic://edge.example.com/path":     false,
		"tls://edge.example.com:7443?x=1":  false,
		"http://edge.example.com:7443":     false,
		"tls://user@edge.example.com:7443": false,
		"":                                 false,
	}
	for raw, want := range cases {
		if got := validAgentServerURL(raw); got != want {
			t.Errorf("validAgentServerURL(%q) = %v, want %v", raw, got, want)
		}
	}
}

func TestInsecureTransportNeedsOptInForPlaintextOnly(t *testing.T) {
	cases := []struct {
		raw     string
		allowed bool
		blocked bool
	}{
		{"wss://edge.example.com/ws/agent", false, false},
		{"tls://edge.example.com:7443", false, false},
		{"quic://edge.example.com:7443", false, false},
		{"ws://edge.example.com/ws/agent", false, true},
		{"ws://edge.example.com/ws/agent", true, false},
		{"tcp://edge.example.com:7000", false, true},
		{"tcp://edge.example.com:7000", true, false},
		{"tcp://127.0.0.1:7000", false, false},
		{"tcp://localhost:7000", false, false},
	}
	for _, tc := range cases {
		if got := insecureTransportDisallowed(tc.raw, tc.allowed); got != tc.blocked {
			t.Errorf("insecureTransportDisallowed(%q, %v) = %v, want %v", tc.raw, tc.allowed, got, tc.blocked)
		}
	}
}

func TestQUICCannotTraverseProxies(t *testing.T) {
	if quicThroughProxy("quic://edge.example.com:7443", TransportConfig{ProxyMode: "direct"}) {
		t.Fatal("direct quic must be allowed")
	}
	if quicThroughProxy("quic://edge.example.com:7443", TransportConfig{}) {
		t.Fatal("empty proxy mode defaults to direct")
	}
	for _, mode := range []string{"socks5", "http_connect", "system"} {
		if !quicThroughProxy("quic://edge.example.com:7443", TransportConfig{ProxyMode: mode}) {
			t.Errorf("quic through %s proxy must be refused", mode)
		}
	}
	if quicThroughProxy("tls://edge.example.com:7443", TransportConfig{ProxyMode: "socks5"}) {
		t.Fatal("tls may use a TCP proxy")
	}
}

func TestAgentTransportListenersAreOptInAndValidated(t *testing.T) {
	// Defaults: nothing enabled, so no problems.
	if got := validateAgentTransports(ServerConfig{HTTPAddr: ":8080"}, TLSConfig{}); len(got) != 0 {
		t.Fatalf("disabled listeners must not be validated: %v", got)
	}
	cases := []struct {
		name   string
		server ServerConfig
		native TLSConfig
		want   string
	}{
		{"bad address", ServerConfig{AgentTransports: ServerAgentTransportsConfig{TCP: ServerAgentListenerConfig{Listen: "not-an-address"}}}, TLSConfig{}, "must be host:port"},
		{"reuses HTTP port", ServerConfig{HTTPAddr: ":8080", AgentTransports: ServerAgentTransportsConfig{TCP: ServerAgentListenerConfig{Listen: "0.0.0.0:8080"}}}, TLSConfig{Enabled: true, CertFile: "c", KeyFile: "k"}, "must not reuse the HTTP port"},
		{"plaintext on all interfaces", ServerConfig{AgentTransports: ServerAgentTransportsConfig{TCP: ServerAgentListenerConfig{Listen: "0.0.0.0:7000"}}}, TLSConfig{}, "must bind a loopback address"},
		{"quic without certificate", ServerConfig{AgentTransports: ServerAgentTransportsConfig{QUIC: ServerAgentListenerConfig{Listen: "0.0.0.0:7443"}}}, TLSConfig{}, "requires a TLS certificate"},
		{"negative limit", ServerConfig{AgentTransports: ServerAgentTransportsConfig{MaxUnauthTotal: -1}}, TLSConfig{}, "must not be negative"},
	}
	for _, tc := range cases {
		problems := strings.Join(validateAgentTransports(tc.server, tc.native), "; ")
		if !strings.Contains(problems, tc.want) {
			t.Errorf("%s: problems %q, want %q", tc.name, problems, tc.want)
		}
	}
	ok := ServerConfig{HTTPAddr: ":8080", AgentTransports: ServerAgentTransportsConfig{
		TCP:  ServerAgentListenerConfig{Listen: "127.0.0.1:7000"},
		QUIC: ServerAgentListenerConfig{Listen: "0.0.0.0:7443"},
	}}
	if got := validateAgentTransports(ok, TLSConfig{Enabled: true, CertFile: "c", KeyFile: "k"}); len(got) != 0 {
		t.Fatalf("valid loopback tcp plus TLS quic must pass: %v", got)
	}
}
