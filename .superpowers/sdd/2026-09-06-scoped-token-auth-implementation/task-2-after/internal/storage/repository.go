package storage

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"
)

type UserRepository interface {
	Create(context.Context, User) error
	Get(context.Context, string) (User, error)
	GetByUsername(context.Context, string) (User, error)
	Update(context.Context, User) error
	Delete(context.Context, string) error
	List(context.Context, string, int) (Page[User], error)
}

type TokenRepository interface {
	Create(context.Context, APIToken) error
	Get(context.Context, string) (APIToken, error)
	GetByHash(context.Context, string) (APIToken, error)
	Revoke(context.Context, string, time.Time) error
	List(context.Context, string, int) (Page[APIToken], error)
}

type ServiceTokenFilter struct {
	OwnerUserID string
	Type        TokenType
	AgentID     string
	NodeID      string
}

type ServiceTokenRepository interface {
	Create(context.Context, ServiceToken) error
	Get(context.Context, string) (ServiceToken, error)
	GetByHash(context.Context, string) (ServiceToken, error)
	List(context.Context, ServiceTokenFilter, string, int) (Page[ServiceToken], error)
	Revoke(context.Context, string, time.Time) error
	TouchLastUsed(context.Context, string, time.Time) error
	Rotate(context.Context, string, ServiceToken, time.Time) error
}

var ErrServiceTokenRevoked = errors.New("service token is already revoked")

type AgentRepository interface {
	Create(context.Context, Agent) error
	Get(context.Context, string) (Agent, error)
	Update(context.Context, Agent) error
	Delete(context.Context, string) error
	List(context.Context, string, int) (Page[Agent], error)
}

type PolicyRepository interface {
	Create(context.Context, AgentPolicy) error
	Get(context.Context, string) (AgentPolicy, error)
	Update(context.Context, AgentPolicy) error
	Delete(context.Context, string) error
	ListByAgent(context.Context, string, string, int) (Page[AgentPolicy], error)
}

type TunnelRepository interface {
	Create(context.Context, Tunnel) error
	Get(context.Context, string) (Tunnel, error)
	Update(context.Context, Tunnel) error
	Delete(context.Context, string) error
	List(context.Context, string, int) (Page[Tunnel], error)
}

type NodeRepository interface {
	Create(context.Context, ServerNode) error
	Get(context.Context, string) (ServerNode, error)
	Update(context.Context, ServerNode) error
	Delete(context.Context, string) error
	List(context.Context, string, int) (Page[ServerNode], error)
}

var ErrMetadataStale = errors.New("runtime metadata is stale")

type AgentMetadataRepository interface {
	Upsert(context.Context, AgentRuntimeMetadata) error
	Get(context.Context, string) (AgentRuntimeMetadata, error)
	List(context.Context, string, int) (Page[AgentRuntimeMetadata], error)
	MarkStale(context.Context, string, int64) error
	Touch(context.Context, string, int64, time.Time, time.Time) error
}

type LeaseRepository interface {
	Acquire(context.Context, AgentLease) (AgentLease, error)
	Renew(context.Context, string, int64, time.Duration) error
	Release(context.Context, string, int64) error
	Get(context.Context, string) (AgentLease, error)
}

type AuditRepository interface {
	Create(context.Context, AuditLog) error
	List(context.Context, string, int) (Page[AuditLog], error)
}

type IdempotencyRepository interface {
	Put(context.Context, IdempotencyRecord) error
	Get(context.Context, string) (IdempotencyRecord, error)
	Delete(context.Context, string) error
}

// AtomicIdempotencyRepository is an optional stronger contract used by the
// HTTP API when the backing store supports compare-and-set key claiming.
type AtomicIdempotencyRepository interface {
	IdempotencyRepository
	Claim(context.Context, IdempotencyRecord) (existing IdempotencyRecord, claimed bool, err error)
	Update(context.Context, IdempotencyRecord) error
}

// Constructor helpers are useful for services that own a database/sql handle
// directly (for example tests or read-only reporting jobs).
func NewUserRepository(db *sql.DB) UserRepository   { return &userRepo{db} }
func NewTokenRepository(db *sql.DB) TokenRepository { return &tokenRepo{db} }
func NewServiceTokenRepository(db *sql.DB) ServiceTokenRepository {
	return NewServiceTokenRepositoryWithDriver(db, DriverSQLite)
}
func NewServiceTokenRepositoryWithDriver(db *sql.DB, driver string) ServiceTokenRepository {
	driver = strings.ToLower(strings.TrimSpace(driver))
	if driver == "sqlite3" {
		driver = DriverSQLite
	}
	if driver != DriverMySQL && driver != DriverSQLite {
		panic(fmt.Sprintf("unsupported service token repository driver %q", driver))
	}
	return &serviceTokenRepo{db: db, starter: db, driver: driver}
}
func NewAgentRepository(db *sql.DB) AgentRepository   { return &agentRepo{db} }
func NewPolicyRepository(db *sql.DB) PolicyRepository { return &policyRepo{db} }
func NewTunnelRepository(db *sql.DB) TunnelRepository { return &tunnelRepo{db} }
func NewNodeRepository(db *sql.DB) NodeRepository     { return &nodeRepo{db} }
func NewAgentMetadataRepository(db *sql.DB) AgentMetadataRepository {
	return &agentMetadataRepo{db: db, driver: DriverSQLite}
}
func NewAgentMetadataRepositoryWithDriver(db *sql.DB, driver string) AgentMetadataRepository {
	if strings.EqualFold(driver, "mysql") {
		return &agentMetadataRepo{db: db, driver: DriverMySQL}
	}
	return &agentMetadataRepo{db: db, driver: DriverSQLite}
}

// NewLeaseRepository is retained for SQLite callers. MySQL callers must use
// NewLeaseRepositoryWithDriver so row-lock fencing is enabled explicitly.
// Deprecated: use NewLeaseRepositoryWithDriver for non-SQLite databases.
func NewLeaseRepository(db *sql.DB) LeaseRepository {
	return NewLeaseRepositoryWithDriver(db, DriverSQLite)
}

// NewLeaseRepositoryWithDriver constructs a lease repository with the driver
// capability needed to select the correct concurrency strategy.
func NewLeaseRepositoryWithDriver(db *sql.DB, driver string) LeaseRepository {
	driver = strings.ToLower(strings.TrimSpace(driver))
	if driver == "sqlite3" {
		driver = DriverSQLite
	}
	if driver != DriverMySQL && driver != DriverSQLite {
		panic(fmt.Sprintf("unsupported lease repository driver %q", driver))
	}
	return &leaseRepo{db: db, driver: driver}
}
func NewAuditRepository(db *sql.DB) AuditRepository { return &auditRepo{db} }
func NewIdempotencyRepository(db *sql.DB) IdempotencyRepository {
	return &idempotencyRepo{db}
}

type sqlRepositories struct{ db *sql.DB }

