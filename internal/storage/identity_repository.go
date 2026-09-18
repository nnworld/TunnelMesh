package storage

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// IsDuplicateError reports whether err is a unique-constraint or
// already-exists failure. Identity writes use it to turn a lost insert race
// into a typed conflict instead of a driver-specific message.
func IsDuplicateError(err error) bool { return isDuplicateError(err) }

// ErrIdentityConflict is returned when a unique identity constraint rejects a
// write, for example a second external identity with the same provider subject.
var ErrIdentityConflict = errors.New("identity already exists")

// tmFixed renders a timestamp with a fixed nine-digit fraction so lexicographic
// comparison in SQL matches chronological order. The generic tm helper trims
// trailing zeros, which makes "...T00:00:00Z" sort after "...T00:00:00.5Z";
// identity tables compare expiry columns directly and must not inherit that
// ambiguity.
func tmFixed(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format("2006-01-02T15:04:05.000000000Z07:00")
}

// AuthSettingsRepository owns the singleton runtime authentication policy.
type AuthSettingsRepository interface {
	Get(context.Context) (AuthSettings, error)
	Update(context.Context, AuthSettings) error
	// SeedIfMissing inserts the supplied defaults only when no row exists yet,
	// and always returns the authoritative stored value. Process configuration
	// seeds the row once; afterwards the database row wins.
	SeedIfMissing(context.Context, AuthSettings) (AuthSettings, error)
}

// OIDCProviderRepository stores identity-provider configuration. The client
// secret only ever exists as sealed ciphertext.
type OIDCProviderRepository interface {
	Create(context.Context, OIDCProvider) (OIDCProvider, error)
	Get(context.Context, string) (OIDCProvider, error)
	GetByName(context.Context, string) (OIDCProvider, error)
	Update(context.Context, OIDCProvider) error
	Delete(context.Context, string) error
	List(context.Context, string, int) (Page[OIDCProvider], error)
	ListEnabledPublic(context.Context) ([]OIDCProvider, error)
	CountEnabled(context.Context) (int, error)
}

// UserIdentityRepository links external subjects to local accounts.
type UserIdentityRepository interface {
	Create(context.Context, UserIdentity) (UserIdentity, error)
	GetByProviderSubject(context.Context, string, string) (UserIdentity, error)
	ListByUser(context.Context, string) ([]UserIdentity, error)
	UpdateLastLogin(context.Context, string, time.Time, string, string) error
	Delete(context.Context, string) error
	DeleteByUser(context.Context, string) error
	CountByUser(context.Context, string) (int, error)
}

// UserMFARepository stores the encrypted TOTP enrollment for one account.
type UserMFARepository interface {
	Upsert(context.Context, UserMFA) error
	Get(context.Context, string) (UserMFA, error)
	SetStatus(context.Context, string, MFAStatus, time.Time) error
	// MarkUsed advances the replay guard. It fails when step is not greater
	// than the stored value, so a TOTP code cannot be accepted twice.
	MarkUsed(context.Context, string, int64, time.Time) error
	// TouchLastUsed records activity without touching the replay guard. A
	// recovery-code login is already single-use through the consumed code row,
	// and writing a timestamp into last_used_step would poison every later
	// TOTP comparison.
	TouchLastUsed(context.Context, string, time.Time) error
	Delete(context.Context, string) error
	CountEnabled(context.Context) (int, error)
}

// UserRecoveryCodeRepository stores single-use bypass code hashes.
type UserRecoveryCodeRepository interface {
	ReplaceAll(context.Context, string, []string) error
	ListUnused(context.Context, string) ([]string, error)
	Consume(context.Context, string, string) (bool, error)
	DeleteByUser(context.Context, string) error
	CountUnused(context.Context, string) (int, error)
}

// UserDeviceRepository stores trusted-device token hashes.
type UserDeviceRepository interface {
	Create(context.Context, UserDevice) (UserDevice, error)
	GetByTokenHash(context.Context, string) (UserDevice, error)
	Get(context.Context, string, string) (UserDevice, error)
	ListByUser(context.Context, string) ([]UserDevice, error)
	Rename(context.Context, string, string, string) error
	Touch(context.Context, string, time.Time) error
	Revoke(context.Context, string, string, time.Time) error
	RevokeAllForUser(context.Context, string, time.Time) (int64, error)
	CountActive(context.Context, string) (int, error)
	OldestActive(context.Context, string) (UserDevice, error)
	CountActiveAll(context.Context) (int, error)
	DeleteExpired(context.Context, time.Time) (int64, error)
}

// AuthChallengeRepository stores short-lived encrypted flow state.
type AuthChallengeRepository interface {
	Create(context.Context, AuthChallenge) error
	Get(context.Context, string) (AuthChallenge, error)
	Consume(context.Context, string, time.Time) (bool, error)
	// IncrementAttempts records one failed guess. It reports the new attempt
	// count and whether the challenge is now unusable.
	IncrementAttempts(context.Context, string) (int, bool, error)
	DeleteExpired(context.Context, time.Time) (int64, error)
	CountPending(context.Context) (int, error)
}

// AuthLoginAttemptRepository is the cluster-safe brute-force counter.
type AuthLoginAttemptRepository interface {
	RegisterFailure(ctx context.Context, bucketKey string, window time.Duration, maxAttempts int, block time.Duration) (blocked bool, retryAfter time.Duration, err error)
	IsBlocked(context.Context, string) (bool, time.Duration, error)
	RegisterSuccess(context.Context, string) error
	CountBlocked(context.Context) (int, error)
	DeleteExpired(context.Context, time.Time) (int64, error)
}

