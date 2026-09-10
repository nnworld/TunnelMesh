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
CREATE INDEX IF NOT EXISTS idx_agent_connection_leases_node ON agent_connection_leases(node_id);
INSERT INTO agent_connection_leases(
    agent_id, connection_id, node_id, instance_id, server_node_id,
    epoch, connection_epoch, active_streams, health_score,
    acquired_at, expires_at, updated_at
)
SELECT
    agent_id, 'legacy', node_id, '', node_id,
    epoch, epoch, 0, 100,
    acquired_at, expires_at, updated_at
FROM agent_runtime_leases old
WHERE NOT EXISTS (
    SELECT 1 FROM agent_connection_leases new
    WHERE new.agent_id = old.agent_id AND new.connection_id = 'legacy'
);

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
    updated_at TEXT NOT NULL,
    PRIMARY KEY (agent_id, instance_id)
);
CREATE INDEX IF NOT EXISTS idx_agent_instance_metadata_node ON agent_instance_metadata(node_id);
CREATE INDEX IF NOT EXISTS idx_agent_instance_metadata_stale ON agent_instance_metadata(stale, updated_at);
INSERT INTO agent_instance_metadata(
    agent_id, instance_id, node_id, epoch, revision, metadata,
    reported_at, last_seen_at, expires_at, stale, updated_at
)
SELECT agent_id, 'legacy', node_id, epoch, revision, metadata,
    reported_at, last_seen_at, expires_at, stale, updated_at
FROM agent_runtime_metadata old
WHERE NOT EXISTS (
    SELECT 1 FROM agent_instance_metadata new
    WHERE new.agent_id = old.agent_id AND new.instance_id = 'legacy'
);