func (r *sqlRepositories) CreateUser(ctx context.Context, v User) error {
	return r.users().Create(ctx, v)
}
func (r *sqlRepositories) users() *userRepo      { return &userRepo{r.db} }
func (r *sqlRepositories) tokens() *tokenRepo    { return &tokenRepo{r.db} }
func (r *sqlRepositories) agents() *agentRepo    { return &agentRepo{r.db} }
func (r *sqlRepositories) policies() *policyRepo { return &policyRepo{r.db} }
func (r *sqlRepositories) tunnels() *tunnelRepo  { return &tunnelRepo{r.db} }
func (r *sqlRepositories) nodes() *nodeRepo      { return &nodeRepo{r.db} }
func (r *sqlRepositories) leases() *leaseRepo {
	return NewLeaseRepositoryWithDriver(r.db, DriverSQLite).(*leaseRepo)
}
func (r *sqlRepositories) audits() *auditRepo            { return &auditRepo{r.db} }
func (r *sqlRepositories) idempotency() *idempotencyRepo { return &idempotencyRepo{r.db} }

func newID(prefix string) string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
	}
	return prefix + "-" + base64.RawURLEncoding.EncodeToString(b)
}
func stamp(id string, created, updated time.Time, prefix string) (string, time.Time, time.Time) {
	if id == "" {
		id = newID(prefix)
	}
	if created.IsZero() {
		created = time.Now().UTC()
	}
	if updated.IsZero() {
		updated = created
	}
	return id, created, updated
}
func stampCreate(id string, created time.Time, prefix string) (string, time.Time) {
	if id == "" {
		id = newID(prefix)
	}
	if created.IsZero() {
		created = time.Now().UTC()
	}
	return id, created
}
func tm(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}
func parseTM(s sql.NullString) *time.Time {
	if !s.Valid || s.String == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, s.String)
	if err != nil {
		return nil
	}
	return &t
}
func parseTime(s string) time.Time { t, _ := time.Parse(time.RFC3339Nano, s); return t }
func pageArgs(cursor string, limit int) (string, int) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	return cursor, limit
}
func encodeCursor(id string) string {
	if id == "" {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString([]byte(id))
}
func decodeCursor(cursor string) string {
	if cursor == "" {
		return ""
	}
	b, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return cursor
	}
	return string(b)
}
func pageLimit(limit int) int {
	if limit <= 0 || limit > 500 {
		return 50
	}
	return limit
}

type dbExecutor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type transactionStarter interface {
	BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
}

type userRepo struct{ db dbExecutor }

func (r *userRepo) Create(ctx context.Context, v User) error {
	v.ID, v.CreatedAt, v.UpdatedAt = stamp(v.ID, v.CreatedAt, v.UpdatedAt, "user")
	if v.Role == "" {
		v.Role = "user"
	}
	_, err := r.db.ExecContext(ctx, `INSERT INTO users(id,username,role,password_hash,disabled,created_at,updated_at) VALUES(?,?,?,?,?,?,?)`, v.ID, v.Username, v.Role, v.PasswordHash, boolInt(v.Disabled), tm(v.CreatedAt), tm(v.UpdatedAt))
	return err
}
func (r *userRepo) Get(ctx context.Context, id string) (User, error) {
	var v User
	var created, updated string
	err := r.db.QueryRowContext(ctx, `SELECT id,username,role,password_hash,disabled,created_at,updated_at FROM users WHERE id=?`, id).Scan(&v.ID, &v.Username, &v.Role, &v.PasswordHash, &v.Disabled, &created, &updated)
	v.CreatedAt = parseTime(created)
	v.UpdatedAt = parseTime(updated)
	return v, err
}
func (r *userRepo) GetByUsername(ctx context.Context, n string) (User, error) {
	var v User
	var created, updated string
	err := r.db.QueryRowContext(ctx, `SELECT id,username,role,password_hash,disabled,created_at,updated_at FROM users WHERE username=?`, n).Scan(&v.ID, &v.Username, &v.Role, &v.PasswordHash, &v.Disabled, &created, &updated)
	v.CreatedAt = parseTime(created)
	v.UpdatedAt = parseTime(updated)
	return v, err
}
func (r *userRepo) Update(ctx context.Context, v User) error {
	if v.UpdatedAt.IsZero() {
		v.UpdatedAt = time.Now().UTC()
	}
	res, err := r.db.ExecContext(ctx, `UPDATE users SET username=?,role=?,password_hash=?,disabled=?,updated_at=? WHERE id=?`, v.Username, v.Role, v.PasswordHash, boolInt(v.Disabled), tm(v.UpdatedAt), v.ID)
	return checkAffected(res, err)
}
func (r *userRepo) Delete(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM users WHERE id=?`, id)
	return checkAffected(res, err)
}
func (r *userRepo) List(ctx context.Context, cursor string, limit int) (Page[User], error) {
	cursor, limit = pageArgs(cursor, limit)
	q := `SELECT id,username,role,password_hash,disabled,created_at,updated_at FROM users`
	args := []any{}
	if c := decodeCursor(cursor); c != "" {
		q += ` WHERE id>?`
		args = append(args, c)
	}
	q += ` ORDER BY id LIMIT ?`
	args = append(args, limit+1)
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return Page[User]{}, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var v User
		var c, u string
		if err := rows.Scan(&v.ID, &v.Username, &v.Role, &v.PasswordHash, &v.Disabled, &c, &u); err != nil {
			return Page[User]{}, err
		}
		v.CreatedAt = parseTime(c)
		v.UpdatedAt = parseTime(u)
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return Page[User]{}, err
	}
	p := Page[User]{Items: out}
	if len(out) > limit {
		p.Items = out[:limit]
		p.HasMore = true
		p.NextCursor = encodeCursor(p.Items[len(p.Items)-1].ID)
	}
	return p, nil
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
func checkAffected(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, e := res.RowsAffected()
	if e != nil {
		return e
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

type tokenRepo struct{ db dbExecutor }

func (r *tokenRepo) Create(ctx context.Context, v APIToken) error {
	v.ID, v.CreatedAt = stampCreate(v.ID, v.CreatedAt, "tok")
	_, err := r.db.ExecContext(ctx, `INSERT INTO api_tokens(id,user_id,token_hash,idempotency_key,expires_at,revoked_at,created_at) VALUES(?,?,?,?,?,?,?)`, v.ID, v.UserID, v.TokenHash, v.IdempotencyKey, nullableTime(v.ExpiresAt), nullableTime(v.RevokedAt), tm(v.CreatedAt))
	return err
}
func (r *tokenRepo) Get(ctx context.Context, id string) (APIToken, error) {
	var v APIToken
	var exp, rev, created sql.NullString
	err := r.db.QueryRowContext(ctx, `SELECT id,user_id,token_hash,idempotency_key,expires_at,revoked_at,created_at FROM api_tokens WHERE id=?`, id).Scan(&v.ID, &v.UserID, &v.TokenHash, &v.IdempotencyKey, &exp, &rev, &created)
	v.ExpiresAt = parseTM(exp)
	v.RevokedAt = parseTM(rev)
	if created.Valid {
		v.CreatedAt = parseTime(created.String)
	}
	return v, err
}
func (r *tokenRepo) GetByHash(ctx context.Context, h string) (APIToken, error) {
	var v APIToken
	var exp, rev, created sql.NullString
	err := r.db.QueryRowContext(ctx, `SELECT id,user_id,token_hash,idempotency_key,expires_at,revoked_at,created_at FROM api_tokens WHERE token_hash=?`, h).Scan(&v.ID, &v.UserID, &v.TokenHash, &v.IdempotencyKey, &exp, &rev, &created)
	v.ExpiresAt = parseTM(exp)
	v.RevokedAt = parseTM(rev)
	if created.Valid {
		v.CreatedAt = parseTime(created.String)
	}
	return v, err
}
func (r *tokenRepo) Revoke(ctx context.Context, id string, when time.Time) error {
	res, err := r.db.ExecContext(ctx, `UPDATE api_tokens SET revoked_at=? WHERE id=?`, tm(when), id)
	return checkAffected(res, err)
}
func (r *tokenRepo) List(ctx context.Context, cursor string, limit int) (Page[APIToken], error) {
	cursor, limit = pageArgs(cursor, limit)
	q := `SELECT id,user_id,token_hash,idempotency_key,expires_at,revoked_at,created_at FROM api_tokens`
	args := []any{}
	if c := decodeCursor(cursor); c != "" {
		q += ` WHERE id>?`
		args = append(args, c)
	}
	q += ` ORDER BY id LIMIT ?`
	args = append(args, limit+1)
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return Page[APIToken]{}, err
	}
	defer rows.Close()
	var out []APIToken
	for rows.Next() {
		var v APIToken
		var exp, rev, created sql.NullString
		if err := rows.Scan(&v.ID, &v.UserID, &v.TokenHash, &v.IdempotencyKey, &exp, &rev, &created); err != nil {
			return Page[APIToken]{}, err
		}
		v.ExpiresAt = parseTM(exp)
		v.RevokedAt = parseTM(rev)
		if created.Valid {
			v.CreatedAt = parseTime(created.String)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return Page[APIToken]{}, err
	}
	p := Page[APIToken]{Items: out}
	if len(out) > limit {
		p.Items = out[:limit]
		p.HasMore = true
		p.NextCursor = encodeCursor(p.Items[len(p.Items)-1].ID)
	}
	return p, nil
}

const serviceTokenColumns = `id,token_type,owner_user_id,agent_id,node_id,token_prefix,token_hash,scope,expires_at,revoked_at,last_used_at,created_at,updated_at`

type serviceTokenRepo struct {
	db      dbExecutor
	starter transactionStarter
	driver  string
}

func (r *serviceTokenRepo) Create(ctx context.Context, v ServiceToken) error {
	return createServiceToken(ctx, r.db, v)
}

func createServiceToken(ctx context.Context, db dbExecutor, v ServiceToken) error {
	v.ID, v.CreatedAt, v.UpdatedAt = stamp(v.ID, v.CreatedAt, v.UpdatedAt, "stok")
	_, err := db.ExecContext(ctx, `INSERT INTO service_tokens(id,token_type,owner_user_id,agent_id,node_id,token_prefix,token_hash,scope,expires_at,revoked_at,last_used_at,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, v.ID, v.Type, nullableString(v.OwnerUserID), nullableString(v.AgentID), nullableString(v.NodeID), v.Prefix, v.TokenHash, v.Scope, nullableTime(v.ExpiresAt), nullableTime(v.RevokedAt), nullableTime(v.LastUsedAt), tm(v.CreatedAt), tm(v.UpdatedAt))
	return err
}

