package server

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/config"
	"github.com/tunnelmesh/tunnelmesh/internal/vpn"
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

// TestVPNNodePublicKeyReadsTheIdentityOnlyWhenItIsNeeded pins the one rule that
// keeps the node key out of a process that has no use for it, and the one that
// keeps the management plane from contradicting the data plane.
func TestVPNNodePublicKeyReadsTheIdentityOnlyWhenItIsNeeded(t *testing.T) {
	const alicePrivate = "dwdtCnMYpX08FsFyUbJmRd9ML4frwJkqsXf7pR25LCo="
	const alicePrivateHex = "77076d0a7318a57d3c16c17251b26645df4c2f87ebc0992ab177fba51db92c2a"
	const alicePublic = "hSDwCYkwp1R0i33ctD73Wg2/Og0mOBr066SpjqqbTmo="
	enabled := enabledVPNTestConfig(t)
	disabled := enabledVPNTestConfig(t)
	disabled.Enabled = false

	t.Run("a disabled gateway never reads the secret", func(t *testing.T) {
		t.Setenv(vpn.NodePrivateKeyEnv, alicePrivate)
		if got := vpnNodePublicKey(disabled); got != "" {
			t.Errorf("vpnNodePublicKey = %q on a node with server.vpn disabled, want the empty string", got)
		}
	})

	t.Run("an enabled gateway derives the public half", func(t *testing.T) {
		for name, supplied := range map[string]string{"base64": alicePrivate, "hex": alicePrivateHex} {
			t.Setenv(vpn.NodePrivateKeyEnv, supplied)
			got := vpnNodePublicKey(enabled)
			if got != alicePublic {
				t.Errorf("%s: vpnNodePublicKey = %q, want %q", name, got, alicePublic)
			}
			if err := vpn.ValidatePublicKey(got); err != nil {
				t.Errorf("%s: the derived key is not usable as a peer public key: %v", name, err)
			}
			// The value is written into a configuration a user downloads, so it
			// must be the public half and nothing else.
			if strings.Contains(got, alicePrivate) || got == alicePrivateHex {
				t.Errorf("%s: vpnNodePublicKey returned private key material: %q", name, got)
			}
		}
	})

	t.Run("an unusable identity degrades instead of failing twice", func(t *testing.T) {
		// startVPNGateway is what refuses to start an enabled gateway without a
		// usable identity, and it does so with a message that names the
		// variable. Answering hard here as well would report one deployment fault
		// twice, in two different words, and the management plane would stop
		// serving the endpoints that do not need the identity at all.
		for name, supplied := range map[string]string{
			"missing": "", "blank": "   ", "garbage": "not-a-wireguard-key",
		} {
			t.Setenv(vpn.NodePrivateKeyEnv, supplied)
			if got := vpnNodePublicKey(enabled); got != "" {
				t.Errorf("%s: vpnNodePublicKey = %q, want the empty string", name, got)
			}
		}
	})
}