const authSettingsColumns = `mfa_mode,device_trust_enabled,device_trust_ttl_seconds,allow_trusted_device_bypass,max_trusted_devices,session_token_ttl_seconds,updated_at`

type authSettingsRepo struct{ db dbExecutor }

func NewAuthSettingsRepository(db dbExecutor) AuthSettingsRepository {
	return &authSettingsRepo{db: db}
}

func scanAuthSettings(row rowScanner) (AuthSettings, error) {
	var s AuthSettings
	var mode string
	var deviceTrust, bypass int
	var updated string
	if err := row.Scan(&mode, &deviceTrust, &s.DeviceTrustTTLSeconds, &bypass, &s.MaxTrustedDevices, &s.SessionTokenTTLSeconds, &updated); err != nil {
		return AuthSettings{}, err
	}
	s.MFAMode = MFAMode(mode)
	s.DeviceTrustEnabled = deviceTrust != 0
	s.AllowTrustedDeviceBypass = bypass != 0
	s.UpdatedAt = parseTime(updated)
	return s, nil
}

func (r *authSettingsRepo) Get(ctx context.Context) (AuthSettings, error) {
	return scanAuthSettings(r.db.QueryRowContext(ctx, `SELECT `+authSettingsColumns+` FROM auth_settings WHERE id=1`))
}

func (r *authSettingsRepo) Update(ctx context.Context, s AuthSettings) error {
	if s.UpdatedAt.IsZero() {
		s.UpdatedAt = time.Now().UTC()
	}
	res, err := r.db.ExecContext(ctx, `UPDATE auth_settings SET mfa_mode=?,device_trust_enabled=?,device_trust_ttl_seconds=?,allow_trusted_device_bypass=?,max_trusted_devices=?,session_token_ttl_seconds=?,updated_at=? WHERE id=1`,
		string(s.MFAMode), boolInt(s.DeviceTrustEnabled), s.DeviceTrustTTLSeconds, boolInt(s.AllowTrustedDeviceBypass), s.MaxTrustedDevices, s.SessionTokenTTLSeconds, tmFixed(s.UpdatedAt))
	return checkAffected(res, err)
}

func (r *authSettingsRepo) SeedIfMissing(ctx context.Context, defaults AuthSettings) (AuthSettings, error) {
	if existing, err := r.Get(ctx); err == nil {
		return existing, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return AuthSettings{}, err
	}
	if defaults.MFAMode == "" {
		defaults.MFAMode = MFAModeDisabled
	}
	if defaults.UpdatedAt.IsZero() {
		defaults.UpdatedAt = time.Now().UTC()
	}
	// A concurrent node may seed the same row. Losing that race is fine: the
	// stored value is authoritative either way, so re-read and return it.
	if _, err := r.db.ExecContext(ctx, `INSERT INTO auth_settings(id,`+authSettingsColumns+`) VALUES(1,?,?,?,?,?,?,?)`,
		string(defaults.MFAMode), boolInt(defaults.DeviceTrustEnabled), defaults.DeviceTrustTTLSeconds,
		boolInt(defaults.AllowTrustedDeviceBypass), defaults.MaxTrustedDevices, defaults.SessionTokenTTLSeconds, tmFixed(defaults.UpdatedAt)); err != nil && !isDuplicateError(err) {
		return AuthSettings{}, err
	}
	return r.Get(ctx)
}

const oidcProviderColumns = `id,name,display_name,issuer,client_id,client_secret_ciphertext,client_secret_nonce,client_secret_key_id,client_secret_version,scopes,redirect_uri,authorization_endpoint,token_endpoint,userinfo_endpoint,jwks_uri,id_token_algs,username_claim,role_mappings,default_role,authoritative_roles,auto_create_users,fetch_userinfo,public_listed,enabled,created_at,updated_at`

type oidcProviderRepo struct{ db dbExecutor }

func NewOIDCProviderRepository(db dbExecutor) OIDCProviderRepository {
	return &oidcProviderRepo{db: db}
}

func scanOIDCProvider(row rowScanner) (OIDCProvider, error) {
	var p OIDCProvider
	var ciphertext, nonce, keyID, authz, token, userinfo, jwks sql.NullString
	var version sql.NullInt64
	var authoritative, autoCreate, fetchUserinfo, publicListed, enabled int
	var created, updated string
	if err := row.Scan(&p.ID, &p.Name, &p.DisplayName, &p.Issuer, &p.ClientID, &ciphertext, &nonce, &keyID, &version,
		&p.Scopes, &p.RedirectURI, &authz, &token, &userinfo, &jwks, &p.IDTokenAlgs, &p.UsernameClaim,
		&p.RoleMappings, &p.DefaultRole, &authoritative, &autoCreate, &fetchUserinfo, &publicListed, &enabled, &created, &updated); err != nil {
		return OIDCProvider{}, err
	}
	p.ClientSecretCiphertext, p.ClientSecretNonce, p.ClientSecretKeyID = ciphertext.String, nonce.String, keyID.String
	p.ClientSecretVersion = int(version.Int64)
	p.AuthorizationEndpoint, p.TokenEndpoint = authz.String, token.String
	p.UserinfoEndpoint, p.JWKSURI = userinfo.String, jwks.String
	p.AuthoritativeRoles = authoritative != 0
	p.AutoCreateUsers = autoCreate != 0
	p.FetchUserinfo = fetchUserinfo != 0
	p.PublicListed = publicListed != 0
	p.Enabled = enabled != 0
	p.CreatedAt, p.UpdatedAt = parseTime(created), parseTime(updated)
	return p, nil
}

