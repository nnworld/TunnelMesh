package auth

import (
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

// AuthPolicy is the effective authentication decision for one account at one
// moment. It is computed, never stored, so the database policy row and the
// per-account override cannot disagree.
type AuthPolicy struct {
	// Mode is the global operator setting.
	Mode storage.MFAMode
	// Required reports whether this account must present a second factor now.
	Required bool
	// DeviceTrustEnabled reports whether trusted devices may be issued at all.
	DeviceTrustEnabled bool
	// AllowTrustedDeviceBypass reports whether a trusted device skips MFA.
	AllowTrustedDeviceBypass bool
	// SessionTokenTTL is the console token lifetime. Zero keeps the historical
	// non-expiring behaviour.
	SessionTokenTTL time.Duration
}

// ResolvePolicy combines the stored settings, the per-account override, and the
// account's enrollment state. A per-account requirement wins over a globally
// disabled policy so an administrator can protect one account without forcing
// MFA on everybody; the reverse is also true, because "required" is a floor.
func ResolvePolicy(settings storage.AuthSettings, user storage.User, mfaEnabled bool) AuthPolicy {
	policy := AuthPolicy{
		Mode:                     settings.MFAMode,
		DeviceTrustEnabled:       settings.DeviceTrustEnabled,
		AllowTrustedDeviceBypass: settings.AllowTrustedDeviceBypass,
		SessionTokenTTL:          time.Duration(settings.SessionTokenTTLSeconds) * time.Second,
	}
	if policy.Mode == "" {
		policy.Mode = storage.MFAModeDisabled
	}
	switch {
	case user.MFARequired:
		policy.Required = true
	case policy.Mode == storage.MFAModeRequired:
		policy.Required = true
	case policy.Mode == storage.MFAModeOptional && mfaEnabled:
		policy.Required = true
	}
	// A bypass only makes sense when device trust is actually enabled.
	policy.AllowTrustedDeviceBypass = policy.AllowTrustedDeviceBypass && policy.DeviceTrustEnabled
	return policy
}

// DefaultAuthSettings renders the process configuration as the seed row for
// auth_settings. It is applied once, when the row does not exist yet; after
// that the database row is authoritative.
func DefaultAuthSettings(mfaMode storage.MFAMode, deviceTrustEnabled, allowBypass bool, deviceTTL time.Duration, maxDevices int, sessionTTL time.Duration) storage.AuthSettings {
	if mfaMode == "" {
		mfaMode = storage.MFAModeDisabled
	}
	if deviceTTL <= 0 {
		deviceTTL = 30 * 24 * time.Hour
	}
	if maxDevices <= 0 {
		maxDevices = 10
	}
	if sessionTTL < 0 {
		sessionTTL = 0
	}
	return storage.AuthSettings{
		MFAMode:                  mfaMode,
		DeviceTrustEnabled:       deviceTrustEnabled,
		DeviceTrustTTLSeconds:    int64(deviceTTL / time.Second),
		AllowTrustedDeviceBypass: allowBypass,
		MaxTrustedDevices:        maxDevices,
		SessionTokenTTLSeconds:   int64(sessionTTL / time.Second),
	}
}
