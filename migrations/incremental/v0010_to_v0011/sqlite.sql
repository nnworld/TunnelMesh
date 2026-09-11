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
CREATE INDEX IF NOT EXISTS idx_client_instance_metadata_owner ON client_instance_metadata(owner_user_id, stale, last_seen_at);
CREATE INDEX IF NOT EXISTS idx_client_instance_metadata_stale ON client_instance_metadata(stale, updated_at);

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
CREATE INDEX IF NOT EXISTS idx_client_connection_leases_instance ON client_connection_leases(client_instance_id);
CREATE INDEX IF NOT EXISTS idx_client_connection_leases_node ON client_connection_leases(server_node_id);
CREATE INDEX IF NOT EXISTS idx_client_connection_leases_owner ON client_connection_leases(owner_user_id);
CREATE INDEX IF NOT EXISTS idx_client_connection_leases_expires ON client_connection_leases(expires_at);
