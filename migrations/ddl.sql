CREATE TABLE IF NOT EXISTS schema_meta (
    id INTEGER PRIMARY KEY,
    version INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS authorization_revision (
    id INTEGER PRIMARY KEY,
    revision BIGINT UNSIGNED NOT NULL,
    updated_at TEXT NOT NULL
);
INSERT INTO authorization_revision(id, revision, updated_at) VALUES (1, 1, '1970-01-01T00:00:00Z');

CREATE TABLE IF NOT EXISTS users (
    id VARBINARY(255) PRIMARY KEY,
    username VARCHAR(191) NOT NULL UNIQUE,
    role VARCHAR(32) NOT NULL,
    password_hash VARCHAR(255) NOT NULL,
    disabled INTEGER NOT NULL DEFAULT 0,
    deleted_at VARCHAR(32),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    -- auth_source records where the primary credential comes from. A stored
    -- password_hash of '*' marks an external-only account that has no local
    -- password, and Argon2id verification can never succeed against it.
    auth_source VARCHAR(32) NOT NULL DEFAULT 'local',
    mfa_required INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX idx_users_role_deleted ON users(role, deleted_at, id);

CREATE TABLE IF NOT EXISTS api_tokens (
    id VARBINARY(255) PRIMARY KEY,
    user_id VARBINARY(255) NOT NULL,
    token_hash VARBINARY(255) NOT NULL UNIQUE,
    idempotency_key VARBINARY(255),
    expires_at TEXT,
    revoked_at TEXT,
    created_at TEXT NOT NULL
);
CREATE INDEX idx_api_tokens_user_id ON api_tokens(user_id);

CREATE TABLE IF NOT EXISTS service_tokens (
    id VARBINARY(255) PRIMARY KEY,
    token_type VARCHAR(32) NOT NULL,
    owner_user_id VARBINARY(255),
    agent_id VARBINARY(255),
    node_id VARBINARY(255),
    token_prefix VARBINARY(32) NOT NULL,
    token_hash VARBINARY(255) NOT NULL UNIQUE,
    scope TEXT NOT NULL,
    expires_at TEXT,
    revoked_at TEXT,
    last_used_at TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE INDEX idx_service_tokens_owner_type ON service_tokens(owner_user_id, token_type, id);
CREATE INDEX idx_service_tokens_agent ON service_tokens(agent_id, token_type, id);
CREATE INDEX idx_service_tokens_node ON service_tokens(node_id, token_type, id);
-- Recoverable bearer secrets are encrypted before persistence. ALTER statements
-- are intentionally idempotent at the application layer for older databases.
ALTER TABLE service_tokens ADD COLUMN secret_ciphertext TEXT;
ALTER TABLE service_tokens ADD COLUMN secret_nonce TEXT;
ALTER TABLE service_tokens ADD COLUMN secret_key_id VARCHAR(128);
ALTER TABLE service_tokens ADD COLUMN secret_version INTEGER;
ALTER TABLE service_tokens ADD COLUMN secret_last_read_at TEXT;

-- Enterprise identity storage (schema v14): OIDC providers, linked external
-- identities, TOTP enrollment, recovery codes, trusted devices, short-lived
-- login challenges, and the cluster-safe login attempt counter.
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

CREATE TABLE IF NOT EXISTS agents (
    id VARBINARY(255) PRIMARY KEY,
    name VARCHAR(255) NOT NULL,
    owner_user_id VARBINARY(255) NOT NULL,
    capabilities TEXT NOT NULL,
    enabled INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE INDEX idx_agents_owner ON agents(owner_user_id);

CREATE TABLE IF NOT EXISTS agent_policies (
    id VARBINARY(255) PRIMARY KEY,
    agent_id VARBINARY(255) NOT NULL,
    target_host VARCHAR(255) NOT NULL,
    target_port INTEGER NOT NULL,
    protocol VARCHAR(32) NOT NULL,
    allowed_cidrs TEXT NOT NULL,
    allowed_ports TEXT NOT NULL,
    deleted_at TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE INDEX idx_agent_policies_agent ON agent_policies(agent_id);

CREATE TABLE IF NOT EXISTS tunnel_groups (
    id VARBINARY(255) PRIMARY KEY,
    user_id VARBINARY(255) NOT NULL,
    name VARCHAR(255) NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS tunnels (
    id VARBINARY(255) PRIMARY KEY,
    group_id VARBINARY(255),
    agent_id VARBINARY(255) NOT NULL,
    protocol VARCHAR(32) NOT NULL,
    domain VARBINARY(255),
    path_prefix VARBINARY(255),
    target_host VARCHAR(255) NOT NULL,
    target_port INTEGER NOT NULL,
    public_port INTEGER,
    status VARCHAR(32) NOT NULL DEFAULT 'active',
    config TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE(domain, path_prefix)
);
CREATE INDEX idx_tunnels_agent ON tunnels(agent_id);
CREATE INDEX idx_tunnels_domain_path ON tunnels(domain, path_prefix);

CREATE TABLE IF NOT EXISTS server_nodes (
    id VARBINARY(255) PRIMARY KEY,
    name VARBINARY(255) NOT NULL,
    address VARBINARY(255) NOT NULL,
    epoch INTEGER NOT NULL DEFAULT 0,
    metadata TEXT NOT NULL,
    enabled INTEGER NOT NULL DEFAULT 1,
    deleted_at TEXT,
    last_seen_at TEXT,
    expires_at TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS agent_connection_leases (
    agent_id VARBINARY(255) NOT NULL,
    connection_id VARBINARY(128) NOT NULL,
    node_id VARBINARY(255) NOT NULL,
    instance_id VARBINARY(128) NOT NULL,
    server_node_id VARBINARY(255) NOT NULL,
    epoch INTEGER NOT NULL,
    connection_epoch INTEGER NOT NULL,
    active_streams INTEGER NOT NULL DEFAULT 0,
    health_score INTEGER NOT NULL DEFAULT 100,
    acquired_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (agent_id, connection_id)
);
CREATE INDEX idx_agent_connection_leases_node ON agent_connection_leases(node_id);

CREATE TABLE IF NOT EXISTS agent_instance_metadata (
    agent_id VARBINARY(255) NOT NULL,
    instance_id VARBINARY(128) NOT NULL,
    node_id VARBINARY(255) NOT NULL,
    epoch INTEGER NOT NULL,
    revision INTEGER NOT NULL,
    metadata TEXT NOT NULL,
    reported_at TEXT NOT NULL,
    last_seen_at TEXT NOT NULL,
    expires_at TEXT,
    stale INTEGER NOT NULL DEFAULT 0,
    updated_at VARCHAR(32) NOT NULL,
    PRIMARY KEY (agent_id, instance_id)
);
CREATE INDEX idx_agent_instance_metadata_node ON agent_instance_metadata(node_id);
CREATE INDEX idx_agent_instance_metadata_stale ON agent_instance_metadata(stale, updated_at);

CREATE TABLE IF NOT EXISTS client_instance_metadata (
    id VARBINARY(255) PRIMARY KEY,
    owner_user_id VARBINARY(255) NOT NULL,
    instance_id VARBINARY(128) NOT NULL,
    metadata TEXT NOT NULL,
    capabilities TEXT NOT NULL,
    reported_at TEXT NOT NULL,
    last_seen_at VARCHAR(32) NOT NULL,
    expires_at TEXT,
    stale INTEGER NOT NULL DEFAULT 0,
    updated_at VARCHAR(32) NOT NULL,
    UNIQUE(owner_user_id, instance_id)
);
CREATE INDEX idx_client_instance_metadata_owner ON client_instance_metadata(owner_user_id, stale, last_seen_at);
CREATE INDEX idx_client_instance_metadata_stale ON client_instance_metadata(stale, updated_at);

CREATE TABLE IF NOT EXISTS client_connection_leases (
    connection_id VARBINARY(128) PRIMARY KEY,
    client_instance_id VARBINARY(255) NOT NULL,
    token_id VARBINARY(255) NOT NULL,
    owner_user_id VARBINARY(255) NOT NULL,
    server_node_id VARBINARY(255) NOT NULL,
    connection_epoch INTEGER NOT NULL,
    active_streams INTEGER NOT NULL DEFAULT 0,
    health_score INTEGER NOT NULL DEFAULT 100,
    acquired_at TEXT NOT NULL,
    expires_at VARCHAR(32) NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE INDEX idx_client_connection_leases_instance ON client_connection_leases(client_instance_id);
CREATE INDEX idx_client_connection_leases_node ON client_connection_leases(server_node_id);
CREATE INDEX idx_client_connection_leases_owner ON client_connection_leases(owner_user_id);
CREATE INDEX idx_client_connection_leases_expires ON client_connection_leases(expires_at);

CREATE TABLE IF NOT EXISTS credentials (
    id VARBINARY(255) PRIMARY KEY,
    owner_user_id VARBINARY(255) NOT NULL,
    name VARCHAR(255) NOT NULL,
    credential_type VARCHAR(32) NOT NULL,
    public_key TEXT NOT NULL,
    fingerprint VARBINARY(255) NOT NULL,
    secret_ciphertext TEXT,
    secret_nonce TEXT,
    secret_key_id VARCHAR(64),
    secret_version INTEGER,
    enabled INTEGER NOT NULL DEFAULT 1,
    deleted_at VARCHAR(32),
    created_at TEXT NOT NULL,
    updated_at VARCHAR(32) NOT NULL
);
CREATE INDEX idx_credentials_owner ON credentials(owner_user_id, credential_type, id);
CREATE INDEX idx_credentials_deleted ON credentials(deleted_at, updated_at);

CREATE TABLE IF NOT EXISTS remote_servers (
    id VARBINARY(255) PRIMARY KEY,
    owner_user_id VARBINARY(255) NOT NULL,
    name VARCHAR(255) NOT NULL,
    host VARCHAR(255) NOT NULL,
    port INTEGER NOT NULL,
    default_username VARCHAR(255) NOT NULL,
    credential_id VARBINARY(255),
    agent_id VARBINARY(255) NOT NULL,
    enabled INTEGER NOT NULL DEFAULT 1,
    deleted_at VARCHAR(32),
    last_connected_at TEXT,
    last_result VARCHAR(32),
    last_error_class VARCHAR(64),
    created_at TEXT NOT NULL,
    updated_at VARCHAR(32) NOT NULL
);
CREATE INDEX idx_remote_servers_owner ON remote_servers(owner_user_id, enabled, id);
CREATE INDEX idx_remote_servers_agent ON remote_servers(agent_id, enabled, id);
CREATE INDEX idx_remote_servers_deleted ON remote_servers(deleted_at, updated_at);

CREATE TABLE IF NOT EXISTS webssh_sessions (
    id VARBINARY(255) PRIMARY KEY,
    owner_user_id VARBINARY(255) NOT NULL,
    remote_server_id VARBINARY(255) NOT NULL,
    agent_id VARBINARY(255) NOT NULL,
    owner_node_id VARBINARY(255) NOT NULL,
    ticket_hash VARBINARY(255) NOT NULL,
    ticket_expires_at VARCHAR(32) NOT NULL,
    status VARCHAR(32) NOT NULL,
    created_at TEXT NOT NULL,
    expires_at VARCHAR(32) NOT NULL,
    connected_at TEXT,
    closed_at TEXT,
    close_reason VARCHAR(255)
);
CREATE INDEX idx_webssh_sessions_owner ON webssh_sessions(owner_user_id, status, expires_at);
CREATE INDEX idx_webssh_sessions_node ON webssh_sessions(owner_node_id, status);
CREATE INDEX idx_webssh_sessions_ticket ON webssh_sessions(ticket_expires_at, status);

CREATE TABLE IF NOT EXISTS vpn_peers (
    id VARBINARY(255) PRIMARY KEY,
    name VARCHAR(255) NOT NULL,
    owner_id VARBINARY(255) NOT NULL,
    -- public_key is the fixed 44-character base64 WireGuard key, so VARCHAR(64)
    -- is generous. The width is also a MySQL 5.6 budget decision: this column is
    -- UNIQUE-indexed, and COMPACT row format keeps up to 768 bytes of every
    -- variable-length column inline (see docs/operations/schema-upgrades.md).
    public_key VARCHAR(64) NOT NULL UNIQUE,
    private_key_ciphertext TEXT,
    -- private_key_nonce is VARCHAR(64) rather than TEXT on purpose. An AES-GCM
    -- nonce is 12 bytes (16 base64 characters), and TEXT would cost 788 bytes of
    -- the 8126-byte MySQL 5.6 COMPACT inline row budget instead of 256. Do not
    -- "normalize" it back to TEXT without recomputing that budget.
    private_key_nonce VARCHAR(64),
    private_key_key_id VARCHAR(64),
    private_key_version INTEGER,
    vpn_ip VARCHAR(45) NOT NULL,
    node_id VARBINARY(255) NOT NULL,
    agent_id VARBINARY(255) NOT NULL,
    allowed_ips TEXT NOT NULL,
    allowed_ports TEXT NOT NULL,
    allow_private_targets INTEGER NOT NULL DEFAULT 0,
    icmp_enabled INTEGER NOT NULL DEFAULT 0,
    max_concurrent_flows INTEGER NOT NULL DEFAULT 128,
    packet_rate_limit INTEGER NOT NULL DEFAULT 0,
    expires_at VARCHAR(32),
    status VARCHAR(32) NOT NULL,
    description VARCHAR(255),
    created_at VARCHAR(32) NOT NULL,
    updated_at VARCHAR(32) NOT NULL,
    UNIQUE(node_id, vpn_ip)
);
CREATE INDEX idx_vpn_peers_owner ON vpn_peers(owner_id, id);
CREATE INDEX idx_vpn_peers_node ON vpn_peers(node_id, status);
CREATE INDEX idx_vpn_peers_expires ON vpn_peers(expires_at);

CREATE TABLE IF NOT EXISTS vpn_ip_leases (
    id VARBINARY(255) PRIMARY KEY,
    node_id VARBINARY(255) NOT NULL,
    subnet VARCHAR(64) NOT NULL,
    allocated_count INTEGER NOT NULL DEFAULT 0,
    lease_holder VARBINARY(255) NOT NULL,
    lease_expires_at VARCHAR(32) NOT NULL,
    epoch INTEGER NOT NULL DEFAULT 0,
    acquired_at VARCHAR(32) NOT NULL,
    updated_at VARCHAR(32) NOT NULL,
    UNIQUE(node_id, subnet)
);
CREATE INDEX idx_vpn_ip_leases_holder ON vpn_ip_leases(lease_holder, lease_expires_at);
CREATE INDEX idx_vpn_ip_leases_expires ON vpn_ip_leases(lease_expires_at);

CREATE TABLE IF NOT EXISTS agent_runtime_stats (
    id VARBINARY(255) PRIMARY KEY,
    agent_id VARBINARY(255) NOT NULL,
    node_id VARBINARY(255) NOT NULL,
    epoch INTEGER NOT NULL,
    window_start VARCHAR(32) NOT NULL,
    window_end VARCHAR(32) NOT NULL,
    connections INTEGER NOT NULL DEFAULT 0,
    active_streams INTEGER NOT NULL DEFAULT 0,
    bytes_in INTEGER NOT NULL DEFAULT 0,
    bytes_out INTEGER NOT NULL DEFAULT 0,
    heartbeat_total INTEGER NOT NULL DEFAULT 0,
    heartbeat_success INTEGER NOT NULL DEFAULT 0,
    heartbeat_rtt_p50_us INTEGER NOT NULL DEFAULT 0,
    heartbeat_rtt_p95_us INTEGER NOT NULL DEFAULT 0,
    reconnects INTEGER NOT NULL DEFAULT 0,
    stream_errors INTEGER NOT NULL DEFAULT 0,
    created_at VARCHAR(32) NOT NULL
);
CREATE INDEX idx_agent_runtime_stats_range ON agent_runtime_stats(agent_id, window_start, id);

CREATE TABLE IF NOT EXISTS agent_probe_results (
    probe_id VARBINARY(255) PRIMARY KEY,
    agent_id VARBINARY(255) NOT NULL,
    node_id VARBINARY(255) NOT NULL,
    epoch INTEGER NOT NULL,
    kind VARCHAR(32) NOT NULL,
    result VARCHAR(32) NOT NULL,
    error_class VARCHAR(64) NOT NULL,
    duration_us INTEGER NOT NULL DEFAULT 0,
    observed_at VARCHAR(32) NOT NULL
);
CREATE INDEX idx_agent_probe_results_range ON agent_probe_results(agent_id, observed_at, probe_id);

CREATE TABLE IF NOT EXISTS audit_logs (
    id VARBINARY(255) PRIMARY KEY,
    actor_user_id VARBINARY(255),
    action VARCHAR(255) NOT NULL,
    resource_type VARCHAR(255) NOT NULL,
    resource_id VARBINARY(255),
    details TEXT NOT NULL,
    created_at VARCHAR(32) NOT NULL
);
CREATE INDEX idx_audit_created ON audit_logs(created_at, id);

CREATE TABLE IF NOT EXISTS idempotency_keys (
    idempotency_key VARBINARY(255) PRIMARY KEY,
    user_id VARBINARY(255),
    response TEXT NOT NULL,
    status_code INTEGER NOT NULL DEFAULT 200,
    created_at TEXT NOT NULL,
    expires_at TEXT
);