func (r *serviceTokenRepo) Get(ctx context.Context, id string) (ServiceToken, error) {
	return scanServiceToken(r.db.QueryRowContext(ctx, `SELECT `+serviceTokenColumns+` FROM service_tokens WHERE id=?`, id))
}

func (r *serviceTokenRepo) GetByHash(ctx context.Context, hash string) (ServiceToken, error) {
	return scanServiceToken(r.db.QueryRowContext(ctx, `SELECT `+serviceTokenColumns+` FROM service_tokens WHERE token_hash=?`, hash))
}

func (r *serviceTokenRepo) List(ctx context.Context, filter ServiceTokenFilter, cursor string, limit int) (Page[ServiceToken], error) {
	cursor, limit = pageArgs(cursor, limit)
	conditions := make([]string, 0, 5)
	args := make([]any, 0, 6)
	if filter.OwnerUserID != "" {
		conditions = append(conditions, `owner_user_id=?`)
		args = append(args, filter.OwnerUserID)
	}
	if filter.Type != "" {
		conditions = append(conditions, `token_type=?`)
		args = append(args, filter.Type)
	}
	if filter.AgentID != "" {
		conditions = append(conditions, `agent_id=?`)
		args = append(args, filter.AgentID)
	}
	if filter.NodeID != "" {
		conditions = append(conditions, `node_id=?`)
		args = append(args, filter.NodeID)
	}
	if decoded := decodeCursor(cursor); decoded != "" {
		conditions = append(conditions, `id>?`)
		args = append(args, decoded)
	}
	query := `SELECT ` + serviceTokenColumns + ` FROM service_tokens`
	if len(conditions) > 0 {
		query += ` WHERE ` + strings.Join(conditions, ` AND `)
	}
	query += ` ORDER BY id LIMIT ?`
	args = append(args, limit+1)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return Page[ServiceToken]{}, err
	}
	defer rows.Close()
	page := Page[ServiceToken]{}
	for rows.Next() {
		token, err := scanServiceToken(rows)
		if err != nil {
			return Page[ServiceToken]{}, err
		}
		page.Items = append(page.Items, token)
	}
	if err := rows.Err(); err != nil {
		return Page[ServiceToken]{}, err
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.HasMore = true
		page.NextCursor = encodeCursor(page.Items[len(page.Items)-1].ID)
	}
	return page, nil
}

func (r *serviceTokenRepo) Revoke(ctx context.Context, id string, when time.Time) error {
	when = timeOrNow(when)
	res, err := r.db.ExecContext(ctx, `UPDATE service_tokens SET revoked_at=?,updated_at=? WHERE id=?`, tm(when), tm(when), id)
	return checkAffected(res, err)
}

func (r *serviceTokenRepo) TouchLastUsed(ctx context.Context, id string, when time.Time) error {
	when = timeOrNow(when)
	res, err := r.db.ExecContext(ctx, `UPDATE service_tokens SET last_used_at=?,updated_at=? WHERE id=?`, tm(when), tm(when), id)
	return checkAffected(res, err)
}

