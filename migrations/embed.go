// Package migrations exposes the single portable schema used by every storage driver.
package migrations

import _ "embed"

// DDL is intentionally embedded once here because go:embed cannot traverse a
// parent directory from internal/storage.
//
//go:embed ddl.sql
var DDL string

//go:embed incremental/v0005_to_v0006/mysql.sql
var V5ToV6MySQL string

//go:embed incremental/v0005_to_v0006/sqlite.sql
var V5ToV6SQLite string

//go:embed incremental/v0006_to_v0007/mysql.sql
var V6ToV7MySQL string

//go:embed incremental/v0006_to_v0007/sqlite.sql
var V6ToV7SQLite string