func oidcProviderArgs(p OIDCProvider) []any {
	return []any{p.ID, p.Name, p.DisplayName, p.Issuer, p.ClientID,
		nullString(p.ClientSecretCiphertext), nullString(p.ClientSecretNonce), nullString(p.ClientSecretKeyID), nullInt(p.ClientSecretVersion),
		p.Scopes, p.RedirectURI, nullString(p.AuthorizationEndpoint), nullString(p.TokenEndpoint), nullString(p.UserinfoEndpoint), nullString(p.JWKSURI),
		p.IDTokenAlgs, p.UsernameClaim, p.RoleMappings, p.DefaultRole,
		boolInt(p.AuthoritativeRoles), boolInt(p.AutoCreateUsers), boolInt(p.FetchUserinfo), boolInt(p.PublicListed), boolInt(p.Enabled),
		tmFixed(p.CreatedAt), tmFixed(p.UpdatedAt)}
}

func nullString(v string) any {
	if v == "" {
		return nil
	}
	return v
}

func nullInt(v int) any {
	if v == 0 {
		return nil
	}
	return v
}

func (r *oidcProviderRepo) Create(ctx context.Context, p OIDCProvider) (OIDCProvider, error) {
	p.ID, p.CreatedAt, p.UpdatedAt = stamp(p.ID, p.CreatedAt, p.UpdatedAt, "oidc")
	if _, err := r.db.ExecContext(ctx, `INSERT INTO oidc_providers(`+oidcProviderColumns+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, oidcProviderArgs(p)...); err != nil {
		if isDuplicateError(err) {
			return OIDCProvider{}, ErrIdentityConflict
		}
		return OIDCProvider{}, err
	}
	return p, nil
}

func (r *oidcProviderRepo) Get(ctx context.Context, id string) (OIDCProvider, error) {
	if id == "" {
		return OIDCProvider{}, sql.ErrNoRows
	}
	return scanOIDCProvider(r.db.QueryRowContext(ctx, `SELECT `+oidcProviderColumns+` FROM oidc_providers WHERE id=?`, id))
}

func (r *oidcProviderRepo) GetByName(ctx context.Context, name string) (OIDCProvider, error) {
	if strings.TrimSpace(name) == "" {
		return OIDCProvider{}, sql.ErrNoRows
	}
	return scanOIDCProvider(r.db.QueryRowContext(ctx, `SELECT `+oidcProviderColumns+` FROM oidc_providers WHERE name=?`, name))
}

func (r *oidcProviderRepo) Update(ctx context.Context, p OIDCProvider) error {
	if p.UpdatedAt.IsZero() {
		p.UpdatedAt = time.Now().UTC()
	}
	args := oidcProviderArgs(p)
	res, err := r.db.ExecContext(ctx, `UPDATE oidc_providers SET name=?,display_name=?,issuer=?,client_id=?,client_secret_ciphertext=?,client_secret_nonce=?,client_secret_key_id=?,client_secret_version=?,scopes=?,redirect_uri=?,authorization_endpoint=?,token_endpoint=?,userinfo_endpoint=?,jwks_uri=?,id_token_algs=?,username_claim=?,role_mappings=?,default_role=?,authoritative_roles=?,auto_create_users=?,fetch_userinfo=?,public_listed=?,enabled=?,created_at=?,updated_at=? WHERE id=?`,
		append(args[1:], p.ID)...)
	return checkAffected(res, err)
}

func (r *oidcProviderRepo) Delete(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM oidc_providers WHERE id=?`, id)
	return checkAffected(res, err)
}

func (r *oidcProviderRepo) List(ctx context.Context, cursor string, limit int) (Page[OIDCProvider], error) {
	cursor, limit = pageArgs(cursor, limit)
	query := `SELECT ` + oidcProviderColumns + ` FROM oidc_providers`
	args := []any{}
	if decoded := decodeCursor(cursor); decoded != "" {
		query += ` WHERE id>?`
		args = append(args, decoded)
	}
	query += ` ORDER BY id LIMIT ?`
	args = append(args, limit+1)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return Page[OIDCProvider]{}, err
	}
	defer rows.Close()
	page := Page[OIDCProvider]{}
	for rows.Next() {
		provider, err := scanOIDCProvider(rows)
		if err != nil {
			return Page[OIDCProvider]{}, err
		}
		page.Items = append(page.Items, provider)
	}
	if err := rows.Err(); err != nil {
		return Page[OIDCProvider]{}, err
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.HasMore = true
		page.NextCursor = encodeCursor(page.Items[len(page.Items)-1].ID)
	}
	return page, nil
}

func (r *oidcProviderRepo) ListEnabledPublic(ctx context.Context) ([]OIDCProvider, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+oidcProviderColumns+` FROM oidc_providers WHERE enabled=1 AND public_listed=1 ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OIDCProvider
	for rows.Next() {
		provider, err := scanOIDCProvider(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, provider)
	}
	return out, rows.Err()
}

