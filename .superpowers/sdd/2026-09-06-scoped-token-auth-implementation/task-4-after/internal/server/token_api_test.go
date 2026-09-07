package server

import (
	"context"
	"encoding/json"
	"fmt"
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

func TestTokenAPIAdminCreatesAllTypesAndReplaysWithoutSecret(t *testing.T) {
	fixture := newTokenAPIFixture(t)
	tests := []struct {
		name string
		key  string
		body map[string]any
	}{
		{name: "agent", key: "admin-agent-token", body: map[string]any{"type": "agent", "ownerUserId": fixture.owner.ID, "agentId": "agent-alice-a", "scope": map[string]any{"agentIds": []string{"agent-alice-a"}, "protocols": []string{"tcp"}}}},
		{name: "client", key: "admin-client-token", body: map[string]any{"type": "client", "ownerUserId": fixture.owner.ID, "scope": map[string]any{"agentIds": []string{"agent-alice-a", "agent-alice-b"}, "targetPorts": []int{22}}}},
		{name: "server_node", key: "admin-node-token", body: map[string]any{"type": "server_node", "ownerUserId": fixture.admin.ID, "nodeId": "node-a", "scope": map[string]any{}}},
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

	audits, err := fixture.api.DB.Audits().List(context.Background(), "", 100)
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

	audits, err := fixture.api.DB.Audits().List(context.Background(), "", 100)
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
