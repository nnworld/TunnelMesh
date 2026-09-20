package storage

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
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

type AccountStatus string

const (
	AccountStatusActive  AccountStatus = "active"
	AccountStatusDeleted AccountStatus = "deleted"
	AccountStatusAll     AccountStatus = "all"
)

// AccountUserRepository adds the administrator-facing lifecycle operations
// without widening the authentication repository contract used by test fakes.
type AccountUserRepository interface {
	UserRepository
	ListChildren(context.Context, AccountStatus, string, int) (Page[User], error)
	SoftDelete(context.Context, string, time.Time) error
	Restore(context.Context, string) error
}

// UserIdentityWriter covers the schema v14 identity columns. It is a separate
// interface so the authentication contract implemented by test fakes stays
// narrow.
type UserIdentityWriter interface {
	SetMFARequired(ctx context.Context, userID string, required bool) error
	SetAuthSource(ctx context.Context, userID string, source AuthSource) error
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
	UpdateExpiration(context.Context, string, *time.Time, time.Time) error
	UpdateScope(context.Context, string, string, time.Time) error
	TouchLastUsed(context.Context, string, time.Time) error
	Rotate(context.Context, string, ServiceToken, time.Time) error
}

// ServiceTokenSecretRepository is an optional extension implemented by the
// SQL repository. Keeping it separate preserves compatibility with validation
// fakes that only need hash-based authentication.
type ServiceTokenSecretRepository interface {
	ServiceTokenRepository
	MarkSecretRead(context.Context, string, time.Time) error
}

type CredentialRepository interface {
	Create(context.Context, Credential) (Credential, error)
	Get(context.Context, string) (Credential, error)
	Update(context.Context, Credential) error
	Delete(context.Context, string, time.Time) error
	Restore(context.Context, string) error
	List(context.Context, CredentialFilter, string, int) (Page[Credential], error)
}

type RemoteServerRepository interface {
	Create(context.Context, RemoteServer) (RemoteServer, error)
	Get(context.Context, string) (RemoteServer, error)
	Update(context.Context, RemoteServer) error
	Delete(context.Context, string, time.Time) error
	Restore(context.Context, string) error
	List(context.Context, RemoteServerFilter, string, int) (Page[RemoteServer], error)
	UpdateConnectionResult(context.Context, string, string, string, time.Time) error
}

type WebSSHSessionRepository interface {
	Create(context.Context, WebSSHSession) (WebSSHSession, error)
	Get(context.Context, string) (WebSSHSession, error)
	ConsumeTicket(context.Context, string, string, time.Time) (WebSSHSession, error)
	Close(context.Context, string, string, time.Time) error
	CountActiveByOwner(context.Context, string, time.Time) (int, error)
	ListActiveByOwner(context.Context, string, time.Time) ([]WebSSHSession, error)
	ListActivePage(context.Context, string, time.Time, string, int) (Page[WebSSHSession], error)
	ExpirePending(context.Context, time.Time) (int64, error)
	CloseExpiredActive(context.Context, time.Time) (int64, error)
	CloseActiveByNode(context.Context, string, time.Time, string) (int64, error)
}

var ErrVPNPeerConflict = errors.New("vpn peer already exists")
var ErrVPNPeerRevoked = errors.New("vpn peer is already revoked")
var ErrVPNIPLeaseHeld = errors.New("vpn ip subnet lease is held by another node")
var ErrVPNIPLeaseStaleEpoch = errors.New("vpn ip lease epoch is stale")

// VPNPeerRepository persists the peers the embedded VPN gateway hands out. It
// has no Delete on purpose: a peer is revoked, which keeps the public key and
// the address it held auditable instead of silently freeing them for reuse.
type VPNPeerRepository interface {
	Create(context.Context, VPNPeer) (VPNPeer, error)
	Get(context.Context, string) (VPNPeer, error)
	GetByPublicKey(context.Context, string) (VPNPeer, error)
	GetByNodeAndIP(context.Context, string, string) (VPNPeer, error)
	Update(context.Context, VPNPeer) error
	SetStatus(context.Context, string, VPNPeerStatus, time.Time) error
	List(context.Context, VPNPeerFilter, string, int) (Page[VPNPeer], error)
	ListByNode(context.Context, string) ([]VPNPeer, error)
	CountByNode(context.Context, string) (int, error)
	CountByOwner(context.Context, string) (int, error)
}

// VPNIPLeaseRepository arbitrates which server node owns which /24 of the VPN
// address pool. Every mutation is fenced by epoch so a node that lost its lease
// cannot keep allocating from a subnet another node already took over.
type VPNIPLeaseRepository interface {
	AcquireSubnet(context.Context, VPNIPLease) (VPNIPLease, error)
	Renew(context.Context, string, string, string, int64, time.Duration) error
	Release(context.Context, string, string, string, int64) error
	Get(context.Context, string, string) (VPNIPLease, error)
	ListByHolder(context.Context, string) ([]VPNIPLease, error)
	AddAllocated(context.Context, string, string, string, int64, int) (int, error)
}

var ErrServiceTokenRevoked = errors.New("service token is already revoked")
var ErrServiceTokenExpired = errors.New("service token is already expired")

type AgentRepository interface {
	Create(context.Context, Agent) error
	Get(context.Context, string) (Agent, error)
	Update(context.Context, Agent) error
	Delete(context.Context, string) error
	List(context.Context, string, int) (Page[Agent], error)
}

type PolicyStatus string

const (
	PolicyStatusActive  PolicyStatus = "active"
	PolicyStatusDeleted PolicyStatus = "deleted"
	PolicyStatusAll     PolicyStatus = "all"
)

type PolicyRepository interface {
	Create(context.Context, AgentPolicy) error
	Get(context.Context, string) (AgentPolicy, error)
	Update(context.Context, AgentPolicy) error
	Delete(context.Context, string) error
	ListByAgent(context.Context, string, string, int) (Page[AgentPolicy], error)
	ListByAgentStatus(context.Context, string, string, int, PolicyStatus) (Page[AgentPolicy], error)
	Restore(context.Context, string) error
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
	Ensure(context.Context, ServerNode) error
	Get(context.Context, string) (ServerNode, error)
	Update(context.Context, ServerNode) error
	Delete(context.Context, string) error
	List(context.Context, string, int) (Page[ServerNode], error)
	Touch(context.Context, string, time.Time, time.Time) error
	StatsByNodeIDs(context.Context, []string) (map[string]ServerNodeStats, error)
}

var ErrMetadataStale = errors.New("runtime metadata is stale")

type AgentMetadataRepository interface {
	Upsert(context.Context, AgentRuntimeMetadata) error
	Get(context.Context, string) (AgentRuntimeMetadata, error)
	List(context.Context, string, int) (Page[AgentRuntimeMetadata], error)
	MarkStale(context.Context, string, int64) error
	Touch(context.Context, string, int64, time.Time, time.Time) error
}

// AgentInstanceMetadataRepository adds instance-scoped operations while the
// legacy repository contract remains usable by existing fakes and callers.
type AgentInstanceMetadataRepository interface {
	AgentMetadataRepository
	GetInstance(context.Context, string, string) (AgentRuntimeMetadata, error)
	ListInstances(context.Context, string) ([]AgentRuntimeMetadata, error)
	MarkInstanceStale(context.Context, string, string, int64) error
	TouchInstance(context.Context, string, string, int64, time.Time, time.Time) error
}

var ErrRuntimeStatsStaleEpoch = errors.New("runtime stats epoch is stale")

// ErrStaleEpoch is a concise compatibility alias used by callers that share
// fencing errors across runtime metadata, leases and history repositories.
var ErrStaleEpoch = ErrRuntimeStatsStaleEpoch

type AgentRuntimeStatsRepository interface {
	Append(context.Context, AgentRuntimeStats) error
	ListRange(context.Context, string, time.Time, time.Time, string, int) (Page[AgentRuntimeStats], error)
	DeleteBefore(context.Context, string, time.Time) error
}

type AgentProbeResultRepository interface {
	Create(context.Context, AgentProbeResult) error
	ListByAgent(context.Context, string, string, int) (Page[AgentProbeResult], error)
	DeleteBefore(context.Context, time.Time) error
}

type LeaseRepository interface {
	Acquire(context.Context, AgentLease) (AgentLease, error)
	Renew(context.Context, string, int64, time.Duration) error
	Release(context.Context, string, int64) error
	Get(context.Context, string) (AgentLease, error)
	RegisterConnection(context.Context, AgentLease) (AgentLease, error)
	RenewConnection(context.Context, string, string, int64, time.Duration) error
	ReleaseConnection(context.Context, string, string, int64) error
	ListActiveByAgent(context.Context, string) ([]AgentLease, error)
	UpdateConnectionStats(context.Context, AgentLease) error
}

type AuditRepository interface {
	Create(context.Context, AuditLog) error
	List(context.Context, AuditFilter, string, int) (Page[AuditLog], error)
}

type IdempotencyRepository interface {
	Put(context.Context, IdempotencyRecord) error
	Get(context.Context, string) (IdempotencyRecord, error)
	Delete(context.Context, string) error
}

