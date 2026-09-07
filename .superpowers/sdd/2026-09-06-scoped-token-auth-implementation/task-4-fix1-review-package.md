# Task 4 fix round 1 review package

Fix base: Task 4 initial reviewed snapshot (HEAD remained `163fe12121d2f839ea4bf4907f55a8e7dd835057`)

## Changed files

```text
Files .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-4-after/internal/server/token_api.go and .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-4-fix1-after/internal/server/token_api.go differ
Files .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-4-after/internal/server/token_api_test.go and .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-4-fix1-after/internal/server/token_api_test.go differ
Files .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-4-after/internal/server/token_service.go and .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-4-fix1-after/internal/server/token_service.go differ
Files .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-4-after/internal/storage/db.go and .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-4-fix1-after/internal/storage/db.go differ
Files .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-4-after/internal/storage/repository.go and .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-4-fix1-after/internal/storage/repository.go differ
Files .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-4-after/internal/storage/service_token_test.go and .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-4-fix1-after/internal/storage/service_token_test.go differ
```

## Full fix-only diff

```diff
diff -ruN --exclude=*.__ABSENT__ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-4-after/internal/server/token_api.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-4-fix1-after/internal/server/token_api.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-4-after/internal/server/token_api.go	2026-09-06 15:41:34
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-4-fix1-after/internal/server/token_api.go	2026-09-06 16:08:23
@@ -3,6 +3,7 @@
 import (
 	"database/sql"
 	"errors"
+	"log/slog"
 	"net/http"
 	"strings"
 	"time"
@@ -180,7 +181,23 @@
 }
 
 func (a *API) denyToken(r *http.Request, principal auth.Principal, record storage.ServiceToken, reason string) {
-	_ = a.tokenService.RecordAuthorizationDenied(r.Context(), principal.UserID, record, reason)
+	if err := a.tokenService.RecordAuthorizationDenied(r.Context(), principal.UserID, record, reason); err != nil {
+		resourceID := record.ID
+		if resourceID == "" {
+			if record.AgentID != "" {
+				resourceID = record.AgentID
+			} else {
+				resourceID = record.NodeID
+			}
+		}
+		slog.ErrorContext(r.Context(), "token_authorization_denied_audit_failed",
+			"action", "token.authorization_denied",
+			"actor_user_id", principal.UserID,
+			"resource_type", "service_token",
+			"resource_id", resourceID,
+			"reason_code", reason,
+		)
+	}
 }
 
 // tokenIdempotencyKey enforces the storage contract's bounded key size.
diff -ruN --exclude=*.__ABSENT__ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-4-after/internal/server/token_api_test.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-4-fix1-after/internal/server/token_api_test.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-4-after/internal/server/token_api_test.go	2026-09-06 15:41:34
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-4-fix1-after/internal/server/token_api_test.go	2026-09-06 16:08:23
@@ -1,9 +1,13 @@
 package server
 
 import (
+	"bytes"
 	"context"
+	"database/sql"
 	"encoding/json"
+	"errors"
 	"fmt"
+	"log/slog"
 	"net/http"
 	"reflect"
 	"strings"
@@ -482,6 +486,384 @@
 	if len(page.Items) != 2 {
 		t.Fatalf("rotation persisted %d tokens, want source plus one replacement: %+v", len(page.Items), page.Items)
 	}
+}
+
+func TestTokenAPIAuthorizationChangeBeforeCommitCannotCreate(t *testing.T) {
+	fixture := newTokenAPIFixture(t)
+	barrier := &nthAgentGetBarrier{
+		AgentRepository: fixture.api.tokenService.agents,
+		targetID:        "agent-alice-a",
+		targetCall:      2,
+		reached:         make(chan struct{}),
+		release:         make(chan struct{}),
+	}
+	fixture.api.tokenService.agents = barrier
+
+	responseCh := make(chan concurrentHTTPResponse, 1)
+	go func() {
+		response := apiJSON(t, fixture.api, http.MethodPost, "/api/v1/tokens", fixture.ownerToken, "authorization-race", map[string]any{
+			"type": "agent", "agentId": "agent-alice-a", "scope": map[string]any{},
+		})
+		responseCh <- concurrentHTTPResponse{status: response.Code, body: append([]byte(nil), response.Body.Bytes()...)}
+	}()
+	select {
+	case <-barrier.reached:
+	case <-time.After(2 * time.Second):
+		t.Fatal("credential planning did not reach the Agent authorization read")
+	}
+	agent, err := fixture.api.DB.Agents().Get(context.Background(), "agent-alice-a")
+	if err != nil {
+		t.Fatal(err)
+	}
+	agent.OwnerUserID = fixture.other.ID
+	agent.Enabled = false
+	if err := fixture.api.DB.Agents().Update(context.Background(), agent); err != nil {
+		t.Fatal(err)
+	}
+	close(barrier.release)
+	response := <-responseCh
+	if response.status == http.StatusCreated {
+		t.Fatalf("Token created from stale owner/enabled authorization: %s", response.body)
+	}
+	page, err := fixture.api.DB.ServiceTokens().List(context.Background(), storage.ServiceTokenFilter{OwnerUserID: fixture.owner.ID, AgentID: "agent-alice-a"}, "", 20)
+	if err != nil {
+		t.Fatal(err)
+	}
+	if len(page.Items) != 0 {
+		t.Fatalf("stale authorization persisted Tokens: %+v", page.Items)
+	}
+	if _, err := fixture.api.DB.Idempotency().Get(context.Background(), "authorization-race"); !errors.Is(err, sql.ErrNoRows) {
+		t.Fatalf("failed authorization retained idempotency claim: %v", err)
+	}
+}
+
+func TestTokenAPIAuthorizationDeniedAuditFailureIsObservable(t *testing.T) {
+	fixture := newTokenAPIFixture(t)
+	fixture.api.tokenService.audits = failingAuditRepository{
+		AuditRepository: fixture.api.DB.Audits(),
+		err:             errors.New("sensitive-secret-value"),
+	}
+	var logs bytes.Buffer
+	previous := slog.Default()
+	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
+	t.Cleanup(func() { slog.SetDefault(previous) })
+
+	response := apiJSON(t, fixture.api, http.MethodPost, "/api/v1/tokens", fixture.ownerToken, "denied-audit-failure", map[string]any{
+		"type": "server_node", "nodeId": "node-a", "scope": map[string]any{},
+	})
+	if response.Code != http.StatusForbidden {
+		t.Fatalf("denied response status = %d, want 403: %s", response.Code, response.Body.String())
+	}
+	logged := logs.String()
+	if !strings.Contains(logged, "token_authorization_denied_audit_failed") || !strings.Contains(logged, "server_node_admin_required") || !strings.Contains(logged, fixture.owner.ID) {
+		t.Fatalf("audit failure was not logged with structured identifiers: %s", logged)
+	}
+	if strings.Contains(logged, "sensitive-secret-value") || strings.Contains(strings.ToLower(logged), "secret=") {
+		t.Fatalf("audit failure log leaked sensitive error content: %s", logged)
+	}
+}
+
+func TestTokenAPIStatusStorageErrorsReturn500(t *testing.T) {
+	tests := []struct {
+		name   string
+		body   map[string]any
+		inject func(*TokenService, error)
+	}{
+		{name: "owner repository", body: map[string]any{"type": "client", "scope": map[string]any{}}, inject: func(service *TokenService, want error) {
+			service.users = failingUserGetRepository{UserRepository: service.users, err: want}
+		}},
+		{name: "Agent repository", body: map[string]any{"type": "agent", "ownerUserId": "OWNER", "agentId": "agent-alice-a", "scope": map[string]any{"agentIds": []string{"agent-alice-a"}}}, inject: func(service *TokenService, want error) {
+			service.agents = failingAgentGetRepository{AgentRepository: service.agents, err: want}
+		}},
+		{name: "node repository", body: map[string]any{"type": "server_node", "nodeId": "node-a", "scope": map[string]any{}}, inject: func(service *TokenService, want error) {
+			service.nodes = failingNodeGetRepository{NodeRepository: service.nodes, err: want}
+		}},
+	}
+	for index, tc := range tests {
+		t.Run(tc.name, func(t *testing.T) {
+			fixture := newTokenAPIFixture(t)
+			if owner, ok := tc.body["ownerUserId"]; ok && owner == "OWNER" {
+				tc.body["ownerUserId"] = fixture.owner.ID
+			}
+			created := apiJSON(t, fixture.api, http.MethodPost, "/api/v1/tokens", fixture.adminToken, fmt.Sprintf("status-error-%d", index), tc.body)
+			if created.Code != http.StatusCreated {
+				t.Fatalf("create status fixture = %d: %s", created.Code, created.Body.String())
+			}
+			wantErr := errors.New("status repository unavailable")
+			tc.inject(fixture.api.tokenService, wantErr)
+			detail := apiJSON(t, fixture.api, http.MethodGet, "/api/v1/tokens/"+tokenIDFromResponse(t, created.Body.Bytes()), fixture.adminToken, "", nil)
+			if detail.Code != http.StatusInternalServerError {
+				t.Fatalf("status repository error returned %d, want 500: %s", detail.Code, detail.Body.String())
+			}
+			if !strings.Contains(detail.Body.String(), wantErr.Error()) {
+				t.Fatalf("status error did not propagate: %s", detail.Body.String())
+			}
+		})
+	}
+}
+
+func TestTokenAPIConcurrentRevokePreservesFirstTransitionAndAudit(t *testing.T) {
+	fixture := newTokenAPIFixture(t)
+	created := apiJSON(t, fixture.api, http.MethodPost, "/api/v1/tokens", fixture.ownerToken, "concurrent-revoke-source", map[string]any{
+		"type": "client", "scope": map[string]any{},
+	})
+	if created.Code != http.StatusCreated {
+		t.Fatalf("create revoke source = %d: %s", created.Code, created.Body.String())
+	}
+	tokenID := tokenIDFromResponse(t, created.Body.Bytes())
+	const requests = 8
+	barrier := newTwoPhaseTokenGetBarrier(fixture.api.tokenService.tokens, tokenID, requests)
+	fixture.api.tokenService.tokens = barrier
+	responses := make(chan concurrentHTTPResponse, requests)
+	start := make(chan struct{})
+	var wg sync.WaitGroup
+	for i := 0; i < requests; i++ {
+		wg.Add(1)
+		go func() {
+			defer wg.Done()
+			<-start
+			response := apiJSON(t, fixture.api, http.MethodPost, "/api/v1/tokens/"+tokenID+"/revoke", fixture.ownerToken, "", nil)
+			responses <- concurrentHTTPResponse{status: response.Code, body: append([]byte(nil), response.Body.Bytes()...)}
+		}()
+	}
+	close(start)
+	barrier.waitAndRelease(t, 1)
+	barrier.waitAndRelease(t, 2)
+	wg.Wait()
+	close(responses)
+	revokedAt := map[string]struct{}{}
+	for response := range responses {
+		if response.status != http.StatusOK {
+			t.Fatalf("concurrent revoke status = %d: %s", response.status, response.body)
+		}
+		value, _ := tokenResponseData(t, response.body)["revokedAt"].(string)
+		if value == "" {
+			t.Fatalf("revoke response has no revokedAt: %s", response.body)
+		}
+		revokedAt[value] = struct{}{}
+	}
+	if len(revokedAt) != 1 {
+		t.Fatalf("concurrent revoke overwrote the first timestamp: %+v", revokedAt)
+	}
+	if got := countTokenLifecycleAudits(t, fixture.api.DB, tokenID, "token.revoked"); got != 1 {
+		t.Fatalf("token.revoked audits = %d, want 1", got)
+	}
+}
+
+func TestTokenAPIConcurrentRevokeVsRotateHasOneLifecycleWinner(t *testing.T) {
+	fixture := newTokenAPIFixture(t)
+	created := apiJSON(t, fixture.api, http.MethodPost, "/api/v1/tokens", fixture.ownerToken, "revoke-rotate-source", map[string]any{
+		"type": "client", "scope": map[string]any{},
+	})
+	if created.Code != http.StatusCreated {
+		t.Fatalf("create lifecycle source = %d: %s", created.Code, created.Body.String())
+	}
+	tokenID := tokenIDFromResponse(t, created.Body.Bytes())
+	barrier := &selectedTokenGetBarrier{
+		ServiceTokenRepository: fixture.api.tokenService.tokens,
+		targetID:               tokenID,
+		blocked: map[int]tokenGetBlock{
+			3: {reached: make(chan struct{}), release: make(chan struct{})},
+			5: {reached: make(chan struct{}), release: make(chan struct{})},
+		},
+	}
+	fixture.api.tokenService.tokens = barrier
+
+	rotateCh := make(chan concurrentHTTPResponse, 1)
+	go func() {
+		response := apiJSON(t, fixture.api, http.MethodPost, "/api/v1/tokens/"+tokenID+"/rotate", fixture.ownerToken, "revoke-vs-rotate", nil)
+		rotateCh <- concurrentHTTPResponse{status: response.Code, body: append([]byte(nil), response.Body.Bytes()...)}
+	}()
+	barrier.wait(t, 3)
+	revokeCh := make(chan concurrentHTTPResponse, 1)
+	go func() {
+		response := apiJSON(t, fixture.api, http.MethodPost, "/api/v1/tokens/"+tokenID+"/revoke", fixture.ownerToken, "", nil)
+		revokeCh <- concurrentHTTPResponse{status: response.Code, body: append([]byte(nil), response.Body.Bytes()...)}
+	}()
+	barrier.wait(t, 5)
+	barrier.release(3)
+	rotate := <-rotateCh
+	if rotate.status != http.StatusCreated {
+		t.Fatalf("forced rotation winner status = %d: %s", rotate.status, rotate.body)
+	}
+	barrier.release(5)
+	revoke := <-revokeCh
+	if revoke.status != http.StatusOK {
+		t.Fatalf("revoke loser status = %d: %s", revoke.status, revoke.body)
+	}
+	rotated := countTokenLifecycleAudits(t, fixture.api.DB, tokenID, "token.rotated")
+	revoked := countTokenLifecycleAudits(t, fixture.api.DB, tokenID, "token.revoked")
+	if rotated != 1 || revoked != 0 {
+		t.Fatalf("lifecycle audits rotated=%d revoked=%d, want 1/0", rotated, revoked)
+	}
+}
+
+type concurrentHTTPResponse struct {
+	status int
+	body   []byte
+}
+
+type nthAgentGetBarrier struct {
+	storage.AgentRepository
+	targetID   string
+	targetCall int
+	reached    chan struct{}
+	release    chan struct{}
+	mu         sync.Mutex
+	calls      int
+}
+
+func (r *nthAgentGetBarrier) Get(ctx context.Context, id string) (storage.Agent, error) {
+	agent, err := r.AgentRepository.Get(ctx, id)
+	if id != r.targetID || err != nil {
+		return agent, err
+	}
+	r.mu.Lock()
+	r.calls++
+	call := r.calls
+	r.mu.Unlock()
+	if call == r.targetCall {
+		close(r.reached)
+		<-r.release
+	}
+	return agent, nil
+}
+
+type failingAuditRepository struct {
+	storage.AuditRepository
+	err error
+}
+
+func (r failingAuditRepository) Create(context.Context, storage.AuditLog) error { return r.err }
+
+type failingUserGetRepository struct {
+	storage.UserRepository
+	err error
+}
+
+func (r failingUserGetRepository) Get(context.Context, string) (storage.User, error) {
+	return storage.User{}, r.err
+}
+
+type failingAgentGetRepository struct {
+	storage.AgentRepository
+	err error
+}
+
+func (r failingAgentGetRepository) Get(context.Context, string) (storage.Agent, error) {
+	return storage.Agent{}, r.err
+}
+
+type failingNodeGetRepository struct {
+	storage.NodeRepository
+	err error
+}
+
+func (r failingNodeGetRepository) Get(context.Context, string) (storage.ServerNode, error) {
+	return storage.ServerNode{}, r.err
+}
+
+type twoPhaseTokenGetBarrier struct {
+	storage.ServiceTokenRepository
+	targetID string
+	phaseN   int
+	mu       sync.Mutex
+	calls    int
+	reached  [2]chan struct{}
+	release  [2]chan struct{}
+}
+
+func newTwoPhaseTokenGetBarrier(base storage.ServiceTokenRepository, targetID string, phaseN int) *twoPhaseTokenGetBarrier {
+	return &twoPhaseTokenGetBarrier{
+		ServiceTokenRepository: base, targetID: targetID, phaseN: phaseN,
+		reached: [2]chan struct{}{make(chan struct{}), make(chan struct{})},
+		release: [2]chan struct{}{make(chan struct{}), make(chan struct{})},
+	}
+}
+
+func (r *twoPhaseTokenGetBarrier) Get(ctx context.Context, id string) (storage.ServiceToken, error) {
+	token, err := r.ServiceTokenRepository.Get(ctx, id)
+	if id != r.targetID || err != nil {
+		return token, err
+	}
+	r.mu.Lock()
+	r.calls++
+	call := r.calls
+	phase := (call - 1) / r.phaseN
+	within := (call-1)%r.phaseN + 1
+	if phase < 2 && within == r.phaseN {
+		close(r.reached[phase])
+	}
+	r.mu.Unlock()
+	if phase < 2 {
+		<-r.release[phase]
+	}
+	return token, nil
+}
+
+func (r *twoPhaseTokenGetBarrier) waitAndRelease(t *testing.T, phase int) {
+	t.Helper()
+	select {
+	case <-r.reached[phase-1]:
+	case <-time.After(2 * time.Second):
+		t.Fatalf("Token Get phase %d did not reach %d readers", phase, r.phaseN)
+	}
+	close(r.release[phase-1])
+}
+
+type tokenGetBlock struct {
+	reached chan struct{}
+	release chan struct{}
+}
+
+type selectedTokenGetBarrier struct {
+	storage.ServiceTokenRepository
+	targetID string
+	mu       sync.Mutex
+	calls    int
+	blocked  map[int]tokenGetBlock
+}
+
+func (r *selectedTokenGetBarrier) Get(ctx context.Context, id string) (storage.ServiceToken, error) {
+	token, err := r.ServiceTokenRepository.Get(ctx, id)
+	if id != r.targetID || err != nil {
+		return token, err
+	}
+	r.mu.Lock()
+	r.calls++
+	block, blocked := r.blocked[r.calls]
+	r.mu.Unlock()
+	if blocked {
+		close(block.reached)
+		<-block.release
+	}
+	return token, nil
+}
+
+func (r *selectedTokenGetBarrier) wait(t *testing.T, call int) {
+	t.Helper()
+	select {
+	case <-r.blocked[call].reached:
+	case <-time.After(2 * time.Second):
+		t.Fatalf("Token Get call %d did not reach barrier", call)
+	}
+}
+
+func (r *selectedTokenGetBarrier) release(call int) { close(r.blocked[call].release) }
+
+func countTokenLifecycleAudits(t *testing.T, db *storage.DB, tokenID, action string) int {
+	t.Helper()
+	page, err := db.Audits().List(context.Background(), "", 500)
+	if err != nil {
+		t.Fatal(err)
+	}
+	count := 0
+	for _, audit := range page.Items {
+		if audit.Action == action && audit.ResourceID == tokenID {
+			count++
+		}
+	}
+	return count
 }
 
 func assertSafeTokenAuditDetails(t *testing.T, raw string) {
diff -ruN --exclude=*.__ABSENT__ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-4-after/internal/server/token_service.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-4-fix1-after/internal/server/token_service.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-4-after/internal/server/token_service.go	2026-09-06 15:41:34
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-4-fix1-after/internal/server/token_service.go	2026-09-06 16:08:23
@@ -109,8 +109,8 @@
 	return out, nil
 }
 
-// Create validates and plans credential material before atomically committing
-// the Token, audit event, and non-secret replay metadata.
+// Create performs a fast preflight before repeating the authoritative
+// authorization checks and credential write in one database transaction.
 func (s *TokenService) Create(ctx context.Context, actorUserID, idempotencyKey string, input auth.CreateTokenInput) (TokenView, error) {
 	fingerprint, err := mutationFingerprint("token.create", input)
 	if err != nil {
@@ -122,24 +122,33 @@
 	planner := &plannedTokenRepository{base: s.tokens}
 	credentials := auth.NewCredentialService(planner, s.users, s.agents, s.nodes, s.policies)
 	defer credentials.Close()
-	created, err := credentials.Create(ctx, input)
+	_, err = credentials.Create(ctx, input)
 	if err != nil {
 		return TokenView{}, invalidCredentialRequest(err)
 	}
 	if planner.created == nil {
 		return TokenView{}, errors.New("credential planner did not produce a token")
 	}
-	record := *planner.created
-	view, err := s.view(ctx, record)
-	if err != nil {
-		return TokenView{}, err
-	}
-	view.Secret = created.Secret
-	return s.persistMutation(ctx, actorUserID, idempotencyKey, "token.create", fingerprint, view, func(tokens storage.ServiceTokenRepository, audits storage.AuditRepository) error {
-		if err := tokens.Create(ctx, record); err != nil {
-			return err
+	return s.persistMutation(ctx, actorUserID, idempotencyKey, "token.create", fingerprint, func(repos storage.ServiceTokenMutationRepositories) (TokenView, error) {
+		credentials := auth.NewCredentialService(repos.Tokens, repos.Users, repos.Agents, repos.Nodes, s.policies)
+		defer credentials.Close()
+		created, err := credentials.Create(ctx, input)
+		if err != nil {
+			return TokenView{}, invalidCredentialRequest(err)
 		}
-		return audits.Create(ctx, tokenAudit(actorUserID, "token.created", record.ID, record, "", ""))
+		record, err := repos.Tokens.Get(ctx, created.TokenID)
+		if err != nil {
+			return TokenView{}, err
+		}
+		view, err := tokenView(ctx, record, repos.Users, repos.Agents, repos.Nodes)
+		if err != nil {
+			return TokenView{}, err
+		}
+		view.Secret = created.Secret
+		if err := repos.Audits.Create(ctx, tokenAudit(actorUserID, "token.created", record.ID, record, "", "")); err != nil {
+			return TokenView{}, err
+		}
+		return view, nil
 	})
 }
 
@@ -157,14 +166,13 @@
 	if replay, found, err := s.completedReplay(ctx, actorUserID, idempotencyKey, operation, fingerprint); found || err != nil {
 		return replay, err
 	}
-	original, err := s.tokens.Get(ctx, id)
-	if err != nil {
+	if _, err := s.tokens.Get(ctx, id); err != nil {
 		return TokenView{}, err
 	}
 	planner := &plannedTokenRepository{base: s.tokens}
 	credentials := auth.NewCredentialService(planner, s.users, s.agents, s.nodes, s.policies)
 	defer credentials.Close()
-	created, err := credentials.Rotate(ctx, id)
+	_, err = credentials.Rotate(ctx, id)
 	if err != nil {
 		if errors.Is(err, auth.ErrUnauthenticated) || errors.Is(err, storage.ErrServiceTokenRevoked) {
 			// A concurrent winner may have committed the old-token revocation and
@@ -179,18 +187,41 @@
 	if planner.rotation == nil {
 		return TokenView{}, errors.New("credential planner did not produce a rotation")
 	}
-	replacement := planner.rotation.replacement
-	view, err := s.view(ctx, replacement)
-	if err != nil {
-		return TokenView{}, err
-	}
-	view.Secret = created.Secret
-	return s.persistMutation(ctx, actorUserID, idempotencyKey, operation, fingerprint, view, func(tokens storage.ServiceTokenRepository, audits storage.AuditRepository) error {
-		if err := tokens.Rotate(ctx, id, replacement, planner.rotation.when); err != nil {
-			return err
+	view, err := s.persistMutation(ctx, actorUserID, idempotencyKey, operation, fingerprint, func(repos storage.ServiceTokenMutationRepositories) (TokenView, error) {
+		original, err := repos.Tokens.Get(ctx, id)
+		if err != nil {
+			return TokenView{}, err
 		}
-		return audits.Create(ctx, tokenAudit(actorUserID, "token.rotated", id, original, replacement.ID, ""))
+		credentials := auth.NewCredentialService(repos.Tokens, repos.Users, repos.Agents, repos.Nodes, s.policies)
+		defer credentials.Close()
+		created, err := credentials.Rotate(ctx, id)
+		if err != nil {
+			return TokenView{}, err
+		}
+		replacement, err := repos.Tokens.Get(ctx, created.TokenID)
+		if err != nil {
+			return TokenView{}, err
+		}
+		view, err := tokenView(ctx, replacement, repos.Users, repos.Agents, repos.Nodes)
+		if err != nil {
+			return TokenView{}, err
+		}
+		view.Secret = created.Secret
+		if err := repos.Audits.Create(ctx, tokenAudit(actorUserID, "token.rotated", id, original, replacement.ID, "")); err != nil {
+			return TokenView{}, err
+		}
+		return view, nil
 	})
+	if err == nil {
+		return view, nil
+	}
+	if errors.Is(err, auth.ErrUnauthenticated) || errors.Is(err, storage.ErrServiceTokenRevoked) || errors.Is(err, sql.ErrNoRows) {
+		if replay, found, replayErr := s.completedReplay(ctx, actorUserID, idempotencyKey, operation, fingerprint); found || replayErr != nil {
+			return replay, replayErr
+		}
+		return TokenView{}, fmt.Errorf("%w: token cannot be rotated", errTokenConflict)
+	}
+	return TokenView{}, err
 }
 
 // Revoke commits the lifecycle timestamp and audit record together.
@@ -203,19 +234,32 @@
 	if record.RevokedAt != nil {
 		return s.view(ctx, record)
 	}
-	when := time.Now().UTC()
+	var result storage.ServiceToken
 	err = s.db.ServiceTokenMutationTransaction(ctx, func(tokens storage.ServiceTokenRepository, audits storage.AuditRepository, _ storage.IdempotencyRepository) error {
+		current, err := tokens.Get(ctx, id)
+		if err != nil {
+			return err
+		}
+		if current.RevokedAt != nil {
+			result = current
+			return nil
+		}
+		when := time.Now().UTC()
 		if err := tokens.Revoke(ctx, id, when); err != nil {
 			return err
 		}
-		return audits.Create(ctx, tokenAudit(actorUserID, "token.revoked", id, record, "", ""))
+		if err := audits.Create(ctx, tokenAudit(actorUserID, "token.revoked", id, current, "", "")); err != nil {
+			return err
+		}
+		current.RevokedAt = &when
+		current.UpdatedAt = when
+		result = current
+		return nil
 	})
 	if err != nil {
 		return TokenView{}, err
 	}
-	record.RevokedAt = &when
-	record.UpdatedAt = when
-	return s.view(ctx, record)
+	return s.view(ctx, result)
 }
 
 // RecordAuthorizationDenied persists only allowlisted non-secret identifiers.
@@ -231,11 +275,11 @@
 	return s.audits.Create(ctx, tokenAudit(actorUserID, "token.authorization_denied", resourceID, record, "", reasonCode))
 }
 
-func (s *TokenService) persistMutation(ctx context.Context, actorUserID, key, operation, fingerprint string, issued TokenView, mutate func(storage.ServiceTokenRepository, storage.AuditRepository) error) (TokenView, error) {
+func (s *TokenService) persistMutation(ctx context.Context, actorUserID, key, operation, fingerprint string, mutate func(storage.ServiceTokenMutationRepositories) (TokenView, error)) (TokenView, error) {
 	var result TokenView
-	err := s.db.ServiceTokenMutationTransaction(ctx, func(tokens storage.ServiceTokenRepository, audits storage.AuditRepository, idempotency storage.IdempotencyRepository) error {
+	err := s.db.ServiceTokenAuthorizedMutationTransaction(ctx, func(repos storage.ServiceTokenMutationRepositories) error {
 		if key != "" {
-			atomic, ok := idempotency.(storage.AtomicIdempotencyRepository)
+			atomic, ok := repos.Idempotency.(storage.AtomicIdempotencyRepository)
 			if !ok {
 				return errors.New("atomic idempotency repository is required")
 			}
@@ -252,7 +296,8 @@
 				return nil
 			}
 		}
-		if err := mutate(tokens, audits); err != nil {
+		issued, err := mutate(repos)
+		if err != nil {
 			return err
 		}
 		result = issued
@@ -267,7 +312,7 @@
 			return err
 		}
 		expiresAt := time.Now().UTC().Add(24 * time.Hour)
-		return idempotency.(storage.AtomicIdempotencyRepository).Update(ctx, storage.IdempotencyRecord{
+		return repos.Idempotency.(storage.AtomicIdempotencyRepository).Update(ctx, storage.IdempotencyRecord{
 			Key: key, UserID: actorUserID, Response: string(encoded), StatusCode: 201, ExpiresAt: &expiresAt,
 		})
 	})
@@ -321,51 +366,71 @@
 }
 
 func (s *TokenService) view(ctx context.Context, record storage.ServiceToken) (TokenView, error) {
+	return tokenView(ctx, record, s.users, s.agents, s.nodes)
+}
+
+func tokenView(ctx context.Context, record storage.ServiceToken, users storage.UserRepository, agents storage.AgentRepository, nodes storage.NodeRepository) (TokenView, error) {
 	var scope auth.TokenScope
 	if err := json.Unmarshal([]byte(record.Scope), &scope); err != nil {
 		return TokenView{}, fmt.Errorf("decode token scope: %w", err)
 	}
+	status, err := tokenStatus(ctx, record, scope, users, agents, nodes)
+	if err != nil {
+		return TokenView{}, err
+	}
 	return TokenView{
 		ID: record.ID, Type: record.Type, OwnerUserID: record.OwnerUserID, AgentID: record.AgentID, NodeID: record.NodeID,
-		Prefix: record.Prefix, Scope: scope, Status: s.status(ctx, record, scope), ExpiresAt: record.ExpiresAt,
+		Prefix: record.Prefix, Scope: scope, Status: status, ExpiresAt: record.ExpiresAt,
 		RevokedAt: record.RevokedAt, LastUsedAt: record.LastUsedAt, CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt,
 	}, nil
 }
 
-func (s *TokenService) status(ctx context.Context, record storage.ServiceToken, scope auth.TokenScope) string {
+func tokenStatus(ctx context.Context, record storage.ServiceToken, scope auth.TokenScope, users storage.UserRepository, agents storage.AgentRepository, nodes storage.NodeRepository) (string, error) {
 	now := time.Now().UTC()
 	if record.RevokedAt != nil {
-		return "revoked"
+		return "revoked", nil
 	}
 	if record.ExpiresAt != nil && !record.ExpiresAt.After(now) {
-		return "expired"
+		return "expired", nil
 	}
-	owner, err := s.users.Get(ctx, record.OwnerUserID)
-	if err != nil || owner.Disabled {
-		return "unavailable"
+	owner, err := users.Get(ctx, record.OwnerUserID)
+	if errors.Is(err, sql.ErrNoRows) || (err == nil && owner.Disabled) {
+		return "unavailable", nil
 	}
+	if err != nil {
+		return "", err
+	}
 	switch record.Type {
 	case storage.TokenTypeAgent:
-		agent, err := s.agents.Get(ctx, record.AgentID)
-		if err != nil || !agent.Enabled || agent.OwnerUserID != record.OwnerUserID {
-			return "unavailable"
+		agent, err := agents.Get(ctx, record.AgentID)
+		if errors.Is(err, sql.ErrNoRows) || (err == nil && (!agent.Enabled || agent.OwnerUserID != record.OwnerUserID)) {
+			return "unavailable", nil
 		}
+		if err != nil {
+			return "", err
+		}
 	case storage.TokenTypeClient:
 		for _, agentID := range scope.AgentIDs {
-			agent, err := s.agents.Get(ctx, agentID)
-			if err != nil || !agent.Enabled || agent.OwnerUserID != record.OwnerUserID {
-				return "unavailable"
+			agent, err := agents.Get(ctx, agentID)
+			if errors.Is(err, sql.ErrNoRows) || (err == nil && (!agent.Enabled || agent.OwnerUserID != record.OwnerUserID)) {
+				return "unavailable", nil
 			}
+			if err != nil {
+				return "", err
+			}
 		}
 	case storage.TokenTypeServerNode:
-		node, err := s.nodes.Get(ctx, record.NodeID)
-		if err != nil || (node.ExpiresAt != nil && !node.ExpiresAt.After(now)) {
-			return "unavailable"
+		node, err := nodes.Get(ctx, record.NodeID)
+		if errors.Is(err, sql.ErrNoRows) || (err == nil && node.ExpiresAt != nil && !node.ExpiresAt.After(now)) {
+			return "unavailable", nil
 		}
+		if err != nil {
+			return "", err
+		}
 	default:
-		return "unavailable"
+		return "unavailable", nil
 	}
-	return "active"
+	return "active", nil
 }
 
 func invalidCredentialRequest(err error) error {
diff -ruN --exclude=*.__ABSENT__ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-4-after/internal/storage/db.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-4-fix1-after/internal/storage/db.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-4-after/internal/storage/db.go	2026-09-06 15:41:34
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-4-fix1-after/internal/storage/db.go	2026-09-06 16:08:23
@@ -38,6 +38,19 @@
 	idempotency   IdempotencyRepository
 }
 
+// ServiceTokenMutationRepositories groups every repository that participates
+// in an authorized service-token mutation. Each repository is bound to the
+// same transaction so authorization facts and the credential write cannot
+// observe different database states.
+type ServiceTokenMutationRepositories struct {
+	Tokens      ServiceTokenRepository
+	Audits      AuditRepository
+	Idempotency IdempotencyRepository
+	Users       UserRepository
+	Agents      AgentRepository
+	Nodes       NodeRepository
+}
+
 // ServiceTokenTransaction atomically applies service-token lifecycle changes
 // and their audit record using repositories bound to the same transaction.
 // It is retained for Task 2 callers; mutation paths that also persist replay
@@ -51,15 +64,35 @@
 // ServiceTokenMutationTransaction commits a service-token mutation, its audit
 // event, and non-secret idempotency metadata as one database fact.
 func (d *DB) ServiceTokenMutationTransaction(ctx context.Context, fn func(ServiceTokenRepository, AuditRepository, IdempotencyRepository) error) error {
+	return d.ServiceTokenAuthorizedMutationTransaction(ctx, func(repos ServiceTokenMutationRepositories) error {
+		return fn(repos.Tokens, repos.Audits, repos.Idempotency)
+	})
+}
+
+// ServiceTokenAuthorizedMutationTransaction commits authorization reads,
+// service-token lifecycle state, audit events, and replay metadata as one
+// database fact. SQLite takes a writer lock before any authorization read;
+// MySQL repositories use locking reads for mutable resource facts.
+func (d *DB) ServiceTokenAuthorizedMutationTransaction(ctx context.Context, fn func(ServiceTokenMutationRepositories) error) error {
 	tx, err := d.sql.BeginTx(ctx, nil)
 	if err != nil {
 		return err
 	}
 	defer func() { _ = tx.Rollback() }()
-	tokens := &serviceTokenRepo{db: tx, driver: d.driver}
-	audits := &auditRepo{db: tx}
-	idempotency := &idempotencyRepo{db: tx}
-	if err := fn(tokens, audits, idempotency); err != nil {
+	if d.driver == DriverSQLite {
+		if _, err := tx.ExecContext(ctx, `UPDATE schema_meta SET version=version WHERE id=1`); err != nil {
+			return err
+		}
+	}
+	repos := ServiceTokenMutationRepositories{
+		Tokens:      &serviceTokenRepo{db: tx, driver: d.driver, lockReads: true},
+		Audits:      &auditRepo{db: tx},
+		Idempotency: &idempotencyRepo{db: tx},
+		Users:       &userRepo{db: tx, driver: d.driver, lockReads: true},
+		Agents:      &agentRepo{db: tx, driver: d.driver, lockReads: true},
+		Nodes:       &nodeRepo{db: tx, driver: d.driver, lockReads: true},
+	}
+	if err := fn(repos); err != nil {
 		return err
 	}
 	return tx.Commit()
@@ -77,7 +110,7 @@
 		_ = tx.Rollback()
 		return err
 	}
-	users := &userRepo{db: tx}
+	users := &userRepo{db: tx, driver: d.driver, lockReads: true}
 	tokens := &tokenRepo{db: tx}
 	audits := &auditRepo{db: tx}
 	if err := fn(users, tokens, audits); err != nil {
@@ -146,13 +179,13 @@
 	return &DB{
 		sql:           db,
 		driver:        driver,
-		users:         &userRepo{db},
-		tokens:        &tokenRepo{db},
+		users:         &userRepo{db: db, driver: driver},
+		tokens:        &tokenRepo{db: db},
 		serviceTokens: NewServiceTokenRepositoryWithDriver(db, driver),
-		agents:        &agentRepo{db},
+		agents:        &agentRepo{db: db, driver: driver},
 		policies:      &policyRepo{db},
 		tunnels:       &tunnelRepo{db},
-		nodes:         &nodeRepo{db},
+		nodes:         &nodeRepo{db: db, driver: driver},
 		metadata:      NewAgentMetadataRepositoryWithDriver(db, driver),
 		leases:        NewLeaseRepositoryWithDriver(db, driver),
 		audits:        &auditRepo{db},
diff -ruN --exclude=*.__ABSENT__ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-4-after/internal/storage/repository.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-4-fix1-after/internal/storage/repository.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-4-after/internal/storage/repository.go	2026-09-06 15:41:34
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-4-fix1-after/internal/storage/repository.go	2026-09-06 16:08:23
@@ -117,7 +117,9 @@
 
 // Constructor helpers are useful for services that own a database/sql handle
 // directly (for example tests or read-only reporting jobs).
-func NewUserRepository(db *sql.DB) UserRepository   { return &userRepo{db} }
+func NewUserRepository(db *sql.DB) UserRepository {
+	return &userRepo{db: db, driver: DriverSQLite}
+}
 func NewTokenRepository(db *sql.DB) TokenRepository { return &tokenRepo{db} }
 func NewServiceTokenRepository(db *sql.DB) ServiceTokenRepository {
 	return NewServiceTokenRepositoryWithDriver(db, DriverSQLite)
@@ -132,10 +134,14 @@
 	}
 	return &serviceTokenRepo{db: db, starter: db, driver: driver}
 }
-func NewAgentRepository(db *sql.DB) AgentRepository   { return &agentRepo{db} }
+func NewAgentRepository(db *sql.DB) AgentRepository {
+	return &agentRepo{db: db, driver: DriverSQLite}
+}
 func NewPolicyRepository(db *sql.DB) PolicyRepository { return &policyRepo{db} }
 func NewTunnelRepository(db *sql.DB) TunnelRepository { return &tunnelRepo{db} }
-func NewNodeRepository(db *sql.DB) NodeRepository     { return &nodeRepo{db} }
+func NewNodeRepository(db *sql.DB) NodeRepository {
+	return &nodeRepo{db: db, driver: DriverSQLite}
+}
 func NewAgentMetadataRepository(db *sql.DB) AgentMetadataRepository {
 	return &agentMetadataRepo{db: db, driver: DriverSQLite}
 }
@@ -175,12 +181,18 @@
 func (r *sqlRepositories) CreateUser(ctx context.Context, v User) error {
 	return r.users().Create(ctx, v)
 }
-func (r *sqlRepositories) users() *userRepo      { return &userRepo{r.db} }
-func (r *sqlRepositories) tokens() *tokenRepo    { return &tokenRepo{r.db} }
-func (r *sqlRepositories) agents() *agentRepo    { return &agentRepo{r.db} }
+func (r *sqlRepositories) users() *userRepo {
+	return &userRepo{db: r.db, driver: DriverSQLite}
+}
+func (r *sqlRepositories) tokens() *tokenRepo { return &tokenRepo{r.db} }
+func (r *sqlRepositories) agents() *agentRepo {
+	return &agentRepo{db: r.db, driver: DriverSQLite}
+}
 func (r *sqlRepositories) policies() *policyRepo { return &policyRepo{r.db} }
 func (r *sqlRepositories) tunnels() *tunnelRepo  { return &tunnelRepo{r.db} }
-func (r *sqlRepositories) nodes() *nodeRepo      { return &nodeRepo{r.db} }
+func (r *sqlRepositories) nodes() *nodeRepo {
+	return &nodeRepo{db: r.db, driver: DriverSQLite}
+}
 func (r *sqlRepositories) leases() *leaseRepo {
 	return NewLeaseRepositoryWithDriver(r.db, DriverSQLite).(*leaseRepo)
 }
@@ -271,7 +283,11 @@
 	BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
 }
 
-type userRepo struct{ db dbExecutor }
+type userRepo struct {
+	db        dbExecutor
+	driver    string
+	lockReads bool
+}
 
 func (r *userRepo) Create(ctx context.Context, v User) error {
 	v.ID, v.CreatedAt, v.UpdatedAt = stamp(v.ID, v.CreatedAt, v.UpdatedAt, "user")
@@ -284,7 +300,11 @@
 func (r *userRepo) Get(ctx context.Context, id string) (User, error) {
 	var v User
 	var created, updated string
-	err := r.db.QueryRowContext(ctx, `SELECT id,username,role,password_hash,disabled,created_at,updated_at FROM users WHERE id=?`, id).Scan(&v.ID, &v.Username, &v.Role, &v.PasswordHash, &v.Disabled, &created, &updated)
+	query := `SELECT id,username,role,password_hash,disabled,created_at,updated_at FROM users WHERE id=?`
+	if r.lockReads && r.driver == DriverMySQL {
+		query += ` FOR UPDATE`
+	}
+	err := r.db.QueryRowContext(ctx, query, id).Scan(&v.ID, &v.Username, &v.Role, &v.PasswordHash, &v.Disabled, &created, &updated)
 	v.CreatedAt = parseTime(created)
 	v.UpdatedAt = parseTime(updated)
 	return v, err
@@ -292,7 +312,11 @@
 func (r *userRepo) GetByUsername(ctx context.Context, n string) (User, error) {
 	var v User
 	var created, updated string
-	err := r.db.QueryRowContext(ctx, `SELECT id,username,role,password_hash,disabled,created_at,updated_at FROM users WHERE username=?`, n).Scan(&v.ID, &v.Username, &v.Role, &v.PasswordHash, &v.Disabled, &created, &updated)
+	query := `SELECT id,username,role,password_hash,disabled,created_at,updated_at FROM users WHERE username=?`
+	if r.lockReads && r.driver == DriverMySQL {
+		query += ` FOR UPDATE`
+	}
+	err := r.db.QueryRowContext(ctx, query, n).Scan(&v.ID, &v.Username, &v.Role, &v.PasswordHash, &v.Disabled, &created, &updated)
 	v.CreatedAt = parseTime(created)
 	v.UpdatedAt = parseTime(updated)
 	return v, err
@@ -443,9 +467,10 @@
 const serviceTokenColumns = `id,token_type,owner_user_id,agent_id,node_id,token_prefix,token_hash,scope,expires_at,revoked_at,last_used_at,created_at,updated_at`
 
 type serviceTokenRepo struct {
-	db      dbExecutor
-	starter transactionStarter
-	driver  string
+	db        dbExecutor
+	starter   transactionStarter
+	driver    string
+	lockReads bool
 }
 
 func (r *serviceTokenRepo) Create(ctx context.Context, v ServiceToken) error {
@@ -459,11 +484,19 @@
 }
 
 func (r *serviceTokenRepo) Get(ctx context.Context, id string) (ServiceToken, error) {
-	return scanServiceToken(r.db.QueryRowContext(ctx, `SELECT `+serviceTokenColumns+` FROM service_tokens WHERE id=?`, id))
+	query := `SELECT ` + serviceTokenColumns + ` FROM service_tokens WHERE id=?`
+	if r.lockReads && r.driver == DriverMySQL {
+		query += ` FOR UPDATE`
+	}
+	return scanServiceToken(r.db.QueryRowContext(ctx, query, id))
 }
 
 func (r *serviceTokenRepo) GetByHash(ctx context.Context, hash string) (ServiceToken, error) {
-	return scanServiceToken(r.db.QueryRowContext(ctx, `SELECT `+serviceTokenColumns+` FROM service_tokens WHERE token_hash=?`, hash))
+	query := `SELECT ` + serviceTokenColumns + ` FROM service_tokens WHERE token_hash=?`
+	if r.lockReads && r.driver == DriverMySQL {
+		query += ` FOR UPDATE`
+	}
+	return scanServiceToken(r.db.QueryRowContext(ctx, query, hash))
 }
 
 func (r *serviceTokenRepo) List(ctx context.Context, filter ServiceTokenFilter, cursor string, limit int) (Page[ServiceToken], error) {
@@ -522,8 +555,25 @@
 
 func (r *serviceTokenRepo) Revoke(ctx context.Context, id string, when time.Time) error {
 	when = timeOrNow(when)
-	res, err := r.db.ExecContext(ctx, `UPDATE service_tokens SET revoked_at=?,updated_at=? WHERE id=?`, tm(when), tm(when), id)
-	return checkAffected(res, err)
+	res, err := r.db.ExecContext(ctx, `UPDATE service_tokens SET revoked_at=?,updated_at=? WHERE id=? AND revoked_at IS NULL`, tm(when), tm(when), id)
+	if err != nil {
+		return err
+	}
+	affected, err := res.RowsAffected()
+	if err != nil {
+		return err
+	}
+	if affected > 0 {
+		return nil
+	}
+	record, err := r.Get(ctx, id)
+	if err != nil {
+		return err
+	}
+	if record.RevokedAt != nil {
+		return ErrServiceTokenRevoked
+	}
+	return fmt.Errorf("revoke service token %q made no state transition", id)
 }
 
 func (r *serviceTokenRepo) TouchLastUsed(ctx context.Context, id string, when time.Time) error {
@@ -602,7 +652,11 @@
 	return token, nil
 }
 
-type agentRepo struct{ db *sql.DB }
+type agentRepo struct {
+	db        dbExecutor
+	driver    string
+	lockReads bool
+}
 
 func (r *agentRepo) Create(ctx context.Context, v Agent) error {
 	v.ID, v.CreatedAt, v.UpdatedAt = stamp(v.ID, v.CreatedAt, v.UpdatedAt, "agent")
@@ -615,7 +669,11 @@
 func (r *agentRepo) Get(ctx context.Context, id string) (Agent, error) {
 	var v Agent
 	var c, u string
-	err := r.db.QueryRowContext(ctx, `SELECT id,name,owner_user_id,capabilities,enabled,created_at,updated_at FROM agents WHERE id=?`, id).Scan(&v.ID, &v.Name, &v.OwnerUserID, &v.Capabilities, &v.Enabled, &c, &u)
+	query := `SELECT id,name,owner_user_id,capabilities,enabled,created_at,updated_at FROM agents WHERE id=?`
+	if r.lockReads && r.driver == DriverMySQL {
+		query += ` FOR UPDATE`
+	}
+	err := r.db.QueryRowContext(ctx, query, id).Scan(&v.ID, &v.Name, &v.OwnerUserID, &v.Capabilities, &v.Enabled, &c, &u)
 	v.CreatedAt = parseTime(c)
 	v.UpdatedAt = parseTime(u)
 	return v, err
@@ -854,7 +912,11 @@
 	return v
 }
 
-type nodeRepo struct{ db *sql.DB }
+type nodeRepo struct {
+	db        dbExecutor
+	driver    string
+	lockReads bool
+}
 
 func (r *nodeRepo) Create(ctx context.Context, v ServerNode) error {
 	v.ID, v.CreatedAt, v.UpdatedAt = stamp(v.ID, v.CreatedAt, v.UpdatedAt, "node")
@@ -867,7 +929,11 @@
 func (r *nodeRepo) Get(ctx context.Context, id string) (ServerNode, error) {
 	var v ServerNode
 	var meta, seen, exp, created, updated sql.NullString
-	err := r.db.QueryRowContext(ctx, `SELECT id,address,epoch,metadata,last_seen_at,expires_at,created_at,updated_at FROM server_nodes WHERE id=?`, id).Scan(&v.ID, &v.Address, &v.Epoch, &meta, &seen, &exp, &created, &updated)
+	query := `SELECT id,address,epoch,metadata,last_seen_at,expires_at,created_at,updated_at FROM server_nodes WHERE id=?`
+	if r.lockReads && r.driver == DriverMySQL {
+		query += ` FOR UPDATE`
+	}
+	err := r.db.QueryRowContext(ctx, query, id).Scan(&v.ID, &v.Address, &v.Epoch, &meta, &seen, &exp, &created, &updated)
 	if meta.Valid {
 		v.Metadata = meta.String
 	}
diff -ruN --exclude=*.__ABSENT__ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-4-after/internal/storage/service_token_test.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-4-fix1-after/internal/storage/service_token_test.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-4-after/internal/storage/service_token_test.go	2026-09-06 15:41:34
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-4-fix1-after/internal/storage/service_token_test.go	2026-09-06 16:08:23
@@ -264,6 +264,70 @@
 	}
 }
 
+func TestServiceTokenRepositoryRevokeCASPreservesFirstTimestamp(t *testing.T) {
+	db := newTestDB(t)
+	ctx := context.Background()
+	token := ServiceToken{ID: "contract-service-token-revoke-cas", Prefix: "revoke-cas", TokenHash: "contract-service-token-hash-revoke-cas", Scope: `{}`, Type: TokenTypeClient}
+	if err := db.ServiceTokens().Create(ctx, token); err != nil {
+		t.Fatal(err)
+	}
+	first := time.Date(2026, 9, 6, 10, 0, 0, 0, time.UTC)
+	second := first.Add(time.Hour)
+	if err := db.ServiceTokens().Revoke(ctx, token.ID, first); err != nil {
+		t.Fatalf("first revoke: %v", err)
+	}
+	if err := db.ServiceTokens().Revoke(ctx, token.ID, second); !errors.Is(err, ErrServiceTokenRevoked) {
+		t.Fatalf("second revoke error = %v, want ErrServiceTokenRevoked", err)
+	}
+	stored, err := db.ServiceTokens().Get(ctx, token.ID)
+	if err != nil {
+		t.Fatal(err)
+	}
+	if stored.RevokedAt == nil || !stored.RevokedAt.Equal(first) {
+		t.Fatalf("revokedAt = %v, want first transition %v", stored.RevokedAt, first)
+	}
+}
+
+func TestServiceTokenAuthorizedMutationTransactionBindsResourceRepositories(t *testing.T) {
+	db := newTestDB(t)
+	ctx := context.Background()
+	user := User{ID: "contract-service-token-auth-user", Username: "contract-service-token-auth-user", PasswordHash: "hash"}
+	agent := Agent{ID: "contract-service-token-auth-agent", Name: "contract-service-token-auth-agent", OwnerUserID: user.ID, Enabled: true}
+	node := ServerNode{ID: "contract-service-token-auth-node", Address: "127.0.0.1:9443"}
+	if err := db.Users().Create(ctx, user); err != nil {
+		t.Fatal(err)
+	}
+	if err := db.Agents().Create(ctx, agent); err != nil {
+		t.Fatal(err)
+	}
+	if err := db.Nodes().Create(ctx, node); err != nil {
+		t.Fatal(err)
+	}
+	wantErr := errors.New("rollback authorized mutation")
+	token := ServiceToken{ID: "contract-service-token-auth-rollback", OwnerUserID: user.ID, Prefix: "auth", TokenHash: "contract-service-token-hash-auth-rollback", Scope: `{}`, Type: TokenTypeClient}
+	err := db.ServiceTokenAuthorizedMutationTransaction(ctx, func(repos ServiceTokenMutationRepositories) error {
+		if got, err := repos.Users.Get(ctx, user.ID); err != nil || got.ID != user.ID {
+			t.Fatalf("transaction user = %+v, err=%v", got, err)
+		}
+		if got, err := repos.Agents.Get(ctx, agent.ID); err != nil || got.OwnerUserID != user.ID || !got.Enabled {
+			t.Fatalf("transaction Agent = %+v, err=%v", got, err)
+		}
+		if got, err := repos.Nodes.Get(ctx, node.ID); err != nil || got.ID != node.ID {
+			t.Fatalf("transaction node = %+v, err=%v", got, err)
+		}
+		if err := repos.Tokens.Create(ctx, token); err != nil {
+			return err
+		}
+		return wantErr
+	})
+	if !errors.Is(err, wantErr) {
+		t.Fatalf("authorized transaction error = %v, want %v", err, wantErr)
+	}
+	if _, err := db.ServiceTokens().Get(ctx, token.ID); !errors.Is(err, sql.ErrNoRows) {
+		t.Fatalf("authorized transaction rollback token error = %v, want sql.ErrNoRows", err)
+	}
+}
+
 func assertServiceToken(t *testing.T, got, want ServiceToken) {
 	t.Helper()
 	if got.ID != want.ID || got.OwnerUserID != want.OwnerUserID || got.AgentID != want.AgentID || got.NodeID != want.NodeID || got.Prefix != want.Prefix || got.TokenHash != want.TokenHash || got.Scope != want.Scope || got.Type != want.Type {
```
