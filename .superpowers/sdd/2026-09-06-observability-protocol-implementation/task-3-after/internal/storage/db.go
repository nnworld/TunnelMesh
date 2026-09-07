package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	_ "github.com/go-sql-driver/mysql"
	_ "modernc.org/sqlite"

	"github.com/tunnelmesh/tunnelmesh/internal/config"
	"github.com/tunnelmesh/tunnelmesh/internal/observability"
	"github.com/tunnelmesh/tunnelmesh/migrations"
)

const (
	DriverSQLite  = "sqlite"
	DriverMySQL   = "mysql"
	SchemaVersion = 3
)

var ErrSchemaVersionMismatch = errors.New("schema version mismatch")

type DB struct {
	sql           *sql.DB
	driver        string
	users         UserRepository
	tokens        TokenRepository
	serviceTokens ServiceTokenRepository
	agents        AgentRepository
	policies      PolicyRepository
	tunnels       TunnelRepository
	nodes         NodeRepository
	metadata      AgentMetadataRepository
	leases        LeaseRepository
	audits        AuditRepository
	idempotency   IdempotencyRepository
	metrics       *observability.Metrics
}

// ServiceTokenMutationRepositories groups every repository that participates
// in an authorized service-token mutation. Each repository is bound to the
// same transaction so authorization facts and the credential write cannot
// observe different database states.
type ServiceTokenMutationRepositories struct {
	Tokens      ServiceTokenRepository
	Audits      AuditRepository
	Idempotency IdempotencyRepository
	Users       UserRepository
	Agents      AgentRepository
	Nodes       NodeRepository
}

// ServiceTokenTransaction atomically applies service-token lifecycle changes
// and their audit record using repositories bound to the same transaction.
// It is retained for Task 2 callers; mutation paths that also persist replay
// metadata should use ServiceTokenMutationTransaction.
func (d *DB) ServiceTokenTransaction(ctx context.Context, fn func(ServiceTokenRepository, AuditRepository) error) error {
	return d.ServiceTokenMutationTransaction(ctx, func(tokens ServiceTokenRepository, audits AuditRepository, _ IdempotencyRepository) error {
		return fn(tokens, audits)
	})
}

// ServiceTokenMutationTransaction commits a service-token mutation, its audit
// event, and non-secret idempotency metadata as one database fact.
func (d *DB) ServiceTokenMutationTransaction(ctx context.Context, fn func(ServiceTokenRepository, AuditRepository, IdempotencyRepository) error) error {
	return d.ServiceTokenAuthorizedMutationTransaction(ctx, func(repos ServiceTokenMutationRepositories) error {
		return fn(repos.Tokens, repos.Audits, repos.Idempotency)
	})
}

// ServiceTokenAuthorizedMutationTransaction commits authorization reads,
// service-token lifecycle state, audit events, and replay metadata as one
// database fact. SQLite takes a writer lock before any authorization read;
// MySQL repositories use locking reads for mutable resource facts.
func (d *DB) ServiceTokenAuthorizedMutationTransaction(ctx context.Context, fn func(ServiceTokenMutationRepositories) error) error {
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if d.driver == DriverSQLite {
		if _, err := tx.ExecContext(ctx, `UPDATE schema_meta SET version=version WHERE id=1`); err != nil {
			return err
		}
	}
	repos := ServiceTokenMutationRepositories{
		Tokens:      &serviceTokenRepo{db: tx, driver: d.driver, lockReads: true},
		Audits:      &auditRepo{db: tx},
		Idempotency: &idempotencyRepo{db: tx},
		Users:       &userRepo{db: tx, driver: d.driver, lockReads: true},
		Agents:      &agentRepo{db: tx, driver: d.driver, lockReads: true},
		Nodes:       &nodeRepo{db: tx, driver: d.driver, lockReads: true},
	}
	if err := fn(repos); err != nil {
		return err
	}
	return tx.Commit()
}

