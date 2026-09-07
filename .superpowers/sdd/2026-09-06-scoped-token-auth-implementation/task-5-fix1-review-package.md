# Task 5 fix round 1 review package

Fix base: Task 5 initial reviewed snapshot (HEAD remained `163fe12121d2f839ea4bf4907f55a8e7dd835057`)

## Changed files

```text
Files .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-after/internal/server/middleware.go and .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-fix1-after/internal/server/middleware.go differ
Files .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-after/internal/server/runtime.go and .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-fix1-after/internal/server/runtime.go differ
Files .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-after/internal/server/runtime_test.go and .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-fix1-after/internal/server/runtime_test.go differ
```

## Full fix-only diff

```diff
diff -ruN --exclude=*.__ABSENT__ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-after/internal/server/middleware.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-fix1-after/internal/server/middleware.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-after/internal/server/middleware.go	2026-09-06 16:50:34
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-fix1-after/internal/server/middleware.go	2026-09-06 17:13:52
@@ -14,7 +14,14 @@
 )
 
 type principalContextKey struct{}
+type agentAuthenticationContextKey struct{}
 
+type agentConnectionAuthentication struct {
+	Identity  auth.TokenIdentity
+	Principal auth.Principal
+	Legacy    bool
+}
+
 func principalFromContext(ctx context.Context) (auth.Principal, bool) {
 	p, ok := ctx.Value(principalContextKey{}).(auth.Principal)
 	return p, ok && p.UserID != ""
@@ -34,30 +41,36 @@
 	return strings.TrimSpace(v[7:])
 }
 
-func agentWebSocketHandshake(security config.SecurityConfig) func(*websocket.Config, *http.Request) error {
+func agentAuthenticationFromContext(ctx context.Context) (agentConnectionAuthentication, bool) {
+	authentication, ok := ctx.Value(agentAuthenticationContextKey{}).(agentConnectionAuthentication)
+	return authentication, ok
+}
+
+func agentWebSocketHandshake(security config.SecurityConfig, authenticate func(context.Context, string) (agentConnectionAuthentication, error)) func(*websocket.Config, *http.Request) error {
 	return func(wsConfig *websocket.Config, r *http.Request) error {
-		if bearerToken(r) == "" {
-			return auth.ErrUnauthenticated
-		}
 		if len(security.AllowedHosts) > 0 && !exactHostAllowed(r.Host, security.AllowedHosts) {
 			return fmt.Errorf("websocket host is not allowed")
 		}
-		if len(security.AllowedOrigins) == 0 {
-			origin, err := websocket.Origin(wsConfig, r)
-			if err != nil || origin == nil {
-				return fmt.Errorf("websocket origin is invalid")
-			}
-			wsConfig.Origin = origin
-			return nil
-		}
 		values := r.Header.Values("Origin")
 		if len(values) != 1 {
 			return fmt.Errorf("websocket origin is not allowed")
 		}
 		origin, normalized, ok := normalizeOrigin(values[0])
-		if !ok || !containsNormalizedOrigin(normalized, security.AllowedOrigins) {
+		if !ok || (len(security.AllowedOrigins) > 0 && !containsNormalizedOrigin(normalized, security.AllowedOrigins)) {
 			return fmt.Errorf("websocket origin is not allowed")
 		}
+		raw := bearerToken(r)
+		if raw == "" || authenticate == nil {
+			return auth.ErrUnauthenticated
+		}
+		authentication, err := authenticate(r.Context(), raw)
+		if err != nil {
+			return auth.ErrUnauthenticated
+		}
+		authenticatedRequest := r.WithContext(context.WithValue(r.Context(), agentAuthenticationContextKey{}, authentication))
+		authenticatedRequest.Header = r.Header.Clone()
+		authenticatedRequest.Header.Del("Authorization")
+		*r = *authenticatedRequest
 		wsConfig.Origin = origin
 		return nil
 	}
diff -ruN --exclude=*.__ABSENT__ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-after/internal/server/runtime.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-fix1-after/internal/server/runtime.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-after/internal/server/runtime.go	2026-09-06 16:50:34
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-fix1-after/internal/server/runtime.go	2026-09-06 17:13:52
@@ -19,6 +19,11 @@
 
 var ErrRuntimeDatabaseRequired = errors.New("server runtime: database is required")
 
+const (
+	agentHelloTimeout             = time.Second
+	agentWebSocketMaxPayloadBytes = protocol.MaxPayload + 16
+)
+
 type RuntimeConfig struct {
 	Security config.SecurityConfig
 	TLS      config.TLSConfig
@@ -73,7 +78,7 @@
 }
 
 func (r *ServerRuntime) agentWebSocketHandler() http.Handler {
-	return websocket.Server{Handler: r.serveAgentWS, Handshake: agentWebSocketHandshake(r.config.Security)}
+	return websocket.Server{Handler: r.serveAgentWS, Handshake: agentWebSocketHandshake(r.config.Security, r.preauthenticateAgentConnection)}
 }
 
 func (r *ServerRuntime) serveAgentWS(conn *websocket.Conn) {
@@ -81,6 +86,18 @@
 	if conn.Request() != nil {
 		ctx = conn.Request().Context()
 	}
+	authentication, ok := agentAuthenticationFromContext(ctx)
+	if !ok {
+		_ = conn.Close()
+		return
+	}
+	// The WebSocket message includes the 16-byte TunnelMesh frame header in
+	// addition to the protocol payload bounded by protocol.MaxPayload.
+	conn.MaxPayloadBytes = agentWebSocketMaxPayloadBytes
+	if err := conn.SetReadDeadline(time.Now().Add(agentHelloTimeout)); err != nil {
+		_ = conn.Close()
+		return
+	}
 	tr := &xNetWSFrameConn{conn: conn}
 	transport := NewWSFrameTransport(tr)
 	initial, err := transport.Receive()
@@ -93,7 +110,11 @@
 		_ = transport.Close()
 		return
 	}
-	tokenID, err := r.authenticateAgentConnection(ctx, bearerToken(conn.Request()), payload.AgentID)
+	if err := conn.SetReadDeadline(time.Time{}); err != nil {
+		_ = transport.Close()
+		return
+	}
+	tokenID, err := r.authenticateAgentConnection(ctx, authentication, payload.AgentID)
 	if err != nil {
 		_ = transport.Close()
 		return
@@ -102,26 +123,36 @@
 	_ = ServeAgentSessionWithInitialFrame(ctx, r.AgentSessions, registration, transport, initial, nil)
 }
 
-func (r *ServerRuntime) authenticateAgentConnection(ctx context.Context, raw, agentID string) (string, error) {
+func (r *ServerRuntime) preauthenticateAgentConnection(ctx context.Context, raw string) (agentConnectionAuthentication, error) {
 	identity, err := r.Credentials.ValidateAs(ctx, raw, storage.TokenTypeAgent)
-	if err == nil && identity.AgentID == agentID {
-		return identity.TokenID, nil
+	if err == nil {
+		return agentConnectionAuthentication{Identity: identity}, nil
 	}
 	// Deprecated: management connection tokens are removed in v0.3.0. This
 	// migration path is disabled unless explicitly enabled in configuration.
 	if !r.config.Security.AllowLegacyConnectionTokens {
-		return "", auth.ErrUnauthenticated
+		return agentConnectionAuthentication{}, auth.ErrUnauthenticated
 	}
 	principal, err := r.Auth.ValidateToken(ctx, raw)
 	if err != nil {
-		return "", auth.ErrUnauthenticated
+		return agentConnectionAuthentication{}, auth.ErrUnauthenticated
 	}
+	return agentConnectionAuthentication{Principal: principal, Legacy: true}, nil
+}
+
+func (r *ServerRuntime) authenticateAgentConnection(ctx context.Context, authentication agentConnectionAuthentication, agentID string) (string, error) {
+	if !authentication.Legacy {
+		if authentication.Identity.TokenID == "" || authentication.Identity.AgentID != agentID {
+			return "", auth.ErrUnauthenticated
+		}
+		return authentication.Identity.TokenID, nil
+	}
 	agent, err := r.DB.Agents().Get(ctx, agentID)
-	if err != nil || !agent.Enabled || (principal.Role != "admin" && agent.OwnerUserID != principal.UserID) {
+	if err != nil || !agent.Enabled || (authentication.Principal.Role != "admin" && agent.OwnerUserID != authentication.Principal.UserID) {
 		return "", auth.ErrForbidden
 	}
 	if err := r.DB.Audits().Create(ctx, storage.AuditLog{
-		ActorUserID:  principal.UserID,
+		ActorUserID:  authentication.Principal.UserID,
 		Action:       "deprecated_connection_token",
 		ResourceType: "agent",
 		ResourceID:   agentID,
@@ -129,7 +160,7 @@
 	}); err != nil {
 		return "", err
 	}
-	slog.WarnContext(ctx, "deprecated_connection_token", "agent_id", agentID, "user_id", principal.UserID, "removal_version", "v0.3.0")
+	slog.WarnContext(ctx, "deprecated_connection_token", "agent_id", agentID, "user_id", authentication.Principal.UserID, "removal_version", "v0.3.0")
 	return "", nil
 }
 
diff -ruN --exclude=*.__ABSENT__ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-after/internal/server/runtime_test.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-fix1-after/internal/server/runtime_test.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-after/internal/server/runtime_test.go	2026-09-06 16:50:34
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-5-fix1-after/internal/server/runtime_test.go	2026-09-06 17:13:52
@@ -4,6 +4,7 @@
 	"bufio"
 	"bytes"
 	"context"
+	"encoding/binary"
 	"encoding/json"
 	"errors"
 	"fmt"
@@ -138,19 +139,9 @@
 		t.Fatal(err)
 	}
 	config.Header.Set("Authorization", "Bearer invalid-token")
-	invalidWS, err := websocket.DialConfig(config)
-	if err != nil {
-		t.Fatal(err)
+	if status := rawWebSocketHandshakeStatus(t, listener.Addr().String(), listener.Addr().String(), baseURL, "invalid-token"); status != http.StatusForbidden {
+		t.Fatalf("invalid Agent token handshake status = %d, want %d", status, http.StatusForbidden)
 	}
-	if err := websocket.Message.Send(invalidWS, frameBytes(t, protocol.FrameAgentHello, payload)); err != nil {
-		t.Fatal(err)
-	}
-	_ = invalidWS.SetReadDeadline(time.Now().Add(time.Second))
-	var rejected []byte
-	if err := websocket.Message.Receive(invalidWS, &rejected); err == nil {
-		t.Fatal("invalid API token unexpectedly received an acknowledgement")
-	}
-	_ = invalidWS.Close()
 
 	config.Header.Set("Authorization", "Bearer "+agentToken.Secret)
 	ws, err := websocket.DialConfig(config)
@@ -308,7 +299,7 @@
 	if err := db.Agents().Create(context.Background(), storage.Agent{ID: "disabled-agent", Name: "disabled-agent", OwnerUserID: owner.ID, Enabled: false}); err != nil {
 		t.Fatal(err)
 	}
-	runtime, err := NewServerRuntime(db, AgentSessionConfig{})
+	runtime, err := NewServerRuntime(db, AgentSessionConfig{}, RuntimeConfig{Security: config.SecurityConfig{AllowLegacyConnectionTokens: true}})
 	if err != nil {
 		t.Fatal(err)
 	}
@@ -479,6 +470,199 @@
 	}
 }
 
+func TestAgentInvalidCredentialRejectedBeforeUpgrade(t *testing.T) {
+	ctx := context.Background()
+	db, err := storage.OpenSQLite(ctx, "file:runtime-agent-preupgrade-auth?mode=memory&cache=shared")
+	if err != nil {
+		t.Fatal(err)
+	}
+	defer db.Close()
+	authService := auth.NewAuthService(db)
+	owner, err := authService.CreateUser(ctx, "preupgrade-owner", "owner-pass", "user")
+	if err != nil {
+		t.Fatal(err)
+	}
+	management, err := authService.Login(ctx, owner.Username, "owner-pass")
+	if err != nil {
+		t.Fatal(err)
+	}
+	if err := db.Agents().Create(ctx, storage.Agent{ID: "preupgrade-agent", Name: "preupgrade-agent", OwnerUserID: owner.ID, Enabled: true}); err != nil {
+		t.Fatal(err)
+	}
+	credentials := auth.NewCredentialService(db)
+	agentToken, err := credentials.Create(ctx, auth.CreateTokenInput{Type: storage.TokenTypeAgent, OwnerUserID: owner.ID, AgentID: "preupgrade-agent"})
+	if err != nil {
+		t.Fatal(err)
+	}
+	clientToken, err := credentials.Create(ctx, auth.CreateTokenInput{Type: storage.TokenTypeClient, OwnerUserID: owner.ID})
+	if err != nil {
+		t.Fatal(err)
+	}
+	revokedToken, err := credentials.Create(ctx, auth.CreateTokenInput{Type: storage.TokenTypeAgent, OwnerUserID: owner.ID, AgentID: "preupgrade-agent"})
+	if err != nil {
+		t.Fatal(err)
+	}
+	if err := credentials.Revoke(ctx, revokedToken.TokenID); err != nil {
+		t.Fatal(err)
+	}
+	_ = credentials.Close()
+
+	runtime, err := NewServerRuntime(db, AgentSessionConfig{})
+	if err != nil {
+		t.Fatal(err)
+	}
+	defer runtime.Close()
+	listener, err := net.Listen("tcp", "127.0.0.1:0")
+	if err != nil {
+		t.Fatal(err)
+	}
+	serverCtx, cancel := context.WithCancel(ctx)
+	defer cancel()
+	serveErr := make(chan error, 1)
+	go func() { serveErr <- runtime.ServeListener(serverCtx, listener) }()
+	defer func() {
+		cancel()
+		<-serveErr
+	}()
+	address := listener.Addr().String()
+
+	for _, tc := range []struct {
+		name  string
+		token string
+	}{
+		{name: "arbitrary token", token: "not-a-valid-token"},
+		{name: "client token", token: clientToken.Secret},
+		{name: "revoked Agent token", token: revokedToken.Secret},
+		{name: "management token with legacy disabled", token: management.Token},
+	} {
+		t.Run(tc.name, func(t *testing.T) {
+			if status := rawWebSocketHandshakeStatus(t, address, address, "http://"+address, tc.token); status != http.StatusForbidden {
+				t.Fatalf("invalid credential handshake status = %d, want %d", status, http.StatusForbidden)
+			}
+		})
+	}
+	if status := rawWebSocketHandshakeStatus(t, address, address, "http://"+address, agentToken.Secret); status != http.StatusSwitchingProtocols {
+		t.Fatalf("valid Agent credential handshake status = %d, want %d", status, http.StatusSwitchingProtocols)
+	}
+}
+
+func TestAgentWebSocketStrictOriginWithoutAllowlist(t *testing.T) {
+	ctx := context.Background()
+	db, err := storage.OpenSQLite(ctx, "file:runtime-agent-strict-origin?mode=memory&cache=shared")
+	if err != nil {
+		t.Fatal(err)
+	}
+	defer db.Close()
+	authService := auth.NewAuthService(db)
+	owner, err := authService.CreateUser(ctx, "strict-origin-owner", "owner-pass", "user")
+	if err != nil {
+		t.Fatal(err)
+	}
+	if err := db.Agents().Create(ctx, storage.Agent{ID: "strict-origin-agent", Name: "strict-origin-agent", OwnerUserID: owner.ID, Enabled: true}); err != nil {
+		t.Fatal(err)
+	}
+	credentials := auth.NewCredentialService(db)
+	created, err := credentials.Create(ctx, auth.CreateTokenInput{Type: storage.TokenTypeAgent, OwnerUserID: owner.ID, AgentID: "strict-origin-agent"})
+	if err != nil {
+		t.Fatal(err)
+	}
+	_ = credentials.Close()
+
+	runtime, err := NewServerRuntime(db, AgentSessionConfig{})
+	if err != nil {
+		t.Fatal(err)
+	}
+	defer runtime.Close()
+	listener, err := net.Listen("tcp", "127.0.0.1:0")
+	if err != nil {
+		t.Fatal(err)
+	}
+	serverCtx, cancel := context.WithCancel(ctx)
+	defer cancel()
+	serveErr := make(chan error, 1)
+	go func() { serveErr <- runtime.ServeListener(serverCtx, listener) }()
+	defer func() {
+		cancel()
+		<-serveErr
+	}()
+	address := listener.Addr().String()
+
+	for _, origin := range []string{
+		"ftp://" + address,
+		"relative-origin",
+		"http://" + address + "/path",
+		"http://" + address + "?query=value",
+		"http://" + address + "#fragment",
+	} {
+		t.Run(origin, func(t *testing.T) {
+			if status := rawWebSocketHandshakeStatus(t, address, address, origin, created.Secret); status != http.StatusForbidden {
+				t.Fatalf("invalid Origin %q handshake status = %d, want %d", origin, status, http.StatusForbidden)
+			}
+		})
+	}
+	for _, origin := range []string{"http://" + address, "https://" + address} {
+		t.Run(origin, func(t *testing.T) {
+			if status := rawWebSocketHandshakeStatus(t, address, address, origin, created.Secret); status != http.StatusSwitchingProtocols {
+				t.Fatalf("valid Origin %q handshake status = %d, want %d", origin, status, http.StatusSwitchingProtocols)
+			}
+		})
+	}
+}
+
+func TestAgentWebSocketClosesConnectionWhenHelloTimesOut(t *testing.T) {
+	address, token, stop := startAuthenticatedAgentWebSocketServer(t, "hello-timeout")
+	defer stop()
+	wsURL := "ws://" + address + "/ws/agent"
+	wsConfig, err := websocket.NewConfig(wsURL, "http://"+address)
+	if err != nil {
+		t.Fatal(err)
+	}
+	wsConfig.Header.Set("Authorization", "Bearer "+token)
+	ws, err := websocket.DialConfig(wsConfig)
+	if err != nil {
+		t.Fatal(err)
+	}
+	defer ws.Close()
+
+	started := time.Now()
+	_ = ws.SetReadDeadline(started.Add(3 * time.Second))
+	var payload []byte
+	if err := websocket.Message.Receive(ws, &payload); err == nil {
+		t.Fatal("connection without Agent Hello remained open")
+	}
+	if elapsed := time.Since(started); elapsed >= 2*time.Second {
+		t.Fatalf("connection without Agent Hello closed after %v, want before 2s", elapsed)
+	}
+}
+
+func TestAgentWebSocketRejectsOversizedFirstFrameBeforeReadingPayload(t *testing.T) {
+	address, token, stop := startAuthenticatedAgentWebSocketServer(t, "oversized-first-frame")
+	defer stop()
+	conn, status := rawWebSocketUpgrade(t, address, address, "http://"+address, token)
+	defer conn.Close()
+	if status != http.StatusSwitchingProtocols {
+		t.Fatalf("handshake status = %d, want %d", status, http.StatusSwitchingProtocols)
+	}
+
+	var frameHeader [14]byte
+	frameHeader[0] = 0x82
+	frameHeader[1] = 0xff
+	binary.BigEndian.PutUint64(frameHeader[2:10], uint64(protocol.MaxPayload+16+1))
+	copy(frameHeader[10:], []byte{1, 2, 3, 4})
+	started := time.Now()
+	if _, err := conn.Write(frameHeader[:]); err != nil {
+		t.Fatal(err)
+	}
+	_ = conn.SetReadDeadline(started.Add(750 * time.Millisecond))
+	_, readErr := io.ReadAll(conn)
+	if netErr, ok := readErr.(net.Error); ok && netErr.Timeout() {
+		t.Fatalf("server waited for oversized first-frame payload instead of rejecting its declared length: %v", readErr)
+	}
+	if elapsed := time.Since(started); elapsed >= 600*time.Millisecond {
+		t.Fatalf("oversized first frame closed after %v, want immediate rejection", elapsed)
+	}
+}
+
 func TestAgentWebSocketHostAndOriginAllowlist(t *testing.T) {
 	ctx := context.Background()
 	db, err := storage.OpenSQLite(ctx, "file:runtime-agent-allowlist?mode=memory&cache=shared")
@@ -590,6 +774,13 @@
 		cancel()
 		<-serveErr
 	}()
+	address := listener.Addr().String()
+	if status := rawWebSocketHandshakeStatus(t, address, address, "http://"+address, "invalid-legacy-token"); status != http.StatusForbidden {
+		t.Fatalf("invalid legacy credential handshake status = %d, want %d", status, http.StatusForbidden)
+	}
+	if status := rawWebSocketHandshakeStatus(t, address, address, "http://"+address, login.Token); status != http.StatusSwitchingProtocols {
+		t.Fatalf("valid legacy credential handshake status = %d, want %d", status, http.StatusSwitchingProtocols)
+	}
 
 	if !agentWebSocketAccepted(t, listener.Addr().String(), login.Token, "legacy-agent", 1, "", "") {
 		t.Fatal("legacy management token was rejected while migration flag was enabled")
@@ -679,24 +870,80 @@
 	return err == nil && ack.Accepted
 }
 
+func startAuthenticatedAgentWebSocketServer(t *testing.T, name string) (string, string, func()) {
+	t.Helper()
+	ctx := context.Background()
+	db, err := storage.OpenSQLite(ctx, "file:runtime-agent-"+name+"?mode=memory&cache=shared")
+	if err != nil {
+		t.Fatal(err)
+	}
+	authService := auth.NewAuthService(db)
+	owner, err := authService.CreateUser(ctx, name+"-owner", "owner-pass", "user")
+	if err != nil {
+		_ = db.Close()
+		t.Fatal(err)
+	}
+	agentID := name + "-agent"
+	if err := db.Agents().Create(ctx, storage.Agent{ID: agentID, Name: agentID, OwnerUserID: owner.ID, Enabled: true}); err != nil {
+		_ = db.Close()
+		t.Fatal(err)
+	}
+	credentials := auth.NewCredentialService(db)
+	created, err := credentials.Create(ctx, auth.CreateTokenInput{Type: storage.TokenTypeAgent, OwnerUserID: owner.ID, AgentID: agentID})
+	if err != nil {
+		_ = credentials.Close()
+		_ = db.Close()
+		t.Fatal(err)
+	}
+	_ = credentials.Close()
+	runtime, err := NewServerRuntime(db, AgentSessionConfig{})
+	if err != nil {
+		_ = db.Close()
+		t.Fatal(err)
+	}
+	listener, err := net.Listen("tcp", "127.0.0.1:0")
+	if err != nil {
+		_ = runtime.Close()
+		_ = db.Close()
+		t.Fatal(err)
+	}
+	serverCtx, cancel := context.WithCancel(ctx)
+	serveErr := make(chan error, 1)
+	go func() { serveErr <- runtime.ServeListener(serverCtx, listener) }()
+	return listener.Addr().String(), created.Secret, func() {
+		cancel()
+		<-serveErr
+		_ = runtime.Close()
+		_ = db.Close()
+	}
+}
+
 func rawWebSocketHandshakeStatus(t *testing.T, address, host, origin, token string) int {
 	t.Helper()
+	conn, status := rawWebSocketUpgrade(t, address, host, origin, token)
+	_ = conn.Close()
+	return status
+}
+
+func rawWebSocketUpgrade(t *testing.T, address, host, origin, token string) (net.Conn, int) {
+	t.Helper()
 	conn, err := net.Dial("tcp", address)
 	if err != nil {
 		t.Fatal(err)
 	}
-	defer conn.Close()
 	request := fmt.Sprintf("GET /ws/agent HTTP/1.1\r\nHost: %s\r\nOrigin: %s\r\nAuthorization: Bearer %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\n", host, origin, token)
 	if _, err := conn.Write([]byte(request)); err != nil {
+		_ = conn.Close()
 		t.Fatal(err)
 	}
 	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
 	response, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodGet})
 	if err != nil {
+		_ = conn.Close()
 		t.Fatal(err)
 	}
-	defer response.Body.Close()
-	return response.StatusCode
+	_ = conn.SetReadDeadline(time.Time{})
+	return conn, response.StatusCode
 }
 
 func TestRealAgentReconnectsWithNewEpochAfterServerClosesSession(t *testing.T) {
```
