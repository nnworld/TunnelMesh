package server

import (
	"context"
	"encoding/json"

	"github.com/tunnelmesh/tunnelmesh/internal/observability"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

const (
	agentConnectionRegisteredAudit = "agent.connection.registered"
	agentConnectionReplacedAudit   = "agent.connection.replaced"
	agentConnectionRejectedAudit   = "agent.connection.rejected"
	agentConnectionClosedAudit     = "agent.connection.closed"
)

func agentConnectionAuditAction(manager *AgentSessionManager, registration AgentRegistration) string {
	if manager.HasConnection(registration.AgentID, registration.ConnectionID) {
		return agentConnectionReplacedAudit
	}
	return agentConnectionRegisteredAudit
}

func writeAgentConnectionAudit(ctx context.Context, audits storage.AuditRepository, actorUserID, action string, registration AgentRegistration, failure error) error {
	if audits == nil {
		return nil
	}
	details := map[string]any{
		"instanceId":      registration.InstanceID,
		"connectionId":    registration.ConnectionID,
		"nodeId":          registration.NodeID,
		"epoch":           registration.Epoch,
		"connectionEpoch": registration.ConnectionEpoch,
		"tokenId":         registration.TokenID,
	}
	if failure != nil {
		details["errorClass"] = observability.NormalizeErrorClass(failure)
	}
	encoded, err := json.Marshal(details)
	if err != nil {
		return err
	}
	return audits.Create(ctx, storage.AuditLog{
		ActorUserID:  actorUserID,
		Action:       action,
		ResourceType: "agent",
		ResourceID:   registration.AgentID,
		Details:      string(encoded),
	})
}
