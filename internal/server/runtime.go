package server

import (
	"errors"

	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

var ErrRuntimeDatabaseRequired = errors.New("server runtime: database is required")

// ServerRuntime is the process-scoped server wiring shared by HTTP handlers
// and authenticated Agent WebSocket handlers. The caller owns DB lifecycle.
type ServerRuntime struct {
	DB            *storage.DB
	AgentSessions *AgentSessionManager
}

// NewServerRuntime creates the server runtime with durable Agent metadata
// persistence enabled. The CLI server run command and embedders should create
// one runtime at startup, then pass AgentSessions to ServeAgentSession.
func NewServerRuntime(db *storage.DB, cfg AgentSessionConfig) (*ServerRuntime, error) {
	if db == nil {
		return nil, ErrRuntimeDatabaseRequired
	}
	return &ServerRuntime{DB: db, AgentSessions: NewAgentSessionManagerWithMetadata(db.Metadata(), cfg)}, nil
}
