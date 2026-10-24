-- v16 -> v17: indexes for cursor-paged management list sorting.
CREATE INDEX idx_agents_created ON agents(created_at(32), id);
CREATE INDEX idx_users_created ON users(created_at(32), id);
CREATE INDEX idx_service_tokens_created ON service_tokens(created_at(32), id);
CREATE INDEX idx_tunnels_created ON tunnels(created_at(32), id);
CREATE INDEX idx_server_nodes_created ON server_nodes(created_at(32), id);
CREATE INDEX idx_agent_policies_created ON agent_policies(created_at(32), id);
CREATE INDEX idx_oidc_providers_created ON oidc_providers(created_at, id);
CREATE INDEX idx_vpn_peers_created ON vpn_peers(created_at, id);
CREATE INDEX idx_credentials_updated ON credentials(updated_at(32), id);
CREATE INDEX idx_remote_servers_updated ON remote_servers(updated_at, id);
CREATE INDEX idx_client_metadata_last_seen ON client_instance_metadata(last_seen_at, id);
