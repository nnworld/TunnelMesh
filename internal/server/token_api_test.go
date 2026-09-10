package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/auth"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

type tokenAPIFixture struct {
	api        *API
	admin      storage.User
	owner      storage.User
	other      storage.User
	adminToken string
	ownerToken string
	otherToken string
}

func newTokenAPIFixture(t *testing.T) tokenAPIFixture {
	t.Helper()
	api, admin, owner := apiTestServer(t)
	authn := auth.NewAuthService(api.DB)
	other, err := authn.CreateUser(context.Background(), "bob", "bob-pass", "user")
	if err != nil {
		t.Fatal(err)
	}
	for _, agent := range []storage.Agent{
		{ID: "agent-alice-a", Name: "alice-a", OwnerUserID: owner.ID, Enabled: true},
		{ID: "agent-alice-b", Name: "alice-b", OwnerUserID: owner.ID, Enabled: true},
		{ID: "agent-alice-disabled", Name: "alice-disabled", OwnerUserID: owner.ID, Enabled: false},
		{ID: "agent-bob", Name: "bob", OwnerUserID: other.ID, Enabled: true},
	} {
		if err := api.DB.Agents().Create(context.Background(), agent); err != nil {
			t.Fatalf("create agent %s: %v", agent.ID, err)
		}
	}
	if err := api.DB.Nodes().Create(context.Background(), storage.ServerNode{ID: "node-a", Address: "127.0.0.1:9443", Epoch: 1}); err != nil {
		t.Fatal(err)
	}
	return tokenAPIFixture{
		api: api, admin: admin, owner: owner, other: other,
		adminToken: apiToken(t, api, admin.Username, "admin-pass"),
		ownerToken: apiToken(t, api, owner.Username, "alice-pass"),
		otherToken: apiToken(t, api, other.Username, "bob-pass"),
	}
}

func tokenResponseData(t *testing.T, responseBody []byte) map[string]any {
	t.Helper()
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(responseBody, &envelope); err != nil {
		t.Fatalf("decode response %s: %v", responseBody, err)
	}
	return envelope.Data
}

func tokenIDFromResponse(t *testing.T, responseBody []byte) string {
	t.Helper()
	id, _ := tokenResponseData(t, responseBody)["id"].(string)
	if id == "" {
		t.Fatalf("response has no token id: %s", responseBody)
	}
	return id
}

func withoutMutationOnlyFields(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for key, value := range in {
		if key != "secret" && key != "replayed" {
			out[key] = value
		}
	}
	return out
}

func assertNoSensitiveFields(t *testing.T, value any) {
	t.Helper()
	var walk func(any)
	walk = func(current any) {
		switch typed := current.(type) {
		case map[string]any:
			for key, child := range typed {
				lower := strings.ToLower(key)
				if lower == "secret" || lower == "hash" || strings.Contains(lower, "tokenhash") || strings.Contains(lower, "token_hash") {
					t.Fatalf("sensitive field %q present in %+v", key, value)
				}
				walk(child)
			}
		case []any:
			for _, child := range typed {
				walk(child)
			}
		}
	}
	walk(value)
}

func TestTokenAPIRequiresManagementAuthentication(t *testing.T) {
	fixture := newTokenAPIFixture(t)
	response := apiJSON(t, fixture.api, http.MethodGet, "/api/v1/tokens", "", "", nil)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated token list status = %d, want 401: %s", response.Code, response.Body.String())
	}
}

func TestTokenAPIAdminCanRevealEncryptedSecretWithConfirmation(t *testing.T) {
	fixture := newTokenAPIFixture(t)
	key := make([]byte, 32)
	store, err := auth.NewSecretStore(base64.RawStdEncoding.EncodeToString(key), "test-key")
	if err != nil {
		t.Fatal(err)
	}
	fixture.api.tokenService.SetSecretStore(store)
	create := apiJSON(t, fixture.api, http.MethodPost, "/api/v1/tokens", fixture.adminToken, "reveal-create", map[string]any{
		"type": "client", "ownerUserId": fixture.admin.ID,
	})
	if create.Code != http.StatusCreated {
		t.Fatalf("create status = %d: %s", create.Code, create.Body.String())
	}
	id := tokenIDFromResponse(t, create.Body.Bytes())
	reveal := apiJSONWithHeaders(t, fixture.api, http.MethodPost, "/api/v1/tokens/"+id+"/reveal", fixture.adminToken, map[string]string{
		"X-Token-Reveal-Confirm": "confirm-1", "Idempotency-Key": "reveal-1",
	}, map[string]any{"reason": "incident", "acknowledgeRisk": true})
	if reveal.Code != http.StatusOK {
		t.Fatalf("reveal status = %d: %s", reveal.Code, reveal.Body.String())
	}
	if reveal.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("cache-control = %q", reveal.Header().Get("Cache-Control"))
	}
	if secret, _ := tokenResponseData(t, reveal.Body.Bytes())["secret"].(string); secret == "" {
		t.Fatalf("reveal response has no secret: %s", reveal.Body.String())
	}
}

