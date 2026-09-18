package auth

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

// IdentityStore is the transaction seam every identity service uses. Binding
// the credential write, the replay guard, and the audit row to one transaction
// is what makes an interrupted enrollment or login impossible to observe half
// applied. *storage.DB satisfies it.
type IdentityStore interface {
	// IdentityTransaction binds every repository to one transaction so a
	// credential write, its replay guard, and its audit row commit together.
	IdentityTransaction(ctx context.Context, fn func(storage.IdentityRepositories) error) error
	// IdentityRead serves queries without taking the writer lock.
	IdentityRead(ctx context.Context, fn func(storage.IdentityRepositories) error) error
}

// MFAConfig carries the operator-facing TOTP settings.
type MFAConfig struct {
	// Issuer is the label authenticator apps show next to the account.
	Issuer string
	// Params are the TOTP digits, period, and accepted skew.
	Params TOTPParams
	// RecoveryCodes is how many single-use bypass codes an enrollment gets.
	RecoveryCodes int
	// EnrollmentTTL bounds how long an unconfirmed enrollment stays usable.
	EnrollmentTTL time.Duration
}

// DefaultMFAConfig matches the common authenticator-app defaults.
func DefaultMFAConfig() MFAConfig {
	return MFAConfig{Issuer: "TunnelMesh", Params: DefaultTOTPParams(), RecoveryCodes: 10, EnrollmentTTL: 15 * time.Minute}
}

func (c MFAConfig) withDefaults() MFAConfig {
	if c.Issuer == "" {
		c.Issuer = "TunnelMesh"
	}
	if c.Params.Digits == 0 {
		c.Params = DefaultTOTPParams()
	}
	if c.RecoveryCodes <= 0 {
		c.RecoveryCodes = 10
	}
	if c.EnrollmentTTL <= 0 {
		c.EnrollmentTTL = 15 * time.Minute
	}
	return c
}

// MFAStatusView is the self-service description of an account's second factor.
// It never carries the secret or any code.
type MFAStatusView struct {
	Status                 storage.MFAStatus
	EnrolledAt             *time.Time
	EnabledAt              *time.Time
	LastUsedAt             *time.Time
	RemainingRecoveryCodes int
	ExpiresAt              *time.Time
}

// Enrollment is returned exactly once, at enrollment time.
type Enrollment struct {
	Secret        string
	OTPAuthURL    string
	RecoveryCodes []string
	ExpiresAt     time.Time
}

// VerifyResult reports which factor satisfied the check so the caller can audit
// it and warn the user when recovery codes are running out.
type VerifyResult struct {
	Method                 string
	RemainingRecoveryCodes int
	RecoveryCodesExhausted bool
}

// MFAService owns the TOTP lifecycle. It has no transport knowledge and never
// sees a plaintext secret after enrollment returns.
type MFAService struct {
	store   IdentityStore
	secrets SecretProvider
	cfg     MFAConfig
	now     func() time.Time
}

// NewMFAService builds the service. A nil SecretProvider fails closed on every
// operation that would have to store a secret.
func NewMFAService(store IdentityStore, secrets SecretProvider, cfg MFAConfig) *MFAService {
	if secrets == nil {
		secrets = NewSecretProvider(nil)
	}
	return &MFAService{store: store, secrets: secrets, cfg: cfg.withDefaults(), now: func() time.Time { return time.Now().UTC() }}
}

// SetClock overrides the time source for tests.
func (s *MFAService) SetClock(now func() time.Time) {
	if now != nil {
		s.now = now
	}
}

// IsEnabled reports whether the account has a confirmed, unexpired enrollment.
// The login path calls it to resolve the effective policy.
func (s *MFAService) IsEnabled(ctx context.Context, userID string) (bool, error) {
	row, usable, err := s.load(ctx, userID)
	if err != nil {
		return false, err
	}
	return usable && row.Status == storage.MFAStatusEnabled, nil
}

// load returns the enrollment and whether it is still usable. An expired
// pending enrollment is reported as absent so a stale QR code can never be
// confirmed later, which is the whole point of bounding the enrollment window.
func (s *MFAService) load(ctx context.Context, userID string) (storage.UserMFA, bool, error) {
	var row storage.UserMFA
	err := s.store.IdentityRead(ctx, func(repos storage.IdentityRepositories) error {
		found, err := repos.MFA.Get(ctx, userID)
		if err != nil {
			return err
		}
		row = found
		return nil
	})
	if errors.Is(err, sql.ErrNoRows) {
		return storage.UserMFA{}, false, nil
	}
	if err != nil {
		return storage.UserMFA{}, false, err
	}
	if row.Status == storage.MFAStatusPending && !row.EnrolledAt.Add(s.cfg.EnrollmentTTL).After(s.now()) {
		return row, false, nil
	}
	return row, true, nil
}