func (r *serviceTokenRepo) Rotate(ctx context.Context, oldID string, replacement ServiceToken, when time.Time) error {
	when = timeOrNow(when)
	if r.starter == nil {
		return rotateServiceToken(ctx, r.db, r.driver, oldID, replacement, when)
	}
	tx, err := r.starter.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := rotateServiceToken(ctx, tx, r.driver, oldID, replacement, when); err != nil {
		return err
	}
	return tx.Commit()
}

func rotateServiceToken(ctx context.Context, db dbExecutor, driver, oldID string, replacement ServiceToken, when time.Time) error {
	query := `SELECT revoked_at FROM service_tokens WHERE id=?`
	if driver == DriverMySQL {
		query += ` FOR UPDATE`
	}
	var revoked sql.NullString
	if err := db.QueryRowContext(ctx, query, oldID).Scan(&revoked); err != nil {
		return err
	}
	if parseTM(revoked) != nil {
		return ErrServiceTokenRevoked
	}
	if err := createServiceToken(ctx, db, replacement); err != nil {
		return err
	}
	res, err := db.ExecContext(ctx, `UPDATE service_tokens SET revoked_at=?,updated_at=? WHERE id=? AND revoked_at IS NULL`, tm(when), tm(when), oldID)
	return checkAffected(res, err)
}

type serviceTokenScanner interface {
	Scan(...any) error
}

func scanServiceToken(scanner serviceTokenScanner) (ServiceToken, error) {
	var token ServiceToken
	var tokenType string
	var owner, agent, node sql.NullString
	var expires, revoked, lastUsed, created, updated sql.NullString
	err := scanner.Scan(&token.ID, &tokenType, &owner, &agent, &node, &token.Prefix, &token.TokenHash, &token.Scope, &expires, &revoked, &lastUsed, &created, &updated)
	if err != nil {
		return ServiceToken{}, err
	}
	token.Type = TokenType(tokenType)
	if owner.Valid {
		token.OwnerUserID = owner.String
	}
	if agent.Valid {
		token.AgentID = agent.String
	}
	if node.Valid {
		token.NodeID = node.String
	}
	token.ExpiresAt = parseTM(expires)
	token.RevokedAt = parseTM(revoked)
	token.LastUsedAt = parseTM(lastUsed)
	if created.Valid {
		token.CreatedAt = parseTime(created.String)
	}
	if updated.Valid {
		token.UpdatedAt = parseTime(updated.String)
	}
	return token, nil
}

type agentRepo struct{ db *sql.DB }

func (r *agentRepo) Create(ctx context.Context, v Agent) error {
	v.ID, v.CreatedAt, v.UpdatedAt = stamp(v.ID, v.CreatedAt, v.UpdatedAt, "agent")
	if v.Capabilities == "" {
		v.Capabilities = "{}"
	}
	_, err := r.db.ExecContext(ctx, `INSERT INTO agents(id,name,owner_user_id,capabilities,enabled,created_at,updated_at) VALUES(?,?,?,?,?,?,?)`, v.ID, v.Name, v.OwnerUserID, v.Capabilities, boolInt(v.Enabled), tm(v.CreatedAt), tm(v.UpdatedAt))
	return err
}
func (r *agentRepo) Get(ctx context.Context, id string) (Agent, error) {
	var v Agent
	var c, u string
	err := r.db.QueryRowContext(ctx, `SELECT id,name,owner_user_id,capabilities,enabled,created_at,updated_at FROM agents WHERE id=?`, id).Scan(&v.ID, &v.Name, &v.OwnerUserID, &v.Capabilities, &v.Enabled, &c, &u)
	v.CreatedAt = parseTime(c)
	v.UpdatedAt = parseTime(u)
	return v, err
}
func (r *agentRepo) Update(ctx context.Context, v Agent) error {
	if v.UpdatedAt.IsZero() {
		v.UpdatedAt = time.Now().UTC()
	}
	res, err := r.db.ExecContext(ctx, `UPDATE agents SET name=?,owner_user_id=?,capabilities=?,enabled=?,updated_at=? WHERE id=?`, v.Name, v.OwnerUserID, v.Capabilities, boolInt(v.Enabled), tm(v.UpdatedAt), v.ID)
	return checkAffected(res, err)
}
func (r *agentRepo) Delete(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM agents WHERE id=?`, id)
	return checkAffected(res, err)
}
func (r *agentRepo) List(ctx context.Context, cursor string, limit int) (Page[Agent], error) {
	cursor, limit = pageArgs(cursor, limit)
	q := `SELECT id,name,owner_user_id,capabilities,enabled,created_at,updated_at FROM agents`
	args := []any{}
	if c := decodeCursor(cursor); c != "" {
		q += ` WHERE id>?`
		args = append(args, c)
	}
	q += ` ORDER BY id LIMIT ?`
	args = append(args, limit+1)
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return Page[Agent]{}, err
	}
	defer rows.Close()
	var out []Agent
	for rows.Next() {
		var v Agent
		var c, u string
		if err := rows.Scan(&v.ID, &v.Name, &v.OwnerUserID, &v.Capabilities, &v.Enabled, &c, &u); err != nil {
			return Page[Agent]{}, err
		}
		v.CreatedAt = parseTime(c)
		v.UpdatedAt = parseTime(u)
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return Page[Agent]{}, err
	}
	p := Page[Agent]{Items: out}
	if len(out) > limit {
		p.Items = out[:limit]
		p.HasMore = true
		p.NextCursor = encodeCursor(p.Items[len(p.Items)-1].ID)
	}
	return p, nil
}

func nullableTime(v *time.Time) any {
	if v == nil {
		return nil
	}
	return tm(*v)
}

type policyRepo struct{ db *sql.DB }

func (r *policyRepo) Create(ctx context.Context, v AgentPolicy) error {
	v.ID, v.CreatedAt, v.UpdatedAt = stamp(v.ID, v.CreatedAt, v.UpdatedAt, "policy")
	if v.AllowedCIDRs == "" {
		v.AllowedCIDRs = "[]"
	}
	if v.AllowedPorts == "" {
		v.AllowedPorts = "[]"
	}
	_, err := r.db.ExecContext(ctx, `INSERT INTO agent_policies(id,agent_id,target_host,target_port,protocol,allowed_cidrs,allowed_ports,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?)`, v.ID, v.AgentID, v.TargetHost, v.TargetPort, v.Protocol, v.AllowedCIDRs, v.AllowedPorts, tm(v.CreatedAt), tm(v.UpdatedAt))
	return err
}
func (r *policyRepo) Get(ctx context.Context, id string) (AgentPolicy, error) {
	var v AgentPolicy
	var c, u string
	err := r.db.QueryRowContext(ctx, `SELECT id,agent_id,target_host,target_port,protocol,allowed_cidrs,allowed_ports,created_at,updated_at FROM agent_policies WHERE id=?`, id).Scan(&v.ID, &v.AgentID, &v.TargetHost, &v.TargetPort, &v.Protocol, &v.AllowedCIDRs, &v.AllowedPorts, &c, &u)
	v.CreatedAt = parseTime(c)
	v.UpdatedAt = parseTime(u)
	return v, err
}
func (r *policyRepo) Update(ctx context.Context, v AgentPolicy) error {
	if v.UpdatedAt.IsZero() {
		v.UpdatedAt = time.Now().UTC()
	}
	res, err := r.db.ExecContext(ctx, `UPDATE agent_policies SET agent_id=?,target_host=?,target_port=?,protocol=?,allowed_cidrs=?,allowed_ports=?,updated_at=? WHERE id=?`, v.AgentID, v.TargetHost, v.TargetPort, v.Protocol, v.AllowedCIDRs, v.AllowedPorts, tm(v.UpdatedAt), v.ID)
	return checkAffected(res, err)
}
func (r *policyRepo) Delete(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM agent_policies WHERE id=?`, id)
	return checkAffected(res, err)
}
func (r *policyRepo) ListByAgent(ctx context.Context, agentID, cursor string, limit int) (Page[AgentPolicy], error) {
	cursor, limit = pageArgs(cursor, limit)
	q := `SELECT id,agent_id,target_host,target_port,protocol,allowed_cidrs,allowed_ports,created_at,updated_at FROM agent_policies WHERE agent_id=?`
	args := []any{agentID}
	if c := decodeCursor(cursor); c != "" {
		q += ` AND id>?`
		args = append(args, c)
	}
	q += ` ORDER BY id LIMIT ?`
	args = append(args, limit+1)
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return Page[AgentPolicy]{}, err
	}
	defer rows.Close()
	var out []AgentPolicy
	for rows.Next() {
		var v AgentPolicy
		var c, u string
		if err := rows.Scan(&v.ID, &v.AgentID, &v.TargetHost, &v.TargetPort, &v.Protocol, &v.AllowedCIDRs, &v.AllowedPorts, &c, &u); err != nil {
			return Page[AgentPolicy]{}, err
		}
		v.CreatedAt = parseTime(c)
		v.UpdatedAt = parseTime(u)
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return Page[AgentPolicy]{}, err
	}
	p := Page[AgentPolicy]{Items: out}
	if len(out) > limit {
		p.Items = out[:limit]
		p.HasMore = true
		p.NextCursor = encodeCursor(p.Items[len(p.Items)-1].ID)
	}
	return p, nil
}