func TestTokenAPIUpdatesExpirationForActiveTokensOnly(t *testing.T) {
	fixture := newTokenAPIFixture(t)
	create := apiJSON(t, fixture.api, http.MethodPost, "/api/v1/tokens", fixture.ownerToken, "expiration-create", map[string]any{
		"type": "client", "scope": map[string]any{"agentIds": []string{"agent-alice-a"}},
	})
	if create.Code != http.StatusCreated {
		t.Fatalf("create status = %d: %s", create.Code, create.Body.String())
	}
	tokenID := tokenIDFromResponse(t, create.Body.Bytes())

	future := time.Now().UTC().Add(48 * time.Hour).Format(time.RFC3339Nano)
	update := apiJSON(t, fixture.api, http.MethodPatch, "/api/v1/tokens/"+tokenID, fixture.ownerToken, "", map[string]any{"expiresAt": future})
	if update.Code != http.StatusOK {
		t.Fatalf("update status = %d: %s", update.Code, update.Body.String())
	}
	if data := tokenResponseData(t, update.Body.Bytes()); data["id"] != tokenID || data["status"] != "active" {
		t.Fatalf("unexpected update response: %+v", data)
	}
	assertNoSensitiveFields(t, tokenResponseData(t, update.Body.Bytes()))

	clear := apiJSON(t, fixture.api, http.MethodPatch, "/api/v1/tokens/"+tokenID, fixture.ownerToken, "", map[string]any{"expiresAt": nil})
	if clear.Code != http.StatusOK {
		t.Fatalf("clear status = %d: %s", clear.Code, clear.Body.String())
	}
	if _, present := tokenResponseData(t, clear.Body.Bytes())["expiresAt"]; present {
		t.Fatalf("cleared expiration should be omitted: %s", clear.Body.String())
	}

	past := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)
	invalid := apiJSON(t, fixture.api, http.MethodPatch, "/api/v1/tokens/"+tokenID, fixture.ownerToken, "", map[string]any{"expiresAt": past})
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("past expiration status = %d, want 400: %s", invalid.Code, invalid.Body.String())
	}
	missing := apiJSON(t, fixture.api, http.MethodPatch, "/api/v1/tokens/"+tokenID, fixture.ownerToken, "", map[string]any{})
	if missing.Code != http.StatusBadRequest {
		t.Fatalf("missing expiration status = %d, want 400: %s", missing.Code, missing.Body.String())
	}

	foreign := apiJSON(t, fixture.api, http.MethodPatch, "/api/v1/tokens/"+tokenID, fixture.otherToken, "", map[string]any{"expiresAt": future})
	if foreign.Code != http.StatusForbidden {
		t.Fatalf("foreign update status = %d, want 403: %s", foreign.Code, foreign.Body.String())
	}

	expired := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano)
	if _, err := fixture.api.DB.SQL().ExecContext(context.Background(), `UPDATE service_tokens SET expires_at=? WHERE id=?`, expired, tokenID); err != nil {
		t.Fatal(err)
	}
	expiredUpdate := apiJSON(t, fixture.api, http.MethodPatch, "/api/v1/tokens/"+tokenID, fixture.ownerToken, "", map[string]any{"expiresAt": future})
	if expiredUpdate.Code != http.StatusConflict {
		t.Fatalf("expired update status = %d, want 409: %s", expiredUpdate.Code, expiredUpdate.Body.String())
	}

	if err := fixture.api.DB.ServiceTokens().Revoke(context.Background(), tokenID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	revokedUpdate := apiJSON(t, fixture.api, http.MethodPatch, "/api/v1/tokens/"+tokenID, fixture.ownerToken, "", map[string]any{"expiresAt": future})
	if revokedUpdate.Code != http.StatusConflict {
		t.Fatalf("revoked update status = %d, want 409: %s", revokedUpdate.Code, revokedUpdate.Body.String())
	}
	if countTokenLifecycleAudits(t, fixture.api.DB, tokenID, "token.expiration_updated") != 2 {
		t.Fatalf("expected two expiration update audits for token %s", tokenID)
	}
}

func TestTokenAPIUpdatesScopeForActiveTokensOnly(t *testing.T) {
	fixture := newTokenAPIFixture(t)
	create := apiJSON(t, fixture.api, http.MethodPost, "/api/v1/tokens", fixture.ownerToken, "scope-create", map[string]any{
		"type":  "client",
		"scope": map[string]any{"agentIds": []string{"agent-alice-a"}, "protocols": []string{"tcp"}, "targetCIDRs": []string{"10.0.0.0/8"}, "targetPorts": []int{22}},
	})
	if create.Code != http.StatusCreated {
		t.Fatalf("create status = %d: %s", create.Code, create.Body.String())
	}
	tokenID := tokenIDFromResponse(t, create.Body.Bytes())

	update := apiJSON(t, fixture.api, http.MethodPatch, "/api/v1/tokens/"+tokenID, fixture.ownerToken, "", map[string]any{
		"scope": map[string]any{"agentIds": []string{"agent-alice-b"}, "protocols": []string{"websocket"}, "targetPorts": []int{22, 80}},
	})
	if update.Code != http.StatusOK {
		t.Fatalf("scope update status = %d: %s", update.Code, update.Body.String())
	}
	scope, _ := tokenResponseData(t, update.Body.Bytes())["scope"].(map[string]any)
	if !reflect.DeepEqual(scope["agentIds"], []any{"agent-alice-b"}) {
		t.Fatalf("updated agentIds = %#v, want [agent-alice-b]", scope["agentIds"])
	}
	if !reflect.DeepEqual(scope["protocols"], []any{"ws"}) {
		t.Fatalf("updated protocols = %#v, want [ws]", scope["protocols"])
	}
	if !reflect.DeepEqual(scope["targetCIDRs"], []any{"10.0.0.0/8"}) {
		t.Fatalf("omitted CIDRs should be preserved: %#v", scope["targetCIDRs"])
	}
	if !reflect.DeepEqual(scope["targetPorts"], []any{float64(22), float64(80)}) {
		t.Fatalf("updated ports = %#v, want [22 80]", scope["targetPorts"])
	}
	assertNoSensitiveFields(t, tokenResponseData(t, update.Body.Bytes()))

	clear := apiJSON(t, fixture.api, http.MethodPatch, "/api/v1/tokens/"+tokenID, fixture.ownerToken, "", map[string]any{
		"scope": map[string]any{"agentIds": []string{}, "protocols": []string{}, "targetCIDRs": []string{}, "targetPorts": []int{}},
	})
	if clear.Code != http.StatusOK {
		t.Fatalf("clear scope status = %d: %s", clear.Code, clear.Body.String())
	}
	scope, _ = tokenResponseData(t, clear.Body.Bytes())["scope"].(map[string]any)
	if (scope["protocols"] != nil && len(scope["protocols"].([]any)) != 0) ||
		(scope["agentIds"] != nil && len(scope["agentIds"].([]any)) != 0) ||
		(scope["targetCIDRs"] != nil && len(scope["targetCIDRs"].([]any)) != 0) ||
		(scope["targetPorts"] != nil && len(scope["targetPorts"].([]any)) != 0) {
		t.Fatalf("empty arrays should remove scope restrictions: %#v", scope)
	}

	invalid := apiJSON(t, fixture.api, http.MethodPatch, "/api/v1/tokens/"+tokenID, fixture.ownerToken, "", map[string]any{
		"scope": map[string]any{"protocols": []string{"icmp"}, "targetCIDRs": []string{"not-a-cidr"}, "targetPorts": []int{0}},
	})
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid scope status = %d, want 400: %s", invalid.Code, invalid.Body.String())
	}
	foreignAgent := apiJSON(t, fixture.api, http.MethodPatch, "/api/v1/tokens/"+tokenID, fixture.ownerToken, "", map[string]any{
		"scope": map[string]any{"agentIds": []string{"agent-bob"}},
	})
	if foreignAgent.Code != http.StatusBadRequest {
		t.Fatalf("foreign agent scope status = %d, want 400: %s", foreignAgent.Code, foreignAgent.Body.String())
	}
	disabledAgent := apiJSON(t, fixture.api, http.MethodPatch, "/api/v1/tokens/"+tokenID, fixture.ownerToken, "", map[string]any{
		"scope": map[string]any{"agentIds": []string{"agent-alice-disabled"}},
	})
	if disabledAgent.Code != http.StatusBadRequest {
		t.Fatalf("disabled agent scope status = %d, want 400: %s", disabledAgent.Code, disabledAgent.Body.String())
	}
	crossType := apiJSON(t, fixture.api, http.MethodPatch, "/api/v1/tokens/"+tokenID, fixture.ownerToken, "", map[string]any{
		"scope": map[string]any{"serverNodeIds": []string{"node-a"}},
	})
	if crossType.Code != http.StatusBadRequest {
		t.Fatalf("client server-node scope status = %d, want 400: %s", crossType.Code, crossType.Body.String())
	}
	missing := apiJSON(t, fixture.api, http.MethodPatch, "/api/v1/tokens/"+tokenID, fixture.ownerToken, "", map[string]any{})
	if missing.Code != http.StatusBadRequest {
		t.Fatalf("missing update status = %d, want 400: %s", missing.Code, missing.Body.String())
	}

	foreign := apiJSON(t, fixture.api, http.MethodPatch, "/api/v1/tokens/"+tokenID, fixture.otherToken, "", map[string]any{
		"scope": map[string]any{"protocols": []string{"tcp"}},
	})
	if foreign.Code != http.StatusForbidden {
		t.Fatalf("foreign scope update status = %d, want 403: %s", foreign.Code, foreign.Body.String())
	}

	expired := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano)
	if _, err := fixture.api.DB.SQL().ExecContext(context.Background(), `UPDATE service_tokens SET expires_at=? WHERE id=?`, expired, tokenID); err != nil {
		t.Fatal(err)
	}
	expiredUpdate := apiJSON(t, fixture.api, http.MethodPatch, "/api/v1/tokens/"+tokenID, fixture.ownerToken, "", map[string]any{
		"scope": map[string]any{"protocols": []string{"tcp"}},
	})
	if expiredUpdate.Code != http.StatusConflict {
		t.Fatalf("expired scope update status = %d, want 409: %s", expiredUpdate.Code, expiredUpdate.Body.String())
	}

	if err := fixture.api.DB.ServiceTokens().Revoke(context.Background(), tokenID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	revokedUpdate := apiJSON(t, fixture.api, http.MethodPatch, "/api/v1/tokens/"+tokenID, fixture.ownerToken, "", map[string]any{
		"scope": map[string]any{"protocols": []string{"tcp"}},
	})
	if revokedUpdate.Code != http.StatusConflict {
		t.Fatalf("revoked scope update status = %d, want 409: %s", revokedUpdate.Code, revokedUpdate.Body.String())
	}
	if got := countTokenLifecycleAudits(t, fixture.api.DB, tokenID, "token.scope_updated"); got != 2 {
		t.Fatalf("scope update audit count = %d, want 2", got)
	}
}

