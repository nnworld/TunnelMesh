package vpn_test

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/tunnelmesh/tunnelmesh/internal/vpn"
)

// TestErrorCodesAreStableAndUnique pins the management-API error contract from
// the design spec §12.1. The code strings are public: docs/api/openapi.yaml,
// the admin console and user troubleshooting tables key off them, so a rename
// here is a breaking API change and must fail this test.
func TestErrorCodesAreStableAndUnique(t *testing.T) {
	cases := []struct {
		err    *vpn.Error
		status int
		code   string
	}{
		{vpn.ErrPeerInvalid, http.StatusBadRequest, "vpn_peer_invalid"},
		{vpn.ErrIPPoolInvalid, http.StatusBadRequest, "vpn_ip_pool_invalid"},
		{vpn.ErrPeerNotFound, http.StatusNotFound, "vpn_peer_not_found"},
		{vpn.ErrPeerConflict, http.StatusConflict, "vpn_peer_conflict"},
		{vpn.ErrAgentCapabilityMissing, http.StatusConflict, "vpn_agent_capability_missing"},
		{vpn.ErrIPPoolExhausted, http.StatusConflict, "vpn_ip_pool_exhausted"},
		{vpn.ErrNodeDisabled, http.StatusConflict, "vpn_node_disabled"},
		{vpn.ErrSecretUnavailable, http.StatusServiceUnavailable, "credential_secret_unavailable"},
		{vpn.ErrCapacityExhausted, http.StatusServiceUnavailable, "vpn_capacity_exhausted"},
		{vpn.ErrNotImplemented, http.StatusNotImplemented, "vpn_not_implemented"},
	}
	seen := make(map[string]int, len(cases))
	for _, tc := range cases {
		if tc.err == nil {
			t.Fatalf("%s: sentinel is nil", tc.code)
		}
		if tc.err.Code != tc.code {
			t.Errorf("code = %q, want %q", tc.err.Code, tc.code)
		}
		if tc.err.Status != tc.status {
			t.Errorf("%s: status = %d, want %d", tc.code, tc.err.Status, tc.status)
		}
		if tc.err.Error() == "" {
			t.Errorf("%s: message must not be empty", tc.code)
		}
		if first, dup := seen[tc.code]; dup {
			t.Errorf("%s: code reused by two sentinels (status %d and %d)", tc.code, first, tc.err.Status)
		}
		seen[tc.code] = tc.err.Status
	}
	if len(seen) != len(cases) {
		t.Fatalf("expected %d distinct codes, got %d", len(cases), len(seen))
	}
}

// TestErrorMessageFallsBackToCode guards the invariant the HTTP layer relies
// on: an Error always renders a non-empty string, so writeAPIError can never
// emit an empty msg.
func TestErrorMessageFallsBackToCode(t *testing.T) {
	if got := vpn.NewError(http.StatusBadRequest, "vpn_peer_invalid", "").Error(); got != "vpn_peer_invalid" {
		t.Fatalf("Error() = %q, want the code", got)
	}
	if got := vpn.NewError(http.StatusBadRequest, "vpn_peer_invalid", "name is required").Error(); got != "name is required" {
		t.Fatalf("Error() = %q, want the message", got)
	}
	var nilErr *vpn.Error
	if got := nilErr.Error(); got != "" {
		t.Fatalf("nil Error() = %q, want empty", got)
	}
}

// TestErrorIsMatchableBySentinel proves callers can use errors.Is on a wrapped
// error, which is how the service layer maps storage sentinels onto VPN codes.
func TestErrorIsMatchableBySentinel(t *testing.T) {
	wrapped := fmt.Errorf("issue peer: %w", vpn.ErrIPPoolExhausted)
	if !errors.Is(wrapped, vpn.ErrIPPoolExhausted) {
		t.Fatal("errors.Is must match the wrapped sentinel")
	}
	if errors.Is(wrapped, vpn.ErrPeerInvalid) {
		t.Fatal("errors.Is must not match a different sentinel")
	}
	// Distinct sentinels that share a code must still be distinguishable, so a
	// second NewError with the same code is a different value.
	if errors.Is(vpn.NewError(http.StatusConflict, "vpn_peer_conflict", "a"), vpn.ErrPeerConflict) {
		t.Fatal("a freshly built Error must not match the package sentinel")
	}
}