type tunnelRepo struct{ db *sql.DB }

func (r *tunnelRepo) Create(ctx context.Context, v Tunnel) error {
	v.ID, v.CreatedAt, v.UpdatedAt = stamp(v.ID, v.CreatedAt, v.UpdatedAt, "tunnel")
	if v.Status == "" {
		v.Status = "active"
	}
	if v.Config == "" {
		v.Config = "{}"
	}
	_, err := r.db.ExecContext(ctx, `INSERT INTO tunnels(id,group_id,agent_id,protocol,domain,path_prefix,target_host,target_port,public_port,status,config,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, v.ID, nullableString(v.GroupID), v.AgentID, v.Protocol, nullableString(v.Domain), nullableString(v.PathPrefix), v.TargetHost, v.TargetPort, nullableInt(v.PublicPort), v.Status, v.Config, tm(v.CreatedAt), tm(v.UpdatedAt))
	return err
}
func (r *tunnelRepo) Get(ctx context.Context, id string) (Tunnel, error) {
	var v Tunnel
	var group, domain, path, created, updated sql.NullString
	var public sql.NullInt64
	err := r.db.QueryRowContext(ctx, `SELECT id,group_id,agent_id,protocol,domain,path_prefix,target_host,target_port,public_port,status,config,created_at,updated_at FROM tunnels WHERE id=?`, id).Scan(&v.ID, &group, &v.AgentID, &v.Protocol, &domain, &path, &v.TargetHost, &v.TargetPort, &public, &v.Status, &v.Config, &created, &updated)
	if group.Valid {
		v.GroupID = group.String
	}
	if domain.Valid {
		v.Domain = domain.String
	}
	if path.Valid {
		v.PathPrefix = path.String
	}
	if public.Valid {
		v.PublicPort = int(public.Int64)
	}
	v.CreatedAt = parseTime(created.String)
	v.UpdatedAt = parseTime(updated.String)
	return v, err
}
func (r *tunnelRepo) Update(ctx context.Context, v Tunnel) error {
	if v.UpdatedAt.IsZero() {
		v.UpdatedAt = time.Now().UTC()
	}
	res, err := r.db.ExecContext(ctx, `UPDATE tunnels SET group_id=?,agent_id=?,protocol=?,domain=?,path_prefix=?,target_host=?,target_port=?,public_port=?,status=?,config=?,updated_at=? WHERE id=?`, nullableString(v.GroupID), v.AgentID, v.Protocol, nullableString(v.Domain), nullableString(v.PathPrefix), v.TargetHost, v.TargetPort, nullableInt(v.PublicPort), v.Status, v.Config, tm(v.UpdatedAt), v.ID)
	return checkAffected(res, err)
}
func (r *tunnelRepo) Delete(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM tunnels WHERE id=?`, id)
	return checkAffected(res, err)
}
func (r *tunnelRepo) List(ctx context.Context, cursor string, limit int) (Page[Tunnel], error) {
	cursor, limit = pageArgs(cursor, limit)
	q := `SELECT id,group_id,agent_id,protocol,domain,path_prefix,target_host,target_port,public_port,status,config,created_at,updated_at FROM tunnels`
	args := []any{}
	if c := decodeCursor(cursor); c != "" {
		q += ` WHERE id>?`
		args = append(args, c)
	}
	q += ` ORDER BY id LIMIT ?`
	args = append(args, limit+1)
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return Page[Tunnel]{}, err
	}
	defer rows.Close()
	var out []Tunnel
	for rows.Next() {
		var v Tunnel
		var group, domain, path, created, updated sql.NullString
		var public sql.NullInt64
		if err := rows.Scan(&v.ID, &group, &v.AgentID, &v.Protocol, &domain, &path, &v.TargetHost, &v.TargetPort, &public, &v.Status, &v.Config, &created, &updated); err != nil {
			return Page[Tunnel]{}, err
		}
		if group.Valid {
			v.GroupID = group.String
		}
		if domain.Valid {
			v.Domain = domain.String
		}
		if path.Valid {
			v.PathPrefix = path.String
		}
		if public.Valid {
			v.PublicPort = int(public.Int64)
		}
		v.CreatedAt = parseTime(created.String)
		v.UpdatedAt = parseTime(updated.String)
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return Page[Tunnel]{}, err
	}
	p := Page[Tunnel]{Items: out}
	if len(out) > limit {
		p.Items = out[:limit]
		p.HasMore = true
		p.NextCursor = encodeCursor(p.Items[len(p.Items)-1].ID)
	}
	return p, nil
}
func nullableString(v string) any {
	if v == "" {
		return nil
	}
	return v
}
func nullableInt(v int) any {
	if v == 0 {
		return nil
	}
	return v
}

