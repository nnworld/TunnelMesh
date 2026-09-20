package storage

import (
	"errors"
	"strings"
	"time"
)

// vpnIPLeaseTTL applies the same default leaseRepo uses for a connection lease.
// A non-positive TTL would persist a lease that is already expired and therefore
// instantly takeable by any other node, so it is normalised to one minute rather
// than rejected: an absent TTL means "use the default", not "this is a bug".
func vpnIPLeaseTTL(ttl time.Duration) time.Duration {
	if ttl <= 0 {
		return time.Minute
	}
	return ttl
}

// validateVPNIPLease enforces the identity fields a lease needs in order to be
// fenced. AllocatedCount and Epoch are only checked for sign because both start
// at zero and are advanced by the repository, never supplied by a caller.
func validateVPNIPLease(lease VPNIPLease) error {
	switch {
	case strings.TrimSpace(lease.NodeID) == "":
		return errors.New("vpn ip lease node is required")
	case strings.TrimSpace(lease.Subnet) == "":
		return errors.New("vpn ip lease subnet is required")
	case strings.TrimSpace(lease.LeaseHolder) == "":
		return errors.New("vpn ip lease holder is required")
	case lease.AllocatedCount < 0:
		return errors.New("vpn ip lease allocated count must not be negative")
	case lease.Epoch < 0:
		return errors.New("vpn ip lease epoch must not be negative")
	}
	return nil
}