// Status describes the account's second factor for the console.
func (s *MFAService) Status(ctx context.Context, userID string) (MFAStatusView, error) {
	row, usable, err := s.load(ctx, userID)
	if err != nil {
		return MFAStatusView{}, err
	}
	if !usable {
		return MFAStatusView{Status: ""}, nil
	}
	view := MFAStatusView{
		Status:     row.Status,
		EnrolledAt: copyTime(row.EnrolledAt),
		EnabledAt:  row.EnabledAt,
		LastUsedAt: row.LastUsedAt,
	}
	if row.Status == storage.MFAStatusPending {
		expires := row.EnrolledAt.Add(s.cfg.EnrollmentTTL)
		view.ExpiresAt = &expires
	}
	var remaining int
	err = s.store.IdentityRead(ctx, func(repos storage.IdentityRepositories) error {
		count, err := repos.RecoveryCodes.CountUnused(ctx, userID)
		remaining = count
		return err
	})
	if err != nil {
		return MFAStatusView{}, err
	}
	view.RemainingRecoveryCodes = remaining
	return view, nil
}

func copyTime(v time.Time) *time.Time {
	if v.IsZero() {
		return nil
	}
	return &v
}

// Enroll creates or replaces a pending enrollment. The plaintext secret and the
// recovery codes leave the process exactly once, in the return value.
func (s *MFAService) Enroll(ctx context.Context, userID, currentPassword string) (Enrollment, error) {
	if !s.secrets.Available() {
		return Enrollment{}, ErrSecretStorageUnavailable
	}
	secret, err := GenerateTOTPSecret()
	if err != nil {
		return Enrollment{}, err
	}
	ciphertext, nonce, keyID, version, err := s.secrets.Encrypt(secret)
	if err != nil {
		return Enrollment{}, err
	}
	codes, hashes, err := GenerateRecoveryCodes(s.cfg.RecoveryCodes)
	if err != nil {
		return Enrollment{}, err
	}
	otpauthURL, err := func() (string, error) {
		var account string
		if err := s.store.IdentityTransaction(ctx, func(repos storage.IdentityRepositories) error {
			user, err := repos.Users.Get(ctx, userID)
			if err != nil {
				return err
			}
			// A local-password account must re-prove the password before a new
			// second factor is bound to it, so a hijacked session cannot be
			// made durable by enrolling the attacker's authenticator.
			if user.PasswordHash != storage.PasswordHashNone {
				if currentPassword == "" {
					return ErrCurrentPasswordRequired
				}
				if !verifyPassword(currentPassword, user.PasswordHash) {
					return ErrCurrentPasswordInvalid
				}
			}
			account = user.Username
			enrolled := s.now()
			if err := repos.MFA.Upsert(ctx, storage.UserMFA{
				UserID:           userID,
				SecretCiphertext: base64.StdEncoding.EncodeToString(ciphertext),
				SecretNonce:      base64.StdEncoding.EncodeToString(nonce),
				SecretKeyID:      keyID,
				SecretVersion:    version,
				Status:           storage.MFAStatusPending,
				EnrolledAt:       enrolled,
				LastUsedStep:     -1,
			}); err != nil {
				return err
			}
			if err := repos.RecoveryCodes.ReplaceAll(ctx, userID, hashes); err != nil {
				return err
			}
			return repos.Audits.Create(ctx, storage.AuditLog{
				ActorUserID:  userID,
				Action:       "auth.mfa.enroll",
				ResourceType: "user_mfa",
				ResourceID:   userID,
				Details:      `{"digits":` + fmt.Sprint(s.cfg.Params.Digits) + `,"periodSeconds":` + fmt.Sprint(int(s.cfg.Params.Period/time.Second)) + `,"recoveryCodes":` + fmt.Sprint(len(hashes)) + `}`,
			})
		}); err != nil {
			return "", err
		}
		return OTPAuthURL(s.cfg.Issuer, account, secret, s.cfg.Params)
	}()
	if err != nil {
		return Enrollment{}, err
	}
	return Enrollment{Secret: secret, OTPAuthURL: otpauthURL, RecoveryCodes: codes, ExpiresAt: s.now().Add(s.cfg.EnrollmentTTL)}, nil
}

