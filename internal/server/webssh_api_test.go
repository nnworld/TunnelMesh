package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/auth"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

func TestWebSSHAPICreateGetAndClose(t *testing.T) {
	api, token, _ := remoteServerAPITest(t)
	ctx := context.Background()
	lease := storage.AgentLease{AgentID: "agent-a", ConnectionID: "conn-webssh", NodeID: "agent-node-a", ServerNodeID: "node-a", InstanceID: "instance", ConnectionEpoch: 1, TTL: time.Minute}
	if _, err := api.DB.Leases().RegisterConnection(ctx, lease); err != nil {
		t.Fatal(err)
	}
	api.websshService = NewWebSSHSessionService(api.DB.WebSSHSessions(), api.DB.RemoteServers(), api.DB.Credentials(), api.DB.Agents(), api.DB.Leases(), api.DB.Audits(), "node-a")
	h := api.Handler()
	create := apiJSON(t, h, http.MethodPost, "/api/v1/remote-servers/server-a/ssh-sessions", token, "webssh-key", map[string]any{"username": "deploy"})
	if create.Code != http.StatusCreated {
		t.Fatalf("create status = %d: %s", create.Code, create.Body.String())
	}
	var response struct {
		Data struct {
			SessionID     string `json:"sessionId"`
			Ticket        string `json:"ticket"`
			WebSocketPath string `json:"websocketPath"`
		} `json:"data"`
	}
	if err := json.Unmarshal(create.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Data.SessionID == "" || response.Data.Ticket == "" || response.Data.WebSocketPath == "" {
		t.Fatalf("ticket response = %+v", response.Data)
	}
	path := "/api/v1/ssh-sessions/" + response.Data.SessionID
	if r := apiJSON(t, h, http.MethodGet, path, token, "", nil); r.Code != http.StatusOK {
		t.Fatalf("get status = %d: %s", r.Code, r.Body.String())
	}
	if r := apiJSON(t, h, http.MethodDelete, path, token, "", nil); r.Code != http.StatusOK {
		t.Fatalf("delete status = %d: %s", r.Code, r.Body.String())
	}
}

// A cross-owner WebSSH request must answer 403 like the remote-server and
// credential APIs. Returning 500 with the raw internal error text hides the
// real reason from the admin console and leaks service internals.
func TestWebSSHAPIForbiddenReturns403(t *testing.T) {
	api, _, _ := remoteServerAPITest(t)
	ctx := context.Background()
	if _, err := api.Auth.CreateUser(ctx, "bob", "bob-pass", "user"); err != nil {
		t.Fatal(err)
	}
	bob := apiToken(t, api, "bob", "bob-pass")
	api.websshService = NewWebSSHSessionService(api.DB.WebSSHSessions(), api.DB.RemoteServers(), api.DB.Credentials(), api.DB.Agents(), api.DB.Leases(), api.DB.Audits(), "node-a")
	h := api.Handler()

	create := apiJSON(t, h, http.MethodPost, "/api/v1/remote-servers/server-a/ssh-sessions", bob, "webssh-forbidden", map[string]any{"username": "deploy"})
	if create.Code != http.StatusForbidden {
		t.Fatalf("create status = %d, want %d: %s", create.Code, http.StatusForbidden, create.Body.String())
	}

	alice := apiToken(t, api, "alice", "alice-pass")
	if _, err := api.DB.Leases().RegisterConnection(ctx, storage.AgentLease{AgentID: "agent-a", ConnectionID: "conn-forbidden", NodeID: "agent-node-a", ServerNodeID: "node-a", InstanceID: "instance", ConnectionEpoch: 1, TTL: time.Minute}); err != nil {
		t.Fatal(err)
	}
	created := apiJSON(t, h, http.MethodPost, "/api/v1/remote-servers/server-a/ssh-sessions", alice, "webssh-owner", map[string]any{"username": "deploy"})
	if created.Code != http.StatusCreated {
		t.Fatalf("owner create status = %d: %s", created.Code, created.Body.String())
	}
	var ticket struct {
		Data struct {
			SessionID string `json:"sessionId"`
		} `json:"data"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &ticket); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/ssh-sessions/" + ticket.Data.SessionID
	if r := apiJSON(t, h, http.MethodGet, path, bob, "", nil); r.Code != http.StatusForbidden {
		t.Fatalf("get status = %d, want %d: %s", r.Code, http.StatusForbidden, r.Body.String())
	}
	if r := apiJSON(t, h, http.MethodDelete, path, bob, "", nil); r.Code != http.StatusForbidden {
		t.Fatalf("delete status = %d, want %d: %s", r.Code, http.StatusForbidden, r.Body.String())
	}
}

// docs/api/openapi.yaml documents 409 for the active-session limit. Falling
// through writeStorageError answered 500 instead, which the admin console
// could not distinguish from a real internal failure.
func TestWebSSHAPIActiveSessionLimitReturnsConflict(t *testing.T) {
	api, token, _ := remoteServerAPITest(t)
	ctx := context.Background()
	if _, err := api.DB.Leases().RegisterConnection(ctx, storage.AgentLease{AgentID: "agent-a", ConnectionID: "conn-limit", NodeID: "agent-node-a", ServerNodeID: "node-a", InstanceID: "instance", ConnectionEpoch: 1, TTL: time.Minute}); err != nil {
		t.Fatal(err)
	}
	api.websshService = NewWebSSHSessionService(api.DB.WebSSHSessions(), api.DB.RemoteServers(), api.DB.Credentials(), api.DB.Agents(), api.DB.Leases(), api.DB.Audits(), "node-a")
	api.websshService.SetLimits(WebSSHSessionLimits{MaxActivePerUser: 1})
	server, err := api.DB.RemoteServers().Get(ctx, "server-a")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	// Only sessions that reached active consume the quota; pending tickets are
	// reclaimed by the sweeper, so seed one active row for the owner.
	if _, err := api.DB.WebSSHSessions().Create(ctx, storage.WebSSHSession{
		OwnerUserID: server.OwnerUserID, RemoteServerID: server.ID, AgentID: server.AgentID,
		OwnerNodeID: "node-a", TicketHash: "seed-hash", TicketExpiresAt: now.Add(time.Minute),
		Status: storage.WebSSHSessionActive, CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	h := api.Handler()
	second := apiJSON(t, h, http.MethodPost, "/api/v1/remote-servers/server-a/ssh-sessions", token, "webssh-limit-1", map[string]any{"username": "deploy"})
	if second.Code != http.StatusConflict {
		t.Fatalf("second create status = %d, want %d: %s", second.Code, http.StatusConflict, second.Body.String())
	}
	if !strings.Contains(second.Body.String(), "webssh active session limit reached") {
		t.Fatalf("conflict body should keep the stable identifier: %s", second.Body.String())
	}
}

// The console needs a self-service way to release a stuck active-session quota
// without database access, so GET /api/v1/ssh-sessions lists the caller's own
// active sessions. Other owners' sessions must never appear, and the list must
// follow the shared cursor pagination contract.
func TestWebSSHSessionAPIListOwnOnly(t *testing.T) {
	api, token, _ := remoteServerAPITest(t)
	ctx := context.Background()
	bobUser, err := api.Auth.CreateUser(ctx, "bob", "bob-pass", "user")
	if err != nil {
		t.Fatal(err)
	}
	bob := apiToken(t, api, "bob", "bob-pass")
	server, err := api.DB.RemoteServers().Get(ctx, "server-a")
	if err != nil {
		t.Fatal(err)
	}
	api.websshService = NewWebSSHSessionService(api.DB.WebSSHSessions(), api.DB.RemoteServers(), api.DB.Credentials(), api.DB.Agents(), api.DB.Leases(), api.DB.Audits(), "node-a")
	now := time.Now().UTC()
	for _, session := range []storage.WebSSHSession{
		{ID: "webssh-api-1", OwnerUserID: server.OwnerUserID, Status: storage.WebSSHSessionActive},
		{ID: "webssh-api-2", OwnerUserID: server.OwnerUserID, Status: storage.WebSSHSessionActive},
		{ID: "webssh-api-closed", OwnerUserID: server.OwnerUserID, Status: storage.WebSSHSessionClosed},
		{ID: "webssh-api-bob", OwnerUserID: bobUser.ID, Status: storage.WebSSHSessionActive},
	} {
		session.RemoteServerID = server.ID
		session.AgentID = server.AgentID
		session.OwnerNodeID = "node-a"
		session.TicketHash = "hash-" + session.ID
		session.TicketExpiresAt = now.Add(time.Minute)
		session.CreatedAt = now
		session.ExpiresAt = now.Add(time.Hour)
		if _, err := api.DB.WebSSHSessions().Create(ctx, session); err != nil {
			t.Fatalf("seed session %s: %v", session.ID, err)
		}
	}
	h := api.Handler()

	if r := apiJSON(t, h, http.MethodGet, "/api/v1/ssh-sessions", "", "", nil); r.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status = %d, want %d: %s", r.Code, http.StatusUnauthorized, r.Body.String())
	}
	if r := apiJSON(t, h, http.MethodPost, "/api/v1/ssh-sessions", token, "", nil); r.Code != http.StatusMethodNotAllowed {
		t.Fatalf("post status = %d, want %d: %s", r.Code, http.StatusMethodNotAllowed, r.Body.String())
	}

	type listData struct {
		Items []struct {
			ID             string `json:"id"`
			RemoteServerID string `json:"remoteServerId"`
			Status         string `json:"status"`
		} `json:"items"`
		NextCursor string `json:"nextCursor"`
		HasMore    bool   `json:"hasMore"`
	}
	decode := func(t *testing.T, r *httptest.ResponseRecorder) listData {
		t.Helper()
		var envelope struct {
			Data listData `json:"data"`
		}
		if err := json.Unmarshal(r.Body.Bytes(), &envelope); err != nil {
			t.Fatalf("decode list body %s: %v", r.Body.String(), err)
		}
		return envelope.Data
	}

	all := decode(t, apiJSON(t, h, http.MethodGet, "/api/v1/ssh-sessions", token, "", nil))
	if len(all.Items) != 2 || all.Items[0].ID != "webssh-api-1" || all.Items[1].ID != "webssh-api-2" {
		t.Fatalf("alice items = %+v", all.Items)
	}
	if all.Items[0].Status != string(storage.WebSSHSessionActive) || all.Items[0].RemoteServerID != server.ID {
		t.Fatalf("alice item shape = %+v", all.Items[0])
	}
	if all.HasMore || all.NextCursor != "" {
		t.Fatalf("alice page = %+v, want the final page", all)
	}

	first := decode(t, apiJSON(t, h, http.MethodGet, "/api/v1/ssh-sessions?limit=1", token, "", nil))
	if len(first.Items) != 1 || first.Items[0].ID != "webssh-api-1" || !first.HasMore || first.NextCursor == "" {
		t.Fatalf("first page = %+v", first)
	}
	second := decode(t, apiJSON(t, h, http.MethodGet, "/api/v1/ssh-sessions?limit=1&cursor="+first.NextCursor, token, "", nil))
	if len(second.Items) != 1 || second.Items[0].ID != "webssh-api-2" || second.HasMore {
		t.Fatalf("second page = %+v", second)
	}

	bobList := decode(t, apiJSON(t, h, http.MethodGet, "/api/v1/ssh-sessions", bob, "", nil))
	if len(bobList.Items) != 1 || bobList.Items[0].ID != "webssh-api-bob" {
		t.Fatalf("bob items = %+v", bobList.Items)
	}
}

type websshAuthPayload struct {
	Kind         string `json:"kind"`
	CredentialID string `json:"credentialId"`
	Password     string `json:"password"`
	PrivateKey   string `json:"privateKey"`
	Passphrase   string `json:"passphrase"`
}

func decodeWebSSHTicket(t *testing.T, response *httptest.ResponseRecorder) (string, *websshAuthPayload) {
	t.Helper()
	var payload struct {
		Data struct {
			SessionID string             `json:"sessionId"`
			Auth      *websshAuthPayload `json:"auth"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	return payload.Data.SessionID, payload.Data.Auth
}

// TestWebSSHAPICreateReturnsOneTimeAuthMaterial covers the auto-authentication
// hand-off: the secret is delivered exactly once in the create response, the
// response is no-store, the persisted idempotency record never contains
// plaintext, and an idempotent replay degrades to the manual prompt.
func TestWebSSHAPICreateReturnsOneTimeAuthMaterial(t *testing.T) {
	api, token, _ := remoteServerAPITest(t)
	setCredentialSecretStore(t, api)
	ctx := context.Background()
	if _, err := api.DB.Leases().RegisterConnection(ctx, storage.AgentLease{
		AgentID: "agent-a", ConnectionID: "conn-auth", NodeID: "agent-node-a",
		ServerNodeID: "node-a", InstanceID: "instance", ConnectionEpoch: 1, TTL: time.Minute,
	}); err != nil {
		t.Fatal(err)
	}
	owner := auth.Principal{UserID: credentialOwnerID(t, api), Username: "alice", Role: "user"}
	password, err := api.credentialService.Create(ctx, owner, CredentialInput{
		Name: "web-password", Type: storage.CredentialTypePassword,
		Secret: &CredentialSecret{Password: "host-password"}, Enabled: true,
	})
	if err != nil {
		t.Fatalf("create password credential: %v", err)
	}
	privateKeyPEM, publicKey := testEd25519PrivateKeyPEM(t, "key-pass")
	keyCredential, err := api.credentialService.Create(ctx, owner, CredentialInput{
		Name: "deploy-key", Type: storage.CredentialTypeSSHPublicKey, PublicKey: publicKey,
		Secret: &CredentialSecret{PrivateKey: privateKeyPEM, Passphrase: "key-pass"}, Enabled: true,
	})
	if err != nil {
		t.Fatalf("create key credential: %v", err)
	}
	publicOnly, err := api.credentialService.Create(ctx, owner, CredentialInput{
		Name: "public-only", Type: storage.CredentialTypeSSHPublicKey, PublicKey: validPublicKey, Enabled: true,
	})
	if err != nil {
		t.Fatalf("create public-only credential: %v", err)
	}

	api.websshService = NewWebSSHSessionService(api.DB.WebSSHSessions(), api.DB.RemoteServers(), api.DB.Credentials(), api.DB.Agents(), api.DB.Leases(), api.DB.Audits(), "node-a")
	api.websshService.SetCredentialSecrets(api.credentialService)
	handler := api.Handler()
	path := "/api/v1/remote-servers/server-a/ssh-sessions"

	create := apiJSON(t, handler, http.MethodPost, path, token, "webssh-auth", map[string]any{
		"username": "deploy", "credentialId": password.ID,
	})
	if create.Code != http.StatusCreated {
		t.Fatalf("create status = %d: %s", create.Code, create.Body.String())
	}
	if cacheControl := create.Header().Get("Cache-Control"); cacheControl != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", cacheControl)
	}
	sessionID, authPayload := decodeWebSSHTicket(t, create)
	if sessionID == "" {
		t.Fatal("session id is empty")
	}
	if authPayload == nil || authPayload.Kind != "password" || authPayload.Password != "host-password" || authPayload.CredentialID != password.ID {
		t.Fatalf("auth payload = %+v, want the stored password", authPayload)
	}

	// An idempotent replay returns the same session but no secret, and the
	// persisted record must not contain plaintext either.
	replay := apiJSON(t, handler, http.MethodPost, path, token, "webssh-auth", map[string]any{
		"username": "deploy", "credentialId": password.ID,
	})
	if replay.Code != http.StatusCreated {
		t.Fatalf("replay status = %d: %s", replay.Code, replay.Body.String())
	}
	replaySessionID, replayAuth := decodeWebSSHTicket(t, replay)
	if replaySessionID != sessionID || replayAuth != nil {
		t.Fatalf("replay = %s/%+v, want the same session without auth material", replaySessionID, replayAuth)
	}
	record, err := api.DB.Idempotency().Get(ctx, "webssh-auth")
	if err != nil {
		t.Fatalf("idempotency record: %v", err)
	}
	if strings.Contains(record.Response, "host-password") {
		t.Fatal("idempotency record persisted the plaintext password")
	}

	keyResponse := apiJSON(t, handler, http.MethodPost, path, token, "", map[string]any{
		"username": "deploy", "credentialId": keyCredential.ID,
	})
	if keyResponse.Code != http.StatusCreated {
		t.Fatalf("key create status = %d: %s", keyResponse.Code, keyResponse.Body.String())
	}
	_, keyAuth := decodeWebSSHTicket(t, keyResponse)
	if keyAuth == nil || keyAuth.Kind != "private_key" || keyAuth.PrivateKey != privateKeyPEM || keyAuth.Passphrase != "key-pass" || keyAuth.Password != "" {
		t.Fatalf("key auth payload = %+v, want the stored private key", keyAuth)
	}

	// A public-key credential without a stored private key cannot auto-authenticate.
	fallback := apiJSON(t, handler, http.MethodPost, path, token, "", map[string]any{
		"username": "deploy", "credentialId": publicOnly.ID,
	})
	if fallback.Code != http.StatusCreated {
		t.Fatalf("fallback status = %d: %s", fallback.Code, fallback.Body.String())
	}
	if _, fallbackAuth := decodeWebSSHTicket(t, fallback); fallbackAuth != nil {
		t.Fatalf("fallback auth = %+v, want nil", fallbackAuth)
	}

	// No credential at all keeps the existing manual-prompt behaviour.
	manual := apiJSON(t, handler, http.MethodPost, path, token, "", map[string]any{"username": "deploy"})
	if manual.Code != http.StatusCreated {
		t.Fatalf("manual status = %d: %s", manual.Code, manual.Body.String())
	}
	if _, manualAuth := decodeWebSSHTicket(t, manual); manualAuth != nil {
		t.Fatalf("manual auth = %+v, want nil", manualAuth)
	}

	audits, err := api.DB.Audits().List(ctx, storage.AuditFilter{ResourceID: password.ID}, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	revealed := 0
	for _, entry := range audits.Items {
		if entry.Action != "credential.revealed" {
			continue
		}
		revealed++
		if strings.Contains(entry.Details, "host-password") {
			t.Fatalf("audit details contain the secret: %s", entry.Details)
		}
	}
	if revealed == 0 {
		t.Fatalf("credential.revealed audit is missing: %+v", audits.Items)
	}
}

// TestWebSSHAPICreateWithoutSecretResolverFallsBack proves that a deployment
// without TUNNELMESH_TOKEN_ENCRYPTION_KEY still issues tickets; only the
// auto-authentication payload is absent.
func TestWebSSHAPICreateWithoutSecretResolverFallsBack(t *testing.T) {
	api, token, _ := remoteServerAPITest(t)
	ctx := context.Background()
	if _, err := api.DB.Leases().RegisterConnection(ctx, storage.AgentLease{
		AgentID: "agent-a", ConnectionID: "conn-noauth", NodeID: "agent-node-a",
		ServerNodeID: "node-a", InstanceID: "instance", ConnectionEpoch: 1, TTL: time.Minute,
	}); err != nil {
		t.Fatal(err)
	}
	api.websshService = NewWebSSHSessionService(api.DB.WebSSHSessions(), api.DB.RemoteServers(), api.DB.Credentials(), api.DB.Agents(), api.DB.Leases(), api.DB.Audits(), "node-a")
	handler := api.Handler()

	response := apiJSON(t, handler, http.MethodPost, "/api/v1/remote-servers/server-a/ssh-sessions", token, "", map[string]any{"username": "deploy"})
	if response.Code != http.StatusCreated {
		t.Fatalf("create status = %d: %s", response.Code, response.Body.String())
	}
	if _, authPayload := decodeWebSSHTicket(t, response); authPayload != nil {
		t.Fatalf("auth payload = %+v, want nil without a secret resolver", authPayload)
	}
}
