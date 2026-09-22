//go:build !vpn

package server

import (
	"context"

	"github.com/tunnelmesh/tunnelmesh/internal/vpn"
)

// vpnBuildTagged is false in every binary that is not built with -tags vpn. It is
// a constant rather than a reflection trick so the check costs nothing and so the
// tagged and untagged builds differ in exactly one place.
const vpnBuildTagged = false

// newVPNGateway has no implementation in this build.
//
// The point of the stub is not to be called: startVPNGateway refuses before
// reaching it, and the test that asserts the refusal is the reason this file
// exists at all. It returns the sentinel rather than panicking so an embedder
// that assembles a gateway by hand gets an error it can log and act on.
func newVPNGateway(_ context.Context, _ VPNGatewayDeps, _ vpn.NodeIdentity) (VPNDataPlane, error) {
	return nil, ErrVPNBuildTagMissing
}