func (r *oidcProviderRepo) CountEnabled(ctx context.Context) (int, error) {
	var count int
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM oidc_providers WHERE enabled=1`).Scan(&count)
	return count, err
}

const userIdentityColumns = `id,user_id,provider_id,subject,email,display_name,created_at,last_login_at`

type userIdentityRepo struct{ db dbExecutor }

func NewUserIdentityRepository(db dbExecutor) UserIdentityRepository {
	return &userIdentityRepo{db: db}
}

func scanUserIdentity(row rowScanner) (UserIdentity, error) {
	var v UserIdentity
	var email, displayName, lastLogin sql.NullString
	var created string
	if err := row.Scan(&v.ID, &v.UserID, &v.ProviderID, &v.Subject, &email, &displayName, &created, &lastLogin); err != nil {
		return UserIdentity{}, err
	}
	v.Email, v.DisplayName = email.String, displayName.String
	v.LastLoginAt = parseTM(lastLogin)
	v.CreatedAt = parseTime(created)
	return v, nil
}

func (r *userIdentityRepo) Create(ctx context.Context, v UserIdentity) (UserIdentity, error) {
	v.ID, v.CreatedAt = stampCreate(v.ID, v.CreatedAt, "identity")
	if _, err := r.db.ExecContext(ctx, `INSERT INTO user_identities(`+userIdentityColumns+`) VALUES(?,?,?,?,?,?,?,?)`,
		v.ID, v.UserID, v.ProviderID, v.Subject, nullString(v.Email), nullString(v.DisplayName), tmFixed(v.CreatedAt), nullableTimeFixed(v.LastLoginAt)); err != nil {
		if isDuplicateError(err) {
			return UserIdentity{}, ErrIdentityConflict
		}
		return UserIdentity{}, err
	}
	return v, nil
}

func nullableTimeFixed(v *time.Time) any {
	if v == nil {
		return nil
	}
	return tmFixed(*v)
}

func (r *userIdentityRepo) GetByProviderSubject(ctx context.Context, providerID, subject string) (UserIdentity, error) {
	return scanUserIdentity(r.db.QueryRowContext(ctx, `SELECT `+userIdentityColumns+` FROM user_identities WHERE provider_id=? AND subject=?`, providerID, subject))
}

func (r *userIdentityRepo) ListByUser(ctx context.Context, userID string) ([]UserIdentity, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+userIdentityColumns+` FROM user_identities WHERE user_id=? ORDER BY created_at, id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UserIdentity
	for rows.Next() {
		v, err := scanUserIdentity(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (r *userIdentityRepo) UpdateLastLogin(ctx context.Context, id string, when time.Time, email, displayName string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE user_identities SET last_login_at=?,email=CASE WHEN ?='' THEN email ELSE ? END,display_name=CASE WHEN ?='' THEN display_name ELSE ? END WHERE id=?`,
		tmFixed(when), email, email, displayName, displayName, id)
	return err
}

func (r *userIdentityRepo) Delete(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM user_identities WHERE id=?`, id)
	return checkAffected(res, err)
}

func (r *userIdentityRepo) DeleteByUser(ctx context.Context, userID string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM user_identities WHERE user_id=?`, userID)
	return err
}

func (r *userIdentityRepo) CountByUser(ctx context.Context, userID string) (int, error) {
	var count int
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM user_identities WHERE user_id=?`, userID).Scan(&count)
	return count, err
}

type userMFARepo struct{ db dbExecutor }

func NewUserMFARepository(db dbExecutor) UserMFARepository { return &userMFARepo{db: db} }

func scanUserMFA(row rowScanner) (UserMFA, error) {
	var v UserMFA
	var status, keyID, enrolled, updated string
	var enabled, lastUsed sql.NullString
	if err := row.Scan(&v.UserID, &v.SecretCiphertext, &v.SecretNonce, &keyID, &v.SecretVersion, &status, &enrolled, &enabled, &lastUsed, &v.LastUsedStep, &updated); err != nil {
		return UserMFA{}, err
	}
	v.SecretKeyID, v.Status = keyID, MFAStatus(status)
	v.EnrolledAt, v.UpdatedAt = parseTime(enrolled), parseTime(updated)
	v.EnabledAt, v.LastUsedAt = parseTM(enabled), parseTM(lastUsed)
	return v, nil
}

func (r *userMFARepo) Upsert(ctx context.Context, v UserMFA) error {
	if v.UserID == "" {
		return errors.New("user id is required")
	}
	if v.EnrolledAt.IsZero() {
		v.EnrolledAt = time.Now().UTC()
	}
	if v.UpdatedAt.IsZero() {
		v.UpdatedAt = time.Now().UTC()
	}
	if v.Status == "" {
		v.Status = MFAStatusPending
	}
	if _, err := r.db.ExecContext(ctx, `INSERT INTO user_mfa(user_id,secret_ciphertext,secret_nonce,secret_key_id,secret_version,status,enrolled_at,enabled_at,last_used_at,last_used_step,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		v.UserID, v.SecretCiphertext, v.SecretNonce, v.SecretKeyID, v.SecretVersion, string(v.Status), tmFixed(v.EnrolledAt),
		nullableTimeFixed(v.EnabledAt), nullableTimeFixed(v.LastUsedAt), v.LastUsedStep, tmFixed(v.UpdatedAt)); err != nil {
		if !isDuplicateError(err) {
			return err
		}
		// Re-enrollment replaces the pending secret and always resets the
		// replay guard, so an old accepted step can never block a new secret.
		res, updateErr := r.db.ExecContext(ctx, `UPDATE user_mfa SET secret_ciphertext=?,secret_nonce=?,secret_key_id=?,secret_version=?,status=?,enrolled_at=?,enabled_at=NULL,last_used_at=NULL,last_used_step=-1,updated_at=? WHERE user_id=?`,
			v.SecretCiphertext, v.SecretNonce, v.SecretKeyID, v.SecretVersion, string(v.Status), tmFixed(v.EnrolledAt), tmFixed(v.UpdatedAt), v.UserID)
		return checkAffected(res, updateErr)
	}
	return nil
}

func (r *userMFARepo) Get(ctx context.Context, userID string) (UserMFA, error) {
	return scanUserMFA(r.db.QueryRowContext(ctx, `SELECT user_id,secret_ciphertext,secret_nonce,secret_key_id,secret_version,status,enrolled_at,enabled_at,last_used_at,last_used_step,updated_at FROM user_mfa WHERE user_id=?`, userID))
}

func (r *userMFARepo) SetStatus(ctx context.Context, userID string, status MFAStatus, when time.Time) error {
	when = timeOrNow(when)
	res, err := r.db.ExecContext(ctx, `UPDATE user_mfa SET status=?,enabled_at=?,updated_at=? WHERE user_id=?`, string(status), tmFixed(when), tmFixed(when), userID)
	return checkAffected(res, err)
}

func (r *userMFARepo) MarkUsed(ctx context.Context, userID string, step int64, when time.Time) error {
	when = timeOrNow(when)
	res, err := r.db.ExecContext(ctx, `UPDATE user_mfa SET last_used_step=?,last_used_at=?,updated_at=? WHERE user_id=? AND last_used_step<?`, step, tmFixed(when), tmFixed(when), userID, step)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrMFAStepReplay
	}
	return nil
}

func (r *userMFARepo) TouchLastUsed(ctx context.Context, userID string, when time.Time) error {
	_, err := r.db.ExecContext(ctx, `UPDATE user_mfa SET last_used_at=?,updated_at=? WHERE user_id=?`, tmFixed(timeOrNow(when)), tmFixed(timeOrNow(when)), userID)
	return err
}

// ErrMFAStepReplay reports that a TOTP counter value was already accepted. It
// is distinct from a driver error so the caller can answer with an invalid-code
// response instead of a 500.
var ErrMFAStepReplay = errors.New("totp step already used")

func (r *userMFARepo) Delete(ctx context.Context, userID string) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM user_mfa WHERE user_id=?`, userID)
	return checkAffected(res, err)
}

func (r *userMFARepo) CountEnabled(ctx context.Context) (int, error) {
	var count int
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM user_mfa WHERE status=?`, string(MFAStatusEnabled)).Scan(&count)
	return count, err
}

type userRecoveryCodeRepo struct{ db dbExecutor }

func NewUserRecoveryCodeRepository(db dbExecutor) UserRecoveryCodeRepository {
	return &userRecoveryCodeRepo{db: db}
}

func (r *userRecoveryCodeRepo) ReplaceAll(ctx context.Context, userID string, hashes []string) error {
	if userID == "" {
		return errors.New("user id is required")
	}
	if _, err := r.db.ExecContext(ctx, `DELETE FROM user_recovery_codes WHERE user_id=?`, userID); err != nil {
		return err
	}
	now := tmFixed(time.Now().UTC())
	for _, hash := range hashes {
		if _, err := r.db.ExecContext(ctx, `INSERT INTO user_recovery_codes(id,user_id,code_hash,used_at,created_at) VALUES(?,?,?,NULL,?)`,
			newID("recovery"), userID, hash, now); err != nil {
			return err
		}
	}
	return nil
}

func (r *userRecoveryCodeRepo) ListUnused(ctx context.Context, userID string) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT code_hash FROM user_recovery_codes WHERE user_id=? AND used_at IS NULL ORDER BY id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var hash string
		if err := rows.Scan(&hash); err != nil {
			return nil, err
		}
		out = append(out, hash)
	}
	return out, rows.Err()
}

func (r *userRecoveryCodeRepo) Consume(ctx context.Context, userID, hash string) (bool, error) {
	res, err := r.db.ExecContext(ctx, `UPDATE user_recovery_codes SET used_at=? WHERE user_id=? AND code_hash=? AND used_at IS NULL`, tmFixed(time.Now().UTC()), userID, hash)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func (r *userRecoveryCodeRepo) DeleteByUser(ctx context.Context, userID string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM user_recovery_codes WHERE user_id=?`, userID)
	return err
}