// Enable confirms a pending enrollment with one live code.
func (s *MFAService) Enable(ctx context.Context, userID, code string) error {
	row, usable, err := s.load(ctx, userID)
	if err != nil {
		return err
	}
	if !usable {
		return ErrMFANotEnrolled
	}
	if row.Status == storage.MFAStatusEnabled {
		return ErrMFAAlreadyEnabled
	}
	secret, err := s.openSecret(row)
	if err != nil {
		return err
	}
	step, ok, err := VerifyTOTP(secret, code, s.now(), s.cfg.Params)
	if err != nil {
		return err
	}
	if !ok {
		return ErrMFACodeInvalid
	}
	return s.store.IdentityTransaction(ctx, func(repos storage.IdentityRepositories) error {
		if err := repos.MFA.MarkUsed(ctx, userID, step, s.now()); err != nil {
			if errors.Is(err, storage.ErrMFAStepReplay) {
				return ErrMFAReplayDetected
			}
			return err
		}
		if err := repos.MFA.SetStatus(ctx, userID, storage.MFAStatusEnabled, s.now()); err != nil {
			return err
		}
		return repos.Audits.Create(ctx, storage.AuditLog{
			ActorUserID:  userID,
			Action:       "auth.mfa.enable",
			ResourceType: "user_mfa",
			ResourceID:   userID,
			Details:      `{"method":"totp"}`,
		})
	})
}

// openSecret decrypts the stored shared secret. It is the only place the
// plaintext exists, and it is never logged or returned.
func (s *MFAService) openSecret(row storage.UserMFA) (string, error) {
	if !s.secrets.Available() {
		return "", ErrSecretStorageUnavailable
	}
	ciphertext, err := base64.StdEncoding.DecodeString(row.SecretCiphertext)
	if err != nil {
		return "", fmt.Errorf("decode totp ciphertext: %w", err)
	}
	nonce, err := base64.StdEncoding.DecodeString(row.SecretNonce)
	if err != nil {
		return "", fmt.Errorf("decode totp nonce: %w", err)
	}
	return s.secrets.Decrypt(ciphertext, nonce, row.SecretKeyID, row.SecretVersion)
}

// VerifyCode accepts either a TOTP code or a recovery code. It is called by the
// login flow while a challenge is open; attempt accounting belongs to the
// challenge, not to this method.
func (s *MFAService) VerifyCode(ctx context.Context, userID, code string) (VerifyResult, error) {
	row, usable, err := s.load(ctx, userID)
	if err != nil {
		return VerifyResult{}, err
	}
	if !usable {
		return VerifyResult{}, ErrMFANotEnrolled
	}
	if row.Status != storage.MFAStatusEnabled {
		return VerifyResult{}, ErrMFAPendingNotConfirmed
	}
	if IsRecoveryCode(code) {
		return s.verifyRecoveryCode(ctx, userID, code)
	}
	secret, err := s.openSecret(row)
	if err != nil {
		return VerifyResult{}, err
	}
	step, ok, err := VerifyTOTP(secret, code, s.now(), s.cfg.Params)
	if err != nil {
		return VerifyResult{}, err
	}
	if !ok {
		return VerifyResult{}, ErrMFACodeInvalid
	}
	err = s.store.IdentityTransaction(ctx, func(repos storage.IdentityRepositories) error {
		if err := repos.MFA.MarkUsed(ctx, userID, step, s.now()); err != nil {
			if errors.Is(err, storage.ErrMFAStepReplay) {
				return ErrMFAReplayDetected
			}
			return err
		}
		return repos.Audits.Create(ctx, storage.AuditLog{
			ActorUserID:  userID,
			Action:       "auth.mfa.verify",
			ResourceType: "user_mfa",
			ResourceID:   userID,
			Details:      `{"method":"totp"}`,
		})
	})
	if err != nil {
		return VerifyResult{}, err
	}
	remaining, err := s.remainingRecoveryCodes(ctx, userID)
	if err != nil {
		return VerifyResult{}, err
	}
	return VerifyResult{Method: "totp", RemainingRecoveryCodes: remaining, RecoveryCodesExhausted: remaining == 0}, nil
}

func (s *MFAService) verifyRecoveryCode(ctx context.Context, userID, code string) (VerifyResult, error) {
	candidate := string(HashRecoveryCode(code))
	var consumed bool
	var remaining int
	err := s.store.IdentityTransaction(ctx, func(repos storage.IdentityRepositories) error {
		ok, err := repos.RecoveryCodes.Consume(ctx, userID, candidate)
		if err != nil {
			return err
		}
		consumed = ok
		if !consumed {
			return nil
		}
		count, err := repos.RecoveryCodes.CountUnused(ctx, userID)
		if err != nil {
			return err
		}
		remaining = count
		if err := repos.MFA.TouchLastUsed(ctx, userID, s.now()); err != nil {
			return err
		}
		return repos.Audits.Create(ctx, storage.AuditLog{
			ActorUserID:  userID,
			Action:       "auth.mfa.verify",
			ResourceType: "user_mfa",
			ResourceID:   userID,
			Details:      `{"method":"recovery","remaining":` + fmt.Sprint(count) + `}`,
		})
	})
	if err != nil {
		return VerifyResult{}, err
	}
	if !consumed {
		return VerifyResult{}, ErrMFACodeInvalid
	}
	return VerifyResult{Method: "recovery", RemainingRecoveryCodes: remaining, RecoveryCodesExhausted: remaining == 0}, nil
}

