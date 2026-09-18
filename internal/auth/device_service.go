package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

// DeviceTrustConfig describes how long a browser stays trusted and how many
// trusted devices one account may hold. Enabled has no default on purpose: the
// runtime must set it from auth_settings.device_trust_enabled, so a partially
// built configuration fails closed instead of silently trusting browsers.
type DeviceTrustConfig struct {
	Enabled    bool
	TTL        time.Duration
	MaxPerUser int
	// TouchThrottle bounds how often a validation refreshes last_seen_at, so a
	// busy console does not turn every request into a write.
	TouchThrottle time.Duration
}

// DefaultDeviceTrustConfig keeps the documented defaults in one place.
func DefaultDeviceTrustConfig() DeviceTrustConfig {
	return DeviceTrustConfig{Enabled: true, TTL: 30 * 24 * time.Hour, MaxPerUser: 10, TouchThrottle: 5 * time.Minute}
}

func (c DeviceTrustConfig) withDefaults() DeviceTrustConfig {
	if c.TTL <= 0 {
		c.TTL = 30 * 24 * time.Hour
	}
	if c.MaxPerUser <= 0 {
		c.MaxPerUser = 10
	}
	if c.TouchThrottle <= 0 {
		c.TouchThrottle = 5 * time.Minute
	}
	return c
}

// DeviceView is what the console may see about a trusted device. The token and
// its hash are never part of it.
// The JSON tags match the camelCase convention every other management API
// response uses, so the console can bind a device row without a second mapping.
type DeviceView struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	UserAgent  string     `json:"userAgent"`
	IP         string     `json:"ip"`
	TrustedAt  time.Time  `json:"trustedAt"`
	ExpiresAt  time.Time  `json:"expiresAt"`
	LastSeenAt *time.Time `json:"lastSeenAt"`
	Current    bool       `json:"current"`
}

// DeviceService issues, validates, and revokes trusted-device tokens. Only the
// SHA-256 digest of a token is stored, so a database leak cannot be replayed.
//
// The service configuration is a seed, not the live policy: whether device trust
// is on, how long a device stays trusted, and how many an account may hold are
// read from the authoritative auth_settings row on every decision, so an
// operator change made through PUT /auth/policy takes effect immediately and
// survives a redeploy of a node carrying a stale config file.
type DeviceService struct {
	store    IdentityStore
	cfg      DeviceTrustConfig
	defaults storage.AuthSettings
	now      func() time.Time
}

// NewDeviceService builds the service from the process configuration, which is
// used to seed auth_settings the first time it is read.
func NewDeviceService(store IdentityStore, cfg DeviceTrustConfig) *DeviceService {
	cfg = cfg.withDefaults()
	return &DeviceService{
		store: store,
		cfg:   cfg,
		// TouchThrottle has no database column: it is a write-amplification guard
		// rather than a security policy, so it stays process-local.
		defaults: DefaultAuthSettings(storage.MFAModeDisabled, cfg.Enabled, true, cfg.TTL, cfg.MaxPerUser, 0),
		now:      func() time.Time { return time.Now().UTC() },
	}
}

// effective renders the authoritative policy for one decision. It is called
// inside an existing transaction or read so the policy and the device row it
// governs can never be observed from two different points in time.
func (s *DeviceService) effective(repos storage.IdentityRepositories, ctx context.Context) (DeviceTrustConfig, error) {
	settings, err := repos.Settings.SeedIfMissing(ctx, s.defaults)
	if err != nil {
		return DeviceTrustConfig{}, err
	}
	cfg := s.cfg
	cfg.Enabled = settings.DeviceTrustEnabled
	if settings.DeviceTrustTTLSeconds > 0 {
		cfg.TTL = time.Duration(settings.DeviceTrustTTLSeconds) * time.Second
	}
	if settings.MaxTrustedDevices > 0 {
		cfg.MaxPerUser = settings.MaxTrustedDevices
	}
	return cfg, nil
}

// effectiveForRead resolves the policy without writing. A missing row means the
// identity layer has never been used, so the process configuration still
// describes the deployment accurately.
func (s *DeviceService) effectiveForRead(ctx context.Context, repos storage.IdentityRepositories) DeviceTrustConfig {
	settings, err := repos.Settings.Get(ctx)
	if err != nil {
		return s.cfg
	}
	cfg := s.cfg
	cfg.Enabled = settings.DeviceTrustEnabled
	if settings.DeviceTrustTTLSeconds > 0 {
		cfg.TTL = time.Duration(settings.DeviceTrustTTLSeconds) * time.Second
	}
	if settings.MaxTrustedDevices > 0 {
		cfg.MaxPerUser = settings.MaxTrustedDevices
	}
	return cfg
}

