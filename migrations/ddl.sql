CREATE TABLE IF NOT EXISTS schema_meta (
    id INTEGER PRIMARY KEY,
    version INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS users (
    id VARBINARY(255) PRIMARY KEY,
    username VARCHAR(191) NOT NULL UNIQUE,
    role VARCHAR(32) NOT NULL,
    password_hash VARCHAR(255) NOT NULL,
    disabled INTEGER NOT NULL DEFAULT 0,
    deleted_at VARCHAR(32),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
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
    address VARBINARY(255) NOT NULL,
    epoch INTEGER NOT NULL DEFAULT 0,
    metadata TEXT NOT NULL,
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
