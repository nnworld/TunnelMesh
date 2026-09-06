package server

import "github.com/tunnelmesh/tunnelmesh/internal/storage"

// NewAgentSessionManagerWithMetadata constructs an Agent session manager with
// runtime metadata persistence wired into every authenticated session. Server
// startup should call this once with storage.DB.Metadata() (or another
// AgentMetadataRepository) and pass the returned manager to ServeAgentSession.
// An explicitly supplied cfg.MetadataService takes precedence over repo.
func NewAgentSessionManagerWithMetadata(repo storage.AgentMetadataRepository, cfg AgentSessionConfig) *AgentSessionManager {
	if cfg.MetadataService == nil && repo != nil {
		cfg.MetadataService = NewAgentMetadataService(repo)
	}
	return NewAgentSessionManager(cfg)
}
