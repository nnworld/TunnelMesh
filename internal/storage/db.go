package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
	_ "modernc.org/sqlite"

	"github.com/tunnelmesh/tunnelmesh/internal/config"
	"github.com/tunnelmesh/tunnelmesh/internal/observability"
	"github.com/tunnelmesh/tunnelmesh/migrations"
)

const (
	DriverSQLite  = "sqlite"
	DriverMySQL   = "mysql"
	SchemaVersion = 11
)

var ErrSchemaVersionMismatch = errors.New("schema version mismatch")

type DB struct {
	sql                    *sql.DB
	driver                 string
	users                  UserRepository
	tokens                 TokenRepository
	serviceTokens          ServiceTokenRepository
	agents                 AgentRepository
	policies               PolicyRepository
	tunnels                TunnelRepository
	nodes                  NodeRepository
	metadata               AgentMetadataRepository
	runtimeStats           AgentRuntimeStatsRepository
	probeResults           AgentProbeResultRepository
	leases                 LeaseRepository
	audits                 AuditRepository
	idempotency            IdempotencyRepository
	dashboard              DashboardRepository
	authorizationRevisions AuthorizationRevisionRepository
	clientInstances        ClientInstanceRepository
	clientConnections      ClientConnectionRepository
	metrics                *observability.Metrics
}

// AccountRepositories are transaction-bound repositories used for account
// lifecycle changes and their audit record.
type AccountRepositories struct {
	Users  AccountUserRepository
	Audits AuditRepository
}

func (d *DB) AccountTransaction(ctx context.Context, fn func(AccountRepositories) error) error {
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `UPDATE schema_meta SET version=version WHERE id=1`); err != nil {
		return err
	}
	repos := AccountRepositories{
		Users:  &userRepo{db: tx, driver: d.driver, lockReads: true},
		Audits: &auditRepo{db: tx},
	}
	if err := fn(repos); err != nil {
		return err
	}
	return tx.Commit()
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
	started := time.Now()
	db, err := Open(ctx, driver, dsn, autoInit)
	if err != nil {
		if metrics != nil {
			class := observability.NormalizeErrorClass(err)
			metrics.ObserveProbe("storage", "failure", class, time.Since(started))
		}
		return nil, err
	}
	db.metrics = metrics
	if metrics != nil {
		metrics.ObserveProbe("storage", "success", "", time.Since(started))
	}
	return db, nil
}

// OpenConfig consumes the already-validated storage settings produced by
// internal/config and keeps driver-specific DSN selection inside storage.
func OpenConfig(ctx context.Context, cfg config.StorageConfig) (*DB, error) {
	dsn := cfg.SQLite.Path
	if cfg.Driver == DriverMySQL {
		var err error
		dsn, err = normalizeMySQLDSNTLS(cfg.MySQL.DSN, cfg.MySQL.TLS)
		if err != nil {
			return nil, fmt.Errorf("normalize mysql DSN: %w", err)
		}
	}
	return Open(ctx, cfg.Driver, dsn, cfg.AutoInit)
}

// normalizeMySQLDSNTLS makes the typed configuration authoritative over a
// stale tls query parameter in an injected DSN. This is especially important
// for MySQL installations that do not support TLS: leaving tls=true in the
// DSN makes the driver negotiate TLS even when storage.mysql.tls is false.
func normalizeMySQLDSNTLS(dsn string, enabled bool) (string, error) {
	dsn = strings.TrimSpace(dsn)
	if dsn == "" {
		return dsn, nil
	}
	base, rawQuery := dsn, ""
	if i := strings.IndexByte(dsn, '?'); i >= 0 {
		base, rawQuery = dsn[:i], dsn[i+1:]
	}
	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		return "", err
	}
	if enabled {
		// Preserve an explicitly registered custom TLS profile. If none is
		// present (or the DSN explicitly disabled TLS), request the driver's
		// built-in TLS configuration.
		profile, ok := values["tls"]
		if !ok || len(profile) == 0 || strings.EqualFold(profile[0], "false") || profile[0] == "0" {
			values.Set("tls", "true")
		}
	} else {
		values.Set("tls", "false")
	}
	return base + "?" + values.Encode(), nil
}

