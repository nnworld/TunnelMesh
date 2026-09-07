# Task 2 review package

Base HEAD: `163fe12121d2f839ea4bf4907f55a8e7dd835057`

## Changed files

```text
Files .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-2-before/internal/storage/db.go and .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-2-after/internal/storage/db.go differ
Files .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-2-before/internal/storage/repository.go and .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-2-after/internal/storage/repository.go differ
Only in .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-2-after/internal/storage: service_token_test.go
Only in .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-2-before/internal/storage: service_token_test.go.__ABSENT__
Files .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-2-before/internal/storage/storage_contract_test.go and .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-2-after/internal/storage/storage_contract_test.go differ
```

## Full task-only diff

```diff
diff -ruN .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-2-before/internal/storage/db.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-2-after/internal/storage/db.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-2-before/internal/storage/db.go	2026-09-06 14:15:01
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-2-after/internal/storage/db.go	2026-09-06 14:32:31
@@ -23,20 +23,37 @@
 var ErrSchemaVersionMismatch = errors.New("schema version mismatch")
 
 type DB struct {
-	sql         *sql.DB
-	driver      string
-	users       UserRepository
-	tokens      TokenRepository
-	agents      AgentRepository
-	policies    PolicyRepository
-	tunnels     TunnelRepository
-	nodes       NodeRepository
-	metadata    AgentMetadataRepository
-	leases      LeaseRepository
-	audits      AuditRepository
-	idempotency IdempotencyRepository
+	sql           *sql.DB
+	driver        string
+	users         UserRepository
+	tokens        TokenRepository
+	serviceTokens ServiceTokenRepository
+	agents        AgentRepository
+	policies      PolicyRepository
+	tunnels       TunnelRepository
+	nodes         NodeRepository
+	metadata      AgentMetadataRepository
+	leases        LeaseRepository
+	audits        AuditRepository
+	idempotency   IdempotencyRepository
 }
 
+// ServiceTokenTransaction atomically applies service-token lifecycle changes
+// and their audit record using repositories bound to the same transaction.
+func (d *DB) ServiceTokenTransaction(ctx context.Context, fn func(ServiceTokenRepository, AuditRepository) error) error {
+	tx, err := d.sql.BeginTx(ctx, nil)
+	if err != nil {
+		return err
+	}
+	defer tx.Rollback()
+	tokens := &serviceTokenRepo{db: tx, driver: d.driver}
+	audits := &auditRepo{db: tx}
+	if err := fn(tokens, audits); err != nil {
+		return err
+	}
+	return tx.Commit()
+}
+
 // AuthTransaction executes user/token/audit changes in one database
 // transaction. The schema_meta no-op update acquires a portable writer lock,
 // fencing concurrent bootstrap or recovery operations across processes.
@@ -115,7 +132,21 @@
 }
 
 func newDB(db *sql.DB, driver string) *DB {
-	return &DB{sql: db, driver: driver, users: &userRepo{db}, tokens: &tokenRepo{db}, agents: &agentRepo{db}, policies: &policyRepo{db}, tunnels: &tunnelRepo{db}, nodes: &nodeRepo{db}, metadata: NewAgentMetadataRepositoryWithDriver(db, driver), leases: NewLeaseRepositoryWithDriver(db, driver), audits: &auditRepo{db}, idempotency: &idempotencyRepo{db}}
+	return &DB{
+		sql:           db,
+		driver:        driver,
+		users:         &userRepo{db},
+		tokens:        &tokenRepo{db},
+		serviceTokens: NewServiceTokenRepositoryWithDriver(db, driver),
+		agents:        &agentRepo{db},
+		policies:      &policyRepo{db},
+		tunnels:       &tunnelRepo{db},
+		nodes:         &nodeRepo{db},
+		metadata:      NewAgentMetadataRepositoryWithDriver(db, driver),
+		leases:        NewLeaseRepositoryWithDriver(db, driver),
+		audits:        &auditRepo{db},
+		idempotency:   &idempotencyRepo{db},
+	}
 }
 
 func initializeSchema(ctx context.Context, db *sql.DB, driver string) error {
@@ -202,6 +233,7 @@
 }
 func (d *DB) Users() UserRepository                  { return d.users }
 func (d *DB) Tokens() TokenRepository                { return d.tokens }
+func (d *DB) ServiceTokens() ServiceTokenRepository  { return d.serviceTokens }
 func (d *DB) Agents() AgentRepository                { return d.agents }
 func (d *DB) Policies() PolicyRepository             { return d.policies }
 func (d *DB) Tunnels() TunnelRepository              { return d.tunnels }
diff -ruN .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-2-before/internal/storage/repository.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-2-after/internal/storage/repository.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-2-before/internal/storage/repository.go	2026-09-06 12:39:27
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-2-after/internal/storage/repository.go	2026-09-06 14:33:17
@@ -28,6 +28,25 @@
 	List(context.Context, string, int) (Page[APIToken], error)
 }
 
+type ServiceTokenFilter struct {
+	OwnerUserID string
+	Type        TokenType
+	AgentID     string
+	NodeID      string
+}
+
+type ServiceTokenRepository interface {
+	Create(context.Context, ServiceToken) error
+	Get(context.Context, string) (ServiceToken, error)
+	GetByHash(context.Context, string) (ServiceToken, error)
+	List(context.Context, ServiceTokenFilter, string, int) (Page[ServiceToken], error)
+	Revoke(context.Context, string, time.Time) error
+	TouchLastUsed(context.Context, string, time.Time) error
+	Rotate(context.Context, string, ServiceToken, time.Time) error
+}
+
+var ErrServiceTokenRevoked = errors.New("service token is already revoked")
+
 type AgentRepository interface {
 	Create(context.Context, Agent) error
 	Get(context.Context, string) (Agent, error)
@@ -98,8 +117,21 @@
 
 // Constructor helpers are useful for services that own a database/sql handle
 // directly (for example tests or read-only reporting jobs).
-func NewUserRepository(db *sql.DB) UserRepository     { return &userRepo{db} }
-func NewTokenRepository(db *sql.DB) TokenRepository   { return &tokenRepo{db} }
+func NewUserRepository(db *sql.DB) UserRepository   { return &userRepo{db} }
+func NewTokenRepository(db *sql.DB) TokenRepository { return &tokenRepo{db} }
+func NewServiceTokenRepository(db *sql.DB) ServiceTokenRepository {
+	return NewServiceTokenRepositoryWithDriver(db, DriverSQLite)
+}
+func NewServiceTokenRepositoryWithDriver(db *sql.DB, driver string) ServiceTokenRepository {
+	driver = strings.ToLower(strings.TrimSpace(driver))
+	if driver == "sqlite3" {
+		driver = DriverSQLite
+	}
+	if driver != DriverMySQL && driver != DriverSQLite {
+		panic(fmt.Sprintf("unsupported service token repository driver %q", driver))
+	}
+	return &serviceTokenRepo{db: db, starter: db, driver: driver}
+}
 func NewAgentRepository(db *sql.DB) AgentRepository   { return &agentRepo{db} }
 func NewPolicyRepository(db *sql.DB) PolicyRepository { return &policyRepo{db} }
 func NewTunnelRepository(db *sql.DB) TunnelRepository { return &tunnelRepo{db} }
@@ -235,6 +267,10 @@
 	QueryRowContext(context.Context, string, ...any) *sql.Row
 }
 
+type transactionStarter interface {
+	BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
+}
+
 type userRepo struct{ db dbExecutor }
 
 func (r *userRepo) Create(ctx context.Context, v User) error {
@@ -402,6 +438,168 @@
 		p.NextCursor = encodeCursor(p.Items[len(p.Items)-1].ID)
 	}
 	return p, nil
+}
+
+const serviceTokenColumns = `id,token_type,owner_user_id,agent_id,node_id,token_prefix,token_hash,scope,expires_at,revoked_at,last_used_at,created_at,updated_at`
+
+type serviceTokenRepo struct {
+	db      dbExecutor
+	starter transactionStarter
+	driver  string
+}
+
+func (r *serviceTokenRepo) Create(ctx context.Context, v ServiceToken) error {
+	return createServiceToken(ctx, r.db, v)
+}
+
+func createServiceToken(ctx context.Context, db dbExecutor, v ServiceToken) error {
+	v.ID, v.CreatedAt, v.UpdatedAt = stamp(v.ID, v.CreatedAt, v.UpdatedAt, "stok")
+	_, err := db.ExecContext(ctx, `INSERT INTO service_tokens(id,token_type,owner_user_id,agent_id,node_id,token_prefix,token_hash,scope,expires_at,revoked_at,last_used_at,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, v.ID, v.Type, nullableString(v.OwnerUserID), nullableString(v.AgentID), nullableString(v.NodeID), v.Prefix, v.TokenHash, v.Scope, nullableTime(v.ExpiresAt), nullableTime(v.RevokedAt), nullableTime(v.LastUsedAt), tm(v.CreatedAt), tm(v.UpdatedAt))
+	return err
+}
+
+func (r *serviceTokenRepo) Get(ctx context.Context, id string) (ServiceToken, error) {
+	return scanServiceToken(r.db.QueryRowContext(ctx, `SELECT `+serviceTokenColumns+` FROM service_tokens WHERE id=?`, id))
+}
+
+func (r *serviceTokenRepo) GetByHash(ctx context.Context, hash string) (ServiceToken, error) {
+	return scanServiceToken(r.db.QueryRowContext(ctx, `SELECT `+serviceTokenColumns+` FROM service_tokens WHERE token_hash=?`, hash))
+}
+
+func (r *serviceTokenRepo) List(ctx context.Context, filter ServiceTokenFilter, cursor string, limit int) (Page[ServiceToken], error) {
+	cursor, limit = pageArgs(cursor, limit)
+	conditions := make([]string, 0, 5)
+	args := make([]any, 0, 6)
+	if filter.OwnerUserID != "" {
+		conditions = append(conditions, `owner_user_id=?`)
+		args = append(args, filter.OwnerUserID)
+	}
+	if filter.Type != "" {
+		conditions = append(conditions, `token_type=?`)
+		args = append(args, filter.Type)
+	}
+	if filter.AgentID != "" {
+		conditions = append(conditions, `agent_id=?`)
+		args = append(args, filter.AgentID)
+	}
+	if filter.NodeID != "" {
+		conditions = append(conditions, `node_id=?`)
+		args = append(args, filter.NodeID)
+	}
+	if decoded := decodeCursor(cursor); decoded != "" {
+		conditions = append(conditions, `id>?`)
+		args = append(args, decoded)
+	}
+	query := `SELECT ` + serviceTokenColumns + ` FROM service_tokens`
+	if len(conditions) > 0 {
+		query += ` WHERE ` + strings.Join(conditions, ` AND `)
+	}
+	query += ` ORDER BY id LIMIT ?`
+	args = append(args, limit+1)
+	rows, err := r.db.QueryContext(ctx, query, args...)
+	if err != nil {
+		return Page[ServiceToken]{}, err
+	}
+	defer rows.Close()
+	page := Page[ServiceToken]{}
+	for rows.Next() {
+		token, err := scanServiceToken(rows)
+		if err != nil {
+			return Page[ServiceToken]{}, err
+		}
+		page.Items = append(page.Items, token)
+	}
+	if err := rows.Err(); err != nil {
+		return Page[ServiceToken]{}, err
+	}
+	if len(page.Items) > limit {
+		page.Items = page.Items[:limit]
+		page.HasMore = true
+		page.NextCursor = encodeCursor(page.Items[len(page.Items)-1].ID)
+	}
+	return page, nil
+}
+
+func (r *serviceTokenRepo) Revoke(ctx context.Context, id string, when time.Time) error {
+	when = timeOrNow(when)
+	res, err := r.db.ExecContext(ctx, `UPDATE service_tokens SET revoked_at=?,updated_at=? WHERE id=?`, tm(when), tm(when), id)
+	return checkAffected(res, err)
+}
+
+func (r *serviceTokenRepo) TouchLastUsed(ctx context.Context, id string, when time.Time) error {
+	when = timeOrNow(when)
+	res, err := r.db.ExecContext(ctx, `UPDATE service_tokens SET last_used_at=?,updated_at=? WHERE id=?`, tm(when), tm(when), id)
+	return checkAffected(res, err)
+}
+
+func (r *serviceTokenRepo) Rotate(ctx context.Context, oldID string, replacement ServiceToken, when time.Time) error {
+	when = timeOrNow(when)
+	if r.starter == nil {
+		return rotateServiceToken(ctx, r.db, r.driver, oldID, replacement, when)
+	}
+	tx, err := r.starter.BeginTx(ctx, nil)
+	if err != nil {
+		return err
+	}
+	defer tx.Rollback()
+	if err := rotateServiceToken(ctx, tx, r.driver, oldID, replacement, when); err != nil {
+		return err
+	}
+	return tx.Commit()
+}
+
+func rotateServiceToken(ctx context.Context, db dbExecutor, driver, oldID string, replacement ServiceToken, when time.Time) error {
+	query := `SELECT revoked_at FROM service_tokens WHERE id=?`
+	if driver == DriverMySQL {
+		query += ` FOR UPDATE`
+	}
+	var revoked sql.NullString
+	if err := db.QueryRowContext(ctx, query, oldID).Scan(&revoked); err != nil {
+		return err
+	}
+	if parseTM(revoked) != nil {
+		return ErrServiceTokenRevoked
+	}
+	if err := createServiceToken(ctx, db, replacement); err != nil {
+		return err
+	}
+	res, err := db.ExecContext(ctx, `UPDATE service_tokens SET revoked_at=?,updated_at=? WHERE id=? AND revoked_at IS NULL`, tm(when), tm(when), oldID)
+	return checkAffected(res, err)
+}
+
+type serviceTokenScanner interface {
+	Scan(...any) error
+}
+
+func scanServiceToken(scanner serviceTokenScanner) (ServiceToken, error) {
+	var token ServiceToken
+	var tokenType string
+	var owner, agent, node sql.NullString
+	var expires, revoked, lastUsed, created, updated sql.NullString
+	err := scanner.Scan(&token.ID, &tokenType, &owner, &agent, &node, &token.Prefix, &token.TokenHash, &token.Scope, &expires, &revoked, &lastUsed, &created, &updated)
+	if err != nil {
+		return ServiceToken{}, err
+	}
+	token.Type = TokenType(tokenType)
+	if owner.Valid {
+		token.OwnerUserID = owner.String
+	}
+	if agent.Valid {
+		token.AgentID = agent.String
+	}
+	if node.Valid {
+		token.NodeID = node.String
+	}
+	token.ExpiresAt = parseTM(expires)
+	token.RevokedAt = parseTM(revoked)
+	token.LastUsedAt = parseTM(lastUsed)
+	if created.Valid {
+		token.CreatedAt = parseTime(created.String)
+	}
+	if updated.Valid {
+		token.UpdatedAt = parseTime(updated.String)
+	}
+	return token, nil
 }
 
 type agentRepo struct{ db *sql.DB }