func TestTokenAPIUpdatesServerNodeScope(t *testing.T) {
	fixture := newTokenAPIFixture(t)
	if err := fixture.api.DB.Nodes().Create(context.Background(), storage.ServerNode{ID: "node-b", Address: "127.0.0.1:9444", Epoch: 1}); err != nil {
		t.Fatal(err)
	}
	create := apiJSON(t, fixture.api, http.MethodPost, "/api/v1/tokens", fixture.adminToken, "server-scope-create", map[string]any{
		"type": "server_node", "scope": map[string]any{"serverNodeIds": []string{"node-a"}},
	})
	if create.Code != http.StatusCreated {
		t.Fatalf("create status = %d: %s", create.Code, create.Body.String())
	}
	tokenID := tokenIDFromResponse(t, create.Body.Bytes())

	update := apiJSON(t, fixture.api, http.MethodPatch, "/api/v1/tokens/"+tokenID, fixture.adminToken, "", map[string]any{
		"scope": map[string]any{"serverNodeIds": []string{"node-b"}},
	})
	if update.Code != http.StatusOK {
		t.Fatalf("server scope update status = %d: %s", update.Code, update.Body.String())
	}
	scope, _ := tokenResponseData(t, update.Body.Bytes())["scope"].(map[string]any)
	if !reflect.DeepEqual(scope["serverNodeIds"], []any{"node-b"}) {
		t.Fatalf("updated serverNodeIds = %#v, want [node-b]", scope["serverNodeIds"])
	}

	clear := apiJSON(t, fixture.api, http.MethodPatch, "/api/v1/tokens/"+tokenID, fixture.adminToken, "", map[string]any{
		"scope": map[string]any{"serverNodeIds": []string{}},
	})
	if clear.Code != http.StatusOK {
		t.Fatalf("clear server scope status = %d: %s", clear.Code, clear.Body.String())
	}
	scope, _ = tokenResponseData(t, clear.Body.Bytes())["scope"].(map[string]any)
	if scope["serverNodeIds"] != nil && len(scope["serverNodeIds"].([]any)) != 0 {
		t.Fatalf("empty serverNodeIds should create a fleet token: %#v", scope)
	}

	missing := apiJSON(t, fixture.api, http.MethodPatch, "/api/v1/tokens/"+tokenID, fixture.adminToken, "", map[string]any{
		"scope": map[string]any{"serverNodeIds": []string{"node-missing"}},
	})
	if missing.Code != http.StatusBadRequest {
		t.Fatalf("missing server node status = %d, want 400: %s", missing.Code, missing.Body.String())
	}
	crossType := apiJSON(t, fixture.api, http.MethodPatch, "/api/v1/tokens/"+tokenID, fixture.adminToken, "", map[string]any{
		"scope": map[string]any{"agentIds": []string{"agent-alice-a"}},
	})
	if crossType.Code != http.StatusBadRequest {
		t.Fatalf("server-node agent scope status = %d, want 400: %s", crossType.Code, crossType.Body.String())
	}
	if got := countTokenLifecycleAudits(t, fixture.api.DB, tokenID, "token.scope_updated"); got != 2 {
		t.Fatalf("server scope update audit count = %d, want 2", got)
	}
}

func TestAgentTracerouteAPIEnforcesSensitiveBoundary(t *testing.T) {
	fixture := newTokenAPIFixture(t)
	request := map[string]any{"maxHops": 8}
	response := apiJSON(t, fixture.api, http.MethodPost, "/api/v1/agents/agent-alice-a/trace", fixture.ownerToken, "", request)
	if response.Code != http.StatusOK {
		t.Fatalf("trace status = %d: %s", response.Code, response.Body.String())
	}
	data := tokenResponseData(t, response.Body.Bytes())
	traceID, _ := data["traceId"].(string)
	if traceID == "" {
		t.Fatalf("trace id missing: %s", response.Body.String())
	}
	get := apiJSON(t, fixture.api, http.MethodGet, "/api/v1/traces/"+traceID, fixture.ownerToken, "", nil)
	if get.Code != http.StatusOK {
		t.Fatalf("get trace status = %d: %s", get.Code, get.Body.String())
	}
	sensitive := apiJSON(t, fixture.api, http.MethodPost, "/api/v1/agents/agent-alice-a/trace", fixture.ownerToken, "", map[string]any{"includeSensitive": true})
	if sensitive.Code != http.StatusForbidden {
		t.Fatalf("user sensitive trace status = %d", sensitive.Code)
	}
	admin := apiJSON(t, fixture.api, http.MethodPost, "/api/v1/agents/agent-alice-a/trace", fixture.adminToken, "", map[string]any{"includeSensitive": true})
	if admin.Code != http.StatusOK || admin.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("admin sensitive trace = %d headers=%q body=%s", admin.Code, admin.Header().Get("Cache-Control"), admin.Body.String())
	}
}

