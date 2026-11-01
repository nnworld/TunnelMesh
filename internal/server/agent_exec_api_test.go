package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tunnelmesh/tunnelmesh/internal/auth"
)

// Only administrators may request a script run, checked before anything else so a
// non-admin cannot probe agent state through the endpoint.
func TestAgentExecEndpointRefusesNonAdministrators(t *testing.T) {
	api := &API{}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agents/a1/exec-sessions", nil)
	rec := httptest.NewRecorder()
	api.handleAgentExec(rec, req, auth.Principal{UserID: "u1", Role: "user"}, "a1")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-admin status = %d, want 403", rec.Code)
	}
}

func TestAgentExecEndpointReportsDisabledSwitch(t *testing.T) {
	api := &API{}
	api.SetAgentExec(nil, true)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agents/a1/exec-sessions", nil)
	rec := httptest.NewRecorder()
	api.handleAgentExec(rec, req, auth.Principal{UserID: "admin", Role: "admin"}, "a1")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 when no broker is attached", rec.Code)
	}
}

func TestAgentExecEndpointRequiresPOST(t *testing.T) {
	api := &API{}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/agents/a1/exec-sessions", nil)
	rec := httptest.NewRecorder()
	api.handleAgentExec(rec, req, auth.Principal{UserID: "admin", Role: "admin"}, "a1")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET status = %d, want 405", rec.Code)
	}
}
