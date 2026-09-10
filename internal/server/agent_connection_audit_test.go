package server

import (
	"context"
	"strings"
	"testing"

	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

func TestAgentConnectionAuditActionsAndDetails(t *testing.T) {
	db, err := storage.OpenSQLite(context.Background(), "file:agent-connection-audit?mode=memory&cache=shared", true)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	manager := NewAgentSessionManager(AgentSessionConfig{})
	registration := AgentRegistration{AgentID: "audit-agent", NodeID: "node-1", Epoch: 2, InstanceID: "instance-1", ConnectionID: "conn-1", ConnectionEpoch: 2, TokenID: "token-id"}
	actions := []string{agentConnectionRegisteredAudit, agentConnectionReplacedAudit, agentConnectionRejectedAudit, agentConnectionClosedAudit}
	for _, action := range actions {
		if err := writeAgentConnectionAudit(context.Background(), db.Audits(), "user-1", action, registration, nil); err != nil {
			t.Fatal(err)
		}
	}
	if action := agentConnectionAuditAction(manager, registration); action != agentConnectionRegisteredAudit {
		t.Fatalf("first action = %s", action)
	}
	if _, err := manager.Register(context.Background(), registration, newFakeTransport()); err != nil {
		t.Fatal(err)
	}
	if action := agentConnectionAuditAction(manager, registration); action != agentConnectionReplacedAudit {
		t.Fatalf("replacement action = %s", action)
	}

	page, err := db.Audits().List(context.Background(), storage.AuditFilter{}, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, item := range page.Items {
		found[item.Action] = true
		if !strings.Contains(item.Details, `"instanceId":"instance-1"`) || !strings.Contains(item.Details, `"connectionId":"conn-1"`) {
			t.Fatalf("audit details missing identities: %s", item.Details)
		}
		if strings.Contains(item.Details, "secret") || strings.Contains(item.Details, "Authorization") {
			t.Fatalf("audit details contain credentials: %s", item.Details)
		}
	}
	for _, action := range actions {
		if !found[action] {
			t.Fatalf("missing audit action %s", action)
		}
	}
}