type nodeRepo struct{ db *sql.DB }

func (r *nodeRepo) Create(ctx context.Context, v ServerNode) error {
	v.ID, v.CreatedAt, v.UpdatedAt = stamp(v.ID, v.CreatedAt, v.UpdatedAt, "node")
	if v.Metadata == "" {
		v.Metadata = "{}"
	}
	_, err := r.db.ExecContext(ctx, `INSERT INTO server_nodes(id,address,epoch,metadata,last_seen_at,expires_at,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?)`, v.ID, v.Address, v.Epoch, v.Metadata, nullableTime(v.LastSeenAt), nullableTime(v.ExpiresAt), tm(v.CreatedAt), tm(v.UpdatedAt))
	return err
}
func (r *nodeRepo) Get(ctx context.Context, id string) (ServerNode, error) {
	var v ServerNode
	var meta, seen, exp, created, updated sql.NullString
	err := r.db.QueryRowContext(ctx, `SELECT id,address,epoch,metadata,last_seen_at,expires_at,created_at,updated_at FROM server_nodes WHERE id=?`, id).Scan(&v.ID, &v.Address, &v.Epoch, &meta, &seen, &exp, &created, &updated)
	if meta.Valid {
		v.Metadata = meta.String
	}
	v.LastSeenAt = parseTM(seen)
	v.ExpiresAt = parseTM(exp)
	v.CreatedAt = parseTime(created.String)
	v.UpdatedAt = parseTime(updated.String)
	return v, err
}
func (r *nodeRepo) Update(ctx context.Context, v ServerNode) error {
	if v.UpdatedAt.IsZero() {
		v.UpdatedAt = time.Now().UTC()
	}
	res, err := r.db.ExecContext(ctx, `UPDATE server_nodes SET address=?,epoch=?,metadata=?,last_seen_at=?,expires_at=?,updated_at=? WHERE id=?`, v.Address, v.Epoch, v.Metadata, nullableTime(v.LastSeenAt), nullableTime(v.ExpiresAt), tm(v.UpdatedAt), v.ID)
	return checkAffected(res, err)
}
func (r *nodeRepo) Delete(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM server_nodes WHERE id=?`, id)
	return checkAffected(res, err)
}
func (r *nodeRepo) List(ctx context.Context, cursor string, limit int) (Page[ServerNode], error) {
	cursor, limit = pageArgs(cursor, limit)
	q := `SELECT id,address,epoch,metadata,last_seen_at,expires_at,created_at,updated_at FROM server_nodes`
	args := []any{}
	if c := decodeCursor(cursor); c != "" {
		q += ` WHERE id>?`
		args = append(args, c)
	}
	q += ` ORDER BY id LIMIT ?`
	args = append(args, limit+1)
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return Page[ServerNode]{}, err
	}
	defer rows.Close()
	var out []ServerNode
	for rows.Next() {
		var v ServerNode
		var meta, seen, exp, created, updated sql.NullString
		if err := rows.Scan(&v.ID, &v.Address, &v.Epoch, &meta, &seen, &exp, &created, &updated); err != nil {
			return Page[ServerNode]{}, err
		}
		if meta.Valid {
			v.Metadata = meta.String
		}
		v.LastSeenAt = parseTM(seen)
		v.ExpiresAt = parseTM(exp)
		v.CreatedAt = parseTime(created.String)
		v.UpdatedAt = parseTime(updated.String)
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return Page[ServerNode]{}, err
	}
	p := Page[ServerNode]{Items: out}
	if len(out) > limit {
		p.Items = out[:limit]
		p.HasMore = true
		p.NextCursor = encodeCursor(p.Items[len(p.Items)-1].ID)
	}
	return p, nil
}

type agentMetadataRepo struct {
	db     *sql.DB
	driver string
}

func (r *agentMetadataRepo) Upsert(ctx context.Context, v AgentRuntimeMetadata) error {
	if v.AgentID == "" || v.NodeID == "" || v.Epoch <= 0 || v.Revision < 0 {
		return errors.New("invalid runtime metadata identity")
	}
	if v.Metadata == "" {
		v.Metadata = "{}"
	}
	now := time.Now().UTC()
	if v.ReportedAt.IsZero() {
		v.ReportedAt = now
	}
	if v.LastSeenAt.IsZero() {
		v.LastSeenAt = now
	}
	if v.UpdatedAt.IsZero() {
		v.UpdatedAt = now
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	selectSQL := `SELECT epoch,revision FROM agent_runtime_metadata WHERE agent_id=?`
	if r.driver == DriverMySQL {
		selectSQL += ` FOR UPDATE`
	}
	var epoch, revision int64
	err = tx.QueryRowContext(ctx, selectSQL, v.AgentID).Scan(&epoch, &revision)
	if errors.Is(err, sql.ErrNoRows) {
		_, err = tx.ExecContext(ctx, `INSERT INTO agent_runtime_metadata(agent_id,node_id,epoch,revision,metadata,reported_at,last_seen_at,expires_at,stale,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, v.AgentID, v.NodeID, v.Epoch, v.Revision, v.Metadata, tm(v.ReportedAt), tm(v.LastSeenAt), nullableTime(v.ExpiresAt), boolInt(v.Stale), tm(v.UpdatedAt))
		if err != nil {
			return err
		}
		return tx.Commit()
	}
	if err != nil {
		return err
	}
	if v.Epoch < epoch || (v.Epoch == epoch && v.Revision < revision) {
		return ErrMetadataStale
	}
	if v.Epoch == epoch && v.Revision == revision {
		return tx.Commit()
	}
	_, err = tx.ExecContext(ctx, `UPDATE agent_runtime_metadata SET node_id=?,epoch=?,revision=?,metadata=?,reported_at=?,last_seen_at=?,expires_at=?,stale=?,updated_at=? WHERE agent_id=?`, v.NodeID, v.Epoch, v.Revision, v.Metadata, tm(v.ReportedAt), tm(v.LastSeenAt), nullableTime(v.ExpiresAt), boolInt(v.Stale), tm(v.UpdatedAt), v.AgentID)
	if err != nil {
		return err
	}
	return tx.Commit()
}
func (r *agentMetadataRepo) Get(ctx context.Context, id string) (AgentRuntimeMetadata, error) {
	var v AgentRuntimeMetadata
	var reported, seen, exp, updated sql.NullString
	var stale int
	err := r.db.QueryRowContext(ctx, `SELECT agent_id,node_id,epoch,revision,metadata,reported_at,last_seen_at,expires_at,stale,updated_at FROM agent_runtime_metadata WHERE agent_id=?`, id).Scan(&v.AgentID, &v.NodeID, &v.Epoch, &v.Revision, &v.Metadata, &reported, &seen, &exp, &stale, &updated)
	v.ReportedAt = parseTime(reported.String)
	v.LastSeenAt = parseTime(seen.String)
	v.ExpiresAt = parseTM(exp)
	v.Stale = stale != 0
	v.UpdatedAt = parseTime(updated.String)
	return v, err
}
func (r *agentMetadataRepo) List(ctx context.Context, cursor string, limit int) (Page[AgentRuntimeMetadata], error) {
	cursor, limit = pageArgs(cursor, limit)
	q := `SELECT agent_id,node_id,epoch,revision,metadata,reported_at,last_seen_at,expires_at,stale,updated_at FROM agent_runtime_metadata`
	args := []any{}
	if c := decodeCursor(cursor); c != "" {
		q += ` WHERE agent_id>?`
		args = append(args, c)
	}
	q += ` ORDER BY agent_id LIMIT ?`
	args = append(args, limit+1)
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return Page[AgentRuntimeMetadata]{}, err
	}
	defer rows.Close()
	page := Page[AgentRuntimeMetadata]{}
	for rows.Next() {
		var v AgentRuntimeMetadata
		var reported, seen, exp, updated sql.NullString
		var stale int
		if err := rows.Scan(&v.AgentID, &v.NodeID, &v.Epoch, &v.Revision, &v.Metadata, &reported, &seen, &exp, &stale, &updated); err != nil {
			return Page[AgentRuntimeMetadata]{}, err
		}
		v.ReportedAt, v.LastSeenAt, v.ExpiresAt, v.Stale, v.UpdatedAt = parseTime(reported.String), parseTime(seen.String), parseTM(exp), stale != 0, parseTime(updated.String)
		page.Items = append(page.Items, v)
	}
	if err := rows.Err(); err != nil {
		return Page[AgentRuntimeMetadata]{}, err
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.HasMore = true
		page.NextCursor = encodeCursor(page.Items[len(page.Items)-1].AgentID)
	}
	return page, nil
}
func (r *agentMetadataRepo) MarkStale(ctx context.Context, id string, epoch int64) error {
	res, err := r.db.ExecContext(ctx, `UPDATE agent_runtime_metadata SET stale=1,updated_at=? WHERE agent_id=? AND epoch<=?`, tm(time.Now().UTC()), id, epoch)
	return checkAffected(res, err)
}
func (r *agentMetadataRepo) Touch(ctx context.Context, id string, epoch int64, lastSeenAt, expiresAt time.Time) error {
	if id == "" || epoch <= 0 || expiresAt.IsZero() {
		return errors.New("invalid runtime metadata lease")
	}
	if lastSeenAt.IsZero() {
		lastSeenAt = time.Now().UTC()
	}
	res, err := r.db.ExecContext(ctx, `UPDATE agent_runtime_metadata SET last_seen_at=?,expires_at=?,stale=0,updated_at=? WHERE agent_id=? AND epoch=?`, tm(lastSeenAt), tm(expiresAt), tm(time.Now().UTC()), id, epoch)
	return checkAffected(res, err)
}

