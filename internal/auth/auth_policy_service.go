package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

// Policy bounds. They exist so a typo cannot produce a policy that is
// technically valid and operationally absurd, such as a one-second device trust
// window or a million trusted devices per account.
const (
	MinDeviceTrustTTLSeconds = int64(3600)
	MaxDeviceTrustTTLSeconds = int64(7776000)
	MinTrustedDevices        = 1
	MaxTrustedDevices        = 100
)

// AuthPolicyInput is one PUT /auth/policy request.
type AuthPolicyInput struct {
	MFAMode                  storage.MFAMode `json:"mfaMode"`
	DeviceTrustEnabled       bool            `json:"deviceTrustEnabled"`
	DeviceTrustTTLSeconds    int64           `json:"deviceTrustTtlSeconds"`
	AllowTrustedDeviceBypass bool            `json:"allowTrustedDeviceBypass"`
	MaxTrustedDevices        int             `json:"maxTrustedDevices"`
	SessionTokenTTLSeconds   int64           `json:"sessionTokenTtlSeconds"`
}

// AuthPolicyService reads and writes the authoritative auth_settings row.
// Configuration supplies the seed; after that the database wins, so an operator
// decision made through the console is never silently reverted by a redeploy.
type AuthPolicyService struct {
	store    IdentityStore
	defaults storage.AuthSettings
}

// NewAuthPolicyService builds the service with the process defaults used for the
// one-time seed.
func NewAuthPolicyService(store IdentityStore, defaults storage.AuthSettings) *AuthPolicyService {
	if defaults.MFAMode == "" {
		defaults = DefaultAuthSettings(storage.MFAModeDisabled, true, true, 0, 0, 0)
	}
	return &AuthPolicyService{store: store, defaults: defaults}
}

// Get returns the current policy, seeding it from configuration on first read.
func (s *AuthPolicyService) Get(ctx context.Context) (storage.AuthSettings, error) {
	if s == nil || s.store == nil {
		return storage.AuthSettings{}, errors.New("auth policy store is required")
	}
	var settings storage.AuthSettings
	err := s.store.IdentityTransaction(ctx, func(repos storage.IdentityRepositories) error {
		seeded, err := repos.Settings.SeedIfMissing(ctx, s.defaults)
		settings = seeded
		return err
	})
	return settings, err
}

// Update validates and stores a new policy, recording which fields changed.
//
// Disabling MFA globally while an account still carries the per-account override
// is allowed: the override is a floor, not a suggestion, and ResolvePolicy keeps
// enforcing it. The audit row makes the interaction visible.
func (s *AuthPolicyService) Update(ctx context.Context, actorID string, in AuthPolicyInput) (storage.AuthSettings, error) {
	if s == nil || s.store == nil {
		return storage.AuthSettings{}, errors.New("auth policy store is required")
	}
	if err := ValidateAuthPolicyInput(in); err != nil {
		return storage.AuthSettings{}, err
	}
	current, err := s.Get(ctx)
	if err != nil {
		return storage.AuthSettings{}, err
	}
	next := storage.AuthSettings{
		MFAMode:                  in.MFAMode,
		DeviceTrustEnabled:       in.DeviceTrustEnabled,
		DeviceTrustTTLSeconds:    in.DeviceTrustTTLSeconds,
		AllowTrustedDeviceBypass: in.AllowTrustedDeviceBypass,
		MaxTrustedDevices:        in.MaxTrustedDevices,
		SessionTokenTTLSeconds:   in.SessionTokenTTLSeconds,
	}
	changed := changedPolicyFields(current, next)
	err = s.store.IdentityTransaction(ctx, func(repos storage.IdentityRepositories) error {
		if err := repos.Settings.Update(ctx, next); err != nil {
			return err
		}
		return repos.Audits.Create(ctx, storage.AuditLog{
			ActorUserID:  actorID,
			Action:       "auth.policy.update",
			ResourceType: "auth_settings",
			ResourceID:   "global",
			Details:      policyAuditDetails(changed, next),
		})
	})
	if err != nil {
		return storage.AuthSettings{}, err
	}
	next.UpdatedAt = current.UpdatedAt
	return next, nil
}