func (s *MFAService) remainingRecoveryCodes(ctx context.Context, userID string) (int, error) {
	var remaining int
	err := s.store.IdentityTransaction(ctx, func(repos storage.IdentityRepositories) error {
		count, err := repos.RecoveryCodes.CountUnused(ctx, userID)
		remaining = count
		return err
	})
	return remaining, err
}

// Disable removes the second factor. It also revokes every trusted device,
// because a device was trusted on the strength of the factor being removed.
func (s *MFAService) Disable(ctx context.Context, userID, code string, policy AuthPolicy) error {
	if policy.Required {
		return ErrMFARequiredByPolicy
	}
	row, usable, err := s.load(ctx, userID)
	if err != nil {
		return err
	}
	if !usable {
		return ErrMFANotEnrolled
	}
	if row.Status != storage.MFAStatusEnabled {
		return ErrMFAPendingNotConfirmed
	}
	// Removing a second factor is the highest-impact self-service action, so it
	// always needs one live proof of possession, TOTP or recovery.
	if _, err := s.VerifyCode(ctx, userID, code); err != nil {
		return err
	}
	return s.store.IdentityTransaction(ctx, func(repos storage.IdentityRepositories) error {
		if err := repos.MFA.Delete(ctx, userID); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err := repos.RecoveryCodes.DeleteByUser(ctx, userID); err != nil {
			return err
		}
		revoked, err := repos.Devices.RevokeAllForUser(ctx, userID, s.now())
		if err != nil {
			return err
		}
		return repos.Audits.Create(ctx, storage.AuditLog{
			ActorUserID:  userID,
			Action:       "auth.mfa.disable",
			ResourceType: "user_mfa",
			ResourceID:   userID,
			Details:      `{"revokedDevices":` + fmt.Sprint(revoked) + `}`,
		})
	})
}

// AdminReset clears a user's second factor so they can re-enroll. It is the
// documented lockout recovery path and revokes trusted devices for the same
// reason Disable does.
func (s *MFAService) AdminReset(ctx context.Context, actorID, userID string) error {
	return s.store.IdentityTransaction(ctx, func(repos storage.IdentityRepositories) error {
		if _, err := repos.MFA.Get(ctx, userID); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err := repos.MFA.Delete(ctx, userID); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err := repos.RecoveryCodes.DeleteByUser(ctx, userID); err != nil {
			return err
		}
		revoked, err := repos.Devices.RevokeAllForUser(ctx, userID, s.now())
		if err != nil {
			return err
		}
		return repos.Audits.Create(ctx, storage.AuditLog{
			ActorUserID:  actorID,
			Action:       "auth.mfa.reset",
			ResourceType: "user_mfa",
			ResourceID:   userID,
			Details:      `{"revokedDevices":` + fmt.Sprint(revoked) + `}`,
		})
	})
}

// RegenerateRecoveryCodes replaces the bypass codes for an account that already
// has MFA enabled. The new plaintext codes are returned exactly once.
func (s *MFAService) RegenerateRecoveryCodes(ctx context.Context, userID, code string) ([]string, error) {
	row, usable, err := s.load(ctx, userID)
	if err != nil {
		return nil, err
	}
	if !usable {
		return nil, ErrMFANotEnrolled
	}
	if row.Status != storage.MFAStatusEnabled {
		return nil, ErrMFAPendingNotConfirmed
	}
	secret, err := s.openSecret(row)
	if err != nil {
		return nil, err
	}
	step, ok, err := VerifyTOTP(secret, code, s.now(), s.cfg.Params)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrMFACodeInvalid
	}
	codes, hashes, err := GenerateRecoveryCodes(s.cfg.RecoveryCodes)
	if err != nil {
		return nil, err
	}
	err = s.store.IdentityTransaction(ctx, func(repos storage.IdentityRepositories) error {
		if err := repos.MFA.MarkUsed(ctx, userID, step, s.now()); err != nil {
			if errors.Is(err, storage.ErrMFAStepReplay) {
				return ErrMFAReplayDetected
			}
			return err
		}
		if err := repos.RecoveryCodes.ReplaceAll(ctx, userID, hashes); err != nil {
			return err
		}
		return repos.Audits.Create(ctx, storage.AuditLog{
			ActorUserID:  userID,
			Action:       "auth.mfa.recovery_regenerate",
			ResourceType: "user_mfa",
			ResourceID:   userID,
			Details:      `{"recoveryCodes":` + fmt.Sprint(len(hashes)) + `}`,
		})
	})
	if err != nil {
		return nil, err
	}
	return codes, nil
}
