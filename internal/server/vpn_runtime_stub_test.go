//go:build !vpn

package server

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/tunnelmesh/tunnelmesh/internal/config"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
	"github.com/tunnelmesh/tunnelmesh/internal/vpn"
)

// Spec §5.4 and ADR 0002 both require the untagged binary to refuse an enabled
// gateway loudly. A silent no-op would leave the operator with a configuration
// that looks correct, a security group opened for UDP 51820, and nothing
// listening on it.
func TestStartVPNGatewayWithoutTheBuildTagFailsFast(t *testing.T) {
	if vpnBuildTagged {
		t.Fatal("vpnBuildTagged is true in a build without the vpn tag")
	}
	pair, err := vpn.GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}
	t.Setenv(vpn.NodePrivateKeyEnv, pair.PrivateKey)
	gateway, err := startVPNGateway(context.Background(), VPNGatewayDeps{Config: enabledVPNTestConfig(t), NodeID: "node-a"})
	if !errors.Is(err, ErrVPNBuildTagMissing) {
		t.Fatalf("error = %v, want ErrVPNBuildTagMissing", err)
	}
	if gateway != nil {
		t.Errorf("a refused gateway must be nil, got %T", gateway)
	}
	// The message is quoted verbatim in the deployment documentation, so the flag
	// it names is part of the contract rather than a wording choice.
	if !strings.Contains(err.Error(), "-tags vpn") {
		t.Errorf("error %q does not tell the operator to rebuild with -tags vpn", err)
	}
}

// The build tag outranks the node key: telling an operator to inject a secret
// into a binary that cannot use it would send them down a dead end, while
// rebuilding with the tag surfaces the missing key on the next start.
func TestStartVPNGatewayWithoutTheBuildTagReportsTheTagBeforeTheKey(t *testing.T) {
	t.Setenv(vpn.NodePrivateKeyEnv, "")
	_, err := startVPNGateway(context.Background(), VPNGatewayDeps{Config: enabledVPNTestConfig(t), NodeID: "node-a"})
	if !errors.Is(err, ErrVPNBuildTagMissing) {
		t.Fatalf("error = %v, want ErrVPNBuildTagMissing", err)
	}
	if errors.Is(err, vpn.ErrNodeKeyMissing) {
		t.Error("the missing key must not be reported before the missing build tag")
	}
}

// startVPNGateway's refusal is only half the contract: the runtime is the assembly
// point an operator's configuration actually reaches, so it has to surface the
// same refusal instead of coming up with an enabled gateway it silently ignored.
// A process that did would leave UDP 51820 open in the security group with nothing
// behind it, which from the outside looks like a network fault.
func TestNewServerRuntimeWithoutTheBuildTagRefusesAnEnabledVPNConfig(t *testing.T) {
	db, err := storage.OpenSQLite(context.Background(), "file:runtime-vpn-stub-refused?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	runtime, err := NewServerRuntime(db, AgentSessionConfig{}, RuntimeConfig{NodeID: "node-a", VPN: enabledVPNTestConfig(t)})
	if !errors.Is(err, ErrVPNBuildTagMissing) {
		t.Fatalf("error = %v, want ErrVPNBuildTagMissing", err)
	}
	if runtime != nil {
		t.Errorf("a refused runtime must be nil, got %T", runtime)
		_ = runtime.Close()
	}
	if !strings.Contains(err.Error(), "-tags vpn") {
		t.Errorf("error %q does not tell the operator to rebuild with -tags vpn", err)
	}
}

// A disabled gateway must leave the runtime exactly as it was before the data plane
// existed: no gateway in the runtime, none installed in the management API, and
// therefore nothing bound and nothing to stop. This is the assertion behind
// ADR 0002's stop-loss path, where flipping server.vpn.enabled off and restarting
// is the whole remedy.
func TestNewServerRuntimeWithoutTheBuildTagCreatesNoVPNResource(t *testing.T) {
	db, err := storage.OpenSQLite(context.Background(), "file:runtime-vpn-stub-disabled?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	runtime, err := NewServerRuntime(db, AgentSessionConfig{}, RuntimeConfig{NodeID: "node-a", VPN: config.VPNConfig{}})
	if err != nil {
		t.Fatalf("NewServerRuntime: %v", err)
	}
	defer runtime.Close()

	if runtime.vpnGateway != nil {
		t.Errorf("a disabled server.vpn installed %T, want no gateway", runtime.vpnGateway)
	}
	if runtime.API.vpnDataPlane != nil {
		t.Errorf("a disabled server.vpn installed %T in the management API, want none", runtime.API.vpnDataPlane)
	}
}