func TestTokenAPIAdminCreatesAllTypesAndReplaysWithoutSecret(t *testing.T) {
	fixture := newTokenAPIFixture(t)
	tests := []struct {
		name string
		key  string
		body map[string]any
	}{
		{name: "agent", key: "admin-agent-token", body: map[string]any{"type": "agent", "ownerUserId": fixture.owner.ID, "agentId": "agent-alice-a", "scope": map[string]any{"agentIds": []string{"agent-alice-a"}, "protocols": []string{"tcp"}}}},
		{name: "client", key: "admin-client-token", body: map[string]any{"type": "client", "ownerUserId": fixture.owner.ID, "scope": map[string]any{"agentIds": []string{"agent-alice-a", "agent-alice-b"}, "targetPorts": []int{22}}}},
		{name: "server_node", key: "admin-node-token", body: map[string]any{"type": "server_node", "ownerUserId": fixture.admin.ID, "scope": map[string]any{}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			first := apiJSON(t, fixture.api, http.MethodPost, "/api/v1/tokens", fixture.adminToken, tt.key, tt.body)
			if first.Code != http.StatusCreated {
				t.Fatalf("create %s token status = %d: %s", tt.name, first.Code, first.Body.String())
			}
			firstData := tokenResponseData(t, first.Body.Bytes())
			secret, _ := firstData["secret"].(string)
			if secret == "" || firstData["replayed"] != false || firstData["type"] != tt.name || firstData["status"] != "active" {
				t.Fatalf("unexpected first create data: %+v", firstData)
			}

			id := tokenIDFromResponse(t, first.Body.Bytes())
			record, err := fixture.api.DB.ServiceTokens().Get(context.Background(), id)
			if err != nil {
				t.Fatal(err)
			}
			if record.TokenHash == "" || record.TokenHash == secret || record.Prefix == secret || strings.Contains(record.Scope, secret) {
				t.Fatalf("stored token contains clear secret: %+v", record)
			}
			idem, err := fixture.api.DB.Idempotency().Get(context.Background(), tt.key)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(idem.Response, secret) {
				t.Fatalf("idempotency response leaked secret: %s", idem.Response)
			}
			var stored any
			if err := json.Unmarshal([]byte(idem.Response), &stored); err != nil {
				t.Fatalf("decode idempotency response: %v", err)
			}
			assertNoSensitiveFields(t, stored)

			replay := apiJSON(t, fixture.api, http.MethodPost, "/api/v1/tokens", fixture.adminToken, tt.key, tt.body)
			if replay.Code != first.Code {
				t.Fatalf("replay status = %d, want %d: %s", replay.Code, first.Code, replay.Body.String())
			}
			replayData := tokenResponseData(t, replay.Body.Bytes())
			if _, present := replayData["secret"]; present {
				t.Fatalf("replay returned secret: %+v", replayData)
			}
			if replayData["replayed"] != true || replayData["id"] != id {
				t.Fatalf("unexpected replay data: %+v", replayData)
			}
			if !reflect.DeepEqual(withoutMutationOnlyFields(firstData), withoutMutationOnlyFields(replayData)) {
				t.Fatalf("replay metadata changed:\nfirst=%+v\nreplay=%+v", firstData, replayData)
			}
		})
	}
}

func TestTokenAPIOwnerAuthorizationAndValidation(t *testing.T) {
	fixture := newTokenAPIFixture(t)
	successes := []struct {
		key  string
		body map[string]any
	}{
		{key: "owner-agent", body: map[string]any{"type": "agent", "agentId": "agent-alice-a", "scope": map[string]any{"agentIds": []string{"agent-alice-a"}}}},
		{key: "owner-client", body: map[string]any{"type": "client", "scope": map[string]any{"agentIds": []string{"agent-alice-a", "agent-alice-b"}}}},
	}
	for _, tc := range successes {
		response := apiJSON(t, fixture.api, http.MethodPost, "/api/v1/tokens", fixture.ownerToken, tc.key, tc.body)
		if response.Code != http.StatusCreated {
			t.Fatalf("owner create status = %d: %s", response.Code, response.Body.String())
		}
		if got := tokenResponseData(t, response.Body.Bytes())["ownerUserId"]; got != fixture.owner.ID {
			t.Fatalf("ownerUserId = %v, want %s", got, fixture.owner.ID)
		}
	}

	denials := []struct {
		name string
		body map[string]any
	}{
		{name: "another owner", body: map[string]any{"type": "client", "ownerUserId": fixture.other.ID, "scope": map[string]any{}}},
		{name: "foreign agent binding", body: map[string]any{"type": "agent", "agentId": "agent-bob", "scope": map[string]any{"agentIds": []string{"agent-bob"}}}},
		{name: "disabled agent binding", body: map[string]any{"type": "agent", "agentId": "agent-alice-disabled", "scope": map[string]any{"agentIds": []string{"agent-alice-disabled"}}}},
		{name: "foreign client scope", body: map[string]any{"type": "client", "scope": map[string]any{"agentIds": []string{"agent-bob"}}}},
		{name: "server node", body: map[string]any{"type": "server_node", "nodeId": "node-a", "scope": map[string]any{}}},
	}
	for index, tc := range denials {
		t.Run(tc.name, func(t *testing.T) {
			response := apiJSON(t, fixture.api, http.MethodPost, "/api/v1/tokens", fixture.ownerToken, fmt.Sprintf("owner-denial-%d", index), tc.body)
			if response.Code != http.StatusForbidden {
				t.Fatalf("denial status = %d, want 403: %s", response.Code, response.Body.String())
			}
		})
	}

	expired := apiJSON(t, fixture.api, http.MethodPost, "/api/v1/tokens", fixture.ownerToken, "owner-expired", map[string]any{
		"type": "client", "scope": map[string]any{}, "expiresAt": time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano),
	})
	if expired.Code != http.StatusBadRequest {
		t.Fatalf("expired token status = %d, want 400: %s", expired.Code, expired.Body.String())
	}
	invalidScope := apiJSON(t, fixture.api, http.MethodPost, "/api/v1/tokens", fixture.ownerToken, "owner-invalid-scope", map[string]any{
		"type": "client", "scope": map[string]any{"protocols": []string{"smtp"}},
	})
	if invalidScope.Code != http.StatusBadRequest {
		t.Fatalf("invalid scope status = %d, want 400: %s", invalidScope.Code, invalidScope.Body.String())
	}

	audits, err := fixture.api.DB.Audits().List(context.Background(), storage.AuditFilter{}, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	denied := 0
	for _, audit := range audits.Items {
		if audit.Action == "token.authorization_denied" && audit.ActorUserID == fixture.owner.ID {
			denied++
			assertSafeTokenAuditDetails(t, audit.Details)
		}
	}
	if denied != len(denials) {
		t.Fatalf("authorization denied audits = %d, want %d: %+v", denied, len(denials), audits.Items)
	}
}

