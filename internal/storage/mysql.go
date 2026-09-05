package storage

import "context"

// OpenMySQL opens a MySQL database and applies the shared schema.
func OpenMySQL(ctx context.Context, dsn string, autoInit ...bool) (*DB, error) {
	initSchema := true
	if len(autoInit) > 0 {
		initSchema = autoInit[0]
	}
	return Open(ctx, DriverMySQL, dsn, initSchema)
}

// OpenMySQLWithOptions allows deployments to require a pre-created schema.
func OpenMySQLWithOptions(ctx context.Context, dsn string, autoInit bool) (*DB, error) {
	return Open(ctx, DriverMySQL, dsn, autoInit)
}