func (r *userRecoveryCodeRepo) CountUnused(ctx context.Context, userID string) (int, error) {
	var count int
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM user_recovery_codes WHERE user_id=? AND used_at IS NULL`, userID).Scan(&count)
	return count, err
}

const userDeviceColumns = `id,user_id,token_hash,name,user_agent,ip,trusted_at,expires_at,last_seen_at,revoked_at`

type userDeviceRepo struct{ db dbExecutor }

func NewUserDeviceRepository(db dbExecutor) UserDeviceRepository { return &userDeviceRepo{db: db} }

func scanUserDevice(row rowScanner) (UserDevice, error) {
	var v UserDevice
	var name, agent, ip, lastSeen, revoked sql.NullString
	var trusted, expires string
	if err := row.Scan(&v.ID, &v.UserID, &v.TokenHash, &name, &agent, &ip, &trusted, &expires, &lastSeen, &revoked); err != nil {
		return UserDevice{}, err
	}
	v.Name, v.UserAgent, v.IP = name.String, agent.String, ip.String
	v.TrustedAt, v.ExpiresAt = parseTime(trusted), parseTime(expires)
	v.LastSeenAt, v.RevokedAt = parseTM(lastSeen), parseTM(revoked)
	return v, nil
}

func (r *userDeviceRepo) Create(ctx context.Context, v UserDevice) (UserDevice, error) {
	v.ID, v.TrustedAt = stampCreate(v.ID, v.TrustedAt, "device")
	if v.ExpiresAt.IsZero() {
		return UserDevice{}, errors.New("device expiry is required")
	}
	if v.TokenHash == "" {
		return UserDevice{}, errors.New("device token hash is required")
	}
	if _, err := r.db.ExecContext(ctx, `INSERT INTO user_devices(`+userDeviceColumns+`) VALUES(?,?,?,?,?,?,?,?,?,?)`,
		v.ID, v.UserID, v.TokenHash, nullString(v.Name), nullString(truncateField(v.UserAgent, 512)), nullString(truncateField(v.IP, 64)),
		tmFixed(v.TrustedAt), tmFixed(v.ExpiresAt), nullableTimeFixed(v.LastSeenAt), nullableTimeFixed(v.RevokedAt)); err != nil {
		if isDuplicateError(err) {
			return UserDevice{}, ErrIdentityConflict
		}
		return UserDevice{}, err
	}
	return v, nil
}

// truncateField keeps untrusted client-supplied strings inside the column width
// so a long User-Agent cannot fail an otherwise valid trust operation.
func truncateField(value string, max int) string {
	value = strings.TrimSpace(value)
	if len(value) <= max {
		return value
	}
	return value[:max]
}

func (r *userDeviceRepo) GetByTokenHash(ctx context.Context, hash string) (UserDevice, error) {
	if hash == "" {
		return UserDevice{}, sql.ErrNoRows
	}
	return scanUserDevice(r.db.QueryRowContext(ctx, `SELECT `+userDeviceColumns+` FROM user_devices WHERE token_hash=?`, hash))
}

func (r *userDeviceRepo) Get(ctx context.Context, userID, deviceID string) (UserDevice, error) {
	return scanUserDevice(r.db.QueryRowContext(ctx, `SELECT `+userDeviceColumns+` FROM user_devices WHERE id=? AND user_id=?`, deviceID, userID))
}

func (r *userDeviceRepo) ListByUser(ctx context.Context, userID string) ([]UserDevice, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+userDeviceColumns+` FROM user_devices WHERE user_id=? AND revoked_at IS NULL ORDER BY trusted_at DESC, id DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UserDevice
	for rows.Next() {
		v, err := scanUserDevice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (r *userDeviceRepo) Rename(ctx context.Context, userID, deviceID, name string) error {
	res, err := r.db.ExecContext(ctx, `UPDATE user_devices SET name=? WHERE id=? AND user_id=?`, nullString(truncateField(name, 191)), deviceID, userID)
	return checkAffected(res, err)
}

func (r *userDeviceRepo) Touch(ctx context.Context, id string, when time.Time) error {
	_, err := r.db.ExecContext(ctx, `UPDATE user_devices SET last_seen_at=? WHERE id=?`, tmFixed(timeOrNow(when)), id)
	return err
}

func (r *userDeviceRepo) Revoke(ctx context.Context, userID, deviceID string, when time.Time) error {
	when = timeOrNow(when)
	res, err := r.db.ExecContext(ctx, `UPDATE user_devices SET revoked_at=CASE WHEN revoked_at IS NULL THEN ? ELSE revoked_at END WHERE id=? AND user_id=?`, tmFixed(when), deviceID, userID)
	return checkAffected(res, err)
}

func (r *userDeviceRepo) RevokeAllForUser(ctx context.Context, userID string, when time.Time) (int64, error) {
	res, err := r.db.ExecContext(ctx, `UPDATE user_devices SET revoked_at=? WHERE user_id=? AND revoked_at IS NULL`, tmFixed(timeOrNow(when)), userID)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return n, err
}

func (r *userDeviceRepo) CountActive(ctx context.Context, userID string) (int, error) {
	var count int
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM user_devices WHERE user_id=? AND revoked_at IS NULL AND expires_at>?`, userID, tmFixed(time.Now().UTC())).Scan(&count)
	return count, err
}

func (r *userDeviceRepo) OldestActive(ctx context.Context, userID string) (UserDevice, error) {
	return scanUserDevice(r.db.QueryRowContext(ctx,
		`SELECT `+userDeviceColumns+` FROM user_devices WHERE user_id=? AND revoked_at IS NULL AND expires_at>? ORDER BY COALESCE(last_seen_at, trusted_at), id LIMIT 1`,
		userID, tmFixed(time.Now().UTC())))
}

func (r *userDeviceRepo) CountActiveAll(ctx context.Context) (int, error) {
	var count int
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM user_devices WHERE revoked_at IS NULL AND expires_at>?`, tmFixed(time.Now().UTC())).Scan(&count)
	return count, err
}

func (r *userDeviceRepo) DeleteExpired(ctx context.Context, olderThan time.Time) (int64, error) {
	res, err := r.db.ExecContext(ctx, `DELETE FROM user_devices WHERE expires_at<? OR (revoked_at IS NOT NULL AND revoked_at<?)`, tmFixed(olderThan), tmFixed(olderThan))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

const authChallengeColumns = `id,kind,user_id,payload_ciphertext,payload_nonce,payload_key_id,payload_version,attempts,max_attempts,consumed_at,expires_at,created_at`

type authChallengeRepo struct{ db dbExecutor }

func NewAuthChallengeRepository(db dbExecutor) AuthChallengeRepository {
	return &authChallengeRepo{db: db}
}

func scanAuthChallenge(row rowScanner) (AuthChallenge, error) {
	var v AuthChallenge
	var kind, userID, keyID, created, expires string
	var consumed sql.NullString
	if err := row.Scan(&v.ID, &kind, &userID, &v.PayloadCiphertext, &v.PayloadNonce, &keyID, &v.PayloadVersion,
		&v.Attempts, &v.MaxAttempts, &consumed, &expires, &created); err != nil {
		return AuthChallenge{}, err
	}
	v.Kind, v.UserID, v.PayloadKeyID = ChallengeKind(kind), userID, keyID
	v.ConsumedAt = parseTM(consumed)
	v.ExpiresAt, v.CreatedAt = parseTime(expires), parseTime(created)
	return v, nil
}

func (r *authChallengeRepo) Create(ctx context.Context, v AuthChallenge) error {
	if v.ID == "" || v.PayloadCiphertext == "" {
		return errors.New("challenge id and encrypted payload are required")
	}
	if v.CreatedAt.IsZero() {
		v.CreatedAt = time.Now().UTC()
	}
	if v.ExpiresAt.IsZero() {
		return errors.New("challenge expiry is required")
	}
	if v.MaxAttempts <= 0 {
		v.MaxAttempts = 1
	}
	_, err := r.db.ExecContext(ctx, `INSERT INTO auth_challenges(`+authChallengeColumns+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		v.ID, string(v.Kind), v.UserID, v.PayloadCiphertext, v.PayloadNonce, v.PayloadKeyID, v.PayloadVersion,
		v.Attempts, v.MaxAttempts, nullableTimeFixed(v.ConsumedAt), tmFixed(v.ExpiresAt), tmFixed(v.CreatedAt))
	return err
}

func (r *authChallengeRepo) Get(ctx context.Context, id string) (AuthChallenge, error) {
	if id == "" {
		return AuthChallenge{}, sql.ErrNoRows
	}
	return scanAuthChallenge(r.db.QueryRowContext(ctx, `SELECT `+authChallengeColumns+` FROM auth_challenges WHERE id=?`, id))
}

func (r *authChallengeRepo) Consume(ctx context.Context, id string, when time.Time) (bool, error) {
	res, err := r.db.ExecContext(ctx, `UPDATE auth_challenges SET consumed_at=? WHERE id=? AND consumed_at IS NULL`, tmFixed(timeOrNow(when)), id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func (r *authChallengeRepo) IncrementAttempts(ctx context.Context, id string) (int, bool, error) {
	res, err := r.db.ExecContext(ctx, `UPDATE auth_challenges SET attempts=attempts+1 WHERE id=? AND consumed_at IS NULL`, id)
	if err != nil {
		return 0, false, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return 0, true, nil
	}
	var attempts, maxAttempts int
	if err := r.db.QueryRowContext(ctx, `SELECT attempts,max_attempts FROM auth_challenges WHERE id=?`, id).Scan(&attempts, &maxAttempts); err != nil {
		return 0, false, err
	}
	if attempts >= maxAttempts {
		// Exhausting the budget burns the challenge so a client cannot start a
		// fresh guess window by retrying the same identifier.
		if _, err := r.db.ExecContext(ctx, `UPDATE auth_challenges SET consumed_at=? WHERE id=? AND consumed_at IS NULL`, tmFixed(time.Now().UTC()), id); err != nil {
			return attempts, true, err
		}
		return attempts, true, nil
	}
	return attempts, false, nil
}

func (r *authChallengeRepo) DeleteExpired(ctx context.Context, now time.Time) (int64, error) {
	res, err := r.db.ExecContext(ctx, `DELETE FROM auth_challenges WHERE expires_at<?`, tmFixed(timeOrNow(now)))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (r *authChallengeRepo) CountPending(ctx context.Context) (int, error) {
	var count int
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM auth_challenges WHERE consumed_at IS NULL AND expires_at>?`, tmFixed(time.Now().UTC())).Scan(&count)
	return count, err
}

type authLoginAttemptRepo struct{ db dbExecutor }

func NewAuthLoginAttemptRepository(db dbExecutor) AuthLoginAttemptRepository {
	return &authLoginAttemptRepo{db: db}
}

// RegisterFailure counts one failed login inside a rolling window. The counter
// is shared by every cluster node because it lives in the database, so an
// attacker cannot multiply the budget by spreading requests across nodes.
func (r *authLoginAttemptRepo) RegisterFailure(ctx context.Context, bucketKey string, window time.Duration, maxAttempts int, block time.Duration) (bool, time.Duration, error) {
	if bucketKey == "" {
		return false, 0, errors.New("bucket key is required")
	}
	if maxAttempts <= 0 {
		maxAttempts = 1
	}
	now := time.Now().UTC()
	windowStart := tmFixed(now.Add(-window))
	res, err := r.db.ExecContext(ctx,
		`UPDATE auth_login_attempts SET attempts=CASE WHEN window_start<? THEN 1 ELSE attempts+1 END,window_start=CASE WHEN window_start<? THEN ? ELSE window_start END,blocked_until=NULL,updated_at=? WHERE bucket_key=?`,
		windowStart, windowStart, tmFixed(now), tmFixed(now), bucketKey)
	if err != nil {
		return false, 0, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if _, err := r.db.ExecContext(ctx, `INSERT INTO auth_login_attempts(bucket_key,attempts,window_start,blocked_until,updated_at) VALUES(?,1,?,NULL,?)`,
			bucketKey, tmFixed(now), tmFixed(now)); err != nil && !isDuplicateError(err) {
			return false, 0, err
		}
	}
	var attempts int
	if err := r.db.QueryRowContext(ctx, `SELECT attempts FROM auth_login_attempts WHERE bucket_key=?`, bucketKey).Scan(&attempts); err != nil {
		return false, 0, err
	}
	if attempts < maxAttempts {
		return false, 0, nil
	}
	if _, err := r.db.ExecContext(ctx, `UPDATE auth_login_attempts SET blocked_until=?,updated_at=? WHERE bucket_key=?`, tmFixed(now.Add(block)), tmFixed(now), bucketKey); err != nil {
		return false, 0, err
	}
	return true, block, nil
}

func (r *authLoginAttemptRepo) IsBlocked(ctx context.Context, bucketKey string) (bool, time.Duration, error) {
	var blocked sql.NullString
	err := r.db.QueryRowContext(ctx, `SELECT blocked_until FROM auth_login_attempts WHERE bucket_key=?`, bucketKey).Scan(&blocked)
	if errors.Is(err, sql.ErrNoRows) || !blocked.Valid || blocked.String == "" {
		return false, 0, nil
	}
	if err != nil {
		return false, 0, err
	}
	until := parseTime(blocked.String)
	now := time.Now().UTC()
	if !until.After(now) {
		return false, 0, nil
	}
	return true, until.Sub(now).Round(time.Second) + time.Second, nil
}

func (r *authLoginAttemptRepo) RegisterSuccess(ctx context.Context, bucketKey string) error {
	if bucketKey == "" {
		return nil
	}
	_, err := r.db.ExecContext(ctx, `DELETE FROM auth_login_attempts WHERE bucket_key=?`, bucketKey)
	return err
}

func (r *authLoginAttemptRepo) CountBlocked(ctx context.Context) (int, error) {
	var count int
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM auth_login_attempts WHERE blocked_until IS NOT NULL AND blocked_until>?`, tmFixed(time.Now().UTC())).Scan(&count)
	return count, err
}

func (r *authLoginAttemptRepo) DeleteExpired(ctx context.Context, olderThan time.Time) (int64, error) {
	res, err := r.db.ExecContext(ctx, `DELETE FROM auth_login_attempts WHERE updated_at<?`, tmFixed(olderThan))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// AdminCounter reports how many enabled, non-deleted administrator accounts
// exist. Identity operations use it to refuse a change that would lock out the
// last administrator, which is the one recovery path that must never break.
type AdminCounter interface {
	CountActiveAdmins(context.Context) (int, error)
}

func (r *userRepo) CountActiveAdmins(ctx context.Context) (int, error) {
	var count int
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE role='admin' AND disabled=0 AND deleted_at IS NULL`).Scan(&count)
	return count, err
}

// ExternalAccountCounter reports how many accounts depend on an identity
// provider for their only credential. Disabling or deleting the last provider
// would lock them out with no password path back in, so the provider service
// refuses that change.
type ExternalAccountCounter interface {
	CountExternalOnly(context.Context) (int, error)
}

func (r *userRepo) CountExternalOnly(ctx context.Context) (int, error) {
	var count int
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE auth_source=? AND password_hash=? AND disabled=0 AND deleted_at IS NULL`, string(AuthSourceOIDC), PasswordHashNone).Scan(&count)
	return count, err
}

// IdentityRepositories bundles every repository that participates in one login,
// enrollment, or provisioning decision. Binding them to a single transaction
// keeps the credential write, the replay guard, and the audit row consistent.
type IdentityRepositories struct {
	Users         UserRepository
	UserIdentity  UserIdentityWriter
	Admins        AdminCounter
	ExternalOnly  ExternalAccountCounter
	Tokens        TokenRepository
	Settings      AuthSettingsRepository
	Providers     OIDCProviderRepository
	Identities    UserIdentityRepository
	MFA           UserMFARepository
	RecoveryCodes UserRecoveryCodeRepository
	Devices       UserDeviceRepository
	Challenges    AuthChallengeRepository
	LoginAttempts AuthLoginAttemptRepository
	Audits        AuditRepository
	Idempotency   IdempotencyRepository
}

// identityRepositories binds every identity repository to one executor. It is
// shared by the mutating and read-only entry points so the two can never drift
// apart in which tables they see.
func (d *DB) identityRepositories(exec dbExecutor, lockReads bool) IdentityRepositories {
	return IdentityRepositories{
		Users:         &userRepo{db: exec, driver: d.driver, lockReads: lockReads},
		UserIdentity:  &userRepo{db: exec, driver: d.driver, lockReads: lockReads},
		Admins:        &userRepo{db: exec, driver: d.driver, lockReads: lockReads},
		ExternalOnly:  &userRepo{db: exec, driver: d.driver, lockReads: lockReads},
		Tokens:        &tokenRepo{exec},
		Settings:      NewAuthSettingsRepository(exec),
		Providers:     NewOIDCProviderRepository(exec),
		Identities:    NewUserIdentityRepository(exec),
		MFA:           NewUserMFARepository(exec),
		RecoveryCodes: NewUserRecoveryCodeRepository(exec),
		Devices:       NewUserDeviceRepository(exec),
		Challenges:    NewAuthChallengeRepository(exec),
		LoginAttempts: NewAuthLoginAttemptRepository(exec),
		Audits:        &auditRepo{exec},
		Idempotency:   &idempotencyRepo{exec},
	}
}

// IdentityRead runs read-only identity queries against the plain connection
// pool. Reads must not take the writer lock that IdentityTransaction acquires,
// or a busy login page would serialize against every other writer.
func (d *DB) IdentityRead(ctx context.Context, fn func(IdentityRepositories) error) error {
	return fn(d.identityRepositories(d.sql, false))
}

// IdentityTransaction commits an identity mutation and its audit record as one
// database fact. The schema_meta no-op update acquires the same portable writer
// lock the other mutation paths use.
func (d *DB) IdentityTransaction(ctx context.Context, fn func(IdentityRepositories) error) error {
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `UPDATE schema_meta SET version=version WHERE id=1`); err != nil {
		return err
	}
	repos := d.identityRepositories(tx, true)
	if err := fn(repos); err != nil {
		return err
	}
	return tx.Commit()
}