func TestTokenAPIPaginationLifecycleRedactionAndAudit(t *testing.T) {
	fixture := newTokenAPIFixture(t)
	createdIDs := make([]string, 0, 3)
	for index, agentID := range []string{"agent-alice-a", "agent-alice-b", "agent-alice-a"} {
		response := apiJSON(t, fixture.api, http.MethodPost, "/api/v1/tokens", fixture.ownerToken, fmt.Sprintf("owner-page-%d", index), map[string]any{
			"type": "client", "scope": map[string]any{"agentIds": []string{agentID}},
		})
		if response.Code != http.StatusCreated {
			t.Fatalf("create paged token %d status = %d: %s", index, response.Code, response.Body.String())
		}
		createdIDs = append(createdIDs, tokenIDFromResponse(t, response.Body.Bytes()))
	}
	foreign := apiJSON(t, fixture.api, http.MethodPost, "/api/v1/tokens", fixture.otherToken, "foreign-page", map[string]any{
		"type": "client", "scope": map[string]any{"agentIds": []string{"agent-bob"}},
	})
	if foreign.Code != http.StatusCreated {
		t.Fatalf("create foreign token: %d %s", foreign.Code, foreign.Body.String())
	}

	page1 := apiJSON(t, fixture.api, http.MethodGet, "/api/v1/tokens?type=client&limit=2", fixture.ownerToken, "", nil)
	if page1.Code != http.StatusOK {
		t.Fatalf("first page status = %d: %s", page1.Code, page1.Body.String())
	}
	page1Data := tokenResponseData(t, page1.Body.Bytes())
	items1, _ := page1Data["items"].([]any)
	if len(items1) != 2 || page1Data["hasMore"] != true || page1Data["nextCursor"] == "" {
		t.Fatalf("unexpected first page: %+v", page1Data)
	}
	assertNoSensitiveFields(t, page1Data)
	for _, item := range items1 {
		if item.(map[string]any)["ownerUserId"] != fixture.owner.ID {
			t.Fatalf("owner filtering happened after pagination: %+v", page1Data)
		}
	}
	cursor := page1Data["nextCursor"].(string)
	page2 := apiJSON(t, fixture.api, http.MethodGet, "/api/v1/tokens?type=client&limit=2&cursor="+cursor, fixture.ownerToken, "", nil)
	page2Data := tokenResponseData(t, page2.Body.Bytes())
	items2, _ := page2Data["items"].([]any)
	if page2.Code != http.StatusOK || len(items2) != 1 || page2Data["hasMore"] != false {
		t.Fatalf("unexpected second page: %d %+v", page2.Code, page2Data)
	}

	detail := apiJSON(t, fixture.api, http.MethodGet, "/api/v1/tokens/"+createdIDs[0], fixture.ownerToken, "", nil)
	if detail.Code != http.StatusOK || tokenResponseData(t, detail.Body.Bytes())["status"] != "active" {
		t.Fatalf("active detail = %d %s", detail.Code, detail.Body.String())
	}
	assertNoSensitiveFields(t, tokenResponseData(t, detail.Body.Bytes()))

	foreignDetail := apiJSON(t, fixture.api, http.MethodGet, "/api/v1/tokens/"+tokenIDFromResponse(t, foreign.Body.Bytes()), fixture.ownerToken, "", nil)
	if foreignDetail.Code != http.StatusForbidden {
		t.Fatalf("foreign detail status = %d, want 403: %s", foreignDetail.Code, foreignDetail.Body.String())
	}

	rotate := apiJSON(t, fixture.api, http.MethodPost, "/api/v1/tokens/"+createdIDs[0]+"/rotate", fixture.ownerToken, "rotate-owner-token", nil)
	if rotate.Code != http.StatusCreated {
		t.Fatalf("rotate status = %d: %s", rotate.Code, rotate.Body.String())
	}
	rotateData := tokenResponseData(t, rotate.Body.Bytes())
	if rotateData["secret"] == "" || rotateData["replayed"] != false || rotateData["id"] == createdIDs[0] {
		t.Fatalf("unexpected rotate response: %+v", rotateData)
	}
	replacementID := rotateData["id"].(string)
	rotateReplay := apiJSON(t, fixture.api, http.MethodPost, "/api/v1/tokens/"+createdIDs[0]+"/rotate", fixture.ownerToken, "rotate-owner-token", nil)
	rotateReplayData := tokenResponseData(t, rotateReplay.Body.Bytes())
	if rotateReplay.Code != rotate.Code || rotateReplayData["id"] != replacementID || rotateReplayData["replayed"] != true {
		t.Fatalf("rotate replay = %d %+v", rotateReplay.Code, rotateReplayData)
	}
	if _, present := rotateReplayData["secret"]; present {
		t.Fatalf("rotate replay leaked secret: %+v", rotateReplayData)
	}

	oldDetail := apiJSON(t, fixture.api, http.MethodGet, "/api/v1/tokens/"+createdIDs[0], fixture.ownerToken, "", nil)
	if tokenResponseData(t, oldDetail.Body.Bytes())["status"] != "revoked" {
		t.Fatalf("old token was not dynamically revoked: %s", oldDetail.Body.String())
	}
	revoke := apiJSON(t, fixture.api, http.MethodPost, "/api/v1/tokens/"+replacementID+"/revoke", fixture.ownerToken, "", nil)
	if revoke.Code != http.StatusOK || tokenResponseData(t, revoke.Body.Bytes())["status"] != "revoked" {
		t.Fatalf("revoke response = %d %s", revoke.Code, revoke.Body.String())
	}

	agent, err := fixture.api.DB.Agents().Get(context.Background(), "agent-alice-b")
	if err != nil {
		t.Fatal(err)
	}
	agent.Enabled = false
	if err := fixture.api.DB.Agents().Update(context.Background(), agent); err != nil {
		t.Fatal(err)
	}
	unavailable := apiJSON(t, fixture.api, http.MethodGet, "/api/v1/tokens/"+createdIDs[1], fixture.ownerToken, "", nil)
	if unavailable.Code != http.StatusOK || tokenResponseData(t, unavailable.Body.Bytes())["status"] != "unavailable" {
		t.Fatalf("disabled scoped Agent status = %d %s", unavailable.Code, unavailable.Body.String())
	}

	past := time.Now().UTC().Add(-time.Hour)
	expiredRecord := storage.ServiceToken{ID: "stok_expired_fixture", Type: storage.TokenTypeClient, OwnerUserID: fixture.owner.ID, Prefix: "expired", TokenHash: "expired-fixture-hash", Scope: `{}`, ExpiresAt: &past}
	if err := fixture.api.DB.ServiceTokens().Create(context.Background(), expiredRecord); err != nil {
		t.Fatal(err)
	}
	expiredDetail := apiJSON(t, fixture.api, http.MethodGet, "/api/v1/tokens/"+expiredRecord.ID, fixture.ownerToken, "", nil)
	if expiredDetail.Code != http.StatusOK || tokenResponseData(t, expiredDetail.Body.Bytes())["status"] != "expired" {
		t.Fatalf("expired detail = %d %s", expiredDetail.Code, expiredDetail.Body.String())
	}

	audits, err := fixture.api.DB.Audits().List(context.Background(), storage.AuditFilter{}, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	wantActions := map[string]bool{"token.created": false, "token.rotated": false, "token.revoked": false, "token.authorization_denied": false}
	for _, audit := range audits.Items {
		if _, relevant := wantActions[audit.Action]; relevant {
			wantActions[audit.Action] = true
			assertSafeTokenAuditDetails(t, audit.Details)
		}
	}
	for action, found := range wantActions {
		if !found {
			t.Fatalf("missing %s audit in %+v", action, audits.Items)
		}
	}
}

func TestTokenAPIConcurrentIdempotencyCreatesOnlyOneToken(t *testing.T) {
	fixture := newTokenAPIFixture(t)
	const requests = 8
	start := make(chan struct{})
	type concurrentResponse struct {
		status int
		body   []byte
	}
	responses := make(chan concurrentResponse, requests)
	var wg sync.WaitGroup
	for i := 0; i < requests; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			response := apiJSON(t, fixture.api, http.MethodPost, "/api/v1/tokens", fixture.ownerToken, "concurrent-create", map[string]any{
				"type": "client", "scope": map[string]any{"agentIds": []string{"agent-alice-a"}},
			})
			responses <- concurrentResponse{status: response.Code, body: append([]byte(nil), response.Body.Bytes()...)}
		}()
	}
	close(start)
	wg.Wait()
	close(responses)

	created := 0
	conflicts := 0
	secretResponses := 0
	ids := map[string]struct{}{}
	for response := range responses {
		switch response.status {
		case http.StatusCreated:
			created++
			data := tokenResponseData(t, response.body)
			ids[data["id"].(string)] = struct{}{}
			if _, present := data["secret"]; present {
				secretResponses++
			}
		case http.StatusConflict:
			conflicts++
		default:
			t.Fatalf("concurrent request status = %d, want 201 or 409: %s", response.status, response.body)
		}
	}
	if created == 0 || created+conflicts != requests {
		t.Fatalf("created=%d conflicts=%d requests=%d", created, conflicts, requests)
	}
	if secretResponses != 1 || len(ids) != 1 {
		t.Fatalf("secret responses = %d, token ids = %+v; want one persisted winner", secretResponses, ids)
	}
	page, err := fixture.api.DB.ServiceTokens().List(context.Background(), storage.ServiceTokenFilter{OwnerUserID: fixture.owner.ID, Type: storage.TokenTypeClient}, "", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("persisted tokens = %d, want 1: %+v", len(page.Items), page.Items)
	}
}