type leaseRepo struct {
	db     *sql.DB
	driver string
}

func (r *leaseRepo) Acquire(ctx context.Context, v AgentLease) (AgentLease, error) {
	if v.TTL <= 0 {
		v.TTL = time.Minute
	}
	// MySQL locks the current lease row while deciding takeover. Other drivers
	// use an epoch compare-and-swap below; this keeps SQLite portable while
	// preventing two concurrent expired-lease owners from publishing one epoch.
	for attempt := 0; attempt < 8; attempt++ {
		now := time.Now().UTC()
		exp := now.Add(v.TTL)
		tx, err := r.db.BeginTx(ctx, nil)
		if err != nil {
			return AgentLease{}, err
		}
		var epoch int64
		var oldNode string
		var oldExp sql.NullString
		selectSQL := `SELECT epoch,node_id,expires_at FROM agent_runtime_leases WHERE agent_id=?`
		if r.driver == DriverMySQL {
			selectSQL += ` FOR UPDATE`
		}
		queryErr := tx.QueryRowContext(ctx, selectSQL, v.AgentID).Scan(&epoch, &oldNode, &oldExp)
		if queryErr != nil && !errors.Is(queryErr, sql.ErrNoRows) {
			_ = tx.Rollback()
			return AgentLease{}, queryErr
		}
		if queryErr == nil {
			active := oldExp.Valid && parseTime(oldExp.String).After(now)
			if active && oldNode != v.NodeID {
				_ = tx.Rollback()
				return AgentLease{}, fmt.Errorf("agent %s lease is held by %s", v.AgentID, oldNode)
			}
			newEpoch := epoch + 1
			res, updateErr := tx.ExecContext(ctx, `UPDATE agent_runtime_leases SET node_id=?,epoch=?,acquired_at=?,expires_at=?,updated_at=? WHERE agent_id=? AND epoch=?`, v.NodeID, newEpoch, tm(now), tm(exp), tm(now), v.AgentID, epoch)
			if updateErr != nil {
				_ = tx.Rollback()
				return AgentLease{}, updateErr
			}
			affected, affectedErr := res.RowsAffected()
			if affectedErr != nil {
				_ = tx.Rollback()
				return AgentLease{}, affectedErr
			}
			if affected != 1 {
				_ = tx.Rollback()
				time.Sleep(time.Duration(attempt+1) * time.Millisecond)
				continue
			}
			if err = tx.Commit(); err != nil {
				return AgentLease{}, err
			}
			v.Epoch, v.AcquiredAt, v.ExpiresAt, v.UpdatedAt = newEpoch, now, exp, now
			return v, nil
		}

		_, insertErr := tx.ExecContext(ctx, `INSERT INTO agent_runtime_leases(agent_id,node_id,epoch,acquired_at,expires_at,updated_at) VALUES(?,?,?,?,?,?)`, v.AgentID, v.NodeID, 1, tm(now), tm(exp), tm(now))
		if insertErr != nil {
			_ = tx.Rollback()
			if isDuplicateError(insertErr) {
				time.Sleep(time.Duration(attempt+1) * time.Millisecond)
				continue
			}
			return AgentLease{}, insertErr
		}
		if err = tx.Commit(); err != nil {
			return AgentLease{}, err
		}
		v.Epoch, v.AcquiredAt, v.ExpiresAt, v.UpdatedAt = 1, now, exp, now
		return v, nil
	}
	return AgentLease{}, errors.New("lease acquisition contention exceeded retry budget")
}