// SetClock overrides the time source for tests.
func (s *DeviceService) SetClock(now func() time.Time) {
	if now != nil {
		s.now = now
	}
}

// Config exposes the effective configuration so handlers can build the cookie
// with identical attributes.
func (s *DeviceService) Config() DeviceTrustConfig { return s.cfg }

// GenerateDeviceToken returns a plaintext token and its digest. The plaintext is
// shown to the client once and never persisted.
func GenerateDeviceToken() (token, hash string, err error) {
	buffer := make([]byte, 32)
	if _, err := rand.Read(buffer); err != nil {
		return "", "", fmt.Errorf("generate device token: %w", err)
	}
	token = base64.RawURLEncoding.EncodeToString(buffer)
	return token, HashDeviceToken(token), nil
}

// HashDeviceToken returns the hex digest stored in user_devices.token_hash.
func HashDeviceToken(token string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return hex.EncodeToString(sum[:])
}

// Issue trusts a device and returns the plaintext token exactly once. Trusting a
// device beyond the configured cap evicts the least recently seen one, so the
// account can never accumulate an unbounded set of long-lived credentials.
func (s *DeviceService) Issue(ctx context.Context, userID, userAgent, ip string) (string, storage.UserDevice, error) {
	if strings.TrimSpace(userID) == "" {
		return "", storage.UserDevice{}, errors.New("user id is required")
	}
	token, hash, err := GenerateDeviceToken()
	if err != nil {
		return "", storage.UserDevice{}, err
	}
	now := s.now()
	var device storage.UserDevice
	err = s.store.IdentityTransaction(ctx, func(repos storage.IdentityRepositories) error {
		// The kill switch is evaluated against the stored policy so disabling
		// device trust in the console stops new trust grants on every node at
		// once instead of only on nodes that were restarted with a new file.
		effective, err := s.effective(repos, ctx)
		if err != nil {
			return err
		}
		if !effective.Enabled {
			return ErrDeviceTrustDisabled
		}
		count, err := repos.Devices.CountActive(ctx, userID)
		if err != nil {
			return err
		}
		if count >= effective.MaxPerUser {
			oldest, err := repos.Devices.OldestActive(ctx, userID)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if err == nil {
				if err := repos.Devices.Revoke(ctx, userID, oldest.ID, now); err != nil {
					return err
				}
			}
		}
		created, err := repos.Devices.Create(ctx, storage.UserDevice{
			UserID:     userID,
			TokenHash:  hash,
			UserAgent:  userAgent,
			IP:         ip,
			TrustedAt:  now,
			ExpiresAt:  now.Add(effective.TTL),
			LastSeenAt: &now,
		})
		if err != nil {
			return err
		}
		device = created
		return repos.Audits.Create(ctx, storage.AuditLog{
			ActorUserID:  userID,
			Action:       "auth.device.trust",
			ResourceType: "user_device",
			ResourceID:   created.ID,
			Details:      `{"expiresAt":"` + created.ExpiresAt.UTC().Format(time.RFC3339) + `"}`,
		})
	})
	if err != nil {
		return "", storage.UserDevice{}, err
	}
	return token, device, nil
}

// Validate reports whether a presented token is currently trusted. A valid
// lookup also refreshes last_seen_at, throttled so a busy console does not write
// on every request.
func (s *DeviceService) Validate(ctx context.Context, token string) (storage.UserDevice, bool, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return storage.UserDevice{}, false, nil
	}
	var device storage.UserDevice
	enabled := s.cfg.Enabled
	err := s.store.IdentityRead(ctx, func(repos storage.IdentityRepositories) error {
		// Reading the policy on the login path is what makes "disable device
		// trust" revoke every bypass immediately, without a token or session
		// change and without waiting for the cookies to expire.
		enabled = s.effectiveForRead(ctx, repos).Enabled
		if !enabled {
			return nil
		}
		found, err := repos.Devices.GetByTokenHash(ctx, HashDeviceToken(token))
		if err != nil {
			return err
		}
		device = found
		return nil
	})
	if errors.Is(err, sql.ErrNoRows) {
		return storage.UserDevice{}, false, nil
	}
	if err != nil {
		return storage.UserDevice{}, false, err
	}
	if !enabled {
		return storage.UserDevice{}, false, nil
	}
	now := s.now()
	if device.RevokedAt != nil || !device.ExpiresAt.After(now) {
		return storage.UserDevice{}, false, nil
	}
	if device.LastSeenAt == nil || now.Sub(*device.LastSeenAt) >= s.cfg.TouchThrottle {
		if err := s.store.IdentityRead(ctx, func(repos storage.IdentityRepositories) error {
			return repos.Devices.Touch(ctx, device.ID, now)
		}); err != nil {
			// A failed freshness write must not fail a login.
			_ = err
		}
		device.LastSeenAt = &now
	}
	return device, true, nil
}

