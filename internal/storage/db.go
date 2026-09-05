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
	"github.com/tunnelmesh/tunnelmesh/migrations"
)

const (
	DriverSQLite  = "sqlite"
	DriverMySQL   = "mysql"
	SchemaVersion = 1
)

var ErrSchemaVersionMismatch = errors.New("schema version mismatch")

type DB struct {
	sql         *sql.DB
	driver      string
	users       UserRepository
	tokens      TokenRepository
	agents      AgentRepository
	policies    PolicyRepository
	tunnels     TunnelRepository
	nodes       NodeRepository
	leases      LeaseRepository
	audits      AuditRepository
	idempotency IdempotencyRepository
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
	users := &userRepo{db: tx}
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
		if err = initializeSchema(ctx, db); err != nil {
			db.Close()
			return nil, err
		}
	} else if err = checkSchema(ctx, db); err != nil {
		db.Close()
		return nil, err
	}
	return newDB(db, driver), nil
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
	return &DB{sql: db, driver: driver, users: &userRepo{db}, tokens: &tokenRepo{db}, agents: &agentRepo{db}, policies: &policyRepo{db}, tunnels: &tunnelRepo{db}, nodes: &nodeRepo{db}, leases: NewLeaseRepositoryWithDriver(db, driver), audits: &auditRepo{db}, idempotency: &idempotencyRepo{db}}
}

func initializeSchema(ctx context.Context, db *sql.DB) error {
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
	if version != SchemaVersion {
		return fmt.Errorf("%w: database has version %d, application requires version %d; run the migration tool", ErrSchemaVersionMismatch, version, SchemaVersion)
	}
	return nil
}
func checkSchema(ctx context.Context, db *sql.DB) error {
	var v int
	err := db.QueryRowContext(ctx, `SELECT version FROM schema_meta WHERE id=1`).Scan(&v)
	if err != nil {
		return fmt.Errorf("schema is not initialized: %w", err)
	}
	if v != SchemaVersion {
		return fmt.Errorf("%w: database has version %d, application requires version %d; run the migration tool", ErrSchemaVersionMismatch, v, SchemaVersion)
	}
	return nil
}

func (d *DB) Close() error {
	if d == nil || d.sql == nil {
		return nil
	}
	return d.sql.Close()
}
func (d *DB) Ping(ctx context.Context) error { return d.sql.PingContext(ctx) }
func (d *DB) SQL() *sql.DB                   { return d.sql }
func (d *DB) Driver() string                 { return d.driver }
func (d *DB) SchemaVersion(ctx context.Context) (int, error) {
	var v int
	err := d.sql.QueryRowContext(ctx, `SELECT version FROM schema_meta WHERE id=1`).Scan(&v)
	return v, err
}
func (d *DB) Users() UserRepository              { return d.users }
func (d *DB) Tokens() TokenRepository            { return d.tokens }
func (d *DB) Agents() AgentRepository            { return d.agents }
func (d *DB) Policies() PolicyRepository         { return d.policies }
func (d *DB) Tunnels() TunnelRepository          { return d.tunnels }
func (d *DB) Nodes() NodeRepository              { return d.nodes }
func (d *DB) Leases() LeaseRepository            { return d.leases }
func (d *DB) Audits() AuditRepository            { return d.audits }
func (d *DB) Idempotency() IdempotencyRepository { return d.idempotency }