func TestTokenAPIConcurrentIdempotencyRotatesOnlyOnce(t *testing.T) {
	fixture := newTokenAPIFixture(t)
	created := apiJSON(t, fixture.api, http.MethodPost, "/api/v1/tokens", fixture.ownerToken, "rotation-source", map[string]any{
		"type": "client", "scope": map[string]any{"agentIds": []string{"agent-alice-a"}},
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("create rotation source: %d %s", created.Code, created.Body.String())
	}
	sourceID := tokenIDFromResponse(t, created.Body.Bytes())

	const requests = 16
	type concurrentResponse struct {
		status int
		body   []byte
	}
	start := make(chan struct{})
	responses := make(chan concurrentResponse, requests)
	var wg sync.WaitGroup
	for i := 0; i < requests; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			response := apiJSON(t, fixture.api, http.MethodPost, "/api/v1/tokens/"+sourceID+"/rotate", fixture.ownerToken, "concurrent-rotate", nil)
			responses <- concurrentResponse{status: response.Code, body: append([]byte(nil), response.Body.Bytes()...)}
		}()
	}
	close(start)
	wg.Wait()
	close(responses)

	secretResponses := 0
	replacementIDs := map[string]struct{}{}
	for response := range responses {
		switch response.status {
		case http.StatusCreated:
			data := tokenResponseData(t, response.body)
			replacementIDs[data["id"].(string)] = struct{}{}
			if _, present := data["secret"]; present {
				secretResponses++
			}
		case http.StatusConflict:
			if !strings.Contains(strings.ToLower(string(response.body)), "in progress") {
				t.Fatalf("rotation loser returned an ambiguous conflict: %s", response.body)
			}
		default:
			t.Fatalf("concurrent rotate status = %d, want 201 or explicit in-progress 409: %s", response.status, response.body)
		}
	}
	if secretResponses != 1 || len(replacementIDs) != 1 {
		t.Fatalf("rotation secret responses = %d, replacement IDs = %+v; want one winner", secretResponses, replacementIDs)
	}
	page, err := fixture.api.DB.ServiceTokens().List(context.Background(), storage.ServiceTokenFilter{OwnerUserID: fixture.owner.ID, Type: storage.TokenTypeClient}, "", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("rotation persisted %d tokens, want source plus one replacement: %+v", len(page.Items), page.Items)
	}
}