// AuthTransaction executes user/token/audit changes in one database
// transaction. The schema_meta no-op update acquires a portable writer lock,
// fencing concurrent bootstrap or recovery operations across processes.
func (d *DB) AuthTransaction(ctx context.Context, fn func(UserRepository, TokenRepository, AuditRepository) error) error {
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE schema_meta SET version=version WHERE id=1`); err != nil {
		_ = tx.Rollback()
		return err
	}
	users := &userRepo{db: tx, driver: d.driver, lockReads: true}
	tokens := &tokenRepo{db: tx}
	audits := &auditRepo{db: tx}
	if err := fn(users, tokens, audits); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func Open(ctx context.Context, driver, dsn string, autoInit bool) (*DB, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	driver = strings.ToLower(strings.TrimSpace(driver))
	if driver == "sqlite3" {
		driver = DriverSQLite
	}
	sqlDriver := driver
	if driver == DriverSQLite {
		sqlDriver = "sqlite"
	}
	if driver != DriverSQLite && driver != DriverMySQL {
		return nil, fmt.Errorf("unsupported storage driver %q", driver)
	}
	if dsn == "" {
		if driver == DriverSQLite {
			dsn = "tunnelmesh.db"
		} else {
			return nil, errors.New("mysql DSN is required")
		}
	}
	db, err := sql.Open(sqlDriver, dsn)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", driver, err)
	}
	if driver == DriverSQLite {
		db.SetMaxOpenConns(1)
	}
	if err = db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping %s: %w", driver, err)
	}
	if autoInit {
		if err = initializeSchema(ctx, db, driver); err != nil {
			db.Close()
			return nil, err
		}
	} else if err = checkSchema(ctx, db, driver); err != nil {
		db.Close()
		return nil, err
	}
	return newDB(db, driver), nil
}

// OpenWithMetrics attaches bounded storage instrumentation without changing
// the storage ownership or transaction behavior of Open.
func OpenWithMetrics(ctx context.Context, driver, dsn string, autoInit bool, metrics *observability.Metrics) (*DB, error) {
	db, err := Open(ctx, driver, dsn, autoInit)
	if err != nil {
		return nil, err
	}
	db.metrics = metrics
	return db, nil
}

// OpenConfig consumes the already-validated storage settings produced by
// internal/config and keeps driver-specific DSN selection inside storage.
func OpenConfig(ctx context.Context, cfg config.StorageConfig) (*DB, error) {
	dsn := cfg.SQLite.Path
	if cfg.Driver == DriverMySQL {
		dsn = cfg.MySQL.DSN
	}
	return Open(ctx, cfg.Driver, dsn, cfg.AutoInit)
}

func newDB(db *sql.DB, driver string) *DB {
	return &DB{
		sql:           db,
		driver:        driver,
		users:         &userRepo{db: db, driver: driver},
		tokens:        &tokenRepo{db: db},
		serviceTokens: NewServiceTokenRepositoryWithDriver(db, driver),
		agents:        &agentRepo{db: db, driver: driver},
		policies:      &policyRepo{db},
		tunnels:       &tunnelRepo{db},
		nodes:         &nodeRepo{db: db, driver: driver},
		metadata:      NewAgentMetadataRepositoryWithDriver(db, driver),
		leases:        NewLeaseRepositoryWithDriver(db, driver),
		audits:        &auditRepo{db},
		idempotency:   &idempotencyRepo{db},
	}
}

func initializeSchema(ctx context.Context, db *sql.DB, driver string) error {
	for _, stmt := range strings.Split(migrations.DDL, ";") {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			if isDuplicateError(err) {
				continue
			}
			return fmt.Errorf("apply schema: %w", err)
		}
	}
	var version int
	err := db.QueryRowContext(ctx, `SELECT version FROM schema_meta WHERE id=1`).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err = db.ExecContext(ctx, `INSERT INTO schema_meta(id,version) VALUES(1,?)`, SchemaVersion); err != nil {
			return fmt.Errorf("write schema version: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if version > SchemaVersion || version < 1 {
		return fmt.Errorf("%w: database has version %d, application requires version %d; run the migration tool", ErrSchemaVersionMismatch, version, SchemaVersion)
	}
	if version < SchemaVersion {
		if _, err := db.ExecContext(ctx, `UPDATE schema_meta SET version=? WHERE id=1`, SchemaVersion); err != nil {
			return fmt.Errorf("migrate schema version: %w", err)
		}
	}
	if err := requireSchemaTables(ctx, db, driver); err != nil {
		return err
	}
	return nil
}
func checkSchema(ctx context.Context, db *sql.DB, driver string) error {
	var v int
	err := db.QueryRowContext(ctx, `SELECT version FROM schema_meta WHERE id=1`).Scan(&v)
	if err != nil {
		return fmt.Errorf("schema is not initialized: %w", err)
	}
	if v != SchemaVersion {
		return fmt.Errorf("%w: database has version %d, application requires version %d; run the migration tool", ErrSchemaVersionMismatch, v, SchemaVersion)
	}
	return requireSchemaTables(ctx, db, driver)
}

func requireSchemaTables(ctx context.Context, db *sql.DB, driver string) error {
	for _, table := range []string{"schema_meta", "users", "agents", "agent_runtime_metadata", "service_tokens"} {
		var found string
		var err error
		if driver == DriverMySQL {
			err = db.QueryRowContext(ctx, `SELECT table_name FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name=?`, table).Scan(&found)
		} else {
			err = db.QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&found)
		}
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("schema is missing required table %s", table)
		}
		if err != nil {
			return fmt.Errorf("check required table %s: %w", table, err)
		}
	}
	return nil
}

func (d *DB) Close() error {
	if d == nil || d.sql == nil {
		return nil
	}
	return d.sql.Close()
}
func (d *DB) Ping(ctx context.Context) error {
	err := d.sql.PingContext(ctx)
	if d.metrics != nil {
		result, class := "success", ""
		if err != nil {
			result, class = "failure", observability.NormalizeErrorClass(err)
		}
		d.metrics.ObserveStage("storage", "ping", result, class, 0)
		d.metrics.ObserveProbe("storage", result, class, 0)
	}
	return err
}
func (d *DB) SQL() *sql.DB   { return d.sql }
func (d *DB) Driver() string { return d.driver }
func (d *DB) SchemaVersion(ctx context.Context) (int, error) {
	var v int
	err := d.sql.QueryRowContext(ctx, `SELECT version FROM schema_meta WHERE id=1`).Scan(&v)
	return v, err
}
func (d *DB) Users() UserRepository                  { return d.users }
func (d *DB) Tokens() TokenRepository                { return d.tokens }
func (d *DB) ServiceTokens() ServiceTokenRepository  { return d.serviceTokens }
func (d *DB) Agents() AgentRepository                { return d.agents }
func (d *DB) Policies() PolicyRepository             { return d.policies }
func (d *DB) Tunnels() TunnelRepository              { return d.tunnels }
func (d *DB) Nodes() NodeRepository                  { return d.nodes }
func (d *DB) Metadata() AgentMetadataRepository      { return d.metadata }
func (d *DB) AgentMetadata() AgentMetadataRepository { return d.metadata }
func (d *DB) Leases() LeaseRepository                { return d.leases }
func (d *DB) Audits() AuditRepository                { return d.audits }
func (d *DB) Idempotency() IdempotencyRepository     { return d.idempotency }
