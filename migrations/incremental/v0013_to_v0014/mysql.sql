-- v13 -> v14: enterprise identity foundation (SSO, MFA, device trust).
-- Every statement is additive, so nothing written by a v13 binary becomes
-- unreadable and the migration can be applied while v13 is still serving.
-- That is not a rolling-upgrade window: internal/storage/db.go refuses to open
-- a database whose schema_meta.version is greater than the binary's
-- SchemaVersion, so a v13 process restarted against a v14 database fails fast.
-- Roll back by restoring the pre-upgrade backup, not by downgrading the binary
-- in place. A partially applied migration is retry-safe because
-- applySchemaStatements tolerates duplicate objects from v6 onward.
-- Indexed timestamps use VARCHAR(32) rather than TEXT so MySQL can build the
-- index without an explicit key length, matching the existing convention.
ALTER TABLE users ADD COLUMN auth_source VARCHAR(32) NOT NULL DEFAULT 'local';
ALTER TABLE users ADD COLUMN mfa_required INTEGER NOT NULL DEFAULT 0;

CREATE TABLE IF NOT EXISTS auth_settings (
    id INTEGER PRIMARY KEY,
    mfa_mode VARCHAR(16) NOT NULL DEFAULT 'disabled',
    device_trust_enabled INTEGER NOT NULL DEFAULT 1,
    device_trust_ttl_seconds INTEGER NOT NULL DEFAULT 2592000,
    allow_trusted_device_bypass INTEGER NOT NULL DEFAULT 1,
    max_trusted_devices INTEGER NOT NULL DEFAULT 10,
    session_token_ttl_seconds INTEGER NOT NULL DEFAULT 0,
    updated_at VARCHAR(32) NOT NULL
);

CREATE TABLE IF NOT EXISTS oidc_providers (
    id VARBINARY(255) PRIMARY KEY,
    name VARCHAR(191) NOT NULL UNIQUE,
    display_name VARCHAR(191) NOT NULL,
    issuer VARCHAR(512) NOT NULL,
    client_id VARCHAR(255) NOT NULL,
    client_secret_ciphertext TEXT,
    client_secret_nonce TEXT,
    client_secret_key_id VARCHAR(128),
    client_secret_version INTEGER,
    scopes TEXT NOT NULL,
    redirect_uri VARCHAR(512) NOT NULL,
    authorization_endpoint VARCHAR(512),
    token_endpoint VARCHAR(512),
    userinfo_endpoint VARCHAR(512),
    jwks_uri VARCHAR(512),
    id_token_algs VARCHAR(255) NOT NULL DEFAULT 'RS256',
    username_claim VARCHAR(64) NOT NULL DEFAULT 'preferred_username',
    role_mappings TEXT NOT NULL,
    default_role VARCHAR(32) NOT NULL DEFAULT 'user',
    authoritative_roles INTEGER NOT NULL DEFAULT 1,
    auto_create_users INTEGER NOT NULL DEFAULT 1,
    fetch_userinfo INTEGER NOT NULL DEFAULT 0,
    public_listed INTEGER NOT NULL DEFAULT 1,
    enabled INTEGER NOT NULL DEFAULT 1,
    created_at VARCHAR(32) NOT NULL,
    updated_at VARCHAR(32) NOT NULL
);
CREATE INDEX idx_oidc_providers_enabled ON oidc_providers(enabled, public_listed, id);

CREATE TABLE IF NOT EXISTS user_identities (
    id VARBINARY(255) PRIMARY KEY,
    user_id VARBINARY(255) NOT NULL,
    provider_id VARBINARY(255) NOT NULL,
    subject VARBINARY(255) NOT NULL,
    email VARCHAR(255),
    display_name VARCHAR(255),
    created_at VARCHAR(32) NOT NULL,
    last_login_at VARCHAR(32),
    UNIQUE(provider_id, subject)
);
CREATE INDEX idx_user_identities_user ON user_identities(user_id, id);

CREATE TABLE IF NOT EXISTS user_mfa (
    user_id VARBINARY(255) PRIMARY KEY,
    secret_ciphertext TEXT NOT NULL,
    secret_nonce TEXT NOT NULL,
    secret_key_id VARCHAR(128) NOT NULL,
    secret_version INTEGER NOT NULL,
    status VARCHAR(16) NOT NULL DEFAULT 'pending',
    enrolled_at VARCHAR(32) NOT NULL,
    enabled_at VARCHAR(32),
    last_used_at VARCHAR(32),
    last_used_step INTEGER NOT NULL DEFAULT -1,
    updated_at VARCHAR(32) NOT NULL
);

CREATE TABLE IF NOT EXISTS user_recovery_codes (
    id VARBINARY(255) PRIMARY KEY,
    user_id VARBINARY(255) NOT NULL,
    code_hash VARBINARY(64) NOT NULL UNIQUE,
    used_at VARCHAR(32),
    created_at VARCHAR(32) NOT NULL
);
CREATE INDEX idx_user_recovery_codes_user ON user_recovery_codes(user_id, id);

CREATE TABLE IF NOT EXISTS user_devices (
    id VARBINARY(255) PRIMARY KEY,
    user_id VARBINARY(255) NOT NULL,
    token_hash VARBINARY(64) NOT NULL UNIQUE,
    name VARCHAR(191),
    user_agent VARCHAR(512),
    ip VARCHAR(64),
    trusted_at VARCHAR(32) NOT NULL,
    expires_at VARCHAR(32) NOT NULL,
    last_seen_at VARCHAR(32),
    revoked_at VARCHAR(32)
);
CREATE INDEX idx_user_devices_user ON user_devices(user_id, revoked_at, expires_at);
CREATE INDEX idx_user_devices_expires ON user_devices(expires_at);

CREATE TABLE IF NOT EXISTS auth_challenges (
    id VARBINARY(255) PRIMARY KEY,
    kind VARCHAR(32) NOT NULL,
    user_id VARBINARY(255),
    payload_ciphertext TEXT NOT NULL,
    payload_nonce TEXT NOT NULL,
    payload_key_id VARCHAR(128) NOT NULL,
    payload_version INTEGER NOT NULL,
    attempts INTEGER NOT NULL DEFAULT 0,
    max_attempts INTEGER NOT NULL,
    consumed_at VARCHAR(32),
    expires_at VARCHAR(32) NOT NULL,
    created_at VARCHAR(32) NOT NULL
);
CREATE INDEX idx_auth_challenges_expires ON auth_challenges(expires_at);
CREATE INDEX idx_auth_challenges_kind_user ON auth_challenges(kind, user_id, id);

CREATE TABLE IF NOT EXISTS auth_login_attempts (
    bucket_key VARBINARY(255) PRIMARY KEY,
    attempts INTEGER NOT NULL DEFAULT 0,
    window_start VARCHAR(32) NOT NULL,
    blocked_until VARCHAR(32),
    updated_at VARCHAR(32) NOT NULL
);
CREATE INDEX idx_auth_login_attempts_blocked ON auth_login_attempts(blocked_until);