func TestTokenAPIAuthorizationChangeBeforeCommitCannotCreate(t *testing.T) {
	fixture := newTokenAPIFixture(t)
	barrier := &nthAgentGetBarrier{
		AgentRepository: fixture.api.tokenService.agents,
		targetID:        "agent-alice-a",
		targetCall:      2,
		reached:         make(chan struct{}),
		release:         make(chan struct{}),
	}
	fixture.api.tokenService.agents = barrier

	responseCh := make(chan concurrentHTTPResponse, 1)
	go func() {
		response := apiJSON(t, fixture.api, http.MethodPost, "/api/v1/tokens", fixture.ownerToken, "authorization-race", map[string]any{
			"type": "agent", "agentId": "agent-alice-a", "scope": map[string]any{},
		})
		responseCh <- concurrentHTTPResponse{status: response.Code, body: append([]byte(nil), response.Body.Bytes()...)}
	}()
	select {
	case <-barrier.reached:
	case <-time.After(2 * time.Second):
		t.Fatal("credential planning did not reach the Agent authorization read")
	}
	agent, err := fixture.api.DB.Agents().Get(context.Background(), "agent-alice-a")
	if err != nil {
		t.Fatal(err)
	}
	agent.OwnerUserID = fixture.other.ID
	agent.Enabled = false
	if err := fixture.api.DB.Agents().Update(context.Background(), agent); err != nil {
		t.Fatal(err)
	}
	close(barrier.release)
	response := <-responseCh
	if response.status == http.StatusCreated {
		t.Fatalf("Token created from stale owner/enabled authorization: %s", response.body)
	}
	page, err := fixture.api.DB.ServiceTokens().List(context.Background(), storage.ServiceTokenFilter{OwnerUserID: fixture.owner.ID, AgentID: "agent-alice-a"}, "", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 0 {
		t.Fatalf("stale authorization persisted Tokens: %+v", page.Items)
	}
	if _, err := fixture.api.DB.Idempotency().Get(context.Background(), "authorization-race"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("failed authorization retained idempotency claim: %v", err)
	}
}

func TestTokenAPIAuthorizationDeniedAuditFailureIsObservable(t *testing.T) {
	fixture := newTokenAPIFixture(t)
	fixture.api.tokenService.audits = failingAuditRepository{
		AuditRepository: fixture.api.DB.Audits(),
		err:             errors.New("sensitive-secret-value"),
	}
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	response := apiJSON(t, fixture.api, http.MethodPost, "/api/v1/tokens", fixture.ownerToken, "denied-audit-failure", map[string]any{
		"type": "server_node", "nodeId": "node-a", "scope": map[string]any{},
	})
	if response.Code != http.StatusForbidden {
		t.Fatalf("denied response status = %d, want 403: %s", response.Code, response.Body.String())
	}
	logged := logs.String()
	if !strings.Contains(logged, "token_authorization_denied_audit_failed") || !strings.Contains(logged, "server_node_admin_required") || !strings.Contains(logged, fixture.owner.ID) {
		t.Fatalf("audit failure was not logged with structured identifiers: %s", logged)
	}
	if strings.Contains(logged, "sensitive-secret-value") || strings.Contains(strings.ToLower(logged), "secret=") {
		t.Fatalf("audit failure log leaked sensitive error content: %s", logged)
	}
}

func TestTokenAPIStatusStorageErrorsReturn500(t *testing.T) {
	tests := []struct {
		name   string
		body   map[string]any
		inject func(*TokenService, error)
	}{
		{name: "owner repository", body: map[string]any{"type": "client", "scope": map[string]any{}}, inject: func(service *TokenService, want error) {
			service.users = failingUserGetRepository{UserRepository: service.users, err: want}
		}},
		{name: "Agent repository", body: map[string]any{"type": "agent", "ownerUserId": "OWNER", "agentId": "agent-alice-a", "scope": map[string]any{"agentIds": []string{"agent-alice-a"}}}, inject: func(service *TokenService, want error) {
			service.agents = failingAgentGetRepository{AgentRepository: service.agents, err: want}
		}},
		{name: "node repository", body: map[string]any{"type": "server_node", "scope": map[string]any{"serverNodeIds": []string{"node-a"}}}, inject: func(service *TokenService, want error) {
			service.nodes = failingNodeGetRepository{NodeRepository: service.nodes, err: want}
		}},
	}
	for index, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newTokenAPIFixture(t)
			if owner, ok := tc.body["ownerUserId"]; ok && owner == "OWNER" {
				tc.body["ownerUserId"] = fixture.owner.ID
			}
			created := apiJSON(t, fixture.api, http.MethodPost, "/api/v1/tokens", fixture.adminToken, fmt.Sprintf("status-error-%d", index), tc.body)
			if created.Code != http.StatusCreated {
				t.Fatalf("create status fixture = %d: %s", created.Code, created.Body.String())
			}
			wantErr := errors.New("status repository unavailable")
			tc.inject(fixture.api.tokenService, wantErr)
			detail := apiJSON(t, fixture.api, http.MethodGet, "/api/v1/tokens/"+tokenIDFromResponse(t, created.Body.Bytes()), fixture.adminToken, "", nil)
			if detail.Code != http.StatusInternalServerError {
				t.Fatalf("status repository error returned %d, want 500: %s", detail.Code, detail.Body.String())
			}
			if !strings.Contains(detail.Body.String(), wantErr.Error()) {
				t.Fatalf("status error did not propagate: %s", detail.Body.String())
			}
		})
	}
}

func TestTokenAPIConcurrentRevokePreservesFirstTransitionAndAudit(t *testing.T) {
	fixture := newTokenAPIFixture(t)
	created := apiJSON(t, fixture.api, http.MethodPost, "/api/v1/tokens", fixture.ownerToken, "concurrent-revoke-source", map[string]any{
		"type": "client", "scope": map[string]any{},
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("create revoke source = %d: %s", created.Code, created.Body.String())
	}
	tokenID := tokenIDFromResponse(t, created.Body.Bytes())
	const requests = 8
	barrier := newTwoPhaseTokenGetBarrier(fixture.api.tokenService.tokens, tokenID, requests)
	fixture.api.tokenService.tokens = barrier
	responses := make(chan concurrentHTTPResponse, requests)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < requests; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			response := apiJSON(t, fixture.api, http.MethodPost, "/api/v1/tokens/"+tokenID+"/revoke", fixture.ownerToken, "", nil)
			responses <- concurrentHTTPResponse{status: response.Code, body: append([]byte(nil), response.Body.Bytes()...)}
		}()
	}
	close(start)
	barrier.waitAndRelease(t, 1)
	barrier.waitAndRelease(t, 2)
	wg.Wait()
	close(responses)
	revokedAt := map[string]struct{}{}
	for response := range responses {
		if response.status != http.StatusOK {
			t.Fatalf("concurrent revoke status = %d: %s", response.status, response.body)
		}
		value, _ := tokenResponseData(t, response.body)["revokedAt"].(string)
		if value == "" {
			t.Fatalf("revoke response has no revokedAt: %s", response.body)
		}
		revokedAt[value] = struct{}{}
	}
	if len(revokedAt) != 1 {
		t.Fatalf("concurrent revoke overwrote the first timestamp: %+v", revokedAt)
	}
	if got := countTokenLifecycleAudits(t, fixture.api.DB, tokenID, "token.revoked"); got != 1 {
		t.Fatalf("token.revoked audits = %d, want 1", got)
	}
}

