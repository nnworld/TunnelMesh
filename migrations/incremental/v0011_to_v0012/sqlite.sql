CREATE TABLE IF NOT EXISTS credentials (
    id VARBINARY(255) PRIMARY KEY,
    owner_user_id VARBINARY(255) NOT NULL,
    name VARCHAR(255) NOT NULL,
    credential_type VARCHAR(32) NOT NULL,
    public_key TEXT NOT NULL,
    fingerprint VARBINARY(255) NOT NULL,
    enabled INTEGER NOT NULL DEFAULT 1,
    deleted_at VARCHAR(32),
    created_at TEXT NOT NULL,
    updated_at VARCHAR(32) NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_credentials_owner ON credentials(owner_user_id, credential_type, id);
CREATE INDEX IF NOT EXISTS idx_credentials_deleted ON credentials(deleted_at, updated_at);

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
CREATE INDEX IF NOT EXISTS idx_remote_servers_owner ON remote_servers(owner_user_id, enabled, id);
CREATE INDEX IF NOT EXISTS idx_remote_servers_agent ON remote_servers(agent_id, enabled, id);
CREATE INDEX IF NOT EXISTS idx_remote_servers_deleted ON remote_servers(deleted_at, updated_at);

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
CREATE INDEX IF NOT EXISTS idx_webssh_sessions_owner ON webssh_sessions(owner_user_id, status, expires_at);
CREATE INDEX IF NOT EXISTS idx_webssh_sessions_node ON webssh_sessions(owner_node_id, status);
CREATE INDEX IF NOT EXISTS idx_webssh_sessions_ticket ON webssh_sessions(ticket_expires_at, status);
