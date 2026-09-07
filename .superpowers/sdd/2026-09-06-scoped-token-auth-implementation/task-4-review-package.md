# Task 4 review package

Base HEAD: `base_head=163fe12121d2f839ea4bf4907f55a8e7dd835057`

## Changed files

```text
Files .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-4-before/docs/api/openapi.yaml and .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-4-after/docs/api/openapi.yaml differ
Files .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-4-before/internal/server/api.go and .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-4-after/internal/server/api.go differ
Only in .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-4-after/internal/server: token_api.go
Only in .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-4-after/internal/server: token_api_test.go
Only in .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-4-after/internal/server: token_service.go
Files .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-4-before/internal/storage/db.go and .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-4-after/internal/storage/db.go differ
Files .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-4-before/internal/storage/repository.go and .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-4-after/internal/storage/repository.go differ
Files .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-4-before/internal/storage/service_token_test.go and .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-4-after/internal/storage/service_token_test.go differ
```

## Full task-only diff

```diff
diff -ruN --exclude=*.__ABSENT__ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-4-before/docs/api/openapi.yaml .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-4-after/docs/api/openapi.yaml
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-4-before/docs/api/openapi.yaml	2026-09-06 10:41:38
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-4-after/docs/api/openapi.yaml	2026-09-06 15:41:34
@@ -81,6 +81,80 @@
     get: {security: [{bearerAuth: []}], responses: {'200': {description: Tunnel}}}
     put: {security: [{bearerAuth: []}], requestBody: {content: {application/json: {schema: {$ref: '#/components/schemas/TunnelRequest'}}}}, responses: {'200': {description: Updated}}}
     delete: {security: [{bearerAuth: []}], responses: {'200': {description: Deleted}}}
+  /api/v1/tokens:
+    get:
+      summary: List scoped service Token metadata
+      description: Admins may filter all Tokens by owner and type. Non-admin callers can list only Tokens they own. Stored hashes and issued secrets are never returned. Status is derived from lifecycle timestamps and current owner/resource availability.
+      security: [{bearerAuth: []}]
+      parameters:
+        - {$ref: '#/components/parameters/TokenTypeFilter'}
+        - {$ref: '#/components/parameters/TokenOwnerFilter'}
+        - {$ref: '#/components/parameters/Cursor'}
+        - {$ref: '#/components/parameters/Limit'}
+      responses:
+        '200': {description: Cursor page of redacted Token metadata, content: {application/json: {schema: {$ref: '#/components/schemas/TokenPageEnvelope'}}}}
+        '400': {description: Invalid Token type or pagination input}
+        '401': {description: Missing or invalid management bearer Token}
+        '403': {description: A non-admin requested another owner's Tokens}
+    post:
+      summary: Create a scoped service Token
+      description: |
+        The plaintext `secret` is returned exactly once on the first successful request and is never stored recoverably. Reusing the same `Idempotency-Key` with the same request returns the same Token ID and metadata with `secret` omitted and `replayed=true`. Reusing a key for another request, or observing a request still in progress, returns 409.
+
+        Admins may create `agent`, `client`, and `server_node` Tokens. Non-admin owners may create an `agent` Token only for an enabled Agent they own, or a `client` Token whose scoped Agents are all enabled and owned by them. Non-admin callers cannot create `server_node` Tokens.
+      security: [{bearerAuth: []}]
+      parameters: [{$ref: '#/components/parameters/IdempotencyKey'}]
+      requestBody:
+        required: true
+        content:
+          application/json:
+            schema: {$ref: '#/components/schemas/TokenCreateRequest'}
+      responses:
+        '201': {description: Token created, or safely replayed without its secret, content: {application/json: {schema: {$ref: '#/components/schemas/TokenMutationEnvelope'}}}}
+        '400': {description: Invalid type, binding, expiry, scope, JSON, or Idempotency-Key}
+        '401': {description: Missing or invalid management bearer Token}
+        '403': {description: Owner/resource authorization denied}
+        '409': {description: Idempotency-Key conflict or request in progress}
+  /api/v1/tokens/{tokenId}:
+    parameters:
+      - {name: tokenId, in: path, required: true, schema: {type: string}}
+    get:
+      summary: Read redacted scoped service Token metadata
+      description: Returns dynamically derived status without the Token hash or plaintext secret.
+      security: [{bearerAuth: []}]
+      responses:
+        '200': {description: Redacted Token metadata, content: {application/json: {schema: {$ref: '#/components/schemas/TokenMetadataEnvelope'}}}}
+        '401': {description: Missing or invalid management bearer Token}
+        '403': {description: Token is not owned by the non-admin caller}
+        '404': {description: Token not found}
+  /api/v1/tokens/{tokenId}/rotate:
+    parameters:
+      - {name: tokenId, in: path, required: true, schema: {type: string}}
+      - {$ref: '#/components/parameters/IdempotencyKey'}
+    post:
+      summary: Atomically rotate and revoke a scoped service Token
+      description: Creates a replacement and revokes the old Token in one transaction. The replacement secret is returned once; an idempotent replay returns the same replacement metadata with no secret and `replayed=true`.
+      security: [{bearerAuth: []}]
+      responses:
+        '201': {description: Replacement Token created, or safely replayed, content: {application/json: {schema: {$ref: '#/components/schemas/TokenMutationEnvelope'}}}}
+        '400': {description: Invalid Idempotency-Key}
+        '401': {description: Missing or invalid management bearer Token}
+        '403': {description: Token is not owned by the non-admin caller}
+        '404': {description: Token not found}
+        '409': {description: Token cannot be rotated or idempotency request conflicts/is in progress}
+  /api/v1/tokens/{tokenId}/revoke:
+    parameters:
+      - {name: tokenId, in: path, required: true, schema: {type: string}}
+    post:
+      summary: Revoke a scoped service Token
+      description: Revocation is idempotent and returns redacted metadata with dynamically derived `revoked` status.
+      security: [{bearerAuth: []}]
+      responses:
+        '200': {description: Token revoked, content: {application/json: {schema: {$ref: '#/components/schemas/TokenMetadataEnvelope'}}}}
+        '401': {description: Missing or invalid management bearer Token}
+        '403': {description: Token is not owned by the non-admin caller}
+        '404': {description: Token not found}
+        '409': {description: Concurrent lifecycle conflict}
   /api/v1/audit-logs:
     get: {security: [{bearerAuth: []}], responses: {'200': {description: Audit records}}}
 components:
@@ -91,6 +165,8 @@
     Limit: {name: limit, in: query, schema: {type: integer, minimum: 1, maximum: 500}}
     IncludeStale: {name: includeStale, in: query, description: Include an expired or explicitly stale snapshot, schema: {type: boolean, default: false}}
     IdempotencyKey: {name: Idempotency-Key, in: header, schema: {type: string, maxLength: 255}}
+    TokenTypeFilter: {name: type, in: query, schema: {type: string, enum: [agent, client, server_node]}}
+    TokenOwnerFilter: {name: owner, in: query, description: Owner user ID; admin-only when it differs from the current user, schema: {type: string}}
   schemas:
     Envelope: {type: object, required: [code, msg, data], properties: {code: {type: integer}, msg: {type: string}, data: {type: object}}}
     LoginRequest: {type: object, required: [username, password], properties: {username: {type: string}, password: {type: string, format: password}}}
@@ -123,3 +199,74 @@
         redacted: {type: boolean}
     PolicyRequest: {type: object, required: [targetHost, targetPort], properties: {targetHost: {type: string}, targetPort: {type: integer, minimum: 1, maximum: 65535}, protocol: {type: string}, allowedCIDRs: {type: array, items: {type: string}}, allowedPorts: {type: array, items: {type: integer}}}}
     TunnelRequest: {type: object, required: [agentId, targetHost, targetPort], properties: {agentId: {type: string}, protocol: {type: string}, domain: {type: string}, pathPrefix: {type: string}, targetHost: {type: string}, targetPort: {type: integer, minimum: 1, maximum: 65535}, publicPort: {type: integer}, status: {type: string}, config: {type: object}}}
+    TokenScope:
+      type: object
+      additionalProperties: false
+      properties:
+        agentIds: {type: array, maxItems: 128, uniqueItems: true, items: {type: string, maxLength: 255}}
+        protocols: {type: array, maxItems: 4, uniqueItems: true, items: {type: string, enum: [tcp, udp, http, ws, websocket]}}
+        targetCIDRs: {type: array, maxItems: 128, uniqueItems: true, items: {type: string, description: IPv4 or IPv6 CIDR}}
+        targetPorts: {type: array, maxItems: 1024, uniqueItems: true, items: {type: integer, minimum: 1, maximum: 65535}}
+    TokenCreateRequest:
+      type: object
+      additionalProperties: false
+      required: [type]
+      properties:
+        type: {type: string, enum: [agent, client, server_node]}
+        ownerUserId: {type: string, description: Defaults to the authenticated management user; only admins may name another owner}
+        agentId: {type: string, description: Required only for agent Tokens}
+        nodeId: {type: string, description: Required only for server_node Tokens}
+        scope:
+          allOf: [{$ref: '#/components/schemas/TokenScope'}]
+          description: Optional narrowing scope; omission is equivalent to an empty scope
+        expiresAt: {type: string, format: date-time, description: Optional future expiration time}
+    TokenMetadata:
+      type: object
+      additionalProperties: false
+      required: [id, type, ownerUserId, prefix, scope, status, createdAt, updatedAt, replayed]
+      properties:
+        id: {type: string}
+        type: {type: string, enum: [agent, client, server_node]}
+        ownerUserId: {type: string}
+        agentId: {type: string}
+        nodeId: {type: string}
+        prefix: {type: string, description: Short non-secret display prefix}
+        scope: {$ref: '#/components/schemas/TokenScope'}
+        status: {type: string, enum: [active, revoked, expired, unavailable], description: Derived from timestamps and current owner/bound-resource state}
+        expiresAt: {type: string, format: date-time}
+        revokedAt: {type: string, format: date-time}
+        lastUsedAt: {type: string, format: date-time}
+        createdAt: {type: string, format: date-time}
+        updatedAt: {type: string, format: date-time}
+        replayed: {type: boolean, description: True only for an idempotent create/rotate replay}
+    TokenMutation:
+      allOf:
+        - {$ref: '#/components/schemas/TokenMetadata'}
+        - type: object
+          properties:
+            secret: {type: string, readOnly: true, description: One-time plaintext credential; present only on the first successful create or rotate response and never on replay}
+    TokenMetadataEnvelope:
+      allOf:
+        - {$ref: '#/components/schemas/Envelope'}
+        - type: object
+          properties:
+            data: {$ref: '#/components/schemas/TokenMetadata'}
+    TokenMutationEnvelope:
+      allOf:
+        - {$ref: '#/components/schemas/Envelope'}
+        - type: object
+          properties:
+            data: {$ref: '#/components/schemas/TokenMutation'}
+    TokenPage:
+      type: object
+      required: [items, nextCursor, hasMore]
+      properties:
+        items: {type: array, items: {$ref: '#/components/schemas/TokenMetadata'}}
+        nextCursor: {type: string}
+        hasMore: {type: boolean}
+    TokenPageEnvelope:
+      allOf:
+        - {$ref: '#/components/schemas/Envelope'}
+        - type: object
+          properties:
+            data: {$ref: '#/components/schemas/TokenPage'}
diff -ruN --exclude=*.__ABSENT__ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-4-before/internal/server/api.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-4-after/internal/server/api.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-4-before/internal/server/api.go	2026-09-06 10:41:00
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-4-after/internal/server/api.go	2026-09-06 15:41:34
@@ -22,16 +22,17 @@
 // storage interfaces so the handler remains usable with SQLite, MySQL, and
 // small in-memory fakes in tests.
 type API struct {
-	DB       *storage.DB
-	Auth     *auth.AuthService
-	users    storage.UserRepository
-	agents   storage.AgentRepository
-	policies storage.PolicyRepository
-	tunnels  storage.TunnelRepository
-	audits   storage.AuditRepository
-	idem     storage.IdempotencyRepository
-	routeMu  sync.Mutex
-	service  *apiService
+	DB           *storage.DB
+	Auth         *auth.AuthService
+	users        storage.UserRepository
+	agents       storage.AgentRepository
+	policies     storage.PolicyRepository
+	tunnels      storage.TunnelRepository
+	audits       storage.AuditRepository
+	idem         storage.IdempotencyRepository
+	routeMu      sync.Mutex
+	service      *apiService
+	tokenService *TokenService
 }
 
 // apiService is the application layer between HTTP handlers and storage. It
@@ -57,6 +58,7 @@
 	if db != nil {
 		a.users, a.agents, a.policies, a.tunnels, a.audits, a.idem = db.Users(), db.Agents(), db.Policies(), db.Tunnels(), db.Audits(), db.Idempotency()
 		a.service = &apiService{agents: a.agents, metadata: NewAgentMetadataService(db.Metadata()), policies: a.policies, tunnels: a.tunnels, audits: a.audits}
+		a.tokenService = NewTokenService(db)
 	}
 	return a
 }
@@ -233,6 +235,8 @@
 		a.handleTunnels(w, r, p, parts[1:])
 	case "audit-logs", "audits":
 		a.handleAudits(w, r, p)
+	case "tokens":
+		a.handleTokens(w, r, p, parts[1:])
 	default:
 		writeAPIError(w, http.StatusNotFound, "not found")
 	}
diff -ruN --exclude=*.__ABSENT__ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-4-before/internal/server/token_api.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-4-after/internal/server/token_api.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-4-before/internal/server/token_api.go	1970-01-01 08:00:00
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-4-after/internal/server/token_api.go	2026-09-06 15:41:34
@@ -0,0 +1,211 @@
+package server
+
+import (
+	"database/sql"
+	"errors"
+	"net/http"
+	"strings"
+	"time"
+
+	"github.com/tunnelmesh/tunnelmesh/internal/auth"
+	"github.com/tunnelmesh/tunnelmesh/internal/storage"
+)
+
+// handleTokens routes only the versioned management Token collection and
+// resource actions after management-session authentication.
+func (a *API) handleTokens(w http.ResponseWriter, r *http.Request, principal auth.Principal, parts []string) {
+	if a.tokenService == nil {
+		writeAPIError(w, http.StatusServiceUnavailable, "token service unavailable")
+		return
+	}
+	if len(parts) == 0 {
+		switch r.Method {
+		case http.MethodGet:
+			a.listTokens(w, r, principal)
+		case http.MethodPost:
+			a.createToken(w, r, principal)
+		default:
+			writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
+		}
+		return
+	}
+	record, view, err := a.tokenService.Get(r.Context(), parts[0])
+	if err != nil {
+		writeTokenError(w, err)
+		return
+	}
+	if !isAdmin(principal) && record.OwnerUserID != principal.UserID {
+		a.denyToken(r, principal, record, "token_not_owned")
+		writeAPIError(w, http.StatusForbidden, "forbidden")
+		return
+	}
+	if len(parts) == 1 {
+		if r.Method != http.MethodGet {
+			writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
+			return
+		}
+		writeJSON(w, http.StatusOK, view)
+		return
+	}
+	if len(parts) != 2 || r.Method != http.MethodPost {
+		writeAPIError(w, http.StatusNotFound, "not found")
+		return
+	}
+	switch parts[1] {
+	case "rotate":
+		key, ok := tokenIdempotencyKey(w, r)
+		if !ok {
+			return
+		}
+		rotated, err := a.tokenService.Rotate(r.Context(), principal.UserID, record.ID, key)
+		if err != nil {
+			writeTokenError(w, err)
+			return
+		}
+		writeJSON(w, http.StatusCreated, rotated)
+	case "revoke":
+		revoked, err := a.tokenService.Revoke(r.Context(), principal.UserID, record.ID)
+		if err != nil {
+			writeTokenError(w, err)
+			return
+		}
+		writeJSON(w, http.StatusOK, revoked)
+	default:
+		writeAPIError(w, http.StatusNotFound, "not found")
+	}
+}
+
+// listTokens fixes non-admin filtering to the caller before repository paging.
+func (a *API) listTokens(w http.ResponseWriter, r *http.Request, principal auth.Principal) {
+	tokenType := storage.TokenType(strings.TrimSpace(r.URL.Query().Get("type")))
+	if tokenType != "" && !validManagementTokenType(tokenType) {
+		writeAPIError(w, http.StatusBadRequest, "invalid token type")
+		return
+	}
+	owner := strings.TrimSpace(r.URL.Query().Get("owner"))
+	if !isAdmin(principal) {
+		if owner != "" && owner != principal.UserID {
+			a.denyToken(r, principal, storage.ServiceToken{Type: tokenType, OwnerUserID: owner}, "owner_filter_forbidden")
+			writeAPIError(w, http.StatusForbidden, "forbidden")
+			return
+		}
+		owner = principal.UserID
+	}
+	page, err := a.tokenService.List(r.Context(), storage.ServiceTokenFilter{OwnerUserID: owner, Type: tokenType}, r.URL.Query().Get("cursor"), queryLimit(r))
+	if err != nil {
+		writeTokenError(w, err)
+		return
+	}
+	items := make([]any, len(page.Items))
+	for index := range page.Items {
+		items[index] = page.Items[index]
+	}
+	writeJSON(w, http.StatusOK, pageData(items, page.NextCursor, page.HasMore))
+}
+
+// createToken owns transport decoding and caller/resource authorization; the
+// service owns lifecycle validation and transaction orchestration.
+func (a *API) createToken(w http.ResponseWriter, r *http.Request, principal auth.Principal) {
+	var request struct {
+		Type        storage.TokenType `json:"type"`
+		OwnerUserID string            `json:"ownerUserId"`
+		AgentID     string            `json:"agentId"`
+		NodeID      string            `json:"nodeId"`
+		Scope       auth.TokenScope   `json:"scope"`
+		ExpiresAt   *time.Time        `json:"expiresAt"`
+	}
+	if err := decodeJSON(r, &request); err != nil {
+		writeAPIError(w, http.StatusBadRequest, "invalid JSON or expiresAt")
+		return
+	}
+	request.OwnerUserID = strings.TrimSpace(request.OwnerUserID)
+	request.AgentID = strings.TrimSpace(request.AgentID)
+	request.NodeID = strings.TrimSpace(request.NodeID)
+	if !validManagementTokenType(request.Type) {
+		writeAPIError(w, http.StatusBadRequest, "invalid token type")
+		return
+	}
+	if request.OwnerUserID == "" {
+		request.OwnerUserID = principal.UserID
+	}
+	requested := storage.ServiceToken{Type: request.Type, OwnerUserID: request.OwnerUserID, AgentID: request.AgentID, NodeID: request.NodeID}
+	if !isAdmin(principal) {
+		if request.OwnerUserID != principal.UserID {
+			a.denyToken(r, principal, requested, "owner_not_self")
+			writeAPIError(w, http.StatusForbidden, "forbidden")
+			return
+		}
+		if request.Type == storage.TokenTypeServerNode {
+			a.denyToken(r, principal, requested, "server_node_admin_required")
+			writeAPIError(w, http.StatusForbidden, "admin role required")
+			return
+		}
+		if request.Type == storage.TokenTypeAgent && !a.callerOwnsEnabledAgent(r, principal, request.AgentID, requested, "agent_not_owned_or_enabled") {
+			writeAPIError(w, http.StatusForbidden, "forbidden")
+			return
+		}
+		if request.Type == storage.TokenTypeClient {
+			for _, agentID := range request.Scope.AgentIDs {
+				requested.AgentID = strings.TrimSpace(agentID)
+				if !a.callerOwnsEnabledAgent(r, principal, requested.AgentID, requested, "scoped_agent_not_owned_or_enabled") {
+					writeAPIError(w, http.StatusForbidden, "forbidden")
+					return
+				}
+			}
+		}
+	}
+	key, ok := tokenIdempotencyKey(w, r)
+	if !ok {
+		return
+	}
+	created, err := a.tokenService.Create(r.Context(), principal.UserID, key, auth.CreateTokenInput{
+		Type: request.Type, OwnerUserID: request.OwnerUserID, AgentID: request.AgentID, NodeID: request.NodeID,
+		Scope: request.Scope, ExpiresAt: request.ExpiresAt,
+	})
+	if err != nil {
+		writeTokenError(w, err)
+		return
+	}
+	writeJSON(w, http.StatusCreated, created)
+}
+
+// callerOwnsEnabledAgent performs the non-admin resource boundary check.
+func (a *API) callerOwnsEnabledAgent(r *http.Request, principal auth.Principal, agentID string, requested storage.ServiceToken, reason string) bool {
+	agent, err := a.tokenService.GetAgent(r.Context(), strings.TrimSpace(agentID))
+	if err == nil && agent.Enabled && agent.OwnerUserID == principal.UserID {
+		return true
+	}
+	a.denyToken(r, principal, requested, reason)
+	return false
+}
+
+func (a *API) denyToken(r *http.Request, principal auth.Principal, record storage.ServiceToken, reason string) {
+	_ = a.tokenService.RecordAuthorizationDenied(r.Context(), principal.UserID, record, reason)
+}
+
+// tokenIdempotencyKey enforces the storage contract's bounded key size.
+func tokenIdempotencyKey(w http.ResponseWriter, r *http.Request) (string, bool) {
+	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
+	if len(key) > 255 {
+		writeAPIError(w, http.StatusBadRequest, "Idempotency-Key is too long")
+		return "", false
+	}
+	return key, true
+}
+
+func validManagementTokenType(tokenType storage.TokenType) bool {
+	return tokenType == storage.TokenTypeAgent || tokenType == storage.TokenTypeClient || tokenType == storage.TokenTypeServerNode
+}
+
+func writeTokenError(w http.ResponseWriter, err error) {
+	switch {
+	case errors.Is(err, sql.ErrNoRows):
+		writeAPIError(w, http.StatusNotFound, "token not found")
+	case errors.Is(err, errInvalidTokenRequest):
+		writeAPIError(w, http.StatusBadRequest, err.Error())
+	case errors.Is(err, errTokenConflict), errors.Is(err, errIdempotencyConflict), errors.Is(err, errIdempotencyInProgress), errors.Is(err, storage.ErrServiceTokenRevoked):
+		writeAPIError(w, http.StatusConflict, err.Error())
+	default:
+		writeStorageError(w, err)
+	}
+}
diff -ruN --exclude=*.__ABSENT__ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-4-before/internal/server/token_api_test.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-4-after/internal/server/token_api_test.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-4-before/internal/server/token_api_test.go	1970-01-01 08:00:00
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-4-after/internal/server/token_api_test.go	2026-09-06 15:41:34
@@ -0,0 +1,503 @@
+package server
+
+import (
+	"context"
+	"encoding/json"
+	"fmt"
+	"net/http"
+	"reflect"
+	"strings"
+	"sync"
+	"testing"
+	"time"
+
+	"github.com/tunnelmesh/tunnelmesh/internal/auth"
+	"github.com/tunnelmesh/tunnelmesh/internal/storage"
+)
+
+type tokenAPIFixture struct {
+	api        *API
+	admin      storage.User
+	owner      storage.User
+	other      storage.User
+	adminToken string
+	ownerToken string
+	otherToken string
+}
+
+func newTokenAPIFixture(t *testing.T) tokenAPIFixture {
+	t.Helper()
+	api, admin, owner := apiTestServer(t)
+	authn := auth.NewAuthService(api.DB)
+	other, err := authn.CreateUser(context.Background(), "bob", "bob-pass", "user")
+	if err != nil {
+		t.Fatal(err)
+	}
+	for _, agent := range []storage.Agent{
+		{ID: "agent-alice-a", Name: "alice-a", OwnerUserID: owner.ID, Enabled: true},
+		{ID: "agent-alice-b", Name: "alice-b", OwnerUserID: owner.ID, Enabled: true},
+		{ID: "agent-alice-disabled", Name: "alice-disabled", OwnerUserID: owner.ID, Enabled: false},
+		{ID: "agent-bob", Name: "bob", OwnerUserID: other.ID, Enabled: true},
+	} {
+		if err := api.DB.Agents().Create(context.Background(), agent); err != nil {
+			t.Fatalf("create agent %s: %v", agent.ID, err)
+		}
+	}
+	if err := api.DB.Nodes().Create(context.Background(), storage.ServerNode{ID: "node-a", Address: "127.0.0.1:9443", Epoch: 1}); err != nil {
+		t.Fatal(err)
+	}
+	return tokenAPIFixture{
+		api: api, admin: admin, owner: owner, other: other,
+		adminToken: apiToken(t, api, admin.Username, "admin-pass"),
+		ownerToken: apiToken(t, api, owner.Username, "alice-pass"),
+		otherToken: apiToken(t, api, other.Username, "bob-pass"),
+	}
+}
+
+func tokenResponseData(t *testing.T, responseBody []byte) map[string]any {
+	t.Helper()
+	var envelope struct {
+		Data map[string]any `json:"data"`
+	}
+	if err := json.Unmarshal(responseBody, &envelope); err != nil {
+		t.Fatalf("decode response %s: %v", responseBody, err)
+	}
+	return envelope.Data
+}
+
+func tokenIDFromResponse(t *testing.T, responseBody []byte) string {
+	t.Helper()
+	id, _ := tokenResponseData(t, responseBody)["id"].(string)
+	if id == "" {
+		t.Fatalf("response has no token id: %s", responseBody)
+	}
+	return id
+}
+
+func withoutMutationOnlyFields(in map[string]any) map[string]any {
+	out := make(map[string]any, len(in))
+	for key, value := range in {
+		if key != "secret" && key != "replayed" {
+			out[key] = value
+		}
+	}
+	return out
+}
+
+func assertNoSensitiveFields(t *testing.T, value any) {
+	t.Helper()
+	var walk func(any)
+	walk = func(current any) {
+		switch typed := current.(type) {
+		case map[string]any:
+			for key, child := range typed {
+				lower := strings.ToLower(key)
+				if lower == "secret" || lower == "hash" || strings.Contains(lower, "tokenhash") || strings.Contains(lower, "token_hash") {
+					t.Fatalf("sensitive field %q present in %+v", key, value)
+				}
+				walk(child)
+			}
+		case []any:
+			for _, child := range typed {
+				walk(child)
+			}
+		}
+	}
+	walk(value)
+}
+
+func TestTokenAPIRequiresManagementAuthentication(t *testing.T) {
+	fixture := newTokenAPIFixture(t)
+	response := apiJSON(t, fixture.api, http.MethodGet, "/api/v1/tokens", "", "", nil)
+	if response.Code != http.StatusUnauthorized {
+		t.Fatalf("unauthenticated token list status = %d, want 401: %s", response.Code, response.Body.String())
+	}
+}
+
+func TestTokenAPIAdminCreatesAllTypesAndReplaysWithoutSecret(t *testing.T) {
+	fixture := newTokenAPIFixture(t)
+	tests := []struct {
+		name string
+		key  string
+		body map[string]any
+	}{
+		{name: "agent", key: "admin-agent-token", body: map[string]any{"type": "agent", "ownerUserId": fixture.owner.ID, "agentId": "agent-alice-a", "scope": map[string]any{"agentIds": []string{"agent-alice-a"}, "protocols": []string{"tcp"}}}},
+		{name: "client", key: "admin-client-token", body: map[string]any{"type": "client", "ownerUserId": fixture.owner.ID, "scope": map[string]any{"agentIds": []string{"agent-alice-a", "agent-alice-b"}, "targetPorts": []int{22}}}},
+		{name: "server_node", key: "admin-node-token", body: map[string]any{"type": "server_node", "ownerUserId": fixture.admin.ID, "nodeId": "node-a", "scope": map[string]any{}}},
+	}
+	for _, tt := range tests {
+		t.Run(tt.name, func(t *testing.T) {
+			first := apiJSON(t, fixture.api, http.MethodPost, "/api/v1/tokens", fixture.adminToken, tt.key, tt.body)
+			if first.Code != http.StatusCreated {
+				t.Fatalf("create %s token status = %d: %s", tt.name, first.Code, first.Body.String())
+			}
+			firstData := tokenResponseData(t, first.Body.Bytes())
+			secret, _ := firstData["secret"].(string)
+			if secret == "" || firstData["replayed"] != false || firstData["type"] != tt.name || firstData["status"] != "active" {
+				t.Fatalf("unexpected first create data: %+v", firstData)
+			}
+
+			id := tokenIDFromResponse(t, first.Body.Bytes())
+			record, err := fixture.api.DB.ServiceTokens().Get(context.Background(), id)
+			if err != nil {
+				t.Fatal(err)
+			}
+			if record.TokenHash == "" || record.TokenHash == secret || record.Prefix == secret || strings.Contains(record.Scope, secret) {
+				t.Fatalf("stored token contains clear secret: %+v", record)
+			}
+			idem, err := fixture.api.DB.Idempotency().Get(context.Background(), tt.key)
+			if err != nil {
+				t.Fatal(err)
+			}
+			if strings.Contains(idem.Response, secret) {
+				t.Fatalf("idempotency response leaked secret: %s", idem.Response)
+			}
+			var stored any
+			if err := json.Unmarshal([]byte(idem.Response), &stored); err != nil {
+				t.Fatalf("decode idempotency response: %v", err)
+			}
+			assertNoSensitiveFields(t, stored)
+
+			replay := apiJSON(t, fixture.api, http.MethodPost, "/api/v1/tokens", fixture.adminToken, tt.key, tt.body)
+			if replay.Code != first.Code {
+				t.Fatalf("replay status = %d, want %d: %s", replay.Code, first.Code, replay.Body.String())
+			}
+			replayData := tokenResponseData(t, replay.Body.Bytes())
+			if _, present := replayData["secret"]; present {
+				t.Fatalf("replay returned secret: %+v", replayData)
+			}
+			if replayData["replayed"] != true || replayData["id"] != id {
+				t.Fatalf("unexpected replay data: %+v", replayData)
+			}
+			if !reflect.DeepEqual(withoutMutationOnlyFields(firstData), withoutMutationOnlyFields(replayData)) {
+				t.Fatalf("replay metadata changed:\nfirst=%+v\nreplay=%+v", firstData, replayData)
+			}
+		})
+	}
+}
+
+func TestTokenAPIOwnerAuthorizationAndValidation(t *testing.T) {
+	fixture := newTokenAPIFixture(t)
+	successes := []struct {
+		key  string
+		body map[string]any
+	}{
+		{key: "owner-agent", body: map[string]any{"type": "agent", "agentId": "agent-alice-a", "scope": map[string]any{"agentIds": []string{"agent-alice-a"}}}},
+		{key: "owner-client", body: map[string]any{"type": "client", "scope": map[string]any{"agentIds": []string{"agent-alice-a", "agent-alice-b"}}}},
+	}
+	for _, tc := range successes {
+		response := apiJSON(t, fixture.api, http.MethodPost, "/api/v1/tokens", fixture.ownerToken, tc.key, tc.body)
+		if response.Code != http.StatusCreated {
+			t.Fatalf("owner create status = %d: %s", response.Code, response.Body.String())
+		}
+		if got := tokenResponseData(t, response.Body.Bytes())["ownerUserId"]; got != fixture.owner.ID {
+			t.Fatalf("ownerUserId = %v, want %s", got, fixture.owner.ID)
+		}
+	}
+
+	denials := []struct {
+		name string
+		body map[string]any
+	}{
+		{name: "another owner", body: map[string]any{"type": "client", "ownerUserId": fixture.other.ID, "scope": map[string]any{}}},
+		{name: "foreign agent binding", body: map[string]any{"type": "agent", "agentId": "agent-bob", "scope": map[string]any{"agentIds": []string{"agent-bob"}}}},
+		{name: "disabled agent binding", body: map[string]any{"type": "agent", "agentId": "agent-alice-disabled", "scope": map[string]any{"agentIds": []string{"agent-alice-disabled"}}}},
+		{name: "foreign client scope", body: map[string]any{"type": "client", "scope": map[string]any{"agentIds": []string{"agent-bob"}}}},
+		{name: "server node", body: map[string]any{"type": "server_node", "nodeId": "node-a", "scope": map[string]any{}}},
+	}
+	for index, tc := range denials {
+		t.Run(tc.name, func(t *testing.T) {
+			response := apiJSON(t, fixture.api, http.MethodPost, "/api/v1/tokens", fixture.ownerToken, fmt.Sprintf("owner-denial-%d", index), tc.body)
+			if response.Code != http.StatusForbidden {
+				t.Fatalf("denial status = %d, want 403: %s", response.Code, response.Body.String())
+			}
+		})
+	}
+
+	expired := apiJSON(t, fixture.api, http.MethodPost, "/api/v1/tokens", fixture.ownerToken, "owner-expired", map[string]any{
+		"type": "client", "scope": map[string]any{}, "expiresAt": time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano),
+	})
+	if expired.Code != http.StatusBadRequest {
+		t.Fatalf("expired token status = %d, want 400: %s", expired.Code, expired.Body.String())
+	}
+	invalidScope := apiJSON(t, fixture.api, http.MethodPost, "/api/v1/tokens", fixture.ownerToken, "owner-invalid-scope", map[string]any{
+		"type": "client", "scope": map[string]any{"protocols": []string{"smtp"}},
+	})
+	if invalidScope.Code != http.StatusBadRequest {
+		t.Fatalf("invalid scope status = %d, want 400: %s", invalidScope.Code, invalidScope.Body.String())
+	}
+
+	audits, err := fixture.api.DB.Audits().List(context.Background(), "", 100)
+	if err != nil {
+		t.Fatal(err)
+	}
+	denied := 0
+	for _, audit := range audits.Items {
+		if audit.Action == "token.authorization_denied" && audit.ActorUserID == fixture.owner.ID {
+			denied++
+			assertSafeTokenAuditDetails(t, audit.Details)
+		}
+	}
+	if denied != len(denials) {
+		t.Fatalf("authorization denied audits = %d, want %d: %+v", denied, len(denials), audits.Items)
+	}
+}
+
+func TestTokenAPIPaginationLifecycleRedactionAndAudit(t *testing.T) {
+	fixture := newTokenAPIFixture(t)
+	createdIDs := make([]string, 0, 3)
+	for index, agentID := range []string{"agent-alice-a", "agent-alice-b", "agent-alice-a"} {
+		response := apiJSON(t, fixture.api, http.MethodPost, "/api/v1/tokens", fixture.ownerToken, fmt.Sprintf("owner-page-%d", index), map[string]any{
+			"type": "client", "scope": map[string]any{"agentIds": []string{agentID}},
+		})
+		if response.Code != http.StatusCreated {
+			t.Fatalf("create paged token %d status = %d: %s", index, response.Code, response.Body.String())
+		}
+		createdIDs = append(createdIDs, tokenIDFromResponse(t, response.Body.Bytes()))
+	}
+	foreign := apiJSON(t, fixture.api, http.MethodPost, "/api/v1/tokens", fixture.otherToken, "foreign-page", map[string]any{
+		"type": "client", "scope": map[string]any{"agentIds": []string{"agent-bob"}},
+	})
+	if foreign.Code != http.StatusCreated {
+		t.Fatalf("create foreign token: %d %s", foreign.Code, foreign.Body.String())
+	}
+
+	page1 := apiJSON(t, fixture.api, http.MethodGet, "/api/v1/tokens?type=client&limit=2", fixture.ownerToken, "", nil)
+	if page1.Code != http.StatusOK {
+		t.Fatalf("first page status = %d: %s", page1.Code, page1.Body.String())
+	}
+	page1Data := tokenResponseData(t, page1.Body.Bytes())
+	items1, _ := page1Data["items"].([]any)
+	if len(items1) != 2 || page1Data["hasMore"] != true || page1Data["nextCursor"] == "" {
+		t.Fatalf("unexpected first page: %+v", page1Data)
+	}
+	assertNoSensitiveFields(t, page1Data)
+	for _, item := range items1 {
+		if item.(map[string]any)["ownerUserId"] != fixture.owner.ID {
+			t.Fatalf("owner filtering happened after pagination: %+v", page1Data)
+		}
+	}
+	cursor := page1Data["nextCursor"].(string)
+	page2 := apiJSON(t, fixture.api, http.MethodGet, "/api/v1/tokens?type=client&limit=2&cursor="+cursor, fixture.ownerToken, "", nil)
+	page2Data := tokenResponseData(t, page2.Body.Bytes())
+	items2, _ := page2Data["items"].([]any)
+	if page2.Code != http.StatusOK || len(items2) != 1 || page2Data["hasMore"] != false {
+		t.Fatalf("unexpected second page: %d %+v", page2.Code, page2Data)
+	}
+
+	detail := apiJSON(t, fixture.api, http.MethodGet, "/api/v1/tokens/"+createdIDs[0], fixture.ownerToken, "", nil)
+	if detail.Code != http.StatusOK || tokenResponseData(t, detail.Body.Bytes())["status"] != "active" {
+		t.Fatalf("active detail = %d %s", detail.Code, detail.Body.String())
+	}
+	assertNoSensitiveFields(t, tokenResponseData(t, detail.Body.Bytes()))
+
+	foreignDetail := apiJSON(t, fixture.api, http.MethodGet, "/api/v1/tokens/"+tokenIDFromResponse(t, foreign.Body.Bytes()), fixture.ownerToken, "", nil)
+	if foreignDetail.Code != http.StatusForbidden {
+		t.Fatalf("foreign detail status = %d, want 403: %s", foreignDetail.Code, foreignDetail.Body.String())
+	}
+
+	rotate := apiJSON(t, fixture.api, http.MethodPost, "/api/v1/tokens/"+createdIDs[0]+"/rotate", fixture.ownerToken, "rotate-owner-token", nil)
+	if rotate.Code != http.StatusCreated {
+		t.Fatalf("rotate status = %d: %s", rotate.Code, rotate.Body.String())
+	}
+	rotateData := tokenResponseData(t, rotate.Body.Bytes())
+	if rotateData["secret"] == "" || rotateData["replayed"] != false || rotateData["id"] == createdIDs[0] {
+		t.Fatalf("unexpected rotate response: %+v", rotateData)
+	}
+	replacementID := rotateData["id"].(string)
+	rotateReplay := apiJSON(t, fixture.api, http.MethodPost, "/api/v1/tokens/"+createdIDs[0]+"/rotate", fixture.ownerToken, "rotate-owner-token", nil)
+	rotateReplayData := tokenResponseData(t, rotateReplay.Body.Bytes())
+	if rotateReplay.Code != rotate.Code || rotateReplayData["id"] != replacementID || rotateReplayData["replayed"] != true {
+		t.Fatalf("rotate replay = %d %+v", rotateReplay.Code, rotateReplayData)
+	}
+	if _, present := rotateReplayData["secret"]; present {
+		t.Fatalf("rotate replay leaked secret: %+v", rotateReplayData)
+	}
+
+	oldDetail := apiJSON(t, fixture.api, http.MethodGet, "/api/v1/tokens/"+createdIDs[0], fixture.ownerToken, "", nil)
+	if tokenResponseData(t, oldDetail.Body.Bytes())["status"] != "revoked" {
+		t.Fatalf("old token was not dynamically revoked: %s", oldDetail.Body.String())
+	}
+	revoke := apiJSON(t, fixture.api, http.MethodPost, "/api/v1/tokens/"+replacementID+"/revoke", fixture.ownerToken, "", nil)
+	if revoke.Code != http.StatusOK || tokenResponseData(t, revoke.Body.Bytes())["status"] != "revoked" {
+		t.Fatalf("revoke response = %d %s", revoke.Code, revoke.Body.String())
+	}
+
+	agent, err := fixture.api.DB.Agents().Get(context.Background(), "agent-alice-b")
+	if err != nil {
+		t.Fatal(err)
+	}
+	agent.Enabled = false
+	if err := fixture.api.DB.Agents().Update(context.Background(), agent); err != nil {
+		t.Fatal(err)
+	}
+	unavailable := apiJSON(t, fixture.api, http.MethodGet, "/api/v1/tokens/"+createdIDs[1], fixture.ownerToken, "", nil)
+	if unavailable.Code != http.StatusOK || tokenResponseData(t, unavailable.Body.Bytes())["status"] != "unavailable" {
+		t.Fatalf("disabled scoped Agent status = %d %s", unavailable.Code, unavailable.Body.String())
+	}
+
+	past := time.Now().UTC().Add(-time.Hour)
+	expiredRecord := storage.ServiceToken{ID: "stok_expired_fixture", Type: storage.TokenTypeClient, OwnerUserID: fixture.owner.ID, Prefix: "expired", TokenHash: "expired-fixture-hash", Scope: `{}`, ExpiresAt: &past}
+	if err := fixture.api.DB.ServiceTokens().Create(context.Background(), expiredRecord); err != nil {
+		t.Fatal(err)
+	}
+	expiredDetail := apiJSON(t, fixture.api, http.MethodGet, "/api/v1/tokens/"+expiredRecord.ID, fixture.ownerToken, "", nil)
+	if expiredDetail.Code != http.StatusOK || tokenResponseData(t, expiredDetail.Body.Bytes())["status"] != "expired" {
+		t.Fatalf("expired detail = %d %s", expiredDetail.Code, expiredDetail.Body.String())
+	}
+
+	audits, err := fixture.api.DB.Audits().List(context.Background(), "", 100)
+	if err != nil {
+		t.Fatal(err)
+	}
+	wantActions := map[string]bool{"token.created": false, "token.rotated": false, "token.revoked": false, "token.authorization_denied": false}
+	for _, audit := range audits.Items {
+		if _, relevant := wantActions[audit.Action]; relevant {
+			wantActions[audit.Action] = true
+			assertSafeTokenAuditDetails(t, audit.Details)
+		}
+	}
+	for action, found := range wantActions {
+		if !found {
+			t.Fatalf("missing %s audit in %+v", action, audits.Items)
+		}
+	}
+}
+
+func TestTokenAPIConcurrentIdempotencyCreatesOnlyOneToken(t *testing.T) {
+	fixture := newTokenAPIFixture(t)
+	const requests = 8
+	start := make(chan struct{})
+	type concurrentResponse struct {
+		status int
+		body   []byte
+	}
+	responses := make(chan concurrentResponse, requests)
+	var wg sync.WaitGroup
+	for i := 0; i < requests; i++ {
+		wg.Add(1)
+		go func() {
+			defer wg.Done()
+			<-start
+			response := apiJSON(t, fixture.api, http.MethodPost, "/api/v1/tokens", fixture.ownerToken, "concurrent-create", map[string]any{
+				"type": "client", "scope": map[string]any{"agentIds": []string{"agent-alice-a"}},
+			})
+			responses <- concurrentResponse{status: response.Code, body: append([]byte(nil), response.Body.Bytes()...)}
+		}()
+	}
+	close(start)
+	wg.Wait()
+	close(responses)
+
+	created := 0
+	conflicts := 0
+	secretResponses := 0
+	ids := map[string]struct{}{}
+	for response := range responses {
+		switch response.status {
+		case http.StatusCreated:
+			created++
+			data := tokenResponseData(t, response.body)
+			ids[data["id"].(string)] = struct{}{}
+			if _, present := data["secret"]; present {
+				secretResponses++
+			}
+		case http.StatusConflict:
+			conflicts++
+		default:
+			t.Fatalf("concurrent request status = %d, want 201 or 409: %s", response.status, response.body)
+		}
+	}
+	if created == 0 || created+conflicts != requests {
+		t.Fatalf("created=%d conflicts=%d requests=%d", created, conflicts, requests)
+	}
+	if secretResponses != 1 || len(ids) != 1 {
+		t.Fatalf("secret responses = %d, token ids = %+v; want one persisted winner", secretResponses, ids)
+	}
+	page, err := fixture.api.DB.ServiceTokens().List(context.Background(), storage.ServiceTokenFilter{OwnerUserID: fixture.owner.ID, Type: storage.TokenTypeClient}, "", 20)
+	if err != nil {
+		t.Fatal(err)
+	}
+	if len(page.Items) != 1 {
+		t.Fatalf("persisted tokens = %d, want 1: %+v", len(page.Items), page.Items)
+	}
+}
+
+func TestTokenAPIConcurrentIdempotencyRotatesOnlyOnce(t *testing.T) {
+	fixture := newTokenAPIFixture(t)
+	created := apiJSON(t, fixture.api, http.MethodPost, "/api/v1/tokens", fixture.ownerToken, "rotation-source", map[string]any{
+		"type": "client", "scope": map[string]any{"agentIds": []string{"agent-alice-a"}},
+	})
+	if created.Code != http.StatusCreated {
+		t.Fatalf("create rotation source: %d %s", created.Code, created.Body.String())
+	}
+	sourceID := tokenIDFromResponse(t, created.Body.Bytes())
+
+	const requests = 16
+	type concurrentResponse struct {
+		status int
+		body   []byte
+	}
+	start := make(chan struct{})
+	responses := make(chan concurrentResponse, requests)
+	var wg sync.WaitGroup
+	for i := 0; i < requests; i++ {
+		wg.Add(1)
+		go func() {
+			defer wg.Done()
+			<-start
+			response := apiJSON(t, fixture.api, http.MethodPost, "/api/v1/tokens/"+sourceID+"/rotate", fixture.ownerToken, "concurrent-rotate", nil)
+			responses <- concurrentResponse{status: response.Code, body: append([]byte(nil), response.Body.Bytes()...)}
+		}()
+	}
+	close(start)
+	wg.Wait()
+	close(responses)
+
+	secretResponses := 0
+	replacementIDs := map[string]struct{}{}
+	for response := range responses {
+		switch response.status {
+		case http.StatusCreated:
+			data := tokenResponseData(t, response.body)
+			replacementIDs[data["id"].(string)] = struct{}{}
+			if _, present := data["secret"]; present {
+				secretResponses++
+			}
+		case http.StatusConflict:
+			if !strings.Contains(strings.ToLower(string(response.body)), "in progress") {
+				t.Fatalf("rotation loser returned an ambiguous conflict: %s", response.body)
+			}
+		default:
+			t.Fatalf("concurrent rotate status = %d, want 201 or explicit in-progress 409: %s", response.status, response.body)
+		}
+	}
+	if secretResponses != 1 || len(replacementIDs) != 1 {
+		t.Fatalf("rotation secret responses = %d, replacement IDs = %+v; want one winner", secretResponses, replacementIDs)
+	}
+	page, err := fixture.api.DB.ServiceTokens().List(context.Background(), storage.ServiceTokenFilter{OwnerUserID: fixture.owner.ID, Type: storage.TokenTypeClient}, "", 20)
+	if err != nil {
+		t.Fatal(err)
+	}
+	if len(page.Items) != 2 {
+		t.Fatalf("rotation persisted %d tokens, want source plus one replacement: %+v", len(page.Items), page.Items)
+	}
+}
+
+func assertSafeTokenAuditDetails(t *testing.T, raw string) {
+	t.Helper()
+	var details map[string]any
+	if err := json.Unmarshal([]byte(raw), &details); err != nil {
+		t.Fatalf("invalid token audit details %q: %v", raw, err)
+	}
+	allowed := map[string]bool{
+		"type": true, "prefix": true, "ownerUserId": true, "agentId": true, "nodeId": true,
+		"tokenId": true, "replacementTokenId": true, "resourceIds": true, "reasonCode": true,
+	}
+	for key := range details {
+		if !allowed[key] {
+			t.Fatalf("token audit detail %q is not allowlisted: %s", key, raw)
+		}
+	}
+	assertNoSensitiveFields(t, details)
+}
diff -ruN --exclude=*.__ABSENT__ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-4-before/internal/server/token_service.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-4-after/internal/server/token_service.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-4-before/internal/server/token_service.go	1970-01-01 08:00:00
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-4-after/internal/server/token_service.go	2026-09-06 15:41:34
@@ -0,0 +1,453 @@
+package server
+
+import (
+	"context"
+	"crypto/sha256"
+	"database/sql"
+	"encoding/hex"
+	"encoding/json"
+	"errors"
+	"fmt"
+	"strings"
+	"time"
+
+	"github.com/tunnelmesh/tunnelmesh/internal/auth"
+	"github.com/tunnelmesh/tunnelmesh/internal/storage"
+)
+
+var (
+	errInvalidTokenRequest   = errors.New("invalid token request")
+	errTokenConflict         = errors.New("token mutation conflict")
+	errIdempotencyConflict   = errors.New("idempotency conflict")
+	errIdempotencyInProgress = errors.New("idempotency request is in progress")
+)
+
+// TokenView is the only service-token representation exposed by management
+// APIs. It deliberately has no field capable of carrying a stored hash.
+type TokenView struct {
+	ID          string            `json:"id"`
+	Type        storage.TokenType `json:"type"`
+	OwnerUserID string            `json:"ownerUserId"`
+	AgentID     string            `json:"agentId,omitempty"`
+	NodeID      string            `json:"nodeId,omitempty"`
+	Prefix      string            `json:"prefix"`
+	Scope       auth.TokenScope   `json:"scope"`
+	Status      string            `json:"status"`
+	ExpiresAt   *time.Time        `json:"expiresAt,omitempty"`
+	RevokedAt   *time.Time        `json:"revokedAt,omitempty"`
+	LastUsedAt  *time.Time        `json:"lastUsedAt,omitempty"`
+	CreatedAt   time.Time         `json:"createdAt"`
+	UpdatedAt   time.Time         `json:"updatedAt"`
+	Secret      string            `json:"secret,omitempty"`
+	Replayed    bool              `json:"replayed"`
+}
+
+type tokenPage struct {
+	Items      []TokenView
+	NextCursor string
+	HasMore    bool
+}
+
+type tokenReplayRecord struct {
+	Operation   string    `json:"operation"`
+	Fingerprint string    `json:"fingerprint"`
+	Token       TokenView `json:"token"`
+}
+
+// TokenService orchestrates credential lifecycle operations while leaving SQL
+// and transaction mechanics in storage repositories.
+type TokenService struct {
+	db       *storage.DB
+	tokens   storage.ServiceTokenRepository
+	users    storage.UserRepository
+	agents   storage.AgentRepository
+	nodes    storage.NodeRepository
+	policies storage.PolicyRepository
+	audits   storage.AuditRepository
+}
+
+// NewTokenService wires management lifecycle orchestration to storage.
+func NewTokenService(db *storage.DB) *TokenService {
+	if db == nil {
+		return nil
+	}
+	return &TokenService{
+		db: db, tokens: db.ServiceTokens(), users: db.Users(), agents: db.Agents(),
+		nodes: db.Nodes(), policies: db.Policies(), audits: db.Audits(),
+	}
+}
+
+// GetAgent exposes the resource fact needed by handler-level owner checks.
+func (s *TokenService) GetAgent(ctx context.Context, id string) (storage.Agent, error) {
+	return s.agents.Get(ctx, strings.TrimSpace(id))
+}
+
+// Get returns both the internal ownership fact and its redacted API view.
+func (s *TokenService) Get(ctx context.Context, id string) (storage.ServiceToken, TokenView, error) {
+	record, err := s.tokens.Get(ctx, strings.TrimSpace(id))
+	if err != nil {
+		return storage.ServiceToken{}, TokenView{}, err
+	}
+	view, err := s.view(ctx, record)
+	return record, view, err
+}
+
+// List applies repository-side filters before deriving public Token status.
+func (s *TokenService) List(ctx context.Context, filter storage.ServiceTokenFilter, cursor string, limit int) (tokenPage, error) {
+	page, err := s.tokens.List(ctx, filter, cursor, limit)
+	if err != nil {
+		return tokenPage{}, err
+	}
+	out := tokenPage{Items: make([]TokenView, 0, len(page.Items)), NextCursor: page.NextCursor, HasMore: page.HasMore}
+	for _, record := range page.Items {
+		view, err := s.view(ctx, record)
+		if err != nil {
+			return tokenPage{}, err
+		}
+		out.Items = append(out.Items, view)
+	}
+	return out, nil
+}
+
+// Create validates and plans credential material before atomically committing
+// the Token, audit event, and non-secret replay metadata.
+func (s *TokenService) Create(ctx context.Context, actorUserID, idempotencyKey string, input auth.CreateTokenInput) (TokenView, error) {
+	fingerprint, err := mutationFingerprint("token.create", input)
+	if err != nil {
+		return TokenView{}, err
+	}
+	if replay, found, err := s.completedReplay(ctx, actorUserID, idempotencyKey, "token.create", fingerprint); found || err != nil {
+		return replay, err
+	}
+	planner := &plannedTokenRepository{base: s.tokens}
+	credentials := auth.NewCredentialService(planner, s.users, s.agents, s.nodes, s.policies)
+	defer credentials.Close()
+	created, err := credentials.Create(ctx, input)
+	if err != nil {
+		return TokenView{}, invalidCredentialRequest(err)
+	}
+	if planner.created == nil {
+		return TokenView{}, errors.New("credential planner did not produce a token")
+	}
+	record := *planner.created
+	view, err := s.view(ctx, record)
+	if err != nil {
+		return TokenView{}, err
+	}
+	view.Secret = created.Secret
+	return s.persistMutation(ctx, actorUserID, idempotencyKey, "token.create", fingerprint, view, func(tokens storage.ServiceTokenRepository, audits storage.AuditRepository) error {
+		if err := tokens.Create(ctx, record); err != nil {
+			return err
+		}
+		return audits.Create(ctx, tokenAudit(actorUserID, "token.created", record.ID, record, "", ""))
+	})
+}
+
+// Rotate creates one replacement, atomically revokes the old Token, and safely
+// resolves concurrent same-key requests to a non-secret replay.
+func (s *TokenService) Rotate(ctx context.Context, actorUserID, id, idempotencyKey string) (TokenView, error) {
+	id = strings.TrimSpace(id)
+	operation := "token.rotate:" + id
+	fingerprint, err := mutationFingerprint(operation, struct {
+		TokenID string `json:"tokenId"`
+	}{TokenID: id})
+	if err != nil {
+		return TokenView{}, err
+	}
+	if replay, found, err := s.completedReplay(ctx, actorUserID, idempotencyKey, operation, fingerprint); found || err != nil {
+		return replay, err
+	}
+	original, err := s.tokens.Get(ctx, id)
+	if err != nil {
+		return TokenView{}, err
+	}
+	planner := &plannedTokenRepository{base: s.tokens}
+	credentials := auth.NewCredentialService(planner, s.users, s.agents, s.nodes, s.policies)
+	defer credentials.Close()
+	created, err := credentials.Rotate(ctx, id)
+	if err != nil {
+		if errors.Is(err, auth.ErrUnauthenticated) || errors.Is(err, storage.ErrServiceTokenRevoked) {
+			// A concurrent winner may have committed the old-token revocation and
+			// replay metadata between the initial lookup and lifecycle planning.
+			if replay, found, replayErr := s.completedReplay(ctx, actorUserID, idempotencyKey, operation, fingerprint); found || replayErr != nil {
+				return replay, replayErr
+			}
+			return TokenView{}, fmt.Errorf("%w: token cannot be rotated", errTokenConflict)
+		}
+		return TokenView{}, err
+	}
+	if planner.rotation == nil {
+		return TokenView{}, errors.New("credential planner did not produce a rotation")
+	}
+	replacement := planner.rotation.replacement
+	view, err := s.view(ctx, replacement)
+	if err != nil {
+		return TokenView{}, err
+	}
+	view.Secret = created.Secret
+	return s.persistMutation(ctx, actorUserID, idempotencyKey, operation, fingerprint, view, func(tokens storage.ServiceTokenRepository, audits storage.AuditRepository) error {
+		if err := tokens.Rotate(ctx, id, replacement, planner.rotation.when); err != nil {
+			return err
+		}
+		return audits.Create(ctx, tokenAudit(actorUserID, "token.rotated", id, original, replacement.ID, ""))
+	})
+}
+
+// Revoke commits the lifecycle timestamp and audit record together.
+func (s *TokenService) Revoke(ctx context.Context, actorUserID, id string) (TokenView, error) {
+	id = strings.TrimSpace(id)
+	record, err := s.tokens.Get(ctx, id)
+	if err != nil {
+		return TokenView{}, err
+	}
+	if record.RevokedAt != nil {
+		return s.view(ctx, record)
+	}
+	when := time.Now().UTC()
+	err = s.db.ServiceTokenMutationTransaction(ctx, func(tokens storage.ServiceTokenRepository, audits storage.AuditRepository, _ storage.IdempotencyRepository) error {
+		if err := tokens.Revoke(ctx, id, when); err != nil {
+			return err
+		}
+		return audits.Create(ctx, tokenAudit(actorUserID, "token.revoked", id, record, "", ""))
+	})
+	if err != nil {
+		return TokenView{}, err
+	}
+	record.RevokedAt = &when
+	record.UpdatedAt = when
+	return s.view(ctx, record)
+}
+
+// RecordAuthorizationDenied persists only allowlisted non-secret identifiers.
+func (s *TokenService) RecordAuthorizationDenied(ctx context.Context, actorUserID string, record storage.ServiceToken, reasonCode string) error {
+	resourceID := record.ID
+	if resourceID == "" {
+		if record.AgentID != "" {
+			resourceID = record.AgentID
+		} else {
+			resourceID = record.NodeID
+		}
+	}
+	return s.audits.Create(ctx, tokenAudit(actorUserID, "token.authorization_denied", resourceID, record, "", reasonCode))
+}
+
+func (s *TokenService) persistMutation(ctx context.Context, actorUserID, key, operation, fingerprint string, issued TokenView, mutate func(storage.ServiceTokenRepository, storage.AuditRepository) error) (TokenView, error) {
+	var result TokenView
+	err := s.db.ServiceTokenMutationTransaction(ctx, func(tokens storage.ServiceTokenRepository, audits storage.AuditRepository, idempotency storage.IdempotencyRepository) error {
+		if key != "" {
+			atomic, ok := idempotency.(storage.AtomicIdempotencyRepository)
+			if !ok {
+				return errors.New("atomic idempotency repository is required")
+			}
+			existing, claimed, err := atomic.Claim(ctx, storage.IdempotencyRecord{Key: key, UserID: actorUserID})
+			if err != nil {
+				return err
+			}
+			if !claimed {
+				replay, err := decodeTokenReplay(existing, actorUserID, operation, fingerprint)
+				if err != nil {
+					return err
+				}
+				result = replay
+				return nil
+			}
+		}
+		if err := mutate(tokens, audits); err != nil {
+			return err
+		}
+		result = issued
+		if key == "" {
+			return nil
+		}
+		replay := issued
+		replay.Secret = ""
+		replay.Replayed = true
+		encoded, err := json.Marshal(tokenReplayRecord{Operation: operation, Fingerprint: fingerprint, Token: replay})
+		if err != nil {
+			return err
+		}
+		expiresAt := time.Now().UTC().Add(24 * time.Hour)
+		return idempotency.(storage.AtomicIdempotencyRepository).Update(ctx, storage.IdempotencyRecord{
+			Key: key, UserID: actorUserID, Response: string(encoded), StatusCode: 201, ExpiresAt: &expiresAt,
+		})
+	})
+	return result, err
+}
+
+func decodeTokenReplay(record storage.IdempotencyRecord, actorUserID, operation, fingerprint string) (TokenView, error) {
+	if record.UserID != "" && record.UserID != actorUserID {
+		return TokenView{}, fmt.Errorf("%w: key belongs to another user", errIdempotencyConflict)
+	}
+	if record.StatusCode == 102 {
+		return TokenView{}, errIdempotencyInProgress
+	}
+	var replay tokenReplayRecord
+	if err := json.Unmarshal([]byte(record.Response), &replay); err != nil {
+		return TokenView{}, fmt.Errorf("%w: invalid replay metadata", errIdempotencyConflict)
+	}
+	if replay.Operation != operation || replay.Fingerprint != fingerprint || replay.Token.ID == "" {
+		return TokenView{}, fmt.Errorf("%w: key was used for another request", errIdempotencyConflict)
+	}
+	replay.Token.Secret = ""
+	replay.Token.Replayed = true
+	return replay.Token, nil
+}
+
+func (s *TokenService) completedReplay(ctx context.Context, actorUserID, key, operation, fingerprint string) (TokenView, bool, error) {
+	if key == "" {
+		return TokenView{}, false, nil
+	}
+	record, err := s.db.Idempotency().Get(ctx, key)
+	if errors.Is(err, sql.ErrNoRows) {
+		return TokenView{}, false, nil
+	}
+	if err != nil {
+		return TokenView{}, false, err
+	}
+	replay, err := decodeTokenReplay(record, actorUserID, operation, fingerprint)
+	return replay, true, err
+}
+
+func mutationFingerprint(operation string, payload any) (string, error) {
+	encoded, err := json.Marshal(struct {
+		Operation string `json:"operation"`
+		Payload   any    `json:"payload"`
+	}{Operation: operation, Payload: payload})
+	if err != nil {
+		return "", err
+	}
+	sum := sha256.Sum256(encoded)
+	return hex.EncodeToString(sum[:]), nil
+}
+
+func (s *TokenService) view(ctx context.Context, record storage.ServiceToken) (TokenView, error) {
+	var scope auth.TokenScope
+	if err := json.Unmarshal([]byte(record.Scope), &scope); err != nil {
+		return TokenView{}, fmt.Errorf("decode token scope: %w", err)
+	}
+	return TokenView{
+		ID: record.ID, Type: record.Type, OwnerUserID: record.OwnerUserID, AgentID: record.AgentID, NodeID: record.NodeID,
+		Prefix: record.Prefix, Scope: scope, Status: s.status(ctx, record, scope), ExpiresAt: record.ExpiresAt,
+		RevokedAt: record.RevokedAt, LastUsedAt: record.LastUsedAt, CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt,
+	}, nil
+}
+
+func (s *TokenService) status(ctx context.Context, record storage.ServiceToken, scope auth.TokenScope) string {
+	now := time.Now().UTC()
+	if record.RevokedAt != nil {
+		return "revoked"
+	}
+	if record.ExpiresAt != nil && !record.ExpiresAt.After(now) {
+		return "expired"
+	}
+	owner, err := s.users.Get(ctx, record.OwnerUserID)
+	if err != nil || owner.Disabled {
+		return "unavailable"
+	}
+	switch record.Type {
+	case storage.TokenTypeAgent:
+		agent, err := s.agents.Get(ctx, record.AgentID)
+		if err != nil || !agent.Enabled || agent.OwnerUserID != record.OwnerUserID {
+			return "unavailable"
+		}
+	case storage.TokenTypeClient:
+		for _, agentID := range scope.AgentIDs {
+			agent, err := s.agents.Get(ctx, agentID)
+			if err != nil || !agent.Enabled || agent.OwnerUserID != record.OwnerUserID {
+				return "unavailable"
+			}
+		}
+	case storage.TokenTypeServerNode:
+		node, err := s.nodes.Get(ctx, record.NodeID)
+		if err != nil || (node.ExpiresAt != nil && !node.ExpiresAt.After(now)) {
+			return "unavailable"
+		}
+	default:
+		return "unavailable"
+	}
+	return "active"
+}
+
+func invalidCredentialRequest(err error) error {
+	if errors.Is(err, auth.ErrInvalidTokenType) || errors.Is(err, auth.ErrInvalidTokenScope) || errors.Is(err, auth.ErrUnauthenticated) ||
+		strings.Contains(strings.ToLower(err.Error()), "expiration") || strings.Contains(strings.ToLower(err.Error()), "requires") || strings.Contains(strings.ToLower(err.Error()), "bound") {
+		return fmt.Errorf("%w: %v", errInvalidTokenRequest, err)
+	}
+	return err
+}
+
+func tokenAudit(actorUserID, action, resourceID string, record storage.ServiceToken, replacementTokenID, reasonCode string) storage.AuditLog {
+	details := map[string]any{}
+	if record.Type != "" {
+		details["type"] = record.Type
+	}
+	if record.Prefix != "" {
+		details["prefix"] = record.Prefix
+	}
+	if record.OwnerUserID != "" {
+		details["ownerUserId"] = record.OwnerUserID
+	}
+	if record.AgentID != "" {
+		details["agentId"] = record.AgentID
+	}
+	if record.NodeID != "" {
+		details["nodeId"] = record.NodeID
+	}
+	if record.ID != "" {
+		details["tokenId"] = record.ID
+	}
+	if replacementTokenID != "" {
+		details["replacementTokenId"] = replacementTokenID
+	}
+	if reasonCode != "" {
+		details["reasonCode"] = reasonCode
+	}
+	encoded, _ := json.Marshal(details)
+	return storage.AuditLog{ActorUserID: actorUserID, Action: action, ResourceType: "service_token", ResourceID: resourceID, Details: string(encoded)}
+}
+
+type plannedRotation struct {
+	replacement storage.ServiceToken
+	when        time.Time
+}
+
+// plannedTokenRepository lets CredentialService perform its complete domain
+// validation and secret generation before the real database transaction. Only
+// the transaction-bound repository later persists the captured mutation.
+type plannedTokenRepository struct {
+	base     storage.ServiceTokenRepository
+	created  *storage.ServiceToken
+	rotation *plannedRotation
+}
+
+func (r *plannedTokenRepository) Create(_ context.Context, token storage.ServiceToken) error {
+	r.created = &token
+	return nil
+}
+
+func (r *plannedTokenRepository) Get(ctx context.Context, id string) (storage.ServiceToken, error) {
+	return r.base.Get(ctx, id)
+}
+
+func (r *plannedTokenRepository) GetByHash(ctx context.Context, hash string) (storage.ServiceToken, error) {
+	return r.base.GetByHash(ctx, hash)
+}
+
+func (r *plannedTokenRepository) List(ctx context.Context, filter storage.ServiceTokenFilter, cursor string, limit int) (storage.Page[storage.ServiceToken], error) {
+	return r.base.List(ctx, filter, cursor, limit)
+}
+
+func (r *plannedTokenRepository) Revoke(context.Context, string, time.Time) error {
+	return errors.New("planned repository cannot revoke")
+}
+
+func (r *plannedTokenRepository) TouchLastUsed(context.Context, string, time.Time) error {
+	return nil
+}
+
+func (r *plannedTokenRepository) Rotate(_ context.Context, _ string, replacement storage.ServiceToken, when time.Time) error {
+	r.rotation = &plannedRotation{replacement: replacement, when: when}
+	return nil
+}
+
+var _ storage.ServiceTokenRepository = (*plannedTokenRepository)(nil)
diff -ruN --exclude=*.__ABSENT__ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-4-before/internal/storage/db.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-4-after/internal/storage/db.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-4-before/internal/storage/db.go	2026-09-06 14:32:31
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-4-after/internal/storage/db.go	2026-09-06 15:41:34
@@ -40,15 +40,26 @@
 
 // ServiceTokenTransaction atomically applies service-token lifecycle changes
 // and their audit record using repositories bound to the same transaction.
+// It is retained for Task 2 callers; mutation paths that also persist replay
+// metadata should use ServiceTokenMutationTransaction.
 func (d *DB) ServiceTokenTransaction(ctx context.Context, fn func(ServiceTokenRepository, AuditRepository) error) error {
+	return d.ServiceTokenMutationTransaction(ctx, func(tokens ServiceTokenRepository, audits AuditRepository, _ IdempotencyRepository) error {
+		return fn(tokens, audits)
+	})
+}
+
+// ServiceTokenMutationTransaction commits a service-token mutation, its audit
+// event, and non-secret idempotency metadata as one database fact.
+func (d *DB) ServiceTokenMutationTransaction(ctx context.Context, fn func(ServiceTokenRepository, AuditRepository, IdempotencyRepository) error) error {
 	tx, err := d.sql.BeginTx(ctx, nil)
 	if err != nil {
 		return err
 	}
-	defer tx.Rollback()
+	defer func() { _ = tx.Rollback() }()
 	tokens := &serviceTokenRepo{db: tx, driver: d.driver}
 	audits := &auditRepo{db: tx}
-	if err := fn(tokens, audits); err != nil {
+	idempotency := &idempotencyRepo{db: tx}
+	if err := fn(tokens, audits, idempotency); err != nil {
 		return err
 	}
 	return tx.Commit()
diff -ruN --exclude=*.__ABSENT__ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-4-before/internal/storage/repository.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-4-after/internal/storage/repository.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-4-before/internal/storage/repository.go	2026-09-06 15:41:34
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-4-after/internal/storage/repository.go	2026-09-06 15:41:34
@@ -1215,7 +1215,7 @@
 	return p, nil
 }
 
-type idempotencyRepo struct{ db *sql.DB }
+type idempotencyRepo struct{ db dbExecutor }
 
 func (r *idempotencyRepo) Claim(ctx context.Context, v IdempotencyRecord) (IdempotencyRecord, bool, error) {
 	if v.Key == "" {
diff -ruN --exclude=*.__ABSENT__ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-4-before/internal/storage/service_token_test.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-4-after/internal/storage/service_token_test.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-4-before/internal/storage/service_token_test.go	2026-09-06 14:36:12
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-4-after/internal/storage/service_token_test.go	2026-09-06 15:41:34
@@ -215,6 +215,55 @@
 	assertAuditExists(t, db.Audits(), "audit-service-token-tx-rollback", false)
 }
 
+func TestServiceTokenMutationTransactionIncludesIdempotency(t *testing.T) {
+	db := newTestDB(t)
+	ctx := context.Background()
+	token := ServiceToken{ID: "contract-service-token-mutation-commit", Prefix: "commit", TokenHash: "contract-service-token-hash-mutation-commit", Scope: `{}`, Type: TokenTypeClient}
+	if err := db.ServiceTokenMutationTransaction(ctx, func(tokens ServiceTokenRepository, audits AuditRepository, idempotency IdempotencyRepository) error {
+		if err := tokens.Create(ctx, token); err != nil {
+			return err
+		}
+		if err := audits.Create(ctx, AuditLog{ID: "audit-service-token-mutation-commit", Action: "token.created", ResourceType: "service_token", ResourceID: token.ID}); err != nil {
+			return err
+		}
+		return idempotency.Put(ctx, IdempotencyRecord{Key: "service-token-mutation-commit", Response: `{"id":"contract-service-token-mutation-commit"}`, StatusCode: 201})
+	}); err != nil {
+		t.Fatalf("commit mutation transaction: %v", err)
+	}
+	if _, err := db.ServiceTokens().Get(ctx, token.ID); err != nil {
+		t.Fatalf("get committed token: %v", err)
+	}
+	assertAuditExists(t, db.Audits(), "audit-service-token-mutation-commit", true)
+	if _, err := db.Idempotency().Get(ctx, "service-token-mutation-commit"); err != nil {
+		t.Fatalf("get committed idempotency record: %v", err)
+	}
+
+	rollbackToken := ServiceToken{ID: "contract-service-token-mutation-rollback", Prefix: "rollback", TokenHash: "contract-service-token-hash-mutation-rollback", Scope: `{}`, Type: TokenTypeClient}
+	wantErr := errors.New("abort complete mutation")
+	err := db.ServiceTokenMutationTransaction(ctx, func(tokens ServiceTokenRepository, audits AuditRepository, idempotency IdempotencyRepository) error {
+		if err := tokens.Create(ctx, rollbackToken); err != nil {
+			return err
+		}
+		if err := audits.Create(ctx, AuditLog{ID: "audit-service-token-mutation-rollback", Action: "token.created", ResourceType: "service_token", ResourceID: rollbackToken.ID}); err != nil {
+			return err
+		}
+		if err := idempotency.Put(ctx, IdempotencyRecord{Key: "service-token-mutation-rollback", Response: `{"id":"contract-service-token-mutation-rollback"}`, StatusCode: 201}); err != nil {
+			return err
+		}
+		return wantErr
+	})
+	if !errors.Is(err, wantErr) {
+		t.Fatalf("rollback mutation transaction error = %v, want %v", err, wantErr)
+	}
+	if _, err := db.ServiceTokens().Get(ctx, rollbackToken.ID); !errors.Is(err, sql.ErrNoRows) {
+		t.Fatalf("rollback token lookup error = %v, want sql.ErrNoRows", err)
+	}
+	assertAuditExists(t, db.Audits(), "audit-service-token-mutation-rollback", false)
+	if _, err := db.Idempotency().Get(ctx, "service-token-mutation-rollback"); !errors.Is(err, sql.ErrNoRows) {
+		t.Fatalf("rollback idempotency lookup error = %v, want sql.ErrNoRows", err)
+	}
+}
+
 func assertServiceToken(t *testing.T, got, want ServiceToken) {
 	t.Helper()
 	if got.ID != want.ID || got.OwnerUserID != want.OwnerUserID || got.AgentID != want.AgentID || got.NodeID != want.NodeID || got.Prefix != want.Prefix || got.TokenHash != want.TokenHash || got.Scope != want.Scope || got.Type != want.Type {
```
