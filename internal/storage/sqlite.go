package storage

import "context"

// OpenSQLite opens a SQLite database and applies the shared schema.
func OpenSQLite(ctx context.Context, dsn string, autoInit ...bool) (*DB, error) {
	initSchema := true
	if len(autoInit) > 0 {
		initSchema = autoInit[0]
	}
	return Open(ctx, DriverSQLite, dsn, initSchema)
}

// OpenSQLiteWithOptions allows callers such as migrations checks to disable
// automatic schema creation explicitly.
func OpenSQLiteWithOptions(ctx context.Context, dsn string, autoInit bool) (*DB, error) {
	return Open(ctx, DriverSQLite, dsn, autoInit)
}
