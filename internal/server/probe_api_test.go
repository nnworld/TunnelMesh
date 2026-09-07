package server

import (
	"net/http"
	"testing"
)

func TestProbeAPIRequiresAgentOwnership(t *testing.T) {
	fixture := newTokenAPIFixture(t)
	response := apiJSON(t, fixture.api, http.MethodPost, "/api/v1/agents/agent-bob/diagnose", fixture.ownerToken, "", map[string]any{"kind": "tcp", "host": "127.0.0.1", "port": 80})
	if response.Code != http.StatusForbidden {
		t.Fatalf("probe status = %d, want 403: %s", response.Code, response.Body.String())
	}
}