type AuthorizationRevisionRepository interface {
	Current(context.Context) (uint64, error)
}

type ClientInstanceRepository interface {
	Upsert(context.Context, ClientInstance) (ClientInstance, error)
	GetByOwnerAndInstance(context.Context, string, string) (ClientInstance, error)
	Get(context.Context, string) (ClientInstance, error)
	List(context.Context, ClientInstanceFilter, string, int) (Page[ClientInstance], error)
	TouchInstance(context.Context, string, string, time.Time, time.Time) error
	MarkStale(context.Context, string, time.Time) error
	MarkExpired(context.Context, time.Time) (int64, error)
}

type ClientConnectionRepository interface {
	Register(context.Context, ClientConnectionLease) (ClientConnectionLease, error)
	Get(context.Context, string) (ClientConnectionLease, error)
	Renew(context.Context, string, int64, time.Duration) error
	Release(context.Context, string, int64) error
	List(context.Context, ClientConnectionFilter, string, int) (Page[ClientConnectionLease], error)
	ListByInstance(context.Context, string) ([]ClientConnectionLease, error)
	ListByInstances(context.Context, []string) ([]ClientConnectionLease, error)
	UpdateStats(context.Context, ClientConnectionLease) error
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
func NewUserRepository(db *sql.DB) UserRepository {
	return &userRepo{db: db, driver: DriverSQLite}
}
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
func NewAgentRepository(db *sql.DB) AgentRepository {
	return &agentRepo{db: db, driver: DriverSQLite}
}
func NewPolicyRepository(db *sql.DB) PolicyRepository { return &policyRepo{db} }
func NewTunnelRepository(db *sql.DB) TunnelRepository { return &tunnelRepo{db} }
func NewNodeRepository(db *sql.DB) NodeRepository {
	return &nodeRepo{db: db, driver: DriverSQLite}
}
func NewAgentMetadataRepository(db *sql.DB) AgentMetadataRepository {
	return &agentMetadataRepo{db: db, driver: DriverSQLite}
}
func NewAgentMetadataRepositoryWithDriver(db *sql.DB, driver string) AgentMetadataRepository {
	if strings.EqualFold(driver, "mysql") {
		return &agentMetadataRepo{db: db, driver: DriverMySQL}
	}
	return &agentMetadataRepo{db: db, driver: DriverSQLite}
}
func NewClientInstanceRepositoryWithDriver(db *sql.DB, driver string) ClientInstanceRepository {
	return &clientInstanceRepo{db: db, driver: normalizeDriver(driver)}
}
func NewClientConnectionRepositoryWithDriver(db *sql.DB, driver string) ClientConnectionRepository {
	return &clientConnectionRepo{db: db, driver: normalizeDriver(driver)}
}
func normalizeDriver(driver string) string {
	driver = strings.ToLower(strings.TrimSpace(driver))
	if driver == "sqlite3" {
		return DriverSQLite
	}
	return driver
}

func NewAgentRuntimeStatsRepository(db *sql.DB) AgentRuntimeStatsRepository {
	return &agentRuntimeStatsRepo{db: db}
}
func NewAgentProbeResultRepository(db *sql.DB) AgentProbeResultRepository {
	return &agentProbeResultRepo{db: db}
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
func (r *sqlRepositories) users() *userRepo {
	return &userRepo{db: r.db, driver: DriverSQLite}
}
func (r *sqlRepositories) tokens() *tokenRepo { return &tokenRepo{r.db} }
func (r *sqlRepositories) agents() *agentRepo {
	return &agentRepo{db: r.db, driver: DriverSQLite}
}
func (r *sqlRepositories) policies() *policyRepo { return &policyRepo{r.db} }
func (r *sqlRepositories) tunnels() *tunnelRepo  { return &tunnelRepo{r.db} }
func (r *sqlRepositories) nodes() *nodeRepo {
	return &nodeRepo{db: r.db, driver: DriverSQLite}
}
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

// newAgentID uses lowercase hexadecimal so generated Agent IDs can be embedded
// directly in case-insensitive DNS labels without changing their identity.
func newAgentID() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("agent-%d", time.Now().UnixNano())
	}
	return "agent-" + hex.EncodeToString(b)
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

// encodeAuditCursor keeps time as the primary pagination key and ID as the
// deterministic tie-breaker. Audit IDs are random, so an ID-only cursor cannot
// preserve the newest-first ordering across pages.
func encodeAuditCursor(createdAt time.Time, id string) string {
	return encodeCursor(tm(createdAt) + "\x00" + id)
}

// decodeAuditCursor accepts the composite cursor and remains compatible with
// cursors issued by older versions that contained only an audit ID.
func (r *auditRepo) decodeAuditCursor(ctx context.Context, cursor string) (string, string, error) {
	decoded := decodeCursor(cursor)
	if decoded == "" {
		return "", "", nil
	}
	if createdAt, id, ok := strings.Cut(decoded, "\x00"); ok {
		return createdAt, id, nil
	}

	var createdAt sql.NullString
	if err := r.db.QueryRowContext(ctx, `SELECT created_at FROM audit_logs WHERE id=?`, decoded).Scan(&createdAt); err != nil {
		return "", "", fmt.Errorf("invalid audit cursor: %w", err)
	}
	if !createdAt.Valid || createdAt.String == "" {
		return "", "", fmt.Errorf("invalid audit cursor: missing created_at")
	}
	return createdAt.String, decoded, nil
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

// withAuthorizationRevision commits an authorization-fact mutation and its
// cache revision as one database transaction. Transaction-bound repositories
// bump the same transaction so callers can safely compose multiple writes.
func withAuthorizationRevision(ctx context.Context, db dbExecutor, operation func(dbExecutor) error) error {
	if tx, ok := db.(*sql.Tx); ok {
		if err := operation(tx); err != nil {
			return err
		}
		return bumpAuthorizationRevision(ctx, tx)
	}
	starter, ok := db.(transactionStarter)
	if !ok {
		return fmt.Errorf("storage: authorization revision requires a transaction-capable executor")
	}
	tx, err := starter.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := operation(tx); err != nil {
		return err
	}
	if err := bumpAuthorizationRevision(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

type userRepo struct {
	db        dbExecutor
	driver    string
	lockReads bool
}

func (r *userRepo) Create(ctx context.Context, v User) error {
	v.ID, v.CreatedAt, v.UpdatedAt = stamp(v.ID, v.CreatedAt, v.UpdatedAt, "user")
	if v.Role == "" {
		v.Role = "user"
	}
	if v.AuthSource == "" {
		v.AuthSource = AuthSourceLocal
	}
	_, err := r.db.ExecContext(ctx, `INSERT INTO users(id,username,role,password_hash,disabled,deleted_at,created_at,updated_at,auth_source,mfa_required) VALUES(?,?,?,?,?,?,?,?,?,?)`, v.ID, v.Username, v.Role, v.PasswordHash, boolInt(v.Disabled), nullableTime(v.DeletedAt), tm(v.CreatedAt), tm(v.UpdatedAt), string(v.AuthSource), boolInt(v.MFARequired))
	return err
}
func (r *userRepo) Get(ctx context.Context, id string) (User, error) {
	var v User
	var created, updated string
	var deleted sql.NullString
	query := `SELECT id,username,role,password_hash,disabled,deleted_at,created_at,updated_at,auth_source,mfa_required FROM users WHERE id=?`
	if r.lockReads && r.driver == DriverMySQL {
		query += ` FOR UPDATE`
	}
	var authSource string
	var mfaRequired int
	err := r.db.QueryRowContext(ctx, query, id).Scan(&v.ID, &v.Username, &v.Role, &v.PasswordHash, &v.Disabled, &deleted, &created, &updated, &authSource, &mfaRequired)
	v.AuthSource = normalizeAuthSource(authSource)
	v.MFARequired = mfaRequired != 0
	v.DeletedAt = parseTM(deleted)
	v.CreatedAt = parseTime(created)
	v.UpdatedAt = parseTime(updated)
	return v, err
}
func (r *userRepo) GetByUsername(ctx context.Context, n string) (User, error) {
	var v User
	var created, updated string
	var deleted sql.NullString
	query := `SELECT id,username,role,password_hash,disabled,deleted_at,created_at,updated_at,auth_source,mfa_required FROM users WHERE username=?`
	if r.lockReads && r.driver == DriverMySQL {
		query += ` FOR UPDATE`
	}
	var authSource string
	var mfaRequired int
	err := r.db.QueryRowContext(ctx, query, n).Scan(&v.ID, &v.Username, &v.Role, &v.PasswordHash, &v.Disabled, &deleted, &created, &updated, &authSource, &mfaRequired)
	v.AuthSource = normalizeAuthSource(authSource)
	v.MFARequired = mfaRequired != 0
	v.DeletedAt = parseTM(deleted)
	v.CreatedAt = parseTime(created)
	v.UpdatedAt = parseTime(updated)
	return v, err
}
func (r *userRepo) Update(ctx context.Context, v User) error {
	if v.UpdatedAt.IsZero() {
		v.UpdatedAt = time.Now().UTC()
	}
	return withAuthorizationRevision(ctx, r.db, func(exec dbExecutor) error {
		res, err := exec.ExecContext(ctx, `UPDATE users SET username=?,role=?,password_hash=?,disabled=?,deleted_at=?,updated_at=? WHERE id=?`, v.Username, v.Role, v.PasswordHash, boolInt(v.Disabled), nullableTime(v.DeletedAt), tm(v.UpdatedAt), v.ID)
		return checkAffected(res, err)
	})
}
func (r *userRepo) Delete(ctx context.Context, id string) error {
	return withAuthorizationRevision(ctx, r.db, func(exec dbExecutor) error {
		res, err := exec.ExecContext(ctx, `DELETE FROM users WHERE id=?`, id)
		return checkAffected(res, err)
	})
}
func (r *userRepo) List(ctx context.Context, cursor string, limit int) (Page[User], error) {
	cursor, limit = pageArgs(cursor, limit)
	q := `SELECT id,username,role,password_hash,disabled,deleted_at,created_at,updated_at,auth_source,mfa_required FROM users`
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
		var deleted sql.NullString
		var authSource string
		var mfaRequired int
		if err := rows.Scan(&v.ID, &v.Username, &v.Role, &v.PasswordHash, &v.Disabled, &deleted, &c, &u, &authSource, &mfaRequired); err != nil {
			return Page[User]{}, err
		}
		v.AuthSource = normalizeAuthSource(authSource)
		v.MFARequired = mfaRequired != 0
		v.DeletedAt = parseTM(deleted)
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

func (r *userRepo) ListChildren(ctx context.Context, status AccountStatus, cursor string, limit int) (Page[User], error) {
	cursor, limit = pageArgs(cursor, limit)
	conditions := []string{`role='user'`}
	switch status {
	case "", AccountStatusActive:
		conditions = append(conditions, `deleted_at IS NULL`)
	case AccountStatusDeleted:
		conditions = append(conditions, `deleted_at IS NOT NULL`)
	case AccountStatusAll:
	default:
		return Page[User]{}, fmt.Errorf("invalid account status %q", status)
	}
	args := make([]any, 0, 2)
	if decoded := decodeCursor(cursor); decoded != "" {
		conditions = append(conditions, `id>?`)
		args = append(args, decoded)
	}
	query := `SELECT id,username,role,password_hash,disabled,deleted_at,created_at,updated_at,auth_source,mfa_required FROM users WHERE ` + strings.Join(conditions, ` AND `) + ` ORDER BY id LIMIT ?`
	args = append(args, limit+1)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return Page[User]{}, err
	}
	defer rows.Close()
	page := Page[User]{}
	for rows.Next() {
		var user User
		var deleted sql.NullString
		var created, updated string
		var authSource string
		var mfaRequired int
		if err := rows.Scan(&user.ID, &user.Username, &user.Role, &user.PasswordHash, &user.Disabled, &deleted, &created, &updated, &authSource, &mfaRequired); err != nil {
			return Page[User]{}, err
		}
		user.AuthSource = normalizeAuthSource(authSource)
		user.MFARequired = mfaRequired != 0
		user.DeletedAt = parseTM(deleted)
		user.CreatedAt = parseTime(created)
		user.UpdatedAt = parseTime(updated)
		page.Items = append(page.Items, user)
	}
	if err := rows.Err(); err != nil {
		return Page[User]{}, err
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.HasMore = true
		page.NextCursor = encodeCursor(page.Items[len(page.Items)-1].ID)
	}
	return page, nil
}

func (r *userRepo) SoftDelete(ctx context.Context, id string, when time.Time) error {
	when = timeOrNow(when)
	res, err := r.db.ExecContext(ctx, `UPDATE users SET deleted_at=CASE WHEN deleted_at IS NULL THEN ? ELSE deleted_at END,disabled=1,updated_at=? WHERE id=?`, tm(when), tm(when), id)
	return checkAffected(res, err)
}

func (r *userRepo) Restore(ctx context.Context, id string) error {
	now := time.Now().UTC()
	res, err := r.db.ExecContext(ctx, `UPDATE users SET deleted_at=NULL,disabled=0,updated_at=? WHERE id=?`, tm(now), id)
	return checkAffected(res, err)
}

// normalizeAuthSource keeps rows written before schema v14 usable: the column
// defaults to 'local', and an unexpected value is treated as local rather than
// escalating an account to an external authentication source.
func normalizeAuthSource(value string) AuthSource {
	switch AuthSource(value) {
	case AuthSourceOIDC:
		return AuthSourceOIDC
	case AuthSourceMixed:
		return AuthSourceMixed
	default:
		return AuthSourceLocal
	}
}

// SetMFARequired flips the per-account second-factor override. It is separate
// from Update so a partial User value can never clear the flag by accident.
func (r *userRepo) SetMFARequired(ctx context.Context, userID string, required bool) error {
	return withAuthorizationRevision(ctx, r.db, func(exec dbExecutor) error {
		res, err := exec.ExecContext(ctx, `UPDATE users SET mfa_required=?,updated_at=? WHERE id=?`, boolInt(required), tm(time.Now().UTC()), userID)
		return checkAffected(res, err)
	})
}

// SetAuthSource records where an account authenticates from. Linking an
// external identity to a local account produces AuthSourceMixed.
func (r *userRepo) SetAuthSource(ctx context.Context, userID string, source AuthSource) error {
	switch source {
	case AuthSourceLocal, AuthSourceOIDC, AuthSourceMixed:
	default:
		return fmt.Errorf("invalid auth source %q", source)
	}
	res, err := r.db.ExecContext(ctx, `UPDATE users SET auth_source=?,updated_at=? WHERE id=?`, string(source), tm(time.Now().UTC()), userID)
	return checkAffected(res, err)
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

const serviceTokenColumns = `id,token_type,owner_user_id,agent_id,node_id,token_prefix,token_hash,secret_ciphertext,secret_nonce,secret_key_id,secret_version,secret_last_read_at,scope,expires_at,revoked_at,last_used_at,created_at,updated_at`

type serviceTokenRepo struct {
	db        dbExecutor
	starter   transactionStarter
	driver    string
	lockReads bool
}

func (r *serviceTokenRepo) Create(ctx context.Context, v ServiceToken) error {
	return withAuthorizationRevision(ctx, r.db, func(exec dbExecutor) error {
		return createServiceToken(ctx, exec, v)
	})
}

func createServiceToken(ctx context.Context, db dbExecutor, v ServiceToken) error {
	v.ID, v.CreatedAt, v.UpdatedAt = stamp(v.ID, v.CreatedAt, v.UpdatedAt, "stok")
	_, err := db.ExecContext(ctx, `INSERT INTO service_tokens(id,token_type,owner_user_id,agent_id,node_id,token_prefix,token_hash,secret_ciphertext,secret_nonce,secret_key_id,secret_version,secret_last_read_at,scope,expires_at,revoked_at,last_used_at,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, v.ID, v.Type, nullableString(v.OwnerUserID), nullableString(v.AgentID), nullableString(v.NodeID), v.Prefix, v.TokenHash, nullableString(v.SecretCiphertext), nullableString(v.SecretNonce), nullableString(v.SecretKeyID), nullableInt(v.SecretVersion), nullableTime(v.SecretLastReadAt), v.Scope, nullableTime(v.ExpiresAt), nullableTime(v.RevokedAt), nullableTime(v.LastUsedAt), tm(v.CreatedAt), tm(v.UpdatedAt))
	return err
}

func (r *serviceTokenRepo) Get(ctx context.Context, id string) (ServiceToken, error) {
	query := `SELECT ` + serviceTokenColumns + ` FROM service_tokens WHERE id=?`
	if r.lockReads && r.driver == DriverMySQL {
		query += ` FOR UPDATE`
	}
	return scanServiceToken(r.db.QueryRowContext(ctx, query, id))
}

func (r *serviceTokenRepo) GetByHash(ctx context.Context, hash string) (ServiceToken, error) {
	query := `SELECT ` + serviceTokenColumns + ` FROM service_tokens WHERE token_hash=?`
	if r.lockReads && r.driver == DriverMySQL {
		query += ` FOR UPDATE`
	}
	return scanServiceToken(r.db.QueryRowContext(ctx, query, hash))
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
	return withAuthorizationRevision(ctx, r.db, func(exec dbExecutor) error {
		res, err := exec.ExecContext(ctx, `UPDATE service_tokens SET revoked_at=?,updated_at=? WHERE id=? AND revoked_at IS NULL`, tm(when), tm(when), id)
		if err != nil {
			return err
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if affected > 0 {
			return nil
		}
		var revoked sql.NullString
		if err := exec.QueryRowContext(ctx, `SELECT revoked_at FROM service_tokens WHERE id=?`, id).Scan(&revoked); err != nil {
			return err
		}
		if parseTM(revoked) != nil {
			return ErrServiceTokenRevoked
		}
		return fmt.Errorf("revoke service token %q made no state transition", id)
	})
}

// UpdateExpiration only changes the lifecycle deadline. Revoked and already
// expired credentials are rejected so an update cannot resurrect them.
func (r *serviceTokenRepo) UpdateExpiration(ctx context.Context, id string, expiresAt *time.Time, when time.Time) error {
	when = timeOrNow(when)
	return withAuthorizationRevision(ctx, r.db, func(exec dbExecutor) error {
		return updateServiceTokenExpiration(ctx, exec, r.driver, id, expiresAt, when)
	})
}

func updateServiceTokenExpiration(ctx context.Context, db dbExecutor, driver, id string, expiresAt *time.Time, when time.Time) error {
	if err := ensureActiveServiceTokenForUpdate(ctx, db, driver, id); err != nil {
		return err
	}
	res, err := db.ExecContext(ctx, `UPDATE service_tokens SET expires_at=?,updated_at=? WHERE id=? AND revoked_at IS NULL`, nullableTime(expiresAt), tm(when), id)
	return checkAffected(res, err)
}

// UpdateScope replaces the stored scope while preserving all other credential
// facts. The service is responsible for merging and validating the new scope.
func (r *serviceTokenRepo) UpdateScope(ctx context.Context, id string, scope string, when time.Time) error {
	when = timeOrNow(when)
	return withAuthorizationRevision(ctx, r.db, func(exec dbExecutor) error {
		return updateServiceTokenScope(ctx, exec, r.driver, id, scope, when)
	})
}

func updateServiceTokenScope(ctx context.Context, db dbExecutor, driver, id, scope string, when time.Time) error {
	if err := ensureActiveServiceTokenForUpdate(ctx, db, driver, id); err != nil {
		return err
	}
	res, err := db.ExecContext(ctx, `UPDATE service_tokens SET scope=?,updated_at=? WHERE id=? AND revoked_at IS NULL`, scope, tm(when), id)
	return checkAffected(res, err)
}

func ensureActiveServiceTokenForUpdate(ctx context.Context, db dbExecutor, driver, id string) error {
	query := `SELECT expires_at,revoked_at FROM service_tokens WHERE id=?`
	if driver == DriverMySQL {
		query += ` FOR UPDATE`
	}
	var expires, revoked sql.NullString
	if err := db.QueryRowContext(ctx, query, id).Scan(&expires, &revoked); err != nil {
		return err
	}
	if parseTM(revoked) != nil {
		return ErrServiceTokenRevoked
	}
	if current := parseTM(expires); current != nil && !current.After(time.Now().UTC()) {
		return ErrServiceTokenExpired
	}
	return nil
}

func (r *serviceTokenRepo) TouchLastUsed(ctx context.Context, id string, when time.Time) error {
	when = timeOrNow(when)
	res, err := r.db.ExecContext(ctx, `UPDATE service_tokens SET last_used_at=?,updated_at=? WHERE id=?`, tm(when), tm(when), id)
	return checkAffected(res, err)
}

func (r *serviceTokenRepo) MarkSecretRead(ctx context.Context, id string, when time.Time) error {
	when = timeOrNow(when)
	res, err := r.db.ExecContext(ctx, `UPDATE service_tokens SET secret_last_read_at=?,updated_at=? WHERE id=?`, tm(when), tm(when), id)
	return checkAffected(res, err)
}

func (r *serviceTokenRepo) Rotate(ctx context.Context, oldID string, replacement ServiceToken, when time.Time) error {
	when = timeOrNow(when)
	return withAuthorizationRevision(ctx, r.db, func(exec dbExecutor) error {
		return rotateServiceToken(ctx, exec, r.driver, oldID, replacement, when)
	})
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
	var ciphertext, nonce, keyID, scope sql.NullString
	var secretVersion sql.NullInt64
	var secretRead, expires, revoked, lastUsed, created, updated sql.NullString
	err := scanner.Scan(&token.ID, &tokenType, &owner, &agent, &node, &token.Prefix, &token.TokenHash, &ciphertext, &nonce, &keyID, &secretVersion, &secretRead, &scope, &expires, &revoked, &lastUsed, &created, &updated)
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
	if ciphertext.Valid {
		token.SecretCiphertext = ciphertext.String
	}
	if nonce.Valid {
		token.SecretNonce = nonce.String
	}
	if keyID.Valid {
		token.SecretKeyID = keyID.String
	}
	if secretVersion.Valid {
		token.SecretVersion = int(secretVersion.Int64)
	}
	token.SecretLastReadAt = parseTM(secretRead)
	token.Scope = scope.String
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

type agentRepo struct {
	db        dbExecutor
	driver    string
	lockReads bool
}

func (r *agentRepo) Create(ctx context.Context, v Agent) error {
	if v.ID == "" {
		v.ID = newAgentID()
	}
	v.ID, v.CreatedAt, v.UpdatedAt = stamp(v.ID, v.CreatedAt, v.UpdatedAt, "agent")
	if v.Capabilities == "" {
		v.Capabilities = "{}"
	}
	return withAuthorizationRevision(ctx, r.db, func(exec dbExecutor) error {
		_, err := exec.ExecContext(ctx, `INSERT INTO agents(id,name,owner_user_id,capabilities,enabled,created_at,updated_at) VALUES(?,?,?,?,?,?,?)`, v.ID, v.Name, v.OwnerUserID, v.Capabilities, boolInt(v.Enabled), tm(v.CreatedAt), tm(v.UpdatedAt))
		return err
	})
}
func (r *agentRepo) Get(ctx context.Context, id string) (Agent, error) {
	var v Agent
	var c, u string
	query := `SELECT id,name,owner_user_id,capabilities,enabled,created_at,updated_at FROM agents WHERE id=?`
	if r.lockReads && r.driver == DriverMySQL {
		query += ` FOR UPDATE`
	}
	err := r.db.QueryRowContext(ctx, query, id).Scan(&v.ID, &v.Name, &v.OwnerUserID, &v.Capabilities, &v.Enabled, &c, &u)
	v.CreatedAt = parseTime(c)
	v.UpdatedAt = parseTime(u)
	return v, err
}
func (r *agentRepo) Update(ctx context.Context, v Agent) error {
	if v.UpdatedAt.IsZero() {
		v.UpdatedAt = time.Now().UTC()
	}
	return withAuthorizationRevision(ctx, r.db, func(exec dbExecutor) error {
		res, err := exec.ExecContext(ctx, `UPDATE agents SET name=?,owner_user_id=?,capabilities=?,enabled=?,updated_at=? WHERE id=?`, v.Name, v.OwnerUserID, v.Capabilities, boolInt(v.Enabled), tm(v.UpdatedAt), v.ID)
		return checkAffected(res, err)
	})
}
func (r *agentRepo) Delete(ctx context.Context, id string) error {
	return withAuthorizationRevision(ctx, r.db, func(exec dbExecutor) error {
		res, err := exec.ExecContext(ctx, `DELETE FROM agents WHERE id=?`, id)
		return checkAffected(res, err)
	})
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
	return withAuthorizationRevision(ctx, r.db, func(exec dbExecutor) error {
		_, err := exec.ExecContext(ctx, `INSERT INTO agent_policies(id,agent_id,target_host,target_port,protocol,allowed_cidrs,allowed_ports,deleted_at,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, v.ID, v.AgentID, v.TargetHost, v.TargetPort, v.Protocol, v.AllowedCIDRs, v.AllowedPorts, nullableTime(v.DeletedAt), tm(v.CreatedAt), tm(v.UpdatedAt))
		return err
	})
}
func (r *policyRepo) Get(ctx context.Context, id string) (AgentPolicy, error) {
	var v AgentPolicy
	var c, u string
	var deleted sql.NullString
	err := r.db.QueryRowContext(ctx, `SELECT id,agent_id,target_host,target_port,protocol,allowed_cidrs,allowed_ports,deleted_at,created_at,updated_at FROM agent_policies WHERE id=?`, id).Scan(&v.ID, &v.AgentID, &v.TargetHost, &v.TargetPort, &v.Protocol, &v.AllowedCIDRs, &v.AllowedPorts, &deleted, &c, &u)
	v.CreatedAt = parseTime(c)
	v.UpdatedAt = parseTime(u)
	v.DeletedAt = parseTM(deleted)
	return v, err
}
func (r *policyRepo) Update(ctx context.Context, v AgentPolicy) error {
	if v.UpdatedAt.IsZero() {
		v.UpdatedAt = time.Now().UTC()
	}
	return withAuthorizationRevision(ctx, r.db, func(exec dbExecutor) error {
		res, err := exec.ExecContext(ctx, `UPDATE agent_policies SET agent_id=?,target_host=?,target_port=?,protocol=?,allowed_cidrs=?,allowed_ports=?,updated_at=? WHERE id=? AND deleted_at IS NULL`, v.AgentID, v.TargetHost, v.TargetPort, v.Protocol, v.AllowedCIDRs, v.AllowedPorts, tm(v.UpdatedAt), v.ID)
		return checkAffected(res, err)
	})
}
func (r *policyRepo) Delete(ctx context.Context, id string) error {
	return withAuthorizationRevision(ctx, r.db, func(exec dbExecutor) error {
		now := tm(time.Now().UTC())
		res, err := exec.ExecContext(ctx, `UPDATE agent_policies SET deleted_at=?,updated_at=? WHERE id=? AND deleted_at IS NULL`, now, now, id)
		return checkAffected(res, err)
	})
}
func (r *policyRepo) Restore(ctx context.Context, id string) error {
	return withAuthorizationRevision(ctx, r.db, func(exec dbExecutor) error {
		res, err := exec.ExecContext(ctx, `UPDATE agent_policies SET deleted_at=NULL,updated_at=? WHERE id=? AND deleted_at IS NOT NULL`, tm(time.Now().UTC()), id)
		return checkAffected(res, err)
	})
}
func (r *policyRepo) ListByAgent(ctx context.Context, agentID, cursor string, limit int) (Page[AgentPolicy], error) {
	return r.listByAgent(ctx, agentID, cursor, limit, PolicyStatusActive)
}
func (r *policyRepo) ListByAgentStatus(ctx context.Context, agentID, cursor string, limit int, status PolicyStatus) (Page[AgentPolicy], error) {
	return r.listByAgent(ctx, agentID, cursor, limit, status)
}
func (r *policyRepo) listByAgent(ctx context.Context, agentID, cursor string, limit int, status PolicyStatus) (Page[AgentPolicy], error) {
	cursor, limit = pageArgs(cursor, limit)
	q := `SELECT id,agent_id,target_host,target_port,protocol,allowed_cidrs,allowed_ports,deleted_at,created_at,updated_at FROM agent_policies WHERE agent_id=?`
	args := []any{agentID}
	switch status {
	case PolicyStatusActive:
		q += ` AND deleted_at IS NULL`
	case PolicyStatusDeleted:
		q += ` AND deleted_at IS NOT NULL`
	case PolicyStatusAll:
	default:
		return Page[AgentPolicy]{}, fmt.Errorf("unsupported policy status %q", status)
	}
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
		var deleted sql.NullString
		if err := rows.Scan(&v.ID, &v.AgentID, &v.TargetHost, &v.TargetPort, &v.Protocol, &v.AllowedCIDRs, &v.AllowedPorts, &deleted, &c, &u); err != nil {
			return Page[AgentPolicy]{}, err
		}
		v.CreatedAt = parseTime(c)
		v.UpdatedAt = parseTime(u)
		v.DeletedAt = parseTM(deleted)
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

var _ PolicyRepository = (*policyRepo)(nil)

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

type nodeRepo struct {
	db        dbExecutor
	driver    string
	lockReads bool
}

func (r *nodeRepo) Create(ctx context.Context, v ServerNode) error {
	v.ID, v.CreatedAt, v.UpdatedAt = stamp(v.ID, v.CreatedAt, v.UpdatedAt, "node")
	if strings.TrimSpace(v.Name) == "" {
		v.Name = v.ID
	}
	v.Enabled = true
	if v.Metadata == "" {
		v.Metadata = "{}"
	}
	_, err := r.db.ExecContext(ctx, `INSERT INTO server_nodes(id,name,address,epoch,metadata,enabled,deleted_at,last_seen_at,expires_at,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, v.ID, v.Name, v.Address, v.Epoch, v.Metadata, boolInt(v.Enabled), nullableTime(v.DeletedAt), nullableTime(v.LastSeenAt), nullableTime(v.ExpiresAt), tm(v.CreatedAt), tm(v.UpdatedAt))
	return err
}

// Ensure creates a self-registered Server node or refreshes runtime fields.
// It deliberately preserves enabled and deleted_at: startup must never undo an
// administrator's lifecycle decision.
func (r *nodeRepo) Ensure(ctx context.Context, v ServerNode) error {
	existing, err := r.Get(ctx, v.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return r.Create(ctx, v)
	}
	if err != nil {
		return err
	}
	// Epoch is owned by lease/relay fencing. A process restart must not reset
	// it merely because the self-registration payload uses a bootstrap value.
	existing.Address, existing.Metadata = v.Address, v.Metadata
	if v.LastSeenAt != nil {
		existing.LastSeenAt = v.LastSeenAt
	}
	if v.ExpiresAt != nil {
		existing.ExpiresAt = v.ExpiresAt
	}
	return r.Update(ctx, existing)
}
func (r *nodeRepo) Get(ctx context.Context, id string) (ServerNode, error) {
	var v ServerNode
	var meta, seen, exp, deleted, created, updated sql.NullString
	query := `SELECT id,name,address,epoch,metadata,enabled,deleted_at,last_seen_at,expires_at,created_at,updated_at FROM server_nodes WHERE id=?`
	if r.lockReads && r.driver == DriverMySQL {
		query += ` FOR UPDATE`
	}
	var enabled int
	err := r.db.QueryRowContext(ctx, query, id).Scan(&v.ID, &v.Name, &v.Address, &v.Epoch, &meta, &enabled, &deleted, &seen, &exp, &created, &updated)
	if err != nil {
		return ServerNode{}, err
	}
	v.Enabled = enabled != 0
	if meta.Valid {
		v.Metadata = meta.String
	}
	v.LastSeenAt = parseTM(seen)
	v.ExpiresAt = parseTM(exp)
	v.DeletedAt = parseTM(deleted)
	v.CreatedAt = parseTime(created.String)
	v.UpdatedAt = parseTime(updated.String)
	return v, nil
}
func (r *nodeRepo) Update(ctx context.Context, v ServerNode) error {
	if strings.TrimSpace(v.Name) == "" {
		v.Name = v.ID
	}
	if v.UpdatedAt.IsZero() {
		v.UpdatedAt = time.Now().UTC()
	}
	res, err := r.db.ExecContext(ctx, `UPDATE server_nodes SET name=?,address=?,epoch=?,metadata=?,enabled=?,deleted_at=?,last_seen_at=?,expires_at=?,updated_at=? WHERE id=?`, v.Name, v.Address, v.Epoch, v.Metadata, boolInt(v.Enabled), nullableTime(v.DeletedAt), nullableTime(v.LastSeenAt), nullableTime(v.ExpiresAt), tm(v.UpdatedAt), v.ID)
	return checkAffected(res, err)
}
func (r *nodeRepo) Delete(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM server_nodes WHERE id=?`, id)
	return checkAffected(res, err)
}
func (r *nodeRepo) List(ctx context.Context, cursor string, limit int) (Page[ServerNode], error) {
	cursor, limit = pageArgs(cursor, limit)
	q := `SELECT id,name,address,epoch,metadata,enabled,deleted_at,last_seen_at,expires_at,created_at,updated_at FROM server_nodes`
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
		var meta, seen, exp, deleted, created, updated sql.NullString
		var enabled int
		if err := rows.Scan(&v.ID, &v.Name, &v.Address, &v.Epoch, &meta, &enabled, &deleted, &seen, &exp, &created, &updated); err != nil {
			return Page[ServerNode]{}, err
		}
		v.Enabled = enabled != 0
		if meta.Valid {
			v.Metadata = meta.String
		}
		v.LastSeenAt = parseTM(seen)
		v.ExpiresAt = parseTM(exp)
		v.DeletedAt = parseTM(deleted)
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

func (r *nodeRepo) Touch(ctx context.Context, id string, lastSeen, expires time.Time) error {
	res, err := r.db.ExecContext(ctx, `UPDATE server_nodes SET last_seen_at=?,expires_at=?,updated_at=? WHERE id=?`, tm(lastSeen), tm(expires), tm(time.Now().UTC()), id)
	return checkAffected(res, err)
}

func (r *nodeRepo) StatsByNodeIDs(ctx context.Context, ids []string) (map[string]ServerNodeStats, error) {
	out := make(map[string]ServerNodeStats, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, 0, len(ids)+1)
	args = append(args, tm(time.Now().UTC()))
	for _, id := range ids {
		args = append(args, id)
	}
	query := `SELECT server_node_id,COUNT(*),COALESCE(SUM(active_streams),0),COALESCE(MIN(health_score),0) FROM agent_connection_leases WHERE expires_at>? AND server_node_id IN (` + placeholders + `) GROUP BY server_node_id`
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var stats ServerNodeStats
		if err := rows.Scan(&stats.NodeID, &stats.ActiveConnections, &stats.ActiveStreams, &stats.HealthScore); err != nil {
			return nil, err
		}
		out[stats.NodeID] = stats
	}
	return out, rows.Err()
}

type agentMetadataRepo struct {
	db     *sql.DB
	driver string
}

type metadataScanner interface{ Scan(dest ...any) error }

func scanAgentMetadata(row metadataScanner) (AgentRuntimeMetadata, error) {
	var v AgentRuntimeMetadata
	var reported, seen, exp, updated sql.NullString
	var stale int
	err := row.Scan(&v.AgentID, &v.InstanceID, &v.NodeID, &v.Epoch, &v.Revision, &v.Metadata, &reported, &seen, &exp, &stale, &updated)
	v.ReportedAt = parseTime(reported.String)
	v.LastSeenAt = parseTime(seen.String)
	v.ExpiresAt = parseTM(exp)
	v.Stale = stale != 0
	v.UpdatedAt = parseTime(updated.String)
	return v, err
}

func (r *agentMetadataRepo) Upsert(ctx context.Context, v AgentRuntimeMetadata) error {
	if v.AgentID == "" || v.NodeID == "" || v.Epoch <= 0 || v.Revision < 0 {
		return errors.New("invalid runtime metadata identity")
	}
	if v.Metadata == "" {
		v.Metadata = "{}"
	}
	if v.InstanceID == "" {
		v.InstanceID = "legacy"
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
	selectSQL := `SELECT epoch,revision FROM agent_instance_metadata WHERE agent_id=? AND instance_id=?`
	if r.driver == DriverMySQL {
		selectSQL += ` FOR UPDATE`
	}
	var epoch, revision int64
	err = tx.QueryRowContext(ctx, selectSQL, v.AgentID, v.InstanceID).Scan(&epoch, &revision)
	if errors.Is(err, sql.ErrNoRows) {
		_, err = tx.ExecContext(ctx, `INSERT INTO agent_instance_metadata(agent_id,instance_id,node_id,epoch,revision,metadata,reported_at,last_seen_at,expires_at,stale,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, v.AgentID, v.InstanceID, v.NodeID, v.Epoch, v.Revision, v.Metadata, tm(v.ReportedAt), tm(v.LastSeenAt), nullableTime(v.ExpiresAt), boolInt(v.Stale), tm(v.UpdatedAt))
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
	_, err = tx.ExecContext(ctx, `UPDATE agent_instance_metadata SET node_id=?,epoch=?,revision=?,metadata=?,reported_at=?,last_seen_at=?,expires_at=?,stale=?,updated_at=? WHERE agent_id=? AND instance_id=?`, v.NodeID, v.Epoch, v.Revision, v.Metadata, tm(v.ReportedAt), tm(v.LastSeenAt), nullableTime(v.ExpiresAt), boolInt(v.Stale), tm(v.UpdatedAt), v.AgentID, v.InstanceID)
	if err != nil {
		return err
	}
	return tx.Commit()
}
func (r *agentMetadataRepo) Get(ctx context.Context, id string) (AgentRuntimeMetadata, error) {
	return scanAgentMetadata(r.db.QueryRowContext(ctx, `SELECT agent_id,instance_id,node_id,epoch,revision,metadata,reported_at,last_seen_at,expires_at,stale,updated_at FROM agent_instance_metadata WHERE agent_id=? ORDER BY last_seen_at DESC LIMIT 1`, id))
}
func (r *agentMetadataRepo) GetInstance(ctx context.Context, agentID, instanceID string) (AgentRuntimeMetadata, error) {
	return scanAgentMetadata(r.db.QueryRowContext(ctx, `SELECT agent_id,instance_id,node_id,epoch,revision,metadata,reported_at,last_seen_at,expires_at,stale,updated_at FROM agent_instance_metadata WHERE agent_id=? AND instance_id=?`, agentID, instanceID))
}
func (r *agentMetadataRepo) List(ctx context.Context, cursor string, limit int) (Page[AgentRuntimeMetadata], error) {
	cursor, limit = pageArgs(cursor, limit)
	q := `SELECT agent_id,instance_id,node_id,epoch,revision,metadata,reported_at,last_seen_at,expires_at,stale,updated_at FROM agent_instance_metadata`
	args := []any{}
	if c := decodeCursor(cursor); c != "" {
		agentID, instanceID := splitMetadataCursor(c)
		q += ` WHERE agent_id>? OR (agent_id=? AND instance_id>?)`
		args = append(args, agentID, agentID, instanceID)
	}
	q += ` ORDER BY agent_id,instance_id LIMIT ?`
	args = append(args, limit+1)
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return Page[AgentRuntimeMetadata]{}, err
	}
	defer rows.Close()
	page := Page[AgentRuntimeMetadata]{}
	for rows.Next() {
		v, err := scanAgentMetadata(rows)
		if err != nil {
			return Page[AgentRuntimeMetadata]{}, err
		}
		page.Items = append(page.Items, v)
	}
	if err := rows.Err(); err != nil {
		return Page[AgentRuntimeMetadata]{}, err
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.HasMore = true
		page.NextCursor = encodeCursor(page.Items[len(page.Items)-1].AgentID + "\x00" + page.Items[len(page.Items)-1].InstanceID)
	}
	return page, nil
}
func (r *agentMetadataRepo) ListInstances(ctx context.Context, agentID string) ([]AgentRuntimeMetadata, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT agent_id,instance_id,node_id,epoch,revision,metadata,reported_at,last_seen_at,expires_at,stale,updated_at FROM agent_instance_metadata WHERE agent_id=? ORDER BY instance_id`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AgentRuntimeMetadata
	for rows.Next() {
		v, err := scanAgentMetadata(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (r *agentMetadataRepo) MarkStale(ctx context.Context, id string, epoch int64) error {
	res, err := r.db.ExecContext(ctx, `UPDATE agent_instance_metadata SET stale=1,updated_at=? WHERE agent_id=? AND epoch<=?`, tm(time.Now().UTC()), id, epoch)
	return checkAffected(res, err)
}
func (r *agentMetadataRepo) MarkInstanceStale(ctx context.Context, agentID, instanceID string, epoch int64) error {
	res, err := r.db.ExecContext(ctx, `UPDATE agent_instance_metadata SET stale=1,updated_at=? WHERE agent_id=? AND instance_id=? AND epoch<=?`, tm(time.Now().UTC()), agentID, instanceID, epoch)
	return checkAffected(res, err)
}
func (r *agentMetadataRepo) Touch(ctx context.Context, id string, epoch int64, lastSeenAt, expiresAt time.Time) error {
	if id == "" || epoch <= 0 || expiresAt.IsZero() {
		return errors.New("invalid runtime metadata lease")
	}
	if lastSeenAt.IsZero() {
		lastSeenAt = time.Now().UTC()
	}
	res, err := r.db.ExecContext(ctx, `UPDATE agent_instance_metadata SET last_seen_at=?,expires_at=?,stale=0,updated_at=? WHERE agent_id=? AND epoch=?`, tm(lastSeenAt), tm(expiresAt), tm(time.Now().UTC()), id, epoch)
	return checkAffected(res, err)
}
func (r *agentMetadataRepo) TouchInstance(ctx context.Context, agentID, instanceID string, epoch int64, lastSeenAt, expiresAt time.Time) error {
	res, err := r.db.ExecContext(ctx, `UPDATE agent_instance_metadata SET last_seen_at=?,expires_at=?,stale=0,updated_at=? WHERE agent_id=? AND instance_id=? AND epoch=?`, tm(lastSeenAt), tm(expiresAt), tm(time.Now().UTC()), agentID, instanceID, epoch)
	return checkAffected(res, err)
}

func splitMetadataCursor(cursor string) (string, string) {
	for i := 0; i < len(cursor); i++ {
		if cursor[i] == 0 {
			return cursor[:i], cursor[i+1:]
		}
	}
	return cursor, ""
}

type agentRuntimeStatsRepo struct{ db *sql.DB }

func (r *agentRuntimeStatsRepo) Append(ctx context.Context, v AgentRuntimeStats) error {
	if r == nil || r.db == nil || v.AgentID == "" || v.NodeID == "" || v.WindowStart.IsZero() {
		return errors.New("invalid runtime stats identity")
	}
	if v.WindowEnd.IsZero() {
		v.WindowEnd = v.WindowStart.Add(time.Minute)
	}
	if v.Epoch <= 0 || v.WindowEnd.Before(v.WindowStart) || v.WindowEnd.Sub(v.WindowStart) > time.Minute || v.Connections < 0 || v.ActiveStreams < 0 || v.BytesIn < 0 || v.BytesOut < 0 || v.HeartbeatTotal < 0 || v.HeartbeatSuccess < 0 || v.HeartbeatRTTP50 < 0 || v.HeartbeatRTTP95 < 0 || v.Reconnects < 0 || v.StreamErrors < 0 {
		return errors.New("invalid bounded runtime stats")
	}
	if v.ID == "" {
		v.ID = newID("runtime-stat")
	}
	if v.CreatedAt.IsZero() {
		v.CreatedAt = time.Now().UTC()
	}
	// INSERT ... SELECT makes the lease fact and history write one database
	// statement, so a takeover cannot race between a process-side check and
	// the insert. The statement is supported by both SQLite and MySQL.
	res, err := r.db.ExecContext(ctx, `INSERT INTO agent_runtime_stats(id,agent_id,node_id,epoch,window_start,window_end,connections,active_streams,bytes_in,bytes_out,heartbeat_total,heartbeat_success,heartbeat_rtt_p50_us,heartbeat_rtt_p95_us,reconnects,stream_errors,created_at) SELECT ?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,? FROM agent_connection_leases WHERE agent_id=? AND node_id=? AND epoch=? LIMIT 1`, v.ID, v.AgentID, v.NodeID, v.Epoch, tm(v.WindowStart), tm(v.WindowEnd), v.Connections, v.ActiveStreams, v.BytesIn, v.BytesOut, v.HeartbeatTotal, v.HeartbeatSuccess, v.HeartbeatRTTP50.Microseconds(), v.HeartbeatRTTP95.Microseconds(), v.Reconnects, v.StreamErrors, tm(v.CreatedAt), v.AgentID, v.NodeID, v.Epoch)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrRuntimeStatsStaleEpoch
	}
	return nil
}

func (r *agentRuntimeStatsRepo) ListRange(ctx context.Context, agentID string, from, to time.Time, cursor string, limit int) (Page[AgentRuntimeStats], error) {
	cursor, limit = pageArgs(cursor, limit)
	args := []any{agentID, tm(from), tm(to)}
	q := `SELECT id,agent_id,node_id,epoch,window_start,window_end,connections,active_streams,bytes_in,bytes_out,heartbeat_total,heartbeat_success,heartbeat_rtt_p50_us,heartbeat_rtt_p95_us,reconnects,stream_errors,created_at FROM agent_runtime_stats WHERE agent_id=? AND window_start>=? AND window_start<?`
	if c := decodeCursor(cursor); c != "" {
		var start string
		var cursorAgent string
		if err := r.db.QueryRowContext(ctx, `SELECT agent_id,window_start FROM agent_runtime_stats WHERE id=?`, c).Scan(&cursorAgent, &start); err != nil {
			return Page[AgentRuntimeStats]{}, err
		}
		if cursorAgent != agentID || start < tm(from) || start >= tm(to) {
			return Page[AgentRuntimeStats]{}, errors.New("invalid runtime stats cursor")
		}
		q += ` AND (window_start>? OR (window_start=? AND id>?))`
		args = append(args, start, start, c)
	}
	q += ` ORDER BY window_start,id LIMIT ?`
	args = append(args, limit+1)
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return Page[AgentRuntimeStats]{}, err
	}
	defer rows.Close()
	page := Page[AgentRuntimeStats]{}
	for rows.Next() {
		var v AgentRuntimeStats
		var start, end, created string
		var p50, p95 int64
		if err := rows.Scan(&v.ID, &v.AgentID, &v.NodeID, &v.Epoch, &start, &end, &v.Connections, &v.ActiveStreams, &v.BytesIn, &v.BytesOut, &v.HeartbeatTotal, &v.HeartbeatSuccess, &p50, &p95, &v.Reconnects, &v.StreamErrors, &created); err != nil {
			return Page[AgentRuntimeStats]{}, err
		}
		v.WindowStart, v.WindowEnd, v.CreatedAt = parseTime(start), parseTime(end), parseTime(created)
		v.HeartbeatRTTP50, v.HeartbeatRTTP95 = time.Duration(p50)*time.Microsecond, time.Duration(p95)*time.Microsecond
		page.Items = append(page.Items, v)
	}
	if err := rows.Err(); err != nil {
		return Page[AgentRuntimeStats]{}, err
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.HasMore = true
		page.NextCursor = encodeCursor(page.Items[len(page.Items)-1].ID)
	}
	return page, nil
}

func (r *agentRuntimeStatsRepo) DeleteBefore(ctx context.Context, agentID string, before time.Time) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM agent_runtime_stats WHERE agent_id=? AND window_start<?`, agentID, tm(before))
	return err
}

var allowedProbeErrorClasses = map[string]struct{}{
	"": {}, "none": {}, "timeout": {}, "dns_timeout": {}, "connect_timeout": {}, "read_timeout": {},
	"connection_refused": {}, "policy_denied": {}, "epoch_stale": {}, "unsupported": {}, "cancelled": {},
	"internal": {}, "http_status": {}, "invalid_response": {},
}

type agentProbeResultRepo struct{ db *sql.DB }

func (r *agentProbeResultRepo) Create(ctx context.Context, v AgentProbeResult) error {
	if r == nil || r.db == nil || v.ProbeID == "" || v.AgentID == "" || v.Kind == "" || v.Result == "" {
		return errors.New("invalid probe result")
	}
	if _, ok := allowedProbeErrorClasses[v.ErrorClass]; !ok {
		return errors.New("probe error class is not bounded")
	}
	if v.NodeID == "" || v.Epoch <= 0 || v.Duration < 0 || len(v.ErrorClass) > 64 || len(v.Kind) > 32 || len(v.Result) > 32 {
		return errors.New("probe result field too long")
	}
	if v.ObservedAt.IsZero() {
		v.ObservedAt = time.Now().UTC()
	}
	res, err := r.db.ExecContext(ctx, `INSERT INTO agent_probe_results(probe_id,agent_id,node_id,epoch,kind,result,error_class,duration_us,observed_at) SELECT ?,?,?,?,?,?,?,?,? FROM agent_connection_leases WHERE agent_id=? AND node_id=? AND epoch=? LIMIT 1`, v.ProbeID, v.AgentID, v.NodeID, v.Epoch, v.Kind, v.Result, v.ErrorClass, v.Duration.Microseconds(), tm(v.ObservedAt), v.AgentID, v.NodeID, v.Epoch)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrRuntimeStatsStaleEpoch
	}
	return nil
}

func (r *agentProbeResultRepo) ListByAgent(ctx context.Context, agentID, cursor string, limit int) (Page[AgentProbeResult], error) {
	cursor, limit = pageArgs(cursor, limit)
	args := []any{agentID}
	q := `SELECT probe_id,agent_id,node_id,epoch,kind,result,error_class,duration_us,observed_at FROM agent_probe_results WHERE agent_id=?`
	if c := decodeCursor(cursor); c != "" {
		var observed string
		var cursorAgent string
		if err := r.db.QueryRowContext(ctx, `SELECT agent_id,observed_at FROM agent_probe_results WHERE probe_id=?`, c).Scan(&cursorAgent, &observed); err != nil {
			return Page[AgentProbeResult]{}, err
		}
		if cursorAgent != agentID {
			return Page[AgentProbeResult]{}, errors.New("invalid probe cursor")
		}
		q += ` AND (observed_at>? OR (observed_at=? AND probe_id>?))`
		args = append(args, observed, observed, c)
	}
	q += ` ORDER BY observed_at,probe_id LIMIT ?`
	args = append(args, limit+1)
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return Page[AgentProbeResult]{}, err
	}
	defer rows.Close()
	page := Page[AgentProbeResult]{}
	for rows.Next() {
		var v AgentProbeResult
		var duration int64
		var observed string
		if err := rows.Scan(&v.ProbeID, &v.AgentID, &v.NodeID, &v.Epoch, &v.Kind, &v.Result, &v.ErrorClass, &duration, &observed); err != nil {
			return Page[AgentProbeResult]{}, err
		}
		v.Duration, v.ObservedAt = time.Duration(duration)*time.Microsecond, parseTime(observed)
		page.Items = append(page.Items, v)
	}
	if err := rows.Err(); err != nil {
		return Page[AgentProbeResult]{}, err
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.HasMore = true
		page.NextCursor = encodeCursor(page.Items[len(page.Items)-1].ProbeID)
	}
	return page, nil
}

func (r *agentProbeResultRepo) DeleteBefore(ctx context.Context, before time.Time) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM agent_probe_results WHERE observed_at<?`, tm(before))
	return err
}

type leaseRepo struct {
	db     *sql.DB
	driver string
}

// Acquire preserves the legacy one-connection lease API by mapping it to the
// deterministic "legacy" connection. New code must use RegisterConnection.
func (r *leaseRepo) Acquire(ctx context.Context, v AgentLease) (AgentLease, error) {
	v.ConnectionID = "legacy"
	v.ConnectionEpoch = 0
	return r.RegisterConnection(ctx, v)
}

func (r *leaseRepo) RegisterConnection(ctx context.Context, v AgentLease) (AgentLease, error) {
	if r == nil || r.db == nil || v.AgentID == "" || v.NodeID == "" {
		return AgentLease{}, errors.New("invalid agent connection lease")
	}
	if v.ConnectionID == "" {
		v.ConnectionID = "legacy"
	}
	if v.ServerNodeID == "" {
		v.ServerNodeID = v.NodeID
	}
	if v.HealthScore == 0 {
		v.HealthScore = 100
	}
	if v.TTL <= 0 {
		v.TTL = time.Minute
	}
	// MySQL locks the current connection row while deciding replacement. The
	// compare-and-set keeps SQLite portable and prevents two owners publishing
	// the same connection epoch.
	for attempt := 0; attempt < 8; attempt++ {
		now := time.Now().UTC()
		exp := now.Add(v.TTL)
		autoEpoch := v.ConnectionEpoch <= 0
		tx, err := r.db.BeginTx(ctx, nil)
		if err != nil {
			return AgentLease{}, err
		}
		var oldEpoch int64
		var oldExpires string
		selectSQL := `SELECT connection_epoch,expires_at FROM agent_connection_leases WHERE agent_id=? AND connection_id=?`
		if r.driver == DriverMySQL {
			selectSQL += ` FOR UPDATE`
		}
		queryErr := tx.QueryRowContext(ctx, selectSQL, v.AgentID, v.ConnectionID).Scan(&oldEpoch, &oldExpires)
		if queryErr != nil && !errors.Is(queryErr, sql.ErrNoRows) {
			_ = tx.Rollback()
			return AgentLease{}, queryErr
		}
		if queryErr == nil {
			if autoEpoch && parseTime(oldExpires).After(now) {
				_ = tx.Rollback()
				return AgentLease{}, fmt.Errorf("agent %s connection %s lease is held by another owner", v.AgentID, v.ConnectionID)
			}
			if v.ConnectionEpoch <= 0 {
				v.ConnectionEpoch = oldEpoch + 1
			}
			if v.Epoch <= 0 {
				v.Epoch = v.ConnectionEpoch
			}
			if oldEpoch >= v.ConnectionEpoch {
				_ = tx.Rollback()
				return AgentLease{}, ErrStaleEpoch
			}
			updateSQL := `UPDATE agent_connection_leases SET node_id=?,instance_id=?,server_node_id=?,epoch=?,connection_epoch=?,active_streams=?,health_score=?,acquired_at=?,expires_at=?,updated_at=? WHERE agent_id=? AND connection_id=? AND connection_epoch=?`
			updateArgs := []any{v.NodeID, v.InstanceID, v.ServerNodeID, v.Epoch, v.ConnectionEpoch, v.ActiveStreams, v.HealthScore, tm(now), tm(exp), tm(now), v.AgentID, v.ConnectionID, oldEpoch}
			if autoEpoch {
				// Legacy takeover is only allowed after expiry. Explicit epochs
				// may replace an active connection to fence a stale process.
				updateSQL += ` AND expires_at<=?`
				updateArgs = append(updateArgs, tm(now))
			}
			res, updateErr := tx.ExecContext(ctx, updateSQL, updateArgs...)
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
			v.AcquiredAt, v.ExpiresAt, v.UpdatedAt = now, exp, now
			return v, nil
		}

		if v.ConnectionEpoch <= 0 {
			v.ConnectionEpoch = 1
		}
		if v.Epoch <= 0 {
			v.Epoch = v.ConnectionEpoch
		}
		_, insertErr := tx.ExecContext(ctx, `INSERT INTO agent_connection_leases(agent_id,connection_id,node_id,instance_id,server_node_id,epoch,connection_epoch,active_streams,health_score,acquired_at,expires_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, v.AgentID, v.ConnectionID, v.NodeID, v.InstanceID, v.ServerNodeID, v.Epoch, v.ConnectionEpoch, v.ActiveStreams, v.HealthScore, tm(now), tm(exp), tm(now))
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
		v.AcquiredAt, v.ExpiresAt, v.UpdatedAt = now, exp, now
		return v, nil
	}
	return AgentLease{}, errors.New("connection lease registration contention exceeded retry budget")
}

func isDuplicateError(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "unique constraint") || strings.Contains(s, "duplicate") || strings.Contains(s, "already exists")
}
func (r *leaseRepo) Renew(ctx context.Context, agentID string, epoch int64, ttl time.Duration) error {
	return r.RenewConnection(ctx, agentID, "legacy", epoch, ttl)
}
func (r *leaseRepo) RenewConnection(ctx context.Context, agentID, connectionID string, epoch int64, ttl time.Duration) error {
	if ttl <= 0 {
		ttl = time.Minute
	}
	now := time.Now().UTC()
	res, err := r.db.ExecContext(ctx, `UPDATE agent_connection_leases SET expires_at=?,updated_at=? WHERE agent_id=? AND connection_id=? AND connection_epoch=? AND expires_at>?`, tm(now.Add(ttl)), tm(now), agentID, connectionID, epoch, tm(now))
	return checkAffected(res, err)
}
func (r *leaseRepo) Release(ctx context.Context, agentID string, epoch int64) error {
	return r.ReleaseConnection(ctx, agentID, "legacy", epoch)
}
func (r *leaseRepo) ReleaseConnection(ctx context.Context, agentID, connectionID string, epoch int64) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM agent_connection_leases WHERE agent_id=? AND connection_id=? AND connection_epoch=?`, agentID, connectionID, epoch)
	return checkAffected(res, err)
}
func (r *leaseRepo) Get(ctx context.Context, agentID string) (AgentLease, error) {
	var v AgentLease
	var acquired, exp, updated string
	err := r.db.QueryRowContext(ctx, `SELECT agent_id,connection_id,node_id,instance_id,server_node_id,epoch,connection_epoch,active_streams,health_score,acquired_at,expires_at,updated_at FROM agent_connection_leases WHERE agent_id=? ORDER BY connection_id`, agentID).Scan(&v.AgentID, &v.ConnectionID, &v.NodeID, &v.InstanceID, &v.ServerNodeID, &v.Epoch, &v.ConnectionEpoch, &v.ActiveStreams, &v.HealthScore, &acquired, &exp, &updated)
	if err != nil {
		return AgentLease{}, err
	}
	v.AcquiredAt, v.ExpiresAt, v.UpdatedAt = parseTime(acquired), parseTime(exp), parseTime(updated)
	if !v.ExpiresAt.IsZero() {
		v.TTL = time.Until(v.ExpiresAt)
	}
	return v, nil
}
func (r *leaseRepo) ListActiveByAgent(ctx context.Context, agentID string) ([]AgentLease, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT agent_id,connection_id,node_id,instance_id,server_node_id,epoch,connection_epoch,active_streams,health_score,acquired_at,expires_at,updated_at FROM agent_connection_leases WHERE agent_id=? AND expires_at>? ORDER BY connection_id`, agentID, tm(time.Now().UTC()))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var leases []AgentLease
	for rows.Next() {
		var v AgentLease
		var acquired, exp, updated string
		if err := rows.Scan(&v.AgentID, &v.ConnectionID, &v.NodeID, &v.InstanceID, &v.ServerNodeID, &v.Epoch, &v.ConnectionEpoch, &v.ActiveStreams, &v.HealthScore, &acquired, &exp, &updated); err != nil {
			return nil, err
		}
		v.AcquiredAt, v.ExpiresAt, v.UpdatedAt = parseTime(acquired), parseTime(exp), parseTime(updated)
		if !v.ExpiresAt.IsZero() {
			v.TTL = time.Until(v.ExpiresAt)
		}
		leases = append(leases, v)
	}
	return leases, rows.Err()
}
func (r *leaseRepo) UpdateConnectionStats(ctx context.Context, v AgentLease) error {
	now := time.Now().UTC()
	res, err := r.db.ExecContext(ctx, `UPDATE agent_connection_leases SET active_streams=?,health_score=?,updated_at=? WHERE agent_id=? AND connection_id=? AND connection_epoch=? AND expires_at>?`, v.ActiveStreams, v.HealthScore, tm(now), v.AgentID, v.ConnectionID, v.ConnectionEpoch, tm(now))
	return checkAffected(res, err)
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
func (r *auditRepo) List(ctx context.Context, filter AuditFilter, cursor string, limit int) (Page[AuditLog], error) {
	cursor, limit = pageArgs(cursor, limit)
	q := `SELECT id,actor_user_id,action,resource_type,resource_id,details,created_at FROM audit_logs`
	args := []any{}
	conditions := []string{}
	if filter.ActorUserID != "" {
		conditions = append(conditions, "actor_user_id=?")
		args = append(args, filter.ActorUserID)
	}
	if filter.Action != "" {
		conditions = append(conditions, "action=?")
		args = append(args, filter.Action)
	}
	if filter.ResourceType != "" {
		conditions = append(conditions, "resource_type=?")
		args = append(args, filter.ResourceType)
	}
	if filter.ResourceID != "" {
		conditions = append(conditions, "resource_id=?")
		args = append(args, filter.ResourceID)
	}
	if filter.CreatedFrom != nil {
		conditions = append(conditions, "created_at>=?")
		args = append(args, tm(*filter.CreatedFrom))
	}
	if filter.CreatedTo != nil {
		conditions = append(conditions, "created_at<=?")
		args = append(args, tm(*filter.CreatedTo))
	}
	cursorCreatedAt, cursorID, err := r.decodeAuditCursor(ctx, cursor)
	if err != nil {
		return Page[AuditLog]{}, err
	}
	if cursorID != "" {
		conditions = append(conditions, `(created_at<? OR (created_at=? AND id<?))`)
		args = append(args, cursorCreatedAt, cursorCreatedAt, cursorID)
	}
	if len(conditions) > 0 {
		q += ` WHERE ` + strings.Join(conditions, ` AND `)
	}
	q += ` ORDER BY created_at DESC,id DESC LIMIT ?`
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
		last := p.Items[len(p.Items)-1]
		p.NextCursor = encodeAuditCursor(last.CreatedAt, last.ID)
	}
	return p, nil
}

type idempotencyRepo struct{ db dbExecutor }

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