func isDuplicateError(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "unique constraint") || strings.Contains(s, "duplicate") || strings.Contains(s, "already exists")
}
func (r *leaseRepo) Renew(ctx context.Context, agentID string, epoch int64, ttl time.Duration) error {
	if ttl <= 0 {
		ttl = time.Minute
	}
	now := time.Now().UTC()
	res, err := r.db.ExecContext(ctx, `UPDATE agent_runtime_leases SET expires_at=?,updated_at=? WHERE agent_id=? AND epoch=? AND expires_at>?`, tm(now.Add(ttl)), tm(now), agentID, epoch, tm(now))
	return checkAffected(res, err)
}
func (r *leaseRepo) Release(ctx context.Context, agentID string, epoch int64) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM agent_runtime_leases WHERE agent_id=? AND epoch=?`, agentID, epoch)
	return checkAffected(res, err)
}
func (r *leaseRepo) Get(ctx context.Context, agentID string) (AgentLease, error) {
	var v AgentLease
	var acquired, exp, updated string
	err := r.db.QueryRowContext(ctx, `SELECT agent_id,node_id,epoch,acquired_at,expires_at,updated_at FROM agent_runtime_leases WHERE agent_id=?`, agentID).Scan(&v.AgentID, &v.NodeID, &v.Epoch, &acquired, &exp, &updated)
	v.AcquiredAt = parseTime(acquired)
	v.ExpiresAt = parseTime(exp)
	v.UpdatedAt = parseTime(updated)
	if !v.ExpiresAt.IsZero() {
		v.TTL = time.Until(v.ExpiresAt)
	}
	return v, err
}

type auditRepo struct{ db dbExecutor }

func (r *auditRepo) Create(ctx context.Context, v AuditLog) error {
	v.ID, v.CreatedAt = stampCreate(v.ID, v.CreatedAt, "audit")
	if v.Details == "" {
		v.Details = "{}"
	}
	_, err := r.db.ExecContext(ctx, `INSERT INTO audit_logs(id,actor_user_id,action,resource_type,resource_id,details,created_at) VALUES(?,?,?,?,?,?,?)`, v.ID, nullableString(v.ActorUserID), v.Action, v.ResourceType, nullableString(v.ResourceID), v.Details, tm(v.CreatedAt))
	return err
}
func (r *auditRepo) List(ctx context.Context, cursor string, limit int) (Page[AuditLog], error) {
	cursor, limit = pageArgs(cursor, limit)
	q := `SELECT id,actor_user_id,action,resource_type,resource_id,details,created_at FROM audit_logs`
	args := []any{}
	if c := decodeCursor(cursor); c != "" {
		q += ` WHERE id>?`
		args = append(args, c)
	}
	q += ` ORDER BY id LIMIT ?`
	args = append(args, limit+1)
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return Page[AuditLog]{}, err
	}
	defer rows.Close()
	var out []AuditLog
	for rows.Next() {
		var v AuditLog
		var actor, res, details, created sql.NullString
		if err := rows.Scan(&v.ID, &actor, &v.Action, &v.ResourceType, &res, &details, &created); err != nil {
			return Page[AuditLog]{}, err
		}
		if actor.Valid {
			v.ActorUserID = actor.String
		}
		if res.Valid {
			v.ResourceID = res.String
		}
		if details.Valid {
			v.Details = details.String
		}
		v.CreatedAt = parseTime(created.String)
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return Page[AuditLog]{}, err
	}
	p := Page[AuditLog]{Items: out}
	if len(out) > limit {
		p.Items = out[:limit]
		p.HasMore = true
		p.NextCursor = encodeCursor(p.Items[len(p.Items)-1].ID)
	}
	return p, nil
}

type idempotencyRepo struct{ db *sql.DB }

func (r *idempotencyRepo) Claim(ctx context.Context, v IdempotencyRecord) (IdempotencyRecord, bool, error) {
	if v.Key == "" {
		return IdempotencyRecord{}, false, errors.New("idempotency key is required")
	}
	v.CreatedAt = timeOrNow(v.CreatedAt)
	if v.ExpiresAt == nil {
		exp := v.CreatedAt.Add(24 * time.Hour)
		v.ExpiresAt = &exp
	}
	_, err := r.db.ExecContext(ctx, `INSERT INTO idempotency_keys(idempotency_key,user_id,response,status_code,created_at,expires_at) VALUES(?,?,?,?,?,?)`, v.Key, nullableString(v.UserID), "", 102, tm(v.CreatedAt), nullableTime(v.ExpiresAt))
	if err == nil {
		return IdempotencyRecord{}, true, nil
	}
	if !isDuplicateError(err) {
		return IdempotencyRecord{}, false, err
	}
	existing, getErr := r.Get(ctx, v.Key)
	if errors.Is(getErr, sql.ErrNoRows) {
		_, _ = r.db.ExecContext(ctx, `DELETE FROM idempotency_keys WHERE idempotency_key=? AND expires_at<?`, v.Key, tm(time.Now().UTC()))
		return r.Claim(ctx, v)
	}
	return existing, false, getErr
}

func (r *idempotencyRepo) Update(ctx context.Context, v IdempotencyRecord) error {
	v.CreatedAt = timeOrNow(v.CreatedAt)
	if v.StatusCode == 0 {
		v.StatusCode = 200
	}
	res, err := r.db.ExecContext(ctx, `UPDATE idempotency_keys SET response=?,status_code=?,created_at=?,expires_at=? WHERE idempotency_key=?`, v.Response, v.StatusCode, tm(v.CreatedAt), nullableTime(v.ExpiresAt), v.Key)
	return checkAffected(res, err)
}

func (r *idempotencyRepo) Put(ctx context.Context, v IdempotencyRecord) error {
	v.CreatedAt = timeOrNow(v.CreatedAt)
	if v.StatusCode == 0 {
		v.StatusCode = 200
	}
	_, err := r.db.ExecContext(ctx, `INSERT INTO idempotency_keys(idempotency_key,user_id,response,status_code,created_at,expires_at) VALUES(?,?,?,?,?,?)`, v.Key, nullableString(v.UserID), v.Response, v.StatusCode, tm(v.CreatedAt), nullableTime(v.ExpiresAt))
	return err
}
func (r *idempotencyRepo) Get(ctx context.Context, key string) (IdempotencyRecord, error) {
	var v IdempotencyRecord
	var user, created, exp sql.NullString
	err := r.db.QueryRowContext(ctx, `SELECT idempotency_key,user_id,response,status_code,created_at,expires_at FROM idempotency_keys WHERE idempotency_key=?`, key).Scan(&v.Key, &user, &v.Response, &v.StatusCode, &created, &exp)
	if user.Valid {
		v.UserID = user.String
	}
	v.CreatedAt = parseTime(created.String)
	v.ExpiresAt = parseTM(exp)
	if v.ExpiresAt != nil && !v.ExpiresAt.After(time.Now().UTC()) {
		return IdempotencyRecord{}, sql.ErrNoRows
	}
	return v, err
}
func (r *idempotencyRepo) Delete(ctx context.Context, key string) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM idempotency_keys WHERE idempotency_key=?`, key)
	return checkAffected(res, err)
}
func timeOrNow(v time.Time) time.Time {
	if v.IsZero() {
		return time.Now().UTC()
	}
	return v
}