func newDB(db *sql.DB, driver string) *DB {
	return &DB{
		sql:                    db,
		driver:                 driver,
		users:                  &userRepo{db: db, driver: driver},
		tokens:                 &tokenRepo{db: db},
		serviceTokens:          NewServiceTokenRepositoryWithDriver(db, driver),
		agents:                 &agentRepo{db: db, driver: driver},
		policies:               &policyRepo{db},
		tunnels:                &tunnelRepo{db},
		nodes:                  &nodeRepo{db: db, driver: driver},
		metadata:               NewAgentMetadataRepositoryWithDriver(db, driver),
		runtimeStats:           NewAgentRuntimeStatsRepository(db),
		probeResults:           NewAgentProbeResultRepository(db),
		leases:                 NewLeaseRepositoryWithDriver(db, driver),
		audits:                 &auditRepo{db},
		idempotency:            &idempotencyRepo{db},
		dashboard:              &dashboardRepo{db: db},
		authorizationRevisions: &authorizationRevisionRepo{db: db},
		clientInstances:        NewClientInstanceRepositoryWithDriver(db, driver),
		clientConnections:      NewClientConnectionRepositoryWithDriver(db, driver),
	}
}

func initializeSchema(ctx context.Context, db *sql.DB, driver string) error {
	exists, err := schemaMetaExists(ctx, db, driver)
	if err != nil {
		return err
	}
	if !exists {
		if err := applySchemaStatements(ctx, db, migrations.DDL, true); err != nil {
			return fmt.Errorf("apply schema: %w", err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO schema_meta(id,version) VALUES(1,?)`, SchemaVersion); err != nil {
			return fmt.Errorf("write schema version: %w", err)
		}
		return requireSchemaTables(ctx, db, driver)
	}
	var version int
	err = db.QueryRowContext(ctx, `SELECT version FROM schema_meta WHERE id=1`).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		if err := applySchemaStatements(ctx, db, migrations.DDL, true); err != nil {
			return fmt.Errorf("apply schema: %w", err)
		}
		if _, err = db.ExecContext(ctx, `INSERT INTO schema_meta(id,version) VALUES(1,?)`, SchemaVersion); err != nil {
			return fmt.Errorf("write schema version: %w", err)
		}
		return requireSchemaTables(ctx, db, driver)
	}
	if err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if version > SchemaVersion || version < 1 {
		return fmt.Errorf("%w: database has version %d, application requires version %d; run the migration tool", ErrSchemaVersionMismatch, version, SchemaVersion)
	}
	for version < SchemaVersion {
		var script string
		switch version {
		case 5:
			script = migrations.V5ToV6SQLite
			if driver == DriverMySQL {
				script = migrations.V5ToV6MySQL
			}
		case 6:
			script = migrations.V6ToV7SQLite
			if driver == DriverMySQL {
				script = migrations.V6ToV7MySQL
			}
		case 7:
			script = migrations.V7ToV8SQLite
			if driver == DriverMySQL {
				script = migrations.V7ToV8MySQL
			}
		case 8:
			script = migrations.V8ToV9SQLite
			if driver == DriverMySQL {
				script = migrations.V8ToV9MySQL
			}
		case 9:
			script = migrations.V9ToV10SQLite
			if driver == DriverMySQL {
				script = migrations.V9ToV10MySQL
			}
		case 10:
			script = migrations.V10ToV11SQLite
			if driver == DriverMySQL {
				script = migrations.V10ToV11MySQL
			}
		default:
			return fmt.Errorf("%w: database has version %d, application requires version %d; missing adjacent migration v%04d_to_v%04d", ErrSchemaVersionMismatch, version, SchemaVersion, version, version+1)
		}
		// MySQL DDL commits implicitly. From v6 onward, tolerating an
		// already-created object makes a partially applied adjacent migration
		// safe to retry. Older migrations must still fail on duplicate objects
		// because a duplicate there usually means the schema is inconsistent.
		if err := applySchemaStatements(ctx, db, script, version >= 6); err != nil {
			return fmt.Errorf("apply migration v%04d_to_v%04d: %w", version, version+1, err)
		}
		version++
		if _, err := db.ExecContext(ctx, `UPDATE schema_meta SET version=? WHERE id=1`, version); err != nil {
			return fmt.Errorf("migrate schema version: %w", err)
		}
	}
	if err := requireSchemaTables(ctx, db, driver); err != nil {
		return err
	}
	return nil
}

func schemaMetaExists(ctx context.Context, db *sql.DB, driver string) (bool, error) {
	var name string
	var err error
	if driver == DriverMySQL {
		err = db.QueryRowContext(ctx, `SELECT table_name FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name='schema_meta'`).Scan(&name)
	} else {
		err = db.QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name='schema_meta'`).Scan(&name)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check schema metadata: %w", err)
	}
	return true, nil
}

func applySchemaStatements(ctx context.Context, db *sql.DB, script string, tolerateDuplicates bool) error {
	for _, statement := range strings.Split(script, ";") {
		statement = strings.TrimSpace(statement)
		if statement == "" {
			continue
		}
		if _, err := db.ExecContext(ctx, statement); err != nil {
			if tolerateDuplicates && isDuplicateError(err) {
				continue
			}
			return err
		}
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
	for _, table := range []string{"schema_meta", "authorization_revision", "users", "agents", "agent_instance_metadata", "service_tokens", "agent_runtime_stats", "agent_probe_results", "agent_connection_leases", "client_instance_metadata", "client_connection_leases"} {
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
	return requireAccountSchema(ctx, db, driver)
}

func requireAccountSchema(ctx context.Context, db *sql.DB, driver string) error {
	var column string
	var rows *sql.Rows
	var err error
	if driver == DriverMySQL {
		err = db.QueryRowContext(ctx, `SELECT column_name FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='users' AND column_name='deleted_at'`).Scan(&column)
		if err == nil {
			rows, err = db.QueryContext(ctx, `SELECT column_name FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name='users' AND index_name='idx_users_role_deleted' ORDER BY seq_in_index`)
		}
	} else {
		err = db.QueryRowContext(ctx, `SELECT name FROM pragma_table_info('users') WHERE name='deleted_at'`).Scan(&column)
		if err == nil {
			rows, err = db.QueryContext(ctx, `SELECT name FROM pragma_index_info('idx_users_role_deleted') ORDER BY seqno`)
		}
	}
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("schema is missing required users.deleted_at column")
	}
	if err != nil {
		return fmt.Errorf("check users.deleted_at column: %w", err)
	}
	defer rows.Close()
	want := []string{"role", "deleted_at", "id"}
	got := make([]string, 0, len(want))
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return fmt.Errorf("check idx_users_role_deleted: %w", err)
		}
		got = append(got, name)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("check idx_users_role_deleted: %w", err)
	}
	if len(got) != len(want) {
		return fmt.Errorf("schema index idx_users_role_deleted has columns %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			return fmt.Errorf("schema index idx_users_role_deleted has columns %v, want %v", got, want)
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
func (d *DB) Users() UserRepository                     { return d.users }
func (d *DB) Tokens() TokenRepository                   { return d.tokens }
func (d *DB) ServiceTokens() ServiceTokenRepository     { return d.serviceTokens }
func (d *DB) Agents() AgentRepository                   { return d.agents }
func (d *DB) Policies() PolicyRepository                { return d.policies }
func (d *DB) Tunnels() TunnelRepository                 { return d.tunnels }
func (d *DB) Nodes() NodeRepository                     { return d.nodes }
func (d *DB) Metadata() AgentMetadataRepository         { return d.metadata }
func (d *DB) AgentMetadata() AgentMetadataRepository    { return d.metadata }
func (d *DB) RuntimeStats() AgentRuntimeStatsRepository { return d.runtimeStats }
func (d *DB) ProbeResults() AgentProbeResultRepository  { return d.probeResults }
func (d *DB) Leases() LeaseRepository                   { return d.leases }
func (d *DB) Audits() AuditRepository                   { return d.audits }
func (d *DB) Idempotency() IdempotencyRepository        { return d.idempotency }
func (d *DB) Dashboard() DashboardRepository            { return d.dashboard }
func (d *DB) ClientInstances() ClientInstanceRepository { return d.clientInstances }
func (d *DB) ClientConnections() ClientConnectionRepository {
	return d.clientConnections
}
func (d *DB) AuthorizationRevisions() AuthorizationRevisionRepository {
	return d.authorizationRevisions
}
