//go:build vpn

package server

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/tunnelmesh/tunnelmesh/internal/vpn"
)

// The tagged build must produce a gateway, and closing it must be safe from the
// first commit that introduces the tag: every later task extends this object, so
// a leak or a double-close panic here would be inherited by all of them.
func TestStartVPNGatewayWithTheBuildTagBuildsAGateway(t *testing.T) {
	if !vpnBuildTagged {
		t.Fatal("vpnBuildTagged is false in a build with the vpn tag")
	}
	pair, err := vpn.GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}
	t.Setenv(vpn.NodePrivateKeyEnv, pair.PrivateKey)
	gateway, err := startVPNGateway(context.Background(), VPNGatewayDeps{Config: enabledVPNTestConfig(t), NodeID: "node-a"})
	if err != nil {
		t.Fatalf("startVPNGateway: %v", err)
	}
	if gateway == nil {
		t.Fatal("an enabled gateway returned a nil data plane")
	}
	if err := gateway.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	if err := gateway.Close(); err != nil {
		t.Errorf("Close is not idempotent: %v", err)
	}
}

// With the tag present the node key becomes the blocking prerequisite, and the
// error has to name the variable: it is the one prerequisite whose absence looks
// exactly like a networking problem from the outside.
func TestStartVPNGatewayWithTheBuildTagRequiresTheNodeKey(t *testing.T) {
	t.Setenv(vpn.NodePrivateKeyEnv, "")
	gateway, err := startVPNGateway(context.Background(), VPNGatewayDeps{Config: enabledVPNTestConfig(t), NodeID: "node-a"})
	if !errors.Is(err, vpn.ErrNodeKeyMissing) {
		t.Fatalf("error = %v, want ErrNodeKeyMissing", err)
	}
	if gateway != nil {
		t.Errorf("a refused gateway must be nil, got %T", gateway)
	}
	if !strings.Contains(err.Error(), vpn.NodePrivateKeyEnv) {
		t.Errorf("error %q does not name %s", err, vpn.NodePrivateKeyEnv)
	}

	t.Setenv(vpn.NodePrivateKeyEnv, vpn.EncodeKey(make([]byte, 32)))
	if _, err := startVPNGateway(context.Background(), VPNGatewayDeps{Config: enabledVPNTestConfig(t), NodeID: "node-a"}); !errors.Is(err, vpn.ErrNodeKeyInvalid) {
		t.Errorf("an all-zero node key gave %v, want ErrNodeKeyInvalid", err)
	}
}

// A configuration the gateway could not serve must fail at startup rather than
// produce a listener that drops everything.
func TestStartVPNGatewayWithTheBuildTagRejectsAnUnusableConfig(t *testing.T) {
	pair, err := vpn.GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}
	t.Setenv(vpn.NodePrivateKeyEnv, pair.PrivateKey)
	cfg := enabledVPNTestConfig(t)
	cfg.Listen = "not-an-address"
	gateway, err := startVPNGateway(context.Background(), VPNGatewayDeps{Config: cfg, NodeID: "node-a"})
	if err == nil {
		if gateway != nil {
			_ = gateway.Close()
		}
		t.Fatal("startVPNGateway accepted an unusable configuration")
	}
	if !strings.Contains(err.Error(), "server.vpn") {
		t.Errorf("error %q does not name the configuration section", err)
	}
	if gateway != nil {
		t.Errorf("a refused gateway must be nil, got %T", gateway)
	}
}
