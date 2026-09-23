//go:build vpn

package server

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/storage"
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

// The runtime is where the gateway becomes part of a serving process, so the
// assembly is asserted end to end rather than one call at a time: an enabled
// server.vpn must produce a gateway that holds a UDP port, hand that same gateway
// to the management API as both the live-state source and the hot-reload sink, and
// give the port back when the runtime closes.
func TestNewServerRuntimeWithTheBuildTagStartsTheVPNGateway(t *testing.T) {
	db, err := storage.OpenSQLite(context.Background(), "file:runtime-vpn-gateway?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	pair, err := vpn.GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}
	t.Setenv(vpn.NodePrivateKeyEnv, pair.PrivateKey)

	runtime, err := NewServerRuntime(db, AgentSessionConfig{}, RuntimeConfig{NodeID: "node-a", VPN: enabledVPNTestConfig(t)})
	if err != nil {
		t.Fatalf("NewServerRuntime: %v", err)
	}
	if runtime.vpnGateway == nil {
		t.Fatal("an enabled server.vpn installed no gateway")
	}
	gateway, ok := runtime.vpnGateway.(*vpnGateway)
	if !ok {
		t.Fatalf("the runtime installed %T, want *vpnGateway", runtime.vpnGateway)
	}
	port := gateway.localPort()
	if port == 0 {
		t.Error("the runtime's gateway holds no UDP port")
	}
	// The identity check matters more than the non-nil one: a second gateway, or a
	// wrapper that dropped the sink, would leave the management API writing peer
	// rows nothing reloads.
	if runtime.API.vpnDataPlane != gateway {
		t.Errorf("the management API holds %v, want the runtime's gateway", runtime.API.vpnDataPlane)
	}

	if err := runtime.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	// A port the runtime kept would fail the restart that follows a shutdown with
	// address-in-use, which turns a routine deploy into an outage.
	deadline := time.Now().Add(10 * time.Second)
	for {
		probe, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: int(port)})
		if err == nil {
			_ = probe.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("port %d was still held 10s after the runtime closed: %v", port, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	// The serve shutdown chain and the runtime's own Close both reach the gateway,
	// so the second call has to be a no-op rather than a second teardown.
	if err := runtime.Close(); err != nil {
		t.Errorf("a second Close returned %v, want nil", err)
	}
}

// A listen address the gateway cannot bind must fail the process at startup. The
// alternative is a server that reports itself ready while every peer configured
// against it times out, which is the failure an operator can least diagnose from
// outside the box.
func TestNewServerRuntimeWithTheBuildTagReportsAnUnusableVPNListenAddress(t *testing.T) {
	db, err := storage.OpenSQLite(context.Background(), "file:runtime-vpn-gateway-taken?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	pair, err := vpn.GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}
	t.Setenv(vpn.NodePrivateKeyEnv, pair.PrivateKey)

	taken, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer taken.Close()
	cfg := enabledVPNTestConfig(t)
	cfg.Listen = taken.LocalAddr().String()

	runtime, err := NewServerRuntime(db, AgentSessionConfig{}, RuntimeConfig{NodeID: "node-a", VPN: cfg})
	if err == nil {
		if runtime != nil {
			_ = runtime.Close()
		}
		t.Fatalf("the runtime came up with server.vpn.listen %q already taken", cfg.Listen)
	}
	if runtime != nil {
		t.Errorf("a refused runtime must be nil, got %T", runtime)
	}
	if !strings.Contains(err.Error(), "vpn") {
		t.Errorf("error %q does not say which subsystem refused to start", err)
	}
}