// ValidateAuthPolicyInput is exported so the configuration loader and the HTTP
// handler reject the same values with the same message.
func ValidateAuthPolicyInput(in AuthPolicyInput) error {
	switch in.MFAMode {
	case storage.MFAModeDisabled, storage.MFAModeOptional, storage.MFAModeRequired:
	default:
		return fmt.Errorf("%w: mfaMode must be disabled, optional, or required", ErrAuthPolicyInvalid)
	}
	if in.DeviceTrustTTLSeconds < MinDeviceTrustTTLSeconds || in.DeviceTrustTTLSeconds > MaxDeviceTrustTTLSeconds {
		return fmt.Errorf("%w: deviceTrustTtlSeconds must be between %d and %d", ErrAuthPolicyInvalid, MinDeviceTrustTTLSeconds, MaxDeviceTrustTTLSeconds)
	}
	if in.MaxTrustedDevices < MinTrustedDevices || in.MaxTrustedDevices > MaxTrustedDevices {
		return fmt.Errorf("%w: maxTrustedDevices must be between %d and %d", ErrAuthPolicyInvalid, MinTrustedDevices, MaxTrustedDevices)
	}
	if in.SessionTokenTTLSeconds < 0 {
		return fmt.Errorf("%w: sessionTokenTtlSeconds must not be negative", ErrAuthPolicyInvalid)
	}
	// A bypass with trust disabled is inert, but storing it would silently grant a
	// bypass the moment an operator re-enables trust without re-reading the row.
	if in.AllowTrustedDeviceBypass && !in.DeviceTrustEnabled {
		return fmt.Errorf("%w: allowTrustedDeviceBypass requires deviceTrustEnabled", ErrAuthPolicyInvalid)
	}
	return nil
}

func changedPolicyFields(before, after storage.AuthSettings) []string {
	changed := []string{}
	if before.MFAMode != after.MFAMode {
		changed = append(changed, "mfaMode")
	}
	if before.DeviceTrustEnabled != after.DeviceTrustEnabled {
		changed = append(changed, "deviceTrustEnabled")
	}
	if before.DeviceTrustTTLSeconds != after.DeviceTrustTTLSeconds {
		changed = append(changed, "deviceTrustTtlSeconds")
	}
	if before.AllowTrustedDeviceBypass != after.AllowTrustedDeviceBypass {
		changed = append(changed, "allowTrustedDeviceBypass")
	}
	if before.MaxTrustedDevices != after.MaxTrustedDevices {
		changed = append(changed, "maxTrustedDevices")
	}
	if before.SessionTokenTTLSeconds != after.SessionTokenTTLSeconds {
		changed = append(changed, "sessionTokenTtlSeconds")
	}
	return changed
}

// policyAuditDetails records the changed field names with their new values. None
// of them is a secret, so including the values is safe and makes the audit row
// self-contained.
func policyAuditDetails(changed []string, next storage.AuthSettings) string {
	values := map[string]any{
		"mfaMode":                  string(next.MFAMode),
		"deviceTrustEnabled":       next.DeviceTrustEnabled,
		"deviceTrustTtlSeconds":    next.DeviceTrustTTLSeconds,
		"allowTrustedDeviceBypass": next.AllowTrustedDeviceBypass,
		"maxTrustedDevices":        next.MaxTrustedDevices,
		"sessionTokenTtlSeconds":   next.SessionTokenTTLSeconds,
	}
	changes := map[string]any{}
	for _, field := range changed {
		changes[field] = values[field]
	}
	details := map[string]any{"changed": changes, "fields": changed}
	encoded, err := json.Marshal(details)
	if err != nil {
		return `{"fields":[]}`
	}
	return string(encoded)
}

// PolicyView is the API shape for the settings row.
type PolicyView struct {
	MFAMode                  string `json:"mfaMode"`
	DeviceTrustEnabled       bool   `json:"deviceTrustEnabled"`
	DeviceTrustTTLSeconds    int64  `json:"deviceTrustTtlSeconds"`
	AllowTrustedDeviceBypass bool   `json:"allowTrustedDeviceBypass"`
	MaxTrustedDevices        int    `json:"maxTrustedDevices"`
	SessionTokenTTLSeconds   int64  `json:"sessionTokenTtlSeconds"`
	UpdatedAt                string `json:"updatedAt"`
}

// View renders the stored row for the API.
func View(settings storage.AuthSettings) PolicyView {
	updatedAt := ""
	if !settings.UpdatedAt.IsZero() {
		updatedAt = settings.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z07:00")
	}
	return PolicyView{
		MFAMode:                  string(normalizeMode(settings.MFAMode)),
		DeviceTrustEnabled:       settings.DeviceTrustEnabled,
		DeviceTrustTTLSeconds:    settings.DeviceTrustTTLSeconds,
		AllowTrustedDeviceBypass: settings.AllowTrustedDeviceBypass,
		MaxTrustedDevices:        settings.MaxTrustedDevices,
		SessionTokenTTLSeconds:   settings.SessionTokenTTLSeconds,
		UpdatedAt:                updatedAt,
	}
}

func normalizeMode(mode storage.MFAMode) storage.MFAMode {
	switch strings.ToLower(strings.TrimSpace(string(mode))) {
	case string(storage.MFAModeOptional):
		return storage.MFAModeOptional
	case string(storage.MFAModeRequired):
		return storage.MFAModeRequired
	default:
		return storage.MFAModeDisabled
	}
}