diff -ruN .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-2-before/internal/storage/service_token_test.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-2-after/internal/storage/service_token_test.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-2-before/internal/storage/service_token_test.go	1970-01-01 08:00:00
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-2-after/internal/storage/service_token_test.go	2026-09-06 14:36:12
@@ -0,0 +1,248 @@
+package storage
+
+import (
+	"context"
+	"database/sql"
+	"errors"
+	"testing"
+	"time"
+)
+
+func TestServiceTokenRepositoryContractSQLite(t *testing.T) {
+	runServiceTokenRepositoryContract(t, newTestDB(t))
+}
+
+func runServiceTokenRepositoryContract(t *testing.T, db *DB) {
+	t.Helper()
+	ctx := context.Background()
+	if _, err := db.SQL().ExecContext(ctx, `DELETE FROM service_tokens WHERE id LIKE 'contract-service-token-%' OR token_hash LIKE 'contract-service-token-%'`); err != nil {
+		t.Fatalf("clean service token fixtures: %v", err)
+	}
+	defer func() {
+		_, _ = db.SQL().ExecContext(context.Background(), `DELETE FROM service_tokens WHERE id LIKE 'contract-service-token-%' OR token_hash LIKE 'contract-service-token-%'`)
+	}()
+
+	repo := db.ServiceTokens()
+	createdAt := time.Date(2026, 9, 6, 8, 0, 0, 0, time.UTC)
+	expiresAt := createdAt.Add(24 * time.Hour)
+	base := ServiceToken{
+		ID:          "contract-service-token-10-base",
+		OwnerUserID: "owner-main",
+		AgentID:     "agent-main",
+		Prefix:      "tm_agent_abc",
+		TokenHash:   "contract-service-token-hash-base",
+		Scope:       `["tunnel:connect"]`,
+		Type:        TokenTypeAgent,
+		ExpiresAt:   &expiresAt,
+		CreatedAt:   createdAt,
+		UpdatedAt:   createdAt,
+	}
+	if err := repo.Create(ctx, base); err != nil {
+		t.Fatalf("create service token: %v", err)
+	}
+	got, err := repo.Get(ctx, base.ID)
+	if err != nil {
+		t.Fatalf("get service token: %v", err)
+	}
+	assertServiceToken(t, got, base)
+	got, err = repo.GetByHash(ctx, base.TokenHash)
+	if err != nil {
+		t.Fatalf("get service token by hash: %v", err)
+	}
+	assertServiceToken(t, got, base)
+
+	duplicate := base
+	duplicate.ID = "contract-service-token-11-duplicate"
+	if err := repo.Create(ctx, duplicate); err == nil {
+		t.Fatal("create duplicate service token hash succeeded")
+	}
+
+	filtered := []ServiceToken{
+		{ID: "contract-service-token-20-other-owner", OwnerUserID: "owner-other", AgentID: "agent-filter", NodeID: "node-filter", Prefix: "p20", TokenHash: "contract-service-token-hash-20", Scope: `[]`, Type: TokenTypeAgent},
+		{ID: "contract-service-token-21-other-type", OwnerUserID: "owner-filter", AgentID: "agent-filter", NodeID: "node-filter", Prefix: "p21", TokenHash: "contract-service-token-hash-21", Scope: `[]`, Type: TokenTypeClient},
+		{ID: "contract-service-token-22-other-agent", OwnerUserID: "owner-filter", AgentID: "agent-other", NodeID: "node-filter", Prefix: "p22", TokenHash: "contract-service-token-hash-22", Scope: `[]`, Type: TokenTypeAgent},
+		{ID: "contract-service-token-23-other-node", OwnerUserID: "owner-filter", AgentID: "agent-filter", NodeID: "node-other", Prefix: "p23", TokenHash: "contract-service-token-hash-23", Scope: `[]`, Type: TokenTypeAgent},
+		{ID: "contract-service-token-24-match", OwnerUserID: "owner-filter", AgentID: "agent-filter", NodeID: "node-filter", Prefix: "p24", TokenHash: "contract-service-token-hash-24", Scope: `[]`, Type: TokenTypeAgent},
+		{ID: "contract-service-token-25-match", OwnerUserID: "owner-filter", AgentID: "agent-filter", NodeID: "node-filter", Prefix: "p25", TokenHash: "contract-service-token-hash-25", Scope: `[]`, Type: TokenTypeAgent},
+	}
+	for _, token := range filtered {
+		if err := repo.Create(ctx, token); err != nil {
+			t.Fatalf("create filtered service token %s: %v", token.ID, err)
+		}
+	}
+	filter := ServiceTokenFilter{OwnerUserID: "owner-filter", Type: TokenTypeAgent, AgentID: "agent-filter", NodeID: "node-filter"}
+	page, err := repo.List(ctx, filter, "", 1)
+	if err != nil {
+		t.Fatalf("list filtered service tokens: %v", err)
+	}
+	if len(page.Items) != 1 || page.Items[0].ID != "contract-service-token-24-match" || !page.HasMore || page.NextCursor == "" {
+		t.Fatalf("first filtered page = %+v", page)
+	}
+	page, err = repo.List(ctx, filter, page.NextCursor, 1)
+	if err != nil {
+		t.Fatalf("list second filtered service token page: %v", err)
+	}
+	if len(page.Items) != 1 || page.Items[0].ID != "contract-service-token-25-match" || page.HasMore || page.NextCursor != "" {
+		t.Fatalf("second filtered page = %+v", page)
+	}
+
+	lastUsedAt := createdAt.Add(time.Hour)
+	if err := repo.TouchLastUsed(ctx, base.ID, lastUsedAt); err != nil {
+		t.Fatalf("touch service token last used: %v", err)
+	}
+	got, err = repo.Get(ctx, base.ID)
+	if err != nil {
+		t.Fatalf("get touched service token: %v", err)
+	}
+	if got.LastUsedAt == nil || !got.LastUsedAt.Equal(lastUsedAt) {
+		t.Fatalf("last used at = %v, want %v", got.LastUsedAt, lastUsedAt)
+	}
+
+	revokedAt := createdAt.Add(2 * time.Hour)
+	if err := repo.Revoke(ctx, base.ID, revokedAt); err != nil {
+		t.Fatalf("revoke service token: %v", err)
+	}
+	got, err = repo.Get(ctx, base.ID)
+	if err != nil {
+		t.Fatalf("get revoked service token: %v", err)
+	}
+	if got.RevokedAt == nil || !got.RevokedAt.Equal(revokedAt) {
+		t.Fatalf("revoked at = %v, want %v", got.RevokedAt, revokedAt)
+	}
+
+	old := ServiceToken{ID: "contract-service-token-30-rotate-old", OwnerUserID: "owner-rotate", Prefix: "old", TokenHash: "contract-service-token-hash-30", Scope: `[]`, Type: TokenTypeClient}
+	if err := repo.Create(ctx, old); err != nil {
+		t.Fatalf("create old rotation token: %v", err)
+	}
+	replacement := ServiceToken{ID: "contract-service-token-31-rotate-new", OwnerUserID: "owner-rotate", Prefix: "new", TokenHash: "contract-service-token-hash-31", Scope: `["route:read"]`, Type: TokenTypeClient}
+	rotationTime := createdAt.Add(3 * time.Hour)
+	if err := repo.Rotate(ctx, old.ID, replacement, rotationTime); err != nil {
+		t.Fatalf("rotate service token: %v", err)
+	}
+	rotatedOld, err := repo.Get(ctx, old.ID)
+	if err != nil {
+		t.Fatalf("get rotated old token: %v", err)
+	}
+	if rotatedOld.RevokedAt == nil || !rotatedOld.RevokedAt.Equal(rotationTime) {
+		t.Fatalf("rotated old revoked at = %v, want %v", rotatedOld.RevokedAt, rotationTime)
+	}
+	rotatedNew, err := repo.GetByHash(ctx, replacement.TokenHash)
+	if err != nil || rotatedNew.ID != replacement.ID {
+		t.Fatalf("rotated replacement = %+v, err=%v", rotatedNew, err)
+	}
+	if err := repo.Rotate(ctx, old.ID, ServiceToken{ID: "contract-service-token-32-revoked-replacement", Prefix: "x", TokenHash: "contract-service-token-hash-32", Scope: `[]`, Type: TokenTypeClient}, rotationTime.Add(time.Minute)); err == nil {
+		t.Fatal("rotating an already revoked service token succeeded")
+	}
+	if _, err := repo.Get(ctx, "contract-service-token-32-revoked-replacement"); !errors.Is(err, sql.ErrNoRows) {
+		t.Fatalf("revoked-token replacement lookup error = %v, want sql.ErrNoRows", err)
+	}
+
+	rollbackOld := ServiceToken{ID: "contract-service-token-40-rollback-old", Prefix: "old", TokenHash: "contract-service-token-hash-40", Scope: `[]`, Type: TokenTypeServerNode}
+	collision := ServiceToken{ID: "contract-service-token-41-collision", Prefix: "collision", TokenHash: "contract-service-token-hash-41", Scope: `[]`, Type: TokenTypeServerNode}
+	if err := repo.Create(ctx, rollbackOld); err != nil {
+		t.Fatalf("create rollback old token: %v", err)
+	}
+	if err := repo.Create(ctx, collision); err != nil {
+		t.Fatalf("create collision token: %v", err)
+	}
+	failedReplacement := ServiceToken{ID: "contract-service-token-42-failed-replacement", Prefix: "failed", TokenHash: collision.TokenHash, Scope: `[]`, Type: TokenTypeServerNode}
+	if err := repo.Rotate(ctx, rollbackOld.ID, failedReplacement, rotationTime); err == nil {
+		t.Fatal("rotation with duplicate replacement hash succeeded")
+	}
+	rollbackOldAfter, err := repo.Get(ctx, rollbackOld.ID)
+	if err != nil {
+		t.Fatalf("get rollback old token: %v", err)
+	}
+	if rollbackOldAfter.RevokedAt != nil {
+		t.Fatalf("rollback old token revoked at = %v, want nil", rollbackOldAfter.RevokedAt)
+	}
+	if _, err := repo.Get(ctx, failedReplacement.ID); !errors.Is(err, sql.ErrNoRows) {
+		t.Fatalf("failed replacement lookup error = %v, want sql.ErrNoRows", err)
+	}
+}
+
+func TestServiceTokenRepositoryTransactionRunner(t *testing.T) {
+	db := newTestDB(t)
+	ctx := context.Background()
+	repo := db.ServiceTokens()
+	old := ServiceToken{ID: "contract-service-token-tx-old", Prefix: "old", TokenHash: "contract-service-token-hash-tx-old", Scope: `[]`, Type: TokenTypeAgent}
+	if err := repo.Create(ctx, old); err != nil {
+		t.Fatal(err)
+	}
+	replacement := ServiceToken{ID: "contract-service-token-tx-new", Prefix: "new", TokenHash: "contract-service-token-hash-tx-new", Scope: `[]`, Type: TokenTypeAgent}
+	when := time.Date(2026, 9, 6, 9, 0, 0, 0, time.UTC)
+	if err := db.ServiceTokenTransaction(ctx, func(tokens ServiceTokenRepository, audits AuditRepository) error {
+		if err := tokens.Rotate(ctx, old.ID, replacement, when); err != nil {
+			return err
+		}
+		return audits.Create(ctx, AuditLog{ID: "audit-service-token-tx-commit", Action: "rotate", ResourceType: "service_token", ResourceID: old.ID})
+	}); err != nil {
+		t.Fatalf("commit service token transaction: %v", err)
+	}
+	if _, err := repo.Get(ctx, replacement.ID); err != nil {
+		t.Fatalf("get committed replacement: %v", err)
+	}
+	assertAuditExists(t, db.Audits(), "audit-service-token-tx-commit", true)
+
+	rollbackOld := ServiceToken{ID: "contract-service-token-tx-rollback-old", Prefix: "old", TokenHash: "contract-service-token-hash-tx-rollback-old", Scope: `[]`, Type: TokenTypeAgent}
+	if err := repo.Create(ctx, rollbackOld); err != nil {
+		t.Fatal(err)
+	}
+	rollbackReplacement := ServiceToken{ID: "contract-service-token-tx-rollback-new", Prefix: "new", TokenHash: "contract-service-token-hash-tx-rollback-new", Scope: `[]`, Type: TokenTypeAgent}
+	wantErr := errors.New("abort credential lifecycle")
+	err := db.ServiceTokenTransaction(ctx, func(tokens ServiceTokenRepository, audits AuditRepository) error {
+		if err := tokens.Rotate(ctx, rollbackOld.ID, rollbackReplacement, when); err != nil {
+			return err
+		}
+		if err := audits.Create(ctx, AuditLog{ID: "audit-service-token-tx-rollback", Action: "rotate", ResourceType: "service_token", ResourceID: rollbackOld.ID}); err != nil {
+			return err
+		}
+		return wantErr
+	})
+	if !errors.Is(err, wantErr) {
+		t.Fatalf("rollback service token transaction error = %v, want %v", err, wantErr)
+	}
+	rollbackOldAfter, err := repo.Get(ctx, rollbackOld.ID)
+	if err != nil {
+		t.Fatal(err)
+	}
+	if rollbackOldAfter.RevokedAt != nil {
+		t.Fatalf("rollback old token revoked at = %v, want nil", rollbackOldAfter.RevokedAt)
+	}
+	if _, err := repo.Get(ctx, rollbackReplacement.ID); !errors.Is(err, sql.ErrNoRows) {
+		t.Fatalf("rollback replacement lookup error = %v, want sql.ErrNoRows", err)
+	}
+	assertAuditExists(t, db.Audits(), "audit-service-token-tx-rollback", false)
+}
+
+func assertServiceToken(t *testing.T, got, want ServiceToken) {
+	t.Helper()
+	if got.ID != want.ID || got.OwnerUserID != want.OwnerUserID || got.AgentID != want.AgentID || got.NodeID != want.NodeID || got.Prefix != want.Prefix || got.TokenHash != want.TokenHash || got.Scope != want.Scope || got.Type != want.Type {
+		t.Fatalf("service token = %+v, want %+v", got, want)
+	}
+	if got.ExpiresAt == nil || want.ExpiresAt == nil || !got.ExpiresAt.Equal(*want.ExpiresAt) {
+		t.Fatalf("expires at = %v, want %v", got.ExpiresAt, want.ExpiresAt)
+	}
+	if !got.CreatedAt.Equal(want.CreatedAt) || !got.UpdatedAt.Equal(want.UpdatedAt) {
+		t.Fatalf("timestamps = (%v, %v), want (%v, %v)", got.CreatedAt, got.UpdatedAt, want.CreatedAt, want.UpdatedAt)
+	}
+}
+
+func assertAuditExists(t *testing.T, repo AuditRepository, id string, want bool) {
+	t.Helper()
+	page, err := repo.List(context.Background(), "", 100)
+	if err != nil {
+		t.Fatal(err)
+	}
+	for _, audit := range page.Items {
+		if audit.ID == id {
+			if !want {
+				t.Fatalf("audit %s exists after rollback", id)
+			}
+			return
+		}
+	}
+	if want {
+		t.Fatalf("audit %s does not exist after commit", id)
+	}
+}
diff -ruN .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-2-before/internal/storage/storage_contract_test.go .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-2-after/internal/storage/storage_contract_test.go
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-2-before/internal/storage/storage_contract_test.go	2026-09-06 08:25:14
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-2-after/internal/storage/storage_contract_test.go	2026-09-06 14:30:38
@@ -58,6 +58,8 @@
 		t.Fatalf("revoke token: %v", err)
 	}
 
+	runServiceTokenRepositoryContract(t, db)
+
 	agent := Agent{ID: "agent-1", Name: "edge", OwnerUserID: user.ID, Capabilities: `{"tcp":true}`}
 	if err := db.Agents().Create(ctx, agent); err != nil {
 		t.Fatalf("create agent: %v", err)
```