// List returns the caller's active trusted devices, newest first, marking the
// device that presented the current token.
func (s *DeviceService) List(ctx context.Context, userID, currentToken string) ([]DeviceView, error) {
	currentHash := ""
	if strings.TrimSpace(currentToken) != "" {
		currentHash = HashDeviceToken(currentToken)
	}
	var views []DeviceView
	now := s.now()
	err := s.store.IdentityRead(ctx, func(repos storage.IdentityRepositories) error {
		devices, err := repos.Devices.ListByUser(ctx, userID)
		if err != nil {
			return err
		}
		for _, device := range devices {
			if device.RevokedAt != nil || !device.ExpiresAt.After(now) {
				continue
			}
			views = append(views, DeviceView{
				ID:         device.ID,
				Name:       device.Name,
				UserAgent:  device.UserAgent,
				IP:         device.IP,
				TrustedAt:  device.TrustedAt,
				ExpiresAt:  device.ExpiresAt,
				LastSeenAt: device.LastSeenAt,
				Current:    currentHash != "" && device.TokenHash == currentHash,
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return views, nil
}

// Rename sets a user-facing label for a device the caller owns.
func (s *DeviceService) Rename(ctx context.Context, userID, deviceID, name string) error {
	return s.store.IdentityTransaction(ctx, func(repos storage.IdentityRepositories) error {
		if _, err := repos.Devices.Get(ctx, userID, deviceID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrDeviceNotFound
			}
			return err
		}
		return repos.Devices.Rename(ctx, userID, deviceID, name)
	})
}

// Revoke untrusts one device. An administrator may revoke another account's
// device; a normal user may only revoke their own.
func (s *DeviceService) Revoke(ctx context.Context, actorID, userID, deviceID string, actorIsAdmin bool) error {
	if !actorIsAdmin && actorID != userID {
		return ErrForbidden
	}
	return s.store.IdentityTransaction(ctx, func(repos storage.IdentityRepositories) error {
		if _, err := repos.Devices.Get(ctx, userID, deviceID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrDeviceNotFound
			}
			return err
		}
		if err := repos.Devices.Revoke(ctx, userID, deviceID, s.now()); err != nil {
			return err
		}
		return repos.Audits.Create(ctx, storage.AuditLog{
			ActorUserID:  actorID,
			Action:       "auth.device.revoke",
			ResourceType: "user_device",
			ResourceID:   deviceID,
			Details:      `{"ownerUserId":"` + userID + `"}`,
		})
	})
}

// RevokeAll untrusts every device of one account. It is used both by the
// self-service "sign out everywhere" action and by MFA reset.
func (s *DeviceService) RevokeAll(ctx context.Context, actorID, userID string, actorIsAdmin bool) (int64, error) {
	if !actorIsAdmin && actorID != userID {
		return 0, ErrForbidden
	}
	var revoked int64
	err := s.store.IdentityTransaction(ctx, func(repos storage.IdentityRepositories) error {
		count, err := repos.Devices.RevokeAllForUser(ctx, userID, s.now())
		if err != nil {
			return err
		}
		revoked = count
		if count == 0 {
			return nil
		}
		return repos.Audits.Create(ctx, storage.AuditLog{
			ActorUserID:  actorID,
			Action:       "auth.device.revoke_all",
			ResourceType: "user_device",
			ResourceID:   userID,
			Details:      `{"revoked":` + fmt.Sprint(count) + `}`,
		})
	})
	return revoked, err
}

// CountActive reports how many devices are currently trusted across all
// accounts. It feeds the observability gauge.
func (s *DeviceService) CountActive(ctx context.Context) (int, error) {
	var count int
	err := s.store.IdentityRead(ctx, func(repos storage.IdentityRepositories) error {
		total, err := repos.Devices.CountActiveAll(ctx)
		count = total
		return err
	})
	return count, err
}
