-- v14 -> v15: embedded VPN gateway persistence (peer records and IP leases).
-- Every statement is additive, so nothing written by a v14 binary becomes
-- unreadable and the migration can be applied while v14 is still serving.
-- That is not a rolling-upgrade window: internal/storage/db.go refuses to open
-- a database whose schema_meta.version is greater than the binary's
-- SchemaVersion, so a v14 process restarted against a v15 database fails fast.
-- Roll back by restoring the pre-upgrade backup, not by downgrading the binary
-- in place. A partially applied migration is retry-safe because
-- applySchemaStatements tolerates duplicate objects from v6 onward.
-- Indexed timestamps use VARCHAR(32) rather than TEXT so MySQL can build the
-- index without an explicit key length, matching the existing convention.
-- Column widths are budgeted against MySQL 5.6: with Antelope, COMPACT rows and
-- innodb_large_prefix=OFF an index key may not exceed 767 bytes and the inline
-- portion of a row may not exceed 8126 bytes. TestVPNMigrationsFitMySQL56Budgets
-- fails the build if any width below is widened without recomputing that budget.
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