func TestTokenAPIConcurrentRevokeVsRotateHasOneLifecycleWinner(t *testing.T) {
	fixture := newTokenAPIFixture(t)
	created := apiJSON(t, fixture.api, http.MethodPost, "/api/v1/tokens", fixture.ownerToken, "revoke-rotate-source", map[string]any{
		"type": "client", "scope": map[string]any{},
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("create lifecycle source = %d: %s", created.Code, created.Body.String())
	}
	tokenID := tokenIDFromResponse(t, created.Body.Bytes())
	barrier := &selectedTokenGetBarrier{
		ServiceTokenRepository: fixture.api.tokenService.tokens,
		targetID:               tokenID,
		blocked: map[int]tokenGetBlock{
			3: {reached: make(chan struct{}), release: make(chan struct{})},
			5: {reached: make(chan struct{}), release: make(chan struct{})},
		},
	}
	fixture.api.tokenService.tokens = barrier

	rotateCh := make(chan concurrentHTTPResponse, 1)
	go func() {
		response := apiJSON(t, fixture.api, http.MethodPost, "/api/v1/tokens/"+tokenID+"/rotate", fixture.ownerToken, "revoke-vs-rotate", nil)
		rotateCh <- concurrentHTTPResponse{status: response.Code, body: append([]byte(nil), response.Body.Bytes()...)}
	}()
	barrier.wait(t, 3)
	revokeCh := make(chan concurrentHTTPResponse, 1)
	go func() {
		response := apiJSON(t, fixture.api, http.MethodPost, "/api/v1/tokens/"+tokenID+"/revoke", fixture.ownerToken, "", nil)
		revokeCh <- concurrentHTTPResponse{status: response.Code, body: append([]byte(nil), response.Body.Bytes()...)}
	}()
	barrier.wait(t, 5)
	barrier.release(3)
	rotate := <-rotateCh
	if rotate.status != http.StatusCreated {
		t.Fatalf("forced rotation winner status = %d: %s", rotate.status, rotate.body)
	}
	barrier.release(5)
	revoke := <-revokeCh
	if revoke.status != http.StatusOK {
		t.Fatalf("revoke loser status = %d: %s", revoke.status, revoke.body)
	}
	rotated := countTokenLifecycleAudits(t, fixture.api.DB, tokenID, "token.rotated")
	revoked := countTokenLifecycleAudits(t, fixture.api.DB, tokenID, "token.revoked")
	if rotated != 1 || revoked != 0 {
		t.Fatalf("lifecycle audits rotated=%d revoked=%d, want 1/0", rotated, revoked)
	}
}

type concurrentHTTPResponse struct {
	status int
	body   []byte
}

type nthAgentGetBarrier struct {
	storage.AgentRepository
	targetID   string
	targetCall int
	reached    chan struct{}
	release    chan struct{}
	mu         sync.Mutex
	calls      int
}

func (r *nthAgentGetBarrier) Get(ctx context.Context, id string) (storage.Agent, error) {
	agent, err := r.AgentRepository.Get(ctx, id)
	if id != r.targetID || err != nil {
		return agent, err
	}
	r.mu.Lock()
	r.calls++
	call := r.calls
	r.mu.Unlock()
	if call == r.targetCall {
		close(r.reached)
		<-r.release
	}
	return agent, nil
}

type failingAuditRepository struct {
	storage.AuditRepository
	err error
}

func (r failingAuditRepository) Create(context.Context, storage.AuditLog) error { return r.err }

type failingUserGetRepository struct {
	storage.UserRepository
	err error
}

func (r failingUserGetRepository) Get(context.Context, string) (storage.User, error) {
	return storage.User{}, r.err
}

type failingAgentGetRepository struct {
	storage.AgentRepository
	err error
}

func (r failingAgentGetRepository) Get(context.Context, string) (storage.Agent, error) {
	return storage.Agent{}, r.err
}

type failingNodeGetRepository struct {
	storage.NodeRepository
	err error
}

func (r failingNodeGetRepository) Get(context.Context, string) (storage.ServerNode, error) {
	return storage.ServerNode{}, r.err
}

type twoPhaseTokenGetBarrier struct {
	storage.ServiceTokenRepository
	targetID string
	phaseN   int
	mu       sync.Mutex
	calls    int
	reached  [2]chan struct{}
	release  [2]chan struct{}
}

func newTwoPhaseTokenGetBarrier(base storage.ServiceTokenRepository, targetID string, phaseN int) *twoPhaseTokenGetBarrier {
	return &twoPhaseTokenGetBarrier{
		ServiceTokenRepository: base, targetID: targetID, phaseN: phaseN,
		reached: [2]chan struct{}{make(chan struct{}), make(chan struct{})},
		release: [2]chan struct{}{make(chan struct{}), make(chan struct{})},
	}
}

func (r *twoPhaseTokenGetBarrier) Get(ctx context.Context, id string) (storage.ServiceToken, error) {
	token, err := r.ServiceTokenRepository.Get(ctx, id)
	if id != r.targetID || err != nil {
		return token, err
	}
	r.mu.Lock()
	r.calls++
	call := r.calls
	phase := (call - 1) / r.phaseN
	within := (call-1)%r.phaseN + 1
	if phase < 2 && within == r.phaseN {
		close(r.reached[phase])
	}
	r.mu.Unlock()
	if phase < 2 {
		<-r.release[phase]
	}
	return token, nil
}

func (r *twoPhaseTokenGetBarrier) waitAndRelease(t *testing.T, phase int) {
	t.Helper()
	select {
	case <-r.reached[phase-1]:
	case <-time.After(2 * time.Second):
		t.Fatalf("Token Get phase %d did not reach %d readers", phase, r.phaseN)
	}
	close(r.release[phase-1])
}

type tokenGetBlock struct {
	reached chan struct{}
	release chan struct{}
}

type selectedTokenGetBarrier struct {
	storage.ServiceTokenRepository
	targetID string
	mu       sync.Mutex
	calls    int
	blocked  map[int]tokenGetBlock
}

func (r *selectedTokenGetBarrier) Get(ctx context.Context, id string) (storage.ServiceToken, error) {
	token, err := r.ServiceTokenRepository.Get(ctx, id)
	if id != r.targetID || err != nil {
		return token, err
	}
	r.mu.Lock()
	r.calls++
	block, blocked := r.blocked[r.calls]
	r.mu.Unlock()
	if blocked {
		close(block.reached)
		<-block.release
	}
	return token, nil
}

func (r *selectedTokenGetBarrier) wait(t *testing.T, call int) {
	t.Helper()
	select {
	case <-r.blocked[call].reached:
	case <-time.After(2 * time.Second):
		t.Fatalf("Token Get call %d did not reach barrier", call)
	}
}

func (r *selectedTokenGetBarrier) release(call int) { close(r.blocked[call].release) }

func countTokenLifecycleAudits(t *testing.T, db *storage.DB, tokenID, action string) int {
	t.Helper()
	page, err := db.Audits().List(context.Background(), storage.AuditFilter{}, "", 500)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, audit := range page.Items {
		if audit.Action == action && audit.ResourceID == tokenID {
			count++
		}
	}
	return count
}

func assertSafeTokenAuditDetails(t *testing.T, raw string) {
	t.Helper()
	var details map[string]any
	if err := json.Unmarshal([]byte(raw), &details); err != nil {
		t.Fatalf("invalid token audit details %q: %v", raw, err)
	}
	allowed := map[string]bool{
		"type": true, "prefix": true, "ownerUserId": true, "agentId": true, "nodeId": true,
		"tokenId": true, "replacementTokenId": true, "resourceIds": true, "reasonCode": true,
	}
	for key := range details {
		if !allowed[key] {
			t.Fatalf("token audit detail %q is not allowlisted: %s", key, raw)
		}
	}
	assertNoSensitiveFields(t, details)
}
