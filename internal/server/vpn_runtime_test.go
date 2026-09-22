package server

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/config"
)

// enabledVPNTestConfig is a fully valid server.vpn section. Every field the loader
// validates is set, so a failure in these tests is about the gateway and never
// about a half-filled configuration sneaking past a check.
func enabledVPNTestConfig(t *testing.T) config.VPNConfig {
	t.Helper()
	return config.VPNConfig{
		Enabled:           true,
		Listen:            "127.0.0.1:0",
		EndpointHost:      "gw-1.mesh.example.com",
		IPPool:            "10.64.0.0/16",
		NodeSubnetSize:    24,
		MTU:               1420,
		ConnectTimeout:    time.Second,
		IdleTimeout:       time.Second,
		ShutdownTimeout:   0,
		ICMPEnabled:       true,
		ICMPTimeout:       time.Second,
		ICMPMaxConcurrent: 4,
	}
}

// A disabled gateway must cost nothing: no socket, no goroutine, no stack. This is
// the assertion behind ADR 0002's stop-loss path, where flipping the switch off
// and restarting is the whole remedy.
func TestStartVPNGatewayDisabledCreatesNothing(t *testing.T) {
	gateway, err := startVPNGateway(context.Background(), VPNGatewayDeps{Config: config.VPNConfig{}})
	if err != nil {
		t.Fatalf("startVPNGateway: %v", err)
	}
	if gateway != nil {
		t.Errorf("a disabled gateway returned %T, want a nil data plane", gateway)
		_ = gateway.Close()
	}
}

// The configuration rules are asserted through the validator rather than through
// startVPNGateway on purpose: in an untagged binary the build tag error is
// reported first, so an assertion made through the assembly function would pass
// for the wrong reason and would not notice a rule being dropped.
func TestValidateVPNGatewayConfig(t *testing.T) {
	if err := validateVPNGatewayConfig(enabledVPNTestConfig(t)); err != nil {
		t.Fatalf("a valid configuration was refused: %v", err)
	}
	if err := validateVPNGatewayConfig(config.VPNConfig{}); err != nil {
		t.Errorf("a disabled section must not be validated, got %v", err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*config.VPNConfig)
		want   string
	}{
		{"unparsable listen", func(cfg *config.VPNConfig) { cfg.Listen = "not-an-address" }, "server.vpn.listen"},
		{"missing endpoint host", func(cfg *config.VPNConfig) { cfg.EndpointHost = "" }, "endpoint_host"},
		{"node subnet not narrower than the pool", func(cfg *config.VPNConfig) {
			cfg.IPPool = "10.64.0.0/24"
			cfg.NodeSubnetSize = 24
		}, "ip_pool"},
		{"mtu out of range", func(cfg *config.VPNConfig) { cfg.MTU = 64 }, "mtu"},
		{"negative flow cap", func(cfg *config.VPNConfig) { cfg.MaxFlowsPerPeer = -1 }, "max_flows_per_peer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := enabledVPNTestConfig(t)
			tc.mutate(&cfg)
			err := validateVPNGatewayConfig(cfg)
			if err == nil {
				t.Fatal("an unusable configuration was accepted")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not name %q, so the operator cannot find the field", err, tc.want)
			}
		})
	}
}
